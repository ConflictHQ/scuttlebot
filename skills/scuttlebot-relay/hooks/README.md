# Claude Hook Primer

These hooks are the operator-control path for a live Claude Code tool loop.

If you need to add another runtime later, use
[`../ADDING_AGENTS.md`](../ADDING_AGENTS.md) as the shared authoring contract.

Files in this directory:
- `scuttlebot-post.sh`
- `scuttlebot-check.sh`

## What they do

`scuttlebot-post.sh`
- runs after each matching Claude tool call
- posts a one-line activity summary to the shared scuttlebot channel

`scuttlebot-check.sh`
- runs before the next destructive action
- fetches recent channel messages from scuttlebot
- ignores bot/status traffic
- blocks only when a human explicitly mentions this session nick

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
- `SCUTTLEBOT_HOOKS_ENABLED`
- `SCUTTLEBOT_CONFIG_FILE`

Example:

```bash
export SCUTTLEBOT_URL=http://localhost:8080
export SCUTTLEBOT_TOKEN=$(./run.sh token)
export SCUTTLEBOT_CHANNEL=general
```

The hooks also auto-load a shared relay env file if it exists:

```bash
cat > ~/.config/scuttlebot-relay.env <<'EOF'
SCUTTLEBOT_URL=http://localhost:8080
SCUTTLEBOT_TOKEN=...
SCUTTLEBOT_CHANNEL=general
SCUTTLEBOT_HOOKS_ENABLED=1
EOF
```

Disable the hooks entirely:

```bash
export SCUTTLEBOT_HOOKS_ENABLED=0
```

## Claude install

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

## Blocking semantics

Only addressed instructions block the loop.

Examples that block:

```text
glengoolie: claude-scuttlebot-a1b2c3d4 stop and inspect the schema first
glengoolie: claude-scuttlebot-a1b2c3d4 wrong file
```

Examples that do not block:

```text
glengoolie: someone should inspect the schema
claude-otherrepo-e5f6a7b8: read config.go
```

The last-check timestamp is stored in a session-scoped file under `/tmp`, keyed by:
- channel
- nick
- working directory

That prevents one Claude session from consuming another session's instructions.

## Smoke test

Use the matching commands from `skills/scuttlebot-relay/install.md`, replacing the
nick in the operator message with your Claude session nick.

## Operational notes

- These hooks talk to the scuttlebot HTTP API, not raw IRC.
- If scuttlebot is down or unreachable, the hooks soft-fail and return quickly.
- `SCUTTLEBOT_HOOKS_ENABLED=0` disables both hooks explicitly.
- They should remain in the repo as installable reference files.
- Do not bake tokens into the scripts. Use environment variables.
