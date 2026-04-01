#!/bin/bash
# BeforeTool hook for Gemini. Checks scuttlebot for operator instructions before
# each tool call and returns a blocking decision when the session nick is
# explicitly mentioned.

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

sanitize() {
  local input="$1"
  if [ -z "$input" ]; then
    input=$(cat)
  fi
  printf '%s' "$input" | tr -cs '[:alnum:]_-' '-'
}

base_name=$(basename "$(pwd)")
base_name=$(sanitize "$base_name")
session_raw="${SCUTTLEBOT_SESSION_ID:-${GEMINI_SESSION_ID:-$PPID}}"
if [ -z "$session_raw" ] || [ "$session_raw" = "0" ]; then
  session_raw=$(date +%s)
fi
session_suffix=$(printf '%s' "$session_raw" | sanitize | cut -c 1-8)
default_nick="gemini-${base_name}-${session_suffix}"
SCUTTLEBOT_NICK="${SCUTTLEBOT_NICK:-$default_nick}"

[ "$SCUTTLEBOT_HOOKS_ENABLED" = "0" ] && { echo '{}'; exit 0; }
[ "$SCUTTLEBOT_HOOKS_ENABLED" = "false" ] && { echo '{}'; exit 0; }
[ -z "$SCUTTLEBOT_TOKEN" ] && { echo '{}'; exit 0; }

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

[ -z "$messages" ] && { echo '{}'; exit 0; }

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

[ -z "$instruction" ] && { echo '{}'; exit 0; }

echo "{\"decision\": \"block\", \"reason\": \"[IRC instruction from operator] $instruction\"}"
exit 0
