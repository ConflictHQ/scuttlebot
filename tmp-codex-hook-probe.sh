#!/bin/sh
set -eu
log_file="${CODEX_HOOK_PROBE_LOG:-/tmp/codex-hook-fired.log}"
input="$(cat || true)"
event="$(printf '%s' "$input" | jq -r '.hook_event_name // "unknown"' 2>/dev/null || printf 'unknown')"
{
  printf '---\n'
  printf 'event=%s\n' "$event"
  printf '%s\n' "$input"
} >> "$log_file"
exit 0
