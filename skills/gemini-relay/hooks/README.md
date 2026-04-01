# Gemini Hook Primer

These hooks are the activity and pre-tool fallback path for a live Gemini tool loop.
Continuous IRC-to-terminal input plus `online` / `offline` presence are handled by
the compiled `cmd/gemini-relay` broker, which now sits on the shared
`pkg/sessionrelay` connector package.

Upstream Gemini CLI has a richer native hook surface than just tool hooks:
`SessionStart`, `SessionEnd`, `BeforeAgent`, `AfterAgent`, `BeforeModel`,
`AfterModel`, `BeforeToolSelection`, `BeforeTool`, `AfterTool`, `PreCompress`,
and `Notification`. In this repo we intentionally wire `BeforeTool`,
`AfterTool`, and `AfterAgent` for the relay hooks, while the broker owns
session presence and continuous live operator message injection.

If you need to add another runtime later, use
[`../../scuttlebot-relay/ADDING_AGENTS.md`](../../scuttlebot-relay/ADDING_AGENTS.md)
as the shared authoring contract.

Files in this directory:
- `scuttlebot-post.sh`
- `scuttlebot-check.sh`
- `scuttlebot-after-agent.sh`

Related launcher:
- `../../../cmd/gemini-relay/main.go`
- `../scripts/gemini-relay.sh`
- `../scripts/install-gemini-relay.sh`

Source of truth:
- the repo copies in this directory and `../scripts/`
- not the installed copies under `~/.gemini/` or `~/.local/bin/`

## What they do

`scuttlebot-post.sh`
- runs on Gemini CLI `AfterTool`
- posts a one-line activity summary into a scuttlebot channel
- remains the primary Gemini activity path today, even when launched through `gemini-relay`
- returns valid JSON success output (`{}`), which Gemini CLI expects for hook success

`scuttlebot-check.sh`
- runs on Gemini CLI `BeforeTool`
- fetches recent channel messages from scuttlebot
- ignores bots and agent status nicks
- blocks only when a human explicitly mentions this session nick
- returns valid JSON success output (`{}`) when no block is needed

`scuttlebot-after-agent.sh`
- runs on Gemini CLI `AfterAgent`
- posts the final assistant reply for each completed turn into scuttlebot
- normalizes whitespace and splits long replies into IRC-safe lines
- returns valid JSON success output (`{}`), which Gemini CLI expects for hook success

With the broker plus hooks together, you get the current control loop:
1. `cmd/gemini-relay` posts `online`.
2. The operator mentions the Gemini session nick.
3. `cmd/gemini-relay` injects that IRC message into the live terminal session immediately using bracketed paste, so operator text is treated literally instead of as Gemini keyboard shortcuts.
4. `scuttlebot-check.sh` still blocks before the next tool action if needed.
5. `scuttlebot-post.sh` posts tool activity summaries.
6. `scuttlebot-after-agent.sh` posts the final assistant reply when the turn completes.

This is deliberate:
- broker: session lifetime, presence, live input
- hooks: pre-tool gate, post-tool activity, and final reply mirroring

## Default nick format

If `SCUTTLEBOT_NICK` is unset, the hooks derive a stable session nick:

```text
gemini-{basename of cwd}-{session id}
```

Session id resolution order:
1. `SCUTTLEBOT_SESSION_ID`
2. `GEMINI_SESSION_ID`
3. parent process id (`PPID`)

Examples:
- `gemini-scuttlebot-a1b2c3d4`
- `gemini-api-e5f6a7b8`

## Required environment

Required:
- `SCUTTLEBOT_URL`
- `SCUTTLEBOT_TOKEN`
- `SCUTTLEBOT_CHANNEL`
- `curl` and `jq` available on `PATH`

Optional:
- `SCUTTLEBOT_NICK`
- `SCUTTLEBOT_SESSION_ID`
- `SCUTTLEBOT_TRANSPORT`
- `SCUTTLEBOT_IRC_ADDR`
- `SCUTTLEBOT_IRC_PASS`
- `SCUTTLEBOT_IRC_DELETE_ON_CLOSE`
- `SCUTTLEBOT_HOOKS_ENABLED`
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE`
- `SCUTTLEBOT_POLL_INTERVAL`
- `SCUTTLEBOT_PRESENCE_HEARTBEAT`
- `SCUTTLEBOT_AFTER_AGENT_MAX_POSTS`
- `SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH`
- `SCUTTLEBOT_CONFIG_FILE`

Example:

```bash
export SCUTTLEBOT_URL=http://localhost:8080
export SCUTTLEBOT_TOKEN=$(./run.sh token)
export SCUTTLEBOT_CHANNEL=general
```

The hooks also auto-load a shared relay env file if it exists:

```bash
cat > ~/.config/scuttlebot-relay.env <<'EOF2'
SCUTTLEBOT_URL=http://localhost:8080
SCUTTLEBOT_TOKEN=...
SCUTTLEBOT_CHANNEL=general
SCUTTLEBOT_TRANSPORT=http
SCUTTLEBOT_IRC_ADDR=127.0.0.1:6667
SCUTTLEBOT_HOOKS_ENABLED=1
SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1
SCUTTLEBOT_POLL_INTERVAL=2s
SCUTTLEBOT_PRESENCE_HEARTBEAT=60s
EOF2
```

Disable the hooks entirely:

```bash
export SCUTTLEBOT_HOOKS_ENABLED=0
```

## Hook config

Preferred path: run the tracked installer and let it wire the files up for you.

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general
```

Manual path:

Install the scripts:

```bash
mkdir -p ~/.gemini/hooks
cp skills/gemini-relay/hooks/scuttlebot-post.sh ~/.gemini/hooks/
cp skills/gemini-relay/hooks/scuttlebot-check.sh ~/.gemini/hooks/
cp skills/gemini-relay/hooks/scuttlebot-after-agent.sh ~/.gemini/hooks/
chmod +x ~/.gemini/hooks/scuttlebot-post.sh ~/.gemini/hooks/scuttlebot-check.sh ~/.gemini/hooks/scuttlebot-after-agent.sh
```

Configure Gemini hooks in `~/.gemini/settings.json`:

```json
{
  "hooks": {
    "BeforeTool": [
      {
        "matcher": ".*",
        "hooks": [
          { "type": "command", "command": "$HOME/.gemini/hooks/scuttlebot-check.sh" }
        ]
      }
    ],
    "AfterTool": [
      {
        "matcher": ".*",
        "hooks": [
          { "type": "command", "command": "$HOME/.gemini/hooks/scuttlebot-post.sh" }
        ]
      }
    ],
    "AfterAgent": [
      {
        "matcher": "*",
        "hooks": [
          { "type": "command", "command": "$HOME/.gemini/hooks/scuttlebot-after-agent.sh" }
        ]
      }
    ]
  }
}
```

Install the compiled broker if you want startup/offline presence plus continuous
IRC input injection:

```bash
mkdir -p ~/.local/bin
go build -o ~/.local/bin/gemini-relay ./cmd/gemini-relay
chmod +x ~/.local/bin/gemini-relay
```

Launch with:

```bash
~/.local/bin/gemini-relay
```

## Message filtering semantics

The check hook only surfaces messages that satisfy all of the following:
- newer than the last check for this session
- not posted by this session nick
- not posted by known service bots
- not posted by `claude-*`, `codex-*`, or `gemini-*` status nicks
- explicitly mention this session nick

Ambient channel chat must not halt a live tool loop.

## Operational notes

- `cmd/gemini-relay` can use either the HTTP bridge API or a real IRC socket.
- `SCUTTLEBOT_TRANSPORT=irc` gives the live session a true IRC presence; `SCUTTLEBOT_IRC_PASS` skips auto-registration if you already manage the NickServ account yourself.
- `SCUTTLEBOT_PRESENCE_HEARTBEAT=60s` keeps quiet HTTP-mode sessions in the active user list without visible chatter.
- Gemini CLI expects hook success responses on `stdout` to be valid JSON; these relay hooks emit `{}` on success and structured deny JSON on blocks.
- Gemini CLI built-in tool names are things like `run_shell_command`, `read_file`, and `write_file`; the activity hook summarizes those native names.
- Gemini outbound mirroring is still hook-owned today: `AfterTool` covers tool activity and `AfterAgent` covers final assistant replies. That is the main behavioral difference from `codex-relay`, which mirrors activity from a richer session log.
- `scuttlebot-after-agent.sh` compacts whitespace, splits replies into IRC-safe chunks, and caps the number of posts so large responses and failure payloads stay under Gemini's hook timeout.
- `SCUTTLEBOT_AFTER_AGENT_MAX_POSTS` defaults to `6`; `SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH` defaults to `360`.
- If scuttlebot is down or unreachable, the hooks soft-fail and return quickly.
- `SCUTTLEBOT_HOOKS_ENABLED=0` disables all Gemini relay hooks explicitly.
- They should remain in the repo as installable reference files.
- Do not bake tokens into the scripts. Use environment variables.
