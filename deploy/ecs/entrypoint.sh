#!/bin/bash
# scuttlebot ECS entrypoint
# Sets up data directory, then hands off to supervisord.
set -euo pipefail

# Ensure data dir exists (EFS may not pre-create subdirs)
SCUTTLEBOT_DATA_DIR="${SCUTTLEBOT_DATA_DIR:-/home/scuttlebot/.scuttlebot}"
mkdir -p "$SCUTTLEBOT_DATA_DIR"

# Compute JUPYTERHUB_SERVICE_PREFIX if not already set.
# JupyterHub injects JUPYTERHUB_USER and JUPYTERHUB_SERVER_NAME; the prefix
# the single-user server must register under is /user/{user}/{server}/
if [ -z "${JUPYTERHUB_SERVICE_PREFIX:-}" ] && [ -n "${JUPYTERHUB_USER:-}" ]; then
    SERVER="${JUPYTERHUB_SERVER_NAME:-}"
    if [ -n "$SERVER" ]; then
        export JUPYTERHUB_SERVICE_PREFIX="/user/${JUPYTERHUB_USER}/${SERVER}/"
    else
        export JUPYTERHUB_SERVICE_PREFIX="/user/${JUPYTERHUB_USER}/"
    fi
fi

exec supervisord -c /etc/supervisor/conf.d/scuttlebot.conf
