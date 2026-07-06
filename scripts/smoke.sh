#!/bin/sh
# Rowtr package smoke test — runs against an unzipped release package.
# Usage: smoke.sh <path-to-zip>
# Verifies the binary starts, reports its version, can create its data dir and
# usage DB, and that the proxy boots and answers its health endpoint. Needs:
# unzip, curl. Exits non-zero on any failure.
set -eu

ZIP="$1"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "── unzip $ZIP"
unzip -q "$ZIP" -d "$WORK"
PKG="$(find "$WORK" -mindepth 1 -maxdepth 1 -type d | head -1)"
BIN="$PKG/rowtr"
[ -f "$BIN" ] || BIN="$PKG/rowtr.exe"

echo "── package contents"
[ -f "$PKG/LICENSE.txt" ]   || { echo "FAIL: LICENSE.txt missing"; exit 1; }
[ -f "$PKG/QUICKSTART.md" ] || { echo "FAIL: QUICKSTART.md missing"; exit 1; }
echo "   license + quickstart present"

echo "── version"
V="$("$BIN" --version)"
echo "   $V"
case "$V" in rowtr\ *) ;; *) echo "FAIL: unexpected version output"; exit 1;; esac

echo "── usage (exercises SQLite store creation)"
"$BIN" usage >/dev/null || { echo "FAIL: rowtr usage errored"; exit 1; }
echo "   usage db ok"

echo "── proxy boot + health"
"$BIN" serve --mode observe --addr 127.0.0.1:8791 >/dev/null 2>&1 &
PID=$!
ok=0
i=0
while [ $i -lt 20 ]; do
  if curl -sf http://127.0.0.1:8791/rowtr/health | grep -q '"service":"rowtr"'; then
    ok=1; break
  fi
  i=$((i+1)); sleep 0.5
done
kill $PID 2>/dev/null || true
[ $ok -eq 1 ] || { echo "FAIL: proxy health never came up"; exit 1; }
echo "   health endpoint ok"

echo "PASS: $(basename "$ZIP")"
