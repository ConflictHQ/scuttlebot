#!/bin/bash
# AfterAgent hook for Gemini agents. Posts final assistant replies to scuttlebot IRC.

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
SCUTTLEBOT_AFTER_AGENT_MAX_POSTS="${SCUTTLEBOT_AFTER_AGENT_MAX_POSTS:-6}"
SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH="${SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH:-360}"

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
  local input="$1"
  if [ -z "$input" ]; then
    input=$(cat)
  fi
  printf '%s' "$input" | tr -cs '[:alnum:]_-' '-'
}

post_line() {
  local text="$1"
  local payload
  [ -z "$text" ] && return 0
  payload="{\"text\": $(printf '%s' "$text" | jq -Rs .), \"nick\": \"$SCUTTLEBOT_NICK\"}"
  for channel in $(relay_channels); do
    curl -sf -X POST "$SCUTTLEBOT_URL/v1/channels/$channel/messages" \
      --connect-timeout 1 \
      --max-time 2 \
      -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
      -H "Content-Type: application/json" \
      -d "$payload" \
      > /dev/null || true
  done
}

normalize_response() {
  printf '%s' "$1" \
    | tr '\r\n\t' '   ' \
    | tr -s '[:space:]' ' ' \
    | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

input=$(cat)

cwd=$(echo "$input" | jq -r '.cwd // empty')
if [ -z "$cwd" ]; then
  cwd=$(pwd)
fi
base_name=$(sanitize "$(basename "$cwd")")
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

response=$(echo "$input" | jq -r '.prompt_response // empty')
[ -z "$response" ] && { echo '{}'; exit 0; }

response=$(normalize_response "$response")
[ -z "$response" ] && { echo '{}'; exit 0; }

posted=0
truncated=0
while IFS= read -r chunk || [ -n "$chunk" ]; do
  chunk=$(printf '%s' "$chunk" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
  [ -z "$chunk" ] && continue
  if [ "$posted" -ge "$SCUTTLEBOT_AFTER_AGENT_MAX_POSTS" ]; then
    truncated=1
    break
  fi
  post_line "$chunk"
  posted=$((posted + 1))
done < <(printf '%s\n' "$response" | fold -s -w "$SCUTTLEBOT_AFTER_AGENT_CHUNK_WIDTH")

if [ "$truncated" -eq 1 ]; then
  post_line "[reply truncated]"
fi

echo '{}'
exit 0
