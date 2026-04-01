# gemini-relay skill

There are two production paths:
- local Gemini terminal session: install and launch the compiled `cmd/gemini-relay` broker
- IRC-resident autonomous agent: run `cmd/gemini-agent`

Use the broker path when you want a human-operated Gemini terminal to appear in IRC
immediately, accept addressed operator instructions continuously while the session is
running, and mirror tool activity plus final assistant replies back to the channel.

Gemini and Codex are the canonical terminal-broker reference implementations in
this repo. The shared path and convention contract lives in
[`../scuttlebot-relay/ADDING_AGENTS.md`](../scuttlebot-relay/ADDING_AGENTS.md).

All source-of-truth code lives in this repo:
- installer: [`scripts/install-gemini-relay.sh`](scripts/install-gemini-relay.sh)
- broker: [`../../cmd/gemini-relay/main.go`](../../cmd/gemini-relay/main.go)
- shared connector: [`../../pkg/sessionrelay/`](../../pkg/sessionrelay/)
- hook scripts: [`hooks/scuttlebot-post.sh`](hooks/scuttlebot-post.sh), [`hooks/scuttlebot-check.sh`](hooks/scuttlebot-check.sh), [`hooks/scuttlebot-after-agent.sh`](hooks/scuttlebot-after-agent.sh)
- fleet rollout guide: [`FLEET.md`](FLEET.md)

Files under `~/.gemini/`, `~/.local/bin/`, and `~/.config/` are installed copies.
The repo remains the source of truth.

## What it does

The relay provides an interactive broker that:
- starts your Gemini session on a real PTY
- posts an "online" message immediately
- continuously polls for addressed operator instructions
- injects operator messages directly into your session as interrupts/input
- posts a summary of every tool call to the IRC channel
- mirrors the final assistant reply through `AfterAgent`

## Install (Gemini CLI)
Detailed primer: [`hooks/README.md`](hooks/README.md)
Shared fleet guide: [`FLEET.md`](FLEET.md)
Shared adapter primer: [`../scuttlebot-relay/ADDING_AGENTS.md`](../scuttlebot-relay/ADDING_AGENTS.md)

Canonical pattern summary:
- broker entrypoint: `cmd/gemini-relay/main.go`
- tracked installer: `skills/gemini-relay/scripts/install-gemini-relay.sh`
- runtime docs: `skills/gemini-relay/install.md` and `skills/gemini-relay/FLEET.md`
- hooks: `skills/gemini-relay/hooks/`
- shared transport: `pkg/sessionrelay/`

### 1. Run the tracked installer

Run from the repo checkout:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general
```

Or via Make:

```bash
SCUTTLEBOT_URL=http://localhost:8080 \
SCUTTLEBOT_TOKEN="$(./run.sh token)" \
SCUTTLEBOT_CHANNEL=general \
make install-gemini-relay
```

### 2. Launch your session

Use the relay wrapper instead of the bare `gemini` command:

```bash
~/.local/bin/gemini-relay
```

The relay will generate a stable, unique nick for the session: `gemini-{repo}-{session_id[:8]}`.

## Behavior

- **Ambient Chat:** Unaddressed chat in the channel does not interrupt your work.
- **Operator Instruction:** Mention your session's nick to interrupt and provide guidance.
- **Presence:** `SCUTTLEBOT_TRANSPORT=http` uses silent presence heartbeats; `SCUTTLEBOT_TRANSPORT=irc` uses a real IRC socket for native presence.
- **Replies:** tool activity is mirrored during the turn and the final assistant reply is mirrored after the turn.
- **Fallbacks:** If the relay server is down, Gemini still runs normally; you just lose the IRC coordination layer.

## Configuration

Useful shared env knobs in `~/.config/scuttlebot-relay.env`:
- `SCUTTLEBOT_TRANSPORT=http|irc` — selects the connector backend
- `SCUTTLEBOT_IRC_ADDR=127.0.0.1:6667` — sets the real IRC address when transport is `irc`
- `SCUTTLEBOT_IRC_PASS=...` — uses a fixed NickServ password instead of auto-registration
- `SCUTTLEBOT_IRC_DELETE_ON_CLOSE=0` — keeps auto-registered session nicks after clean exit
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1` — interrupts the live Gemini session when it appears busy
- `SCUTTLEBOT_POLL_INTERVAL=2s` — controls how often the broker checks for new addressed IRC messages
- `SCUTTLEBOT_PRESENCE_HEARTBEAT=60s` — controls HTTP presence touches; set `0` to disable
- `SCUTTLEBOT_AFTER_AGENT_MAX_POSTS=6` — caps how many IRC messages one final Gemini reply may emit
- `SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH=360` — sets the maximum width of each mirrored reply chunk

Disable without uninstalling:
```bash
SCUTTLEBOT_HOOKS_ENABLED=0 gemini-relay
```
