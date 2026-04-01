#!/bin/bash
# PreToolUse hook — checks IRC for human instructions before each tool call.
# Only messages that explicitly mention this session nick are surfaced back into
# the agent loop as a blocking instruction.

SCUTTLEBOT_CONFIG_FILE="${SCUTTLEBOT_CONFIG_FILE:-$HOME/.config/scuttlebot-relay.env}"
if [ -f "$SCUTTLEBOT_CONFIG_FILE" ]; then
  set -a
  . "$SCUTTLEBOT_CONFIG_FILE"
  set +a
fi

SCUTTLEBOT_URL="${SCUTTLEBOT_URL:-http://localhost:8080}"
SCUTTLEBOT_TOKEN="${SCUTTLEBOT_TOKEN}"
SCUTTLEBOT_CHANNEL="${SCUTTLEBOT_CHANNEL:-general}"
SCUTTLEBOT_HOOKS_ENABLED="${SCUTTLEBOT_HOOKS_ENABLED:-1}"

input=$(cat)
session_id=$(echo "$input" | jq -r '.session_id // empty' 2>/dev/null | head -c 8)

sanitize() {
  printf '%s' "$1" | tr -cs '[:alnum:]_-' '-'
}

cwd=$(echo "$input" | jq -r '.cwd // empty' 2>/dev/null)
if [ -z "$cwd" ]; then cwd=$(pwd); fi
base_name=$(sanitize "$(basename "$cwd")")
session_suffix="${session_id:-$PPID}"
default_nick="claude-${base_name}-${session_suffix}"
SCUTTLEBOT_NICK="${SCUTTLEBOT_NICK:-$default_nick}"

[ "$SCUTTLEBOT_HOOKS_ENABLED" = "0" ] && exit 0
[ "$SCUTTLEBOT_HOOKS_ENABLED" = "false" ] && exit 0
[ -z "$SCUTTLEBOT_TOKEN" ] && exit 0

state_key=$(printf '%s' "$SCUTTLEBOT_CHANNEL|$SCUTTLEBOT_NICK|$(pwd)" | cksum | awk '{print $1}')
LAST_CHECK_FILE="/tmp/.scuttlebot-last-check-$state_key"

contains_mention() {
  local text="$1"
  [[ "$text" =~ (^|[^[:alnum:]_./\\-])$SCUTTLEBOT_NICK($|[^[:alnum:]_./\\-]) ]]
}

last_check=0
if [ -f "$LAST_CHECK_FILE" ]; then
  last_check=$(cat "$LAST_CHECK_FILE")
fi
now=$(date +%s)
echo "$now" > "$LAST_CHECK_FILE"

messages=$(curl -sf \
  --connect-timeout 1 \
  --max-time 2 \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  "$SCUTTLEBOT_URL/v1/channels/$SCUTTLEBOT_CHANNEL/messages" 2>/dev/null)

[ -z "$messages" ] && exit 0

BOTS='["bridge","oracle","sentinel","steward","scribe","warden","snitch","herald","scroll","systembot","auditbot","claude"]'

instruction=$(
  echo "$messages" | jq -r --argjson bots "$BOTS" --arg self "$SCUTTLEBOT_NICK" '
    .messages[]
    | select(.nick as $n |
        ($bots | index($n) | not) and
        ($n | startswith("claude-") | not) and
        ($n | startswith("codex-") | not) and
        ($n | startswith("gemini-") | not) and
        $n != $self
      )
    | "\(.at)\t\(.nick)\t\(.text)"
  ' 2>/dev/null | while IFS=$'\t' read -r at nick text; do
    ts_clean=$(echo "$at" | sed 's/\.[0-9]*//' | sed 's/\([+-][0-9][0-9]\):\([0-9][0-9]\)$/\1\2/')
    ts=$(date -j -f "%Y-%m-%dT%H:%M:%S%z" "$ts_clean" "+%s" 2>/dev/null || \
         date -d "$ts_clean" "+%s" 2>/dev/null)
    [ -n "$ts" ] || continue
    [ "$ts" -gt "$last_check" ] || continue
    contains_mention "$text" || continue
    echo "$nick: $text"
  done | tail -1
)

[ -z "$instruction" ] && exit 0

echo "{\"decision\": \"block\", \"reason\": \"[IRC instruction from operator] $instruction\"}"
exit 0
