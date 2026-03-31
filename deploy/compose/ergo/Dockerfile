FROM ghcr.io/ergochat/ergo:stable

# Install envsubst (gettext package).
USER root
RUN apk add --no-cache gettext

COPY ircd.yaml.tmpl /ergo/ircd.yaml.tmpl
COPY entrypoint.sh /ergo/entrypoint.sh
RUN chmod +x /ergo/entrypoint.sh

ENTRYPOINT ["/ergo/entrypoint.sh"]
