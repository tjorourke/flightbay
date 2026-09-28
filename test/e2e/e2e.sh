#!/usr/bin/env bash
# Flightbay end-to-end test against a dedicated kind cluster.
#
#   test/e2e/e2e.sh            # create (or reuse) kind-flightbay-e2e, test, delete it
#   KEEP=1 test/e2e/e2e.sh     # keep the cluster afterwards
#
# Needs: go, kind, kubectl, docker, zstd, curl. k9s is optional (smoke test).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
CLUSTER=${CLUSTER:-flightbay-e2e}
CTX=kind-$CLUSTER
CANARY=FLIGHTBAY-E2E-CANARY-9d2f
WORK=$(mktemp -d "${TMPDIR:-/tmp}/flightbay-e2e.XXXXXX")
export FLIGHTBAY_HOME=$WORK/home
BIN=$ROOT/bin
DESK_PID=""
PASSED=0

cleanup() {
  [ -n "$DESK_PID" ] && kill "$DESK_PID" 2>/dev/null || true
  if [ "${KEEP:-0}" != "1" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

pass() { PASSED=$((PASSED + 1)); printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; echo "  work dir kept at $WORK"; trap - EXIT; cleanup; exit 1; }
step() { printf '\n\033[36m== %s\033[0m\n' "$*"; }
contains() { grep -q -- "$2" <<<"$1" || fail "$3 (expected to find: $2)"; }
absent() { if grep -q -- "$2" <<<"$1"; then fail "$3 (found: $2)"; fi; }
LIVE_KUBECONFIG=${KUBECONFIG:-$HOME/.kube/config}
k() { kubectl --kubeconfig "$LIVE_KUBECONFIG" --context "$CTX" "$@"; }

step "Build"
(cd "$ROOT" && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/ ./cmd/flightbay && go build -o bin/ ./cmd/flightbay-desk)
pass "collector built with CGO_ENABLED=0; desk built"

step "Cluster and seeded faults"
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 120s
ROUTES=1
if ! k apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.1/standard-install.yaml >/dev/null 2>&1; then
  ROUTES=0; echo "  (Gateway API CRDs unavailable offline; skipping the HTTPRoute checks)"
fi
k apply -f "$ROOT/test/e2e/fixtures.yaml" >/dev/null
if [ "$ROUTES" = 1 ]; then
  k wait --for=condition=Established crd/httproutes.gateway.networking.k8s.io --timeout=60s >/dev/null
  k apply -f "$ROOT/test/e2e/routes.yaml" >/dev/null
fi
# On a reused cluster (KEEP=1) the checkout pod may have crashlooped for hours
# and the kubelet can lose the previous container's log. Start it fresh.
k -n payments delete pod -l app=checkout --wait=true >/dev/null 2>&1 || true
prevok=0
for i in $(seq 1 90); do
  restarts=$(k -n payments get pods -l app=checkout -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}' 2>/dev/null || echo 0)
  pull=$(k -n payments get pod bad-image -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}' 2>/dev/null || true)
  healthy=$(k -n payments get pod healthy -o jsonpath='{.status.phase}' 2>/dev/null || true)
  prevok=0
  # In CrashLoopBackOff the crash output is in the current stream, and the
  # kubelet may already have garbage-collected the previous container's log.
  if [ "${restarts:-0}" -ge 2 ] && { k -n payments logs -l app=checkout 2>/dev/null; k -n payments logs -l app=checkout --previous 2>/dev/null; } | grep -q "Process exiting"; then prevok=1; fi
  if [ "$prevok" = 1 ] && [[ "$pull" == *Image* || "$pull" == ErrImagePull ]] && [ "$healthy" = Running ]; then break; fi
  sleep 2
done
[ "${restarts:-0}" -ge 2 ] || fail "checkout never crash looped"
[ "$prevok" = 1 ] || fail "checkout crash log not readable from the live cluster"
pass "faults seeded (checkout restarts=$restarts, bad-image=$pull, inventory Pending)"
sleep 3

step "Collector: dry run and RBAC"
out=$("$BIN/flightbay" collect --context "$CTX" --namespaces payments --dry-run)
contains "$out" "pods" "dry run lists pods"
contains "$out" "Required RBAC" "dry run prints RBAC"
absent "$out" "Resource types.*nodes" "dry run excludes cluster-scoped types by default"
[ -z "$(ls "$WORK" | grep tar.zst || true)" ] || fail "dry run wrote a bundle"
pass "dry run shows the selection and RBAC without collecting"
rbac=$("$BIN/flightbay" rbac --namespaces payments --resources pods,deployments.apps)
contains "$rbac" "kind: Role" "namespace-scoped Role"
contains "$rbac" "pods/log" "logs permission"
absent "$rbac" "portforward" "no port-forward without opt-in"
pass "policy-derived RBAC is namespace-scoped and minimal"

step "Collector: standard collection of one namespace"
"$BIN/flightbay" collect --context "$CTX" --namespaces payments --case E2E-1 --since 1h -o "$WORK" -q
B=$(ls "$WORK"/flightbay-E2E-1-*.tar.zst)
[ -f "$B" ] || fail "bundle not written"
[ ! -e "$B.partial" ] || fail "partial file left behind"
pass "bundle written: $(basename "$B") ($(wc -c <"$B" | tr -d ' ') bytes)"
# Grep the raw bytes: macOS strings(1) does not reliably scan non-object input.
if zstd -dc "$B" | grep -aq -- "$CANARY"; then fail "canary must never appear in the bundle (found in the decompressed bytes)"; fi
raw=$(zstd -dc "$B" | tr -c '[:print:]\n' ' ')
absent "$raw" "EXCLUDED-NAMESPACE-MARKER" "excluded namespace logs must not be collected"
absent "$raw" "should-not-appear" "excluded namespace objects must not be collected"
pass "redaction canary absent from the decompressed bundle; namespace scope honoured"
members=$(zstd -dc "$B" | tar -tf -)
[ "$(head -1 <<<"$members")" = "bundle.json" ] || fail "bundle.json is not first"
[ "$(tail -1 <<<"$members")" = "manifest.json" ] || fail "manifest.json is not last"
pass "stock zstd + tar open the bundle; header first, manifest last"
ins=$("$BIN/flightbay" inspect "$B" --redactions)
contains "$ins" "Verified: yes" "inspect verifies"
contains "$ins" "Secret values: never collected" "inspect states the secret rule"
grep -Eq "Redactions: [1-9]" <<<"$ins" || fail "no redactions recorded"
pass "inspect verifies checksums and reports redactions"

step "Collector: customer exclusions"
"$BIN/flightbay" collect --context "$CTX" --namespaces payments --case E2E-2 --exclude-resources configmaps,secrets --no-logs -o "$WORK" -q
B2=$(ls "$WORK"/flightbay-E2E-2-*.tar.zst)
m2=$(zstd -dc "$B2" | tar -tf -)
absent "$m2" "/configmaps/" "configmaps excluded"
absent "$m2" "/secrets/" "secrets excluded"
absent "$m2" "/logs/" "logs excluded"
contains "$m2" "/pods/" "pods still collected"
pass "excluded resources and logs are absent from the bundle"

step "Collector: age encryption"
RECIP=$("$BIN/flightbay-desk" keygen -o "$WORK/support.key" 2>/dev/null)
"$BIN/flightbay" collect --context "$CTX" --namespaces payments --case E2E-3 --encrypt-to "$RECIP" -o "$WORK" -q
B3=$(ls "$WORK"/flightbay-E2E-3-*.tar.zst.age)
head -c 21 "$B3" | grep -q "age-encryption.org/v1" || fail "not age encrypted"
if "$BIN/flightbay" inspect "$B3" >/dev/null 2>&1; then fail "encrypted bundle opened without a key"; fi
contains "$("$BIN/flightbay" inspect "$B3" -i "$WORK/support.key")" "Verified: yes" "encrypted bundle verifies with the key"
pass "encrypted bundle unreadable without the identity, verified with it"

wait_ready() { # $1 = workdir
  for i in $(seq 1 150); do [ -f "$1/desk.json" ] && return 0; sleep 0.2; done
  fail "desk did not become ready"
}
desk_field() { python3 -c "import json,sys;print(json.load(open('$1/desk.json'))['$2'])"; }
api() { curl -sf -H "Authorization: Bearer $TOKEN" "$URL$1"; }

step "Desk: open the bundle"
"$BIN/flightbay-desk" open "$B" --no-browser --listen 127.0.0.1:0 --workdir "$WORK/ws" >"$WORK/desk.log" 2>&1 &
DESK_PID=$!
wait_ready "$WORK/ws"
URL=$(desk_field "$WORK/ws" url); TOKEN=$(desk_field "$WORK/ws" token)
for i in $(seq 1 100); do grep -q "Import complete" "$WORK/desk.log" && break; sleep 0.1; done
contains "$(cat "$WORK/desk.log")" "Import complete and verified" "import completes"
ws=$(api /api/workspace)
contains "$ws" '"verified":true' "workspace verified"
contains "$ws" '"case":"E2E-1"' "case recorded"
pass "desk opened, import verified"
code=$(curl -s -o /dev/null -w '%{http_code}' "$URL/api/workspace")
[ "$code" = 401 ] || fail "API reachable without the token ($code)"
pass "API refuses requests without the per-process token"

step "Desk: kubectl against the snapshot"
export KUBECONFIG=$WORK/ws/kubeconfig
contains "$(kubectl config get-contexts)" "$CTX@snapshot" "snapshot context"
pods=$(kubectl get pods -n payments)
contains "$pods" "checkout-" "kubectl get pods"
grep -Eq "checkout-.*(CrashLoopBackOff|Error|Completed|Running)" <<<"$pods" || fail "pod status column missing"
contains "$pods" "Pending" "inventory Pending shown"
absent "$(kubectl get pods -A)" "excluded-team" "excluded namespace not served"
POD=$(kubectl get pods -n payments -l app=checkout -o jsonpath='{.items[0].metadata.name}')
desc=$(kubectl describe pod -n payments "$POD")
contains "$desc" "Restart Count" "kubectl describe renders the pod"
contains "$desc" "BackOff" "describe shows captured events"
prev=$(kubectl logs -n payments "$POD" --previous 2>&1 || true)
cur=$(kubectl logs -n payments "$POD" 2>&1 || true)
if grep -q "database connection timed out" <<<"$prev"; then CRASH=(--previous); crash=$prev; else CRASH=(); crash=$cur
  contains "$prev" "this log stream was not collected" "an uncollected --previous stream is reported, not faked"
fi
contains "$crash" "database connection timed out" "kubectl logs shows the crash"
contains "$crash" "[REDACTED]" "bearer token redacted in logs"
absent "$prev$cur" "$CANARY" "no canary in served logs"
absent "$prev$cur" "^unable to retrieve container logs" "kubelet error text never served as log lines"
contains "$(kubectl logs -n payments "$POD" ${CRASH[@]+"${CRASH[@]}"} --tail=1)" "Process exiting" "--tail"
contains "$(kubectl get deploy,svc -n payments)" "deployment.apps/checkout" "deployments and services"
contains "$(kubectl get secret checkout-db -n payments -o yaml)" "valuesCollected: false" "secret served as metadata only"
contains "$(kubectl get ns)" "payments" "namespaces synthesised"
if err=$(kubectl delete pod -n payments "$POD" 2>&1); then fail "delete succeeded"; fi
contains "$err" "read-only snapshot" "writes rejected with a clear message"
if [ "$ROUTES" = 1 ]; then contains "$(kubectl get httproutes -n payments)" "checkout-route" "CRD objects served"; fi
pass "kubectl get/describe/logs/-o yaml work; writes are rejected"
if command -v k9s >/dev/null; then
  # Drive k9s in a pty for a few seconds, then kill it hard. k9s ignores
  # SIGALRM and `script` blocks on a non-tty stdin, so neither can be trusted
  # to end the smoke test. Keep k9s state inside the work dir.
  K9S_CONFIG_DIR=$WORK/k9s XDG_DATA_HOME=$WORK/xdg XDG_STATE_HOME=$WORK/xdg XDG_CACHE_HOME=$WORK/xdg \
  python3 - "$WORK/k9s.out" k9s --kubeconfig "$KUBECONFIG" -n payments -c pods --readonly <<'PY' || true
import fcntl, os, pty, select, signal, struct, sys, termios, time
out, argv = sys.argv[1], sys.argv[2:]
pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    os.execvp(argv[0], argv)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 160, 0, 0))
buf, end = b"", time.time() + 8
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            buf += os.read(fd, 65536)
        except OSError:
            break
os.kill(pid, signal.SIGKILL)
os.waitpid(pid, 0)
open(out, "wb").write(buf)
PY
  if grep -aq "checkout-" "$WORK/k9s.out"; then pass "k9s lists snapshot pods"; else fail "k9s did not show pods"; fi
fi

export KUBECONFIG=$LIVE_KUBECONFIG

step "Desk: API, findings and helpers"
f=$(api /api/findings)
for rule in workload.crashloop workload.unschedulable workload.image-pull logs.error-signature; do contains "$f" "\"$rule\"" "finding $rule"; done
if [ "$ROUTES" = 1 ]; then contains "$f" '"gateway.unresolved-backend"' "absence rule fires once Service coverage is complete"; fi
contains "$(api '/api/logs/search?q=trace_id:a4e98f02')" '"total":' "trace search"
[ "$(api '/api/logs/search?q=trace_id:a4e98f02' | python3 -c 'import json,sys;print(json.load(sys.stdin)["total"])')" -ge 3 ] || fail "trace pivot finds fewer than 3 lines"
fbq=$(FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbq" 'level:error')
contains "$fbq" "database connection timed out" "fbq helper"
contains "$(FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbfind")" "CRITICAL" "fbfind helper"
contains "$(curl -sf -H "Authorization: Bearer $TOKEN" "$URL/")" "flightbay" "UI served"
pass "findings, trace pivot, fbq/fbfind helpers and UI"
srch=$(env FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbsearch" 'kind:pod ns:payments "connection pool"')
contains "$srch" "checkout-" "fbsearch finds a pod by text inside its spec"
contains "$(env FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbsearch" 'busybox')" "Deployment" "object search matches content (images) across kinds"
contains "$(api '/api/search?q=checkout')" '"kind":"Service"' "the jump palette searches every kind"
sq=$(env FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbsql" "SELECT count(*) AS n FROM logs WHERE msg ILIKE '%timed out%'")
grep -Eq "^[1-9]" <<<"$(sed -n 2p <<<"$sq")" || fail "fbsql counted no lines: $sq"
if env FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbsql" "DELETE FROM log_lines" >/dev/null 2>&1; then fail "SQL write accepted"; fi
contains "$(env FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbsql" "SELECT kind FROM objects WHERE name = 'checkout-db'")" "Secret" "objects are queryable in SQL"
absent "$(api "/api/sql?q=SELECT%20string_agg(object,'')%20FROM%20objects")" "$CANARY" "no canary reachable through SQL"
[ -f "$WORK/ws/workspace.duckdb" ] || fail "no DuckDB workspace file"
pass "resource search (CLI, API), read-only SQL over logs and objects, DuckDB workspace"

step "Desk: best-practice scores"
pst=$(api /api/posture)
contains "$pst" 'Istio ambient' "posture reports the ambient area"
contains "$pst" '"checks"' "posture returns its checks"
score=$(env FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws/bin/fbscore" --all)
contains "$score" "Istio ambient: " "fbscore prints the ambient area (scored, or why not)"
contains "$score" "Agentic (agentgateway, kagent, MCP): " "fbscore prints the agentic area (scored, or why not)"
absent "$pst" "$CANARY" "no canary in posture output"
pass "posture API and fbscore score the snapshot and give fixes"
kill "$DESK_PID"; wait "$DESK_PID" 2>/dev/null || true; DESK_PID=""

step "Desk: open with no argument, workspace cleaned up on exit"
mkdir -p "$WORK/cwd" && cp "$B" "$WORK/cwd/"
(cd "$WORK/cwd" && FLIGHTBAY_HOME=$WORK/home2 "$BIN/flightbay-desk" open --no-browser --listen 127.0.0.1:0 >"$WORK/desk-cwd.log" 2>&1) &
DESK_PID=$!
for i in $(seq 1 100); do grep -q "Import complete" "$WORK/desk-cwd.log" 2>/dev/null && break; sleep 0.1; done
contains "$(cat "$WORK/desk-cwd.log")" "Opening flightbay-E2E-1" "picked the bundle in the current directory"
[ -n "$(ls "$WORK/home2/workspaces")" ] || fail "no workspace while open"
pkill -INT -f "flightbay-desk open --no-browser --listen 127.0.0.1:0$" 2>/dev/null || kill -INT "$DESK_PID"
for i in $(seq 1 50); do [ -z "$(ls "$WORK/home2/workspaces")" ] && break; sleep 0.1; done
[ -z "$(ls "$WORK/home2/workspaces")" ] || fail "workspace left behind after closing"
DESK_PID=""
pass "flightbay-desk open with no argument; workspace removed on close"
contains "$("$BIN/flightbay" inspect "$ROOT/examples/sample-bundle.tar.zst")" "Verified: yes" "sample bundle verifies"
pass "the shipped sample bundle verifies"

step "Desk: progressive loading (throttled to rehearse a slow upload)"
SIZE=$(wc -c <"$B" | tr -d ' ')
RATE=$((SIZE / 8 + 1))
"$BIN/flightbay-desk" open "$B" --no-browser --listen 127.0.0.1:0 --workdir "$WORK/ws2" --throttle "$RATE" >"$WORK/desk2.log" 2>&1 &
DESK_PID=$!
wait_ready "$WORK/ws2"
URL=$(desk_field "$WORK/ws2" url); TOKEN=$(desk_field "$WORK/ws2" token)
seen_loading=0
for i in $(seq 1 100); do
  ws=$(api /api/workspace)
  if grep -q '"phase":"loading"' <<<"$ws"; then
    rows=$(api '/api/objects?cluster=&group=&resource=pods' | python3 -c 'import json,sys;print(len(json.load(sys.stdin)["rows"]))')
    if [ "$rows" -gt 0 ]; then
      seen_loading=1
      KUBECONFIG=$WORK/ws2/kubeconfig kubectl get pods -n payments >/dev/null || fail "kubectl failed while loading"
      logs=$(api '/api/logs/search?q=%22no-such-phrase%22')
      contains "$logs" '"loading":true' "search reports loading"
      break
    fi
  fi
  sleep 0.1
done
[ "$seen_loading" = 1 ] || fail "never observed objects while the import was still loading"
pass "pods browsable and kubectl working while the bundle was still loading"
for i in $(seq 1 200); do grep -q "Import complete" "$WORK/desk2.log" && break; sleep 0.1; done
contains "$(api /api/workspace)" '"verified":true' "throttled import completes and verifies"
pass "throttled import finished and verified"
kill "$DESK_PID"; wait "$DESK_PID" 2>/dev/null || true; DESK_PID=""

step "Streaming: collect straight into the desk"
("$BIN/flightbay" collect --context "$CTX" --namespaces payments --case E2E-4 -o - -q | \
  "$BIN/flightbay-desk" open - --no-browser --listen 127.0.0.1:0 --workdir "$WORK/ws3" >"$WORK/desk3.log" 2>&1) &
DESK_PID=$!
wait_ready "$WORK/ws3"
for i in $(seq 1 300); do grep -q "Import complete" "$WORK/desk3.log" && break; sleep 0.1; done
contains "$(cat "$WORK/desk3.log")" "Import complete and verified" "stdin stream imported and verified"
pass "collector output streamed directly into the desk"
pkill -f "flightbay-desk open - --no-browser" 2>/dev/null || true
DESK_PID=""

step "Truncated bundle"
head -c $((SIZE * 2 / 3)) "$B" >"$WORK/truncated.tar.zst"
if "$BIN/flightbay" inspect "$WORK/truncated.tar.zst" >/dev/null 2>&1; then fail "truncated bundle verified"; fi
"$BIN/flightbay-desk" open "$WORK/truncated.tar.zst" --no-browser --listen 127.0.0.1:0 --workdir "$WORK/ws4" >"$WORK/desk4.log" 2>&1 &
DESK_PID=$!
for i in $(seq 1 100); do grep -q "Import stopped" "$WORK/desk4.log" && break; sleep 0.1; done
contains "$(cat "$WORK/desk4.log")" "Import stopped" "desk reports the truncated import"
pass "truncated bundles fail verification and are flagged in the desk"

printf '\n\033[32mAll %d end-to-end checks passed.\033[0m\n' "$PASSED"
