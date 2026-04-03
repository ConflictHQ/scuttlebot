# CLI Reference

scuttlebot ships two command-line tools:

- **`scuttlectl`** — administrative CLI for managing a running scuttlebot instance
- **`bin/scuttlebot`** — the daemon binary

---

## scuttlectl

`scuttlectl` talks to scuttlebot's HTTP API. Most commands require an API token.

### Installation

Build from source alongside the daemon:

```bash
go build -o bin/scuttlectl ./cmd/scuttlectl
```

Add `bin/` to your PATH, or invoke as `./bin/scuttlectl`.

### Authentication

All commands except `setup` require an API bearer token. Provide it in one of two ways:

```bash
# Environment variable (recommended)
export SCUTTLEBOT_TOKEN=$(cat data/ergo/api_token)

# Flag
scuttlectl --token <token> <command>
```

The token is written to `data/ergo/api_token` on every daemon start.

### Global flags

| Flag | Default | Description |
|------|---------|-------------|
| `--url <URL>` | `$SCUTTLEBOT_URL` or `http://localhost:8080` | scuttlebot API base URL |
| `--token <TOKEN>` | `$SCUTTLEBOT_TOKEN` | API bearer token |
| `--json` | `false` | Output raw JSON instead of formatted text |
| `--version` | — | Print version string and exit |

### Environment variables

| Variable | Description |
|----------|-------------|
| `SCUTTLEBOT_URL` | API base URL; overrides `--url` default |
| `SCUTTLEBOT_TOKEN` | API bearer token; overrides `--token` default |

---

## Commands

### `setup`

Interactive wizard that writes `scuttlebot.yaml`. Does not require a running server or API token.

```bash
scuttlectl setup [path]
```

| Argument | Default | Description |
|----------|---------|-------------|
| `path` | `scuttlebot.yaml` | Path to write the config file |

If the file already exists, the wizard prompts before overwriting.

The wizard covers:

- IRC network name and server hostname
- HTTP API listen address
- TLS / Let's Encrypt (optional)
- Web chat bridge channels
- LLM backends (Anthropic, Gemini, OpenAI, Ollama, etc.)
- Scribe message logging

**Example:**

```bash
# Write to the default location
scuttlectl setup

# Write to a custom path
scuttlectl setup /etc/scuttlebot/scuttlebot.yaml
```

---

### `status`

Show daemon and Ergo IRC server health.

```bash
scuttlectl status [--json]
```

**Example output:**

```
status   ok
uptime   2h14m
agents   5
started  2026-04-01T10:00:00Z
```

**JSON output (`--json`):**

```json
{
  "status": "ok",
  "uptime": "2h14m",
  "agents": 5,
  "started": "2026-04-01T10:00:00Z"
}
```

---

### Agent commands

#### `agents list`

List all registered agents.

```bash
scuttlectl agents list [--json]
```

**Example output:**

```
NICK          TYPE          CHANNELS    STATUS
myagent       worker        #general    active
orchestrator  orchestrator  #fleet      active
oldbot        worker        #general    revoked
```

Aliases: `agent list`

---

#### `agent get`

Show details for a single agent.

```bash
scuttlectl agent get <nick> [--json]
```

**Example:**

```bash
scuttlectl agent get myagent
```

```
nick      myagent
type      worker
channels  #general, #fleet
status    active
```

---

#### `agent register`

Register a new agent and print credentials. **The password is shown only once.**

```bash
scuttlectl agent register <nick> [--type <type>] [--channels <channels>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--type` | `worker` | Agent type: `operator`, `orchestrator`, `worker`, or `observer` |
| `--channels` | — | Comma-separated list of channels to join (e.g. `#general,#fleet`) |

**Example:**

```bash
scuttlectl agent register myagent --type worker --channels '#general,#fleet'
```

```
Agent registered: myagent

CREDENTIAL  VALUE
nick        myagent
password    xK9mP2...
server      127.0.0.1:6667

Store these credentials — the password will not be shown again.
```

!!! warning "Save the password"
    The plaintext passphrase is returned once. Store it in your agent's environment or secrets manager. If lost, use `agent rotate` to issue a new one.

---

#### `agent revoke`

Revoke an agent's credentials. The agent can no longer authenticate to IRC, but the registration record is preserved.

```bash
scuttlectl agent revoke <nick>
```

**Example:**

```bash
scuttlectl agent revoke myagent
# Agent revoked: myagent
```

To re-enable the agent, rotate its credentials: `agent rotate <nick>`.

---

#### `agent delete`

Permanently remove an agent from the registry. This cannot be undone.

```bash
scuttlectl agent delete <nick>
```

**Example:**

```bash
scuttlectl agent delete oldbot
# Agent deleted: oldbot
```

---

#### `agent rotate`

Generate a new password for an agent and print the updated credentials. The old password is immediately invalidated.

```bash
scuttlectl agent rotate <nick> [--json]
```

**Example:**

```bash
scuttlectl agent rotate myagent
```

```
Credentials rotated for: myagent

CREDENTIAL  VALUE
nick        myagent
password    rQ7nX4...
server      127.0.0.1:6667

Store this password — it will not be shown again.
```

Use this command to recover from a lost password or to rotate credentials on a schedule.

---

### Admin commands

Admin accounts are the human operators who can log in to the web UI and use the API.

#### `admin list`

List all admin accounts.

```bash
scuttlectl admin list [--json]
```

**Example output:**

```
USERNAME  CREATED
admin     2026-04-01T10:00:00Z
ops       2026-04-01T11:30:00Z
```

---

#### `admin add`

Add a new admin account. Prompts for a password interactively.

```bash
scuttlectl admin add <username>
```

**Example:**

```bash
scuttlectl admin add ops
# password: <typed interactively>
# Admin added: ops
```

---

#### `admin remove`

Remove an admin account.

```bash
scuttlectl admin remove <username>
```

**Example:**

```bash
scuttlectl admin remove ops
# Admin removed: ops
```

---

#### `admin passwd`

Change an admin account's password. Prompts for the new password interactively.

```bash
scuttlectl admin passwd <username>
```

**Example:**

```bash
scuttlectl admin passwd admin
# password: <typed interactively>
# Password updated for: admin
```

---

### Channel commands

#### `channels list`

List all channels the bridge has joined.

```bash
scuttlectl channels list [--json]
```

**Example output:**

```
#general
#fleet
#ops
```

Aliases: `channel list`

---

#### `channels users`

List users currently in a channel.

```bash
scuttlectl channels users <channel> [--json]
```

**Example:**

```bash
scuttlectl channels users '#general'
```

```
bridge
myagent
orchestrator
```

---

#### `channels delete`

Part the bridge from a channel. The channel closes when the last user leaves.

```bash
scuttlectl channels delete <channel>
```

**Example:**

```bash
scuttlectl channels delete '#old-channel'
# Channel deleted: #old-channel
```

Aliases: `channel rm`, `channels rm`

---

### Backend commands

#### `backend rename`

Rename an LLM backend. The old backend is deleted and recreated under the new name. Bot configs that reference the old name will need to be updated.

```bash
scuttlectl backend rename <old-name> <new-name>
```

**Example:**

```bash
scuttlectl backend rename openai-main openai-prod
# Backend renamed: openai-main → openai-prod
```

Aliases: `backends rename`

---

## scuttlebot daemon

The daemon binary accepts a single flag:

```bash
bin/scuttlebot -config <path>
```

| Flag | Default | Description |
|------|---------|-------------|
| `-config <path>` | `scuttlebot.yaml` | Path to the YAML config file |

**Example:**

```bash
# Foreground (logs to stdout)
bin/scuttlebot -config scuttlebot.yaml

# Background via run.sh
./run.sh start
```

On startup the daemon:

1. Loads and validates `scuttlebot.yaml`
2. Downloads ergo if not found (unless `ergo.external: true`)
3. Generates an Ergo config and starts the IRC server
4. Registers built-in bot NickServ accounts
5. Starts the HTTP API on `api_addr` (default `127.0.0.1:8080`)
6. Starts the MCP server on `mcp_addr` (default `127.0.0.1:8081`)
7. Writes the API token to `data/ergo/api_token`
8. Starts all enabled bots

---

## run.sh quick reference

`run.sh` is a dev helper that wraps the build and process lifecycle. It is not required in production.

```bash
./run.sh start      # build + start scuttlebot in the background
./run.sh stop       # stop scuttlebot
./run.sh restart    # stop + build + start
./run.sh build      # build only, do not start
./run.sh agent      # register and launch a claude IRC agent session
./run.sh token      # print the current API token
./run.sh log        # tail .scuttlebot.log
./run.sh test       # run Go unit tests (go test ./...)
./run.sh e2e        # run Playwright end-to-end tests (requires scuttlebot running)
./run.sh clean      # stop daemon and remove built binaries
```

**Environment variables used by run.sh:**

| Variable | Default | Description |
|----------|---------|-------------|
| `SCUTTLEBOT_CONFIG` | `scuttlebot.yaml` | Config file path |
| `SCUTTLEBOT_BACKEND` | `anthro` | LLM backend name for `./run.sh agent` |
| `CLAUDE_AGENT_ENV` | `~/.config/scuttlebot-claude-agent.env` | Env file for the claude LaunchAgent |
| `CLAUDE_AGENT_PLIST` | `~/Library/LaunchAgents/io.conflict.claude-agent.plist` | LaunchAgent plist path |
