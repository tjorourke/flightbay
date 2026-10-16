#!/usr/bin/env bash
# Proxy diagnostics against an existing mesh cluster (read-only: lists,
# log reads and port-forwards to admin ports; nothing is changed).
#
#   MESH_CONTEXT=kind-mesh1 test/e2e/mesh.sh
#   MESH_CONTEXT=kind-mesh1 MESH_NAMESPACES=istio-system,kagent test/e2e/mesh.sh
#
# Needs a cluster running istiod and ztunnel in the selected namespaces;
# agentgateway gateways or waypoints are checked when present.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
CTX=${MESH_CONTEXT:?set MESH_CONTEXT to a kubeconfig context running Istio ambient}
NS=${MESH_NAMESPACES:-istio-system,kagent,agentgateway-system}
WORK=$(mktemp -d "${TMPDIR:-/tmp}/flightbay-mesh.XXXXXX")
export FLIGHTBAY_HOME=$WORK/home
BIN=$ROOT/bin
DESK_PID=""
PASSED=0
cleanup() { [ -n "$DESK_PID" ] && kill "$DESK_PID" 2>/dev/null || true; }
trap cleanup EXIT
pass() { PASSED=$((PASSED + 1)); printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n  work dir kept at %s\n' "$*" "$WORK"; exit 1; }
step() { printf '\n\033[36m== %s\033[0m\n' "$*"; }

step "Build"
(cd "$ROOT" && CGO_ENABLED=0 go build -o bin/ ./cmd/flightbay && go build -o bin/ ./cmd/flightbay-desk)
pass "built"

step "Collect $NS from $CTX with --diagnostics"
"$BIN/flightbay" collect --context "$CTX" --namespaces "$NS" --diagnostics --case MESH-E2E --since 30m -o "$WORK" -q
B=$(ls "$WORK"/flightbay-MESH-E2E-*.tar.zst)
members=$(zstd -dc "$B" | tar -tf -)
grep -q "/diagnostics/proxies/.*/ztunnel-.*/config_dump.json" <<<"$members" || fail "no ztunnel config_dump"
grep -q "/diagnostics/control-plane/.*/istiod-.*/syncz.json" <<<"$members" || fail "no istiod syncz"
grep -q "/diagnostics/summary.json" <<<"$members" || fail "no diagnostics summary"
pass "ztunnel, istiod and the diagnostics summary are in the bundle ($(grep -c /diagnostics/ <<<"$members") diagnostic members)"
if grep -q "krt" <<<"$members"; then fail "a controller krt snapshot was collected"; fi
pass "no controller krt snapshot"

step "Namespace scope inside the dumps"
mkdir -p "$WORK/x" && zstd -dc "$B" | tar -xf - -C "$WORK/x"
others=$(kubectl --context "$CTX" get ns -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep -vxF -f <(tr ',' '\n' <<<"$NS") || true)
leaks=""
for ns in $others; do
  # Only data forms count: a namespace field, a service FQDN or a SPIFFE identity.
  if grep -rqE "\"namespace\": \"$ns\"|\.$ns\.svc|/ns/$ns/" "$WORK/x/clusters"/*/diagnostics 2>/dev/null; then leaks="$leaks $ns"; fi
done
[ -z "$leaks" ] || fail "namespaces outside the selection found in diagnostics:$leaks"
pass "no workloads, services or identities from $(wc -w <<<"$others" | tr -d ' ') unselected namespaces"

step "Desk: proxies API and fbpc"
"$BIN/flightbay-desk" open "$B" --no-browser --listen 127.0.0.1:0 --workdir "$WORK/ws" >"$WORK/desk.log" 2>&1 &
DESK_PID=$!
for i in $(seq 1 150); do grep -q "Import complete" "$WORK/desk.log" 2>/dev/null && break; sleep 0.2; done
grep -q "Import complete and verified" "$WORK/desk.log" || fail "import did not verify"
list=$("$BIN/flightbay-desk" proxy-config)
grep -q "ztunnel" <<<"$list" || fail "fbpc does not list ztunnel"
grep -q "istiod" <<<"$list" || fail "fbpc does not list istiod"
zt=$(awk '$2=="ztunnel"{print $4; exit}' <<<"$list")
out=$("$BIN/flightbay-desk" proxy-config "$zt")
grep -q "== Workloads" <<<"$out" || fail "fbpc $zt has no workloads table"
grep -q "== Certificates" <<<"$out" || fail "fbpc $zt has no certificates"
syncz=$("$BIN/flightbay-desk" proxy-config istiod --section syncz)
grep -q "synced\|STALE" <<<"$syncz" || fail "fbpc istiod shows no xDS sync state"
pass "fbpc lists components, and shows ztunnel workloads/certificates and istiod sync state"
if grep -qE "^[^ ]+ +agentgateway " <<<"$list"; then
  ag=$(awk '$2=="agentgateway"{print $4; exit}' <<<"$list")
  agout=$("$BIN/flightbay-desk" proxy-config "$ag")
  grep -q "== Listeners and routes" <<<"$agout" || fail "agentgateway summary missing"
  pass "agentgateway $ag summary"
fi

printf '\n\033[32mAll %d mesh checks passed.\033[0m\n' "$PASSED"
