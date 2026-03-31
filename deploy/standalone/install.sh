#!/usr/bin/env bash
# install.sh — scuttlebot standalone installer
# Usage: curl -fsSL https://scuttlebot.dev/install.sh | bash
#   or:  bash install.sh [--version v0.1.0] [--dir /usr/local/bin]
set -euo pipefail

REPO="ConflictHQ/scuttlebot"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-}"

# Parse flags.
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --dir)     INSTALL_DIR="$2"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="x86_64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# Fetch latest version if not specified.
if [[ -z "$VERSION" ]]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"tag_name": "\(.*\)".*/\1/')
fi

ASSET="scuttlebot-${VERSION}-${OS}-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"

echo "Installing scuttlebot ${VERSION} for ${OS}/${ARCH}..."
echo "Downloading ${URL}"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" -o "${TMP}/${ASSET}"
tar -xzf "${TMP}/${ASSET}" -C "$TMP"

install -m 755 "${TMP}/scuttlebot" "${INSTALL_DIR}/scuttlebot"

echo ""
echo "scuttlebot ${VERSION} installed to ${INSTALL_DIR}/scuttlebot"
echo ""
echo "Quick start:"
echo "  scuttlebot             # boots ergo + daemon, auto-downloads ergo on first run"
echo "  scuttlebot --config /path/to/scuttlebot.yaml"
echo ""
echo "See: https://scuttlebot.dev/docs/deployment/standalone"
