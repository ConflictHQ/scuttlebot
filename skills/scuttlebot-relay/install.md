# scuttlebot-relay skill

Installs Claude Code hooks that post your activity to an IRC channel in real time
and surface human instructions from IRC back into your context before each action.

Shared relay skill entry: [`SKILL.md`](SKILL.md)

## What it does

The relay provides an interactive broker that:
- starts your Claude session on a real PTY
- posts an "online" message immediately
- continuously polls for addressed operator instructions
- injects operator messages directly into your session as interrupts/input
- posts a summary of every tool call to the IRC channel

## Install (Claude Code)
Detailed primer: [`hooks/README.md`](hooks/README.md)
Shared fleet guide: [`FLEET.md`](FLEET.md)

### 1. Run the tracked installer

Run from the repo checkout:

```bash
bash skills/scuttlebot-relay/scripts/install-claude-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general \
  --channels general,task-42
```

Or via Make:

```bash
SCUTTLEBOT_URL=http://localhost:8080 \
SCUTTLEBOT_TOKEN="$(./run.sh token)" \
SCUTTLEBOT_CHANNEL=general \
make install-claude-relay
```

### 2. Launch your session

Use the relay wrapper instead of the bare `claude` command:

```bash
~/.local/bin/claude-relay
```

The relay will generate a stable, unique nick for the session: `claude-{repo}-{session_id[:8]}`.

## Behavior

- **Ambient Chat:** Unaddressed chat in the channel does not interrupt your work.
- **Operator Instruction:** Mention your session's nick to interrupt and provide guidance.
- **IRC Auth:** Leave `SCUTTLEBOT_IRC_PASS` unset for the default auto-registration path. Use `--irc-pass <passphrase>` only when you intentionally want a fixed identity.
- **Fallbacks:** If the relay server is down, Claude still runs normally; you just lose the IRC coordination layer.

## Configuration

Useful shared env knobs in `~/.config/scuttlebot-relay.env`:
- `SCUTTLEBOT_TRANSPORT=http|irc` — selects the connector backend
- `SCUTTLEBOT_CHANNEL` — primary control channel
- `SCUTTLEBOT_CHANNELS=general,task-42` — optional startup channel set, including the control channel
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1` — interrupts the live Claude session when it appears busy
- `SCUTTLEBOT_POLL_INTERVAL=2s` — controls how often the broker checks for new addressed IRC messages
- `SCUTTLEBOT_PRESENCE_HEARTBEAT=60s` — controls HTTP presence touches; set `0` to disable
- `SCUTTLEBOT_MIRROR_REASONING=0` — set to `1` to mirror Claude's thinking blocks to IRC, prefixed with `💭` (off by default)

Disable without uninstalling:
```bash
SCUTTLEBOT_HOOKS_ENABLED=0 claude-relay
```

Live channel commands:
- `/channels`
- `/join #task-42`
- `/part #task-42`

Those commands change the joined channel set for the current session without
rewriting the shared env file.
