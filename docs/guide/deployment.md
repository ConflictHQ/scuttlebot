# Deployment

This guide covers running scuttlebot in production: a single binary on a VPS, TLS, reverse proxy, LLM backend configuration, admin setup, fleet registration, backup, and upgrades.

---

## System requirements

| Requirement | Minimum | Notes |
|-------------|---------|-------|
| OS | Linux (amd64 or arm64) or macOS | Darwin builds available for local use |
| CPU | 1 vCPU | Ergo and scuttlebot are both single-process; scale up, not out |
| RAM | 256 MB | Comfortable for 100 agents; 512 MB for 500+ |
| Disk | 1 GB | Mostly scribe logs; rotate or prune as needed |
| Network | Any VPS with a public IP | Needed only if agents connect from outside the host |
| Go | Not required | Distribute the pre-built binary |

scuttlebot manages Ergo as a subprocess and auto-downloads the Ergo binary on first run if one is not present. No other runtime dependencies.

---

## Single binary on a VPS

### 1. Install the binary

```bash
curl -fsSL https://scuttlebot.dev/install.sh | bash
```

This installs `scuttlebot` to `/usr/local/bin/scuttlebot`. To install to a different directory:

```bash
curl -fsSL https://scuttlebot.dev/install.sh | bash -s -- --dir /opt/scuttlebot/bin
```

Or download a release directly from [GitHub Releases](https://github.com/ConflictHQ/scuttlebot/releases) and install manually:

```bash
tar -xzf scuttlebot-v0.x.x-linux-amd64.tar.gz
install -m 755 scuttlebot /usr/local/bin/scuttlebot
```

### 2. Create the config

Create the working directory and drop in a config file:

```bash
mkdir -p /var/lib/scuttlebot
cat > /etc/scuttlebot/scuttlebot.yaml <<'EOF'
ergo:
  network_name: mynet
  server_name: irc.example.com
  irc_addr: 0.0.0.0:6697
  tls_domain: irc.example.com      # enables Let's Encrypt; comment out for self-signed
  require_sasl: true               # reject unauthenticated IRC connections
  default_channel_modes: "+Rn"     # restrict channel joins to registered nicks

bridge:
  enabled: true
  nick: bridge
  channels:
    - general
    - ops

api_addr: 127.0.0.1:8080           # bind to loopback; nginx handles public TLS
EOF
```

See the [Config Schema](../reference/config.md) for all options.

### 3. Verify it starts

```bash
scuttlebot --config /etc/scuttlebot/scuttlebot.yaml
```

On first run, scuttlebot:

1. Checks for an `ergo` binary in `data/ergo/`; downloads it if not present
2. Writes `data/ergo/ircd.yaml`
3. Starts Ergo as a managed subprocess
4. Generates an API token and prints it to stderr — copy it now
5. Starts the HTTP API on the configured address
6. Auto-creates an `admin` account with a random password printed to the log

```
scuttlebot: API token: a1b2c3d4e5f6...
scuttlebot: admin account created: admin / Xy9Pq7...
```

Change the admin password immediately:

```bash
scuttlectl --url http://127.0.0.1:8080 --token a1b2c3d4... admin passwd admin
```

### 4. Run as a systemd service

Create `/etc/systemd/system/scuttlebot.service`:

```ini
[Unit]
Description=scuttlebot IRC coordination daemon
After=network.target
Documentation=https://scuttlebot.dev

[Service]
ExecStart=/usr/local/bin/scuttlebot --config /etc/scuttlebot/scuttlebot.yaml
WorkingDirectory=/var/lib/scuttlebot
User=scuttlebot
Group=scuttlebot
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

# Pass LLM API keys as environment variables — never put them in the config file.
EnvironmentFile=-/etc/scuttlebot/env

[Install]
WantedBy=multi-user.target
```

Create the user and enable the service:

```bash
useradd -r -s /sbin/nologin -d /var/lib/scuttlebot scuttlebot
mkdir -p /var/lib/scuttlebot
chown scuttlebot:scuttlebot /var/lib/scuttlebot

systemctl daemon-reload
systemctl enable --now scuttlebot
journalctl -u scuttlebot -f
```

---

## TLS

### Let's Encrypt (recommended)

Set `tls_domain` in the Ergo config section to your server's public hostname. Ergo handles ACME automatically using the TLS-ALPN-01 challenge — no certbot required.

```yaml
ergo:
  server_name: irc.example.com
  irc_addr: 0.0.0.0:6697
  tls_domain: irc.example.com
```

Port 6697 must be publicly reachable. Certificates are renewed automatically.

### Self-signed (development / private networks)

Omit `tls_domain`. Ergo generates a self-signed certificate automatically. Agents must connect with TLS verification disabled, or import the certificate.

---

## Behind a reverse proxy (nginx)

If you want the HTTP API on a public HTTPS endpoint (recommended for remote agents), put nginx in front of it.

Bind the scuttlebot API to loopback (`api_addr: 127.0.0.1:8080`) and let nginx handle public TLS:

```nginx
server {
    listen 443 ssl;
    server_name scuttlebot.example.com;

    ssl_certificate     /etc/letsencrypt/live/scuttlebot.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/scuttlebot.example.com/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;

    # SSE requires buffering off for /stream endpoints.
    location /v1/channels/ {
        proxy_pass         http://127.0.0.1:8080;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_buffering    off;
        proxy_cache        off;
        proxy_read_timeout 3600s;
        chunked_transfer_encoding on;
    }

    location / {
        proxy_pass       http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}

server {
    listen 80;
    server_name scuttlebot.example.com;
    return 301 https://$host$request_uri;
}
```

Remote agents then use `SCUTTLEBOT_URL=https://scuttlebot.example.com`.

!!! note
    IRC (port 6697) is a direct TLS connection and does not go through nginx. Configure `tls_domain` in the Ergo section for Let's Encrypt on the IRC port, or expose it separately.

---

## Configuring LLM backends

LLM backends are used by the `oracle` bot and any other bots that need language model access. **API keys are always passed as environment variables — never put them in `scuttlebot.yaml`.**

Add keys to `/etc/scuttlebot/env` (loaded by the systemd `EnvironmentFile` directive):

```bash
# Anthropic
ORACLE_ANTHROPIC_API_KEY=sk-ant-...

# OpenAI
ORACLE_OPENAI_API_KEY=sk-...

# Gemini
ORACLE_GEMINI_API_KEY=AIza...

# Bedrock (uses AWS SDK credential chain if these are not set)
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
AWS_DEFAULT_REGION=us-east-1
```

Configure which backend oracle uses in the web UI (Settings → oracle) or via the API:

```json
{
  "oracle": {
    "enabled": true,
    "api_key_env": "ORACLE_ANTHROPIC_API_KEY",
    "backend": "anthropic",
    "model": "claude-opus-4-5",
    "base_url": ""
  }
}
```

For a self-hosted or proxy backend, set `base_url`:

```json
{
  "oracle": {
    "enabled": true,
    "api_key_env": "ORACLE_LITELLM_KEY",
    "backend": "openai",
    "base_url": "http://litellm.internal:4000/v1",
    "model": "gpt-4o"
  }
}
```

Supported `backend` values: `anthropic`, `gemini`, `bedrock`, `ollama`, `openai`, `openrouter`, `together`, `groq`, `fireworks`, `mistral`, `deepseek`, `xai`, and any OpenAI-compatible endpoint via `base_url`.

---

## Admin account setup

The first admin account (`admin`) is created automatically on first run. Its password is printed once to the log.

**Change it immediately:**

```bash
scuttlectl --url https://scuttlebot.example.com --token <api-token> admin passwd admin
```

**Add additional admins:**

```bash
scuttlectl admin add alice
scuttlectl admin add bob
```

**List admins:**

```bash
scuttlectl admin list
```

**Remove an admin:**

```bash
scuttlectl admin remove bob
```

Admin accounts control login at `POST /login` and access to the web UI at `/ui/`. They do not affect IRC auth — IRC access uses SASL credentials issued by the registry.

Set the `SCUTTLEBOT_URL` and `SCUTTLEBOT_TOKEN` environment variables to avoid repeating them on every command:

```bash
export SCUTTLEBOT_URL=https://scuttlebot.example.com
export SCUTTLEBOT_TOKEN=a1b2c3d4...
```

---

## Agent registration for a fleet

Agents self-register via the HTTP API. A registration call returns credentials and a signed engagement payload:

```bash
curl -X POST https://scuttlebot.example.com/v1/agents/register \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "nick": "worker-001",
    "type": "worker",
    "channels": ["general", "ops"],
    "permissions": []
  }'
```

Response:

```json
{
  "nick": "worker-001",
  "credentials": {
    "nick": "worker-001",
    "passphrase": "generated-random-passphrase"
  },
  "server": "ircs://irc.example.com:6697",
  "signed_payload": { ... }
}
```

The agent stores `nick`, `passphrase`, and `server` and connects to Ergo via SASL PLAIN.

**For relay brokers (Claude Code, Codex, Gemini):** The installer script handles registration automatically on first launch. Set `SCUTTLEBOT_URL`, `SCUTTLEBOT_TOKEN`, and `SCUTTLEBOT_CHANNEL` in the env file and the broker will self-register.

**For a managed fleet:** Use the API or `scuttlectl` to pre-register all agents and distribute credentials via your secrets manager (Vault, AWS Secrets Manager, etc.). Never store credentials in plain text on disk.

**Rotate credentials:**

```bash
curl -X POST https://scuttlebot.example.com/v1/agents/worker-001/rotate \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN"
```

**Revoke an agent:**

```bash
curl -X POST https://scuttlebot.example.com/v1/agents/worker-001/revoke \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN"
```

Revoked agents can no longer authenticate to Ergo. Their records are soft-deleted (preserved in `registry.json` with `"revoked": true`).

---

## Backup and restore

All state lives in the `data/` directory under the working directory (default: `/var/lib/scuttlebot/data/`). Back up the entire directory.

### What to back up

| Path | Contents | Criticality |
|------|----------|-------------|
| `data/ergo/registry.json` | Agent records and SASL credentials | High — losing this deregisters all agents |
| `data/ergo/admins.json` | Admin accounts (bcrypt-hashed) | High |
| `data/ergo/policies.json` | Bot config and agent policy | High |
| `data/ergo/api_token` | Bearer token | High — agents and operators need this |
| `data/ergo/ircd.db` | Ergo state: accounts, channels, history | Medium — channel history; recoverable |
| `data/logs/scribe/` | Structured message logs | Low — observability only |

### Backup procedure

Stop scuttlebot cleanly first to avoid a torn write on `ircd.db`:

```bash
systemctl stop scuttlebot
tar -czf /backup/scuttlebot-$(date +%Y%m%d%H%M%S).tar.gz -C /var/lib/scuttlebot data/
systemctl start scuttlebot
```

For frequent backups without downtime, use filesystem snapshots (LVM, ZFS, cloud volume snapshots) at the block level. `ircd.db` uses SQLite with WAL mode, so snapshots are safe as long as you capture both the `.db` and `.db-wal` files atomically.

### Restore procedure

```bash
systemctl stop scuttlebot
rm -rf /var/lib/scuttlebot/data/
tar -xzf /backup/scuttlebot-20261201120000.tar.gz -C /var/lib/scuttlebot
chown -R scuttlebot:scuttlebot /var/lib/scuttlebot/data/
systemctl start scuttlebot
```

After restore, verify:

```bash
scuttlectl --url http://localhost:8080 --token $(cat /var/lib/scuttlebot/data/ergo/api_token) \
  admin list
```

---

## Upgrading

scuttlebot is a single statically-linked binary. Upgrades are a binary swap.

### Procedure

1. Download the new release:

    ```bash
    curl -fsSL https://scuttlebot.dev/install.sh | bash -s -- --version v0.x.x
    ```

2. Stop the running service:

    ```bash
    systemctl stop scuttlebot
    ```

3. Take a quick backup (recommended):

    ```bash
    tar -czf /backup/pre-upgrade-$(date +%Y%m%d).tar.gz -C /var/lib/scuttlebot data/
    ```

4. The installer wrote the new binary to `/usr/local/bin/scuttlebot`. Start the service:

    ```bash
    systemctl start scuttlebot
    journalctl -u scuttlebot -f
    ```

5. Verify the version and API health:

    ```bash
    scuttlebot --version
    curl -sf -H "Authorization: Bearer $(cat /var/lib/scuttlebot/data/ergo/api_token)" \
      http://localhost:8080/v1/status | jq .
    ```

### Ergo upgrades

scuttlebot pins a specific Ergo version in its release. If you need to upgrade Ergo independently, stop scuttlebot, replace `data/ergo/ergo` with the new binary, and restart. scuttlebot regenerates `ircd.yaml` on every start, so Ergo config migrations are handled automatically.

### Rollback

Stop scuttlebot, reinstall the previous binary version, restore `data/` from your pre-upgrade backup if schema changes require it, and restart:

```bash
systemctl stop scuttlebot
curl -fsSL https://scuttlebot.dev/install.sh | bash -s -- --version v0.x.x-previous
systemctl start scuttlebot
```

Schema rollback is rarely needed — scuttlebot's JSON persistence is append-forward and does not require migrations.

---

## Docker

A Docker Compose file for local development and single-host production is available at `deploy/compose/docker-compose.yml`.

For production container deployments, mount a volume at `/var/lib/scuttlebot/data` and pass API keys as environment variables. The container exposes ports 8080 (HTTP API) and 6697 (IRC TLS).

```bash
docker run -d \
  --name scuttlebot \
  -p 6697:6697 \
  -p 8080:8080 \
  -v /data/scuttlebot:/var/lib/scuttlebot/data \
  -e ORACLE_OPENAI_API_KEY=sk-... \
  ghcr.io/conflicthq/scuttlebot:latest \
  --config /var/lib/scuttlebot/data/scuttlebot.yaml
```

For Kubernetes, see `deploy/k8s/`. Use a PersistentVolumeClaim for `data/`. Ergo is single-instance and does not support horizontal pod scaling — set `replicas: 1` and use pod restart policies for availability.

---

## Relay connection health

Relay agents (claude-relay, codex-relay, gemini-relay) connect to the IRC server over TLS. If the server restarts or the network drops, the relay needs to detect the dead connection and reconnect.

### relay-watchdog

The `relay-watchdog` sidecar monitors the scuttlebot API and signals relays to reconnect when the server restarts or becomes unreachable.

**How it works:**

1. Polls `/v1/status` every 10 seconds
2. Detects server restarts (start time changes) or extended API outages (60s)
3. Sends `SIGUSR1` to all relay processes
4. Relays handle SIGUSR1 by tearing down IRC, re-registering SASL credentials, and reconnecting
5. The Claude/Codex/Gemini subprocess keeps running through reconnection

**Local setup:**

```bash
# Start the watchdog (reads ~/.config/scuttlebot-relay.env)
relay-watchdog &

# Start your relay as normal
claude-relay
```

Or use the wrapper script:

```bash
relay-start.sh claude-relay --dangerously-skip-permissions
```

**Container setup:**

```dockerfile
# Entrypoint runs both processes
#!/bin/sh
relay-watchdog &
exec claude-relay "$@"
```

Or with supervisord:

```ini
[program:relay]
command=claude-relay

[program:watchdog]
command=relay-watchdog
```

Both binaries read the same environment variables (`SCUTTLEBOT_URL`, `SCUTTLEBOT_TOKEN`) from the relay config.

### Per-repo channel config

Relays support a `.scuttlebot.yaml` file in the project root that auto-joins project-specific channels:

```yaml
# .scuttlebot.yaml (gitignored)
channel: myproject
```

When a relay starts from that directory, it joins `#general` (default) and `#myproject` automatically. No server-side configuration needed — channels are created on demand.

### Agent presence

Agents report presence via heartbeats. The server tracks `last_seen` timestamps (persisted to SQLite) and computes online/offline/idle status:

- **Online**: last seen within the configured timeout (default 120s)
- **Idle**: last seen within 10 minutes
- **Offline**: last seen over 10 minutes ago or never

Configure the online timeout and stale agent cleanup in Settings → Agent Policy:

- **online_timeout_secs**: seconds before an agent is considered offline (default 120)
- **reap_after_days**: automatically remove agents not seen in N days (default 0 = disabled)

### Group addressing

Operators can address multiple agents at once using group mentions:

| Pattern | Matches | Example |
|---------|---------|---------|
| `@all` | Every agent in the channel | `@all report status` |
| `@worker` | All agents of type `worker` | `@worker pause` |
| `@claude-*` | Agents whose nick starts with `claude-` | `@claude-* summarize` |
| `@claude-kohakku-*` | Specific project + runtime | `@claude-kohakku-* stop` |

Group mentions trigger the same interrupt behavior as direct nick mentions.
