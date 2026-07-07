#!/bin/sh
# Rowtr installer — macOS & Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/chouli12/rowtr-releases/main/install.sh | sh
#
# Downloads the right zip for this machine, installs `rowtr` into your PATH,
# and (macOS) puts Rowtr.app in ~/Applications. curl downloads carry no
# browser quarantine flag, so there's no Gatekeeper wall to fight.
#
# Overrides: ROWTR_VERSION, ROWTR_INSTALL_DIR, ROWTR_BASE_URL (for testing).
set -eu

VERSION="${ROWTR_VERSION:-0.1.0}"
REPO="chouli12/rowtr-releases"
BASE="${ROWTR_BASE_URL:-https://github.com/$REPO/releases/download/v$VERSION}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  darwin|linux) ;;
  *) echo "unsupported OS: $OS (Windows: download the windows zip from github.com/$REPO/releases)"; exit 1 ;;
esac
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH"; exit 1 ;;
esac

NAME="rowtr-$VERSION-$OS-$ARCH"
URL="$BASE/$NAME.zip"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "→ downloading $URL"
curl -fSL --progress-bar "$URL" -o "$TMP/rowtr.zip"
unzip -q "$TMP/rowtr.zip" -d "$TMP"

# Install dir: prefer /usr/local/bin when writable, else ~/.local/bin.
DIR="${ROWTR_INSTALL_DIR:-}"
if [ -z "$DIR" ]; then
  if [ -w /usr/local/bin ]; then DIR=/usr/local/bin; else DIR="$HOME/.local/bin"; fi
fi
mkdir -p "$DIR"
install -m 0755 "$TMP/$NAME/rowtr" "$DIR/rowtr"
echo "→ installed rowtr to $DIR/rowtr"

if [ "$OS" = darwin ] && [ -d "$TMP/$NAME/Rowtr.app" ]; then
  mkdir -p "$HOME/Applications"
  rm -rf "$HOME/Applications/Rowtr.app"
  cp -R "$TMP/$NAME/Rowtr.app" "$HOME/Applications/Rowtr.app"
  echo "→ installed menu-bar app to ~/Applications/Rowtr.app"
fi

case ":$PATH:" in
  *":$DIR:"*) ;;
  *) echo "⚠ $DIR is not on your PATH — add:  export PATH=\"$DIR:\$PATH\"" ;;
esac

echo
echo "Done. Next:"
echo "  rowtr setup     # installs Ollama + a local model if missing"
echo "  rowtr claude    # use instead of \`claude\` — routing + savings tracking"
