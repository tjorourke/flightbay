#!/usr/bin/env bash
# Restart the Ambient browser preview (serves web/ from disk, backend compiled from source).
# Usage: scripts/ambient-preview.sh [bundle.tar.zst]   (default: newest .preview/*.tar.zst)
set -euo pipefail
cd "$(dirname "$0")/.."
bundle="${1:-$(ls -t .preview/*.tar.zst | head -1)}"
pkill -f 'ambient-preview.test' 2>/dev/null || true
go test -c -o /tmp/ambient-preview.test ./internal/server
python3 - "$PWD" "$bundle" <<'PY'
import os, subprocess, sys
root, bundle = sys.argv[1], sys.argv[2]
env = dict(os.environ, FLIGHTBAY_AMBIENT_PREVIEW_DEMO='1', FLIGHTBAY_PREVIEW_ADDR='127.0.0.1:47800',
           FLIGHTBAY_PREVIEW_LIFETIME='48h', FLIGHTBAY_AMBIENT_PREVIEW=os.path.join(root, bundle))
log = open('/tmp/preview.log', 'w')
subprocess.Popen(['/tmp/ambient-preview.test', '-test.run', 'TestAmbientBrowserPreview', '-test.v', '-test.timeout', '49h'],
                 cwd=os.path.join(root, 'internal/server'), env=env, stdout=log, stderr=log, start_new_session=True)
PY
for i in $(seq 1 60); do grep -q AMBIENT_PREVIEW /tmp/preview.log 2>/dev/null && break; sleep 1; done
grep AMBIENT_PREVIEW /tmp/preview.log
