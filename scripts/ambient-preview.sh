#!/usr/bin/env bash
# Restart the Ambient browser preview (serves web/ from disk, backend compiled from source).
# Usage: scripts/ambient-preview.sh [bundle.tar.zst]   (default: newest .preview/*.tar.zst)
set -euo pipefail
cd "$(dirname "$0")/.."
bundle="${1:-$(ls -t .preview/*.tar.zst | head -1)}"
pkill -f 'server.test.*TestAmbientBrowserPreview' 2>/dev/null || true
pkill -f 'server.test -test.run' 2>/dev/null || true
FLIGHTBAY_PREVIEW_ADDR=127.0.0.1:47800 FLIGHTBAY_PREVIEW_LIFETIME=48h FLIGHTBAY_AMBIENT_PREVIEW="$PWD/$bundle" \
  nohup go test ./internal/server -run TestAmbientBrowserPreview -v -timeout 49h -count=1 >/tmp/preview.log 2>&1 &
for i in $(seq 1 60); do grep -q AMBIENT_PREVIEW /tmp/preview.log 2>/dev/null && break; sleep 1; done
grep AMBIENT_PREVIEW /tmp/preview.log
