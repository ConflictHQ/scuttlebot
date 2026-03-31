# Docker Compose deployment

Three-service stack: **ergo** (IRC server) + **scuttlebot** (daemon) + **postgres** (state + IRC history).

## Quick start

```sh
cd deploy/compose

# 1. Create your .env
cp .env.example .env
# Edit .env — set ERGO_API_TOKEN to a random secret.
# Generate one: openssl rand -hex 32

# 2. Boot
docker compose up --build

# 3. The scuttlebot API token is printed in scuttlebot's startup logs.
docker compose logs scuttlebot | grep "api token"
```

## Services

| Service     | Internal port | Published by default | Notes |
|-------------|---------------|----------------------|-------|
| postgres    | 5432          | No (override: yes)   | State store + ergo IRC history |
| ergo        | 6667 (IRC)    | Yes                  | IRC server |
| ergo        | 8089 (API)    | No (override: yes)   | Ergo management API |
| scuttlebot  | 8080          | Yes                  | REST management API |

## Environment variables

See `.env.example` for the full list. Only `ERGO_API_TOKEN` is required — everything else has a default.

## Persistence

Data survives container restarts via named Docker volumes:

- `ergo_data` — Ergo's embedded database (`ircd.db`)
- `postgres_data` — Postgres data directory

To reset completely: `docker compose down -v`

## Local dev overrides

`docker-compose.override.yml` is applied automatically and:
- Exposes postgres on `5432` and the ergo API on `8089` to localhost
- Disables ergo persistent history (faster startup)
- Sets debug log level on scuttlebot

To run without overrides (production-like):

```sh
docker compose -f docker-compose.yml up
```

## Architecture

```
                ┌─────────────┐
    IRC clients │    ergo     │ :6667
    ───────────>│  IRC server │
                └──────┬──────┘
                       │ HTTP API :8089
                ┌──────▼──────┐
                │ scuttlebot  │ :8080
                │   daemon    │<──── REST API (agent registration, etc.)
                └──────┬──────┘
                       │
                ┌──────▼──────┐
                │  postgres   │ :5432
                └─────────────┘
```

Ergo runs as a separate container. Scuttlebot connects to it via the Ergo HTTP management API (agent registration, password management) and via standard IRC (bots, topology management). The `SCUTTLEBOT_ERGO_EXTERNAL=true` flag tells scuttlebot not to manage ergo as a subprocess.
