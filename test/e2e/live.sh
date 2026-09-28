#!/usr/bin/env bash
# Flightbay live mode, end to end, against a throwaway kind cluster with its
# own kubeconfig (your ~/.kube/config is not touched).
#
#   test/e2e/live.sh           # create kind-flightbay-live, test, delete it
#   KEEP=1 test/e2e/live.sh    # keep the cluster afterwards (reused next run)
#
# Needs: go, kind, kubectl, docker, curl, jq.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
CLUSTER=${CLUSTER:-flightbay-live}
CTX=kind-$CLUSTER
CANARY=FLIGHTBAY-LIVE-CANARY-7c1e
WORK=$(mktemp -d "${TMPDIR:-/tmp}/flightbay-live.XXXXXX")
KCFG=$WORK/kubeconfig
export FLIGHTBAY_HOME=$WORK/home
BIN=$ROOT/bin
DESK_PID="" WATCH_PID="" OPEN_PID=""
PASSED=0

cleanup() {
  for p in $WATCH_PID $OPEN_PID; do kill "$p" 2>/dev/null || true; done
  [ -n "$DESK_PID" ] && kill "$DESK_PID" 2>/dev/null || true
  if [ "${KEEP:-0}" != "1" ]; then kind delete cluster --name "$CLUSTER" --kubeconfig "$KCFG" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

pass() { PASSED=$((PASSED + 1)); printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; echo "  work dir kept at $WORK (desk log: $WORK/desk.log)"; trap - EXIT; cleanup; exit 1; }
step() { printf '\n\033[36m== %s\033[0m\n' "$*"; }
contains() { grep -q -- "$2" <<<"$1" || fail "$3 (expected to find: $2)"; }
absent() { if grep -q -- "$2" <<<"$1"; then fail "$3 (found: $2)"; fi; }
k() { kubectl --kubeconfig "$KCFG" --context "$CTX" "$@"; }
# kubectl against the desk's read-only API (the live mirror)
m() { KUBECONFIG="$WORK/ws/kubeconfig" kubectl "$@"; }
api() { curl -sf -H "Authorization: Bearer $TOKEN" "$URL$1"; }
post() { curl -sf -X POST -H "Authorization: Bearer $TOKEN" "$URL$1"; }
# until SECONDS CMD...: retry a check every half second
until_ok() { local t=$1; shift; local end=$((SECONDS + t)); until "$@"; do [ $SECONDS -lt $end ] || return 1; sleep 0.5; done; }

step "Build"
(cd "$ROOT" && CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/ ./cmd/flightbay && go build -o bin/ ./cmd/flightbay-desk)
pass "collector built with CGO_ENABLED=0; desk built"

step "Cluster (own kubeconfig) and a small app"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind get kubeconfig --name "$CLUSTER" >"$KCFG"
else
  kind create cluster --name "$CLUSTER" --kubeconfig "$KCFG" --wait 120s >/dev/null
fi
k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: demo}
---
apiVersion: v1
kind: Secret
metadata: {name: db, namespace: demo}
stringData: {password: $CANARY}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: talker, namespace: demo}
spec:
  replicas: 2
  selector: {matchLabels: {app: talker}}
  template:
    metadata: {labels: {app: talker}}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
      - name: app
        image: busybox:1.36
        command: [sh, -c]
        args:
        - |
          echo "starting pod \$HOSTNAME Authorization: Bearer $CANARY"
          i=0
          while [ ! -f /tmp/crash ]; do echo "tick \$i from \$HOSTNAME level=info"; i=\$((i+1)); sleep 0.2; done
          echo "crash-marker: \$HOSTNAME is exiting on purpose"
          rm -f /tmp/crash
          exit 1
EOF
k -n demo rollout status deploy/talker --timeout=180s >/dev/null || fail "talker did not start"
# A reused cluster (KEEP=1) may still be scaled up or terminating pods from the last run.
k -n demo delete pod ztunnel-e2e --ignore-not-found --wait=true >/dev/null 2>&1 || true
twopods() { [ "$(k -n demo get pods -l app=talker --no-headers 2>/dev/null | wc -l | tr -d ' ')" = 2 ]; }
until_ok 90 twopods || fail "talker did not settle at 2 pods"
pass "demo/talker running with 2 pods writing logs"

step "flightbay-desk live: dry run"
out=$("$BIN/flightbay-desk" live --kubeconfig "$KCFG" --context "$CTX" --namespaces demo --dry-run)
contains "$out" "Required RBAC (live adds watch)" "dry run prints RBAC"
contains "$out" "get, list, watch" "live RBAC includes watch"
absent "$out" "create" "no write verbs in the live RBAC"
pass "dry run prints the plan and RBAC with watch, and no write verbs"

step "Start live mode"
START=$(date +%s)
"$BIN/flightbay-desk" live --kubeconfig "$KCFG" --context "$CTX" --namespaces demo --no-browser --listen 127.0.0.1:0 \
  --workdir "$WORK/ws" --save-dir "$WORK/saved" --log-tail 50 >"$WORK/desk.log" 2>&1 &
DESK_PID=$!
until_ok 30 test -s "$WORK/ws/desk.json" || fail "desk did not start"
URL=$(jq -r .url "$WORK/ws/desk.json"); TOKEN=$(jq -r .token "$WORK/ws/desk.json")
contains "$(cat "$WORK/desk.log")" "READ-ONLY" "start-up says it is read-only"
phase() { [ "$(api /api/workspace | jq -r .state.phase)" = live ]; }
until_ok 90 phase || fail "phase live not reached"
pass "phase live after $(( $(date +%s) - START ))s"
ws=$(api /api/workspace)
[ "$(jq -r .source <<<"$ws")" = live ] && [ "$(jq -r .live.context <<<"$ws")" = "$CTX" ] && [ "$(jq -r .live.connected <<<"$ws")" = true ] || fail "workspace live fields: $(jq -c .live <<<"$ws")"
pass "/api/workspace reports live=true, context $CTX, connected"

step "Pods through the API and the snapshot kubectl"
pods() { api "/api/objects?resource=pods&namespace=demo" | jq -r '.rows[].name' | sort; }
[ "$(pods | wc -l | tr -d ' ')" = 2 ] || fail "API pods: $(pods)"
out=$(m get pods -n demo)
[ "$(grep -c talker <<<"$out")" = 2 ] || fail "snapshot kubectl: $out"
contains "$(m config current-context)" "$CTX@live" "kubeconfig context is <context>@live"
pass "2 pods via /api/objects and KUBECONFIG=<ws>/kubeconfig kubectl get pods (context $CTX@live)"
count() { api "/api/logs/search?q=ns:demo&limit=1" | jq -r .total; }
haslogs() { [ "$(count)" -gt 0 ]; }
until_ok 20 haslogs || fail "no log lines arrived"
c1=$(count); sleep 4; c2=$(count)
[ "$c2" -gt "$c1" ] || fail "log count did not increase ($c1 -> $c2)"
pass "log lines arrive: $c1 -> $c2 in 4s"
red=$(api "/api/logs/search?q=$CANARY&limit=5" | jq -r .total)
[ "$red" = 0 ] || fail "canary found in logs"
sql=$(api "/api/sql?q=$(jq -rn --arg c "$CANARY" '"SELECT count(*) FROM objects WHERE object LIKE '"'"'%" + $c + "%'"'"'" | @uri')" | jq -r '.rows[0][0]')
[ "$sql" = 0 ] || fail "canary found in objects ($sql)"
pass "redaction: the canary is in neither logs nor objects (Secret value never kept)"

step "Changes: scale up, delete, kubectl get pods -w through the mirror"
m get pods -n demo -w --output-watch-events >"$WORK/watch.log" 2>&1 &
WATCH_PID=$!
sleep 1
before=$(pods)
k -n demo scale deploy/talker --replicas=3 >/dev/null
t0=$SECONDS
three() { [ "$(pods | wc -l | tr -d ' ')" -ge 3 ]; }
until_ok 15 three || fail "third pod did not appear within 15s"
newpod=$(comm -13 <(echo "$before") <(pods) | head -1)
pass "third pod $newpod appeared in $((SECONDS - t0))s"
victim=$(echo "$before" | head -1)
k -n demo delete pod "$victim" --wait=false >/dev/null
t0=$SECONDS
goneck() { ! pods | grep -qx "$victim"; }
until_ok 20 goneck || fail "$victim still listed after delete"
pass "deleted pod $victim disappeared in $((SECONDS - t0))s"
sleep 2
kill "$WATCH_PID" 2>/dev/null || true; wait "$WATCH_PID" 2>/dev/null || true; WATCH_PID=""
wl=$(cat "$WORK/watch.log")
contains "$wl" "ADDED.*$newpod" "kubectl -w saw the new pod"
contains "$wl" "DELETED.*$victim" "kubectl -w saw the deletion"
contains "$wl" "MODIFIED" "kubectl -w saw modifications"
pass "kubectl get pods -w on the mirror saw ADDED $newpod, MODIFIED, DELETED $victim"
del=$(m -n demo delete pod "$newpod" 2>&1 || true)
contains "$del" "read-only" "writes through the mirror are refused"
k -n demo get pod "$newpod" >/dev/null 2>&1 || fail "pod deleted in the real cluster"
pass "kubectl delete through the mirror is refused and the real pod is untouched"

step "Crash a container: previous logs are fetched after the restart"
k -n demo exec "$newpod" -- touch /tmp/crash
restarted() { [ "$(k -n demo get pod "$newpod" -o jsonpath='{.status.containerStatuses[0].restartCount}')" -ge 1 ]; }
until_ok 60 restarted || fail "$newpod did not restart"
prev() { m -n demo logs "$newpod" --previous 2>/dev/null | grep -q "crash-marker"; }
until_ok 30 prev || fail "previous log not fetched: $(m -n demo logs "$newpod" --previous 2>&1 | tail -3)"
pass "kubectl logs --previous on the mirror shows the crashed container's last lines"
cur() { m -n demo logs "$newpod" --tail 5 2>/dev/null | grep -q "tick"; }
until_ok 30 cur || fail "current stream not resumed after the restart"
pass "the current stream resumed after the restart"

step "Save as bundle"
saved=$(post /api/live/save) || fail "save failed"
path=$(jq -r .path <<<"$saved")
[ -f "$path" ] || fail "saved bundle missing: $saved"
insp=$("$BIN/flightbay" inspect "$path")
contains "$insp" "Verified: yes" "flightbay inspect verifies the saved bundle"
contains "$insp" "logs demo/" "saved bundle has log coverage"
pass "saved $(basename "$path") ($(wc -c <"$path" | tr -d ' ') bytes); flightbay inspect: Verified: yes"
FLIGHTBAY_HOME=$WORK/home2 "$BIN/flightbay-desk" open "$path" --no-browser --listen 127.0.0.1:0 --workdir "$WORK/ws2" >"$WORK/open.log" 2>&1 &
OPEN_PID=$!
until_ok 30 test -s "$WORK/ws2/desk.json" || fail "desk open did not start"
OURL=$(jq -r .url "$WORK/ws2/desk.json"); OTOKEN=$(jq -r .token "$WORK/ws2/desk.json")
oapi() { curl -sf -H "Authorization: Bearer $OTOKEN" "$OURL$1"; }
opened() { [ "$(oapi /api/workspace | jq -r .state.phase)" = complete ]; }
until_ok 30 opened || fail "saved bundle did not open: $(tail -5 "$WORK/open.log")"
n=$(oapi "/api/objects?resource=pods&namespace=demo" | jq '.rows | length')
[ "$n" -ge 3 ] || fail "opened bundle has $n demo pods"
[ "$(oapi /api/workspace | jq -r .state.verified)" = true ] || fail "opened bundle not verified"
[ "$(oapi /api/logs/search?q=crash-marker | jq -r .total)" -ge 1 ] || fail "previous logs missing from the saved bundle"
kill "$OPEN_PID" 2>/dev/null || true; wait "$OPEN_PID" 2>/dev/null || true; OPEN_PID=""
pass "flightbay-desk open shows the $n pods and the crash log from the saved bundle"

step "Solo diagnostics on demand (--solo-diagnostics)"
# A stand-in ztunnel: busybox httpd serving the two admin endpoints the
# collector reads from ztunnel. Detection is by image name.
mkdir -p "$WORK/zt"
cat >"$WORK/zt/Dockerfile" <<'DOCKER'
FROM busybox:1.36
RUN mkdir -p /www && echo '{"workloads":{"demo/talker":{"name":"talker","namespace":"demo"}},"services":{},"certificates":[]}' >/www/config_dump \
 && printf '# TYPE istio_build gauge\nistio_build{component="ztunnel"} 1\n' >/www/metrics
CMD ["sh", "-c", "httpd -p 15020 -h /www && httpd -f -p 15000 -h /www"]
DOCKER
docker build -q -t flightbay.local/ztunnel:e2e "$WORK/zt" >/dev/null || fail "stand-in ztunnel image did not build"
kind load docker-image flightbay.local/ztunnel:e2e --name "$CLUSTER" >/dev/null 2>&1 || fail "kind load failed"
k -n demo apply -f - >/dev/null <<'POD'
apiVersion: v1
kind: Pod
metadata: {name: ztunnel-e2e, namespace: demo, labels: {app: ztunnel}}
spec:
  containers:
  - {name: ztunnel, image: flightbay.local/ztunnel:e2e, imagePullPolicy: Never}
POD
k -n demo wait --for=condition=Ready pod/ztunnel-e2e --timeout=60s >/dev/null || fail "stand-in ztunnel not ready"
"$BIN/flightbay-desk" live --kubeconfig "$KCFG" --context "$CTX" --namespaces demo --no-browser --listen 127.0.0.1:0 \
  --workdir "$WORK/ws3" --solo-diagnostics --no-logs >"$WORK/diag.log" 2>&1 &
OPEN_PID=$!
until_ok 30 test -s "$WORK/ws3/desk.json" || fail "diagnostics desk did not start"
DURL=$(jq -r .url "$WORK/ws3/desk.json"); DTOKEN=$(jq -r .token "$WORK/ws3/desk.json")
dapi() { curl -sf -H "Authorization: Bearer $DTOKEN" "$DURL$1"; }
dcaptured() { [ "$(dapi /api/proxies | jq '[.components[] | select(.pod == "ztunnel-e2e")] | length')" -ge 1 ]; }
until_ok 60 dcaptured || fail "initial diagnostics pass did not capture the ztunnel: $(tail -5 "$WORK/diag.log")"
pass "diagnostics read once when the mirror was ready"
r=$(curl -sf -X POST -H "Authorization: Bearer $DTOKEN" "$DURL/api/live/refresh-diagnostics?pod=demo/ztunnel-e2e") || fail "refresh failed"
[ "$(jq -r .files <<<"$r")" -ge 2 ] || fail "refresh captured too little: $r"
dapi "/api/proxy/file?cluster=$CTX&kind=proxies&namespace=demo&pod=ztunnel-e2e&file=config_dump.json" | grep -q talker || fail "config_dump not served after refresh"
bad=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $DTOKEN" "$DURL/api/live/refresh-diagnostics?pod=demo/$newpod")
[ "$bad" = 400 ] || fail "refresh of a non-Solo pod should be refused ($bad)"
# The live state is republished once a second.
pfcounted() { dapi /api/workspace | jq -e '.live.requests.portforward >= 2' >/dev/null; }
until_ok 10 pfcounted || fail "port-forward not counted by the guard: $(dapi /api/workspace | jq -c .live.requests)"
dreqs=$(dapi /api/workspace | jq -c .live.requests)
[ "$(dapi /api/workspace | jq -r .live.refused)" = 0 ] || fail "guard refused requests with diagnostics on"
kill "$OPEN_PID" 2>/dev/null || true; wait "$OPEN_PID" 2>/dev/null || true; OPEN_PID=""
pass "POST /api/live/refresh-diagnostics replaced ztunnel-e2e's files ($(jq -r .files <<<"$r") files); requests $dreqs"
k -n demo delete pod ztunnel-e2e --wait=false >/dev/null 2>&1 || true

step "Nothing was written to the cluster"
reqs=$(api /api/workspace | jq -c .live.requests)
refused=$(api /api/workspace | jq -r .live.refused)
bad=$(jq -r 'keys[] | select(. != "discovery" and . != "get" and . != "list" and . != "watch" and . != "log")' <<<"$reqs")
[ -z "$bad" ] || fail "verbs other than get/list/watch/log: $bad ($reqs)"
[ "$refused" = 0 ] || fail "the guard refused $refused requests"
pass "requests made: $reqs; refused 0"
kill -INT "$DESK_PID"; wait "$DESK_PID" 2>/dev/null || true; DESK_PID=""
contains "$(cat "$WORK/desk.log")" "refused 0" "shutdown summary"
pass "shutdown summary: $(grep 'Requests made' "$WORK/desk.log" | sed 's/^ *//')"

printf '\n\033[32mAll %d live checks passed.\033[0m\n' "$PASSED"
rm -rf "$WORK"
