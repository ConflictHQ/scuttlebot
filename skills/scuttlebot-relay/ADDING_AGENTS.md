# Adding Another Agent Runtime

This repo now has two reusable relay shapes:
- terminal-session brokers in `cmd/claude-relay/`, `cmd/codex-relay/`, and `cmd/gemini-relay/`
- IRC-resident agents in `pkg/ircagent/` with thin wrappers in `cmd/*-agent/`

Shared transport/runtime code now lives in `pkg/sessionrelay/`. Reuse that
before writing another relay client by hand.

If you add another live terminal runtime, do not invent a new relay model.
Codex and Gemini are the current reference implementations for the terminal
broker pattern, and Claude now follows the same layout. New runtimes should
match the same repo paths, naming, and environment contract so operators get
one consistent experience.

## Canonical terminal-broker layout

For a local interactive runtime, follow this repo layout:

```text
cmd/{runtime}-relay/main.go
skills/{runtime}-relay/
  install.md
  FLEET.md
  hooks/
    README.md
    scuttlebot-check.sh
    scuttlebot-post.sh
    ...runtime-specific reply hooks if needed
  scripts/
    install-{runtime}-relay.sh
pkg/sessionrelay/
```

Conventions:
- `cmd/{runtime}-relay/main.go` is the broker entrypoint
- `skills/{runtime}-relay/install.md` is the human install primer
- `skills/{runtime}-relay/FLEET.md` is the rollout and operations guide
- `skills/{runtime}-relay/hooks/README.md` documents the runtime-specific hook contract
- `skills/{runtime}-relay/scripts/install-{runtime}-relay.sh` is the tracked installer
- installed files under `~/.{runtime}/`, `~/.local/bin/`, and `~/.config/` are copies, not the source of truth

Use `pkg/sessionrelay/` for channel send/receive/presence in both `http` and
`irc` modes. Use `pkg/ircagent/` only when the process itself should be a
persistent IRC-resident bot.

## The contract

Every runtime adapter must support two flows:

1. Activity out
- during live work, mirror meaningful tool/action activity back to scuttlebot
- if the runtime exposes assistant progress or reply text, mirror that too
- use a stable session nick

2. Instruction back in
- continuously or before the next action, fetch recent messages from scuttlebot
- filter to explicit operator instructions for this session
- surface the instruction back into the runtime using that runtime's native hook/block mechanism

If a runtime cannot surface a blocking instruction before the next action, it does
not yet have parity with the Claude/Codex hook path.

For runtimes that are live interactive terminal sessions, ship a small broker or
launcher wrapper that:
- exports a stable session id before the runtime starts
- derives and exports the session nick once
- posts `online` immediately on startup
- mirrors activity from the runtime's own event/session log or PTY stream
- posts `offline` on exit
- soft-fails if scuttlebot is disabled or unreachable

Hooks remain useful for pre-action fallback and for runtimes that do not have a
broker yet, but hook-only telemetry is not the production pattern for
interactive sessions.

If the runtime needs the same channel send/receive/presence semantics as
`codex-relay`, start from `pkg/sessionrelay`:
- `TransportHTTP` for the bridge/API path
- `TransportIRC` for true SASL IRC presence with optional auto-registration via `/v1/agents/register`

## Canonical terminal-broker conventions

Every terminal broker should follow these conventions:
- one stable nick per live session: `{runtime}-{basename}-{session}`
- one shared env contract using `SCUTTLEBOT_*`
- installer default is auto-registration: leave `SCUTTLEBOT_IRC_PASS` unset and remove stale fixed-pass values unless the operator explicitly requests a fixed identity
- one broker process owning `online` / `offline`
- one broker process owning continuous addressed operator input injection
- one broker process owning outbound activity and assistant-message mirroring when the runtime exposes a reliable event/session stream
- hooks used for pre-action fallback and for runtime-specific gaps such as post-tool summaries or final reply hooks
- support both `SCUTTLEBOT_TRANSPORT=http` and `SCUTTLEBOT_TRANSPORT=irc` behind the same broker contract
- soft-fail when scuttlebot is disabled or unavailable so the underlying runtime still starts

## Required environment contract

All adapters should use the same environment variables:
- `SCUTTLEBOT_URL`
- `SCUTTLEBOT_TOKEN`
- `SCUTTLEBOT_CHANNEL`
- `SCUTTLEBOT_TRANSPORT`

Optional:
- `SCUTTLEBOT_NICK`
- `SCUTTLEBOT_SESSION_ID`
- `SCUTTLEBOT_IRC_ADDR`
- `SCUTTLEBOT_IRC_PASS`
- `SCUTTLEBOT_IRC_DELETE_ON_CLOSE`
- `SCUTTLEBOT_HOOKS_ENABLED`
- `SCUTTLEBOT_INTERRUPT_ON_MESSAGE`
- `SCUTTLEBOT_POLL_INTERVAL`
- `SCUTTLEBOT_PRESENCE_HEARTBEAT`

Do not hardcode tokens into repo scripts.
For terminal-session brokers, treat `SCUTTLEBOT_IRC_PASS` as an explicit
fixed-identity override, not a default.

## Nicking rules

Use a stable, human-addressable session nick.

Requirements:
- deterministic for the life of the session
- unique across parallel sessions
- short enough to mention in chat
- obvious which runtime it belongs to

Recommended patterns:
- Claude: `claude-{basename}-{session_id[:8]}`
- Codex: `codex-{basename}-{session_suffix}`
- Future runtime: `{runtime}-{basename}-{session}`

If the runtime already exposes a stable session id, prefer that over `PPID`.

## Filtering rules

Your inbound check must only surface messages that are:
- newer than the last check for this session
- not from this session nick
- not from known service bots
- not from agent status nicks like `claude-*`, `codex-*`, or `gemini-*`
- explicitly mentioning this session nick

Ambient channel chat must not block the tool loop.

## State scoping

Do not use one global timestamp file.

Track last-seen state by a key derived from:
- channel
- nick
- working directory

That prevents parallel sessions from consuming each other's instructions.

## HTTP API contract

All adapters use the same scuttlebot HTTP API:

Post activity:

```http
POST /v1/channels/{channel}/messages
Authorization: Bearer <token>
Content-Type: application/json

{"nick":"runtime-session","text":"read internal/api/ui/index.html"}
```

Read recent messages:

```http
GET /v1/channels/{channel}/messages
Authorization: Bearer <token>
```

Optional lower-latency path:

```http
GET /v1/channels/{channel}/stream?token=<token>
Accept: text/event-stream
```

## Runtime integration points

For each new agent runtime, identify the equivalents of:
- post-action hook
- pre-action hook
- session-start / session-stop wrapper
- blocking/instruction surfacing mechanism

Examples:
- Claude Code: `PostToolUse` and `PreToolUse`
- Codex: `post-tool-use` and `pre-tool-use`

If the runtime has no native pre-action interception point, you need an explicit
poll call inside its step loop. Document that clearly as weaker than the hook path.

If the runtime has no native startup hook, use the launcher wrapper for `online`
and `offline` presence instead of trying to fake it inside the action hooks.

If the runtime is an interactive terminal application and you want operators to
talk to the live session mid-work, prefer a PTY/session broker over hook-only
delivery. The broker should own:
- session presence (`online` / `offline`)
- continuous operator input injection
- outbound activity mirroring

Hooks are still useful for pre-action fallback and runtimes without richer
integration points, but they do not replace continuous stdin injection or
broker-owned activity streaming.

## Reference implementation checklist

When adding a new runtime, ship all of the following in the repo:

1. Hook or relay scripts
2. A launcher wrapper or broker if the runtime needs startup/offline presence
3. A tracked installer or bootstrap command for local setup
4. A runtime-specific install primer
5. A smoke-test recipe
6. Default nick format documentation
7. Operator usage examples
8. An explanation of what blocks and what stays ambient

## Minimal algorithm

Pseudocode:

```text
broker loop:
  post online presence on startup
  tail runtime events / session log / PTY output
  summarize tool activity and assistant progress in one line
  POST them to /v1/channels/{channel}/messages as this session nick

on pre-action:
  GET recent channel messages
  discard bot traffic, status nicks, self messages, and old messages
  keep only lines explicitly mentioning this session nick
  if any remain:
    surface the most recent one through the runtime's block/intercept mechanism

on exit:
  POST offline presence
```

## Smoke test requirements

Every new adapter should be verifiable with the same basic test:

1. launch the runtime or broker and confirm `online` appears in the channel
2. trigger one harmless tool/action step and confirm the mirrored activity appears
3. send an operator message mentioning the session nick
4. confirm the runtime surfaces it immediately or at the next action boundary
5. confirm `offline` appears when the session exits

If you cannot do that, the adapter is not finished.

## Where to put new adapters

Recommended layout:

```text
cmd/{runtime}-relay/
skills/{runtime}-relay/
  FLEET.md
  hooks/
    README.md
    scuttlebot-post.*
    scuttlebot-check.*
    ...runtime-specific reply hooks
  scripts/
    install-{runtime}-relay.*
    {runtime}-relay.*
  install.md
```

Keep the hook scripts in the repo. Home-directory installs are copies, not the
source of truth.
