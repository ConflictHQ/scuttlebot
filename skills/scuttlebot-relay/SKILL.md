---
name: scuttlebot-relay
description: Install, configure, or extend the shared scuttlebot relay brokers for Claude, Codex, Gemini, and future runtimes. Use when wiring a local terminal agent into scuttlebot, choosing `http` versus `irc` transport, setting control/work channels, or adding a new runtime that should follow the canonical broker contract.
---

# Scuttlebot Relay

Use this skill when the task is about relay setup or relay architecture. Do not
invent setup commands from memory. Prefer the tracked installers and the shared
broker contract already in this repo.

Installed files under `~/.claude/`, `~/.codex/`, `~/.gemini/`, `~/.local/bin/`,
and `~/.config/` are generated copies. The repo is the source of truth.

## Existing runtimes

Pick the runtime first, then use its tracked installer and docs:

- Claude:
  - installer: `skills/scuttlebot-relay/scripts/install-claude-relay.sh`
  - install doc: `skills/scuttlebot-relay/install.md`
  - fleet guide: `skills/scuttlebot-relay/FLEET.md`
- Codex:
  - installer: `skills/openai-relay/scripts/install-codex-relay.sh`
  - install doc: `skills/openai-relay/install.md`
  - fleet guide: `skills/openai-relay/FLEET.md`
- Gemini:
  - installer: `skills/gemini-relay/scripts/install-gemini-relay.sh`
  - install doc: `skills/gemini-relay/install.md`
  - fleet guide: `skills/gemini-relay/FLEET.md`

When installing or reconfiguring an existing runtime:

1. Prefer the tracked installer script over manual edits.
2. Default to `SCUTTLEBOT_TRANSPORT=irc` when real IRC presence matters.
3. Leave `SCUTTLEBOT_IRC_PASS` unset unless the operator explicitly wants a fixed identity.
4. Always set one primary control channel with `SCUTTLEBOT_CHANNEL`.
5. Use `SCUTTLEBOT_CHANNELS` only for extra joined work channels at startup.
6. Validate the live loop after install: `online`, one mirrored action, one addressed operator instruction, `offline`.

## Channel conventions

Relay brokers use two channel concepts:

- `SCUTTLEBOT_CHANNEL`: primary control channel
- `SCUTTLEBOT_CHANNELS`: comma-separated startup channel set, including the control channel

Live brokers support runtime channel control:

- `/channels`
- `/join #channel`
- `/part #channel`

Use the control channel for operator coordination. Join extra work channels only
when the session needs to mirror activity there too.

## Connection health and reconnection

All three relay binaries (`claude-relay`, `codex-relay`, `gemini-relay`) handle
`SIGUSR1` as a reconnect signal. When the relay receives `SIGUSR1` it tears down
its current IRC/HTTP session and re-establishes the connection from scratch
without restarting the process.

The `relay-watchdog` sidecar automates this:

- Reads `~/.config/scuttlebot-relay.env` (same env file the relays use).
- Polls `$SCUTTLEBOT_URL/v1/status` every 10 seconds.
- Detects server restarts (changed boot ID) and extended outages.
- Sends `SIGUSR1` to the relay process when a reconnect is needed.

Run the watchdog alongside any relay:

```bash
relay-watchdog &
claude-relay "$@"
```

Or use the convenience wrapper:

```bash
skills/scuttlebot-relay/scripts/relay-start.sh claude-relay [args...]
```

Container / fleet pattern: have the entrypoint run both processes, or use
supervisord. The watchdog exits cleanly when its parent relay exits.

## Per-repo channel config

Drop a `.scuttlebot.yaml` in a repo root (gitignored) to override channel
settings per project:

```yaml
# .scuttlebot.yaml
channel: my-project          # auto-joins this as the control channel
channels:                     # additional channels joined at startup
  - my-project
  - design-review
```

`channel` sets the primary control channel for the session (equivalent to
`SCUTTLEBOT_CHANNEL`). The optional `channels` list adds extra work channels
(equivalent to `SCUTTLEBOT_CHANNELS`). Values in the file override the
environment for that repo only.

## Transport conventions

Use one broker contract for both transport modes:

- `SCUTTLEBOT_TRANSPORT=irc`
  - real IRC presence
  - real channel join/part semantics
  - appears in the user list and agent roster through auto-registration
- `SCUTTLEBOT_TRANSPORT=http`
  - bridge/API transport
  - uses silent presence touches instead of visible chatter
  - useful when a direct IRC socket is not available

Default auth convention:

- broker sessions: auto-register ephemeral session nicks
- persistent `*-agent` bots: fixed NickServ credentials when appropriate

## Canonical broker contract

Read `skills/scuttlebot-relay/ADDING_AGENTS.md` when:

- adding another runtime
- changing the shared env contract
- changing nick/channel conventions
- changing who owns presence, input injection, or activity mirroring

The shared runtime pieces are:

- terminal-session brokers in `cmd/*-relay/`
- IRC-resident agents in `cmd/*-agent/`
- shared transport layer in `pkg/sessionrelay/`
- shared IRC bot runtime in `pkg/ircagent/`

## New runtime checklist

For a new terminal runtime, ship this exact shape:

- `cmd/{runtime}-relay/main.go`
- `skills/{runtime}-relay/install.md`
- `skills/{runtime}-relay/FLEET.md`
- `skills/{runtime}-relay/hooks/`
- `skills/{runtime}-relay/scripts/install-{runtime}-relay.sh`

Reuse `pkg/sessionrelay/` before writing another connector by hand.

Match these conventions:

- nick format: `{runtime}-{basename}-{session}`
- shared `SCUTTLEBOT_*` env contract
- broker owns `online` / `offline`
- broker owns live operator message injection
- broker owns transport and presence
- hooks stay as runtime-specific fallback/integration points

## Examples

Codex:

```bash
bash skills/openai-relay/scripts/install-codex-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general \
  --transport irc
```

Gemini:

```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general \
  --channels general,task-42 \
  --transport irc
```

Claude:

```bash
bash skills/scuttlebot-relay/scripts/install-claude-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general \
  --transport irc
```
