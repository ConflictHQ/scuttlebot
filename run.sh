#!/usr/bin/env bash
# run.sh — scuttlebot dev helper
# Usage: ./run.sh [command]
#   (no args)   build and start scuttlebot
#   stop        kill running scuttlebot
#   restart     stop + build + start
#   token       print the current API token
#   log         tail the log (if logging to file is configured)
#   test        run Go unit tests
#   e2e         run Playwright e2e tests (requires scuttlebot running)
#   clean       remove built binaries

set -euo pipefail

BINARY=bin/scuttlebot
CONFIG=${SCUTTLEBOT_CONFIG:-scuttlebot.yaml}
TOKEN_FILE=data/ergo/api_token
PID_FILE=.scuttlebot.pid
LOG_FILE=.scuttlebot.log
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


case "$cmd" in
  start)
    _build
    _start
    ;;
  stop)
    _stop
    ;;
  restart)
    _stop || true
    _build
    _start
    ;;
  build)
    _build
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
    echo "usage: $0 {start|stop|restart|build|token|log|test|e2e|clean}"
    exit 1
    ;;
esac
