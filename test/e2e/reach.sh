#!/usr/bin/env bash
# Agent reach end to end, on a dedicated kind cluster running Istio ambient.
#
#   test/e2e/reach.sh            # create (or reuse) kind-flightbay-reach, test, delete it
#   KEEP=1 test/e2e/reach.sh     # keep the cluster afterwards (a rerun reuses it)
#
# Builds the OpenAI and Hugging Face incident shape (two agents, a registry that
# accepts PUT and MKCOL and can reach the internet, a database), then proves the
# reach analysis twice: statically, from Flightbay's report on a capture, and
# live, with real requests. It applies the fix YAML Flightbay generated, exactly
# as generated, and checks the traffic really is blocked and the report agrees.
#
# Needs: go, kind, kubectl, docker, istioctl, python3, curl. The cluster gets
# its own kubeconfig file, so no other context is read or changed.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
CLUSTER=${CLUSTER:-flightbay-reach}
CTX=kind-$CLUSTER
ISTIOCTL=${ISTIOCTL:-$HOME/.istioctl/bin/istioctl}
[ -x "$ISTIOCTL" ] || ISTIOCTL=$(command -v istioctl)
# The Solo istioctl defaults to private images; upstream ones need no licence.
ISTIO_HUB=${ISTIO_HUB:-docker.io/istio}
ISTIO_TAG=${ISTIO_TAG:-1.29.2}
GATEWAY_API=${GATEWAY_API:-v1.4.0}
WORK=$(mktemp -d "${TMPDIR:-/tmp}/flightbay-reach.XXXXXX")
export FLIGHTBAY_HOME=$WORK/home
export KUBECONFIG=${REACH_KUBECONFIG:-${TMPDIR:-/tmp}/flightbay-reach.kubeconfig}
export PATH="$(dirname "$ISTIOCTL"):$PATH"
BIN=$ROOT/bin
DESK_PIDS=()
PASSED=0
NOTES=()

cleanup() {
  for p in ${DESK_PIDS[@]+"${DESK_PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
  if [ "${KEEP:-0}" != "1" ]; then kind delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

pass() { PASSED=$((PASSED + 1)); printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; echo "  work dir kept at $WORK"; trap - EXIT; cleanup; exit 1; }
note() { NOTES+=("$*"); printf '  \033[33mNOTE\033[0m %s\n' "$*"; }
step() { printf '\n\033[36m== %s\033[0m\n' "$*"; }
contains() { grep -q -- "$2" <<<"$1" || fail "$3 (expected to find: $2)"; }
absent() { if grep -q -- "$2" <<<"$1"; then fail "$3 (found: $2)"; fi; }
k() { kubectl --context "$CTX" "$@"; }
# http <agent> <method> <url>: the status code, or 000 when nothing answered.
http() { k -n agents exec "deploy/$1" -c curl -- curl -s -o /dev/null -m 5 -w '%{http_code}' -X "$2" "$3" 2>/dev/null || true; }
# until_code <want> <agent> <method> <url>: poll until the code matches (90s).
until_code() {
  local got=""
  for i in $(seq 1 45); do got=$(http "$2" "$3" "$4"); [ "$got" = "$1" ] && { echo "$got"; return 0; }; sleep 2; done
  echo "$got"; return 1
}
REG=http://artifactory.tools:8081

step "Build"
(cd "$ROOT" && go build -o bin/ ./cmd/...)
pass "flightbay and flightbay-desk built"

step "Cluster: kind, NetworkPolicy, Istio ambient, Gateway API"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1
  echo "  reusing cluster $CLUSTER"
else
  kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --wait 120s >/dev/null 2>&1 || fail "kind create cluster"
fi
pass "cluster $CTX up (kubeconfig $KUBECONFIG)"
k apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" >/dev/null ||
  fail "Gateway API CRDs (needed for waypoints)"
k wait --for=condition=Established crd/gateways.gateway.networking.k8s.io --timeout=60s >/dev/null
pass "Gateway API $GATEWAY_API CRDs installed"
if ! k -n istio-system get deploy istiod >/dev/null 2>&1; then
  "$ISTIOCTL" install --context "$CTX" --set profile=ambient --set hub="$ISTIO_HUB" --set tag="$ISTIO_TAG" -y --readiness-timeout 200s >"$WORK/istio.log" 2>&1 ||
    { tail -20 "$WORK/istio.log"; fail "istioctl install"; }
fi
k -n istio-system rollout status ds/ztunnel --timeout=180s >/dev/null || fail "ztunnel not ready"
k -n istio-system rollout status deploy/istiod --timeout=120s >/dev/null || fail "istiod not ready"
pass "Istio ambient installed ($("$ISTIOCTL" version --short --remote=false 2>/dev/null | head -1) istioctl, images $ISTIO_HUB:$ISTIO_TAG)"

step "Workloads: the incident shape"
k apply -f "$ROOT/test/e2e/reach-workloads.yaml" >/dev/null
# A reused cluster (KEEP=1) still has the last run's fixes: start open again.
for ns in agents tools; do k -n "$ns" delete networkpolicy,authorizationpolicy --all >/dev/null 2>&1 || true; done
k -n tools label svc artifactory istio.io/use-waypoint- >/dev/null 2>&1 || true
k -n tools delete gateway waypoint --ignore-not-found >/dev/null 2>&1 || true
for ns in agents tools; do k -n "$ns" wait --for=condition=Available deploy --all --timeout=200s >/dev/null || fail "deployments in $ns not ready"; done
for app in eval-agent eval-agent-2; do
  red=$(k -n agents get pod -l app=$app -o jsonpath='{.items[0].metadata.annotations.ambient\.istio\.io/redirection}')
  [ "$red" = enabled ] || fail "$app is not captured by ztunnel (redirection=$red)"
done
k -n agents get pvc scratch -o jsonpath='{.status.phase}' | grep -qx Bound || fail "PVC scratch not bound"
pass "eval-agent, eval-agent-2 (sharing PVC scratch), artifactory and postgres ready and in the ambient mesh"

# kindnet enforces NetworkPolicy since kind v0.24. Prove it rather than assume:
# a pod outside the mesh loses the registry once an egress deny selects it.
k create ns np-probe --dry-run=client -o yaml | k apply -f - >/dev/null
k -n np-probe delete networkpolicy deny-all --ignore-not-found >/dev/null
k -n np-probe run probe --image=curlimages/curl:8.11.1 --restart=Never --labels=app=probe --command -- sleep infinity >/dev/null 2>&1 || true
k -n np-probe wait --for=condition=Ready pod/probe --timeout=90s >/dev/null || fail "probe pod not ready"
REGIP=$(k -n tools get svc artifactory -o jsonpath='{.spec.clusterIP}')
probe() { k -n np-probe exec probe -- curl -s -o /dev/null -m 4 -w '%{http_code}' "http://$REGIP:8081/" 2>/dev/null || true; }
b=$(probe)
k -n np-probe apply -f - >/dev/null <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: deny-all}
spec: {podSelector: {}, policyTypes: [Egress], egress: []}
EOF
a=""; for i in $(seq 1 15); do a=$(probe); [ "$a" = 000 ] && break; sleep 2; done
k delete ns np-probe --wait=false >/dev/null
NP_ENFORCED=0
if [ "$b" = 200 ] && [ "$a" = 000 ]; then NP_ENFORCED=1; pass "kindnet enforces NetworkPolicy (probe: $b before an egress deny, $a after)"
else note "NetworkPolicy does not appear to be enforced (probe: $b before, $a after): live egress checks become notes, static checks still run"; fi

step "BEFORE: live proof that the agent owns the registry and the internet"
c=$(http eval-agent PUT "$REG/x"); [ "$c" = 200 ] || fail "PUT to the registry from eval-agent: $c"
pass "eval-agent PUT $REG/x -> $c (the message board)"
c=$(http eval-agent MKCOL "$REG/board"); [ "$c" = 200 ] || fail "MKCOL to the registry from eval-agent: $c"
pass "eval-agent MKCOL $REG/board -> $c (the WebDAV rebuild)"
INTERNET=1
c=$(http eval-agent GET https://example.com)
if [[ "$c" =~ ^[23] ]]; then pass "eval-agent GET https://example.com -> $c (the internet)"
else INTERNET=0; note "eval-agent could not reach https://example.com ($c): no internet here, so the internet assertions are skipped"; fi
# A few reads for the access logs, so the capture has observed traffic too.
for i in 1 2 3; do http eval-agent GET "$REG/api/pypi/pypi/simple/" >/dev/null; done

# ---------- capture and desk helpers ----------
capture() { # $1 = name
  "$BIN/flightbay" collect --context "$CTX" --namespaces agents,tools,istio-system,kube-system -o "$WORK/$1.tar.zst" -q ||
    fail "collect $1"
  [ -f "$WORK/$1.tar.zst" ] || fail "no bundle at $WORK/$1.tar.zst"
}
open_desk() { # $1 = name; sets URL TOKEN
  FLIGHTBAY_ENTERPRISE=1 "$BIN/flightbay-desk" open "$WORK/$1.tar.zst" --no-browser --listen 127.0.0.1:0 --workdir "$WORK/ws-$1" >"$WORK/desk-$1.log" 2>&1 &
  DESK_PIDS+=($!)
  for i in $(seq 1 150); do [ -f "$WORK/ws-$1/desk.json" ] && break; sleep 0.2; done
  [ -f "$WORK/ws-$1/desk.json" ] || fail "desk for $1 did not start"
  URL=$(python3 -c "import json;print(json.load(open('$WORK/ws-$1/desk.json'))['url'])")
  TOKEN=$(python3 -c "import json;print(json.load(open('$WORK/ws-$1/desk.json'))['token'])")
  for i in $(seq 1 300); do grep -q "Import complete" "$WORK/desk-$1.log" && break; sleep 0.2; done
  contains "$(cat "$WORK/desk-$1.log")" "Import complete and verified" "the $1 bundle imports and verifies"
}
api() { curl -sf -H "Authorization: Bearer $TOKEN" "$URL$1"; }
# jv <file> <python expression over d>: evaluate against a saved agent report.
jv() { python3 -c "import json,sys;d=json.load(open(sys.argv[1]));print($2)" "$1"; }
edge() { # the Direct edge to Service $2 in report $1, as JSON
  python3 - "$1" "$2" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
e = next((e for e in d["direct"] if e["to"].endswith("/svc/" + sys.argv[2])), None)
print(json.dumps(e))
PY
}

step "BEFORE: capture and analyse"
capture before
pass "captured before.tar.zst ($(wc -c <"$WORK/before.tar.zst" | tr -d ' ') bytes)"
open_desk before
api "/api/reach?agent=agents/eval-agent" >"$WORK/before-eval-agent.json" || fail "/api/reach?agent=agents/eval-agent"
api "/api/reach?agent=agents/eval-agent-2" >"$WORK/before-eval-agent-2.json" || fail "eval-agent-2 is not reported as an agent"
B=$WORK/before-eval-agent.json
[ "$(jv "$B" "d['internet']")" = allowed ] || fail "internet should be allowed, got $(jv "$B" "d['internet']")"
[ "$(jv "$B" "d['radius']")" = open ] || fail "radius should be open, got $(jv "$B" "d['radius']")"
pass "eval-agent: internet allowed, radius open ($(jv "$B" "d['summary']"))"
e=$(edge "$B" artifactory)
[ "$(python3 -c "import json,sys;e=json.loads(sys.argv[1]);print(e and e['verdict']=='allowed' and e['write'] and e['risk']=='red')" "$e")" = True ] ||
  fail "artifactory edge should be allowed, write, red: $e"
pass "artifactory edge: allowed, write=true, risk red"
ft=$(jv "$B" "'\n'.join(f['severity']+': '+f['title'] for f in d['findings'])")
contains "$ft" "critical: Reaches the internet directly" "direct internet finding"
contains "$ft" "Can write to" "write finding"
contains "$ft" "Inherits internet access through tools/artifactory" "fetcher finding"
pass "findings: $(tr '\n' ';' <<<"$ft" | sed 's/;$//; s/;/; /g')"
sv=$(jv "$B" "len(d['identity']['sharedVolumes'])")
if [ "$sv" -gt 0 ] || grep -q "Mounts a volume other agents also mount" <<<"$ft"; then pass "shared PVC reported: $(jv "$B" "d['identity']['sharedVolumes']")"
else fail "the shared PVC scratch is not reported"; fi
fbr=$(FLIGHTBAY_URL=$URL FLIGHTBAY_TOKEN=$TOKEN "$WORK/ws-before/bin/fbreach")
contains "$fbr" "agents/eval-agent " "fbreach lists eval-agent"
contains "$fbr" "agents/eval-agent-2" "fbreach lists eval-agent-2"
pass "fbreach lists both agents"
# The list view blanks the fix text; the per-agent view must still carry it.
api "/api/reach?agent=agents/eval-agent" >"$WORK/before-eval-agent-again.json"
[ -n "$(jv "$WORK/before-eval-agent-again.json" "next(f['fix'] for f in d['findings'] if f['title']=='Reaches the internet directly')")" ] ||
  fail "the per-agent report lost its fix YAML after the list view was served"
pass "fix YAML survives the list view"

step "APPLY THE FIXES FLIGHTBAY GENERATED"
python3 - "$B" "$WORK" <<'PY' || fail "could not extract the fixes"
import json, sys
d, work = json.load(open(sys.argv[1])), sys.argv[2]
by = {f["title"]: f for f in d["findings"]}
egress = by["Reaches the internet directly"]["fix"]
write = next(f for f in d["findings"] if f["title"].startswith("Can write to"))
ro = write["fix"]
if "name: artifactory}" not in ro:  # the first write target is not the registry
    ro = next(f["fix"] for f in d["findings"] if f["title"].startswith("Inherits internet access")).split("\n---\n")[0]
open(work + "/fix-egress.yaml", "w").write(egress + "\n")
open(work + "/fix-read-only.yaml", "w").write(ro + "\n")
# The read-only fix says how to add a waypoint when the Service has none.
steps = [l.lstrip("#").strip() for l in ro.splitlines() if l.startswith("#   ")]
open(work + "/fix-steps.sh", "w").write("\n".join(steps) + "\n")
PY
for f in fix-egress.yaml fix-read-only.yaml; do printf '  --- %s\n' "$f"; sed 's/^/  | /' "$WORK/$f"; done
contains "$(cat "$WORK/fix-read-only.yaml")" "methods: \[GET, HEAD\]" "read-only fix limits methods"
contains "$(cat "$WORK/fix-egress.yaml")" "kind: NetworkPolicy" "egress fix is a NetworkPolicy"
if [ -s "$WORK/fix-steps.sh" ]; then
  printf '  --- steps from the fix comments\n'; sed 's/^/  $ /' "$WORK/fix-steps.sh"
  while IFS= read -r cmd; do
    [ -z "$cmd" ] && continue
    eval "$cmd" >"$WORK/step.log" 2>&1 || { cat "$WORK/step.log"; fail "fix step failed: $cmd"; }
  done <"$WORK/fix-steps.sh"
  pass "ran the waypoint steps from the fix comments"
fi
wp=$(k -n tools get svc artifactory -o jsonpath='{.metadata.labels.istio\.io/use-waypoint}')
[ -n "$wp" ] || fail "artifactory has no istio.io/use-waypoint label after the fix steps"
for i in $(seq 1 60); do
  k -n tools get deploy "$wp" >/dev/null 2>&1 && break; sleep 2
done
k -n tools rollout status "deploy/$wp" --timeout=150s >/dev/null || fail "waypoint $wp not ready"
pass "waypoint tools/$wp running; artifactory uses it"
k apply -f "$WORK/fix-read-only.yaml" >/dev/null || fail "kubectl apply the read-only AuthorizationPolicy"
k apply -f "$WORK/fix-egress.yaml" >/dev/null || fail "kubectl apply the egress NetworkPolicy"
pass "applied fix-read-only.yaml and fix-egress.yaml exactly as generated"
# The registry itself must lose the internet too, or its SSRF still lends it.
k apply -f - >/dev/null <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: artifactory-egress, namespace: tools}
spec:
  podSelector: {matchLabels: {app: artifactory}}
  policyTypes: [Egress]
  egress: []
EOF
pass "applied a default egress deny for the registry's own pods"

step "AFTER: live proof that the traffic is blocked"
c=$(until_code 403 eval-agent PUT "$REG/x") || fail "PUT from eval-agent should be denied by the waypoint, got $c"
pass "eval-agent PUT $REG/x -> $c"
c=$(until_code 200 eval-agent GET "$REG/api/pypi/pypi/simple/") ||
  fail "GET from eval-agent should still work, got $c (does the egress fix allow the waypoint?)"
pass "eval-agent GET $REG/api/pypi/pypi/simple/ -> $c"
c=$(http eval-agent MKCOL "$REG/board"); [ "$c" = 403 ] || fail "MKCOL from eval-agent should be denied, got $c"
pass "eval-agent MKCOL $REG/board -> $c"
c=$(until_code 403 eval-agent-2 GET "$REG/") || fail "eval-agent-2 is not in the ALLOW rule, so GET should be 403, got $c"
pass "eval-agent-2 GET $REG/ -> $c (not in the ALLOW rule)"
if [ "$INTERNET" = 1 ] && [ "$NP_ENFORCED" = 1 ]; then
  c=$(until_code 000 eval-agent GET https://example.com) || fail "eval-agent still reaches the internet: $c"
  pass "eval-agent GET https://example.com -> $c (no answer)"
else note "internet egress after the fix not checked live (internet=$INTERNET, NetworkPolicy enforced=$NP_ENFORCED)"; fi
for i in 1 2 3; do http eval-agent GET "$REG/api/pypi/pypi/simple/" >/dev/null; done

step "AFTER: capture and analyse"
capture after
open_desk after
A=$WORK/after-eval-agent.json
api "/api/reach?agent=agents/eval-agent" >"$A" || fail "/api/reach after"
api "/api/reach?agent=agents/eval-agent-2" >"$WORK/after-eval-agent-2.json" || fail "/api/reach eval-agent-2 after"
[ "$(jv "$A" "d['internet']")" = denied ] || fail "internet should be denied, got $(jv "$A" "d['internet']")"
pass "eval-agent: internet denied, radius $(jv "$A" "d['radius']") ($(jv "$A" "d['summary']"))"
crit=$(jv "$A" "'; '.join(f['title'] for f in d['findings'] if f['severity']=='critical')")
[ -z "$crit" ] || fail "no critical findings expected, got: $crit"
pass "no critical findings (remaining: $(jv "$A" "'; '.join(f['severity']+': '+f['title'] for f in d['findings'])"))"
e=$(edge "$A" artifactory)
[ "$(python3 -c "import json,sys;e=json.loads(sys.argv[1]);print(e and e['verdict']=='allowed' and e['methods']==['GET','HEAD'] and not e['write'] and e['risk']=='green')" "$e")" = True ] ||
  fail "artifactory edge should be allowed, GET/HEAD only, read-only, green: $e"
pass "artifactory edge: methods [GET, HEAD], write=false, risk green"
[ "$(jv "$A" "[p['hops'] for p in d['paths'] if p['target']=='internet']")" = "[]" ] || fail "internet paths remain: $(jv "$A" "d['paths']")"
pass "no path from eval-agent to the internet, direct or through the registry"
e2=$(edge "$WORK/after-eval-agent-2.json" artifactory)
[ "$e2" = null ] || [ "$(python3 -c "import json,sys;print(json.loads(sys.argv[1])['verdict'])" "$e2")" != allowed ] ||
  fail "eval-agent-2 should not reach the registry: $e2"
pass "eval-agent-2: the registry is no longer reachable (not in the ALLOW rule)"
obs=$(python3 -c "import json,sys;e=json.loads(sys.argv[1]);print(e['observed'], e.get('observedMethods') or [])" "$e")
if [ "${obs%% *}" -gt 0 ]; then pass "observed traffic on the artifactory edge from the access logs: $obs"
else note "no observed traffic on the artifactory edge (ztunnel access logs: $(jv "$A" "d['observed']"))"; fi

step "Summary"
for n in ${NOTES[@]+"${NOTES[@]}"}; do echo "  note: $n"; done
printf '\n\033[32mAll %d reach checks passed.\033[0m\n' "$PASSED"
