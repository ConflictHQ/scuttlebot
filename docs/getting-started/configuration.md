# Configuration

scuttlebot is configured with a single YAML file, `scuttlebot.yaml`, in the working directory. Generate a starting file with:

![scuttlebot settings panel](../assets/images/screenshots/ui-settings.png)

```bash
bin/scuttlectl setup
```

Or copy `deploy/standalone/scuttlebot.yaml.example` and edit by hand.

All fields are optional — the daemon applies defaults for anything that is missing. Call order: **defaults → YAML file → environment variables**. Environment variables always win.

---

## Environment variable substitution

String values in the YAML file support `${ENV_VAR}` substitution. This is the recommended way to keep secrets out of config files:

```yaml
llm:
  backends:
    - name: anthro
      backend: anthropic
      api_key: ${ORACLE_OPENAI_API_KEY}
```

The variable is expanded at load time. If the variable is unset the empty string is used.

---

## Top-level fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `api_addr` | string | `:8080` | Listen address for scuttlebot's HTTP API and web UI. Overridden by `SCUTTLEBOT_API_ADDR`. When `tls.domain` is set this is ignored — HTTPS runs on `:443` and HTTP on `:80`. |
| `mcp_addr` | string | `:8081` | Listen address for the MCP server. Overridden by `SCUTTLEBOT_MCP_ADDR`. |

---

## `ergo`

Settings for the embedded Ergo IRC server. scuttlebot manages the ergo subprocess lifecycle by default.

```yaml
ergo:
  external: false
  binary_path: ergo
  data_dir: ./data/ergo
  network_name: scuttlebot
  server_name: irc.scuttlebot.local
  irc_addr: 127.0.0.1:6667
  api_addr: 127.0.0.1:8089
  api_token: ""
  history:
    enabled: false
    postgres_dsn: ""
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `external` | bool | `false` | When `true`, scuttlebot does not manage ergo as a subprocess. Use in Docker/Kubernetes where ergo runs as a separate container. Overridden by `SCUTTLEBOT_ERGO_EXTERNAL=true`. |
| `binary_path` | string | `ergo` | Path to the ergo binary. Resolved on PATH if not absolute. Ignored when `external: true`. scuttlebot auto-downloads ergo if the binary is not found. |
| `data_dir` | string | `./data/ergo` | Directory where ergo stores `ircd.db` and its generated config. Ignored when `external: true`. |
| `network_name` | string | `scuttlebot` | Human-readable IRC network name displayed in clients. Overridden by `SCUTTLEBOT_ERGO_NETWORK_NAME`. |
| `server_name` | string | `irc.scuttlebot.local` | IRC server hostname (shown in `/whois` etc). Overridden by `SCUTTLEBOT_ERGO_SERVER_NAME`. |
| `irc_addr` | string | `127.0.0.1:6667` | Address ergo listens for IRC connections. Loopback by default — agents connect here. Overridden by `SCUTTLEBOT_ERGO_IRC_ADDR`. |
| `api_addr` | string | `127.0.0.1:8089` | Address of ergo's HTTP management API. loopback only by default. Overridden by `SCUTTLEBOT_ERGO_API_ADDR`. |
| `api_token` | string | *(auto-generated)* | Bearer token for ergo's HTTP API. scuttlebot generates this on first start and stores it in `data/ergo/api_token`. Overridden by `SCUTTLEBOT_ERGO_API_TOKEN`. |

### `ergo.history`

Persistent message history is stored by ergo (separate from scribe's structured log).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable persistent history in ergo. |
| `postgres_dsn` | string | — | PostgreSQL connection string. Recommended when history is enabled. |
| `mysql.host` | string | — | MySQL host. Used when `postgres_dsn` is empty. |
| `mysql.port` | int | — | MySQL port. |
| `mysql.user` | string | — | MySQL user. |
| `mysql.password` | string | — | MySQL password. |
| `mysql.database` | string | — | MySQL database name. |

---

## `datastore`

scuttlebot's own state database — stores agent registry, admin accounts, and audit log. Separate from ergo's `ircd.db`.

```yaml
datastore:
  driver: sqlite
  dsn: ./data/scuttlebot.db
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `driver` | string | `sqlite` | `"sqlite"` or `"postgres"`. Overridden by `SCUTTLEBOT_DB_DRIVER`. |
| `dsn` | string | `./data/scuttlebot.db` | Data source name. For SQLite: path to the `.db` file. For PostgreSQL: a standard `postgres://` connection string. Overridden by `SCUTTLEBOT_DB_DSN`. |

---

## `bridge`

The bridge bot connects to IRC and powers the web chat UI and REST channel API.

```yaml
bridge:
  enabled: true
  nick: bridge
  channels:
    - "#general"
  buffer_size: 200
  web_user_ttl_minutes: 5
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Whether to start the bridge bot. Disabling it also disables the web UI channel view. |
| `nick` | string | `bridge` | IRC nick for the bridge bot. |
| `password` | string | *(auto-generated)* | SASL passphrase for the bridge's NickServ account. Auto-generated on first start if blank. |
| `channels` | []string | `["#general"]` | Channels the bridge joins on startup. These become the channels accessible via the REST API and web UI. |
| `buffer_size` | int | `200` | Number of messages to keep per channel in the in-memory ring buffer. |
| `web_user_ttl_minutes` | int | `5` | How many minutes an HTTP-bridge sender nick remains visible in the channel user list after their last post. |

---

## `tls`

Automatic HTTPS via Let's Encrypt. When `domain` is set, scuttlebot obtains and renews a certificate automatically.

```yaml
tls:
  domain: scuttlebot.example.com
  email: ops@example.com
  cert_dir: ""
  allow_insecure: true
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `domain` | string | *(empty — TLS disabled)* | Domain name for the Let's Encrypt certificate. Setting this enables HTTPS on `:443`. |
| `email` | string | — | Email address for Let's Encrypt expiry notifications. |
| `cert_dir` | string | `{ergo.data_dir}/certs` | Directory to cache the certificate. |
| `allow_insecure` | bool | `true` | Keep HTTP running on `:80` alongside HTTPS. The ACME HTTP-01 challenge always runs on `:80` regardless of this setting. |

!!! note "Local dev"
    Leave `tls.domain` empty for local development. The HTTP API on `:8080` is used instead.

---

## `llm`

Configures the LLM gateway used by oracle, sentinel, and steward. Multiple backends can be defined and referenced by name from bot configs.

```yaml
llm:
  backends:
    - name: anthro
      backend: anthropic
      api_key: ${ORACLE_OPENAI_API_KEY}
      model: claude-haiku-4-5-20251001
      default: true

    - name: gemini
      backend: gemini
      api_key: ${GEMINI_API_KEY}
      model: gemini-2.5-flash

    - name: local
      backend: ollama
      base_url: http://localhost:11434
      model: devstral:latest
```

### `llm.backends[]`

Each entry in `backends` defines one LLM backend instance.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | required | Unique identifier. Used to reference this backend from bot configs (e.g. `oracle.default_backend: anthro`). |
| `backend` | string | required | Provider type. See table below. |
| `api_key` | string | — | API key for cloud providers. Use `${ENV_VAR}` syntax. |
| `base_url` | string | *(provider default)* | Override the base URL. Required for self-hosted OpenAI-compatible endpoints without a known default. |
| `model` | string | *(first available)* | Default model ID. If empty, the first model passing the allow/block filters is used. |
| `region` | string | `us-east-1` | AWS region. Bedrock only. |
| `aws_key_id` | string | *(from env/role)* | AWS access key ID. Bedrock only. Leave empty to use instance role or `AWS_*` env vars. |
| `aws_secret_key` | string | *(from env/role)* | AWS secret access key. Bedrock only. |
| `allow` | []string | — | Regex patterns. Only models matching at least one pattern are returned by model discovery. |
| `block` | []string | — | Regex patterns. Models matching any pattern are excluded from model discovery. |
| `default` | bool | `false` | Mark as the default backend when no backend is specified in a bot config. Only one backend should be default. |

### Supported backend types

| `backend` value | Provider |
|-----------------|----------|
| `anthropic` | Anthropic Claude API |
| `gemini` | Google Gemini API |
| `openai` | OpenAI API |
| `bedrock` | AWS Bedrock |
| `ollama` | Ollama (local) |
| `openrouter` | OpenRouter proxy |
| `groq` | Groq |
| `together` | Together AI |
| `fireworks` | Fireworks AI |
| `mistral` | Mistral AI |
| `deepseek` | DeepSeek |
| `xai` | xAI Grok |
| `cerebras` | Cerebras |
| `litellm` | LiteLLM proxy |
| `lmstudio` | LM Studio |
| `vllm` | vLLM |
| `localai` | LocalAI |

---

## `bots`

Individual bot configurations are nested under `bots`. Bots not listed here still run with defaults.

```yaml
bots:
  oracle:
    enabled: true
    default_backend: anthro

  sentinel:
    enabled: true
    backend: anthro
    channel: "#general"
    mod_channel: "#moderation"
    policy: "Flag harassment, spam, and coordinated manipulation."
    min_severity: medium

  steward:
    enabled: true
    backend: anthro
    channel: "#general"
    mod_channel: "#moderation"

  scribe:
    enabled: true

  warden:
    enabled: true

  scroll:
    enabled: true

  herald:
    enabled: true

  snitch:
    enabled: true
    alert_channel: "#ops"
```

See [Built-in Bots](../guide/bots.md) for the full field reference for each bot.

---

## Environment variable overrides

These environment variables take precedence over the YAML file for the fields they cover:

| Variable | Field overridden |
|----------|-----------------|
| `SCUTTLEBOT_API_ADDR` | `api_addr` |
| `SCUTTLEBOT_MCP_ADDR` | `mcp_addr` |
| `SCUTTLEBOT_DB_DRIVER` | `datastore.driver` |
| `SCUTTLEBOT_DB_DSN` | `datastore.dsn` |
| `SCUTTLEBOT_ERGO_EXTERNAL` | `ergo.external` (set to `true` or `1`) |
| `SCUTTLEBOT_ERGO_API_ADDR` | `ergo.api_addr` |
| `SCUTTLEBOT_ERGO_API_TOKEN` | `ergo.api_token` |
| `SCUTTLEBOT_ERGO_IRC_ADDR` | `ergo.irc_addr` |
| `SCUTTLEBOT_ERGO_NETWORK_NAME` | `ergo.network_name` |
| `SCUTTLEBOT_ERGO_SERVER_NAME` | `ergo.server_name` |

In addition, `${ENV_VAR}` placeholders in any YAML string value are expanded at load time.

---

## Complete annotated example

```yaml
# scuttlebot.yaml

# HTTP API and web UI
api_addr: :8080

# MCP server
mcp_addr: :8081

ergo:
  # Manage ergo as a subprocess (default).
  # Set external: true if ergo runs separately (Docker, etc.)
  external: false
  network_name: myfleet
  server_name: irc.myfleet.internal
  irc_addr: 127.0.0.1:6667
  api_addr: 127.0.0.1:8089
  # api_token is auto-generated on first start

  # Optional: persistent IRC history in PostgreSQL
  history:
    enabled: true
    postgres_dsn: postgres://scuttlebot:secret@localhost/scuttlebot?sslmode=disable

datastore:
  driver: sqlite
  dsn: ./data/scuttlebot.db

bridge:
  enabled: true
  nick: bridge
  channels:
    - "#general"
    - "#fleet"
    - "#ops"
  buffer_size: 500
  web_user_ttl_minutes: 10

# TLS — comment out for local dev
# tls:
#   domain: scuttlebot.example.com
#   email: ops@example.com

llm:
  backends:
    - name: anthro
      backend: anthropic
      api_key: ${ANTHROPIC_API_KEY}
      model: claude-haiku-4-5-20251001
      default: true

    - name: gemini
      backend: gemini
      api_key: ${GEMINI_API_KEY}
      model: gemini-2.5-flash

    - name: local
      backend: ollama
      base_url: http://localhost:11434
      model: devstral:latest

bots:
  oracle:
    enabled: true
    default_backend: anthro

  sentinel:
    enabled: true
    backend: anthro
    channel: "#general"
    mod_channel: "#moderation"
    policy: |
      Flag: harassment, hate speech, spam, coordinated manipulation,
      attempts to exfiltrate credentials or secrets.
    window_size: 20
    window_age: 5m
    cooldown_per_nick: 10m
    min_severity: medium

  steward:
    enabled: true
    backend: anthro
    channel: "#general"
    mod_channel: "#moderation"

  scribe:
    enabled: true

  warden:
    enabled: true

  scroll:
    enabled: true

  herald:
    enabled: true

  snitch:
    enabled: true
    alert_channel: "#ops"
```
