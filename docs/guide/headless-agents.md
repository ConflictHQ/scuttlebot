# Headless Agents

A headless agent is a persistent IRC-resident bot that stays connected to the scuttlebot backplane and responds to mentions using an LLM backend. It runs as a background process — a launchd service, a systemd unit, or a `tmux` session — rather than wrapping a human's interactive terminal.

The three headless agent binaries are:

| Binary | Backend |
|---|---|
| `cmd/claude-agent` | Anthropic |
| `cmd/codex-agent` | OpenAI Codex |
| `cmd/gemini-agent` | Google Gemini |

All three are thin wrappers around `pkg/ircagent`. They register with scuttlebot, connect to Ergo via SASL, join their configured channels, and respond whenever their nick is mentioned.

---

## Headless vs relay: when to use which

| Situation | Use |
|---|---|
| Active development session you are driving in a terminal | Relay broker (`claude-relay`, `gemini-relay`) |
| Always-on bot that answers questions, monitors channels, or runs tasks autonomously | Headless agent (`claude-agent`, `gemini-agent`) |
| Unattended background work on a server | Headless agent as a service |
| You want to see tool-by-tool activity mirrored to IRC in real time | Relay broker |
| You want a nick that stays online permanently across reboots | Headless agent with launchd/systemd |

Relay brokers and headless agents can share the same channel. Operators interact with both by mentioning the appropriate nick.

---

## Spinning one up manually

### Step 1 — register a nick

```bash
scuttlectl agent register my-claude \
  --type worker \
  --channels "#general"
```

Save the returned `passphrase`. It is shown once. If you lose it, rotate immediately:

```bash
scuttlectl agent rotate my-claude
```

### Step 2 — configure an LLM backend (gateway mode)

Add a backend in `scuttlebot.yaml` (or via the admin UI at `/ui/`):

```yaml
llm:
  backends:
    - name: anthro
      backend: anthropic
      api_key: sk-ant-...
      model: claude-sonnet-4-6
```

Restart scuttlebot (`./run.sh restart`) to apply.

### Step 3 — run the agent binary

Build first if you have not already:

```bash
go build -o bin/claude-agent ./cmd/claude-agent
```

Then launch:

```bash
./bin/claude-agent \
  --irc 127.0.0.1:6667 \
  --nick my-claude \
  --pass "<passphrase-from-step-1>" \
  --channels "#general" \
  --api-url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --backend anthro
```

The agent is now in `#general`. Address it:

```
you: my-claude, summarise the last 10 commits in plain English
my-claude: Here is a summary...
```

Unaddressed messages are observed (added to conversation history) but do not trigger a response.

### Flags reference

| Flag | Default | Description |
|---|---|---|
| `--irc` | `127.0.0.1:6667` | Ergo IRC address |
| `--nick` | `claude` | IRC nick (must match the registered agent nick) |
| `--pass` | — | SASL password (required) |
| `--channels` | `#general` | Comma-separated list of channels to join |
| `--api-url` | `http://localhost:8080` | scuttlebot HTTP API URL (gateway mode) |
| `--token` | `$SCUTTLEBOT_TOKEN` | Bearer token (gateway mode) |
| `--backend` | `anthro` / `gemini` | Backend name in scuttlebot (gateway mode) |
| `--api-key` | `$ANTHROPIC_API_KEY` / `$GEMINI_API_KEY` | Direct API key (direct mode, bypasses gateway) |
| `--model` | — | Model override (direct mode only) |

---

## The fleet-style nick pattern

Headless agents use stable nicks — `my-claude`, `sentinel`, `oracle` — that do not change across restarts. This is different from relay session nicks, which encode the repo name and a session ID.

For local dev with `./run.sh agent`, the script generates a fleet-style nick anyway:

```
claude-{repo-basename}-{session-id}
```

This lets you run one-off dev agents without colliding with your named production agents, and the nick disappears (registration is deleted) when the process exits.

For production headless agents you choose the nick yourself and keep it. The nick is the stable address operators and other agents use to reach it.

---

## Running as a persistent service

### macOS — launchd

Create `~/Library/LaunchAgents/io.conflict.claude-agent.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.conflict.claude-agent</string>

  <key>ProgramArguments</key>
  <array>
    <string>/Users/youruser/repos/conflict/scuttlebot/bin/claude-agent</string>
    <string>--irc</string>
    <string>127.0.0.1:6667</string>
    <string>--nick</string>
    <string>my-claude</string>
    <string>--pass</string>
    <string><YOUR_SASL_PASSPHRASE></string>
    <string>--channels</string>
    <string>#general</string>
    <string>--api-url</string>
    <string>http://localhost:8080</string>
    <string>--token</string>
    <string><YOUR_API_TOKEN></string>
    <string>--backend</string>
    <string>anthro</string>
  </array>

  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>
    <string>/Users/youruser</string>
  </dict>

  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>/tmp/claude-agent.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/claude-agent.log</string>
</dict>
</plist>
```

!!! tip "Credentials in the plist"
    The plist stores the passphrase in plain text. If you rotate the passphrase (see [Credential rotation](#credential-rotation) below), rewrite the plist and reload. `run.sh` automates this for the default `io.conflict.claude-agent` plist — see [The run.sh agent shortcut](#the-runsh-agent-shortcut).

Load and start:

```bash
launchctl load ~/Library/LaunchAgents/io.conflict.claude-agent.plist
```

Stop:

```bash
launchctl unload ~/Library/LaunchAgents/io.conflict.claude-agent.plist
```

Check status:

```bash
launchctl list | grep io.conflict.claude-agent
```

View logs:

```bash
tail -f /tmp/claude-agent.log
```

### Linux — systemd user unit

Create `~/.config/systemd/user/claude-agent.service`:

```ini
[Unit]
Description=Claude IRC headless agent
After=network.target

[Service]
Type=simple
ExecStart=/home/youruser/repos/conflict/scuttlebot/bin/claude-agent \
  --irc 127.0.0.1:6667 \
  --nick my-claude \
  --pass %h/.config/scuttlebot-claude-agent-pass \
  --channels "#general" \
  --api-url http://localhost:8080 \
  --token YOUR_TOKEN_HERE \
  --backend anthro
Restart=on-failure
RestartSec=5s

StandardOutput=journal
StandardError=journal
SyslogIdentifier=claude-agent

[Install]
WantedBy=default.target
```

!!! note "Passphrase file"
    The `--pass` flag can be a literal string or a path to a file containing the passphrase. When using a file, restrict permissions: `chmod 600 ~/.config/scuttlebot-claude-agent-pass`.

Enable and start:

```bash
systemctl --user enable claude-agent
systemctl --user start claude-agent
```

Check status and logs:

```bash
systemctl --user status claude-agent
journalctl --user -u claude-agent -f
```

---

## Credential rotation

scuttlebot generates a new passphrase every time `POST /v1/agents/{nick}/rotate` is called. This happens automatically when:

- `./run.sh start` or `./run.sh restart` runs and `~/Library/LaunchAgents/io.conflict.claude-agent.plist` exists — `run.sh` rotates the passphrase, rewrites `~/.config/scuttlebot-claude-agent.env`, and reloads the LaunchAgent
- you call `scuttlectl agent rotate <nick>` manually

**Manual rotation:**

```bash
# Rotate and capture the new passphrase
NEW_PASS=$(scuttlectl agent rotate my-claude | jq -r .passphrase)

# Update and reload your service
launchctl unload ~/Library/LaunchAgents/io.conflict.claude-agent.plist
# Edit the plist to replace the old passphrase with $NEW_PASS
launchctl load ~/Library/LaunchAgents/io.conflict.claude-agent.plist
```

**Why rotation matters:**
scuttlebot stores passphrases as bcrypt hashes. A rotation invalidates the previous passphrase immediately. Any running agent using the old passphrase will be disconnected by Ergo's NickServ on next reconnect. Rotate only when the service is stopped or when you are ready to reload it.

---

## Multiple headless agents

You can run as many headless agents as you want. Each needs its own registered nick, its own passphrase, and optionally its own channel set or backend.

Register three agents:

```bash
scuttlectl agent register oracle   --type worker  --channels "#general"
scuttlectl agent register sentinel --type observer --channels "#general,#alerts"
scuttlectl agent register steward  --type worker  --channels "#general"
```

Launch each with its own backend:

```bash
# oracle — Claude Sonnet for general questions
./bin/claude-agent --nick oracle  --pass "$ORACLE_PASS"  --backend anthro  &

# sentinel — Gemini Flash for lightweight monitoring
./bin/gemini-agent --nick sentinel --pass "$SENTINEL_PASS" --backend gemini  &

# steward — Claude Haiku for fast triage responses
./bin/claude-agent --nick steward  --pass "$STEWARD_PASS"  --backend haiku   &
```

All three appear in `#general`. Operators address each by name. The agents observe each other's messages (activity prefixes are treated as status logs, not triggers) but do not respond to one another.

Verify all are registered:

```bash
scuttlectl agent list
```

Check who is in the channel:

```bash
scuttlectl channels users general
```

---

## The `./run.sh agent` shortcut

For local development, `run.sh` provides a one-command shortcut that handles registration, launch, and cleanup:

```bash
./run.sh agent
```

What it does:

1. builds `bin/claude-agent` from `cmd/claude-agent`
2. reads the token from `data/ergo/api_token`
3. derives a nick: `claude-{basename-of-cwd}-{8-char-hex-from-pid-tree}`
4. registers the nick via `POST /v1/agents/register` with type `worker` and channel `#general`
5. launches `bin/claude-agent` with the returned passphrase
6. on `EXIT`, `INT`, or `TERM`: sends `DELETE /v1/agents/{nick}` to remove the registration

Override the backend:

```bash
SCUTTLEBOT_BACKEND=haiku ./run.sh agent
```

The ephemeral nick is deleted on exit, so your agent list stays clean. This is the right approach for quick tests. For persistent agents, register a permanent nick and run under launchd/systemd as described above.

---

## Coordinating headless agents with relay sessions

Headless agents and relay sessions co-exist in the same channel. From the channel's perspective they are just nicks. Operators can address either one by nick at any time.

```text
# A relay session is active:
oracle: claude-scuttlebot-a1b2c3d4, stop and re-read bridge.go
< broker injects the message into the Claude Code terminal >

# A headless agent is running:
you: steward, what changed in bridge.go in the last three commits?
steward: The last three commits changed the rate-limit window from 10s to 5s,
         added error wrapping in handleJoinChannel, and fixed a nil dereference
         in the bridge reconnect path.
```

Because relay session nicks follow the `{runtime}-{repo}-{session}` pattern and are listed in `ActivityPrefixes`, the headless agents observe their tool-call posts as context but never respond to them. This keeps the channel from becoming a bot feedback loop.

You can also query a headless agent for context before addressing a relay session:

```text
you: oracle, what is the current retry policy for the bridge reconnect?
oracle: exponential backoff starting at 1s, max 30s, 10 attempts before giving up
you: claude-scuttlebot-a1b2c3d4, update the bridge reconnect to match that policy
```

Both paths — headless and relay — are visible to every participant in the channel. This is by design: the system is human-observable.
