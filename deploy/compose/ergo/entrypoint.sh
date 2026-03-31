#!/bin/sh
set -e

CONFIG_DIR="${ERGO_DATA_DIR:-/ircd}"
CONFIG_FILE="${CONFIG_DIR}/ircd.yaml"
TEMPLATE="/ergo/ircd.yaml.tmpl"

mkdir -p "${CONFIG_DIR}"

# Render template with env var substitution.
envsubst < "${TEMPLATE}" > "${CONFIG_FILE}"

exec ergo run --conf "${CONFIG_FILE}"
