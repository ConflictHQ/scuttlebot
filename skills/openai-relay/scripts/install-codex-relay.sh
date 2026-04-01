#!/usr/bin/env bash
# Install the tracked Codex relay hooks plus the compiled broker into a local Codex setup.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash skills/openai-relay/scripts/install-codex-relay.sh [options]

Options:
  --url URL                Set SCUTTLEBOT_URL in the shared env file.
  --token TOKEN            Set SCUTTLEBOT_TOKEN in the shared env file.
  --channel CHANNEL        Set SCUTTLEBOT_CHANNEL in the shared env file.
  --transport MODE         Set SCUTTLEBOT_TRANSPORT (http or irc). Default: http.
  --irc-addr ADDR          Set SCUTTLEBOT_IRC_ADDR. Default: 127.0.0.1:6667.
  --enabled                Write SCUTTLEBOT_HOOKS_ENABLED=1. Default.
  --disabled               Write SCUTTLEBOT_HOOKS_ENABLED=0.
  --config-file PATH       Shared env file path. Default: ~/.config/scuttlebot-relay.env
  --hooks-dir PATH         Codex hooks install dir. Default: ~/.codex/hooks
  --hooks-json PATH        Codex hooks config JSON. Default: ~/.codex/hooks.json
  --codex-config PATH      Codex config TOML. Default: ~/.codex/config.toml
  --bin-dir PATH           Launcher install dir. Default: ~/.local/bin
  --help                   Show this help.

Environment defaults:
  SCUTTLEBOT_URL
  SCUTTLEBOT_TOKEN
  SCUTTLEBOT_CHANNEL
  SCUTTLEBOT_TRANSPORT
  SCUTTLEBOT_IRC_ADDR
  SCUTTLEBOT_IRC_PASS
  SCUTTLEBOT_HOOKS_ENABLED
  SCUTTLEBOT_INTERRUPT_ON_MESSAGE
  SCUTTLEBOT_POLL_INTERVAL
  SCUTTLEBOT_PRESENCE_HEARTBEAT
  SCUTTLEBOT_CONFIG_FILE
  CODEX_HOOKS_DIR
  CODEX_HOOKS_JSON
  CODEX_CONFIG_TOML
  CODEX_BIN_DIR

Examples:
  bash skills/openai-relay/scripts/install-codex-relay.sh \
    --url http://localhost:8080 \
    --token "$(./run.sh token)" \
    --channel general
EOF
}

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)

SCUTTLEBOT_URL_VALUE="${SCUTTLEBOT_URL:-}"
SCUTTLEBOT_TOKEN_VALUE="${SCUTTLEBOT_TOKEN:-}"
SCUTTLEBOT_CHANNEL_VALUE="${SCUTTLEBOT_CHANNEL:-}"
SCUTTLEBOT_TRANSPORT_VALUE="${SCUTTLEBOT_TRANSPORT:-http}"
SCUTTLEBOT_IRC_ADDR_VALUE="${SCUTTLEBOT_IRC_ADDR:-127.0.0.1:6667}"
SCUTTLEBOT_IRC_PASS_VALUE="${SCUTTLEBOT_IRC_PASS:-}"
SCUTTLEBOT_HOOKS_ENABLED_VALUE="${SCUTTLEBOT_HOOKS_ENABLED:-1}"
SCUTTLEBOT_INTERRUPT_ON_MESSAGE_VALUE="${SCUTTLEBOT_INTERRUPT_ON_MESSAGE:-1}"
SCUTTLEBOT_POLL_INTERVAL_VALUE="${SCUTTLEBOT_POLL_INTERVAL:-2s}"
SCUTTLEBOT_PRESENCE_HEARTBEAT_VALUE="${SCUTTLEBOT_PRESENCE_HEARTBEAT:-60s}"

CONFIG_FILE="${SCUTTLEBOT_CONFIG_FILE:-$HOME/.config/scuttlebot-relay.env}"
HOOKS_DIR="${CODEX_HOOKS_DIR:-$HOME/.codex/hooks}"
HOOKS_JSON="${CODEX_HOOKS_JSON:-$HOME/.codex/hooks.json}"
CODEX_CONFIG="${CODEX_CONFIG_TOML:-$HOME/.codex/config.toml}"
BIN_DIR="${CODEX_BIN_DIR:-$HOME/.local/bin}"

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
    --transport)
      SCUTTLEBOT_TRANSPORT_VALUE="${2:?missing value for --transport}"
      shift 2
      ;;
    --irc-addr)
      SCUTTLEBOT_IRC_ADDR_VALUE="${2:?missing value for --irc-addr}"
      shift 2
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
    --hooks-json)
      HOOKS_JSON="${2:?missing value for --hooks-json}"
      shift 2
      ;;
    --codex-config)
      CODEX_CONFIG="${2:?missing value for --codex-config}"
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
      printf 'install-codex-relay: unknown argument %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'install-codex-relay: required command not found: %s\n' "$1" >&2
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

ensure_codex_hooks_feature() {
  local file="$1"
  local tmp="${file}.tmp"
  if [ ! -f "$file" ]; then
    cat > "$tmp" <<'EOF'
[features]
codex_hooks = true
EOF
    mv "$tmp" "$file"
    return
  fi

  awk '
    BEGIN {
      in_features = 0
      features_seen = 0
      codex_hooks_set = 0
    }
    /^\[features\][[:space:]]*$/ {
      print
      in_features = 1
      features_seen = 1
      next
    }
    /^\[/ {
      if (in_features && !codex_hooks_set) {
        print "codex_hooks = true"
        codex_hooks_set = 1
      }
      in_features = 0
      print
      next
    }
    {
      if (in_features && $0 ~ /^[[:space:]]*codex_hooks[[:space:]]*=/) {
        if (!codex_hooks_set) {
          print "codex_hooks = true"
          codex_hooks_set = 1
        }
        next
      }
      print
    }
    END {
      if (!features_seen) {
        if (NR > 0) {
          print ""
        }
        print "[features]"
        print "codex_hooks = true"
      } else if (in_features && !codex_hooks_set) {
        print "codex_hooks = true"
      }
    }
  ' "$file" > "$tmp"
  mv "$tmp" "$file"
}

need_cmd go
need_cmd jq

POST_CMD="$HOOKS_DIR/scuttlebot-post.sh"
CHECK_CMD="$HOOKS_DIR/scuttlebot-check.sh"
LAUNCHER_DST="$BIN_DIR/codex-relay"

mkdir -p "$HOOKS_DIR" "$BIN_DIR"
ensure_parent_dir "$HOOKS_JSON"
ensure_parent_dir "$CODEX_CONFIG"
ensure_parent_dir "$CONFIG_FILE"

backup_file "$POST_CMD"
backup_file "$CHECK_CMD"
backup_file "$LAUNCHER_DST"
install -m 0755 "$REPO_ROOT/skills/openai-relay/hooks/scuttlebot-post.sh" "$POST_CMD"
install -m 0755 "$REPO_ROOT/skills/openai-relay/hooks/scuttlebot-check.sh" "$CHECK_CMD"
(cd "$REPO_ROOT" && go build -o "$LAUNCHER_DST" ./cmd/codex-relay)

backup_file "$HOOKS_JSON"
if [ -f "$HOOKS_JSON" ]; then
  jq --arg pre_matcher "Bash|Edit|Write" \
     --arg pre_cmd "$CHECK_CMD" \
     --arg post_matcher "Bash|Read|Edit|Write|Glob|Grep|Agent" \
     --arg post_cmd "$POST_CMD" '
    def ensure_command(matcher; cmd):
      .hooks = (.hooks // {})
      | .hooks[matcher] = (.hooks[matcher] // [])
      | if any(.hooks[matcher][]?; .type == "command" and .command == cmd) then
          .
        else
          .hooks[matcher] += [{"type":"command","command":cmd}]
        end;
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
    ensure_matcher_entry("pre-tool-use"; $pre_matcher; $pre_cmd)
    | ensure_matcher_entry("post-tool-use"; $post_matcher; $post_cmd)
  ' "$HOOKS_JSON" > "${HOOKS_JSON}.tmp"
else
  jq -n \
    --arg pre_cmd "$CHECK_CMD" \
    --arg post_cmd "$POST_CMD" '
    {
      hooks: {
        "pre-tool-use": [
          {
            matcher: "Bash|Edit|Write",
            hooks: [{type: "command", command: $pre_cmd}]
          }
        ],
        "post-tool-use": [
          {
            matcher: "Bash|Read|Edit|Write|Glob|Grep|Agent",
            hooks: [{type: "command", command: $post_cmd}]
          }
        ]
      }
    }
  ' > "${HOOKS_JSON}.tmp"
fi
mv "${HOOKS_JSON}.tmp" "$HOOKS_JSON"

backup_file "$CODEX_CONFIG"
ensure_codex_hooks_feature "$CODEX_CONFIG"

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
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_TRANSPORT "$SCUTTLEBOT_TRANSPORT_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_IRC_ADDR "$SCUTTLEBOT_IRC_ADDR_VALUE"
if [ -n "$SCUTTLEBOT_IRC_PASS_VALUE" ]; then
  upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_IRC_PASS "$SCUTTLEBOT_IRC_PASS_VALUE"
fi
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_HOOKS_ENABLED "$SCUTTLEBOT_HOOKS_ENABLED_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_INTERRUPT_ON_MESSAGE "$SCUTTLEBOT_INTERRUPT_ON_MESSAGE_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_POLL_INTERVAL "$SCUTTLEBOT_POLL_INTERVAL_VALUE"
upsert_env_var "$CONFIG_FILE" SCUTTLEBOT_PRESENCE_HEARTBEAT "$SCUTTLEBOT_PRESENCE_HEARTBEAT_VALUE"

printf 'Installed Codex relay files:\n'
printf '  hooks:    %s\n' "$HOOKS_DIR"
printf '  hooks.json: %s\n' "$HOOKS_JSON"
printf '  config:   %s\n' "$CODEX_CONFIG"
printf '  broker:   %s\n' "$LAUNCHER_DST"
printf '  env:      %s\n' "$CONFIG_FILE"
printf '\n'
printf 'Next steps:\n'
printf '  1. Launch with: %s\n' "$LAUNCHER_DST"
printf '  2. Watch IRC for: codex-{repo}-{session}\n'
printf '  3. Mention that nick to interrupt before the next action\n'
printf '\n'
printf 'Disable without uninstalling:\n'
printf '  SCUTTLEBOT_HOOKS_ENABLED=0 %s\n' "$LAUNCHER_DST"
