#!/usr/bin/env bash
# Run the Ambient UX comparison for every mockup screen against the preview fixture.
# Usage: scripts/ambient-ux-check.sh [bundle]   Output: docs/ux/ambient/ux-review/<screen>/{app.png,side-by-side.png,report.md}
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/ambient-preview.sh "$@" >/dev/null
for f in web/ambient*.js; do node --check "$f"; done
python3 docs/ux/ambient/tools/ux_compare.py --base 'http://127.0.0.1:47800/?token=ambient-ui-test' --all || true
node docs/ux/ambient/tools/drawers.mjs add:access-rule add:methods add:jwt add:l4-default-deny add:weighted-routing add:header-routing add:mirroring add:load-balancing add:strict-mtls add:timeouts add:endpoint-failover add:circuit-breaker add:retries add:rate-limit add:waypoint add:enrol-namespace
