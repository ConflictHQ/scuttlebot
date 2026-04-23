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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"encoding/json"

	"github.com/conflicthq/scuttlebot/pkg/ircagent"
	"github.com/conflicthq/scuttlebot/pkg/relaymirror"
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
	defaultConnectWait   = 30 * time.Second
	defaultInjectDelay   = 150 * time.Millisecond
	defaultBusyWindow    = 1500 * time.Millisecond
	defaultMirrorLineMax = 360
	defaultHeartbeat     = 60 * time.Second
	defaultConfigFile    = ".config/scuttlebot-relay.env"
	bracketedPasteStart  = "\x1b[200~"
	bracketedPasteEnd    = "\x1b[201~"
)

// relayDebug enables verbose per-message logging. Set RELAY_DEBUG=1 to activate.
var relayDebug = os.Getenv("RELAY_DEBUG") != ""

func debugf(format string, args ...any) {
	if relayDebug {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

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
	IRCTLS             bool
	Channel            string
	Channels           []string
	ProjectChannel     string
	TeamChannel        string
	ChannelResolutions string // "chan:level,chan:level" override
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
	var filtered *sessionrelay.FilteredConnector
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
				TLS:           cfg.IRCTLS,
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

				// Auto-provision dedicated session channel.
				sessionChannel := fmt.Sprintf("session-%s", cfg.Nick)
				if err := relay.JoinChannel(ctx, sessionChannel); err != nil {
					fmt.Fprintf(os.Stderr, "gemini-relay: session channel provision: %v\n", err)
				} else {
					cfg.Channels = mergeChannels(cfg.Channels, []string{sessionChannel})
				}

				filtered = buildFilteredConnector(relay, cfg)
				if err := sessionrelay.WriteChannelStateFile(cfg.ChannelStateFile, relay.ControlChannel(), relay.Channels()); err != nil {
					fmt.Fprintf(os.Stderr, "gemini-relay: channel state disabled: %v\n", err)
				}
				onlineAt = time.Now()
				_ = filtered.PostAtLevel(context.Background(), sessionrelay.LevelLifecycle, fmt.Sprintf(
					"online in %s; mention %s to interrupt before the next action",
					filepath.Base(cfg.TargetCWD), cfg.Nick,
				), nil)
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
	)
	if relayActive {
		go presenceLoopFiltered(ctx, &relay, filtered, cfg.HeartbeatInterval)
	}

	// Non-interactive pass-through only when relay is disabled. When relayActive,
	// we always use a PTY so relayInputLoop can inject IRC messages regardless of
	// whether stdin/stdout are real terminals.
	if !relayActive && !isInteractiveTTY() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return err
	}
	defer func() { _ = ptmx.Close() }()

	state := &relayState{}

	// Terminal size and raw mode only apply when stdin is an actual TTY.
	if isInteractiveTTY() {
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
	}

	go func() {
		_, _ = io.Copy(ptmx, os.Stdin)
	}()
	// Dual-path mirroring: PTY for real-time text + session file for metadata.
	ptyMirror := relaymirror.NewPTYMirror(defaultMirrorLineMax, 500*time.Millisecond, func(_ string) {
		// no-op: session file mirror handles IRC output
	})
	ptyMirror.BusyCallback = func(now time.Time) {
		state.mu.Lock()
		state.lastBusy = now
		state.mu.Unlock()
	}
	go func() {
		_ = ptyMirror.Copy(ptmx, os.Stdout)
	}()
	if relayActive {
		// Start Gemini session file tailing for structured metadata.
		go geminiSessionMirrorLoop(ctx, relay, filtered, cfg, ptyMirror)
		go relayInputLoop(ctx, relay, cfg, state, ptmx, onlineAt)
		go handleReconnectSignal(ctx, &relay, &filtered, cfg, state, ptmx, startedAt)
	}

	err = cmd.Wait()
	cancel()

	exitCode := exitStatus(err)
	if relayActive {
		_ = filtered.PostAtLevel(context.Background(), sessionrelay.LevelLifecycle, fmt.Sprintf("offline (exit %d)", exitCode), nil)
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
			for _, m := range pending {
				debugf("gemini-relay: injecting from=%s: %s\n", m.Nick, truncateMsg(m.Text, 100))
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

func handleReconnectSignal(ctx context.Context, relayPtr *sessionrelay.Connector, filteredPtr **sessionrelay.FilteredConnector, cfg config, state *relayState, ptmx *os.File, startedAt time.Time) {
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
					TLS:           cfg.IRCTLS,
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
			newFiltered := buildFilteredConnector(conn, cfg)
			*filteredPtr = newFiltered
			now := time.Now()
			_ = newFiltered.PostAtLevel(context.Background(), sessionrelay.LevelLifecycle, fmt.Sprintf(
				"reconnected in %s; mention %s to interrupt",
				filepath.Base(cfg.TargetCWD), cfg.Nick,
			), nil)
			fmt.Fprintf(os.Stderr, "gemini-relay: reconnected, restarting input loop\n")

			// Restart input loop with the new connector.
			go relayInputLoop(ctx, conn, cfg, state, ptmx, now)
			break
		}
	}
}

func presenceLoopFiltered(ctx context.Context, relayPtr *sessionrelay.Connector, filtered *sessionrelay.FilteredConnector, interval time.Duration) {
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

// geminiSessionMirrorLoop discovers and polls a Gemini CLI session file
// for structured tool call metadata, emitting it via PostWithMeta.
func geminiSessionMirrorLoop(ctx context.Context, relay sessionrelay.Connector, filtered *sessionrelay.FilteredConnector, cfg config, ptyDedup *relaymirror.PTYMirror) {
	// Discover the Gemini session file directory.
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gemini-relay: session mirror: %v\n", err)
		return
	}
	// Gemini CLI stores sessions under ~/.gemini/tmp/<basename(gitRoot)>/chats/
	// when running inside a git repo, with sha256(gitRoot) as projectHash in
	// the session JSON. Outside a git repo it falls back to a slug of the cwd.
	// Try candidates in order of likelihood and pick the first that exists —
	// or the first one a new session file appears in.
	candidates := geminiChatsDirCandidates(home, cfg.TargetCWD)
	chatsDir := candidates[0]
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			chatsDir = c
			break
		}
	}
	_ = os.MkdirAll(chatsDir, 0755)
	existing := relaymirror.SnapshotDir(chatsDir)

	// Wait for a new session file.
	watcher := relaymirror.NewSessionWatcher(chatsDir, "session-", 60*time.Second)
	sessionPath, err := watcher.Discover(ctx, existing)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "gemini-relay: session discovery in %s: %v\n", chatsDir, err)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "gemini-relay: session file discovered: %s\n", sessionPath)

	// Poll the session file for new messages.
	msgIdx := 0
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			msgs, newIdx, err := relaymirror.PollGeminiSession(sessionPath, msgIdx)
			if err != nil {
				continue
			}
			msgIdx = newIdx
			for _, msg := range msgs {
				if msg.Type != "gemini" {
					continue
				}
				// Mirror thinking/reasoning blocks.
				if cfg.MirrorReasoning {
					for _, t := range msg.Thoughts {
						if t.Subject != "" {
							for _, line := range splitMirrorText(t.Subject) {
								text := "\xf0\x9f\x92\xad " + line
								if ptyDedup != nil {
									ptyDedup.MarkSeen(text)
								}
								if filtered != nil {
									_ = filtered.PostAtLevel(ctx, sessionrelay.LevelReasoning, text, nil)
								} else {
									_ = relay.Post(ctx, text)
								}
							}
						}
						if t.Description != "" {
							for _, line := range splitMirrorText(t.Description) {
								text := "\xf0\x9f\x92\xad " + line
								if ptyDedup != nil {
									ptyDedup.MarkSeen(text)
								}
								if filtered != nil {
									_ = filtered.PostAtLevel(ctx, sessionrelay.LevelReasoning, text, nil)
								} else {
									_ = relay.Post(ctx, text)
								}
							}
						}
					}
				}

				// Mirror content text.
				if msg.Content != "" {
					for _, line := range splitMirrorText(msg.Content) {
						if ptyDedup != nil {
							ptyDedup.MarkSeen(line)
						}
						if filtered != nil {
							_ = filtered.PostAtLevel(ctx, sessionrelay.LevelContent, line, nil)
						} else {
							_ = relay.Post(ctx, line)
						}
					}
				}

				// Mirror tool calls.
				for _, tc := range msg.ToolCalls {
					meta, _ := json.Marshal(map[string]any{
						"type": "tool_result",
						"data": map[string]any{
							"tool":   tc.Name,
							"status": tc.Status,
							"args":   tc.Args,
						},
					})
					text := fmt.Sprintf("[%s] %s", tc.Name, tc.Status)
					if ptyDedup != nil {
						ptyDedup.MarkSeen(text)
					}
					if filtered != nil {
						_ = filtered.PostAtLevel(ctx, sessionrelay.LevelAction, text, meta)
					} else {
						_ = relay.PostWithMeta(ctx, text, meta)
					}
				}
			}
		}
	}
}

func slugify(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return "default"
	}
	return s
}

// geminiChatsDirCandidates returns candidate ~/.gemini/tmp/<slug>/chats paths
// in order of likelihood. Gemini CLI ≥0.36 uses basename(gitRoot); older
// versions and non-git-repo runs use a full-path slug.
func geminiChatsDirCandidates(home, targetCWD string) []string {
	base := filepath.Join(home, ".gemini", "tmp")
	out := []string{}
	if gr := findGitRoot(targetCWD); gr != "" {
		out = append(out, filepath.Join(base, filepath.Base(gr), "chats"))
	}
	out = append(out,
		filepath.Join(base, filepath.Base(targetCWD), "chats"),
		filepath.Join(base, slugify(targetCWD), "chats"),
	)
	return out
}

// findGitRoot walks up from dir looking for a .git directory/file.
// Returns "" if not inside a git repo.
func findGitRoot(dir string) string {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// splitMirrorText normalises line endings, drops blank lines, and wraps
// long lines at word boundaries to fit within defaultMirrorLineMax.
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
			debugf("gemini-relay: filter skip self: %s\n", truncateMsg(msg.Text, 60))
			continue
		}
		if _, ok := serviceBots[msg.Nick]; ok {
			debugf("gemini-relay: filter skip service-bot %s\n", msg.Nick)
			continue
		}
		if ircagent.HasAnyPrefix(msg.Nick, ircagent.DefaultActivityPrefixes()) {
			debugf("gemini-relay: filter skip activity-prefix %s\n", msg.Nick)
			continue
		}
		if !ircagent.MentionsNick(msg.Text, nick) && !ircagent.MatchesGroupMention(msg.Text, nick, agentType) {
			debugf("gemini-relay: filter drop from=%s: no nick mention (nick=%s text=%q)\n", msg.Nick, nick, truncateMsg(msg.Text, 80))
			continue
		}
		debugf("gemini-relay: filter pass from=%s: %s\n", msg.Nick, truncateMsg(msg.Text, 100))
		filtered = append(filtered, msg)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].At.Before(filtered[j].At)
	})
	return filtered, newest
}

func loadConfig(args []string) (config, error) {
	fileConfig := readEnvFile(configFilePath())

	target, err := targetCWD(args)
	if err != nil {
		return config{}, err
	}

	// Load per-repo .scuttlebot.yaml (if any) and overlay its values onto the
	// user-global env file. Process env still wins over both via getenvOr.
	var repoCfg *repoConfig
	if rc, repoErr := loadRepoConfig(target); repoErr == nil && rc != nil {
		repoCfg = rc
		for k, v := range rc.envOverrides() {
			fileConfig[k] = v
		}
	}

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
		IRCTLS:             getenvBoolOr(fileConfig, "SCUTTLEBOT_IRC_TLS", false),
		HooksEnabled:       getenvBoolOr(fileConfig, "SCUTTLEBOT_HOOKS_ENABLED", true),
		InterruptOnMessage: getenvBoolOr(fileConfig, "SCUTTLEBOT_INTERRUPT_ON_MESSAGE", true),
		MirrorReasoning:    getenvBoolOr(fileConfig, "SCUTTLEBOT_MIRROR_REASONING", true),
		PollInterval:       getenvDurationOr(fileConfig, "SCUTTLEBOT_POLL_INTERVAL", defaultPollInterval),
		HeartbeatInterval:  getenvDurationAllowZeroOr(fileConfig, "SCUTTLEBOT_PRESENCE_HEARTBEAT", defaultHeartbeat),
		Args:               append([]string(nil), args...),
	}

	controlChannel := getenvOr(fileConfig, "SCUTTLEBOT_CHANNEL", defaultChannel)
	cfg.Channels = sessionrelay.ChannelSlugs(sessionrelay.ParseEnvChannels(controlChannel, getenvOr(fileConfig, "SCUTTLEBOT_CHANNELS", "")))
	if len(cfg.Channels) > 0 {
		cfg.Channel = cfg.Channels[0]
	}

	cfg.TargetCWD = target

	// Merge per-repo channel list if present (server/auth overrides already
	// applied via fileConfig above).
	if repoCfg != nil {
		cfg.Channels = mergeChannels(cfg.Channels, repoCfg.allChannels())
	}

	// Merge project/team channels if configured.
	cfg.ProjectChannel = getenvOr(fileConfig, "SCUTTLEBOT_PROJECT_CHANNEL", "")
	cfg.TeamChannel = getenvOr(fileConfig, "SCUTTLEBOT_TEAM_CHANNEL", "")
	if cfg.ProjectChannel != "" {
		cfg.Channels = mergeChannels(cfg.Channels, []string{cfg.ProjectChannel})
	}
	if cfg.TeamChannel != "" {
		cfg.Channels = mergeChannels(cfg.Channels, []string{cfg.TeamChannel})
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

	cfg.ChannelResolutions = getenvOr(fileConfig, "SCUTTLEBOT_CHANNEL_RESOLUTION", "")

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

// buildFilteredConnector constructs a FilteredConnector that assigns default
// resolutions by channel naming convention and applies explicit overrides.
func buildFilteredConnector(relay sessionrelay.Connector, cfg config) *sessionrelay.FilteredConnector {
	resMap := make(map[string]sessionrelay.Resolution)
	for _, ch := range relay.Channels() {
		slug := strings.TrimPrefix(ch, "#")
		switch {
		case strings.HasPrefix(slug, "session-"):
			resMap[ch] = sessionrelay.ResDebug
		case strings.HasPrefix(slug, "project-"):
			resMap[ch] = sessionrelay.ResActions
		case strings.HasPrefix(slug, "team-"):
			resMap[ch] = sessionrelay.ResFull
		default:
			resMap[ch] = sessionrelay.ResFull
		}
	}
	if cfg.ChannelResolutions != "" {
		overrides, err := sessionrelay.ParseChannelResolutions(cfg.ChannelResolutions)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gemini-relay: channel resolution config: %v\n", err)
		} else {
			for ch, res := range overrides {
				resMap[ch] = res
			}
		}
	}
	return sessionrelay.NewFilteredConnector(relay, resMap, sessionrelay.ResFull)
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
//
// Precedence is process env > repo yaml > user env file > defaults. That is,
// anything the yaml sets wins over the user-global env file but never
// overrides an explicit process env var at run time.
type repoConfig struct {
	URL       string   `yaml:"url"`
	Token     string   `yaml:"token"`
	Transport string   `yaml:"transport"`
	IRCAddr   string   `yaml:"irc_addr"`
	IRCTLS    *bool    `yaml:"irc_tls"`
	IRCPass   string   `yaml:"irc_pass"`
	Channel   string   `yaml:"channel"`
	Channels  []string `yaml:"channels"`
}

// allChannels returns the singular channel (if set) prepended to the channels list.
func (rc *repoConfig) allChannels() []string {
	if rc.Channel == "" {
		return rc.Channels
	}
	return append([]string{rc.Channel}, rc.Channels...)
}

// envOverrides returns the env-var key/value pairs the repo yaml wants to
// impose over the user-global env file. Only keys the yaml actually set are
// present; missing keys fall through to the env file / defaults unchanged.
func (rc *repoConfig) envOverrides() map[string]string {
	if rc == nil {
		return nil
	}
	out := map[string]string{}
	if rc.URL != "" {
		out["SCUTTLEBOT_URL"] = rc.URL
	}
	if rc.Token != "" {
		out["SCUTTLEBOT_TOKEN"] = rc.Token
	}
	if rc.Transport != "" {
		out["SCUTTLEBOT_TRANSPORT"] = rc.Transport
	}
	if rc.IRCAddr != "" {
		out["SCUTTLEBOT_IRC_ADDR"] = rc.IRCAddr
	}
	if rc.IRCPass != "" {
		out["SCUTTLEBOT_IRC_PASS"] = rc.IRCPass
	}
	if rc.IRCTLS != nil {
		out["SCUTTLEBOT_IRC_TLS"] = strconv.FormatBool(*rc.IRCTLS)
	}
	return out
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
