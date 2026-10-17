#!/usr/bin/env bash
# Create a matching test instance, never copy source publication labels/secrets.
set -euo pipefail
SOURCE=${SOURCE:-kind-mesh1}
TARGET=${TARGET:-kind-mesh2}
NS=petstore-prod
case "${1:-status}" in
  up)
    [[ "$SOURCE" != "$TARGET" ]] || { printf 'Source and target must be different contexts.\n'; exit 1; }
    image=$(kubectl --context "$SOURCE" -n "$NS" get deployment petstore -o 'jsonpath={.spec.template.spec.containers[0].image}')
    if [[ "$TARGET" == kind-* && "$image" == localhost:5001/* ]]; then
      kind load docker-image "$image" --name "${TARGET#kind-}"
    fi
    existing=$(kubectl --context "$TARGET" get namespace "$NS" --ignore-not-found -o 'jsonpath={.metadata.labels.app\.kubernetes\.io/managed-by}')
    if kubectl --context "$TARGET" get namespace "$NS" >/dev/null 2>&1; then
      [[ "$existing" == flightbay ]] || { printf 'Existing namespace is not the Flightbay fixture; refusing to overwrite it.\n'; exit 1; }
    else
      kubectl --context "$TARGET" create namespace "$NS" --dry-run=client -o json |
        jq '.metadata.labels={"app.kubernetes.io/managed-by":"flightbay","istio.io/dataplane-mode":"ambient","topology.istio.io/network":"mesh2"}' |
        kubectl --context "$TARGET" apply -f -
    fi
    for resource in service/petstore deployment/petstore; do
      owner=$(kubectl --context "$TARGET" -n "$NS" get "$resource" --ignore-not-found -o 'jsonpath={.metadata.labels.app\.kubernetes\.io/managed-by}')
      if kubectl --context "$TARGET" -n "$NS" get "$resource" >/dev/null 2>&1; then
        [[ "$owner" == flightbay ]] || { printf 'Existing %s is not owned by this fixture; refusing to overwrite it.\n' "$resource"; exit 1; }
      fi
    done
    kubectl --context "$SOURCE" -n "$NS" get deployment petstore -o json |
      jq -e 'if any(.spec.template.spec.containers[]; ((.env//[])|length)>0 or ((.envFrom//[])|length)>0 or ((.volumeMounts//[])|length)>0) then error("Source has environment/volume dependencies; review those explicitly") else {apiVersion:"apps/v1",kind:"Deployment",metadata:{name:"petstore",namespace:"petstore-prod",labels:{"app.kubernetes.io/managed-by":"flightbay"}},spec:{replicas:1,selector:.spec.selector,template:{metadata:{labels:.spec.template.metadata.labels},spec:{containers:[.spec.template.spec.containers[]|{name,image,command,args,ports,resources,readinessProbe,livenessProbe}]}}}} end' |
      kubectl --context "$TARGET" apply -f -
    kubectl --context "$SOURCE" -n "$NS" get service petstore -o json |
      jq '{apiVersion:"v1",kind:"Service",metadata:{name:"petstore",namespace:"petstore-prod",labels:{"app.kubernetes.io/managed-by":"flightbay"}},spec:{ports:.spec.ports,selector:.spec.selector}}' |
      kubectl --context "$TARGET" apply -f -
    kubectl --context "$TARGET" -n "$NS" rollout status deployment/petstore --timeout=180s
    ;;
  remove-app)
    for resource in service/petstore deployment/petstore; do
      owner=$(kubectl --context "$TARGET" -n "$NS" get "$resource" -o 'jsonpath={.metadata.labels.app\.kubernetes\.io/managed-by}')
      [[ "$owner" == flightbay ]] || { printf 'Refusing to delete a resource not owned by this fixture.\n'; exit 1; }
    done
    kubectl --context "$TARGET" -n "$NS" delete service/petstore deployment/petstore
    ;;
  status) kubectl --context "$TARGET" -n "$NS" get deployment/petstore service/petstore ;;
  *) printf 'Usage: bash test/e2e/petstore-mesh2.sh up|status|remove-app\n'; exit 1 ;;
esac
