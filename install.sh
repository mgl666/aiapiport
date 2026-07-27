#!/bin/sh
set -e

REPO="mgl666/aiapiport"
BIN="aiapiport"
INSTALL_DIR="/usr/local/bin"

# ---- detect platform ----
OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
  Linux)
    case "$ARCH" in
      x86_64)  ASSET="${BIN}-linux-amd64" ;;
      aarch64) ASSET="${BIN}-linux-arm64" ;;
      *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
    esac
    ;;
  Darwin)
    case "$ARCH" in
      arm64)   ASSET="${BIN}-darwin-arm64" ;;
      x86_64)  ASSET="${BIN}-darwin-amd64" ;;
      *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
    esac
    ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# ---- resolve version ----
if [ -z "$VERSION" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
fi

if [ -z "$VERSION" ]; then
  echo "Could not determine latest version. Set VERSION= env to override." >&2
  exit 1
fi

URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"

echo "Installing aiapiport ${VERSION} (${ASSET}) to ${INSTALL_DIR}..."

# ---- download and install ----
TMP="$(mktemp)"
curl -fsSL "$URL" -o "$TMP"
chmod +x "$TMP"

# install to /usr/local/bin; use sudo if needed
if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP" "${INSTALL_DIR}/${BIN}"
else
  sudo mv "$TMP" "${INSTALL_DIR}/${BIN}"
fi

echo "Done. Run: aiapiport --version"
