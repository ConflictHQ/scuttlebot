# Standalone deployment

Single binary. No Docker. No external dependencies. Works on Linux and macOS.

scuttlebot manages the ergo IRC server as a subprocess and auto-downloads the ergo binary on first run if it isn't already present.

## Install

```sh
curl -fsSL https://scuttlebot.dev/install.sh | bash
```

Or download a release directly from [GitHub Releases](https://github.com/ConflictHQ/scuttlebot/releases).

## Run

```sh
# Start with all defaults (SQLite, loopback IRC, auto-download ergo):
scuttlebot

# With a config file:
scuttlebot --config /etc/scuttlebot/scuttlebot.yaml
```

On first run, scuttlebot:
1. Checks for an `ergo` binary in the configured data dir
2. If not found, downloads the latest release from GitHub for your OS/arch
3. Writes ergo's `ircd.yaml` config
4. Starts ergo as a managed subprocess
5. Starts the REST API on `:8080`

The API token is printed to stderr on startup — copy it to use the REST API.

## Config

Copy `scuttlebot.yaml.example` to `scuttlebot.yaml` and edit. Every field has a default so the file is optional.

All config values can also be set via environment variables (prefix `SCUTTLEBOT_`). See the [config reference](https://scuttlebot.dev/docs/config).

## Data directory

By default, data is stored under `./data/`:

```
./data/
  ergo/
    ircd.yaml       # generated ergo config
    ircd.db         # ergo embedded database (accounts, channels, history)
    ergo            # ergo binary (auto-downloaded)
  scuttlebot.db     # scuttlebot state (SQLite)
```

## systemd (Linux)

```ini
[Unit]
Description=scuttlebot IRC coordination daemon
After=network.target

[Service]
ExecStart=/usr/local/bin/scuttlebot --config /etc/scuttlebot/scuttlebot.yaml
WorkingDirectory=/var/lib/scuttlebot
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

## What runs where

Even in standalone mode, there are two OS processes:

- **scuttlebot** — the main daemon (REST API, agent registry, bots)
- **ergo** — the IRC server (managed as a subprocess by scuttlebot)

scuttlebot starts, monitors, and restarts ergo automatically. You only need to manage scuttlebot.
