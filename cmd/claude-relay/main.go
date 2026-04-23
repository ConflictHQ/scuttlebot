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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/conflicthq/scuttlebot/pkg/ircagent"
	"github.com/conflicthq/scuttlebot/pkg/relaymirror"
	"github.com/conflicthq/scuttlebot/pkg/sessionrelay"
	"github.com/creack/pty"
	"github.com/google/uuid"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

const (
	defaultRelayURL      = "http://localhost:8080"
	defaultIRCAddr       = "127.0.0.1:6667"
	defaultChannel       = "general"
	defaultTransport     = sessionrelay.TransportIRC
	defaultPollInterval  = 2 * time.Second
	defaultConnectWait   = 30 * time.Second
	defaultInjectDelay   = 150 * time.Millisecond
	defaultBusyWindow    = 1500 * time.Millisecond
	defaultHeartbeat     = 60 * time.Second
	defaultConfigFile    = ".config/scuttlebot-relay.env"
	defaultScanInterval  = 250 * time.Millisecond
	defaultDiscoverWait  = 60 * time.Second
	defaultMirrorLineMax = 360
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

var (
	secretHexPattern   = regexp.MustCompile(`\b[a-f0-9]{32,}\b`)
	secretKeyPattern   = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+\b`)
	bearerPattern      = regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._:-]+)`)
	assignTokenPattern = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(TOKEN|KEY|SECRET|PASSPHRASE)[A-Z0-9_]*=)([^ \t"'` + "`" + `]+)`)
)

type config struct {
	ClaudeBin          string
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
	ClaudeSessionID    string // UUID passed to Claude Code via --session-id
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
	Meta json.RawMessage // nil for plain text lines
}

type relayState struct {
	mu       sync.RWMutex
	lastBusy time.Time
}

// Claude Code JSONL session entry.
type claudeSessionEntry struct {
	Type    string `json:"type"`
	CWD     string `json:"cwd"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
}

func main() {
	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude-relay:", err)
		os.Exit(1)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "claude-relay:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	fmt.Fprintf(os.Stderr, "claude-relay: nick %s\n", cfg.Nick)
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
			fmt.Fprintf(os.Stderr, "claude-relay: relay disabled: %v\n", err)
		} else {
			connectCtx, connectCancel := context.WithTimeout(ctx, defaultConnectWait)
			if err := conn.Connect(connectCtx); err != nil {
				fmt.Fprintf(os.Stderr, "claude-relay: relay disabled: %v\n", err)
				_ = conn.Close(context.Background())
			} else {
				relay = conn
				relayActive = true

				// Auto-provision dedicated session channel.
				sessionChannel := fmt.Sprintf("session-%s", cfg.Nick)
				if err := relay.JoinChannel(ctx, sessionChannel); err != nil {
					fmt.Fprintf(os.Stderr, "claude-relay: session channel provision: %v\n", err)
				} else {
					cfg.Channels = mergeChannels(cfg.Channels, []string{sessionChannel})
				}

				filtered = buildFilteredConnector(relay, cfg)
				if err := sessionrelay.WriteChannelStateFile(cfg.ChannelStateFile, relay.ControlChannel(), relay.Channels()); err != nil {
					fmt.Fprintf(os.Stderr, "claude-relay: channel state disabled: %v\n", err)
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

	startedAt := time.Now()
	// If resuming, extract the session ID from --resume arg. Otherwise use
	// our generated UUID via --session-id for new sessions.
	if resumeID := extractResumeID(cfg.Args); resumeID != "" {
		cfg.ClaudeSessionID = resumeID
		fmt.Fprintf(os.Stderr, "claude-relay: resuming session %s\n", resumeID)
	} else {
		// New session — inject --session-id so the file name is deterministic.
		cfg.Args = append([]string{"--session-id", cfg.ClaudeSessionID}, cfg.Args...)
		fmt.Fprintf(os.Stderr, "claude-relay: new session %s\n", cfg.ClaudeSessionID)
	}
	cmd := exec.Command(cfg.ClaudeBin, cfg.Args...)
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
	// PTY mirror: only used for busy signal detection and dedup, NOT for
	// posting to IRC. The session file mirror handles all IRC output with
	// clean structured data. PTY output is too noisy (spinners, partial
	// renders, ANSI fragments) for direct IRC posting.
	var ptyMirror *relaymirror.PTYMirror
	if relayActive {
		ptyMirror = relaymirror.NewPTYMirror(defaultMirrorLineMax, 500*time.Millisecond, func(line string) {
			// no-op: session file mirror handles IRC output
		})
		go mirrorSessionLoop(ctx, relay, filtered, cfg, startedAt, ptyMirror)
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
	// Wire PTY mirror for dual-path: real-time text to IRC + session file for metadata.
	if ptyMirror != nil {
		ptyMirror.BusyCallback = func(now time.Time) {
			state.mu.Lock()
			state.lastBusy = now
			state.mu.Unlock()
		}
		go func() {
			_ = ptyMirror.Copy(ptmx, os.Stdout)
		}()
	} else {
		go func() {
			copyPTYOutput(ptmx, os.Stdout, state)
		}()
	}
	if relayActive {
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

// --- Session mirroring ---

func mirrorSessionLoop(ctx context.Context, relay sessionrelay.Connector, filtered *sessionrelay.FilteredConnector, cfg config, startedAt time.Time, ptyDedup *relaymirror.PTYMirror) {
	for {
		if ctx.Err() != nil {
			return
		}
		sessionPath, err := discoverSessionPath(ctx, cfg, startedAt)
		if err != nil {
			// Context cancelled — Claude Code exited while we were waiting.
			return
		}
		fmt.Fprintf(os.Stderr, "claude-relay: session file discovered: %s\n", sessionPath)
		if err := tailSessionFile(ctx, sessionPath, cfg.MirrorReasoning, startedAt, func(ml mirrorLine) {
			for _, line := range splitMirrorText(ml.Text) {
				if line == "" {
					continue
				}
				// Mark as seen so PTY mirror deduplicates.
				if ptyDedup != nil {
					ptyDedup.MarkSeen(line)
				}
				if filtered != nil {
					level := sessionrelay.LevelContent
					if strings.HasPrefix(line, "\xf0\x9f\x92\xad ") {
						level = sessionrelay.LevelReasoning
					} else if len(ml.Meta) > 0 {
						level = sessionrelay.LevelAction
					}
					_ = filtered.PostAtLevel(ctx, level, line, ml.Meta)
				} else if len(ml.Meta) > 0 {
					_ = relay.PostWithMeta(ctx, line, ml.Meta)
				} else {
					_ = relay.Post(ctx, line)
				}
			}
		}); err != nil && ctx.Err() == nil {
			// Tail lost — retry discovery.
			time.Sleep(5 * time.Second)
			continue
		}
		return
	}
}

// discoverSessionPath blocks until a session file is found (or ctx is cancelled).
//
// Claude Code creates the .jsonl lazily — only after the first user interaction —
// so there is no meaningful deadline to impose. We scan every 250ms:
//   - Primary:  look for the exact UUID we passed via --session-id
//   - Fallback: look for any .jsonl whose mtime is after startedAt
//     (handles Claude Code versions that ignore --session-id)
func discoverSessionPath(ctx context.Context, cfg config, startedAt time.Time) (string, error) {
	root, err := claudeSessionsRoot(cfg.TargetCWD)
	if err != nil {
		return "", err
	}

	target := filepath.Join(root, cfg.ClaudeSessionID+".jsonl")
	fmt.Fprintf(os.Stderr, "claude-relay: waiting for session file (send a message to Claude to begin)...\n")

	ticker := time.NewTicker(defaultScanInterval)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(os.Stderr, "claude-relay: found session file %s\n", target)
			return target, nil
		}
		// Fallback: Claude Code may ignore --session-id and create the file
		// under a self-chosen UUID once the session starts.
		if p := newestSessionFile(root, startedAt); p != "" {
			fmt.Fprintf(os.Stderr, "claude-relay: session file (by mtime) %s\n", p)
			return p, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// dumpSessionDir logs all .jsonl files in dir with their mtimes, flagging any
// that post-date minTime. Used to diagnose session file discovery failures.
func dumpSessionDir(dir string, minTime time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-relay: session dir unreadable: %v\n", dir)
		return
	}
	fmt.Fprintf(os.Stderr, "claude-relay: session dir %s (startedAt=%s):\n", dir, minTime.Format(time.RFC3339))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mark := " "
		if info.ModTime().After(minTime) {
			mark = ">"
		}
		fmt.Fprintf(os.Stderr, "  %s %s  %s\n", mark, info.ModTime().Format("15:04:05"), e.Name())
	}
}

// newestSessionFile returns the most recently modified .jsonl file in dir
// whose modification time is after minTime. Returns "" if none found.
func newestSessionFile(dir string, minTime time.Time) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mod := info.ModTime(); mod.After(minTime) && mod.After(bestMod) {
			best = filepath.Join(dir, e.Name())
			bestMod = mod
		}
	}
	return best
}

// extractResumeID finds --resume or -r in args and returns the session UUID
// that follows it. Returns "" if not resuming or if the value isn't a UUID.
func extractResumeID(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--resume" || args[i] == "-r" || args[i] == "--continue" {
			val := args[i+1]
			// Must look like a UUID (contains dashes, right length)
			if len(val) >= 32 && strings.Contains(val, "-") {
				return val
			}
		}
	}
	return ""
}

// claudeSessionsRoot returns ~/.claude/projects/<sanitized-cwd>/
func claudeSessionsRoot(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sanitized := strings.ReplaceAll(cwd, "/", "-")
	sanitized = strings.TrimLeft(sanitized, "-")
	return filepath.Join(home, ".claude", "projects", "-"+sanitized), nil
}

func tailSessionFile(ctx context.Context, path string, mirrorReasoning bool, since time.Time, emit func(mirrorLine)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Read from the beginning and filter by timestamp so we don't miss
	// the first response when Claude Code creates the session file lazily
	// (after the first message is processed, not at startup).
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			for _, ml := range sessionMessages(line, mirrorReasoning, since) {
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
			// Reset the buffered reader so it retries the underlying
			// file descriptor. bufio.Reader caches EOF and won't see
			// new bytes appended to the file without a reset.
			reader.Reset(file)
			continue
		}
		return err
	}
}

// sessionMessages parses a Claude Code JSONL line and returns mirror lines
// with optional structured metadata for rich rendering in the web UI.
// If mirrorReasoning is true, thinking blocks are included prefixed with "💭 ".
// Only entries whose Timestamp is after since are returned (zero since = no filter).
func sessionMessages(line []byte, mirrorReasoning bool, since time.Time) []mirrorLine {
	var entry claudeSessionEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil
	}
	if entry.Type != "assistant" || entry.Message.Role != "assistant" {
		return nil
	}
	// Skip entries that predate our session start.
	if !since.IsZero() && entry.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil && !ts.After(since) {
			return nil
		}
	}

	var out []mirrorLine
	for _, block := range entry.Message.Content {
		switch block.Type {
		case "text":
			for _, l := range splitMirrorText(block.Text) {
				if l != "" {
					out = append(out, mirrorLine{Text: sanitizeSecrets(l)})
				}
			}
		case "tool_use":
			if msg := summarizeToolUse(block.Name, block.Input); msg != "" {
				out = append(out, mirrorLine{
					Text: msg,
					Meta: toolMeta(block.Name, block.Input),
				})
			}
		case "thinking":
			if mirrorReasoning {
				for _, l := range splitMirrorText(block.Text) {
					if l != "" {
						out = append(out, mirrorLine{Text: "💭 " + sanitizeSecrets(l)})
					}
				}
			}
		}
	}
	return out
}

// toolMeta builds a JSON metadata envelope for a tool_use block.
func toolMeta(name string, inputRaw json.RawMessage) json.RawMessage {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(inputRaw, &input)

	data := map[string]string{"tool": name}

	str := func(key string) string {
		v, ok := input[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return strings.Trim(string(v), `"`)
		}
		return s
	}

	switch name {
	case "Bash":
		if cmd := str("command"); cmd != "" {
			data["command"] = sanitizeSecrets(cmd)
		}
	case "Edit", "Write", "Read":
		if p := str("file_path"); p != "" {
			data["file"] = p
		}
	case "Glob":
		if p := str("pattern"); p != "" {
			data["pattern"] = p
		}
	case "Grep":
		if p := str("pattern"); p != "" {
			data["pattern"] = p
		}
	case "WebFetch":
		if u := str("url"); u != "" {
			data["url"] = sanitizeSecrets(u)
		}
	case "WebSearch":
		if q := str("query"); q != "" {
			data["query"] = q
		}
	}

	meta := map[string]any{
		"type": "tool_result",
		"data": data,
	}
	b, _ := json.Marshal(meta)
	return b
}

func summarizeToolUse(name string, inputRaw json.RawMessage) string {
	var input map[string]json.RawMessage
	_ = json.Unmarshal(inputRaw, &input)

	str := func(key string) string {
		v, ok := input[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return strings.Trim(string(v), `"`)
		}
		return s
	}

	switch name {
	case "Bash":
		cmd := sanitizeSecrets(compactCommand(str("command")))
		if cmd != "" {
			return "› " + cmd
		}
		return "› bash"
	case "Edit":
		if p := str("file_path"); p != "" {
			return "edit " + p
		}
		return "edit"
	case "Write":
		if p := str("file_path"); p != "" {
			return "write " + p
		}
		return "write"
	case "Read":
		if p := str("file_path"); p != "" {
			return "read " + p
		}
		return "read"
	case "Glob":
		if p := str("pattern"); p != "" {
			return "glob " + p
		}
		return "glob"
	case "Grep":
		if p := str("pattern"); p != "" {
			return "grep " + p
		}
		return "grep"
	case "Agent":
		return "spawn agent"
	case "WebFetch":
		if u := str("url"); u != "" {
			return "fetch " + sanitizeSecrets(u)
		}
		return "fetch"
	case "WebSearch":
		if q := str("query"); q != "" {
			return "search " + q
		}
		return "search"
	case "TodoWrite":
		return "update todos"
	case "NotebookEdit":
		if p := str("notebook_path"); p != "" {
			return "edit notebook " + p
		}
		return "edit notebook"
	default:
		if name == "" {
			return ""
		}
		return name
	}
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

// --- Relay input (operator → Claude) ---

func relayInputLoop(ctx context.Context, relay sessionrelay.Connector, cfg config, state *relayState, ptyFile *os.File, since time.Time) {
	fmt.Fprintf(os.Stderr, "claude-relay: relayInputLoop started (nick=%s since=%s)\n", cfg.Nick, since.Format(time.RFC3339))
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
				debugf("claude-relay: injecting from=%s: %s\n", m.Nick, truncateMsg(m.Text, 100))
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

// handleReconnectSignal listens for SIGUSR1 and tears down/rebuilds
// the IRC connection. The relay-watchdog sidecar sends this signal
// when it detects the server restarted or the network is down.
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

		fmt.Fprintf(os.Stderr, "claude-relay: received SIGUSR1, reconnecting IRC...\n")
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
			fmt.Fprintf(os.Stderr, "claude-relay: reconnected, restarting mirror and input loops\n")

			// Restart mirror and input loops with the new connector.
			// Use epoch time for mirror so it finds the existing session file
			// regardless of when it was last modified.
			go mirrorSessionLoop(ctx, conn, newFiltered, cfg, time.Time{}, nil)
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

	// Claude Code's input widget is an Ink/React TUI that treats raw
	// keystrokes as shortcuts (C, brackets, newlines trigger UI actions
	// rather than inserting text). Multi-line content must arrive via
	// bracketed paste so the widget takes it as literal text. Strip the
	// trailing newline so the submit \r lands on the text, not an empty
	// line, then submit.
	payload := strings.TrimRight(block.String(), "\r\n \t")
	const (
		pasteStart = "\x1b[200~"
		pasteEnd   = "\x1b[201~"
	)
	if _, err := writer.Write([]byte(pasteStart + payload + pasteEnd)); err != nil {
		return err
	}
	// Give the TUI a moment to finish processing the paste before submit.
	time.Sleep(50 * time.Millisecond)
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
	// Claude Code uses "esc to interrupt" as its busy signal.
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
			debugf("claude-relay: filter skip self: %s\n", truncateMsg(msg.Text, 60))
			continue
		}
		if _, ok := serviceBots[msg.Nick]; ok {
			debugf("claude-relay: filter skip service-bot %s\n", msg.Nick)
			continue
		}
		if ircagent.HasAnyPrefix(msg.Nick, ircagent.DefaultActivityPrefixes()) {
			debugf("claude-relay: filter skip activity-prefix %s\n", msg.Nick)
			continue
		}
		if !ircagent.MentionsNick(msg.Text, nick) && !ircagent.MatchesGroupMention(msg.Text, nick, agentType) {
			debugf("claude-relay: filter drop from=%s: no nick mention (nick=%s text=%q)\n", msg.Nick, nick, truncateMsg(msg.Text, 80))
			continue
		}
		debugf("claude-relay: filter pass from=%s: %s\n", msg.Nick, truncateMsg(msg.Text, 100))
		filtered = append(filtered, msg)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].At.Before(filtered[j].At)
	})
	return filtered, newest
}

// --- Config loading ---

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
		ClaudeBin:          getenvOr(fileConfig, "CLAUDE_BIN", "claude"),
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
		sessionID = defaultSessionID(target)
	}
	cfg.SessionID = sanitize(sessionID)
	cfg.ClaudeSessionID = uuid.New().String()

	nick := getenvOr(fileConfig, "SCUTTLEBOT_NICK", "")
	if nick == "" {
		nick = fmt.Sprintf("claude-%s-%s", sanitize(filepath.Base(target)), cfg.SessionID)
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
			fmt.Fprintf(os.Stderr, "claude-relay: channel resolution config: %v\n", err)
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
		return filepath.Join(".config", "scuttlebot-relay.env")
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
