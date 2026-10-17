#!/usr/bin/env bash
# Verify remote global-Service responses without disrupting application namespaces.
set -euo pipefail
CTX1="${CTX1:-kind-mesh1}"
CTX2="${CTX2:-kind-mesh2}"
HOST="${HOST:-global-echo.flightbay-global-demo.mesh.internal}"
NS=flightbay-global-demo
for ctx in "$CTX1" "$CTX2"; do
  owner=$(kubectl --context "$ctx" get namespace "$NS" -o 'jsonpath={.metadata.labels.app\.kubernetes\.io/managed-by}')
  [[ "$owner" == flightbay ]] || { printf 'Refusing changes outside the Flightbay fixture on %s\n' "$ctx"; exit 1; }
done
replicas1=$(kubectl --context "$CTX1" -n "$NS" get deployment global-echo -o 'jsonpath={.spec.replicas}')
replicas2=$(kubectl --context "$CTX2" -n "$NS" get deployment global-echo -o 'jsonpath={.spec.replicas}')
[[ "$replicas1" -gt 0 && "$replicas2" -gt 0 ]] || { printf 'Both fixture servers must start with positive replica counts\n'; exit 1; }
restore() {
  kubectl --context "$CTX1" -n "$NS" scale deployment/global-echo --replicas="$replicas1"
  kubectl --context "$CTX2" -n "$NS" scale deployment/global-echo --replicas="$replicas2"
  kubectl --context "$CTX1" -n "$NS" rollout status deployment/global-echo --timeout=120s
  kubectl --context "$CTX2" -n "$NS" rollout status deployment/global-echo --timeout=120s
}
trap restore EXIT
request() {
  local origin="$1" expected="$2" response
  for _ in {1..30}; do
    response=$(kubectl --context "$origin" -n "$NS" exec deployment/client -- curl -fsS --max-time 5 "http://$HOST:8080/" 2>/dev/null || true)
    if [[ "$response" == "$expected/"* ]]; then
      printf 'PASS %s -> %s: %s\n' "$origin" "$HOST" "$response"
      return 0
    fi
    sleep 2
  done
  printf 'FAIL %s expected %s, last response: %s\n' "$origin" "$expected" "$response"
  return 1
}
kubectl --context "$CTX1" -n "$NS" scale deployment/global-echo --replicas=0
kubectl --context "$CTX1" -n "$NS" wait --for=delete pod -l app=global-echo --timeout=120s
request "$CTX1" "${EXPECTED2:-mesh2}"
kubectl --context "$CTX1" -n "$NS" scale deployment/global-echo --replicas="$replicas1"
kubectl --context "$CTX1" -n "$NS" rollout status deployment/global-echo --timeout=120s
kubectl --context "$CTX2" -n "$NS" scale deployment/global-echo --replicas=0
kubectl --context "$CTX2" -n "$NS" wait --for=delete pod -l app=global-echo --timeout=120s
request "$CTX2" "${EXPECTED1:-mesh1}"
restore
trap - EXIT
printf 'Both remote directions passed; original replica counts restored.\n'
