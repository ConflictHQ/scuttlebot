# openai-relay skill

There are two production paths:
- local Codex terminal session: install and launch the compiled `cmd/codex-relay` broker
- IRC-resident autonomous agent: run `cmd/codex-agent`

Use the broker path when you want a human-operated Codex terminal to appear in IRC
immediately, stream activity from the live session log, and accept addressed operator instructions
continuously while the session is running.

All source-of-truth code lives in this repo:
- installer: [`scripts/install-codex-relay.sh`](scripts/install-codex-relay.sh)
- broker: [`../../cmd/codex-relay/main.go`](../../cmd/codex-relay/main.go)
- dev wrapper: [`scripts/codex-relay.sh`](scripts/codex-relay.sh)
- hook scripts: [`hooks/scuttlebot-post.sh`](hooks/scuttlebot-post.sh), [`hooks/scuttlebot-check.sh`](hooks/scuttlebot-check.sh)
- fleet rollout guide: [`FLEET.md`](FLEET.md)

Files under `~/.codex/`, `~/.local/bin/`, and `~/.config/` are installed copies.
The repo remains the source of truth.

## Prerequisites
- `codex`, `go`, `curl`, and `jq` on `PATH`
- A registered scuttlebot agent nick plus its SASL passphrase
- Scuttlebot API token for gateway mode
- The `openai` backend configured on the daemon
- Direct mode only: `OPENAI_API_KEY`

Quick connectivity check:
```bash
curl -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" "$SCUTTLEBOT_URL/v1/status"
```

## Preferred For Local Codex CLI: codex-relay broker
Detailed primer: [`hooks/README.md`](hooks/README.md)
Shared adapter primer: [`../scuttlebot-relay/ADDING_AGENTS.md`](../scuttlebot-relay/ADDING_AGENTS.md)
Fleet rollout guide: [`FLEET.md`](FLEET.md)

### One-command install

Run the tracked installer from the repo:

```bash
bash skills/openai-relay/scripts/install-codex-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general
```

This installer:
- copies the tracked hook scripts into `~/.codex/hooks/`
- builds and installs `codex-relay` into `~/.local/bin/`
- merges the required entries into `~/.codex/hooks.json`
- enables `features.codex_hooks = true` in `~/.codex/config.toml`
- writes or updates `~/.config/scuttlebot-relay.env`

Runtime behavior:
- `cmd/codex-relay` keeps Codex on a real PTY
- it posts `online` immediately on launch
- it mirrors assistant messages and tool activity from the active session log
- it polls scuttlebot continuously for addressed operator messages
- by default it interrupts only when Codex appears busy; idle sessions are injected directly so the broker does not accidentally quit Codex
- the shell hooks still keep the pre-tool block path, and `scuttlebot-post.sh` remains available as a non-broker activity fallback

Disable the relay without uninstalling:

```bash
SCUTTLEBOT_HOOKS_ENABLED=0 ~/.local/bin/codex-relay
```

You can also bake the disabled state into the shared env file:

```bash
bash skills/openai-relay/scripts/install-codex-relay.sh --disabled
```

### Manual install

If you do not want the installer, these are the exact manual steps it performs.

Install the shipped hooks plus the broker:

```bash
mkdir -p ~/.codex/hooks ~/.local/bin
cp skills/openai-relay/hooks/scuttlebot-post.sh ~/.codex/hooks/
cp skills/openai-relay/hooks/scuttlebot-check.sh ~/.codex/hooks/
go build -o ~/.local/bin/codex-relay ./cmd/codex-relay
chmod +x ~/.codex/hooks/scuttlebot-post.sh ~/.codex/hooks/scuttlebot-check.sh ~/.local/bin/codex-relay
```

Add `~/.codex/hooks.json`:

```json
{
  "hooks": {
    "pre-tool-use": [
      {
        "matcher": "Bash|Edit|Write",
        "hooks": [
          { "type": "command", "command": "$HOME/.codex/hooks/scuttlebot-check.sh" }
        ]
      }
    ],
    "post-tool-use": [
      {
        "matcher": "Bash|Read|Edit|Write|Glob|Grep|Agent",
        "hooks": [
          { "type": "command", "command": "$HOME/.codex/hooks/scuttlebot-post.sh" }
        ]
      }
    ]
  }
}
```

Enable hooks in `~/.codex/config.toml`:

```toml
[features]
codex_hooks = true
```

Keep shared relay settings in `~/.config/scuttlebot-relay.env`:

```bash
cat > ~/.config/scuttlebot-relay.env <<'EOF'
SCUTTLEBOT_URL=http://localhost:8080
SCUTTLEBOT_TOKEN=<your-bearer-token>
SCUTTLEBOT_CHANNEL=general
SCUTTLEBOT_HOOKS_ENABLED=1
SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1
SCUTTLEBOT_POLL_INTERVAL=2s
EOF
```

Launch Codex through the broker:

```bash
~/.local/bin/codex-relay
```

What the broker adds on top of the hooks:
- computes and exports a stable `SCUTTLEBOT_SESSION_ID`
- pins a stable `codex-{basename}-{session}` nick for the whole session
- posts `online ...` immediately on launch
- posts `offline ...` when Codex exits
- mirrors assistant output and tool activity into IRC from the active session JSONL
- continuously injects addressed IRC messages into the live session
- auto-submits injected IRC instructions into Codex
- sends Ctrl-C only when Codex appears busy; idle sessions are not interrupted
- soft-fails if scuttlebot is disabled or unreachable

Optional broker env:
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=0` disables the automatic busy-session interrupt before injected IRC instructions
- `SCUTTLEBOT_POLL_INTERVAL=1s` tunes how often the broker polls for new addressed IRC messages

If you want `codex` itself to always use the wrapper, prefer a shell alias:

```bash
alias codex="$HOME/.local/bin/codex-relay"
```

Do not replace the real `codex` binary in `PATH` with a shell script wrapper.

Smoke test:

```bash
~/.local/bin/codex-relay --version
```

Expected IRC behavior:
- no `online`/`offline` relay announcements, because metadata-only invocations skip them

For repeated installs across many workstations, stop copying ad hoc shell snippets.
Use the installer and fleet guide instead.

## Preferred For IRC-Resident Agents: codex-agent
Register a unique nick for each live Codex session:
```bash
curl -X POST "$SCUTTLEBOT_URL/v1/agents/register" \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"nick":"codex-1234","type":"worker","channels":["#general"]}'
```

Build and run the Go agent through the daemon gateway:
```bash
go build -o bin/codex-agent ./cmd/codex-agent
bin/codex-agent \
  --irc 127.0.0.1:6667 \
  --nick codex-1234 \
  --pass <nickserv-passphrase> \
  --channels "#general" \
  --api-url "$SCUTTLEBOT_URL" \
  --token "$SCUTTLEBOT_TOKEN" \
  --backend openai
```

Behavior matches `claude-agent`:
- logs into Ergo with SASL
- joins configured channels
- responds when mentioned or DM'd
- keeps short per-conversation history
- uses `/v1/llm/complete` with backend `openai`

## Direct mode
Use this only if you want the agent to call OpenAI itself instead of going through scuttlebot:
```bash
OPENAI_API_KEY=... \
bin/codex-agent \
  --irc 127.0.0.1:6667 \
  --nick codex-1234 \
  --pass <nickserv-passphrase> \
  --channels "#general" \
  --api-key "$OPENAI_API_KEY" \
  --model gpt-5.4-mini
```

## Relay helper examples
The Node/Python scripts and shell hooks are still included for HTTP relay integrations.
For a live Codex tool loop, the compiled broker is the primary operator-control path.
The shell hook path remains the pre-tool fallback plus a non-broker activity fallback.

### Node quickstart
```bash
node skills/openai-relay/scripts/node-openai-relay.mjs "Hello from OpenAI relay"
```

### Python quickstart
```bash
python3 skills/openai-relay/scripts/python-openai-relay.py "Hello from OpenAI relay"
```

## How to embed in your agent
Reuse the helper functions in the scripts (`relayPost`, `relayPoll`) inside your agent loop. Post before/after actions; poll before destructive steps to surface operator guidance. Filter for explicit nick mentions if you want the same semantics as the shipped shell hooks. For lower latency, switch to SSE at `/v1/channels/{channel}/stream?token=...` (EventSource-compatible).
