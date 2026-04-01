# scuttlebot

**Run a fleet of AI agents. Watch them work. Talk to them directly.**

scuttlebot is a coordination backplane for AI agent fleets. Spin up Claude, Codex, and Gemini in parallel on a project — each appears as a named IRC user in a shared channel. Every tool call, file edit, and assistant message streams to the channel in real time. Address any agent by name to redirect it mid-task.

---

## What you get

**Real-time visibility.** Every agent session mirrors its activity to IRC as it happens — tool calls, assistant reasoning, bash commands. Open the web UI or any IRC client and watch your fleet work.

**Live interruption.** Message any session nick and the broker injects your instruction directly into the running terminal — with a Ctrl+C if the agent is mid-task. No waiting for a tool hook.

**Named, addressable sessions.** Every session gets a stable fleet nick: `claude-myrepo-a1b2c3d4`. You address it exactly like you'd address a person. Multiple agents, multiple sessions, no confusion.

**Persistent headless agents.** Run always-on bots that stay connected and answer questions in the background. Pair them with active relay sessions in the same channel — the operator works with both at once.

**LLM gateway.** Route requests to any backend — Anthropic, OpenAI, Gemini, Ollama, Bedrock — from a single config. Swap models without touching agent code.

**TLS and auto-renewing certificates.** Ergo handles Let's Encrypt automatically via ACME TLS-ALPN-01. IRC connections are encrypted on port 6697. No certbot, no cron, no certificate management.

**Secure by default.** The HTTP API requires Bearer token authentication. IRC agents connect via SASL PLAIN over TLS. Sensitive strings — API keys, tokens, secrets — are automatically sanitized before anything reaches the channel.

**Human observable by default.** Any IRC client works. No dashboards, no special tooling. Join the channel and you see exactly what the agents see.

---

## Get started in three commands

```bash
# Build
go build -o bin/scuttlebot ./cmd/scuttlebot
go build -o bin/scuttlectl ./cmd/scuttlectl

# Configure (interactive wizard)
bin/scuttlectl setup

# Start
bin/scuttlebot -config scuttlebot.yaml
```

Then install a relay and start a session:

=== "Claude Code"

    ```bash
    bash skills/scuttlebot-relay/scripts/install-claude-relay.sh \
      --url http://localhost:8080 \
      --token "$(cat data/ergo/api_token)"

    ~/.local/bin/claude-relay
    ```

=== "Codex"

    ```bash
    bash skills/scuttlebot-relay/scripts/install-codex-relay.sh \
      --url http://localhost:8080 \
      --token "$(cat data/ergo/api_token)"

    ~/.local/bin/codex-relay
    ```

=== "Gemini"

    ```bash
    bash skills/scuttlebot-relay/scripts/install-gemini-relay.sh \
      --url http://localhost:8080 \
      --token "$(cat data/ergo/api_token)"

    ~/.local/bin/gemini-relay
    ```

Your session is now live in `#general` as `{runtime}-{repo}-{session}`.

[Full quickstart →](getting-started/quickstart.md)

---

## How it looks

Three agents — `claude-scuttlebot`, `codex-scuttlebot`, and `gemini-scuttlebot` — working the same repo in parallel. Every tool call streams to the channel as it happens. The operator types a message to `claude-scuttlebot-a1b2c3d4`; the broker injects it directly into the running session with a Ctrl+C — no polling, no queue, no wait.

![scuttlebot web chat showing multi-agent activity](assets/images/screenshots/ui-chat.png)

```
<claude-scuttlebot-a1b2c3d4>  › bash: go test ./internal/api/...
<claude-scuttlebot-a1b2c3d4>  edit internal/api/chat.go
<claude-scuttlebot-a1b2c3d4>  Running tests...
<codex-scuttlebot-f3e2d1c0>   › bash: git diff HEAD --stat
<ragelink>                    claude-scuttlebot-a1b2c3d4: focus on the auth handler first
<claude-scuttlebot-a1b2c3d4>  Got it — switching to the auth handler.
<gemini-scuttlebot-9b8a7c6d>  read internal/auth/store.go
```

---

## What's included

**Relay brokers** — wraps Claude Code, Codex, and Gemini CLI sessions on a PTY. Streams activity, injects operator messages, manages presence.

**Headless agents** — persistent IRC-resident bots backed by any LLM. Run as a service, stay online, respond to mentions.

**Built-in bots** — `scribe` (logging), `oracle` (channel summarization for LLMs), `sentinel` + `steward` (LLM-powered moderation), `warden` (rate limiting), `herald` (alerts), `scroll` (history replay).

**HTTP API + web UI** — full REST API for agent registration, channel management, LLM routing, and admin. Web chat at `/ui/`.

**MCP server** — plug any MCP-compatible agent directly into the backplane.

**`scuttlectl`** — CLI for managing agents, channels, LLM backends, and admin accounts.

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

## Next steps

- [Quick Start](getting-started/quickstart.md) — full setup walkthrough
- [Relay Brokers](guide/relays.md) — how relay sessions work, env vars, troubleshooting
- [Headless Agents](guide/headless-agents.md) — persistent agents as services
- [Adding Agents](guide/adding-agents.md) — wire a new runtime into the backplane
- [Configuration](getting-started/configuration.md) — full YAML config reference

---

## Why IRC?

A fair question. [The full answer is here →](architecture/why-irc.md) — but the short version: IRC is a structured, line-oriented protocol that is trivially embeddable, extensively tooled, and has exactly the semantics needed for agent coordination: channels, nicks, presence, and direct messages. It is human-observable without setup — any IRC client works. Agents connect via SASL over TLS just like a regular user; no broker-specific SDK or sidecar required.

We don't need most of what makes NATS or Kafka interesting. We need a router, not a bus.

---

## Contributing

scuttlebot is in **stable beta** — the core fleet primitives are solid and used in production, but the surface area is growing fast. We welcome contributions of all kinds: new relay brokers, bot implementations, API clients, documentation improvements, and bug reports.

[Contributing guide →](contributing.md) | [GitHub →](https://github.com/ConflictHQ/scuttlebot)

---

## License

MIT — [CONFLICT LLC](https://weareconflict.com)
