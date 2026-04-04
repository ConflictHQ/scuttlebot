package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	defaultRelayURL     = "http://localhost:8080"
	defaultIRCAddr      = "127.0.0.1:6667"
	defaultChannel      = "general"
	defaultTransport    = sessionrelay.TransportHTTP
	defaultPollInterval = 2 * time.Second
	defaultConnectWait  = 10 * time.Second
	defaultInjectDelay  = 150 * time.Millisecond
	defaultBusyWindow   = 1500 * time.Millisecond
	defaultHeartbeat    = 60 * time.Second
	defaultConfigFile   = ".config/scuttlebot-relay.env"
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
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

type config struct {
	GeminiBin          string
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
	PollInterval       time.Duration
	HeartbeatInterval  time.Duration
	TargetCWD          string
	Args               []string
}

type message = sessionrelay.Message

type relayState struct {
	mu       sync.RWMutex
	lastBusy time.Time
}

func main() {
	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "gemini-relay:", err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "gemini-relay:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	fmt.Fprintf(os.Stderr, "gemini-relay: nick %s\n", cfg.Nick)
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
			fmt.Fprintf(os.Stderr, "gemini-relay: relay disabled: %v\n", err)
		} else {
			connectCtx, connectCancel := context.WithTimeout(ctx, defaultConnectWait)
			if err := conn.Connect(connectCtx); err != nil {
				fmt.Fprintf(os.Stderr, "gemini-relay: relay disabled: %v\n", err)
				_ = conn.Close(context.Background())
			} else {
				relay = conn
				relayActive = true
				if err := sessionrelay.WriteChannelStateFile(cfg.ChannelStateFile, relay.ControlChannel(), relay.Channels()); err != nil {
					fmt.Fprintf(os.Stderr, "gemini-relay: channel state disabled: %v\n", err)
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

	cmd := exec.Command(cfg.GeminiBin, cfg.Args...)
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
	)
	if relayActive {
		go presenceLoopPtr(ctx, &relay, cfg.HeartbeatInterval)
		go handleReconnectSignal(ctx, &relay, cfg)
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

func handleReconnectSignal(ctx context.Context, relayPtr *sessionrelay.Connector, cfg config) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
		}

		fmt.Fprintf(os.Stderr, "gemini-relay: received SIGUSR1, reconnecting IRC...\n")
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
			_ = conn.Post(context.Background(), fmt.Sprintf(
				"reconnected in %s; mention %s to interrupt",
				filepath.Base(cfg.TargetCWD), cfg.Nick,
			))
			fmt.Fprintf(os.Stderr, "gemini-relay: reconnected successfully\n")
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

	// Gemini treats bracketed paste as literal input, which avoids shell-mode
	// toggles and other shortcut handling for operator text like "!" or "??".
	paste := bracketedPasteStart + block.String() + bracketedPasteEnd
	if _, err := writer.Write([]byte(paste)); err != nil {
		return err
	}
	time.Sleep(defaultInjectDelay)
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
	// Gemini CLI uses different busy indicators, but we can look for generic prompt signals
	// or specific strings if we know them. For now, we'll keep it simple or add generic ones.
	if strings.Contains(strings.ToLower(string(data)), "esc to interrupt") ||
		strings.Contains(strings.ToLower(string(data)), "working...") {
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
		GeminiBin:          getenvOr(fileConfig, "GEMINI_BIN", "gemini"),
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
		sessionID = getenvOr(fileConfig, "GEMINI_SESSION_ID", "")
	}
	if sessionID == "" {
		sessionID = defaultSessionID(target)
	}
	cfg.SessionID = sanitize(sessionID)

	nick := getenvOr(fileConfig, "SCUTTLEBOT_NICK", "")
	if nick == "" {
		nick = fmt.Sprintf("gemini-%s-%s", sanitize(filepath.Base(target)), cfg.SessionID)
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
		return filepath.Join(".config", "scuttlebot-relay.env") // Fallback
	}
	return filepath.Join(home, ".config", "scuttlebot-relay.env")
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
