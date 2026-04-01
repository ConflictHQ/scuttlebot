---
name: Scuttlebot current state
description: What's built, what's wired, what's tested as of end of March 2026 sprint
type: project
---

All 8 bots fully implemented and wired through the manager. Oracle reads history from scribe's log files via scribeHistoryAdapter. Login screen with username/password auth. Admin account management via CLI and web UI.

**Why:** Major feature push to complete implementations and improve the web chat experience.

**How to apply:** The core backplane is feature-complete for v1. Next focus areas would be polish, production hardening, and the Kohakku agent dispatch system.

## What's complete
- All 8 system bots: auditbot, herald, oracle, scribe, scroll, snitch, systembot, warden
- Bot manager: starts/stops bots dynamically on policy change
- Login screen: username/password auth via POST /login, rate limited 10/min per IP
- Admin accounts: bcrypt, persisted to JSON, managed via scuttlectl admin + web UI
- Web UI: login screen, message grouping, nick colors, unread badge, auto-scroll banner, admin card
- Logging: jsonl/csv/text formats, daily/weekly/monthly/yearly/size rotation, per-channel files, age pruning
- TLS: Let's Encrypt via tlsDomain config
- MCP server for AI agent connectivity
- run.sh dev helper

## Test coverage
- internal/auth — AdminStore (persistence, auth, CRUD)
- internal/api — login handler, rate limiting, admin endpoints, auth required
- internal/bots/manager — Sync, password persistence, start/stop/idempotency
- internal/bots/snitch — nickWindow trim, flood counting, join/part threshold (internal tests)
- internal/bots/scribe — FileStore (jsonl/csv/text, rotation, per-channel, prune)
- All other bots have construction-level tests

## What's complete (added)
- `internal/llm/` omnibus LLM gateway — Provider interface, ModelDiscoverer interface, ModelFilter (regex allow/block), factory `llm.New(BackendConfig)`
- Native providers: Anthropic (Messages API + static model list), Google Gemini (generateContent + /v1beta/models discovery), AWS Bedrock (Converse API + SigV4 signing + foundation-models discovery), Ollama (/api/generate + /api/tags discovery)
- OpenAI-compatible: openai, openrouter, together, groq, fireworks, mistral, ai21, huggingface, deepseek, cerebras, xai, litellm, lmstudio, jan, localai, vllm, anythingllm
- `internal/config/config.go` — `LLMConfig` with `[]LLMBackendConfig` (name, backend, api_key, model, region, aws credentials, allow/block regex lists)
- API endpoints: `GET /v1/llm/known`, `GET /v1/llm/backends`, `GET /v1/llm/backends/{name}/models`
- UI: AI tab with configured backend list, per-backend model discovery, supported backends reference, YAML example card
- Oracle manager buildBot now uses `llm.New()` with configurable `backend` field

## Known remaining
- Kohakku (agent dispatch system) — not yet started
- Per-session tokens (login returns shared server token — fine for v1)
- scuttlectl / apiclient have no tests
- `internal/llm` has no tests yet
