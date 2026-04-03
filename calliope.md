# Calliope — scuttlebot
<!-- Agent shim for https://github.com/calliopeai/calliope-cli -->

Primary conventions doc: [`bootstrap.md`](bootstrap.md)

Read it before writing any code.

---

## Project-specific notes

- Language: Go 1.22+
- Transport: IRC — all agent coordination flows through Ergo IRC channels and messages
- HTTP API: `internal/api/` — Bearer token auth, JSON, serves the web UI at `/ui/`
- No ORM, no database — state persisted as YAML/JSON files
- Human observable by design: everything an agent does is visible in IRC
- Test runner: `go test ./...`
- Formatter: `gofmt` (enforced)
- Linter: `golangci-lint run`
- Dev helper: `./run.sh` (start / stop / restart / token / log / test / e2e / clean)
