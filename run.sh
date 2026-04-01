#!/usr/bin/env bash
# run.sh — scuttlebot dev helper
# Usage: ./run.sh [command]
#   (no args)   build and start scuttlebot
#   stop        kill running scuttlebot
#   restart     stop + build + start
#   agent       build and run a claude IRC agent with a fleet-style nick
#   token       print the current API token
#   log         tail the log (if logging to file is configured)
#   test        run Go unit tests
#   e2e         run Playwright e2e tests (requires scuttlebot running)
#   clean       remove built binaries
#
# After start/restart, if ~/Library/LaunchAgents/io.conflict.claude-agent.plist
# exists, the claude IRC agent credentials are rotated and the LaunchAgent reloaded.

set -euo pipefail

BINARY=bin/scuttlebot
CONFIG=${SCUTTLEBOT_CONFIG:-scuttlebot.yaml}
TOKEN_FILE=data/ergo/api_token
PID_FILE=.scuttlebot.pid
LOG_FILE=.scuttlebot.log
CLAUDE_AGENT_ENV="${CLAUDE_AGENT_ENV:-$HOME/.config/scuttlebot-claude-agent.env}"
CLAUDE_AGENT_PLIST="${CLAUDE_AGENT_PLIST:-$HOME/Library/LaunchAgents/io.conflict.claude-agent.plist}"

cmd=${1:-start}

_pid() { cat "$PID_FILE" 2>/dev/null || echo ""; }

_running() {
  local pid; pid=$(_pid)
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

_stop() {
  if _running; then
    local pid; pid=$(_pid)
    kill "$pid" && rm -f "$PID_FILE"
    echo "stopped (pid $pid)"
  else
    echo "not running"
  fi
  # Kill any stale ergo processes holding IRC/API ports.
  pkill -f "data/ergo/ergo" 2>/dev/null && sleep 1 || true
}

_build() {
  echo "building..."
  go build -o "$BINARY" ./cmd/scuttlebot
  echo "ok → $BINARY"
}

_start() {
  if _running; then
    echo "already running (pid $(_pid)) — use ./run.sh restart"
    exit 0
  fi

  if [[ ! -f "$CONFIG" ]]; then
    echo "no $CONFIG found — copying from example"
    cp deploy/standalone/scuttlebot.yaml.example "$CONFIG"
    echo "edit $CONFIG if needed, then re-run"
  fi

  mkdir -p bin data/ergo

  "$BINARY" -config "$CONFIG" >"$LOG_FILE" 2>&1 &
  echo $! >"$PID_FILE"
  local pid; pid=$(_pid)
  echo "started (pid $pid) — logs: $LOG_FILE"

  # wait briefly and print token so it's handy
  sleep 1
  if [[ -f "$TOKEN_FILE" ]]; then
    echo "token: $(cat "$TOKEN_FILE")"
  fi

  echo "ui:   http://localhost:8080/ui/"
}

_token() {
  if [[ -f "$TOKEN_FILE" ]]; then
    cat "$TOKEN_FILE"
  else
    echo "no token file found (is scuttlebot running?)" >&2
    exit 1
  fi
}

# _sync_claude_agent rotates the claude IRC agent's credentials, updates the
# env file, and reloads the LaunchAgent. No-ops silently if the plist or env
# file don't exist (agent not installed on this machine).
_sync_claude_agent() {
  [[ -f "$CLAUDE_AGENT_PLIST" ]] || return 0
  [[ -f "$CLAUDE_AGENT_ENV" ]] || return 0

  local token; token=$(_token 2>/dev/null) || return 0

  # Wait up to 15s for the HTTP API, then give ergo another 5s to finish
  # starting NickServ before we attempt a password rotation.
  local ready=0
  for i in $(seq 1 15); do
    curl -sf -H "Authorization: Bearer $token" "http://localhost:8080/v1/status" >/dev/null 2>&1 && ready=1 && break
    sleep 1
  done
  [[ $ready -eq 1 ]] || { echo "warning: scuttlebot API not ready, skipping claude-agent sync" >&2; return 0; }
  sleep 5

  echo "syncing claude-agent credentials..."
  local resp; resp=$(curl -sf -X POST \
    -H "Authorization: Bearer $token" \
    "http://localhost:8080/v1/agents/claude/rotate" 2>/dev/null) || {
    echo "warning: could not rotate claude-agent credentials (API not ready?)" >&2
    return 0
  }

  local pass; pass=$(echo "$resp" | grep -o '"passphrase":"[^"]*"' | cut -d'"' -f4)
  [[ -n "$pass" ]] || { echo "warning: empty passphrase in rotate response" >&2; return 0; }

  # Rewrite only the CLAUDE_AGENT_PASS line; preserve everything else.
  sed -i '' "s|^CLAUDE_AGENT_PASS=.*|CLAUDE_AGENT_PASS=$pass|" "$CLAUDE_AGENT_ENV"

  launchctl unload "$CLAUDE_AGENT_PLIST" 2>/dev/null || true
  launchctl load  "$CLAUDE_AGENT_PLIST"
  echo "claude-agent reloaded"
}

_run_agent() {
  local token; token=$(_token)
  local base; base=$(basename "$(pwd)" | tr -cs '[:alnum:]_-' '-' | tr '[:upper:]' '[:lower:]')
  local session; session=$(printf '%s' "$$|$PPID|$(date +%s)" | cksum | awk '{printf "%08x\n", $1}')
  local nick="claude-${base}-${session}"

  echo "registering agent nick: $nick"
  local resp; resp=$(curl -sf -X POST \
    -H "Authorization: Bearer $token" \
    -H "Content-Type: application/json" \
    -d "{\"nick\":\"$nick\",\"type\":\"worker\",\"channels\":[\"#general\"]}" \
    "http://localhost:8080/v1/agents/register")
  local pass; pass=$(echo "$resp" | grep -o '"passphrase":"[^"]*"' | cut -d'"' -f4)
  [[ -n "$pass" ]] || { echo "error: failed to register agent" >&2; exit 1; }

  # Clean up registration on exit.
  trap 'echo "removing agent $nick..."; curl -sf -X DELETE \
    -H "Authorization: Bearer '"$token"'" \
    "http://localhost:8080/v1/agents/$nick" >/dev/null || true' EXIT INT TERM

  echo "connecting as $nick..."
  local backend="${SCUTTLEBOT_BACKEND:-anthro}"
  bin/claude-agent \
    --irc 127.0.0.1:6667 \
    --nick "$nick" \
    --pass "$pass" \
    --channels "#general" \
    --api-url "http://localhost:8080" \
    --token "$token" \
    --backend "$backend"
}

case "$cmd" in
  start)
    _build
    _start
    _sync_claude_agent
    ;;
  stop)
    _stop
    ;;
  restart)
    _stop || true
    _build
    _start
    _sync_claude_agent
    ;;
  build)
    _build
    ;;
  agent)
    go build -o bin/claude-agent ./cmd/claude-agent
    _run_agent "${@:2}"
    ;;
  token)
    _token
    ;;
  log|logs)
    tail -f "$LOG_FILE"
    ;;
  test)
    go test ./...
    ;;
  e2e)
    SB_TOKEN=$(cat "$TOKEN_FILE" 2>/dev/null) \
    SB_USERNAME=${SB_USERNAME:-admin} \
    SB_PASSWORD=${SB_PASSWORD:-} \
      npx --prefix tests/e2e playwright test "${@:2}"
    ;;
  clean)
    _stop || true
    rm -f "$BINARY" bin/scuttlectl "$LOG_FILE" "$PID_FILE"
    echo "clean"
    ;;
  *)
    echo "usage: $0 {start|stop|restart|agent|build|token|log|test|e2e|clean}"
    exit 1
    ;;
esac
