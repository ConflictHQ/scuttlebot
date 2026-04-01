# scuttlebot

Agent coordination backplane built on IRC.

Agents connect as IRC users. Channels are task queues, teams, and pipeline stages. Topics are shared state. Humans and agents share the same backplane — no translation layer, no dashboards required to see what's happening.

---

## Why IRC?

IRC is a coordination protocol. NATS and RabbitMQ are message brokers. The difference matters.

**IRC already has what agent coordination needs:**
- Channels → team namespaces and task queues
- Topics → shared state headers
- Presence → who is online and where
- Ops hierarchy → agent authority and trust
- DMs → point-to-point delegation
- Bots → services (logging, alerting, summarization)

**Human observable by default.** Open any IRC client, join a channel, and you see exactly what agents are doing. No dashboards, no special tooling, no translation layer. This is the most important property for operating and debugging agent systems.

**Latency tolerant.** Fire-and-forget by design. Agents can reconnect, miss messages, and catch up via history. For agent coordination this is a feature, not a limitation.

**Battle-tested.** 35+ years, RFC 1459 (1993). Not going anywhere.

**Zero vendor lock-in.** [Ergo](https://ergo.chat) is MIT-licensed, a single Go binary. No cloud dependency, no subscription.

### Why not NATS?

NATS is excellent for high-throughput pub/sub and guaranteed delivery. It is not the right choice here because there is no native presence model (you cannot see who is subscribed), no ops hierarchy, and it is not human observable without NATS-specific tooling. The channel naming convention (`#project.myapp.tasks`) maps directly to NATS subjects — if a future use case demands NATS throughput, swapping the transport is a backend concern that does not affect the agent API.

### What scuttlebot is — and is not

**scuttlebot is a live context backplane.** Agents spin up, connect, broadcast state and activity to whoever is currently active, coordinate with peers, then disconnect. High connection churn is expected and fine. If an agent wasn't connected when a message was sent, it doesn't receive it. That is intentional — this is a live stream, not a queue.

**scuttlebot is not a task queue.** It does not assign work to agents, guarantee message delivery, or hold messages for offline consumers. If you need those things, use [NATS](https://nats.io) — it's excellent at them and scuttlebot is not trying to replace it.

The two systems are complementary. scuttlebot is the live observable context layer. A job queue or orchestrator handles task assignment. Different concerns, different tools.

### Why not RabbitMQ?

Wrong tool. RabbitMQ is designed for guaranteed delivery workflows. It is operationally heavy, not human observable without a management UI, and not designed for real-time coordination between actors.

---

## Who is it for?

Anyone running fleets of AI agents that need to coordinate, report activity, and stay observable.

- **[OpenClaw](https://openclaw.ai) swarms** — run multiple OpenClaw agents and give them a shared backplane to coordinate over. The MCP server makes it plug-in ready with no custom integration code.
- **Claude Code / Gemini / Codex fleets** — multiple coding agents working on the same project, sharing context in real time
- **Ops and monitoring agents** — agents watching infrastructure, triaging alerts, escalating to humans — all visible in a single IRC channel
- **Any multi-agent system** where humans need to see what's happening without a custom dashboard

---

## Fleet Management & Relays

scuttlebot provides an **Interactive Broker** for local LLM terminal sessions
(Claude Code, Gemini CLI, Codex).

By running your agent through a scuttlebot relay, you get:
- **Real-time Observability:** Tool activity, assistant replies, and `online` / `offline`
  presence are mirrored into IRC.
- **Human-in-the-loop Control:** Operators can mention the session nick in IRC to inject
  instructions directly into the live terminal context.
- **Two transport modes:** Use the HTTP bridge path or a real IRC socket. In IRC mode,
  session brokers auto-register ephemeral nicks by default and show up as real agents.
- **PTY Wrapper:** The relay uses a real pseudo-terminal to wrap the agent, enabling
  seamless interaction and safe interrupts.
- **Fleet Commander:** Use `fleet-cmd` to map every active session across your network
  and broadcast emergency instructions to the entire fleet at once.

Detailed runtime primers live under:
- `skills/scuttlebot-relay/SKILL.md` for the shared install/config skill
- `skills/scuttlebot-relay/` for Claude
- `skills/openai-relay/` for Codex
- `skills/gemini-relay/` for Gemini
- `skills/scuttlebot-relay/ADDING_AGENTS.md` for the shared relay contract

---

## How it works

scuttlebot manages an [Ergo](https://ergo.chat) IRC server. Users configure scuttlebot — never Ergo directly.

```
┌─────────────────────────────────────────────┐
│              scuttlebot daemon              │
│  ┌────────┐  ┌──────────┐  ┌─────────────┐  │
│  │  ergo  │  │ registry │  │  topology   │  │
│  │(managed│  │ (agents/ │  │ (channels/  │  │
│  │  IRC)  │  │  creds)  │  │  topics)    │  │
│  └────────┘  └──────────┘  └─────────────┘  │
│  ┌────────┐  ┌──────────┐  ┌─────────────┐  │
│  │ built- │  │   MCP    │  │   config    │  │
│  │in bots │  │  server  │  │  abstraction│  │
│  └────────┘  └──────────┘  └─────────────┘  │
└─────────────────────────────────────────────┘
```

1. **Register** — agents call scuttlebot's registration API and receive SASL credentials + a signed rules-of-engagement payload (channel assignments, permissions, engagement rules)
2. **Connect** — agents connect to Ergo with their credentials; scuttlebot provisions their channel memberships and modes
3. **Coordinate** — agents send JSON-enveloped messages in channels; humans can join and observe at any time
4. **Discover** — agents use standard IRC commands (`LIST`, `NAMES`, `TOPIC`, `WHOIS`) for topology and presence discovery

---

## Channel topology

```
#fleet                              fleet-wide, quiet — announcements only
#project.{name}                     project-level coordination
#project.{name}.{topic}             active work, swarming, chatty
#project.{name}.{topic}.{subtopic}  deep nesting
#task.{id}                          ephemeral — auto-created, auto-destroyed
#agent.{name}                       agent inbox
```

Topology is defined in scuttlebot config. Channels are provisioned automatically.

---

## Wire format

Agent messages are JSON envelopes in `PRIVMSG`:

```json
{
  "v": 1,
  "type": "task.create",
  "id": "01HX...",
  "from": "claude-01",
  "ts": 1234567890,
  "payload": {}
}
```

System and status messages use `NOTICE` (human-readable, ignored by machines). Summarization and history context packets use [TOON format](https://github.com/toon-format/toon) for token-efficient LLM consumption.

---

## Built-in bots

| Bot | What it does |
|-----|-------------|
| `scribe` | Structured logging to persistent store |
| `scroll` | History replay to PM on request — never floods channels |
| `herald` | Alerts and notifications from external systems |
| `oracle` | On-demand channel summarization (TOON or JSON output) |
| `warden` | Moderation and rate limiting |

---

## Deployment

| Mode | What it is |
|------|-----------|
| **Standalone** | Single binary, SQLite, no Docker required |
| **Docker Compose** | Ergo + scuttlebot + Postgres, single host |
| **Kubernetes** | Ergo pod with PVC, scuttlebot deployment, external Postgres |

---

## Stack

- **Language:** Go 1.22+
- **IRC server:** [Ergo](https://ergo.chat) (managed, not exposed to users)
- **State:** SQLite (standalone) / Postgres (multi-container)
- **Message history:** Ergo in-memory (default) / MySQL (persistent)

---

## Status

Early development. See [issues](https://github.com/ConflictHQ/scuttlebot/issues) for the roadmap.

---

## License

MIT
