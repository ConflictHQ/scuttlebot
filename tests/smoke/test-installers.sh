#!/usr/bin/env bash
# Smoke test for scuttlebot relay installers.

set -euo pipefail

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TEMP_HOME=$(mktemp -d)
export HOME="$TEMP_HOME"
export SCUTTLEBOT_CONFIG_FILE="$HOME/.config/scuttlebot-relay.env"
export CODEX_HOOKS_DIR="$HOME/.codex/hooks"
export CODEX_HOOKS_JSON="$HOME/.codex/hooks.json"
export CODEX_CONFIG_TOML="$HOME/.codex/config.toml"
export CODEX_BIN_DIR="$HOME/.local/bin"
export GEMINI_HOOKS_DIR="$HOME/.gemini/hooks"
export GEMINI_SETTINGS_JSON="$HOME/.gemini/settings.json"
export GEMINI_BIN_DIR="$HOME/.local/bin"
export CLAUDE_HOOKS_DIR="$HOME/.claude/hooks"
export CLAUDE_SETTINGS_JSON="$HOME/.claude/settings.json"
export CLAUDE_BIN_DIR="$HOME/.local/bin"

printf 'Smoke testing installers in %s...\n' "$TEMP_HOME"

mkdir -p "$HOME/.config"
cat > "$SCUTTLEBOT_CONFIG_FILE" <<'EOF'
SCUTTLEBOT_IRC_PASS=stale-pass
EOF

# Mock binaries
mkdir -p "$HOME/.local/bin"
touch "$HOME/.local/bin/codex" "$HOME/.local/bin/gemini" "$HOME/.local/bin/claude"
chmod +x "$HOME/.local/bin/codex" "$HOME/.local/bin/gemini" "$HOME/.local/bin/claude"
export PATH="$HOME/.local/bin:$PATH"

# 1. Codex
printf 'Testing Codex installer...\n'
bash "$REPO_ROOT/skills/openai-relay/scripts/install-codex-relay.sh" \
  --url http://localhost:8080 \
  --token "test-token" \
  --channel general

# Verify files
[ -f "$HOME/.codex/hooks/scuttlebot-post.sh" ]
[ -f "$HOME/.codex/hooks/scuttlebot-check.sh" ]
[ -f "$HOME/.codex/hooks.json" ]
[ -f "$HOME/.codex/config.toml" ]
[ -f "$HOME/.local/bin/codex-relay" ]
[ -f "$HOME/.config/scuttlebot-relay.env" ]
! grep -q '^SCUTTLEBOT_IRC_PASS=' "$SCUTTLEBOT_CONFIG_FILE"
grep -q '^SCUTTLEBOT_IRC_DELETE_ON_CLOSE=1$' "$SCUTTLEBOT_CONFIG_FILE"

# 2. Gemini
printf 'Testing Gemini installer...\n'
bash "$REPO_ROOT/skills/gemini-relay/scripts/install-gemini-relay.sh" \
  --url http://localhost:8080 \
  --token "test-token" \
  --channel general \
  --irc-pass "gemini-fixed"

# Verify files
[ -f "$HOME/.gemini/hooks/scuttlebot-post.sh" ]
[ -f "$HOME/.gemini/hooks/scuttlebot-check.sh" ]
[ -f "$HOME/.gemini/settings.json" ]
[ -f "$HOME/.local/bin/gemini-relay" ]
grep -q '^SCUTTLEBOT_IRC_PASS=gemini-fixed$' "$SCUTTLEBOT_CONFIG_FILE"

printf 'Testing Gemini auto-register scrub...\n'
bash "$REPO_ROOT/skills/gemini-relay/scripts/install-gemini-relay.sh" \
  --channel general \
  --auto-register
! grep -q '^SCUTTLEBOT_IRC_PASS=' "$SCUTTLEBOT_CONFIG_FILE"

# 3. Claude
printf 'Testing Claude installer...\n'
bash "$REPO_ROOT/skills/scuttlebot-relay/scripts/install-claude-relay.sh" \
  --url http://localhost:8080 \
  --token "test-token" \
  --channel general \
  --transport irc \
  --irc-addr 127.0.0.1:6667 \
  --irc-pass "claude-fixed"

# Verify files
[ -f "$HOME/.claude/hooks/scuttlebot-post.sh" ]
[ -f "$HOME/.claude/hooks/scuttlebot-check.sh" ]
[ -f "$HOME/.claude/settings.json" ]
[ -f "$HOME/.local/bin/claude-relay" ]
grep -q '^SCUTTLEBOT_IRC_PASS=claude-fixed$' "$SCUTTLEBOT_CONFIG_FILE"
grep -q '^SCUTTLEBOT_TRANSPORT=irc$' "$SCUTTLEBOT_CONFIG_FILE"

printf 'Testing Claude auto-register scrub...\n'
bash "$REPO_ROOT/skills/scuttlebot-relay/scripts/install-claude-relay.sh" \
  --channel general \
  --auto-register
! grep -q '^SCUTTLEBOT_IRC_PASS=' "$SCUTTLEBOT_CONFIG_FILE"

printf 'ALL INSTALLERS PASSED SMOKE TEST\n'
chmod -R +w "$TEMP_HOME"
rm -rf "$TEMP_HOME"
