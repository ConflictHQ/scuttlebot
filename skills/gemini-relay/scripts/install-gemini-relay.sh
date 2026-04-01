#!/usr/bin/env bash
# Install the tracked Gemini relay hooks plus binary launcher into a local setup.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash skills/gemini-relay/scripts/install-gemini-relay.sh [options]

Options:
  --url URL                Set SCUTTLEBOT_URL in the shared env file.
  --token TOKEN            Set SCUTTLEBOT_TOKEN in the shared env file.
  --channel CHANNEL        Set SCUTTLEBOT_CHANNEL in the shared env file.
  --channels CSV           Set SCUTTLEBOT_CHANNELS in the shared env file.
  --transport MODE         Set SCUTTLEBOT_TRANSPORT (http or irc). Default: http.
  --irc-addr ADDR          Set SCUTTLEBOT_IRC_ADDR. Default: 127.0.0.1:6667.
  --irc-pass PASS          Write SCUTTLEBOT_IRC_PASS for fixed-identity IRC mode.
  --auto-register          Remove SCUTTLEBOT_IRC_PASS so IRC mode auto-registers session nicks. Default.
  --enabled                Write SCUTTLEBOT_HOOKS_ENABLED=1. Default.
  --disabled               Write SCUTTLEBOT_HOOKS_ENABLED=0.
  --config-file PATH       Shared env file path. Default: ~/.config/scuttlebot-relay.env
  --hooks-dir PATH         Gemini hooks install dir. Default: ~/.gemini/hooks
  --settings-json PATH     Gemini settings JSON. Default: ~/.gemini/settings.json
  --bin-dir PATH           Launcher install dir. Default: ~/.local/bin
  --help                   Show this help.

Environment defaults:
  SCUTTLEBOT_URL
  SCUTTLEBOT_TOKEN
  SCUTTLEBOT_CHANNEL
  SCUTTLEBOT_CHANNELS
  SCUTTLEBOT_TRANSPORT
  SCUTTLEBOT_IRC_ADDR
  SCUTTLEBOT_IRC_PASS
  SCUTTLEBOT_HOOKS_ENABLED
  SCUTTLEBOT_INTERRUPT_ON_MESSAGE
  SCUTTLEBOT_POLL_INTERVAL
  SCUTTLEBOT_PRESENCE_HEARTBEAT
  SCUTTLEBOT_CONFIG_FILE
  GEMINI_HOOKS_DIR
  GEMINI_SETTINGS_JSON
  GEMINI_BIN_DIR

Examples:
  bash skills/gemini-relay/scripts/install-gemini-relay.sh \
    --url http://localhost:8080 \
    --token "$(./run.sh token)" \
    --channel general

  SCUTTLEBOT_HOOKS_ENABLED=0 make install-gemini-relay
EOF
}

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)

SCUTTLEBOT_URL_VALUE="${SCUTTLEBOT_URL:-}"
SCUTTLEBOT_TOKEN_VALUE="${SCUTTLEBOT_TOKEN:-}"
SCUTTLEBOT_CHANNEL_VALUE="${SCUTTLEBOT_CHANNEL:-}"
SCUTTLEBOT_CHANNELS_VALUE="${SCUTTLEBOT_CHANNELS:-}"
SCUTTLEBOT_TRANSPORT_VALUE="${SCUTTLEBOT_TRANSPORT:-http}"
SCUTTLEBOT_IRC_ADDR_VALUE="${SCUTTLEBOT_IRC_ADDR:-127.0.0.1:6667}"
if [ -n "${SCUTTLEBOT_IRC_PASS:-}" ]; then
  SCUTTLEBOT_IRC_PASS_MODE="fixed"
  SCUTTLEBOT_IRC_PASS_VALUE="$SCUTTLEBOT_IRC_PASS"
else
  SCUTTLEBOT_IRC_PASS_MODE="auto"
  SCUTTLEBOT_IRC_PASS_VALUE=""
fi
SCUTTLEBOT_IRC_DELETE_ON_CLOSE_VALUE="${SCUTTLEBOT_IRC_DELETE_ON_CLOSE:-1}"
SCUTTLEBOT_HOOKS_ENABLED_VALUE="${SCUTTLEBOT_HOOKS_ENABLED:-1}"
SCUTTLEBOT_INTERRUPT_ON_MESSAGE_VALUE="${SCUTTLEBOT_INTERRUPT_ON_MESSAGE:-1}"
SCUTTLEBOT_POLL_INTERVAL_VALUE="${SCUTTLEBOT_POLL_INTERVAL:-2s}"
SCUTTLEBOT_PRESENCE_HEARTBEAT_VALUE="${SCUTTLEBOT_PRESENCE_HEARTBEAT:-60s}"

CONFIG_FILE="${SCUTTLEBOT_CONFIG_FILE:-$HOME/.config/scuttlebot-relay.env}"
HOOKS_DIR="${GEMINI_HOOKS_DIR:-$HOME/.gemini/hooks}"
SETTINGS_JSON="${GEMINI_SETTINGS_JSON:-$HOME/.gemini/settings.json}"
BIN_DIR="${GEMINI_BIN_DIR:-$HOME/.local/bin}"

while [ $# -gt 0 ]; do
  case "$1" in
    --url)
      SCUTTLEBOT_URL_VALUE="${2:?missing value for --url}"
      shift 2
      ;;
    --token)
      SCUTTLEBOT_TOKEN_VALUE="${2:?missing value for --token}"
      shift 2
      ;;
    --channel)
      SCUTTLEBOT_CHANNEL_VALUE="${2:?missing value for --channel}"
      shift 2
      ;;
    --channels)
      SCUTTLEBOT_CHANNELS_VALUE="${2:?missing value for --channels}"
      shift 2
      ;;
    --transport)
      SCUTTLEBOT_TRANSPORT_VALUE="${2:?missing value for --transport}"
      shift 2
      ;;
    --irc-addr)
      SCUTTLEBOT_IRC_ADDR_VALUE="${2:?missing value for --irc-addr}"
      shift 2
      ;;
    --irc-pass)
      SCUTTLEBOT_IRC_PASS_MODE="fixed"
      SCUTTLEBOT_IRC_PASS_VALUE="${2:?missing value for --irc-pass}"
      shift 2
      ;;
    --auto-register)
      SCUTTLEBOT_IRC_PASS_MODE="auto"
      SCUTTLEBOT_IRC_PASS_VALUE=""
      shift
      ;;
    --enabled)
      SCUTTLEBOT_HOOKS_ENABLED_VALUE=1
      shift
      ;;
    --disabled)
      SCUTTLEBOT_HOOKS_ENABLED_VALUE=0
      shift
      ;;
    --config-file)
      CONFIG_FILE="${2:?missing value for --config-file}"
      shift 2
      ;;
    --hooks-dir)
      HOOKS_DIR="${2:?missing value for --hooks-dir}"
      shift 2
      ;;
    --settings-json)
      SETTINGS_JSON="${2:?missing value for --settings-json}"
      shift 2
      ;;
    --bin-dir)
      BIN_DIR="${2:?missing value for --bin-dir}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      printf 'install-gemini-relay: unknown argument %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'install-gemini-relay: required command not found: %s\n' "$1" >&2
    exit 1
  fi
}

backup_file() {
  local path="$1"
  if [ -f "$path" ] && [ ! -f "${path}.bak" ]; then
    cp "$path" "${path}.bak"
  fi
}

ensure_parent_dir() {
  mkdir -p "$(dirname "$1")"
}

normalize_channels() {
  local primary="$1"
  local raw="$2"
  local IFS=','
  local items=()
  local extra_items=()
  local item channel seen=""

  if [ -n "$primary" ]; then
    items+=("$primary")
  fi
  if [ -n "$raw" ]; then
    read -r -a extra_items <<< "$raw"
    items+=("${extra_items[@]}")
  fi

  for item in "${items[@]}"; do
    channel="${item//[$' \t\r\n']/}"
    channel="${channel#\#}"
    [ -n "$channel" ] || continue
    case ",$seen," in
      *,"$channel",*) ;;
      *) seen="${seen:+$seen,}$channel" ;;
    esac
  done

  printf '%s' "$seen"
}

first_channel() {
  local channels
  channels=$(normalize_channels "" "$1")
  printf '%s' "${channels%%,*}"
}

if [ -z "$SCUTTLEBOT_CHANNEL_VALUE" ] && [ -n "$SCUTTLEBOT_CHANNELS_VALUE" ]; then
  SCUTTLEBOT_CHANNEL_VALUE="$(first_channel "$SCUTTLEBOT_CHANNELS_VALUE")"
fi
if [ -n "$SCUTTLEBOT_CHANNEL_VALUE" ]; then
  SCUTTLEBOT_CHANNELS_VALUE="$(normalize_channels "$SCUTTLEBOT_CHANNEL_VALUE" "$SCUTTLEBOT_CHANNELS_VALUE")"
fi

upsert_env_var() {
  local file="$1"
  local key="$2"
  local value="$3"
  local escaped
  escaped=$(printf '%q' "$value")
  awk -v key="$key" -v value="$escaped" '
    BEGIN { done = 0 }
    $0 ~ "^(export[[:space:]]+)?" key "=" {
      if (!done) {
        print key "=" value
        done = 1
      }
      next
    }
    { print }
    END {
      if (!done) {
        print key "=" value
      }
    }
  ' "$file" > "${file}.tmp"
  mv "${file}.tmp" "$file"
}

remove_env_var() {
  local file="$1"
  local key="$2"
  awk -v key="$key" '
    $0 ~ "^(export[[:space:]]+)?" key "=" { next }
    { print }
  ' "$file" > "${file}.tmp"
  mv "${file}.tmp" "$file"
}

need_cmd jq
need_cmd go

POST_CMD="$HOOKS_DIR/scuttlebot-post.sh"
CHECK_CMD="$HOOKS_DIR/scuttlebot-check.sh"
AFTER_AGENT_CMD="$HOOKS_DIR/scuttlebot-after-agent.sh"
LAUNCHER_DST="$BIN_DIR/gemini-relay"

mkdir -p "$HOOKS_DIR" "$BIN_DIR"
ensure_parent_dir "$SETTINGS_JSON"
ensure_parent_dir "$CONFIG_FILE"

backup_file "$POST_CMD"
backup_file "$CHECK_CMD"
backup_file "$AFTER_AGENT_CMD"
backup_file "$LAUNCHER_DST"
install -m 0755 "$REPO_ROOT/skills/gemini-relay/hooks/scuttlebot-post.sh" "$POST_CMD"
install -m 0755 "$REPO_ROOT/skills/gemini-relay/hooks/scuttlebot-check.sh" "$CHECK_CMD"
install -m 0755 "$REPO_ROOT/skills/gemini-relay/hooks/scuttlebot-after-agent.sh" "$AFTER_AGENT_CMD"

printf 'Building gemini-relay binary...\n'
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/gemini-relay.XXXXXX")
tmp_bin="$tmp_dir/gemini-relay"
cleanup_tmp_bin() {
  rm -rf "$tmp_dir"
}
trap cleanup_tmp_bin EXIT
(cd "$REPO_ROOT" && go build -o "$tmp_bin" ./cmd/gemini-relay)
install -m 0755 "$tmp_bin" "$LAUNCHER_DST"

backup_file "$SETTINGS_JSON"
if [ -f "$SETTINGS_JSON" ]; then
  jq --arg pre_matcher ".*" \
     --arg pre_cmd "$CHECK_CMD" \
     --arg post_matcher ".*" \
     --arg post_cmd "$POST_CMD" \
     --arg after_agent_matcher "*" \
     --arg after_agent_cmd "$AFTER_AGENT_CMD" '
    def ensure_matcher_entry(section; matcher; cmd):
      .hooks = (.hooks // {})
      | .hooks[section] = (.hooks[section] // [])
      | if any(.hooks[section][]?; .matcher == matcher) then
          .hooks[section] |= map(
            if .matcher == matcher then
              (.hooks = (.hooks // []))
              | if any(.hooks[]?; .type == "command" and .command == cmd) then . else .hooks += [{"type":"command","command":cmd}] end
            else
              .
            end
          )
        else
          .hooks[section] += [{"matcher":matcher,"hooks":[{"type":"command","command":cmd}]}]
        end;
    ensure_matcher_entry("BeforeTool"; $pre_matcher; $pre_cmd)
    | ensure_matcher_entry("AfterTool"; $post_matcher; $post_cmd)
    | ensure_matcher_entry("AfterAgent"; $after_agent_matcher; $after_agent_cmd)
  ' "$SETTINGS_JSON" > "${SETTINGS_JSON}.tmp"
else
  jq -n \
    --arg pre_cmd "$CHECK_CMD" \
    --arg post_cmd "$POST_CMD" \
    --arg after_agent_cmd "$AFTER_AGENT_CMD" '
    {
      hooks: {
        "BeforeTool": [
          {
            matcher: ".*",
            hooks: [{type: "command", command: $pre_cmd}]
          }
        ],
        "AfterTool": [
          {
            matcher: ".*",
            hooks: [{type: "command", command: $post_cmd}]
          }
        ],
        "AfterAgent": [
          {
            matcher: "*",
            hooks: [{type: "command", command: $after_agent_cmd}]
          }
        ]
      }
    }
  ' > "${SETTINGS_JSON}.tmp"
fi
mv "${SETTINGS_JSON}.tmp" "$SETTINGS_JSON"

backup_file "$CONFIG_FILE"
if [ ! -f "$CONFIG_FILE" ]; then
  : > "$CONFIG_FILE"
fi
if [ -n "$SCUTTLEBOT_URL_VALUE" ]; then
  upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_URL "$SCUTTLEBOT_URL_VALUE"
fi
if [ -n "$SCUTTLEBOT_TOKEN_VALUE" ]; then
  upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_TOKEN "$SCUTTLEBOT_TOKEN_VALUE"
fi
if [ -n "$SCUTTLEBOT_CHANNEL_VALUE" ]; then
  upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_CHANNEL "${SCUTTLEBOT_CHANNEL_VALUE#\#}"
fi
if [ -n "$SCUTTLEBOT_CHANNELS_VALUE" ]; then
  upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_CHANNELS "$SCUTTLEBOT_CHANNELS_VALUE"
fi
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_TRANSPORT "$SCUTTLEBOT_TRANSPORT_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_IRC_ADDR "$SCUTTLEBOT_IRC_ADDR_VALUE"
if [ "$SCUTTLEBOT_IRC_PASS_MODE" = "fixed" ]; then
  upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_IRC_PASS "$SCUTTLEBOT_IRC_PASS_VALUE"
else
  remove_env_var "$CONFIG_FILE" SCUTTLEBOT_IRC_PASS
fi
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_IRC_DELETE_ON_CLOSE "$SCUTTLEBOT_IRC_DELETE_ON_CLOSE_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_HOOKS_ENABLED "$SCUTTLEBOT_HOOKS_ENABLED_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_INTERRUPT_ON_MESSAGE "$SCUTTLEBOT_INTERRUPT_ON_MESSAGE_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_POLL_INTERVAL "$SCUTTLEBOT_POLL_INTERVAL_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_PRESENCE_HEARTBEAT "$SCUTTLEBOT_PRESENCE_HEARTBEAT_VALUE"

printf 'Installed Gemini relay files:\n'
printf '  hooks:      %s\n' "$HOOKS_DIR"
printf '  settings:   %s\n' "$SETTINGS_JSON"
printf '  launcher:   %s\n' "$LAUNCHER_DST"
printf '  env:        %s\n' "$CONFIG_FILE"
printf '  irc auth:   %s\n' "$([ "$SCUTTLEBOT_IRC_PASS_MODE" = "fixed" ] && printf 'fixed-pass override' || printf 'auto-register')"
printf '\n'
printf 'Next steps:\n'
printf '  1. Launch with: %s\n' "$LAUNCHER_DST"
printf '  2. Watch IRC for: gemini-{repo}-{session}\n'
printf '  3. Mention that nick to interrupt before the next action\n'
printf '\n'
printf 'Disable without uninstalling:\n'
printf '  SCUTTLEBOT_HOOKS_ENABLED=0 %s\n' "$LAUNCHER_DST"
