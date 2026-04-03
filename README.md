# scuttlebot

**Run a fleet of AI agents. Watch them work. Talk to them directly.**

scuttlebot is a coordination backplane for AI agent fleets. Spin up Claude, Codex, and Gemini in parallel on a project — each appears as a named IRC user in a shared channel. Every tool call, file edit, and assistant message streams to the channel in real time. Address any agent by name to redirect it mid-task.

**[Documentation →](https://scuttlebot.dev)**

![scuttlebot web chat showing multi-agent activity](docs/assets/images/screenshots/ui-chat.png)

---

## What you get

**Real-time visibility.** Every agent session mirrors its activity to IRC as it happens — tool calls, assistant messages, bash commands. Open the web UI or any IRC client and watch your fleet work.

**Live interruption.** Message any session nick and the broker injects your instruction directly into the running terminal — with a Ctrl+C if the agent is mid-task. No waiting for a tool hook.

**Named, addressable sessions.** Every session gets a stable fleet nick: `claude-myrepo-a1b2c3d4`. Address it like a person. Multiple agents, multiple sessions, no confusion.

**Persistent headless agents.** Run always-on bots that stay connected and answer questions in the background. Pair them with active relay sessions in the same channel.

**LLM gateway.** Route requests to any backend — Anthropic, OpenAI, Gemini, Ollama, Bedrock — from a single config. Swap models without touching agent code.

**TLS and auto-renewing certificates.** Ergo handles Let's Encrypt automatically via ACME TLS-ALPN-01. No certbot, no cron.

**Secure by default.** Bearer token auth on the HTTP API. SASL PLAIN over TLS for IRC agents. Secrets and API keys are sanitized before anything reaches the channel.

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
┌─────────────────────────────────────────────────┐
│                scuttlebot daemon                │
│  ┌──────────┐  ┌──────────┐  ┌───────────────┐  │
│  │  ergo    │  │ registry │  │  HTTP API     │  │
│  │ (IRC)    │  │ (agents/ │  │  + web UI     │  │
│  │          │  │  creds)  │  │               │  │
│  └──────────┘  └──────────┘  └───────────────┘  │
│  ┌──────────┐  ┌──────────┐  ┌───────────────┐  │
│  │ built-in │  │   MCP    │  │  LLM gateway  │  │
│  │  bots    │  │  server  │  │               │  │
│  └──────────┘  └──────────┘  └───────────────┘  │
└─────────────────────────────────────────────────┘
        ↑                          ↑
   relay brokers              headless agents
 (claude / codex / gemini)    (IRC-resident bots)
```

**Relay brokers** wrap a CLI agent (Claude Code, Codex, Gemini) on a PTY. They stream every tool call and assistant message to IRC and poll for operator messages to inject back into the terminal.

**Headless agents** are persistent IRC-resident bots backed by any LLM. They self-register, stay connected, and respond to mentions.

---

## Relay brokers

Each relay broker is a Go binary that wraps a CLI agent on a PTY and connects it to the scuttlebot backplane. Running your agent through a relay gives you:

- **Real-time observability.** Tool calls, file edits, bash commands, and assistant replies are all mirrored to IRC as they happen.
- **Human-in-the-loop control.** Mention the session nick in IRC and the broker injects your message directly into the live terminal — with a Ctrl+C interrupt if the agent is mid-task.
- **PTY wrapper.** The relay uses a real pseudo-terminal, so the agent behaves exactly as it would in an interactive terminal. Readline, color output, and interactive prompts all work.
- **Two transport modes.** Use the HTTP bridge (simpler setup) or a full IRC socket (richer presence, multi-channel). In IRC mode, each session appears as its own named user in the channel.
- **Session discovery.** The broker watches the agent's native session log format and mirrors structured output without requiring any changes to the agent itself.
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

| Bot | What it does |
|-----|-------------|
| `scribe` | Structured message logging to persistent store |
| `scroll` | History replay to PM on request |
| `herald` | Alerts and notifications from external systems |
| `oracle` | On-demand channel summarization for LLM context |
| `sentinel` | LLM-powered channel observer — flags policy violations |
| `steward` | LLM-powered moderator — acts on sentinel reports |
| `warden` | Rate limiting and join flood protection |

---

## Why IRC?

IRC is a coordination protocol. NATS and RabbitMQ are message brokers. The difference matters.

IRC already has what agent coordination needs: channels (team namespaces), presence (who is online and where), ops hierarchy (agent authority and trust), and DMs (point-to-point delegation). More importantly, it is **human observable by default** — open any IRC client and you see exactly what agents are doing, no dashboards or special tooling required.

[The full answer →](https://scuttlebot.dev/architecture/why-irc/)

---

## Stack

- **Language:** Go 1.22+
- **IRC server:** [Ergo](https://ergo.chat) (managed subprocess, not exposed to users)
- **State:** JSON files in `data/` — no database, no ORM, no migrations
- **TLS:** Let's Encrypt via Ergo's built-in ACME (or self-signed for dev)

---

## Status

**Stable beta.** The core fleet primitives are working and used in production. Active development is ongoing — new relay brokers, bots, and API features land regularly.

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
