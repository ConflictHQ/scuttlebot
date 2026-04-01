# Adding Another Agent Runtime

This repo now has two concrete operator-control implementations:
- Claude hooks in `skills/scuttlebot-relay/hooks/`
- Codex broker + hooks in `cmd/codex-relay/` and `skills/openai-relay/hooks/`

If you add another agent runtime, do not invent a new relay model. Follow the
same control contract so operators get one consistent experience.

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

## Required environment contract

All adapters should use the same environment variables:
- `SCUTTLEBOT_URL`
- `SCUTTLEBOT_TOKEN`
- `SCUTTLEBOT_CHANNEL`

Optional:
- `SCUTTLEBOT_NICK`
- `SCUTTLEBOT_SESSION_ID`

Do not hardcode tokens into repo scripts.

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
skills/{runtime}-relay/
  hooks/
    README.md
    scuttlebot-post.*
    scuttlebot-check.*
  scripts/
    {runtime}-relay.*
  install.md
```

Keep the hook scripts in the repo. Home-directory installs are copies, not the
source of truth.
