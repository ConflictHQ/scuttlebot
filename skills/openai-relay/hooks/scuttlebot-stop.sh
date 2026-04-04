#!/bin/bash
# Stop hook for Codex agents. Posts final assistant reply to scuttlebot IRC.

SCUTTLEBOT_CONFIG_FILE="${SCUTTLEBOT_CONFIG_FILE:-$HOME/.config/scuttlebot-relay.env}"
if [ -f "$SCUTTLEBOT_CONFIG_FILE" ]; then
  set -a
  . "$SCUTTLEBOT_CONFIG_FILE"
  set +a
fi
if [ -n "${SCUTTLEBOT_CHANNEL_STATE_FILE:-}" ] && [ -f "$SCUTTLEBOT_CHANNEL_STATE_FILE" ]; then
  set -a
  . "$SCUTTLEBOT_CHANNEL_STATE_FILE"
  set +a
fi

SCUTTLEBOT_URL="${SCUTTLEBOT_URL:-http://localhost:8080}"
SCUTTLEBOT_TOKEN="${SCUTTLEBOT_TOKEN}"
SCUTTLEBOT_CHANNEL="${SCUTTLEBOT_CHANNEL:-general}"
SCUTTLEBOT_HOOKS_ENABLED="${SCUTTLEBOT_HOOKS_ENABLED:-1}"

normalize_channel() {
  local channel="$1"
  channel="${channel//[$' \t\r\n']/}"
  channel="${channel#\#}"
  printf '%s' "$channel"
}

relay_channels() {
  local raw="${SCUTTLEBOT_CHANNELS:-$SCUTTLEBOT_CHANNEL}"
  local IFS=','
  local item channel seen=""
  read -r -a items <<< "$raw"
  for item in "${items[@]}"; do
    channel=$(normalize_channel "$item")
    [ -n "$channel" ] || continue
    case ",$seen," in
      *,"$channel",*) ;;
      *)
        seen="${seen:+$seen,}$channel"
        printf '%s\n' "$channel"
        ;;
    esac
  done
}

sanitize() {
  printf '%s' "$1" | tr -cs '[:alnum:]_-' '-'
}

input=$(cat)

cwd=$(echo "$input" | jq -r '.cwd // empty')
[ -z "$cwd" ] && cwd=$(pwd)
base_name=$(sanitize "$(basename "$cwd")")
session_suffix=$(echo "$input" | jq -r '.session_id // empty' | head -c 8)
[ -z "$session_suffix" ] && session_suffix=$PPID
default_nick="codex-${base_name}-$(sanitize "$session_suffix")"
SCUTTLEBOT_NICK="${SCUTTLEBOT_NICK:-$default_nick}"

[ "$SCUTTLEBOT_HOOKS_ENABLED" = "0" ] && exit 0
[ "$SCUTTLEBOT_HOOKS_ENABLED" = "false" ] && exit 0
[ -z "$SCUTTLEBOT_TOKEN" ] && exit 0

response=$(echo "$input" | jq -r '.last_assistant_message // empty')
[ -z "$response" ] && exit 0

# Truncate long responses.
response=$(printf '%s' "$response" | head -c 360)

payload="{\"text\": $(printf '%s' "$response" | jq -Rs .), \"nick\": \"$SCUTTLEBOT_NICK\"}"
for channel in $(relay_channels); do
  curl -sf -X POST "$SCUTTLEBOT_URL/v1/channels/$channel/messages" \
    --connect-timeout 1 \
    --max-time 2 \
    -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$payload" \
    > /dev/null || true
done

exit 0
