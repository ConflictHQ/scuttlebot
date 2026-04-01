#!/bin/bash
# PostToolUse hook for OpenAI agents (Codex-style). Posts activity to scuttlebot IRC.

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
SCUTTLEBOT_ACTIVITY_VIA_BROKER="${SCUTTLEBOT_ACTIVITY_VIA_BROKER:-0}"

input=$(cat)

tool=$(echo "$input" | jq -r '.tool_name // empty')
cwd=$(echo "$input" | jq -r '.cwd // empty')

sanitize() {
  printf '%s' "$1" | tr -cs '[:alnum:]_-' '-'
}

if [ -z "$cwd" ]; then
  cwd=$(pwd)
fi
base_name=$(sanitize "$(basename "$cwd")")
session_suffix="${SCUTTLEBOT_SESSION_ID:-${CODEX_SESSION_ID:-$PPID}}"
session_suffix=$(sanitize "$session_suffix")
default_nick="codex-${base_name}-${session_suffix}"
SCUTTLEBOT_NICK="${SCUTTLEBOT_NICK:-$default_nick}"

[ "$SCUTTLEBOT_HOOKS_ENABLED" = "0" ] && exit 0
[ "$SCUTTLEBOT_HOOKS_ENABLED" = "false" ] && exit 0
[ "$SCUTTLEBOT_ACTIVITY_VIA_BROKER" = "1" ] && exit 0
[ "$SCUTTLEBOT_ACTIVITY_VIA_BROKER" = "true" ] && exit 0
[ -z "$SCUTTLEBOT_TOKEN" ] && exit 0

case "$tool" in
  Bash)
    cmd=$(echo "$input" | jq -r '.tool_input.command // empty' | head -c 120)
    msg="› $cmd"
    ;;
  Read)
    file=$(echo "$input" | jq -r '.tool_input.file_path // empty' | sed "s|$cwd/||")
    msg="read $file"
    ;;
  Edit)
    file=$(echo "$input" | jq -r '.tool_input.file_path // empty' | sed "s|$cwd/||")
    msg="edit $file"
    ;;
  Write)
    file=$(echo "$input" | jq -r '.tool_input.file_path // empty' | sed "s|$cwd/||")
    msg="write $file"
    ;;
  Glob)
    pattern=$(echo "$input" | jq -r '.tool_input.pattern // empty')
    msg="glob $pattern"
    ;;
  Grep)
    pattern=$(echo "$input" | jq -r '.tool_input.pattern // empty')
    msg="grep \"$pattern\""
    ;;
  Agent)
    desc=$(echo "$input" | jq -r '.tool_input.description // empty' | head -c 80)
    msg="spawn agent: $desc"
    ;;
  *)
    msg="$tool"
    ;;
esac

[ -z "$msg" ] && exit 0

curl -sf -X POST "$SCUTTLEBOT_URL/v1/channels/$SCUTTLEBOT_CHANNEL/messages" \
  --connect-timeout 1 \
  --max-time 2 \
  -H "Authorization: Bearer $SCUTTLEBOT_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"text\": $(echo "$msg" | jq -Rs .), \"nick\": \"$SCUTTLEBOT_NICK\"}" \
  > /dev/null

exit 0
