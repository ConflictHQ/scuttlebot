#!/bin/bash
# AfterTool hook for Gemini agents. Posts activity to scuttlebot IRC.

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

post_message() {
  local text="$1"
  local payload
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

sanitize() {
  local input="$1"
  if [ -z "$input" ]; then
    input=$(cat)
  fi
  printf '%s' "$input" | tr -cs '[:alnum:]_-' '-'
}

input=$(cat)

tool=$(echo "$input" | jq -r '.tool_name // empty')
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

case "$tool" in
  run_shell_command|Bash)
    cmd=$(echo "$input" | jq -r '.tool_input.command // empty' | head -c 120)
    msg="› $cmd"
    ;;
  read_file|Read)
    file=$(echo "$input" | jq -r '.tool_input.file_path // empty' | sed "s|$cwd/||")
    msg="read $file"
    ;;
  edit|Edit)
    file=$(echo "$input" | jq -r '.tool_input.file_path // empty' | sed "s|$cwd/||")
    msg="edit $file"
    ;;
  write_file|Write)
    file=$(echo "$input" | jq -r '.tool_input.file_path // empty' | sed "s|$cwd/||")
    msg="write $file"
    ;;
  Glob)
    pattern=$(echo "$input" | jq -r '.tool_input.pattern // empty')
    msg="glob $pattern"
    ;;
  read_many_files)
    paths=$(echo "$input" | jq -r '.tool_input.paths[]? // empty' 2>/dev/null | head -n 3 | paste -sd ", " -)
    [ -z "$paths" ] && paths=$(echo "$input" | jq -r '.tool_input.path // empty')
    msg="read many ${paths:-files}"
    ;;
  grep|search_file_content|Grep)
    pattern=$(echo "$input" | jq -r '.tool_input.pattern // empty')
    msg="grep \"$pattern\""
    ;;
  list_directory)
    path=$(echo "$input" | jq -r '.tool_input.path // empty')
    msg="list ${path:-.}"
    ;;
  write_todos)
    msg="update todos"
    ;;
  activate_skill)
    skill=$(echo "$input" | jq -r '.tool_input.name // empty')
    msg="activate skill ${skill:-unknown}"
    ;;
  ask_user)
    msg="ask user"
    ;;
  Agent)
    desc=$(echo "$input" | jq -r '.tool_input.description // empty' | head -c 80)
    msg="spawn agent: $desc"
    ;;
  *)
    msg="$tool"
    ;;
esac

[ -z "$msg" ] && { echo '{}'; exit 0; }

post_message "$msg"

echo '{}'
exit 0
