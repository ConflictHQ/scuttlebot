package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/conflicthq/scuttlebot/pkg/ircagent"
	"github.com/creack/pty"
	"golang.org/x/term"
)

const (
	defaultRelayURL      = "http://localhost:8080"
	defaultChannel       = "general"
	defaultPollInterval  = 2 * time.Second
	defaultInjectDelay   = 150 * time.Millisecond
	defaultBusyWindow    = 1500 * time.Millisecond
	defaultRequestTimout = 3 * time.Second
	defaultConfigFile    = ".config/scuttlebot-relay.env"
	defaultScanInterval  = 250 * time.Millisecond
	defaultDiscoverWait  = 20 * time.Second
	defaultMirrorLineMax = 360
)

var serviceBots = map[string]struct{}{
	"bridge":    {},
	"oracle":    {},
	"sentinel":  {},
	"steward":   {},
	"scribe":    {},
	"warden":    {},
	"snitch":    {},
	"herald":    {},
	"scroll":    {},
	"systembot": {},
	"auditbot":  {},
}

var (
	secretHexPattern   = regexp.MustCompile(`\b[a-f0-9]{32,}\b`)
	secretKeyPattern   = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+\b`)
	bearerPattern      = regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._:-]+)`)
	assignTokenPattern = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(TOKEN|KEY|SECRET|PASSPHRASE)[A-Z0-9_]*=)([^ \t"'` + "`" + `]+)`)
)

type config struct {
	CodexBin           string
	ConfigFile         string
	URL                string
	Token              string
	Channel            string
	SessionID          string
	Nick               string
	HooksEnabled       bool
	InterruptOnMessage bool
	PollInterval       time.Duration
	TargetCWD          string
	Args               []string
}

type relayClient struct {
	http  *http.Client
	url   string
	token string
}

type message struct {
	At   string `json:"at"`
	Nick string `json:"nick"`
	Text string `json:"text"`
	Time time.Time
}

type relayState struct {
	mu       sync.RWMutex
	lastBusy time.Time
}

type sessionEnvelope struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

type sessionResponsePayload struct {
	Type      string           `json:"type"`
	Name      string           `json:"name"`
	Arguments string           `json:"arguments"`
	Input     string           `json:"input"`
	Role      string           `json:"role"`
	Phase     string           `json:"phase"`
	Content   []sessionContent `json:"content"`
}

type sessionContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type execCommandArgs struct {
	Cmd string `json:"cmd"`
}

type parallelArgs struct {
	ToolUses []struct {
		RecipientName string                 `json:"recipient_name"`
		Parameters    map[string]interface{} `json:"parameters"`
	} `json:"tool_uses"`
}

func main() {
	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex-relay:", err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "codex-relay:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	fmt.Fprintf(os.Stderr, "codex-relay: nick %s\n", cfg.Nick)
	relayActive := cfg.HooksEnabled && shouldRelaySession(cfg.Args)

	client := relayClient{
		http:  &http.Client{Timeout: defaultRequestTimout},
		url:   strings.TrimRight(cfg.URL, "/"),
		token: cfg.Token,
	}

	if relayActive {
		_ = client.postStatus(cfg.Channel, cfg.Nick, fmt.Sprintf(
			"online in %s; mention %s to interrupt before the next action",
			filepath.Base(cfg.TargetCWD), cfg.Nick,
		))
	}

	cmd := exec.Command(cfg.CodexBin, cfg.Args...)
	startedAt := time.Now()
	cmd.Env = append(os.Environ(),
		"SCUTTLEBOT_CONFIG_FILE="+cfg.ConfigFile,
		"SCUTTLEBOT_URL="+cfg.URL,
		"SCUTTLEBOT_TOKEN="+cfg.Token,
		"SCUTTLEBOT_CHANNEL="+cfg.Channel,
		"SCUTTLEBOT_HOOKS_ENABLED="+boolString(cfg.HooksEnabled),
		"SCUTTLEBOT_SESSION_ID="+cfg.SessionID,
		"SCUTTLEBOT_NICK="+cfg.Nick,
		"SCUTTLEBOT_ACTIVITY_VIA_BROKER=1",
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if relayActive {
		go mirrorSessionLoop(ctx, client, cfg, startedAt)
	}

	if !isInteractiveTTY() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			exitCode := exitStatus(err)
			if relayActive {
				_ = client.postStatus(cfg.Channel, cfg.Nick, fmt.Sprintf("offline (exit %d)", exitCode))
			}
			return err
		}
		if relayActive {
			_ = client.postStatus(cfg.Channel, cfg.Nick, "offline (exit 0)")
		}
		return nil
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = ptmx.Close() }()

	state := &relayState{}

	if err := pty.InheritSize(os.Stdin, ptmx); err == nil {
		resizeCh := make(chan os.Signal, 1)
		signal.Notify(resizeCh, syscall.SIGWINCH)
		defer signal.Stop(resizeCh)
		go func() {
			for range resizeCh {
				_ = pty.InheritSize(os.Stdin, ptmx)
			}
		}()
		resizeCh <- syscall.SIGWINCH
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	go func() {
		_, _ = io.Copy(ptmx, os.Stdin)
	}()
	go func() {
		copyPTYOutput(ptmx, os.Stdout, state)
	}()
	if relayActive {
		go relayInputLoop(ctx, client, cfg, state, ptmx)
	}

	err = cmd.Wait()
	cancel()

	exitCode := exitStatus(err)
	if relayActive {
		_ = client.postStatus(cfg.Channel, cfg.Nick, fmt.Sprintf("offline (exit %d)", exitCode))
	}
	return err
}

func relayInputLoop(ctx context.Context, client relayClient, cfg config, state *relayState, ptyFile *os.File) {
	lastSeen := time.Now()
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			messages, err := client.fetchMessages(cfg.Channel)
			if err != nil {
				continue
			}
			batch, newest := filterMessages(messages, lastSeen, cfg.Nick)
			if len(batch) == 0 {
				continue
			}
			lastSeen = newest
			if err := injectMessages(ptyFile, cfg, state, batch); err != nil {
				return
			}
		}
	}
}

func injectMessages(writer io.Writer, cfg config, state *relayState, batch []message) error {
	lines := make([]string, 0, len(batch))
	for _, msg := range batch {
		text := ircagent.TrimAddressedText(strings.TrimSpace(msg.Text), cfg.Nick)
		if text == "" {
			text = strings.TrimSpace(msg.Text)
		}
		lines = append(lines, fmt.Sprintf("%s: %s", msg.Nick, text))
	}

	var block strings.Builder
	block.WriteString("[IRC operator messages]\n")
	for _, line := range lines {
		block.WriteString(line)
		block.WriteByte('\n')
	}

	notice := "\r\n" + block.String() + "\r\n"
	_, _ = os.Stdout.WriteString(notice)

	if cfg.InterruptOnMessage && state.shouldInterrupt(time.Now()) {
		if _, err := writer.Write([]byte{3}); err != nil {
			return err
		}
		time.Sleep(defaultInjectDelay)
	}

	if _, err := writer.Write([]byte(block.String())); err != nil {
		return err
	}
	_, err := writer.Write([]byte{'\r'})
	return err
}

func copyPTYOutput(src io.Reader, dst io.Writer, state *relayState) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			state.observeOutput(buf[:n], time.Now())
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *relayState) observeOutput(data []byte, now time.Time) {
	if s == nil {
		return
	}
	if strings.Contains(strings.ToLower(string(data)), "esc to interrupt") {
		s.mu.Lock()
		s.lastBusy = now
		s.mu.Unlock()
	}
}

func (s *relayState) shouldInterrupt(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	lastBusy := s.lastBusy
	s.mu.RUnlock()
	return !lastBusy.IsZero() && now.Sub(lastBusy) <= defaultBusyWindow
}

func filterMessages(messages []message, since time.Time, nick string) ([]message, time.Time) {
	filtered := make([]message, 0, len(messages))
	newest := since
	for _, msg := range messages {
		if msg.Time.IsZero() || !msg.Time.After(since) {
			continue
		}
		if msg.Time.After(newest) {
			newest = msg.Time
		}
		if msg.Nick == nick {
			continue
		}
		if _, ok := serviceBots[msg.Nick]; ok {
			continue
		}
		if ircagent.HasAnyPrefix(msg.Nick, ircagent.DefaultActivityPrefixes()) {
			continue
		}
		if !ircagent.MentionsNick(msg.Text, nick) {
			continue
		}
		filtered = append(filtered, msg)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Time.Before(filtered[j].Time)
	})
	return filtered, newest
}

func loadConfig(args []string) (config, error) {
	fileConfig := readEnvFile(configFilePath())

	cfg := config{
		CodexBin:           getenvOr(fileConfig, "CODEX_BIN", "codex"),
		ConfigFile:         getenvOr(fileConfig, "SCUTTLEBOT_CONFIG_FILE", configFilePath()),
		URL:                getenvOr(fileConfig, "SCUTTLEBOT_URL", defaultRelayURL),
		Token:              getenvOr(fileConfig, "SCUTTLEBOT_TOKEN", ""),
		Channel:            strings.TrimPrefix(getenvOr(fileConfig, "SCUTTLEBOT_CHANNEL", defaultChannel), "#"),
		HooksEnabled:       getenvBoolOr(fileConfig, "SCUTTLEBOT_HOOKS_ENABLED", true),
		InterruptOnMessage: getenvBoolOr(fileConfig, "SCUTTLEBOT_INTERRUPT_ON_MESSAGE", true),
		PollInterval:       getenvDurationOr(fileConfig, "SCUTTLEBOT_POLL_INTERVAL", defaultPollInterval),
		Args:               append([]string(nil), args...),
	}

	target, err := targetCWD(args)
	if err != nil {
		return config{}, err
	}
	cfg.TargetCWD = target

	sessionID := getenvOr(fileConfig, "SCUTTLEBOT_SESSION_ID", "")
	if sessionID == "" {
		sessionID = getenvOr(fileConfig, "CODEX_SESSION_ID", "")
	}
	if sessionID == "" {
		sessionID = defaultSessionID(target)
	}
	cfg.SessionID = sanitize(sessionID)

	nick := getenvOr(fileConfig, "SCUTTLEBOT_NICK", "")
	if nick == "" {
		nick = fmt.Sprintf("codex-%s-%s", sanitize(filepath.Base(target)), cfg.SessionID)
	}
	cfg.Nick = sanitize(nick)

	if cfg.Channel == "" {
		cfg.Channel = defaultChannel
	}
	if cfg.Token == "" {
		cfg.HooksEnabled = false
	}
	return cfg, nil
}

func configFilePath() string {
	if value := os.Getenv("SCUTTLEBOT_CONFIG_FILE"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultConfigFile
	}
	return filepath.Join(home, defaultConfigFile)
}

func readEnvFile(path string) map[string]string {
	values := make(map[string]string)
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(strings.Trim(value, `"'`))
	}
	return values
}

func getenvOr(file map[string]string, key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	if value := file[key]; value != "" {
		return value
	}
	return fallback
}

func getenvBoolOr(file map[string]string, key string, fallback bool) bool {
	value := getenvOr(file, key, "")
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func getenvDurationOr(file map[string]string, key string, fallback time.Duration) time.Duration {
	value := getenvOr(file, key, "")
	if value == "" {
		return fallback
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		value += "s"
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func targetCWD(args []string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	target := cwd
	var prev string
	for _, arg := range args {
		switch {
		case prev == "-C" || prev == "--cd":
			target = arg
			prev = ""
			continue
		case arg == "-C" || arg == "--cd":
			prev = arg
			continue
		case strings.HasPrefix(arg, "-C="):
			target = strings.TrimPrefix(arg, "-C=")
		case strings.HasPrefix(arg, "--cd="):
			target = strings.TrimPrefix(arg, "--cd=")
		}
	}
	if filepath.IsAbs(target) {
		return target, nil
	}
	return filepath.Abs(target)
}

func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "session"
	}
	return result
}

func defaultSessionID(target string) string {
	sum := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s|%d|%d|%d", target, os.Getpid(), os.Getppid(), time.Now().UnixNano())))
	return fmt.Sprintf("%08x", sum)
}

func (c relayClient) postStatus(channel, nick, text string) error {
	if c.token == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"nick": nick, "text": text})
	req, err := http.NewRequest(http.MethodPost, c.url+"/v1/channels/"+channel+"/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("status post: %s", resp.Status)
	}
	return nil
}

func (c relayClient) fetchMessages(channel string) ([]message, error) {
	req, err := http.NewRequest(http.MethodGet, c.url+"/v1/channels/"+channel+"/messages", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("message fetch: %s", resp.Status)
	}
	var payload struct {
		Messages []message `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	for i := range payload.Messages {
		ts, err := time.Parse(time.RFC3339Nano, payload.Messages[i].At)
		if err == nil {
			payload.Messages[i].Time = ts
		}
	}
	return payload.Messages, nil
}

func mirrorSessionLoop(ctx context.Context, client relayClient, cfg config, startedAt time.Time) {
	sessionPath, err := discoverSessionPath(ctx, cfg, startedAt)
	if err != nil {
		return
	}
	_ = tailSessionFile(ctx, sessionPath, func(text string) {
		for _, line := range splitMirrorText(text) {
			if line == "" {
				continue
			}
			_ = client.postStatus(cfg.Channel, cfg.Nick, line)
		}
	})
}

func discoverSessionPath(ctx context.Context, cfg config, startedAt time.Time) (string, error) {
	root, err := codexSessionsRoot()
	if err != nil {
		return "", err
	}

	if threadID := explicitThreadID(cfg.Args); threadID != "" {
		return waitForSessionPath(ctx, func() (string, error) {
			return findSessionPathByThreadID(root, threadID)
		})
	}

	target := filepath.Clean(cfg.TargetCWD)
	return waitForSessionPath(ctx, func() (string, error) {
		return findLatestSessionPath(root, target, startedAt.Add(-2*time.Second))
	})
}

func waitForSessionPath(ctx context.Context, find func() (string, error)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultDiscoverWait)
	defer cancel()

	ticker := time.NewTicker(defaultScanInterval)
	defer ticker.Stop()

	for {
		path, err := find()
		if err == nil && path != "" {
			return path, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func tailSessionFile(ctx context.Context, path string, emit func(string)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			for _, text := range sessionMessages(line) {
				if text != "" {
					emit(text)
				}
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(defaultScanInterval):
			}
			continue
		}
		return err
	}
}

func sessionMessages(line []byte) []string {
	var env sessionEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil
	}
	if env.Type != "response_item" {
		return nil
	}

	var payload sessionResponsePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return nil
	}

	switch payload.Type {
	case "function_call":
		if msg := summarizeFunctionCall(payload.Name, payload.Arguments); msg != "" {
			return []string{msg}
		}
	case "custom_tool_call":
		if msg := summarizeCustomToolCall(payload.Name, payload.Input); msg != "" {
			return []string{msg}
		}
	case "message":
		if payload.Role != "assistant" {
			return nil
		}
		return flattenAssistantContent(payload.Content)
	}
	return nil
}

func summarizeFunctionCall(name, argsJSON string) string {
	switch name {
	case "exec_command":
		var args execCommandArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err == nil && strings.TrimSpace(args.Cmd) != "" {
			return "› " + sanitizeSecrets(compactCommand(args.Cmd))
		}
		return "› command"
	case "parallel":
		var args parallelArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err == nil && len(args.ToolUses) > 0 {
			return fmt.Sprintf("parallel %d tools", len(args.ToolUses))
		}
		return "parallel"
	case "update_plan":
		return "plan updated"
	case "spawn_agent":
		return "spawn agent"
	default:
		if name == "" {
			return ""
		}
		return name
	}
}

func summarizeCustomToolCall(name, input string) string {
	switch name {
	case "apply_patch":
		files := patchTargets(input)
		if len(files) == 0 {
			return "patch"
		}
		if len(files) == 1 {
			return "patch " + files[0]
		}
		return fmt.Sprintf("patch %d files: %s", len(files), strings.Join(files, ", "))
	default:
		if name == "" {
			return ""
		}
		return name
	}
}

func flattenAssistantContent(content []sessionContent) []string {
	var lines []string
	for _, item := range content {
		if item.Type != "output_text" {
			continue
		}
		for _, line := range splitMirrorText(item.Text) {
			if line != "" {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

func compactCommand(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	trimmed = strings.Join(strings.Fields(trimmed), " ")
	if strings.HasPrefix(trimmed, "cd ") {
		if idx := strings.Index(trimmed, " && "); idx > 0 {
			trimmed = strings.TrimSpace(trimmed[idx+4:])
		}
	}
	if len(trimmed) > 140 {
		return trimmed[:140] + "..."
	}
	return trimmed
}

func sanitizeSecrets(text string) string {
	if text == "" {
		return ""
	}
	text = bearerPattern.ReplaceAllString(text, "${1}[redacted]")
	text = assignTokenPattern.ReplaceAllString(text, "${1}[redacted]")
	text = secretKeyPattern.ReplaceAllString(text, "[redacted]")
	text = secretHexPattern.ReplaceAllString(text, "[redacted]")
	return text
}

func splitMirrorText(text string) []string {
	clean := strings.ReplaceAll(text, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")
	raw := strings.Split(clean, "\n")
	var out []string
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for len(line) > defaultMirrorLineMax {
			cut := strings.LastIndex(line[:defaultMirrorLineMax], " ")
			if cut <= 0 {
				cut = defaultMirrorLineMax
			}
			out = append(out, line[:cut])
			line = strings.TrimSpace(line[cut:])
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func patchTargets(input string) []string {
	var files []string
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: "} {
			if strings.HasPrefix(line, prefix) {
				files = append(files, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
				break
			}
		}
	}
	return files
}

func explicitThreadID(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "resume" {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

func codexSessionsRoot() (string, error) {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return filepath.Join(value, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

func findSessionPathByThreadID(root, threadID string) (string, error) {
	var match string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if strings.Contains(path, threadID) {
			match = path
			return io.EOF
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if match == "" {
		return "", os.ErrNotExist
	}
	return match, nil
}

func findLatestSessionPath(root, target string, notBefore time.Time) (string, error) {
	var (
		bestPath string
		bestTime time.Time
	)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		meta, ts, err := readSessionMeta(path)
		if err != nil {
			return nil
		}
		if filepath.Clean(meta.Cwd) != target {
			return nil
		}
		if ts.Before(notBefore) {
			return nil
		}
		if bestPath == "" || ts.After(bestTime) {
			bestPath = path
			bestTime = ts
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if bestPath == "" {
		return "", os.ErrNotExist
	}
	return bestPath, nil
}

func readSessionMeta(path string) (sessionMetaPayload, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionMetaPayload{}, time.Time{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return sessionMetaPayload{}, time.Time{}, err
		}
		return sessionMetaPayload{}, time.Time{}, io.EOF
	}

	var env sessionEnvelope
	if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
		return sessionMetaPayload{}, time.Time{}, err
	}
	if env.Type != "session_meta" {
		return sessionMetaPayload{}, time.Time{}, io.EOF
	}

	var meta sessionMetaPayload
	if err := json.Unmarshal(env.Payload, &meta); err != nil {
		return sessionMetaPayload{}, time.Time{}, err
	}

	if ts, err := time.Parse(time.RFC3339Nano, meta.Timestamp); err == nil {
		return meta, ts, nil
	}
	info, err := file.Stat()
	if err != nil {
		return meta, time.Time{}, nil
	}
	return meta, info.ModTime(), nil
}

func isInteractiveTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func shouldRelaySession(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "-V", "--version":
			return false
		}
	}

	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "help", "completion":
			return false
		default:
			return true
		}
	}

	return true
}

func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}
