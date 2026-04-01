#!/usr/bin/env bash
# Development wrapper for the compiled Gemini relay broker.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)

if [ -x "$REPO_ROOT/bin/gemini-relay" ]; then
  exec "$REPO_ROOT/bin/gemini-relay" "$@"
fi

if ! command -v go >/dev/null 2>&1; then
  printf 'gemini-relay: go is required to run the broker from the repo checkout\n' >&2
  exit 1
fi

exec go run "$REPO_ROOT/cmd/gemini-relay" "$@"
