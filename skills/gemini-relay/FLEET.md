# Gemini Relay Fleet Launch

This is the rollout guide for making local Gemini CLI terminal sessions IRC-visible and
operator-addressable through scuttlebot.

Source of truth:
- installer: [`scripts/install-gemini-relay.sh`](scripts/install-gemini-relay.sh)
- broker: [`../../cmd/gemini-relay/main.go`](../../cmd/gemini-relay/main.go)
- shared connector: [`../../pkg/sessionrelay/`](../../pkg/sessionrelay/)
- hooks: [`hooks/scuttlebot-post.sh`](hooks/scuttlebot-post.sh), [`hooks/scuttlebot-check.sh`](hooks/scuttlebot-check.sh)
- runtime docs: [`install.md`](install.md)
- shared runtime contract: [`../scuttlebot-relay/ADDING_AGENTS.md`](../scuttlebot-relay/ADDING_AGENTS.md)

Installed files under `~/.gemini/`, `~/.local/bin/`, and `~/.config/` are generated
copies. Point other engineers and agents at the repo docs and installer, not at one
person's home directory.

Runtime prerequisites:
- `gemini`
- `go`
- `curl`
- `jq`

## What this gives you

For each local Gemini session launched through `gemini-relay`:
- a stable nick: `gemini-{repo}-{session}`
- immediate `online` post when the session starts
- real-time tool activity posts via hooks
- continuous addressed IRC input injection into the live terminal session
- explicit pre-tool fallback interrupts before the next action
- `offline` post on exit

Transport choice:
- `SCUTTLEBOT_TRANSPORT=http` keeps the bridge/API path and now uses presence heartbeats
- `SCUTTLEBOT_TRANSPORT=irc` logs the session nick directly into Ergo for real presence

This is the production control path for a human-operated Gemini terminal. If you
want an always-on IRC-resident bot instead, use `cmd/gemini-agent`.

## One-machine install

Run from the repo checkout:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general
```

Then launch:

```bash
~/.local/bin/gemini-relay
```

## Fleet rollout

For multiple workstations or VM images:

1. Distribute this repo revision.
2. Run the tracked installer on each machine.
3. Launch Gemini through `~/.local/bin/gemini-relay` instead of `gemini`.

Example:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://scuttlebot.internal:8080 \
  --token "$SCUTTLEBOT_TOKEN" \
  --channel fleet \
  --transport irc \
  --irc-addr scuttlebot.internal:6667
```

If you need hooks present but inactive until the server is live:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh --disabled
```

Later, re-enable by editing `~/.config/scuttlebot-relay.env` or rerunning:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh --enabled
```

## What the installer changes

The installer is intentionally narrow. It:
- copies the tracked hook scripts into `~/.gemini/hooks/`
- builds and installs `gemini-relay` into `~/.local/bin/`
- merges required hook entries into `~/.gemini/settings.json`
- writes `SCUTTLEBOT_*` settings into `~/.config/scuttlebot-relay.env`
- keeps one backup copy as `*.bak` before overwriting an existing installed file

It does not:
- replace the real `gemini` binary in `PATH`
- force a fixed nick across sessions
- require IRC to be up at install time

Useful shared env knobs:
- `SCUTTLEBOT_TRANSPORT=http|irc` selects the connector backend
- `SCUTTLEBOT_IRC_ADDR=127.0.0.1:6667` sets the real IRC address when transport is `irc`
- `SCUTTLEBOT_IRC_PASS=...` uses a fixed NickServ password instead of auto-registration
- `SCUTTLEBOT_IRC_DELETE_ON_CLOSE=0` keeps auto-registered session nicks after clean exit
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE=1` interrupts the live Gemini session when it appears busy
- `SCUTTLEBOT_POLL_INTERVAL=2s` controls how often the broker checks for new addressed IRC messages
- `SCUTTLEBOT_PRESENCE_HEARTBEAT=60s` controls HTTP presence touches; set `0` to disable
- `SCUTTLEBOT_AFTER_AGENT_MAX_POSTS=6` caps how many IRC messages one final Gemini reply may emit
- `SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH=360` controls the maximum width of each mirrored reply chunk
- `SCUTTLEBOT_ACTIVITY_VIA_BROKER=1` tells `scuttlebot-post.sh` to stay quiet so broker-launched sessions do not duplicate activity posts

## Operator workflow

1. Watch the configured channel in scuttlebot.
2. Wait for a new `gemini-{repo}-{session}` online post.
3. Mention that nick when you need to steer the session.
4. `cmd/gemini-relay` injects the addressed IRC message into the live terminal session.
5. The pre-tool hook still blocks on the next action if needed.

Examples:

```text
glengoolie: gemini-scuttlebot-a1b2c3d4 stop and re-read bridge.go
glengoolie: gemini-scuttlebot-a1b2c3d4 wrong file, inspect policies.go first
```

Ambient channel chat does not block the loop. Only explicit nick mentions do.

## When IRC/scuttlebot is down

Disable without uninstalling:

```bash
SCUTTLEBOT_HOOKS_ENABLED=0 ~/.local/bin/gemini-relay
```

Or persist the disabled state in the shared env file:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh --disabled
```

The hooks and broker soft-fail if the HTTP API is unavailable. Gemini still runs;
you just lose the IRC coordination layer until the server comes back.

## Adding more runtimes

Do not fork the protocol. Reuse the same control contract:
- post activity out after each action
- accept addressed operator instructions back in before the next action
- use stable, human-addressable session nicks
- keep the repo as the source of truth

The shared authoring contract lives in
[`../scuttlebot-relay/ADDING_AGENTS.md`](../scuttlebot-relay/ADDING_AGENTS.md).
