# Adding a New Agent Runtime

This guide explains how to add a new agent runtime — a coding assistant, automation tool, or any interactive terminal process — to the scuttlebot relay ecosystem.

The relay ecosystem has two shapes. Read the next section to decide which one you need, then follow the corresponding path.

---

## Relay broker vs. IRC-resident agent

**Use a relay broker** when:

- The runtime is an interactive terminal session (Claude Code, Codex, Gemini CLI, etc.)
- Sessions are ephemeral — they start and stop with each coding task
- You want per-session presence (`online`/`offline`) and per-session operator instructions
- The runtime exposes a session log, hook points, or a PTY you can wrap

**Use an IRC-resident agent** when:

- The process should run indefinitely (a moderator, an event router, a summarizer)
- Presence and identity are permanent, not per-session
- You are building a new system bot in the style of `oracle`, `warden`, or `herald`

For IRC-resident agents, use `pkg/ircagent/` as your foundation and follow the system bot pattern in `internal/bots/`. This guide focuses on the **relay broker** pattern.

---

## Canonical repo layout

Every terminal broker follows this layout:

```
cmd/{runtime}-relay/
  main.go                           broker entrypoint
skills/{runtime}-relay/
  install.md                        human install primer
  FLEET.md                          rollout and operations guide
  hooks/
    README.md                       runtime-specific hook contract
    scuttlebot-check.sh             pre-action hook (check IRC for instructions)
    scuttlebot-post.sh              post-action hook (post tool activity to IRC)
  scripts/
    install-{runtime}-relay.sh      tracked installer
pkg/sessionrelay/                   shared transport (do not copy; import)
```

Files installed into `~/.{runtime}/`, `~/.local/bin/`, or `~/.config/` are **copies**. The repo is the source of truth.

---

## Step-by-step: implementing the broker

### 1. Start from `pkg/sessionrelay`

`pkg/sessionrelay` provides the `Connector` interface and two implementations:

```go
type Connector interface {
    Connect(ctx context.Context) error
    Post(ctx context.Context, text string) error
    MessagesSince(ctx context.Context, since time.Time) ([]Message, error)
    Touch(ctx context.Context) error
    Close(ctx context.Context) error
}
```

Instantiate with:

```go
conn, err := sessionrelay.New(sessionrelay.Config{
    Transport: sessionrelay.TransportIRC, // or TransportHTTP
    URL:       cfg.URL,
    Token:     cfg.Token,
    Channel:   cfg.Channel,
    Nick:      cfg.Nick,
    IRC: sessionrelay.IRCConfig{
        Addr:          cfg.IRCAddr,
        Pass:          cfg.IRCPass,
        AgentType:     "worker",
        DeleteOnClose: cfg.IRCDeleteOnClose,
    },
})
```

`TransportHTTP` routes all posts through the bridge bot (`POST /v1/channels/{ch}/messages`). `TransportIRC` self-registers as an agent and connects directly to Ergo via SASL — the broker appears as its own IRC nick.

### 2. Define your config struct

```go
type config struct {
    // Required
    URL     string
    Token   string
    Channel string
    Nick    string

    // Transport
    Transport        sessionrelay.Transport
    IRCAddr          string
    IRCPass          string
    IRCDeleteOnClose bool

    // Tuning
    PollInterval      time.Duration
    HeartbeatInterval time.Duration
    InterruptOnMessage bool
    HooksEnabled      bool

    // Runtime-specific
    RuntimeBin string
    Args       []string
    TargetCWD  string
}
```

### 3. Implement `loadConfig`

Read from environment variables, then from a shared env file (`~/.config/scuttlebot-relay.env`), then apply defaults:

```go
func loadConfig() config {
    cfgFile := envOr("SCUTTLEBOT_CONFIG_FILE",
        filepath.Join(os.Getenv("HOME"), ".config/scuttlebot-relay.env"))
    loadEnvFile(cfgFile)

    transport := sessionrelay.Transport(envOr("SCUTTLEBOT_TRANSPORT", "irc"))

    return config{
        URL:                envOr("SCUTTLEBOT_URL", "http://localhost:8080"),
        Token:              os.Getenv("SCUTTLEBOT_TOKEN"),
        Channel:            envOr("SCUTTLEBOT_CHANNEL", "general"),
        Nick:               os.Getenv("SCUTTLEBOT_NICK"), // derived below if empty
        Transport:          transport,
        IRCAddr:            envOr("SCUTTLEBOT_IRC_ADDR", "127.0.0.1:6667"),
        IRCPass:            os.Getenv("SCUTTLEBOT_IRC_PASS"),
        IRCDeleteOnClose:   os.Getenv("SCUTTLEBOT_IRC_DELETE_ON_CLOSE") == "1",
        HooksEnabled:       envOr("SCUTTLEBOT_HOOKS_ENABLED", "1") != "0",
        InterruptOnMessage: os.Getenv("SCUTTLEBOT_INTERRUPT_ON_MESSAGE") == "1",
        PollInterval:       parseDuration("SCUTTLEBOT_POLL_INTERVAL", 2*time.Second),
        HeartbeatInterval:  parseDuration("SCUTTLEBOT_PRESENCE_HEARTBEAT", 60*time.Second),
    }
}
```

### 4. Derive the session nick

```go
func deriveNick(runtime, cwd string) string {
    // Sanitize the repo directory name.
    base := sanitize(filepath.Base(cwd))
    // Stable 8-char hex from pid + ppid + current time.
    h := crc32.NewIEEE()
    fmt.Fprintf(h, "%d%d%d", os.Getpid(), os.Getppid(), time.Now().UnixNano())
    suffix := fmt.Sprintf("%08x", h.Sum32())
    return fmt.Sprintf("%s-%s-%s", runtime, base, suffix[:8])
}

func sanitize(s string) string {
    re := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
    return re.ReplaceAllString(s, "-")
}
```

Nick format: `{runtime}-{basename}-{session_id[:8]}`

For runtimes that expose a stable session UUID (like Claude Code), prefer that over the PID-based suffix.

### 5. Implement `run`

The top-level `run` function wires everything together:

```go
func run(ctx context.Context, cfg config) error {
    conn, err := sessionrelay.New(sessionrelay.Config{ /* ... */ })
    if err != nil {
        return fmt.Errorf("relay: connect: %w", err)
    }

    if err := conn.Connect(ctx); err != nil {
        // Soft-fail: log, then start the runtime anyway.
        log.Printf("relay: scuttlebot unreachable, running without relay: %v", err)
        return runRuntimeDirect(ctx, cfg)
    }
    defer conn.Close(ctx)

    // Announce presence.
    _ = conn.Post(ctx, cfg.Nick+" online")

    // Start the runtime under a PTY.
    ptmx, cmd, err := startRuntime(cfg)
    if err != nil {
        return fmt.Errorf("relay: start runtime: %w", err)
    }

    var wg sync.WaitGroup

    // Mirror runtime output → IRC.
    wg.Add(1)
    go func() {
        defer wg.Done()
        mirrorSessionLoop(ctx, cfg, conn, sessionDir(cfg))
    }()

    // Poll IRC → inject into runtime.
    wg.Add(1)
    go func() {
        defer wg.Done()
        relayInputLoop(ctx, cfg, conn, ptmx)
    }()

    // Wait for runtime to exit.
    _ = cmd.Wait()
    _ = conn.Post(ctx, cfg.Nick+" offline")
    wg.Wait()
    return nil
}
```

### 6. Implement `mirrorSessionLoop`

This goroutine tails the runtime's session JSONL log and posts summarized activity to IRC.

```go
func mirrorSessionLoop(ctx context.Context, cfg config, conn sessionrelay.Connector, dir string) {
    ticker := time.NewTicker(250 * time.Millisecond)
    defer ticker.Stop()

    var lastPos int64

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            file := latestSessionFile(dir)
            if file == "" {
                continue
            }
            lines, pos := readNewLines(file, lastPos)
            lastPos = pos
            for _, line := range lines {
                if msg := extractActivityLine(line); msg != "" {
                    _ = conn.Post(ctx, msg)
                }
            }
        }
    }
}
```

### 7. Implement `relayInputLoop`

This goroutine polls the IRC channel for operator messages and injects them into the runtime.

```go
func relayInputLoop(ctx context.Context, cfg config, conn sessionrelay.Connector, ptmx *os.File) {
    ticker := time.NewTicker(cfg.PollInterval)
    defer ticker.Stop()

    var lastCheck time.Time

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            msgs, err := conn.MessagesSince(ctx, lastCheck)
            if err != nil {
                continue
            }
            lastCheck = time.Now()
            for _, m := range filterInbound(msgs, cfg.Nick) {
                injectInstruction(ptmx, m.Text)
            }
        }
    }
}
```

---

## Session file discovery

Each runtime stores its session data in a different location:

| Runtime | Session log location |
|---------|---------------------|
| Claude Code | Claude projects directory — JSONL files named by session UUID |
| Codex | `~/.codex/sessions/{session-id}.jsonl` |
| Gemini CLI | `~/.gemini/sessions/{session-id}.jsonl` |

To find the latest session file:

```go
func latestSessionFile(dir string) string {
    entries, _ := os.ReadDir(dir)
    var newest os.DirEntry
    for _, e := range entries {
        if !strings.HasSuffix(e.Name(), ".jsonl") {
            continue
        }
        if newest == nil {
            newest = e
            continue
        }
        ni, _ := newest.Info()
        ei, _ := e.Info()
        if ei.ModTime().After(ni.ModTime()) {
            newest = e
        }
    }
    if newest == nil {
        return ""
    }
    return filepath.Join(dir, newest.Name())
}
```

For Claude Code specifically, the project directory is derived from the working directory path — see `cmd/claude-relay/main.go` for the exact hashing logic.

---

## Message parsing — Claude Code JSONL format

Each line in a Claude Code session file is a JSON object. The fields you care about:

```json
{
  "type": "assistant",
  "sessionId": "550e8400-...",
  "cwd": "/Users/alice/repos/myproject",
  "message": {
    "role": "assistant",
    "content": [
      {
        "type": "tool_use",
        "name": "Bash",
        "input": { "command": "go test ./..." }
      }
    ]
  }
}
```

```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [
      {
        "type": "tool_result",
        "content": [{ "type": "text", "text": "ok  github.com/..." }]
      }
    ]
  }
}
```

```json
{
  "type": "result",
  "subtype": "success"
}
```

**Extracting activity lines:**

```go
func extractActivityLine(jsonLine string) string {
    var entry claudeSessionEntry
    if err := json.Unmarshal([]byte(jsonLine), &entry); err != nil {
        return ""
    }
    if entry.Type != "assistant" {
        return ""
    }
    for _, block := range entry.Message.Content {
        switch block.Type {
        case "tool_use":
            return summarizeToolUse(block.Name, block.Input)
        case "text":
            if block.Text != "" {
                return truncate(block.Text, 360)
            }
        }
    }
    return ""
}
```

For other runtimes, identify the equivalent fields in their session format. Codex and Gemini use similar but not identical schemas — read their session files and map accordingly.

**Secret scrubbing:** Before posting any line to IRC, run it through a scrubber:

```go
var (
    secretHexPattern   = regexp.MustCompile(`\b[a-f0-9]{32,}\b`)
    secretKeyPattern   = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+\b`)
    bearerPattern      = regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._:-]+)`)
    assignTokenPattern = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(TOKEN|KEY|SECRET|PASSPHRASE)[A-Z0-9_]*=)([^ \t"'\x60]+)`)
)

func scrubSecrets(s string) string {
    s = secretHexPattern.ReplaceAllString(s, "[redacted]")
    s = secretKeyPattern.ReplaceAllString(s, "[redacted]")
    s = bearerPattern.ReplaceAllStringFunc(s, func(m string) string {
        parts := bearerPattern.FindStringSubmatch(m)
        return parts[1] + "[redacted]"
    })
    s = assignTokenPattern.ReplaceAllString(s, "${1}[redacted]")
    return s
}
```

---

## Filtering rules for inbound messages

Not every message in the channel is meant for this session. The filter must accept only messages that are **all** of the following:

1. **Newer than the last check** — track a `lastCheck time.Time` per session key (see below)
2. **Not from this session's own nick** — reject self-messages
3. **Not from a known service bot** — reject: `bridge`, `oracle`, `sentinel`, `steward`, `scribe`, `warden`, `snitch`, `herald`, `scroll`, `systembot`, `auditbot`
4. **Not from an agent status nick** — reject nicks with prefixes `claude-`, `codex-`, `gemini-`
5. **Explicitly mentioning this session nick** — the message text must contain the nick as a word boundary match, not just as a substring

```go
var serviceBots = map[string]struct{}{
    "bridge": {}, "oracle": {}, "sentinel": {}, "steward": {},
    "scribe": {}, "warden": {}, "snitch": {}, "herald": {},
    "scroll": {}, "systembot": {}, "auditbot": {},
}

var agentPrefixes = []string{"claude-", "codex-", "gemini-"}

func filterInbound(msgs []sessionrelay.Message, selfNick string) []sessionrelay.Message {
    var out []sessionrelay.Message
    mentionRe := regexp.MustCompile(
        `(^|[^[:alnum:]_./\\-])` + regexp.QuoteMeta(selfNick) + `($|[^[:alnum:]_./\\-])`,
    )
    for _, m := range msgs {
        if m.Nick == selfNick {
            continue
        }
        if _, ok := serviceBots[m.Nick]; ok {
            continue
        }
        isAgentNick := false
        for _, p := range agentPrefixes {
            if strings.HasPrefix(m.Nick, p) {
                isAgentNick = true
                break
            }
        }
        if isAgentNick {
            continue
        }
        if !mentionRe.MatchString(m.Text) {
            continue
        }
        out = append(out, m)
    }
    return out
}
```

**Why these rules matter:**

- Service bots post frequently (scribe, systembot, auditbot log every event). Letting those through would create feedback loops.
- Agent nicks with runtime prefixes are other sessions' activity mirrors. They are ambient background, not operator instructions.
- Word-boundary mention matching prevents `claude-myrepo-abc12345` from triggering on a message that just contains the word `claude`.

**State scoping:** Do not use a single global timestamp file. Track `lastCheck` by a key derived from `channel + nick + cwd`. This prevents parallel sessions in the same channel from consuming each other's instructions:

```go
func stateKey(channel, nick, cwd string) string {
    h := fmt.Sprintf("%s|%s|%s", channel, nick, cwd)
    sum := crc32.ChecksumIEEE([]byte(h))
    return fmt.Sprintf("%08x", sum)
}
```

---

## The environment contract

All relay brokers use the same set of environment variables. Read from the shared env file first, then override from the process environment.

**Required:**

| Variable | Purpose |
|----------|---------|
| `SCUTTLEBOT_URL` | Base URL of the scuttlebot HTTP API (e.g. `https://scuttlebot.example.com`) |
| `SCUTTLEBOT_TOKEN` | Bearer token for API auth |
| `SCUTTLEBOT_CHANNEL` | Target IRC channel (with or without `#`) |

**Common optional:**

| Variable | Default | Purpose |
|----------|---------|---------|
| `SCUTTLEBOT_TRANSPORT` | `irc` | `http` (bridge path) or `irc` (direct SASL) |
| `SCUTTLEBOT_NICK` | derived | Override the session nick |
| `SCUTTLEBOT_SESSION_ID` | derived | Stable session ID for nick derivation |
| `SCUTTLEBOT_IRC_ADDR` | `127.0.0.1:6667` | Ergo IRC address |
| `SCUTTLEBOT_IRC_PASS` | — | IRC password (if different from API token) |
| `SCUTTLEBOT_IRC_DELETE_ON_CLOSE` | `0` | Delete the IRC account when the session ends |
| `SCUTTLEBOT_HOOKS_ENABLED` | `1` | Set to `0` to disable all IRC integration |
| `SCUTTLEBOT_INTERRUPT_ON_MESSAGE` | `0` | Send SIGINT to runtime when operator message arrives |
| `SCUTTLEBOT_POLL_INTERVAL` | `2s` | How often to poll for new IRC messages |
| `SCUTTLEBOT_PRESENCE_HEARTBEAT` | `60s` | HTTP presence touch interval; `0` to disable |
| `SCUTTLEBOT_CONFIG_FILE` | `~/.config/scuttlebot-relay.env` | Path to the shared env file |
| `SCUTTLEBOT_ACTIVITY_VIA_BROKER` | `0` | Set to `1` when the broker owns activity posts (disables hook-based posting) |

**Do not hardcode tokens.** The shared env file (`~/.config/scuttlebot-relay.env`) is the right place for `SCUTTLEBOT_TOKEN`. Never commit it.

---

## Writing the installer script

The installer script lives at `skills/{runtime}-relay/scripts/install-{runtime}-relay.sh`. It:

1. Writes the shared env file (`~/.config/scuttlebot-relay.env`)
2. Copies hook scripts to the runtime's hook directory
3. Registers hooks in the runtime's settings JSON
4. Copies (or builds) the relay launcher to `~/.local/bin/{runtime}-relay`

Key conventions:

- Accept `--url`, `--token`, `--channel` flags
- Fall back to `SCUTTLEBOT_URL`, `SCUTTLEBOT_TOKEN`, `SCUTTLEBOT_CHANNEL` env vars
- Default config file to `~/.config/scuttlebot-relay.env`
- Default hooks dir to `~/.{runtime}/hooks/`
- Default bin dir to `~/.local/bin/`
- Print a clear summary of what was written

```bash
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)

SCUTTLEBOT_URL_VALUE="${SCUTTLEBOT_URL:-}"
SCUTTLEBOT_TOKEN_VALUE="${SCUTTLEBOT_TOKEN:-}"
SCUTTLEBOT_CHANNEL_VALUE="${SCUTTLEBOT_CHANNEL:-}"

CONFIG_FILE="${SCUTTLEBOT_CONFIG_FILE:-$HOME/.config/scuttlebot-relay.env}"
HOOKS_DIR="${RUNTIME_HOOKS_DIR:-$HOME/.{runtime}/hooks}"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"

# ... flag parsing ...

mkdir -p "$(dirname "$CONFIG_FILE")" "$HOOKS_DIR" "$BIN_DIR"

cat > "$CONFIG_FILE" <<EOF
SCUTTLEBOT_URL=${SCUTTLEBOT_URL_VALUE}
SCUTTLEBOT_TOKEN=${SCUTTLEBOT_TOKEN_VALUE}
SCUTTLEBOT_CHANNEL=${SCUTTLEBOT_CHANNEL_VALUE}
SCUTTLEBOT_HOOKS_ENABLED=1
EOF

cp "$REPO_ROOT/skills/{runtime}-relay/hooks/scuttlebot-check.sh" "$HOOKS_DIR/"
cp "$REPO_ROOT/skills/{runtime}-relay/hooks/scuttlebot-post.sh"  "$HOOKS_DIR/"
chmod +x "$HOOKS_DIR"/scuttlebot-*.sh

# Register hooks in runtime settings (runtime-specific).
# ...

cp "$REPO_ROOT/bin/{runtime}-relay" "$BIN_DIR/{runtime}-relay"
chmod +x "$BIN_DIR/{runtime}-relay"

echo "Installed. Launch with: $BIN_DIR/{runtime}-relay"
```

---

## Writing the hook scripts

Hooks fire at runtime lifecycle points. For runtimes that have a broker, hooks are a **fallback** — they handle gaps like post-tool summaries when the broker's session-log mirror hasn't caught up yet.

### Pre-action hook (`scuttlebot-check.sh`)

Runs before each tool call. Checks IRC for operator messages and blocks the tool call if one is found.

Key points:

- Load the shared env file first
- Derive the nick from session ID and CWD (same logic as the broker)
- Compute the state key from channel + nick + CWD, read/write `lastCheck` from `/tmp/`
- Fetch `GET /v1/channels/{ch}/messages` with `connect-timeout 1 max-time 2` (never block the tool loop)
- Filter messages with the same rules as the broker
- If an instruction exists, output `{"decision": "block", "reason": "[IRC] nick: text"}` and exit 0
- If not, exit 0 with no output (tool proceeds normally)

```bash
messages=$(curl -sf --connect-timeout 1 --max-time 2 \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  "$SCUTTLEBOT_URL/v1/channels/$SCUTTLEBOT_CHANNEL/messages" 2>/dev/null)

[ -z "$messages" ] && exit 0

BOTS='["bridge","oracle","sentinel","steward","scribe","warden","snitch","herald","scroll","systembot","auditbot"]'

instruction=$(echo "$messages" | jq -r \
  --argjson bots "$BOTS" --arg self "$SCUTTLEBOT_NICK" '
  .messages[]
  | select(.nick as $n |
      ($bots | index($n) | not) and
      ($n | startswith("claude-") | not) and
      ($n | startswith("codex-") | not) and
      ($n | startswith("gemini-") | not) and
      $n != $self)
  | "\(.at)\t\(.nick)\t\(.text)"
' 2>/dev/null | while IFS=$'\t' read -r at nick text; do
    # ... timestamp comparison, mention check ...
    echo "$nick: $text"
  done | tail -1)

[ -z "$instruction" ] && exit 0
echo "{\"decision\": \"block\", \"reason\": \"[IRC instruction from operator] $instruction\"}"
```

### Post-action hook (`scuttlebot-post.sh`)

Runs after each tool call. Posts a one-line summary to IRC.

Key points:

- Skip if `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` — the broker already owns activity posting
- Skip if `SCUTTLEBOT_HOOKS_ENABLED=0` or token is empty
- Parse the tool name and key input from stdin JSON
- Build a short human-readable summary (under 120 chars)
- `POST /v1/channels/{ch}/messages` with `connect-timeout 1 max-time 2`
- Exit 0 always (never block the tool)

Example summaries by tool:

| Tool | Summary format |
|------|---------------|
| `Bash` | `› {command[:120]}` |
| `Read` | `read {relative-path}` |
| `Edit` | `edit {relative-path}` |
| `Write` | `write {relative-path}` |
| `Glob` | `glob {pattern}` |
| `Grep` | `grep "{pattern}"` |
| `Agent` | `spawn agent: {description[:80]}` |
| Other | `{tool_name}` |

---

## The smoke test checklist

Every adapter must pass this test before it is considered complete:

1. **Online presence** — launch the runtime or broker; confirm `{nick} online` appears in the IRC channel within a few seconds
2. **Tool activity mirror** — trigger one harmless tool call (e.g. list files); confirm a mirrored one-liner appears in the channel
3. **Operator inject** — from an IRC client, send a message mentioning the session nick (e.g. `claude-myrepo-abc12345: please stop`); confirm the runtime surfaces it as a blocking instruction or injects it into stdin
4. **Offline presence** — exit the runtime; confirm `{nick} offline` appears in the channel
5. **Soft-fail** — stop scuttlebot and launch the runtime; confirm it starts normally and the relay exits gracefully

If any of these fail, the adapter is not finished.

---

## Common mistakes

### Duplicate activity posts

If the broker mirrors the session log AND the post-hook fires for the same tool call, operators see every action twice.

**Fix:** Set `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` in the env file when the broker is active. The post-hook checks this variable and exits early:

```bash
[ "${SCUTTLEBOT_ACTIVITY_VIA_BROKER:-0}" = "1" ] && exit 0
```

### Parallel session interference

If two sessions in the same repo and channel use a single shared `lastCheck` timestamp file, one session will consume instructions meant for the other.

**Fix:** Key the state file by `channel + nick + cwd` (see "State scoping" above). Each session gets its own file under `/tmp/`.

### Secrets in activity output

Session logs may contain tokens, passphrases, or API keys in command output or assistant text. Posting these to IRC leaks them to everyone in the channel.

**Fix:** Always run the scrubber on any line before posting. Redact: long hex strings (`[a-f0-9]{32,}`), `sk-*` key patterns, `Bearer <token>` patterns, and `VAR=value` assignments for names containing `TOKEN`, `KEY`, `SECRET`, or `PASSPHRASE`.

### Missing word-boundary check for mentions

A check like `echo "$text" | grep -q "$nick"` will match `claude-myrepo-abc12345` inside `re-claude-myrepo-abc12345d` or as part of a URL. Use the word-boundary regex from the filtering rules section.

### Blocking the tool loop

The pre-action hook runs synchronously before every tool call. If it hangs (e.g. scuttlebot is slow or unreachable), it delays every action indefinitely.

**Fix:** Always use `--connect-timeout 1 --max-time 2` in curl calls. Exit 0 immediately on any curl error. The relay is a best-effort observer — it must never impede the runtime.
