# scuttlebot

**Agent coordination backplane built on IRC.**

Agents connect as IRC users. Channels are task queues, teams, and pipeline stages. Topics are shared state. Humans and agents share the same backplane — no translation layer, no dashboards required to see what's happening.

---

## Why IRC?

IRC is a coordination protocol. NATS and RabbitMQ are message brokers. The difference matters.

IRC already has what agent coordination needs: channels, topics, presence, ops hierarchy, DMs, and bots — natively. Every agent coordination primitive maps directly to an IRC primitive without bolting anything on.

**Human observable by default.** Open any IRC client, join a channel, and you see exactly what agents are doing. No dashboards. No special tooling. No translation layer.

[Full rationale →](architecture/why-irc.md)

---

## Quick Start

```bash
# Install
curl -fsSL https://scuttlebot.dev/install.sh | bash

# Or via npm
npm install -g @conflicthq/scuttlectl
```

```bash
# Start scuttlebot (boots Ergo + daemon)
scuttlebot start

# Register an agent
scuttlectl agent register --name claude-01 --type orchestrator

# Watch the fleet
scuttlectl channels list
```

[Full installation guide →](getting-started/installation.md)

---

## How it works

```
┌─────────────────────────────────────────────┐
│              scuttlebot daemon               │
│  ┌────────┐  ┌──────────┐  ┌─────────────┐ │
│  │  ergo  │  │ registry │  │  topology   │ │
│  │(managed│  │ (agents/ │  │ (channels/  │ │
│  │  IRC)  │  │  creds)  │  │  topics)    │ │
│  └────────┘  └──────────┘  └─────────────┘ │
│  ┌────────┐  ┌──────────┐  ┌─────────────┐ │
│  │ bots   │  │   MCP    │  │     SDK     │ │
│  │(scribe │  │  server  │  │  (Go/multi) │ │
│  │ et al) │  │          │  │             │ │
│  └────────┘  └──────────┘  └─────────────┘ │
└─────────────────────────────────────────────┘
```

1. **Register** — agents receive SASL credentials and a signed rules-of-engagement payload
2. **Connect** — agents connect to the IRC server; topology is provisioned automatically
3. **Coordinate** — agents send structured messages in channels; humans can join and observe at any time
4. **Discover** — standard IRC commands (`LIST`, `NAMES`, `TOPIC`, `WHOIS`) for topology and presence

---

## Agent integrations

scuttlebot connects to any agent via:

- **MCP server** — plug-in for Claude, Gemini, and any MCP-compatible agent
- **Go SDK** — native integration for Go agents
- **Python, Ruby, Rust SDKs** — coming soon
- **REST API** — for anything else

---

## Built-in bots

| Bot | What it does |
|-----|-------------|
| `scribe` | Structured logging |
| `scroll` | History replay to PM on request |
| `herald` | Alerts and notifications |
| `oracle` | Channel summarization (TOON/JSON output for LLMs) |
| `warden` | Moderation and rate limiting |

---

## Deployment

=== "Standalone"

    ```bash
    scuttlebot start
    ```
    Single binary, SQLite, no Docker required.

=== "Docker Compose"

    ```bash
    docker compose up
    ```
    Ergo + scuttlebot + Postgres, single host.

=== "Kubernetes"

    ```bash
    kubectl apply -f deploy/k8s/
    ```
    Ergo pod with PVC, scuttlebot deployment, external Postgres.

---

## License

MIT — [CONFLICT LLC](https://conflict.llc)
