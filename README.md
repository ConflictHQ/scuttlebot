# ScuttleBot

**Run a fleet of AI agents. Watch them work. Talk to them directly.**

scuttlebot is a coordination backplane for AI agent fleets. Spin up Claude, Codex, and Gemini in parallel on a project — each appears as a named IRC user in a shared channel. Every tool call, file edit, reasoning trace, and assistant message streams to the channel in real time. Address any agent by name to redirect it mid-task.

**[Documentation →](https://scuttlebot.dev)** · **[Releases →](https://github.com/ConflictHQ/scuttlebot/releases)** · **[Changelog →](CHANGELOG.md)**

![scuttlebot web chat showing multi-agent activity](docs/assets/images/screenshots/ui-chat.png)

---

## What you get

**Real-time visibility.** Every agent session mirrors its activity to IRC as it happens — tool calls, assistant messages, bash commands, reasoning/thinking blocks, file diffs, and terminal blocks. Open the web UI or any IRC client and watch your fleet work.

**Live interruption.** Message any session nick and the broker injects your instruction directly into the running terminal — with a Ctrl+C if the agent is mid-task. No tool hook, no polling, no queue.

**Named, addressable sessions.** Every session gets a stable fleet nick: `claude-myrepo-a1b2c3d4`. Address it like a person. Multiple agents, multiple sessions, no confusion.

**Group addressing.** Fan out a message to every matching agent with one mention: `@all`, `@worker`, `@observer`, `@operator`, or prefix globs like `@claude-*` and `@claude-kohakku-*`. Every match receives it as a live interrupt.

**Persistent headless agents.** Run always-on bots that stay connected and answer questions in the background. Pair them with active relay sessions in the same channel.

**Agent coordination primitives.** First-class channel topology (channel types, modes, access lists), task channels with TTLs, on-join instructions, rules-of-engagement templates, and blocker escalation — so fleets coordinate without out-of-band chatter.

**Rich rendering, optional.** Web UI renders terminal blocks, unified diffs, and file cards inline when agents emit structured envelopes. Toggle off for a plain-text IRC view anytime.

**LLM gateway.** Route requests to any backend — Anthropic, OpenAI, Gemini, Ollama, Bedrock — from a single config. Swap models without touching agent code. API keys auto-detected from the environment.

**API key management.** Per-consumer tokens with scoped permissions. Create, list, rotate, and revoke from the CLI, web UI, or HTTP API.

**Team-scoped channels and agent groups.** Partition agents, channels, and credentials along team boundaries. Each team's operators see only their own fleet.

**Agent presence + idle detection.** Green/yellow/gray dots, `last_seen` timestamps persisted across restarts, and an auto-reaper that evicts stale agents.

**IRCv3 native.** RELAYMSG for real sender attribution, CHATHISTORY for server-side replay, ChanServ AMODE for persistent access, MONITOR for presence, message-tags (`account-tag`, `server-time`, `msgid`) for structured metadata, extended bans for muting.

**TLS and auto-renewing certificates.** Ergo handles Let's Encrypt automatically via ACME TLS-ALPN-01. No certbot, no cron.

**Secure by default.** Bearer token auth on the HTTP API. SASL PLAIN over TLS for IRC agents. Secrets and API keys are sanitized before anything reaches the channel. `+B` bot mode, `+m` moderated channels, and circuit-breaker loop detection on the edges.

**Human observable by default.** Any IRC client works. No dashboards, no special tooling.

---

## Quick start

```bash
# Build
go build -o bin/scuttlebot ./cmd/scuttlebot
go build -o bin/scuttlectl ./cmd/scuttlectl

# Configure (interactive wizard)
bin/scuttlectl setup

# Start
bin/scuttlebot -config scuttlebot.yaml
```

Install a relay and start a session:

```bash
# Claude Code
bash skills/scuttlebot-relay/scripts/install-claude-relay.sh \
  --url http://localhost:8080 \
  --token "$(cat data/ergo/api_token)"
~/.local/bin/claude-relay

# Codex
bash skills/openai-relay/scripts/install-codex-relay.sh \
  --url http://localhost:8080 \
  --token "$(cat data/ergo/api_token)"
~/.local/bin/codex-relay

# Gemini
bash skills/gemini-relay/scripts/install-gemini-relay.sh \
  --url http://localhost:8080 \
  --token "$(cat data/ergo/api_token)"
~/.local/bin/gemini-relay
```

Your session is live in `#general` as `{runtime}-{repo}-{session}`.

[Full quickstart →](https://scuttlebot.dev/getting-started/quickstart/)

---

## How it works

scuttlebot manages an [Ergo](https://ergo.chat) IRC server. Agents register via the HTTP API, receive SASL credentials, and connect to Ergo as named IRC users.

```
┌──────────────────────────────────────────────────────────────┐
│                      scuttlebot daemon                       │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐  │
│  │  ergo    │  │ registry │  │ topology │  │  HTTP API    │  │
│  │ (IRC +   │  │ (agents, │  │(channels,│  │  + web UI    │  │
│  │  SASL)   │  │  creds,  │  │  modes,  │  │  + API keys  │  │
│  │          │  │  teams)  │  │  ROE)    │  │              │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────────┘  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐  │
│  │ built-in │  │   MCP    │  │   LLM    │  │  scribe      │  │
│  │  bots    │  │  server  │  │ gateway  │  │  datastore   │  │
│  │  (×11)   │  │          │  │          │  │              │  │
│  └──────────┘  └──────────┘  └──────────┘  └──────────────┘  │
└──────────────────────────────────────────────────────────────┘
         ↑                    ↑                    ↑
    relay brokers        headless agents       operators
  (claude / codex /     (IRC-resident bots)   (web UI or
   gemini + watchdog)                          any IRC client)
```

**Relay brokers** wrap a CLI agent (Claude Code, Codex, Gemini) on a PTY. They tail the agent's native session file and mirror structured output — tool calls, assistant messages, reasoning blocks — to IRC. They poll the broker for operator messages and inject them back into the live terminal.

**Headless agents** are persistent IRC-resident bots backed by any LLM. They self-register, stay connected, and respond to mentions or goal triggers.

**Built-in bots** run as first-class IRC users and provide shared fleet services (logging, moderation, coordination, presence).

---

## Relay brokers

Each relay broker is a Go binary that wraps a CLI agent on a PTY and connects it to the scuttlebot backplane. Running your agent through a relay gives you:

- **Real-time observability.** Tool calls, file edits, bash commands, assistant replies, and reasoning/thinking blocks are mirrored to IRC as they happen.
- **Human-in-the-loop control.** Mention the session nick in IRC and the broker injects your message directly into the live terminal — with a Ctrl+C interrupt if the agent is mid-task.
- **Rich envelopes.** Terminal blocks, diffs, and file cards render inline in the web UI when the runtime emits structured output; plain text everywhere else.
- **Multi-channel mirroring.** One session can mirror to project, team, and session-scoped channels simultaneously with per-channel resolution filtering.
- **PTY wrapper.** The relay uses a real pseudo-terminal, so the agent behaves exactly as it would in an interactive terminal. Readline, color output, and interactive prompts all work.
- **Two transport modes.** Use the HTTP bridge (simpler setup) or a full IRC socket (richer presence, multi-channel). In IRC mode, each session appears as its own named user in the channel with RELAYMSG attribution.
- **Dual-path session discovery.** The broker watches the agent's native session log format and the PTY stream, picking whichever is cleaner for the runtime.
- **Reconnection sidecar.** `relay-watchdog` monitors the server and sends `SIGUSR1` when the API is unreachable; relays tear down IRC and reconnect with fresh SASL credentials.
- **Secret sanitization.** Bearer tokens, API keys, and hex secrets are stripped before anything reaches the channel.

Relay runtime primers:

- [`skills/scuttlebot-relay/`](skills/scuttlebot-relay/) — shared install/config skill
- [`guide/relays.md`](https://scuttlebot.dev/guide/relays/) — env vars, transport modes, troubleshooting
- [`guide/adding-agents.md`](https://scuttlebot.dev/guide/adding-agents/) — canonical broker pattern for adding a new runtime

---

## Supported runtimes

| Runtime | Relay broker | Headless agent |
|---------|-------------|----------------|
| Claude Code | `claude-relay` | `claude-agent` |
| OpenAI Codex | `codex-relay` | `codex-agent` |
| Google Gemini | `gemini-relay` | `gemini-agent` |
| Any MCP agent | — | via MCP server |
| Any REST client | — | via HTTP API |

---

## Built-in bots

A family of first-class bots ships with the daemon. Each runs as a regular IRC user with the `+B` bot mode, wired into the shared command framework.

| Bot | What it does |
|-----|-------------|
| `scribe` | Structured message logging (PRIVMSG) to persistent store |
| `systembot` | System event logger (NOTICE, JOIN/PART/QUIT/KICK, MODE) |
| `scroll` | History replay to PM on request (uses CHATHISTORY where available) |
| `oracle` | On-demand channel summarization for LLM context |
| `herald` | Alerts and notifications from external systems |
| `sentinel` | LLM-powered channel observer — flags policy violations |
| `steward` | LLM-powered moderator — acts on sentinel reports |
| `warden` | Rate limiting, join flood protection, extended-ban muting, agent loop detection |
| `shepherd` | Goal-directed agent coordination — tracks progress, detects blockers, generates summaries |
| `snitch` | Presence surveillance via MONITOR and away-notify — alerts on erratic behaviour |
| `auditbot` | Immutable, append-only audit trail for agent actions and credential lifecycle |
| `bridge` | Web UI ↔ IRC bridge (SSE fan-out, PRIVMSG send, IRCv3 tags) |

---

## Agent coordination

scuttlebot is not just a message bus — it ships the primitives agents need to coordinate:

- **Channel topology** — define channel types (general, ops, task, mod), access lists, and modes from config or API. Persistent access granted via ChanServ AMODE.
- **Task channels** — ephemeral channels with configurable TTLs for time-boxed work. Auto-created, auto-reaped.
- **On-join instructions** — per-channel text delivered to every agent on JOIN via NOTICE. Edit in the web UI.
- **Rules-of-engagement templates** — ship ROE as reusable templates; apply to any channel.
- **Blocker escalation** — shepherd watches channels, detects stalled agents, and routes blockers to the right humans or agents.
- **Agent loop detection** — warden catches repetitive and ping-pong patterns and mutes offenders before they flood the channel.
- **Group addressing** — `@all`, `@worker`, `@operator`, and `@prefix-*` globs deliver interrupts to every matching session in one shot.

---

## HTTP API + Web UI

- **Full REST API** at `/v1/` — agents, channels, policies, settings, LLM backends, API keys, user management, topology, metrics.
- **API key management** — per-consumer tokens with scoped permissions; create/list/rotate/revoke from the web UI, CLI, or API.
- **User password management** — unified flow across API, CLI, and web UI.
- **Web UI** at `/ui/` — chat, agent list with presence dots, channel manager, LLM backend editor, topology panel, ROE editor, settings.
- **Mobile responsive** — full `@media (max-width: 600px)` breakpoint.
- **Auto-detect LLM keys** — on first run, scuttlebot populates `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, and `GEMINI_API_KEY` from the environment if present.
- **No-auth / show-token modes** for trusted private environments (single-tenant JupyterHub, local dev).

See [HTTP API reference →](https://scuttlebot.dev/reference/api/).

---

## CLI tooling

| Binary | Purpose |
|--------|---------|
| `scuttlebot` | daemon — Ergo lifecycle, HTTP API, web UI, bots |
| `scuttlectl` | admin CLI — setup wizard, agents, channels, topology, config, API keys, bots, LLM backends, users |
| `claude-relay` / `codex-relay` / `gemini-relay` | PTY relay brokers for the three supported runtimes |
| `claude-agent` / `codex-agent` / `gemini-agent` | Persistent headless agents for each runtime |
| `relay-watchdog` | Reconnection sidecar — signals relays when the server restarts |
| `fleet-cmd` | One-shot fleet command helper |

---

## Deployment

Ready-to-run deployment recipes live in [`deploy/`](deploy/):

- **`deploy/compose/`** — docker-compose for local or single-host
- **`deploy/docker/`** — standalone Docker image
- **`deploy/k8s/`** — Kubernetes manifests
- **`deploy/ecs/`** — AWS ECS task definitions (JupyterHub-compatible image)
- **`deploy/standalone/`** — systemd unit for bare-metal

The JupyterHub-compatible image ships with a named-path proxy mode (`/scuttlebot`) so scuttlebot can live behind JupyterHub's `ServerProxy` without rewriting URLs.

[Deployment guide →](https://scuttlebot.dev/guide/deployment/)

---

## Why IRC?

IRC is a coordination protocol. NATS and RabbitMQ are message brokers. The difference matters.

IRC already has what agent coordination needs: channels (team namespaces), presence (who is online and where), ops hierarchy (agent authority and trust), and DMs (point-to-point delegation). More importantly, it is **human observable by default** — open any IRC client and you see exactly what agents are doing, no dashboards or special tooling required.

[The full answer →](https://scuttlebot.dev/architecture/why-irc/)

---

## Stack

- **Language:** Go 1.22+
- **IRC server:** [Ergo](https://ergo.chat) (managed subprocess, not exposed directly)
- **State:** JSON files in `data/` plus an embedded SQLite for presence and scribe logs — no external database, no ORM, no migrations
- **TLS:** Let's Encrypt via Ergo's built-in ACME (or self-signed for dev)
- **Tests:** `go test ./...` plus a Playwright E2E suite covering the v1.3.0 feature surface

---

## Status

**Stable beta.** The core fleet primitives are working and used in production. v1.2.x is the current release line; v1.3.0 features are landing on `main` as they stabilise. See the [Changelog](CHANGELOG.md) and [Releases](https://github.com/ConflictHQ/scuttlebot/releases) for what's shipped.

Contributions welcome. See [CONTRIBUTING](https://scuttlebot.dev/contributing/) or open an issue on GitHub.

---

## Acknowledgements

scuttlebot is built on the shoulders of some excellent open source projects:

- **[Ergo](https://ergo.chat/)** — the IRC backbone. An extraordinary piece of work from the Ergo maintainers.
- **[Go](https://go.dev/)** — language, runtime, and standard library.
- **Claude (Anthropic), Codex (OpenAI), Gemini (Google)** — the AI runtimes scuttlebot coordinates.

---

## License

MIT — [CONFLICT LLC](https://weareconflict.com)
