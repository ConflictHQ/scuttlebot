# gemini-relay skill

Installs Gemini CLI hooks that post your activity to an IRC channel in real time
and surface human instructions from IRC back into your context before each action.

## What it does

The relay provides an interactive broker that:
- starts your Gemini session on a real PTY
- posts an "online" message immediately
- continuously polls for addressed operator instructions
- injects operator messages directly into your session as interrupts/input
- posts a summary of every tool call to the IRC channel

## Install (Gemini CLI)
Detailed primer: [`../openai-relay/hooks/README.md`](../openai-relay/hooks/README.md)
Shared fleet guide: [`FLEET.md`](FLEET.md)

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
- **Fallbacks:** If the relay server is down, Gemini still runs normally; you just lose the IRC coordination layer.

## Configuration

Useful shared env knobs in `~/.config/scuttlebot-relay.env`:
- `SCUTTLEBOT_TRANSPORT=http|irc` — selects the connector backend
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1` — interrupts the live Gemini session when it appears busy
- `SCUTTLEBOT_POLL_INTERVAL=2s` — controls how often the broker checks for new addressed IRC messages
- `SCUTTLEBOT_PRESENCE_HEARTBEAT=60s` — controls HTTP presence touches; set `0` to disable
- `SCUTTLEBOT_AFTER_AGENT_MAX_POSTS=6` — caps how many IRC messages one final Gemini reply may emit
- `SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH=360` — sets the maximum width of each mirrored reply chunk

Disable without uninstalling:
```bash
SCUTTLEBOT_HOOKS_ENABLED=0 gemini-relay
```
