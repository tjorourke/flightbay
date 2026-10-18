#!/usr/bin/env bash
# Intentional local demo faults. Run in your own terminal, not the read-only desk shell.
set -euo pipefail
CONTEXT=kind-mesh1
BOOKINFO_NAMESPACE=${BOOKINFO_NAMESPACE:-bookinfo}
BOOKINFO_WAYPOINT=bookinfo-waypoint
BOOKINFO_WAYPOINT_NAMESPACE=$BOOKINFO_NAMESPACE
usage() {
  printf '%s\n' \
    'Usage: mesh1-demo-faults.sh {on|off|status} [bookinfo-route|petstore-route|petstore-global|all]' \
    '' \
    'on  = enable the selected failure; off = repair it to the documented demo state.' \
    'on/off default to bookinfo-route. status defaults to all.' \
    'bookinfo-route: reviews-mesh-split uses a nonexistent waypoint listener.' \
    'petstore-route: petstore-prod HTTPRoute points at missing-gateway.' \
    'petstore-global: remove the global publication label from mesh1 Petstore.' \
    '' \
    'Only context kind-mesh1 on a loopback API server with mesh1-* nodes is accepted.' \
    'Route rules, backend weights and other parent entries are preserved.' \
    'Set BOOKINFO_NAMESPACE for a custom demo namespace; the reviews waypoint is detected.' \
    'Off uses known demo parents: reviews:9080 and agentgateway-system/ar-ingress.' \
    'It does not restore arbitrary previous custom parents.'
}
action=${1:-help}
case "$action" in help|-h|--help) usage; exit 0;; on|off|status) ;; *) usage >&2; exit 2;; esac
[[ $# -le 2 ]] || { usage >&2; exit 2; }
fault=${2:-bookinfo-route}
[[ "$action" != status || $# -ge 2 ]] || fault=all
case "$fault" in all) faults=(bookinfo-route petstore-route petstore-global);; bookinfo-route|petstore-route|petstore-global) faults=("$fault");; *) usage >&2; exit 2;; esac
for tool in kubectl jq; do command -v "$tool" >/dev/null || { printf 'Missing dependency: %s\n' "$tool" >&2; exit 1; }; done
kc() { kubectl --context "$CONTEXT" "$@"; }
server=$(kubectl config view --context "$CONTEXT" --minify -o jsonpath='{.clusters[0].cluster.server}')
local_server='^https://(127\.0\.0\.1|localhost|\[::1\]):[0-9]+/?$'
[[ "$server" =~ $local_server ]] || { printf 'Refusing non-local API server for %s: %s\n' "$CONTEXT" "$server" >&2; exit 1; }
kc get nodes -o json | jq -e '.items | length > 0 and all(.[]; .metadata.name | startswith("mesh1-"))' >/dev/null || {
  printf 'Refusing cluster: expected local mesh1-* nodes. Check your kubeconfig.\n' >&2; exit 1;
}

route_args() {
  case "$1" in bookinfo-route) namespace=$BOOKINFO_NAMESPACE; route=reviews-mesh-split;; petstore-route) namespace=petstore-prod; route=petstore-prod;; esac
}
preflight() {
  case "$1" in
    bookinfo-route)
      kc get httproute reviews-mesh-split -n "$BOOKINFO_NAMESPACE" -o json | jq -e '.spec.parentRefs | length > 0' >/dev/null
      service=$(kc get svc reviews -n "$BOOKINFO_NAMESPACE" -o json)
      jq -e 'any(.spec.ports[]; .port == 9080)' <<<"$service" >/dev/null
      if [[ "$action" == on ]]; then
        BOOKINFO_WAYPOINT=$(jq -r '.metadata.labels["istio.io/use-waypoint"] // empty' <<<"$service")
        BOOKINFO_WAYPOINT_NAMESPACE=$(jq -r '.metadata.labels["istio.io/use-waypoint-namespace"] // empty' <<<"$service")
        if [[ -z "$BOOKINFO_WAYPOINT" ]]; then
          ns=$(kc get namespace "$BOOKINFO_NAMESPACE" -o json)
          BOOKINFO_WAYPOINT=$(jq -r '.metadata.labels["istio.io/use-waypoint"] // empty' <<<"$ns")
          BOOKINFO_WAYPOINT_NAMESPACE=$(jq -r '.metadata.labels["istio.io/use-waypoint-namespace"] // empty' <<<"$ns")
        fi
        BOOKINFO_WAYPOINT_NAMESPACE=${BOOKINFO_WAYPOINT_NAMESPACE:-$BOOKINFO_NAMESPACE}
        [[ -n "$BOOKINFO_WAYPOINT" && "$BOOKINFO_WAYPOINT" != none ]] || { printf 'Reviews has no selected waypoint; cannot demonstrate a listener fault.\n' >&2; exit 1; }
        kc get gateway "$BOOKINFO_WAYPOINT" -n "$BOOKINFO_WAYPOINT_NAMESPACE" >/dev/null
      fi
      ;;
    petstore-route)
      kc get httproute petstore-prod -n petstore-prod -o json | jq -e '.spec.parentRefs | length > 0' >/dev/null
      kc get gateway ar-ingress -n agentgateway-system >/dev/null
      ;;
    petstore-global) kc get svc petstore -n petstore-prod >/dev/null;;
  esac
}
status() {
  case "$1" in
    petstore-global)
      kc get svc petstore -n petstore-prod -o json | jq -r '"petstore-global: " + (if .metadata.labels["solo.io/service-scope"] == "global" then "OFF (healthy: global)" else "ON (not published globally)" end)'
      ;;
    *)
      route_args "$1"
      kc get httproute "$route" -n "$namespace" -o json | jq -r --arg fault "$1" '
        .spec.parentRefs[0] as $p | .metadata.generation as $g |
        ($p.kind // "Gateway") as $kind |
        (if $fault == "bookinfo-route" then $kind == "Service" and $p.name == "reviews" and $p.port == 9080
         else $kind == "Gateway" and $p.name == "ar-ingress" and $p.namespace == "agentgateway-system" end) as $healthy |
        $fault + ": " + (if $healthy then "OFF (healthy parent)" else "ON / custom parent" end) +
        " · " + $kind + " " + ($p.namespace // .metadata.namespace) + "/" + $p.name +
        (if $p.sectionName then " listener=" + $p.sectionName else "" end) +
        " · current accepted=" + ([.status.parents[]?.conditions[]? | select(.type == "Accepted" and .observedGeneration == $g) | .status] | join(","))'
      ;;
  esac
}
patch_route() {
  route_args "$1"
  if [[ "$1" == bookinfo-route ]]; then
    if [[ "$action" == on ]]; then parent=$(jq -nc --arg name "$BOOKINFO_WAYPOINT" --arg namespace "$BOOKINFO_WAYPOINT_NAMESPACE" '{group:"gateway.networking.k8s.io",kind:"Gateway",name:$name,namespace:$namespace,sectionName:"wrong-listener"}')
    else parent='{"group":"","kind":"Service","name":"reviews","port":9080}'; fi
  else
    if [[ "$action" == on ]]; then parent='{"group":"gateway.networking.k8s.io","kind":"Gateway","name":"missing-gateway","namespace":"agentgateway-system"}'
    else parent='{"group":"gateway.networking.k8s.io","kind":"Gateway","name":"ar-ingress","namespace":"agentgateway-system"}'; fi
  fi
  current=$(kc get httproute "$route" -n "$namespace" -o json)
  version=$(jq -r '.metadata.resourceVersion' <<<"$current")
  patch=$(jq -nc --arg rv "$version" --argjson parent "$parent" '[{op:"test",path:"/metadata/resourceVersion",value:$rv},{op:"replace",path:"/spec/parentRefs/0",value:$parent}]')
  kc patch httproute "$route" -n "$namespace" --type=json -p "$patch"
  if [[ "$action" == on ]]; then
    kc annotate httproute "$route" -n "$namespace" flightbay.dev/demo-fault="Intentional $1 failure enabled by mesh1-demo-faults.sh" --overwrite
  else
    kc annotate httproute "$route" -n "$namespace" flightbay.dev/demo-fault- >/dev/null
    name=$(jq -r '.name' <<<"$parent"); kind=$(jq -r '.kind' <<<"$parent")
    ready=false
    for ((i=0;i<20;i++)); do
      if kc get httproute "$route" -n "$namespace" -o json | jq -e --arg name "$name" --arg kind "$kind" '
        .metadata.generation as $g | any(.status.parents[]?;
          .parentRef.name == $name and (.parentRef.kind // "Gateway") == $kind and
          ([.conditions[]? | select((.type == "Accepted" or .type == "ResolvedRefs") and .status == "True" and .observedGeneration == $g) | .type] | unique | length == 2))' >/dev/null; then ready=true; break; fi
      sleep 1
    done
    [[ "$ready" == true ]] || { printf 'Parent repaired, but current Accepted/ResolvedRefs did not become True. Inspect %s/%s.\n' "$namespace" "$route" >&2; exit 1; }
  fi
}
for selected in "${faults[@]}"; do preflight "$selected"; done
if [[ "$action" != status ]]; then
  for selected in "${faults[@]}"; do
    if [[ "$selected" == petstore-global ]]; then
      if [[ "$action" == on ]]; then kc label svc petstore -n petstore-prod solo.io/service-scope-
      else kc label svc petstore -n petstore-prod solo.io/service-scope=global --overwrite; fi
    else patch_route "$selected"; fi
  done
fi
for selected in "${faults[@]}"; do status "$selected"; done
printf '\nRe-run Trace request path or Refresh in Flightbay to read the updated evidence.\n'
