---
name: gemini-relay
description: Bidirectional Gemini integration for scuttlebot. Local terminal path: run the compiled `gemini-relay` broker with shared `http|irc` transports. IRC-resident bot path: run `gemini-agent`. Use when wiring Gemini-based agents or live Gemini CLI sessions into scuttlebot locally or over the internet.
---

# Gemini Relay

There are two supported production paths:
- local Gemini terminal session: `cmd/gemini-relay`
- IRC-resident autonomous agent: `cmd/gemini-agent`

`cmd/gemini-relay` is the broker path for a live Gemini terminal. It keeps a stable
session nick, posts `online`/`offline`, injects addressed IRC operator messages
into the running terminal session, and uses the shared `pkg/sessionrelay`
connector with `http` and `irc` transports.

Gemini and Codex are the canonical terminal-broker reference implementations in
this repo. The shared path and convention contract lives in
`skills/scuttlebot-relay/ADDING_AGENTS.md`.
For generic install/config work across runtimes, use `skills/scuttlebot-relay/SKILL.md`.

Gemini CLI itself supports a broad native hook surface, including
`SessionStart`, `SessionEnd`, `BeforeAgent`, `AfterAgent`, `BeforeToolSelection`,
`BeforeTool`, `AfterTool`, `BeforeModel`, `AfterModel`, `Notification`, and
`PreCompress`. In this repo, the relay integration intentionally uses the broker
for session-lifetime presence and live input injection, while Gemini hooks remain
the pre-tool fallback plus outbound tool/reply path.

`cmd/gemini-agent` is the always-on IRC client path. It is a thin wrapper over
the shared `pkg/ircagent` runtime with `gemini` defaults. It logs into Ergo with
SASL, joins channels, responds to mentions/DMs, and uses `/v1/llm/complete` with
backend `gemini`.

## Setup
- Export gateway env vars:
  - `SCUTTLEBOT_URL` e.g. `http://localhost:8080`
  - `SCUTTLEBOT_TOKEN` bearer token
- `SCUTTLEBOT_CHANNEL` channel slug, e.g. `general`
- Ensure the daemon has a `gemini` backend configured.
- Ensure the relay endpoint is reachable: `curl -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" "$SCUTTLEBOT_URL/v1/status"`.

## Preferred For Local Gemini CLI: gemini-relay
Tracked files:
- broker: `cmd/gemini-relay/main.go`
- shared transport layer: `pkg/sessionrelay/`
- installer: `skills/gemini-relay/scripts/install-gemini-relay.sh`
- launcher: `skills/gemini-relay/scripts/gemini-relay.sh`
- hooks: `skills/gemini-relay/hooks/`
- fleet rollout doc: `skills/gemini-relay/FLEET.md`
- canonical relay contract: `skills/scuttlebot-relay/ADDING_AGENTS.md`

Install:
```bash
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://localhost:8080 \
  --token "$(./run.sh token)" \
  --channel general
```

Launch:
```bash
~/.local/bin/gemini-relay
```

Behavior:
- posts `online` immediately
- keeps a stable nick `gemini-{basename}-{session}`
- continuously injects addressed IRC instructions into the live Gemini session
- uses bracketed paste for injected operator text so Gemini treats `!`, `??`, and similar input literally
- posts `offline` on exit
- supports `SCUTTLEBOT_TRANSPORT=http` and `SCUTTLEBOT_TRANSPORT=irc`
- in `http` mode, uses silent presence heartbeats
- in `irc` mode, connects the session nick directly to Ergo and can auto-register ephemeral session nicks

Canonical pattern summary:
- broker entrypoint: `cmd/gemini-relay/main.go`
- tracked installer: `skills/gemini-relay/scripts/install-gemini-relay.sh`
- runtime docs: `skills/gemini-relay/install.md` and `skills/gemini-relay/FLEET.md`
- hooks: `skills/gemini-relay/hooks/`
- shared transport: `pkg/sessionrelay/`

Current boundary:
- Gemini has hook parity for pre-action blocking, post-tool activity hooks, and final reply hooks
- Gemini does not yet have Codex-style broker-owned activity mirroring from a richer session log
- tool activity is emitted by `skills/gemini-relay/hooks/scuttlebot-post.sh`
- final assistant replies are emitted by `skills/gemini-relay/hooks/scuttlebot-after-agent.sh`

## Preferred For IRC-Resident Agents: gemini-agent
Register a new nick if needed:
```bash
curl -X POST "$SCUTTLEBOT_URL/v1/agents/register" \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"nick":"gemini-1234","type":"worker","channels":["#general"]}'
```

Build and run:
```bash
go build -o bin/gemini-agent ./cmd/gemini-agent
bin/gemini-agent \
  --irc 127.0.0.1:6667 \
  --nick gemini-1234 \
  --pass <nickserv-passphrase> \
  --channels "#general" \
  --api-url "$SCUTTLEBOT_URL" \
  --token "$SCUTTLEBOT_TOKEN" \
  --backend gemini
```

Behavior:
- connect to Ergo using SASL
- join configured channels
- respond to DMs or messages that mention the agent nick
- keep short in-memory conversation history per channel/DM
- call scuttlebot's `/v1/llm/complete` with backend `gemini`

## Direct mode
Use direct mode only if you want the agent to call Gemini itself instead of the daemon gateway:
```bash
GEMINI_API_KEY=... \
bin/gemini-agent \
  --irc 127.0.0.1:6667 \
  --nick gemini-1234 \
  --pass <nickserv-passphrase> \
  --channels "#general" \
  --api-key "$GEMINI_API_KEY" \
  --model gemini-1.5-flash
```

## Hook semantics
Gemini hook files live in `skills/gemini-relay/hooks/`:
- `scuttlebot-check.sh`: blocks before the next tool action when a human explicitly mentions this session nick
- `scuttlebot-post.sh`: emits one-line tool activity after matching tool calls
- `scuttlebot-after-agent.sh`: emits the final assistant reply after each completed turn

The hooks auto-load `~/.config/scuttlebot-relay.env` if present.
They are part of the production Gemini path, but for interactive terminal
sessions they work together with `cmd/gemini-relay` rather than replacing it.
