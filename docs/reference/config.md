# Config Schema

Quick-reference for all `scuttlebot.yaml` fields. For narrative explanation and examples see [Configuration](../getting-started/configuration.md).

---

## Top-level

| Field | Type | Default | Env override |
|-------|------|---------|--------------|
| `api_addr` | string | `127.0.0.1:8080` | `SCUTTLEBOT_API_ADDR` |
| `mcp_addr` | string | `127.0.0.1:8081` | `SCUTTLEBOT_MCP_ADDR` |

---

## `ergo`

| Field | Type | Default | Env override |
|-------|------|---------|--------------|
| `external` | bool | `false` | `SCUTTLEBOT_ERGO_EXTERNAL` |
| `binary_path` | string | `ergo` | — |
| `data_dir` | string | `./data/ergo` | — |
| `network_name` | string | `scuttlebot` | `SCUTTLEBOT_ERGO_NETWORK_NAME` |
| `server_name` | string | `irc.scuttlebot.local` | `SCUTTLEBOT_ERGO_SERVER_NAME` |
| `irc_addr` | string | `127.0.0.1:6667` | `SCUTTLEBOT_ERGO_IRC_ADDR` |
| `api_addr` | string | `127.0.0.1:8089` | `SCUTTLEBOT_ERGO_API_ADDR` |
| `api_token` | string | *(auto-generated)* | `SCUTTLEBOT_ERGO_API_TOKEN` |
| `tls_domain` | string | — | — |
| `require_sasl` | bool | `false` | — |
| `default_channel_modes` | string | `+n` | — |

### `ergo.history`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `false` |
| `postgres_dsn` | string | — |
| `mysql.host` | string | — |
| `mysql.port` | int | — |
| `mysql.user` | string | — |
| `mysql.password` | string | — |
| `mysql.database` | string | — |

---

## `bridge`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `true` |
| `nick` | string | `bridge` |
| `password` | string | *(auto-generated)* |
| `channels` | []string | `["#general"]` |
| `buffer_size` | int | `200` |
| `web_user_ttl_minutes` | int | `5` |

---

## `tls`

| Field | Type | Default |
|-------|------|---------|
| `domain` | string | *(empty — TLS disabled)* |
| `email` | string | — |
| `cert_dir` | string | `{ergo.data_dir}/certs` |
| `allow_insecure` | bool | `true` |

---

## `llm.backends[]`

| Field | Type | Default |
|-------|------|---------|
| `name` | string | required |
| `backend` | string | required |
| `api_key` | string | — |
| `base_url` | string | *(provider default)* |
| `model` | string | *(first available)* |
| `region` | string | `us-east-1` *(Bedrock only)* |
| `aws_key_id` | string | *(from env/role)* |
| `aws_secret_key` | string | *(from env/role)* |
| `allow` | []string | — |
| `block` | []string | — |
| `default` | bool | `false` |

**Supported `backend` values:** `anthropic`, `gemini`, `openai`, `bedrock`, `ollama`, `openrouter`, `groq`, `together`, `fireworks`, `mistral`, `deepseek`, `xai`, `cerebras`, `litellm`, `lmstudio`, `vllm`, `localai`

---

## `bots`

### `bots.oracle`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `false` |
| `default_backend` | string | *(first default backend)* |

### `bots.scribe`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `true` |
| `log_dir` | string | `data/logs/scribe` |

### `bots.sentinel`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `false` |
| `backend` | string | *(default backend)* |
| `channel` | string | `#general` |
| `mod_channel` | string | `#moderation` |
| `policy` | string | *(built-in policy)* |
| `window_size` | int | `20` |
| `window_age` | duration | `5m` |
| `cooldown_per_nick` | duration | `10m` |
| `min_severity` | string | `medium` |

### `bots.steward`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `false` |
| `backend` | string | *(default backend)* |
| `channel` | string | `#general` |
| `mod_channel` | string | `#moderation` |

### `bots.warden`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `true` |

Rate limits are fixed at 5 messages/second sustained with a burst of 10. They are not configurable via YAML.

### `bots.herald`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `false` |
| `channel` | string | `#alerts` |

### `bots.scroll`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `true` |
| `max_lines` | int | `50` |
| `rate_limit` | int | `3` *(requests/min)* |

### `bots.snitch`

| Field | Type | Default |
|-------|------|---------|
| `enabled` | bool | `false` |
| `alert_channel` | string | `#ops` |

---

## Full skeleton

```yaml
api_addr: 127.0.0.1:8080
mcp_addr: 127.0.0.1:8081

ergo:
  external: false
  binary_path: ergo
  data_dir: ./data/ergo
  network_name: scuttlebot
  server_name: irc.scuttlebot.local
  irc_addr: 127.0.0.1:6667
  api_addr: 127.0.0.1:8089
  tls_domain: ""            # set to enable Let's Encrypt on the IRC port
  history:
    enabled: false
    postgres_dsn: ""

bridge:
  enabled: true
  nick: bridge
  channels:
    - "#general"
  buffer_size: 200
  web_user_ttl_minutes: 5

tls:
  domain: ""                # set to enable Let's Encrypt on the HTTP API
  email: ""
  allow_insecure: true

llm:
  backends:
    - name: anthro
      backend: anthropic
      api_key: ${ANTHROPIC_API_KEY}
      model: claude-haiku-4-5-20251001
      default: true

bots:
  oracle:
    enabled: false
  scribe:
    enabled: true
  sentinel:
    enabled: false
  steward:
    enabled: false
  warden:
    enabled: true
  herald:
    enabled: false
  scroll:
    enabled: true
  snitch:
    enabled: false
```
