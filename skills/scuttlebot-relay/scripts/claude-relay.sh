#!/usr/bin/env bash
# Launch Claude with a fleet-style session nick.
# Registers a claude-{project}-{session} nick, starts the IRC agent in the
# background under that nick (so hook activity and IRC responses share one
# identity), then runs the Claude CLI. Deregisters on exit.

set -u

SCUTTLEBOT_CONFIG_FILE="${SCUTTLEBOT_CONFIG_FILE:-$HOME/.config/scuttlebot-relay.env}"
if [ -f "$SCUTTLEBOT_CONFIG_FILE" ]; then
  set -a
  . "$SCUTTLEBOT_CONFIG_FILE"
  set +a
fi

SCUTTLEBOT_URL="${SCUTTLEBOT_URL:-http://localhost:8080}"
SCUTTLEBOT_TOKEN="${SCUTTLEBOT_TOKEN:-}"
SCUTTLEBOT_CHANNEL="${SCUTTLEBOT_CHANNEL:-general}"
SCUTTLEBOT_HOOKS_ENABLED="${SCUTTLEBOT_HOOKS_ENABLED:-1}"
SCUTTLEBOT_IRC="${SCUTTLEBOT_IRC:-127.0.0.1:6667}"
SCUTTLEBOT_BACKEND="${SCUTTLEBOT_BACKEND:-anthro}"
CLAUDE_AGENT_BIN="${CLAUDE_AGENT_BIN:-}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"

sanitize() {
  local input="$1"
  if [ -z "$input" ]; then
    input=$(cat)
  fi
  printf '%s' "$input" | tr -cs '[:alnum:]_-' '-'
}

target_cwd() {
  local cwd="$PWD"
  local prev=""
  local arg
  for arg in "$@"; do
    if [ "$prev" = "-C" ] || [ "$prev" = "--cd" ]; then
      cwd="$arg"
      prev=""
      continue
    fi
    case "$arg" in
      -C|--cd)
        prev="$arg"
        ;;
      -C=*|--cd=*)
        cwd="${arg#*=}"
        ;;
    esac
  done
  if [ -d "$cwd" ]; then
    (cd "$cwd" && pwd)
  else
    printf '%s\n' "$PWD"
  fi
}

hooks_enabled() {
  [ "$SCUTTLEBOT_HOOKS_ENABLED" != "0" ] &&
    [ "$SCUTTLEBOT_HOOKS_ENABLED" != "false" ] &&
    [ -n "$SCUTTLEBOT_TOKEN" ]
}

post_status() {
  local text="$1"
  hooks_enabled || return 0
  command -v curl >/dev/null 2>&1 || return 0
  command -v jq >/dev/null 2>&1 || return 0
  curl -sf -X POST "$SCUTTLEBOT_URL/v1/channels/$SCUTTLEBOT_CHANNEL/messages" \
    --connect-timeout 1 \
    --max-time 2 \
    -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"text\": $(printf '%s' "$text" | jq -Rs .), \"nick\": \"$SCUTTLEBOT_NICK\"}" \
    > /dev/null || true
}

if ! command -v "$CLAUDE_BIN" >/dev/null 2>&1; then
  printf 'claude-relay: %s not found in PATH\n' "$CLAUDE_BIN" >&2
  exit 127
fi

TARGET_CWD=$(target_cwd "$@")
BASE_NAME=$(sanitize "$(basename "$TARGET_CWD")")

if [ -z "${SCUTTLEBOT_SESSION_ID:-}" ]; then
  SCUTTLEBOT_SESSION_ID=$(
    printf '%s' "$TARGET_CWD|$$|$PPID|$(date +%s)" | cksum | awk '{print $1}' | cut -c 1-8
  )
fi
SCUTTLEBOT_SESSION_ID=$(sanitize "$SCUTTLEBOT_SESSION_ID")
if [ -z "${SCUTTLEBOT_NICK:-}" ]; then
  SCUTTLEBOT_NICK="claude-${BASE_NAME}-${SCUTTLEBOT_SESSION_ID}"
fi
SCUTTLEBOT_CHANNEL="${SCUTTLEBOT_CHANNEL#\#}"

export SCUTTLEBOT_CONFIG_FILE
export SCUTTLEBOT_URL
export SCUTTLEBOT_TOKEN
export SCUTTLEBOT_CHANNEL
export SCUTTLEBOT_HOOKS_ENABLED
export SCUTTLEBOT_SESSION_ID
export SCUTTLEBOT_NICK

printf 'claude-relay: nick %s\n' "$SCUTTLEBOT_NICK" >&2

# --- IRC agent: register nick and start in background ---
irc_agent_pid=""
irc_agent_nick=""

_start_irc_agent() {
  [ -n "$SCUTTLEBOT_TOKEN" ] || return 0

  # Find the claude-agent binary: next to this script, in PATH, or skip.
  local bin="$CLAUDE_AGENT_BIN"
  if [ -z "$bin" ]; then
    local script_dir; script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
    local repo_root; repo_root=$(CDPATH= cd -- "$script_dir/../../.." && pwd)
    if [ -x "$repo_root/bin/claude-agent" ]; then
      bin="$repo_root/bin/claude-agent"
    elif command -v claude-agent >/dev/null 2>&1; then
      bin="claude-agent"
    else
      printf 'claude-relay: claude-agent not found, IRC responses disabled\n' >&2
      return 0
    fi
  fi

  local resp; resp=$(curl -sf -X POST \
    --connect-timeout 2 --max-time 5 \
    -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"nick\":\"$SCUTTLEBOT_NICK\",\"type\":\"worker\",\"channels\":[\"#$SCUTTLEBOT_CHANNEL\"]}" \
    "$SCUTTLEBOT_URL/v1/agents/register" 2>/dev/null) || return 0

  local pass; pass=$(printf '%s' "$resp" | grep -o '"passphrase":"[^"]*"' | cut -d'"' -f4)
  [ -n "$pass" ] || return 0

  irc_agent_nick="$SCUTTLEBOT_NICK"
  "$bin" \
    --irc "$SCUTTLEBOT_IRC" \
    --nick "$irc_agent_nick" \
    --pass "$pass" \
    --channels "#$SCUTTLEBOT_CHANNEL" \
    --api-url "$SCUTTLEBOT_URL" \
    --token "$SCUTTLEBOT_TOKEN" \
    --backend "$SCUTTLEBOT_BACKEND" \
    2>/dev/null &
  irc_agent_pid=$!
  printf 'claude-relay: IRC agent started (pid %s)\n' "$irc_agent_pid" >&2
}

_stop_irc_agent() {
  if [ -n "$irc_agent_pid" ]; then
    kill "$irc_agent_pid" 2>/dev/null || true
    irc_agent_pid=""
  fi
  if [ -n "$irc_agent_nick" ] && [ -n "$SCUTTLEBOT_TOKEN" ]; then
    curl -sf -X DELETE \
      --connect-timeout 2 --max-time 5 \
      -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
      "$SCUTTLEBOT_URL/v1/agents/$irc_agent_nick" >/dev/null 2>&1 || true
    irc_agent_nick=""
  fi
}

_start_irc_agent

# --- Claude CLI ---
post_status "online in $(basename "$TARGET_CWD"); mention $SCUTTLEBOT_NICK to interrupt"

child_pid=""
_cleanup() {
  [ -n "$child_pid" ] && kill "$child_pid" 2>/dev/null || true
  _stop_irc_agent
  post_status "offline"
}

forward_signal() {
  local signal="$1"
  [ -n "$child_pid" ] && kill "-$signal" "$child_pid" 2>/dev/null || true
}

trap '_cleanup' EXIT
trap 'forward_signal TERM' TERM
trap 'forward_signal INT' INT
trap 'forward_signal HUP' HUP

"$CLAUDE_BIN" "$@" &
child_pid=$!
wait "$child_pid"
status=$?
child_pid=""

exit "$status"
