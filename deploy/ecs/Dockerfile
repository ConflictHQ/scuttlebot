################################################################################
# scuttlebot — JupyterHub-compatible ECS image
#
# Ports:
#   8070  jupyterhub-singleuser (JupyterHub proxy target)
#   8080  scuttlebot HTTP API
#   8081  scuttlebot MCP
#   6667  Ergo IRC (agent relay connections)
#
# supervisord manages all processes. EFS is mounted at /mnt/efs;
# SCUTTLEBOT_DATA_DIR points into the user's home there.
################################################################################

FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /scuttlebot \
    ./cmd/scuttlebot

# ── Runtime ───────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl \
    python3 python3-venv \
    supervisor \
    && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/venv && \
    /opt/venv/bin/pip install --no-cache-dir \
      "jupyterhub>=4.0,<6.0" \
      "jupyter-server-proxy>=4.0"

ENV PATH="/opt/venv/bin:$PATH"

COPY --from=builder /scuttlebot /usr/local/bin/scuttlebot

COPY deploy/ecs/supervisord.conf /etc/supervisor/conf.d/scuttlebot.conf
COPY deploy/ecs/entrypoint.sh    /entrypoint.sh
RUN chmod +x /entrypoint.sh

# jupyter-server-proxy named-path config: /scuttlebot → port 8080
COPY deploy/ecs/jupyter_server_config.py /etc/jupyter/jupyter_server_config.py

RUN groupadd -g 1000 scuttlebot && \
    useradd -m -u 1000 -g scuttlebot -s /bin/bash scuttlebot

ENV SCUTTLEBOT_DATA_DIR=/home/scuttlebot/.scuttlebot
ENV SCUTTLEBOT_API_ADDR=:8080
ENV SCUTTLEBOT_MCP_ADDR=:8081
ENV JUPYTERHUB_DEFAULT_URL=/scuttlebot

VOLUME ["/home/scuttlebot"]

EXPOSE 8070 8080 8081 6667

ENTRYPOINT ["/entrypoint.sh"]
