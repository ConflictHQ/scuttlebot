# Codex Hook Primer

These hooks are the pre-tool fallback path for a live Codex tool loop.
Continuous IRC-to-terminal input plus outbound message and tool mirroring are
handled by the compiled `cmd/codex-relay` broker, which now sits on the shared
`pkg/sessionrelay` connector package.

If you need to add another runtime later, use
[`../../scuttlebot-relay/ADDING_AGENTS.md`](../../scuttlebot-relay/ADDING_AGENTS.md)
as the shared authoring contract.

Files in this directory:
- `scuttlebot-post.sh`
- `scuttlebot-check.sh`

Related launcher:
- `../../../cmd/codex-relay/main.go`
- `../scripts/codex-relay.sh`
- `../scripts/install-codex-relay.sh`

Source of truth:
- the repo copies in this directory and `../scripts/`
- not the installed copies under `~/.codex/` or `~/.local/bin/`

## What they do

`scuttlebot-post.sh`
- runs after each tool call
- posts a one-line activity summary into a scuttlebot channel when Codex is not launched through `codex-relay`
- uses the session nick as the IRC/web bridge sender nick

`scuttlebot-check.sh`
- runs before the next action
- fetches recent channel messages from scuttlebot
- ignores bots and agent status nicks
- blocks only when a human explicitly mentions this session nick
- prints a JSON decision block that Codex can surface into the live tool loop

With the broker plus hooks together, you get the full control loop:
1. `cmd/codex-relay` posts `online`.
2. `cmd/codex-relay` mirrors assistant output and tool activity from the active session log.
3. The operator mentions the Codex session nick.
4. `cmd/codex-relay` injects that IRC message into the live terminal session immediately.
5. `scuttlebot-check.sh` still blocks before the next tool action if needed.

For immediate startup visibility and continuous IRC input injection, launch Codex
through the compiled broker installed as `~/.local/bin/codex-relay`. The repo
wrapper `../scripts/codex-relay.sh` is only a development convenience.

## Default nick format

If `SCUTTLEBOT_NICK` is unset, the hooks derive a stable session nick:

```text
codex-{basename of cwd}-{session id}
```

Session id resolution order:
1. `SCUTTLEBOT_SESSION_ID`
2. `CODEX_SESSION_ID`
3. parent process id (`PPID`)

Examples:
- `codex-scuttlebot-8421`
- `codex-calliope-qa`

This is deliberate. Multiple Codex sessions in the same repo must not collide.

## Required environment

Required:
- `SCUTTLEBOT_URL`
- `SCUTTLEBOT_TOKEN`
- `SCUTTLEBOT_CHANNEL`
- `curl` and `jq` available on `PATH`

Optional:
- `SCUTTLEBOT_NICK`
- `SCUTTLEBOT_SESSION_ID`
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
SCUTTLEBOT_TRANSPORT=http
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
bash skills/openai-relay/scripts/install-codex-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general \
  --channels general,task-42
```

Manual path:

Install the scripts:

```bash
mkdir -p ~/.codex/hooks
cp skills/openai-relay/hooks/scuttlebot-post.sh ~/.codex/hooks/
cp skills/openai-relay/hooks/scuttlebot-check.sh ~/.codex/hooks/
chmod +x ~/.codex/hooks/scuttlebot-post.sh ~/.codex/hooks/scuttlebot-check.sh
```

Configure native Codex hooks in `~/.codex/hooks.json`:

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

Enable the feature in `~/.codex/config.toml`:

```toml
[features]
codex_hooks = true
```

Install the compiled broker if you want startup/offline presence plus continuous
IRC input injection:

```bash
mkdir -p ~/.local/bin
go build -o ~/.local/bin/codex-relay ./cmd/codex-relay
chmod +x ~/.local/bin/codex-relay
```

Launch with:

```bash
~/.local/bin/codex-relay
```

Optional shell alias:

```bash
alias codex="$HOME/.local/bin/codex-relay"
```

Do not replace the real `codex` binary in `PATH` with a shell wrapper.

## Message filtering semantics

The check hook only surfaces messages that satisfy all of the following:
- newer than the last check for this session
- not posted by this session nick
- not posted by known service bots
- not posted by `claude-*`, `codex-*`, or `gemini-*` status nicks
- explicitly mention this session nick

This is the critical fallback behavior. Ambient channel chat must not halt a live tool loop.

Examples that block:

```text
operator: codex-scuttlebot-8421 stop and re-read the schema
operator: codex-scuttlebot-8421 wrong file, look at internal/api first
```

Examples that do not block:

```text
operator: can someone check the schema
codex-otherrepo-7712: read internal/config/config.go
bridge: [operator] hello
```

## Per-session state

The check hook stores its last-seen timestamp in:

```text
/tmp/.scuttlebot-last-check-{checksum}
```

The checksum is derived from:
- session nick
- current working directory

Live channel changes come from `SCUTTLEBOT_CHANNEL_STATE_FILE`, which the broker
rewrites as `/join` and `/part` commands change the current session channel set.

That avoids one session consuming another session's instructions.

## Smoke test

Launcher smoke test:

```bash
~/.local/bin/codex-relay --version
```

Expected IRC behavior:
- no relay announcements, because metadata-only invocations skip them

Hook smoke test:

Post a synthetic activity event:

```bash
printf '{"tool_name":"Read","cwd":"%s","tool_input":{"file_path":"%s/README.md"}}\n' "$PWD" "$PWD" \
  | SCUTTLEBOT_URL=http://localhost:8080 \
    SCUTTLEBOT_TOKEN="$(./run.sh token)" \
    SCUTTLEBOT_CHANNEL=general \
    SCUTTLEBOT_SESSION_ID=smoke \
    bash skills/openai-relay/hooks/scuttlebot-post.sh
```

Then mention the expected nick from the operator side:

```bash
curl -sf -X POST http://localhost:8080/v1/channels/general/messages \
  -H "Authorization: Bearer $(./run.sh token)" \
  -H "Content-Type: application/json" \
  -d '{"nick":"<your-operator-nick>","text":"codex-scuttlebot-smoke stop and check the bridge TTL"}'
```

Run the check hook:

```bash
SCUTTLEBOT_URL=http://localhost:8080 \
SCUTTLEBOT_TOKEN="$(./run.sh token)" \
SCUTTLEBOT_CHANNEL=general \
SCUTTLEBOT_SESSION_ID=smoke \
bash skills/openai-relay/hooks/scuttlebot-check.sh
```

Expected output:

```json
{"decision":"block","reason":"[IRC instruction from operator] <your-operator-nick>: codex-scuttlebot-smoke stop and check the bridge TTL"}
```

## Operational notes

- `cmd/codex-relay` continuously polls for addressed IRC messages and injects them into the live Codex PTY.
- `cmd/codex-relay` can do that over either the HTTP bridge API or a real IRC socket.
- `cmd/codex-relay` also tails the active session JSONL and mirrors assistant output plus tool activity into IRC.
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=0` disables the automatic busy-session interrupt before injected IRC instructions.
- With the default `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1`, the broker only sends Ctrl-C when Codex appears busy. Idle sessions are injected directly and auto-submitted so the broker does not accidentally quit Codex at the prompt.
- `SCUTTLEBOT_POLL_INTERVAL=1s` changes the broker poll interval.
- `SCUTTLEBOT_TRANSPORT=irc` gives the live session a true IRC presence; `SCUTTLEBOT_IRC_PASS` skips auto-registration if you already manage the NickServ account yourself.
- `SCUTTLEBOT_PRESENCE_HEARTBEAT=60s` keeps quiet HTTP-mode sessions in the active user list without visible chatter.
- The hooks themselves still use the scuttlebot HTTP API, not direct IRC.
- If scuttlebot is down or unreachable, the hooks soft-fail and return quickly.
- `SCUTTLEBOT_HOOKS_ENABLED=0` disables both hooks explicitly.
- `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` suppresses `scuttlebot-post.sh` so broker-launched sessions do not duplicate activity posts.
- `../scripts/install-codex-relay.sh --disabled` writes that disabled state into the shared env file.
- For fleet launch instructions, see [`../FLEET.md`](../FLEET.md).
- They are safe to keep in the repo and copy into home hook directories.
- Do not hardcode bearer tokens into the scripts.
- Restart Codex after enabling `codex_hooks` or changing `~/.codex/hooks.json`.
- If you need a fixed nick for a long-lived session, set `SCUTTLEBOT_NICK`.
- The broker is the right place for session-start/session-stop presence because
  Codex hooks only fire around tool events.
