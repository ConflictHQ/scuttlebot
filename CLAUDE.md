# Claude — scuttlebot

Primary conventions doc: [`bootstrap.md`](bootstrap.md)

Read it before writing any code.

---

## Project-specific notes

- Language: Go 1.22+
- Transport: IRC — all agent coordination flows through Ergo IRC channels and messages
- HTTP API: `internal/api/` — Bearer token auth, JSON, serves the web UI at `/ui/`
- Admin auth: `internal/auth/` — bcrypt-hashed accounts, login at `POST /login`
- Bot manager: `internal/bots/manager/` — starts/stops system bots based on policy changes
- Human observable by design: everything an agent does is visible in IRC
- Test runner: `go test ./...`
- Formatter: `gofmt` (enforced — run before committing)
- Linter: `golangci-lint run`
- Dev helper: `./run.sh` (start / stop / restart / token / log / test / e2e / clean)
- No ORM, no database — state persisted as JSON files in `data/`

## Key entry points

| Path | Purpose |
|------|---------|
| `cmd/scuttlebot/` | daemon binary |
| `cmd/scuttlectl/` | admin CLI |
| `internal/api/` | HTTP API server + web UI |
| `internal/auth/` | admin account store (bcrypt) |
| `internal/registry/` | agent registration + credential issuance |
| `internal/bots/manager/` | bot lifecycle (start/stop on policy change) |
| `internal/ergo/` | Ergo IRC server lifecycle + config generation |
| `internal/config/` | YAML config loading |
| `pkg/client/` | Go agent SDK |
| `pkg/protocol/` | JSON envelope wire format |

## Conventions

- Errors returned, not panicked. Wrap: `fmt.Errorf("pkg: operation: %w", err)`
- Interfaces defined at point of use, not in the implementing package
- No global state. Dependencies injected via constructor args or struct fields.
- Env vars for secrets only (e.g. `ORACLE_OPENAI_API_KEY`); everything else in `scuttlebot.yaml`

## Memory

See [`memory/MEMORY.md`](memory/MEMORY.md)
