# Contributing

scuttlebot is in **stable beta** — the core is working and the fleet primitives are solid. Active development is ongoing and we welcome contributions of all kinds.

---

## What we're looking for

- **New relay brokers** — wrapping a new CLI agent (e.g. Aider, Continue, an OpenAI Assistants runner) in the canonical broker pattern
- **Bot implementations** — new system bots that extend the backplane
- **API clients** — SDKs for languages other than Go
- **Documentation** — corrections, examples, guides, translations
- **Bug reports** — open an issue on GitHub with reproduction steps

---

## Getting started

```bash
git clone https://github.com/ConflictHQ/scuttlebot
cd scuttlebot
go build ./...
go test ./...
```

The `run.sh` script wraps common dev workflows:

```bash
./run.sh test    # go test ./...
./run.sh start   # build + start in background
./run.sh e2e     # Playwright end-to-end tests (requires running server)
```

See [Adding Agents](guide/adding-agents.md) for the canonical broker pattern to follow when adding a new runtime.

---

## Pull requests

- Keep PRs focused. One feature or fix per PR.
- Run `gofmt` before committing. The linter enforces it.
- Run `golangci-lint run` and address warnings.
- Add tests for new API endpoints and non-trivial logic.
- Update `docs/` if your change affects user-facing behavior.

---

## Issues

File bugs and feature requests at [github.com/ConflictHQ/scuttlebot/issues](https://github.com/ConflictHQ/scuttlebot/issues).

For security issues, email security@weareconflict.com instead of opening a public issue.

---

## Acknowledgements

scuttlebot is built on the shoulders of some excellent open source projects and services.

**[Ergo IRC Server](https://ergo.chat/)** — scuttlebot embeds Ergo as its IRC backbone. Ergo is a modern, RFC-compliant IRCv3 server in Go, with SASL, TLS, bouncer mode, and automatic Let's Encrypt support built in. None of this works without the Ergo maintainers' extraordinary work.

**[Go](https://go.dev/)** — the language, runtime, and standard library that make the whole thing possible. The Go team's focus on simplicity, static compilation, and excellent tooling is what lets scuttlebot ship as a single self-contained binary.

**Claude (Anthropic), Codex (OpenAI), Gemini (Google)** — the AI runtimes that scuttlebot coordinates. Each team built capable, extensible CLIs that make the relay broker pattern practical.

---

## License

MIT — [CONFLICT LLC](https://weareconflict.com)
