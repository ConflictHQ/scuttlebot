# Changelog

## v1.2.0

### Features

- **Group addressing** — `@all`, `@worker`/`@observer`/`@operator` (by role), and `@prefix-*` (e.g. `@claude-*`, `@claude-kohakku-*`) group mentions in IRC channels. All matching agents receive the message as an interrupt.
- **Agent presence tracking** — `online`, `last_seen` fields on agents. Green/yellow/gray status dots in the UI. Configurable online timeout (Settings > Agent Policy).
- **Stale agent cleanup** — configurable `reap_after_days` in agent policy. Agents not seen in N days are automatically removed. Runs hourly.
- **Persist `last_seen` across restarts** — `last_seen` stored in SQLite, survives server restarts. Persisted at most once per minute to avoid disk thrashing.
- **Relay reconnection** — `relay-watchdog` sidecar monitors the server and sends SIGUSR1 to relays when the server restarts or the API is unreachable for 60s. All three relays (claude, codex, gemini) handle SIGUSR1 by tearing down IRC and reconnecting with fresh SASL credentials.
- **Per-repo channel config** — `.scuttlebot.yaml` in a project root auto-joins the project channel. Gitignored, relay reads it at startup.
- **TLS dual-listener** — Ergo config supports `tls_domain` + `tls_addr` for a public TLS IRC listener alongside the plaintext loopback for internal bots.
- **PATCH /v1/settings/policies** — partial policy updates without wiping other sections.
- **Configurable online timeout** — Settings > Agent Policy > online timeout (seconds).
- **LLM backend rename** — edit a backend's name in the AI tab (delete + create under the hood).
- **OpenClaw integration skill** — native IRC connection guide for OpenClaw agents.
- **Project setup skill** — standardized onboarding for new projects to the coordination backplane.
- **`relay-start.sh`** — wrapper script that starts watchdog + relay together.

### UI

- **Mobile responsive** — full `@media (max-width: 600px)` breakpoint. Scrollable nav, stacked grids, overlay chat panels, compact header.
- **Agent presence indicators** — green (online), yellow (idle), gray (offline), red (revoked) dots. Sorted online-first, with relative `last_seen` times.
- **Pagination + filtering** on agents tab — status filter (all/online/offline/revoked), text search, 25 per page.
- **Channel search** on channels tab.
- **Chat layout toggle** — inline (compact) vs columnar (traditional IRC) layout, persisted in localStorage.
- **Tighter chat spacing** — reduced padding, gaps, and line height globally.

### Fixes

- **Bridge channels** — normalize channel names with `#` prefix so the bridge actually joins configured channels.
- **Bot `splitHostPort`** — fix `fmt.Sscanf` parser in 5 bot packages; use `net.SplitHostPort` from stdlib.
- **Topology nil panic** — guard all topology API handlers against nil topology manager.
- **API fetch caching** — `cache: 'no-store'` on all UI API calls to prevent stale 301 redirect caching.
- **Aggressive IRC keepalive** — `PingDelay=30s`, `PingTimeout=30s` on all girc clients (relay + 11 bots).
- **SASL credential refresh** — relay clears stale credentials and re-registers on reconnect.
