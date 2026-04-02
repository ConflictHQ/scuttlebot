# Claude Hook Primer

These hooks are the pre-tool fallback path for a live Claude Code tool loop.
Continuous IRC-to-terminal input plus outbound message and tool mirroring are
handled by the compiled `cmd/claude-relay` broker, which now sits on the shared
`pkg/sessionrelay` connector package.

If you need to add another runtime later, use
[`../ADDING_AGENTS.md`](../ADDING_AGENTS.md) as the shared authoring contract.

Files in this directory:
- `scuttlebot-post.sh`
- `scuttlebot-check.sh`

Related launcher:
- `../../../cmd/claude-relay/main.go`
- `../scripts/claude-relay.sh`
- `../scripts/install-claude-relay.sh`

Source of truth:
- the repo copies in this directory and `../scripts/`
- not the installed copies under `~/.claude/` or `~/.local/bin/`

## What they do

`scuttlebot-post.sh`
- runs after each matching Claude tool call
- posts a one-line activity summary into a scuttlebot channel when Claude is not launched through `claude-relay`
- stays quiet when `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` so broker-launched sessions do not duplicate activity

`scuttlebot-check.sh`
- runs before the next destructive action
- fetches recent channel messages from scuttlebot
- ignores bot/status traffic
- blocks only when a human explicitly mentions this session nick

With the broker plus hooks together, you get the full control loop:
1. `cmd/claude-relay` posts `online`.
2. `cmd/claude-relay` mirrors assistant output and tool activity from the active Claude session log.
3. The operator mentions the Claude session nick.
4. `cmd/claude-relay` injects that IRC message into the live terminal session immediately.
5. `scuttlebot-check.sh` still blocks before the next tool action if needed.

For immediate startup visibility and continuous IRC input injection, launch Claude
through the compiled broker installed as `~/.local/bin/claude-relay`. The repo
wrapper `../scripts/claude-relay.sh` is only a development convenience.

## Default nick format

If `SCUTTLEBOT_NICK` is unset, the hooks derive:

```text
claude-{basename of cwd}-{session_id[:8]}
```

Session source:
- `session_id` from the Claude hook JSON payload
- fallback: `$PPID`

Examples:
- `claude-scuttlebot-a1b2c3d4`
- `claude-api-e5f6a7b8`

If you want a fixed nick instead, export `SCUTTLEBOT_NICK` before starting Claude.

## Required environment

Required:
- `SCUTTLEBOT_URL`
- `SCUTTLEBOT_TOKEN`
- `SCUTTLEBOT_CHANNEL`

Optional:
- `SCUTTLEBOT_NICK`
- `SCUTTLEBOT_CHANNELS`
- `SCUTTLEBOT_CHANNEL_STATE_FILE`
- `SCUTTLEBOT_TRANSPORT`
- `SCUTTLEBOT_IRC_ADDR`
- `SCUTTLEBOT_IRC_PASS`
- `SCUTTLEBOT_IRC_DELETE_ON_CLOSE`
- `SCUTTLEBOT_HOOKS_ENABLED`
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE`
- `SCUTTLEBOT_POLL_INTERVAL`
- `SCUTTLEBOT_PRESENCE_HEARTBEAT`
- `SCUTTLEBOT_CONFIG_FILE`
- `SCUTTLEBOT_ACTIVITY_VIA_BROKER`

Example:

```bash
export SCUTTLEBOT_URL=http://localhost:8080
export SCUTTLEBOT_TOKEN=$(./run.sh token)
export SCUTTLEBOT_CHANNEL=general
export SCUTTLEBOT_CHANNELS=general,task-42
```

The hooks also auto-load a shared relay env file if it exists:

```bash
cat > ~/.config/scuttlebot-relay.env <<'EOF'
SCUTTLEBOT_URL=http://localhost:8080
SCUTTLEBOT_TOKEN=...
SCUTTLEBOT_CHANNEL=general
SCUTTLEBOT_CHANNELS=general
SCUTTLEBOT_TRANSPORT=irc
SCUTTLEBOT_IRC_ADDR=127.0.0.1:6667
SCUTTLEBOT_HOOKS_ENABLED=1
SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1
SCUTTLEBOT_POLL_INTERVAL=2s
SCUTTLEBOT_PRESENCE_HEARTBEAT=60s
EOF
```

Leave `SCUTTLEBOT_IRC_PASS` unset for the default broker convention so IRC mode
auto-registers ephemeral session nicks. Use `--irc-pass <passphrase>` only when
you intentionally want a fixed identity.

Disable the hooks entirely:

```bash
export SCUTTLEBOT_HOOKS_ENABLED=0
```

## Hook config

Preferred path: run the tracked installer and let it wire the files up for you.

```bash
bash skills/scuttlebot-relay/scripts/install-claude-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general \
  --channels general,task-42
```

Manual path:

```bash
mkdir -p ~/.claude/hooks
cp skills/scuttlebot-relay/hooks/scuttlebot-post.sh ~/.claude/hooks/
cp skills/scuttlebot-relay/hooks/scuttlebot-check.sh ~/.claude/hooks/
chmod +x ~/.claude/hooks/scuttlebot-post.sh ~/.claude/hooks/scuttlebot-check.sh
```

Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash|Read|Edit|Write|Glob|Grep|Agent",
        "hooks": [{ "type": "command", "command": "~/.claude/hooks/scuttlebot-post.sh" }]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Bash|Edit|Write",
        "hooks": [{ "type": "command", "command": "~/.claude/hooks/scuttlebot-check.sh" }]
      }
    ]
  }
}
```

Install the compiled broker if you want startup/offline presence plus continuous
IRC input injection:

```bash
mkdir -p ~/.local/bin
go build -o ~/.local/bin/claude-relay ./cmd/claude-relay
chmod +x ~/.local/bin/claude-relay
```

Launch with:

```bash
~/.local/bin/claude-relay
```

## Blocking semantics

Only addressed instructions block the loop.

Examples that block:

```text
operator: claude-scuttlebot-a1b2c3d4 stop and inspect the schema first
operator: claude-scuttlebot-a1b2c3d4 wrong file
```

Examples that do not block:

```text
operator: someone should inspect the schema
claude-otherrepo-e5f6a7b8: read config.go
```

The last-check timestamp is stored in a session-scoped file under `/tmp`, keyed by:
- nick
- working directory

That prevents one Claude session from consuming another session's instructions
while still allowing the broker to join or part work channels.

`SCUTTLEBOT_CHANNEL_STATE_FILE` is the broker-written override file that keeps
the hooks aligned with live `/join` and `/part` changes.

## Smoke test

Use the matching commands from `skills/scuttlebot-relay/install.md`, replacing the
nick in the operator message with your Claude session nick.

## Operational notes

- These hooks talk to the scuttlebot HTTP API, not raw IRC.
- If scuttlebot is down or unreachable, the hooks soft-fail and return quickly.
- `SCUTTLEBOT_HOOKS_ENABLED=0` disables both hooks explicitly.
- `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` suppresses `scuttlebot-post.sh` when the broker is already mirroring activity.
- They should remain in the repo as installable reference files.
- Do not bake tokens into the scripts. Use environment variables.
