package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
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
	"github.com/conflicthq/scuttlebot/pkg/sessionrelay"
	"github.com/creack/pty"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

const (
	defaultRelayURL      = "http://localhost:8080"
	defaultIRCAddr       = "127.0.0.1:6667"
	defaultChannel       = "general"
	defaultTransport     = sessionrelay.TransportHTTP
	defaultPollInterval  = 2 * time.Second
	defaultConnectWait   = 10 * time.Second
	defaultInjectDelay   = 150 * time.Millisecond
	defaultBusyWindow    = 1500 * time.Millisecond
	defaultHeartbeat     = 60 * time.Second
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
	Transport          sessionrelay.Transport
	URL                string
	Token              string
	IRCAddr            string
	IRCPass            string
	IRCAgentType       string
	IRCDeleteOnClose   bool
	Channel            string
	Channels           []string
	ChannelStateFile   string
	SessionID          string
	Nick               string
	HooksEnabled       bool
	InterruptOnMessage bool
	MirrorReasoning    bool
	PollInterval       time.Duration
	HeartbeatInterval  time.Duration
	TargetCWD          string
	Args               []string
}

type message = sessionrelay.Message

// mirrorLine is a single line of relay output with optional structured metadata.
type mirrorLine struct {
	Text string
	Meta json.RawMessage
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
	relayRequested := cfg.HooksEnabled && shouldRelaySession(cfg.Args)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = sessionrelay.RemoveChannelStateFile(cfg.ChannelStateFile)
	defer func() { _ = sessionrelay.RemoveChannelStateFile(cfg.ChannelStateFile) }()

	var relay sessionrelay.Connector
	relayActive := false
	var onlineAt time.Time
	if relayRequested {
		conn, err := sessionrelay.New(sessionrelay.Config{
			Transport: cfg.Transport,
			URL:       cfg.URL,
			Token:     cfg.Token,
			Channel:   cfg.Channel,
			Channels:  cfg.Channels,
			Nick:      cfg.Nick,
			IRC: sessionrelay.IRCConfig{
				Addr:          cfg.IRCAddr,
				Pass:          cfg.IRCPass,
				AgentType:     cfg.IRCAgentType,
				DeleteOnClose: cfg.IRCDeleteOnClose,
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "codex-relay: relay disabled: %v\n", err)
		} else {
			connectCtx, connectCancel := context.WithTimeout(ctx, defaultConnectWait)
			if err := conn.Connect(connectCtx); err != nil {
				fmt.Fprintf(os.Stderr, "codex-relay: relay disabled: %v\n", err)
				_ = conn.Close(context.Background())
			} else {
				relay = conn
				relayActive = true
				if err := sessionrelay.WriteChannelStateFile(cfg.ChannelStateFile, relay.ControlChannel(), relay.Channels()); err != nil {
					fmt.Fprintf(os.Stderr, "codex-relay: channel state disabled: %v\n", err)
				}
				onlineAt = time.Now()
				_ = relay.Post(context.Background(), fmt.Sprintf(
					"online in %s; mention %s to interrupt before the next action",
					filepath.Base(cfg.TargetCWD), cfg.Nick,
				))
			}
			connectCancel()
		}
	}
	if relay != nil {
		defer func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), defaultConnectWait)
			defer closeCancel()
			_ = relay.Close(closeCtx)
		}()
	}

	cmd := exec.Command(cfg.CodexBin, cfg.Args...)
	startedAt := time.Now()
	cmd.Env = append(os.Environ(),
		"SCUTTLEBOT_CONFIG_FILE="+cfg.ConfigFile,
		"SCUTTLEBOT_URL="+cfg.URL,
		"SCUTTLEBOT_TOKEN="+cfg.Token,
		"SCUTTLEBOT_CHANNEL="+cfg.Channel,
		"SCUTTLEBOT_CHANNELS="+strings.Join(cfg.Channels, ","),
		"SCUTTLEBOT_CHANNEL_STATE_FILE="+cfg.ChannelStateFile,
		"SCUTTLEBOT_HOOKS_ENABLED="+boolString(cfg.HooksEnabled),
		"SCUTTLEBOT_SESSION_ID="+cfg.SessionID,
		"SCUTTLEBOT_NICK="+cfg.Nick,
		"SCUTTLEBOT_ACTIVITY_VIA_BROKER="+boolString(relayActive),
	)
	if relayActive {
		go mirrorSessionLoop(ctx, relay, cfg, startedAt)
		go presenceLoopPtr(ctx, &relay, cfg.HeartbeatInterval)
	}

	if !isInteractiveTTY() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			exitCode := exitStatus(err)
			if relayActive {
				_ = relay.Post(context.Background(), fmt.Sprintf("offline (exit %d)", exitCode))
			}
			return err
		}
		if relayActive {
			_ = relay.Post(context.Background(), "offline (exit 0)")
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
		go relayInputLoop(ctx, relay, cfg, state, ptmx, onlineAt)
		go handleReconnectSignal(ctx, &relay, cfg, state, ptmx, startedAt)
	}

	err = cmd.Wait()
	cancel()

	exitCode := exitStatus(err)
	if relayActive {
		_ = relay.Post(context.Background(), fmt.Sprintf("offline (exit %d)", exitCode))
	}
	return err
}

func relayInputLoop(ctx context.Context, relay sessionrelay.Connector, cfg config, state *relayState, ptyFile *os.File, since time.Time) {
	lastSeen := since
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			messages, err := relay.MessagesSince(ctx, lastSeen)
			if err != nil {
				continue
			}
			batch, newest := filterMessages(messages, lastSeen, cfg.Nick, cfg.IRCAgentType)
			if len(batch) == 0 {
				continue
			}
			lastSeen = newest
			pending := make([]message, 0, len(batch))
			for _, msg := range batch {
				handled, err := handleRelayCommand(ctx, relay, cfg, msg)
				if err != nil {
					if ctx.Err() == nil {
						_ = relay.Post(context.Background(), fmt.Sprintf("input loop error: %v — session may be unsteerable", err))
					}
					return
				}
				if handled {
					continue
				}
				pending = append(pending, msg)
			}
			if len(pending) == 0 {
				continue
			}
			if err := injectMessages(ptyFile, cfg, state, relay.ControlChannel(), pending); err != nil {
				if ctx.Err() == nil {
					_ = relay.Post(context.Background(), fmt.Sprintf("input loop error: %v — session may be unsteerable", err))
				}
				return
			}
		}
	}
}

func handleReconnectSignal(ctx context.Context, relayPtr *sessionrelay.Connector, cfg config, state *relayState, ptmx *os.File, startedAt time.Time) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
		}

		fmt.Fprintf(os.Stderr, "codex-relay: received SIGUSR1, reconnecting IRC...\n")
		old := *relayPtr
		if old != nil {
			_ = old.Close(context.Background())
		}

		// Retry with backoff.
		wait := 2 * time.Second
		for attempt := 0; attempt < 10; attempt++ {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(wait)

			conn, err := sessionrelay.New(sessionrelay.Config{
				Transport: cfg.Transport,
				URL:       cfg.URL,
				Token:     cfg.Token,
				Channel:   cfg.Channel,
				Channels:  cfg.Channels,
				Nick:      cfg.Nick,
				IRC: sessionrelay.IRCConfig{
					Addr:          cfg.IRCAddr,
					Pass:          "", // force re-registration
					AgentType:     cfg.IRCAgentType,
					DeleteOnClose: cfg.IRCDeleteOnClose,
				},
			})
			if err != nil {
				wait = min(wait*2, 30*time.Second)
				continue
			}

			connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := conn.Connect(connectCtx); err != nil {
				_ = conn.Close(context.Background())
				cancel()
				wait = min(wait*2, 30*time.Second)
				continue
			}
			cancel()

			*relayPtr = conn
			now := time.Now()
			_ = conn.Post(context.Background(), fmt.Sprintf(
				"reconnected in %s; mention %s to interrupt",
				filepath.Base(cfg.TargetCWD), cfg.Nick,
			))
			fmt.Fprintf(os.Stderr, "codex-relay: reconnected, restarting mirror and input loops\n")

			// Restart mirror and input loops with the new connector.
			// Use epoch time for mirror so it finds the existing session file
			// regardless of when it was last modified.
			go mirrorSessionLoop(ctx, conn, cfg, time.Time{})
			go relayInputLoop(ctx, conn, cfg, state, ptmx, now)
			break
		}
	}
}

func presenceLoopPtr(ctx context.Context, relayPtr *sessionrelay.Connector, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r := *relayPtr; r != nil {
				_ = r.Touch(ctx)
			}
		}
	}
}

func injectMessages(writer io.Writer, cfg config, state *relayState, controlChannel string, batch []message) error {
	lines := make([]string, 0, len(batch))
	for _, msg := range batch {
		text := ircagent.TrimAddressedText(strings.TrimSpace(msg.Text), cfg.Nick)
		if text == "" {
			text = strings.TrimSpace(msg.Text)
		}
		channelPrefix := ""
		if msg.Channel != "" {
			channelPrefix = "[" + strings.TrimPrefix(msg.Channel, "#") + "] "
		}
		if msg.Channel == "" || msg.Channel == controlChannel {
			channelPrefix = "[" + strings.TrimPrefix(controlChannel, "#") + "] "
		}
		lines = append(lines, fmt.Sprintf("%s%s: %s", channelPrefix, msg.Nick, text))
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

func handleRelayCommand(ctx context.Context, relay sessionrelay.Connector, cfg config, msg message) (bool, error) {
	text := ircagent.TrimAddressedText(strings.TrimSpace(msg.Text), cfg.Nick)
	if text == "" {
		text = strings.TrimSpace(msg.Text)
	}

	cmd, ok := sessionrelay.ParseBrokerCommand(text)
	if !ok {
		return false, nil
	}

	postStatus := func(channel, text string) error {
		if channel == "" {
			channel = relay.ControlChannel()
		}
		return relay.PostTo(ctx, channel, text)
	}

	switch cmd.Name {
	case "channels":
		return true, postStatus(msg.Channel, fmt.Sprintf("channels: %s (control %s)", sessionrelay.FormatChannels(relay.Channels()), relay.ControlChannel()))
	case "join":
		if cmd.Channel == "" {
			return true, postStatus(msg.Channel, "usage: /join #channel")
		}
		if err := relay.JoinChannel(ctx, cmd.Channel); err != nil {
			return true, postStatus(msg.Channel, fmt.Sprintf("join %s failed: %v", cmd.Channel, err))
		}
		if err := sessionrelay.WriteChannelStateFile(cfg.ChannelStateFile, relay.ControlChannel(), relay.Channels()); err != nil {
			return true, postStatus(msg.Channel, fmt.Sprintf("joined %s, but channel state update failed: %v", cmd.Channel, err))
		}
		return true, postStatus(msg.Channel, fmt.Sprintf("joined %s; channels: %s", cmd.Channel, sessionrelay.FormatChannels(relay.Channels())))
	case "part":
		if cmd.Channel == "" {
			return true, postStatus(msg.Channel, "usage: /part #channel")
		}
		if err := relay.PartChannel(ctx, cmd.Channel); err != nil {
			return true, postStatus(msg.Channel, fmt.Sprintf("part %s failed: %v", cmd.Channel, err))
		}
		if err := sessionrelay.WriteChannelStateFile(cfg.ChannelStateFile, relay.ControlChannel(), relay.Channels()); err != nil {
			return true, postStatus(msg.Channel, fmt.Sprintf("parted %s, but channel state update failed: %v", cmd.Channel, err))
		}
		replyChannel := msg.Channel
		if sameChannel(replyChannel, cmd.Channel) {
			replyChannel = relay.ControlChannel()
		}
		return true, postStatus(replyChannel, fmt.Sprintf("parted %s; channels: %s", cmd.Channel, sessionrelay.FormatChannels(relay.Channels())))
	default:
		return false, nil
	}
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

func filterMessages(messages []message, since time.Time, nick, agentType string) ([]message, time.Time) {
	filtered := make([]message, 0, len(messages))
	newest := since
	for _, msg := range messages {
		if msg.At.IsZero() || !msg.At.After(since) {
			continue
		}
		if msg.At.After(newest) {
			newest = msg.At
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
		if !ircagent.MentionsNick(msg.Text, nick) && !ircagent.MatchesGroupMention(msg.Text, nick, agentType) {
			continue
		}
		filtered = append(filtered, msg)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].At.Before(filtered[j].At)
	})
	return filtered, newest
}

func loadConfig(args []string) (config, error) {
	fileConfig := readEnvFile(configFilePath())

	cfg := config{
		CodexBin:           getenvOr(fileConfig, "CODEX_BIN", "codex"),
		ConfigFile:         getenvOr(fileConfig, "SCUTTLEBOT_CONFIG_FILE", configFilePath()),
		Transport:          sessionrelay.Transport(strings.ToLower(getenvOr(fileConfig, "SCUTTLEBOT_TRANSPORT", string(defaultTransport)))),
		URL:                getenvOr(fileConfig, "SCUTTLEBOT_URL", defaultRelayURL),
		Token:              getenvOr(fileConfig, "SCUTTLEBOT_TOKEN", ""),
		IRCAddr:            getenvOr(fileConfig, "SCUTTLEBOT_IRC_ADDR", defaultIRCAddr),
		IRCPass:            getenvOr(fileConfig, "SCUTTLEBOT_IRC_PASS", ""),
		IRCAgentType:       getenvOr(fileConfig, "SCUTTLEBOT_IRC_AGENT_TYPE", "worker"),
		IRCDeleteOnClose:   getenvBoolOr(fileConfig, "SCUTTLEBOT_IRC_DELETE_ON_CLOSE", true),
		HooksEnabled:       getenvBoolOr(fileConfig, "SCUTTLEBOT_HOOKS_ENABLED", true),
		InterruptOnMessage: getenvBoolOr(fileConfig, "SCUTTLEBOT_INTERRUPT_ON_MESSAGE", true),
		MirrorReasoning:    getenvBoolOr(fileConfig, "SCUTTLEBOT_MIRROR_REASONING", false),
		PollInterval:       getenvDurationOr(fileConfig, "SCUTTLEBOT_POLL_INTERVAL", defaultPollInterval),
		HeartbeatInterval:  getenvDurationAllowZeroOr(fileConfig, "SCUTTLEBOT_PRESENCE_HEARTBEAT", defaultHeartbeat),
		Args:               append([]string(nil), args...),
	}

	controlChannel := getenvOr(fileConfig, "SCUTTLEBOT_CHANNEL", defaultChannel)
	cfg.Channels = sessionrelay.ChannelSlugs(sessionrelay.ParseEnvChannels(controlChannel, getenvOr(fileConfig, "SCUTTLEBOT_CHANNELS", "")))
	if len(cfg.Channels) > 0 {
		cfg.Channel = cfg.Channels[0]
	}

	target, err := targetCWD(args)
	if err != nil {
		return config{}, err
	}
	cfg.TargetCWD = target

	// Merge per-repo config if present.
	if rc, err := loadRepoConfig(target); err == nil && rc != nil {
		cfg.Channels = mergeChannels(cfg.Channels, rc.allChannels())
	}

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
	cfg.ChannelStateFile = getenvOr(fileConfig, "SCUTTLEBOT_CHANNEL_STATE_FILE", defaultChannelStateFile(cfg.Nick))

	if cfg.Channel == "" {
		cfg.Channel = defaultChannel
		cfg.Channels = []string{defaultChannel}
	}
	if cfg.Transport == sessionrelay.TransportHTTP && cfg.Token == "" {
		cfg.HooksEnabled = false
	}
	return cfg, nil
}

func defaultChannelStateFile(nick string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf(".scuttlebot-channels-%s.env", sanitize(nick)))
}

func sameChannel(a, b string) bool {
	return strings.TrimPrefix(a, "#") == strings.TrimPrefix(b, "#")
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

func getenvDurationAllowZeroOr(file map[string]string, key string, fallback time.Duration) time.Duration {
	value := getenvOr(file, key, "")
	if value == "" {
		return fallback
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		value += "s"
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
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

func mirrorSessionLoop(ctx context.Context, relay sessionrelay.Connector, cfg config, startedAt time.Time) {
	for {
		if ctx.Err() != nil {
			return
		}
		sessionPath, err := discoverSessionPath(ctx, cfg, startedAt)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Second)
			continue
		}
		if err := tailSessionFile(ctx, sessionPath, cfg.MirrorReasoning, func(ml mirrorLine) {
			for _, line := range splitMirrorText(ml.Text) {
				if line == "" {
					continue
				}
				if len(ml.Meta) > 0 {
					_ = relay.PostWithMeta(ctx, line, ml.Meta)
				} else {
					_ = relay.Post(ctx, line)
				}
			}
		}); err != nil && ctx.Err() == nil {
			time.Sleep(5 * time.Second)
			continue
		}
		return
	}
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

func tailSessionFile(ctx context.Context, path string, mirrorReasoning bool, emit func(mirrorLine)) error {
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
			for _, ml := range sessionMessages(line, mirrorReasoning) {
				if ml.Text != "" {
					emit(ml)
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

func sessionMessages(line []byte, mirrorReasoning bool) []mirrorLine {
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
			meta := codexToolMeta(payload.Name, payload.Arguments)
			return []mirrorLine{{Text: msg, Meta: meta}}
		}
	case "custom_tool_call":
		if msg := summarizeCustomToolCall(payload.Name, payload.Input); msg != "" {
			meta := codexToolMeta(payload.Name, payload.Input)
			return []mirrorLine{{Text: msg, Meta: meta}}
		}
	case "message":
		if payload.Role != "assistant" {
			return nil
		}
		return flattenAssistantContent(payload.Content, mirrorReasoning)
	}
	return nil
}

// codexToolMeta builds a JSON metadata envelope for a Codex tool call.
func codexToolMeta(name, argsJSON string) json.RawMessage {
	data := map[string]string{"tool": name}
	switch name {
	case "exec_command":
		var args execCommandArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err == nil && args.Cmd != "" {
			data["command"] = sanitizeSecrets(args.Cmd)
		}
	case "apply_patch":
		files := patchTargets(argsJSON)
		if len(files) > 0 {
			data["file"] = files[0]
		}
	}
	meta := map[string]any{"type": "tool_result", "data": data}
	b, _ := json.Marshal(meta)
	return b
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

func flattenAssistantContent(content []sessionContent, mirrorReasoning bool) []mirrorLine {
	var lines []mirrorLine
	for _, item := range content {
		switch item.Type {
		case "output_text":
			for _, line := range splitMirrorText(item.Text) {
				if line != "" {
					lines = append(lines, mirrorLine{Text: line})
				}
			}
		case "reasoning":
			if mirrorReasoning {
				for _, line := range splitMirrorText(item.Text) {
					if line != "" {
						lines = append(lines, mirrorLine{Text: "💭 " + line})
					}
				}
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

// findLatestSessionPath finds the .jsonl file in root whose first entry
// timestamp is closest to (but after) notBefore — this ensures each relay
// latches onto its own subprocess's session rather than whichever session
// happens to have the latest timestamp when multiple sessions share a CWD.
func findLatestSessionPath(root, target string, notBefore time.Time) (string, error) {
	type candidate struct {
		path string
		ts   time.Time
	}
	var candidates []candidate

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
		candidates = append(candidates, candidate{path: path, ts: ts})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", os.ErrNotExist
	}
	// Sort newest first — the session that started most recently
	// (closest to our relay's startedAt) is most likely ours.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ts.After(candidates[j].ts)
	})
	return candidates[0].path, nil
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

// repoConfig is the per-repo .scuttlebot.yaml format.
type repoConfig struct {
	Channel  string   `yaml:"channel"`
	Channels []string `yaml:"channels"`
}

// allChannels returns the singular channel (if set) prepended to the channels list.
func (rc *repoConfig) allChannels() []string {
	if rc.Channel == "" {
		return rc.Channels
	}
	return append([]string{rc.Channel}, rc.Channels...)
}

// loadRepoConfig walks up from dir looking for .scuttlebot.yaml.
// Stops at the git root (directory containing .git) or the filesystem root.
// Returns nil, nil if no config file is found.
func loadRepoConfig(dir string) (*repoConfig, error) {
	current := dir
	for {
		candidate := filepath.Join(current, ".scuttlebot.yaml")
		if data, err := os.ReadFile(candidate); err == nil {
			var rc repoConfig
			if err := yaml.Unmarshal(data, &rc); err != nil {
				return nil, fmt.Errorf("loadRepoConfig: parse %s: %w", candidate, err)
			}
			fmt.Fprintf(os.Stderr, "scuttlebot: loaded repo config from %s\n", candidate)
			return &rc, nil
		}

		// Stop if this directory is a git root.
		if info, err := os.Stat(filepath.Join(current, ".git")); err == nil && info.IsDir() {
			return nil, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return nil, nil
		}
		current = parent
	}
}

// mergeChannels appends extra channels to existing, deduplicating.
func mergeChannels(existing, extra []string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, ch := range existing {
		seen[ch] = struct{}{}
	}
	merged := append([]string(nil), existing...)
	for _, ch := range extra {
		if _, ok := seen[ch]; ok {
			continue
		}
		seen[ch] = struct{}{}
		merged = append(merged, ch)
	}
	return merged
}
