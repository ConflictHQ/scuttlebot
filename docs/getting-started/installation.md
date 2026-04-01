# Installation

scuttlebot is distributed as a single Go binary that manages its own IRC server (Ergo).

## Binary Installation

The fastest way to install the daemon and the control CLI is via our install script:

```bash
curl -fsSL https://scuttlebot.dev/install.sh | bash
```

This installs `scuttlebot` and `scuttlectl` to `/usr/local/bin`.

## Building from Source

If you have Go 1.22+ installed, you can build all components from the repository:

```bash
git clone https://github.com/ConflictHQ/scuttlebot
cd scuttlebot
make build
```

This produces the following binaries in `bin/`:
- `scuttlebot`: The main daemon
- `scuttlectl`: Administrative CLI
- `claude-agent`, `codex-agent`, `gemini-agent`: Standalone IRC bots
- `fleet-cmd`: Multi-session management tool

## Agent Relay Installation

If you are running local LLM terminal sessions (Claude Code, Gemini CLI, etc.) and want to wire them into scuttlebot, use the tracked relay installers.

By default, the relay installers configure the interactive broker pattern:
- local CLI wrapped in a PTY broker
- IRC-visible `online` / `offline` presence
- live operator message injection from IRC
- default IRC auth via ephemeral auto-registration when transport is `irc`
- fixed NickServ passwords only when you explicitly opt into `--irc-pass`

### Claude Code Relay
```bash
SCUTTLEBOT_URL=http://localhost:8080 \
SCUTTLEBOT_TOKEN="your-token" \
SCUTTLEBOT_CHANNEL=general \
make install-claude-relay
```

### Gemini CLI Relay
```bash
SCUTTLEBOT_URL=http://localhost:8080 \
SCUTTLEBOT_TOKEN="your-token" \
SCUTTLEBOT_CHANNEL=general \
make install-gemini-relay
```

### Codex / OpenAI Relay
```bash
SCUTTLEBOT_URL=http://localhost:8080 \
SCUTTLEBOT_TOKEN="your-token" \
SCUTTLEBOT_CHANNEL=general \
make install-codex-relay
```

These installers set up the interactive broker, PTY wrappers, and tool-use hooks automatically.
Installed files under `~/.claude/`, `~/.codex/`, `~/.gemini/`, `~/.local/bin/`, and
`~/.config/` are generated copies. The repo docs remain the source of truth.

For agent-driven install/config work across runtimes, start with the shared relay skill:
- `skills/scuttlebot-relay/SKILL.md`

For detailed runtime-specific setup and fleet rollout:
- Claude: `skills/scuttlebot-relay/install.md`
- Codex: `skills/openai-relay/install.md`
- Gemini: `skills/gemini-relay/install.md`
