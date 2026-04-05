# scuttlebot Bootstrap

This is the primary conventions document. All agent shims (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, `calliope.md`) point here.

An agent given this document and a business requirement should be able to generate correct, idiomatic code without exploring the codebase.

---

## Why IRC (and not NATS or RabbitMQ)

The short answer: IRC is a coordination protocol. NATS and RabbitMQ are message brokers. The difference matters.

### IRC

IRC has presence, identity, channels, topics, ops hierarchy, DMs, and bots — natively. These map directly to agent coordination concepts without bolting anything on. A channel is a team. A topic is shared state. Ops are authority. Bots are services. It all just works.

It is also **human observable by default**. No dashboards, no special tooling, no translation layer. Open any IRC client, join a channel, and you see exactly what agents are doing. This is the single biggest advantage for debugging and operating agent systems.

Other properties that matter for agent coordination:
- **Latency tolerant** — fire-and-forget, designed for unreliable networks. Agents can reconnect, miss messages, catch up via history. This is a feature, not a limitation.
- **Battle-tested** — 35+ years, RFC 1459 (1993), proven at scale. Not going anywhere.
- **Self-hostable, zero vendor lock-in** — Ergo is MIT, single Go binary. No cloud, no subscription.
- **Bots are a solved problem** — NickServ, ChanServ, BotServ, 35 years of tooling. We inherit all of it.
- **Simple enough to debug naked** — the protocol is plain text. When something breaks, you can read it.

### Why not NATS

NATS is excellent and fast. It is the right choice when you need guaranteed delivery, high-throughput pub/sub, or JetStream persistence at scale. It is not the right choice here because:

- No native presence model — you cannot `WHOIS` a subject or see who is subscribed to a stream
- No ops hierarchy — authority and trust are not protocol concepts
- Not human observable without NATS-specific tooling (no standard client exists for "just watching")
- More moving pieces — JetStream, clustering, leaf nodes, consumers, streams. Powerful but not simple.
- The subject hierarchy (`project.myapp.tasks`) is conceptually identical to our channel naming convention — if we ever needed to swap, the mapping is straightforward

### Why not RabbitMQ

RabbitMQ is a serious enterprise message broker designed for guaranteed delivery workflows. It is operationally heavy (Erlang runtime, clustering, exchanges, bindings, queues), not human observable without a management UI, and not designed for real-time coordination between actors. Wrong tool for this job.

### Swappability

The JSON envelope format and the SDK abstraction (`pkg/client/`) are intentionally transport-agnostic. The channel naming convention maps cleanly to NATS subjects. If a use case demands NATS-level throughput or delivery guarantees, swapping the transport is a backend concern that does not affect the agent-facing API.

---

## What is scuttlebot

An agent coordination backplane built on IRC. Agents connect as IRC users, coordinate via channels, and communicate via structured messages. IRC is an implementation detail — users configure scuttlebot, never Ergo directly.

**Why IRC:** lightweight TCP transport, encryption, channels, presence, ops hierarchy, DMs, human observable by default. Humans and agents share the same backplane with no translation layer.

**Ergo** (https://ergo.chat) is the IRC server. scuttlebot manages its lifecycle and config. Federation, auth, history, TLS, rate limiting — all Ergo. scuttlebot abstracts it.

---

## Monorepo Layout

```
cmd/
  scuttlebot/           # daemon binary
  scuttlectl/           # admin CLI
    internal/apiclient/ # typed API client used by scuttlectl
internal/
  api/                  # HTTP API server (Bearer auth) + embedded web UI at /ui/
    ui/index.html       # single-file operator web UI
  auth/                 # admin account store — bcrypt hashed, persisted to JSON
  bots/
    manager/            # bot lifecycle — starts/stops bots on policy change
    auditbot/           # immutable append-only audit trail
    herald/             # external event → channel routing (webhooks)
    oracle/             # on-demand channel summarization via LLM (PM only)
    scribe/             # structured logging to rotating files
    scroll/             # history replay to PM on request
    snitch/             # flood + join/part cycling detection → operator alerts
    systembot/          # IRC system events (joins, parts, modes, kicks)
    warden/             # channel moderation — warn → mute → kick
  config/               # YAML config loading + validation
  ergo/                 # Ergo IRC server lifecycle + config generation
  mcp/                  # MCP server for AI agent connectivity
  registry/             # agent registration + SASL credential issuance
  topology/             # channel provisioning + mode/topic management
pkg/
  client/               # Go agent SDK (public)
  protocol/             # JSON envelope wire format
deploy/
  docker/               # Dockerfile(s)
  compose/              # Docker Compose (local dev + single-host)
  k8s/                  # Kubernetes manifests
  standalone/           # single binary, no container required
tests/
  e2e/                  # Playwright end-to-end tests (require scuttlebot running)
go.mod
go.sum
bootstrap.md
CLAUDE.md               # Claude Code shim — points here
```

Single Go module. All state persisted as JSON files under `data/` (no database required).

---

## Architecture

### Ergo relationship

scuttlebot owns the Ergo process and config. Users never edit `ircd.yaml` directly. scuttlebot generates it from its own config and manages Ergo as a subprocess.

- Ergo provides: TLS, SASL accounts, channel persistence, message history, ops hierarchy, server federation, rate limiting
- scuttlebot provides: agent registration, topology provisioning, rules-of-engagement delivery, built-in bots, SDK/MCP layer

### Agent lifecycle

1. Agent calls scuttlebot registration endpoint
2. scuttlebot creates Ergo account, issues SASL credentials
3. On connect, agent receives signed rules-of-engagement payload (channel assignments, engagement rules, permissions)
4. Agent connects to Ergo with SASL credentials
5. scuttlebot verifies presence, assigns channel modes

### Channel topology

Hierarchical, configurable. Convention:

```
#fleet                              fleet-wide, quiet, announcements only
#project.{name}                     project coordination
#project.{name}.{topic}             swarming, chatty, active work
#project.{name}.{topic}.{subtopic}  deep nesting
#task.{id}                          ephemeral, auto-created/destroyed
#agent.{name}                       agent-specific inbox
```

Users define topology in scuttlebot config. scuttlebot provisions the channels, sets modes and topics.

### Wire format

- **Agent messages:** JSON envelope in `PRIVMSG`
- **System/status:** `NOTICE` — human readable, machines ignore
- **Agent context packets** (summarization, history replay): TOON format (token-efficient for LLM consumption)

JSON envelope structure:

```json
{
  "v": 1,
  "type": "task.create",
  "id": "ulid",
  "from": "agent-nick",
  "ts": 1234567890,
  "payload": {}
}
```

### Authority / trust hierarchy

IRC ops model maps directly:
- `+o` (channel op) — orchestrator agents, privileged
- `+v` (voice) — trusted worker agents
- no mode — standard agents

### Built-in bots

All 10 bots are implemented. Enabled/configured via the web UI or `scuttlectl bot list`. The manager (`internal/bots/manager/`) starts/stops them dynamically when policies change. All bots set `+B` (bot) user mode on connect and auto-accept INVITE.

| Bot | Nick | Role |
|-----|------|------|
| `auditbot` | auditbot | Immutable append-only audit trail of agent actions and credential events |
| `herald` | herald | Routes inbound webhook events to IRC channels |
| `oracle` | oracle | On-demand channel summarization via DM — calls any OpenAI-compatible LLM |
| `scribe` | scribe | Structured message logging to rotating files (jsonl/csv/text) |
| `scroll` | scroll | History replay to PM on request (`replay #channel [format=toon]`) |
| `sentinel` | sentinel | LLM-powered channel observer — detects policy violations, posts structured incident reports to mod channel. Never takes enforcement action. |
| `snitch` | snitch | Flood and join/part cycling detection, MONITOR-based presence tracking, away-notify alerts |
| `steward` | steward | Acts on sentinel incident reports — issues warnings, mutes (extended ban `m:`), or kicks based on severity |
| `systembot` | systembot | Logs IRC system events (joins, parts, quits, mode changes) |
| `warden` | warden | Channel moderation — warn → mute (extended ban) → kick on flood |

Oracle uses TOON format (`pkg/toon/`) for token-efficient LLM context. Scroll supports `format=toon` for compact replay output. Configure `api_key_env` to the name of the env var holding the API key (e.g. `ORACLE_OPENAI_API_KEY`), and `base_url` for non-OpenAI providers.

### Scale

Target: 100s to low 1000s of agents on a private network. Single Ergo instance handles this comfortably (documented up to 10k clients, 2k per channel). Ergo scales up (multi-core), not out — no horizontal clustering today. Federation is planned upstream but has no timeline; not a scuttlebot concern for now.

### Persistence

No database required. All state is persisted as JSON files under `data/` by default.

| What | File | Notes |
|------|------|-------|
| Agent registry | `data/ergo/registry.json` | Agent records + SASL credentials |
| Admin accounts | `data/ergo/admins.json` | bcrypt-hashed; created by `scuttlectl admin add` |
| Policies | `data/ergo/policies.json` | Bot config, agent policy, logging settings |
| Bot passwords | `data/ergo/bot_passwords.json` | Auto-generated SASL passwords for system bots |
| API token | `data/ergo/api_token` | Legacy token; migrated to api_keys.json on first run |
| API keys | `data/ergo/api_keys.json` | Per-consumer tokens with scoped permissions (SHA-256 hashed) |
| Ergo state | `data/ergo/ircd.db` | Ergo-native: accounts, channels, topics, history |
| scribe logs | `data/logs/scribe/` | Rotating log files (jsonl/csv/text); configurable |

K8s / Docker: mount a PersistentVolume at `data/`. Ergo is single-instance — HA = fast pod restart with durable storage, not horizontal scaling.

---

## Conventions

### Go

- Go 1.22+
- `gofmt` + `golangci-lint`
- Errors returned, not panicked. Wrap with context: `fmt.Errorf("registry: create account: %w", err)`
- Interfaces defined at point of use, not in the package that implements them
- No global state. Dependencies injected via struct fields or constructor args.
- Config via struct + YAML/TOML — no env var spaghetti (env vars for secrets only)

### Tests

- `go test ./...`
- Integration tests use a real Ergo instance (Docker Compose in CI)
- Assert against observable state — channel membership, messages received, account existence
- Both happy path and error cases
- No mocking the IRC connection in integration tests

### Commits + branches

- Branch: `feature/{issue}-short-description` or `fix/{issue}-short-description`
- No rebases. New commits only.
- No AI attribution in commits.

---

## HTTP API

`internal/api/` — two-mux pattern:

- **Outer mux** (unauthenticated): `POST /login`, `GET /` (redirect), `GET /ui/` (web UI)
- **Inner mux** (`/v1/` routes): require `Authorization: Bearer <token>` header

### Auth

API keys are per-consumer tokens with scoped permissions. Each key has a name, scopes, optional expiry, and last-used tracking. Scopes: `admin`, `agents`, `channels`, `chat`, `topology`, `bots`, `config`, `read`. The `admin` scope implies all others.

`POST /login` accepts `{username, password}` and returns a 24h session token with admin scope. Rate limited to 10 attempts per minute per IP.

On first run, the legacy `api_token` file is migrated into `api_keys.json` as the first admin-scope key. New keys are created via `POST /v1/api-keys`, `scuttlectl api-key create`, or the web UI settings tab.

Admin accounts managed via `scuttlectl admin` or web UI. First run auto-creates `admin` with a random password printed to the log.

### Key endpoints

All `/v1/` endpoints require a Bearer token with the appropriate scope.

| Method | Path | Scope | Description |
|--------|------|-------|-------------|
| `POST` | `/login` | — | Username/password login (unauthenticated) |
| `GET` | `/v1/status` | read | Server status |
| `GET` | `/v1/metrics` | read | Runtime metrics + bridge stats |
| `GET` | `/v1/settings` | read | Full settings (policies, TLS, bot commands) |
| `GET/PUT/PATCH` | `/v1/settings/policies` | admin | Bot config, agent policy, logging |
| `GET` | `/v1/agents` | agents | List all registered agents |
| `GET` | `/v1/agents/{nick}` | agents | Get single agent |
| `PATCH` | `/v1/agents/{nick}` | agents | Update agent |
| `POST` | `/v1/agents/register` | agents | Register an agent |
| `POST` | `/v1/agents/{nick}/rotate` | agents | Rotate credentials |
| `POST` | `/v1/agents/{nick}/adopt` | agents | Adopt existing IRC nick |
| `POST` | `/v1/agents/{nick}/revoke` | agents | Revoke agent credentials |
| `DELETE` | `/v1/agents/{nick}` | agents | Delete agent |
| `GET` | `/v1/channels` | channels | List joined channels |
| `POST` | `/v1/channels/{ch}/join` | channels | Join channel |
| `DELETE` | `/v1/channels/{ch}` | channels | Leave channel |
| `GET` | `/v1/channels/{ch}/messages` | channels | Get message history |
| `POST` | `/v1/channels/{ch}/messages` | chat | Send message |
| `POST` | `/v1/channels/{ch}/presence` | chat | Touch presence (keep web user visible) |
| `GET` | `/v1/channels/{ch}/users` | channels | User list with IRC modes |
| `GET` | `/v1/channels/{ch}/config` | channels | Per-channel display config |
| `PUT` | `/v1/channels/{ch}/config` | channels | Set display config (mirror detail, render mode) |
| `GET` | `/v1/channels/{ch}/stream` | channels | SSE stream (`?token=` query param auth) |
| `POST` | `/v1/channels` | topology | Provision channel via ChanServ |
| `DELETE` | `/v1/topology/channels/{ch}` | topology | Drop channel |
| `GET` | `/v1/topology` | topology | Channel types, static channels, active channels |
| `GET/PUT` | `/v1/config` | config | Server config read/write |
| `GET` | `/v1/config/history` | config | Config change history |
| `GET/POST` | `/v1/admins` | admin | List / add admin accounts |
| `DELETE` | `/v1/admins/{username}` | admin | Remove admin |
| `PUT` | `/v1/admins/{username}/password` | admin | Change password |
| `GET/POST` | `/v1/api-keys` | admin | List / create API keys |
| `DELETE` | `/v1/api-keys/{id}` | admin | Revoke API key |
| `GET/POST/PUT/DELETE` | `/v1/llm/backends[/{name}]` | bots | LLM backend CRUD |
| `GET` | `/v1/llm/backends/{name}/models` | bots | List models for backend |
| `POST` | `/v1/llm/discover` | bots | Discover models from provider |
| `POST` | `/v1/llm/complete` | bots | LLM completion proxy |

---

## Adding a New Bot

1. Create `internal/bots/{name}/` package with a `Bot` struct and `Start(ctx context.Context) error` method
2. Set `+B` user mode on connect, handle INVITE for auto-join
3. Add a `BotSpec` config struct if the bot needs user-configurable settings
4. Register in `internal/bots/manager/manager.go`:
   - Add a case to `buildBot()` that constructs your bot from the spec config
   - Add a `BehaviorConfig` entry to `defaultBehaviors` in `internal/api/policies.go`
5. Add commands to `botCommands` map in `internal/api/policies.go` for the web UI command reference
6. Add the UI config schema to `BEHAVIOR_SCHEMAS` in `internal/api/ui/index.html`
7. Use `internal/bots/cmdparse/` for command routing if the bot accepts DM commands
8. Write tests: bot logic, config parsing, edge cases. IRC connection can be skipped in unit tests.
9. Update this bootstrap

No separate registration file or global registry. The manager builds bots by ID from the `BotSpec`. Bots satisfy the `bot` interface (unexported in manager package):

```go
type bot interface {
    Start(ctx context.Context) error
}
```

---

## Adding a New SDK

1. Create `sdk/{language}/` as its own module
2. Implement the client interface defined in `pkg/client/` as reference
3. Cover: connect, register, send message, receive message, disconnect
4. Own CI workflow in `.github/workflows/sdk-{language}.yml`

---

## Ports (local)

| Service | Address |
|---------|---------|
| Ergo IRC | `ircs://localhost:6697` |
| scuttlebot API | `http://localhost:8080` |
| MCP server | `http://localhost:8081` |

---

## Common Commands

```bash
# Dev helper (recommended)
./run.sh                       # build + start
./run.sh restart               # rebuild + restart
./run.sh stop                  # stop
./run.sh token                 # print current API token
./run.sh log                   # tail the log
./run.sh test                  # go test ./...
./run.sh e2e                   # Playwright e2e (requires scuttlebot running)

# Direct Go commands
go build ./cmd/scuttlebot      # build daemon
go build ./cmd/scuttlectl      # build CLI
go test ./...                  # run all tests
golangci-lint run              # lint

# Admin CLI
scuttlectl status              # server health
scuttlectl admin list          # list admin accounts
scuttlectl admin add alice     # add admin (prompts for password)
scuttlectl admin passwd alice  # change password
scuttlectl admin remove alice  # remove admin
scuttlectl api-key list        # list API keys
scuttlectl api-key create --name "relay" --scopes chat,channels
scuttlectl api-key revoke <id> # revoke key
scuttlectl topology list       # show channel types + static channels
scuttlectl topology provision #channel  # create channel
scuttlectl topology drop #channel       # remove channel
scuttlectl config show         # dump config JSON
scuttlectl config history      # config change history
scuttlectl bot list            # show system bot status
scuttlectl agent list          # list agents
scuttlectl agent register <nick> --type worker --channels #fleet
scuttlectl agent rotate <nick> # rotate credentials
scuttlectl backend list        # LLM backends

# Docker
docker compose -f deploy/compose/docker-compose.yml up
```

## Optional: IRC Chatbot Agents

`cmd/claude-agent`, `cmd/codex-agent`, and `cmd/gemini-agent` are standalone IRC bots that connect to a channel and respond to prompts using an LLM backend. They are **not part of the default build** — they exist as a reference pattern for operators who want a persistent chatbot presence in a channel.

These are distinct from the relay brokers (`claude-relay`, `codex-relay`, `gemini-relay`). The difference:

| | Chatbot agent | Relay broker |
|---|---|---|
| Wraps a coding CLI | No | Yes |
| Reads/writes files, runs commands | No | Yes (via the CLI) |
| Always-on, responds to any mention | Yes | No — tied to an active session |
| Useful for fleet coordination | Novelty only | Core pattern |

The relay broker is the right tool for agent work. The chatbot agent is a nice-to-have for operators who want an LLM available in IRC for quick Q&A, but it cannot act — it can only respond.

### Running one

```bash
# Build (not included in make all)
make chatbots

# Register a nick in scuttlebot
TOKEN=$(./run.sh token)
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"nick":"claude","type":"worker","channels":["#general"]}' \
  http://localhost:8080/v1/agents/register

# Connect (use the passphrase from the register response)
bin/claude-agent --irc 127.0.0.1:6667 --nick claude --pass <passphrase> \
  --api-url http://localhost:8080 --token $TOKEN --backend anthro
```

Swap `claude-agent` → `codex-agent` (backend `openai`) or `gemini-agent` (backend `gemini`) for other providers. All three accept the same flags.
