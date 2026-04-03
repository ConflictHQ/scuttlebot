#!/bin/bash
set -e

RELAY="${1:-claude-relay}"
shift 2>/dev/null || true

# Start watchdog if available.
WATCHDOG_PID=""
if command -v relay-watchdog &>/dev/null; then
    relay-watchdog &
    WATCHDOG_PID=$!
    trap "kill $WATCHDOG_PID 2>/dev/null" EXIT
fi

# Run the relay in the foreground.
exec "$RELAY" "$@"
