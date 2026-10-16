package ambient

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func put(ws *store.Workspace, g, res, kind string, objs ...map[string]any) {
	r := bundle.APIResource{Group: g, Version: "v1", Resource: res, Kind: kind, Namespaced: res != "namespaces" && res != "nodes"}
	ws.AddObjects("c", r, objs, "fixture-"+res)
	ws.SetCoverage(bundle.Coverage{ID: res, Cluster: "c", Type: bundle.CoverageResource, Group: g, Version: "v1", Resource: res, State: bundle.StateComplete})
}
func object(kind, name string, spec map[string]any) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": "app", "uid": "uid-" + name, "generation": 1}, "spec": spec}
}
func fixture(t *testing.T) *store.Workspace {
	t.Helper()
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}})
	n := object("Namespace", "app", nil)
	M(n["metadata"])["namespace"] = ""
	M(n["metadata"])["labels"] = map[string]any{"istio.io/dataplane-mode": "ambient", "istio.io/use-waypoint": "wp"}
	put(ws, "", "namespaces", "Namespace", n)
	services := []map[string]any{}
	for _, name := range []string{"front", "stable", "canary"} {
		services = append(services, object("Service", name, map[string]any{"selector": map[string]any{"app": name}, "ports": []any{map[string]any{"port": 80, "targetPort": 8080}}}))
	}
	put(ws, "", "services", "Service", services...)
	wp := object("Gateway", "wp", map[string]any{"gatewayClassName": "istio-waypoint", "listeners": []any{map[string]any{"name": "mesh", "port": 15008, "protocol": "HBONE"}}})
	wp["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": 1}, map[string]any{"type": "Programmed", "status": "True", "observedGeneration": 1}}}
	put(ws, GatewayGroup, "gateways", "Gateway", wp)
	pod := object("Pod", "front-1", map[string]any{})
	M(pod["metadata"])["labels"] = map[string]any{"app": "front"}
	pod["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	put(ws, "", "pods", "Pod", pod)
	ep := object("EndpointSlice", "front-1", nil)
	M(ep["metadata"])["labels"] = map[string]any{"kubernetes.io/service-name": "front"}
	ep["endpoints"] = []any{map[string]any{"conditions": map[string]any{"ready": true}}}
	put(ws, "discovery.k8s.io", "endpointslices", "EndpointSlice", ep)
	for _, r := range []struct{ g, r, k string }{{GatewayGroup, "httproutes", "HTTPRoute"}, {GatewayGroup, "referencegrants", "ReferenceGrant"}, {"security.istio.io", "authorizationpolicies", "AuthorizationPolicy"}, {"security.istio.io", "peerauthentications", "PeerAuthentication"}, {"networking.istio.io", "destinationrules", "DestinationRule"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy"}} {
		put(ws, r.g, r.r, r.k)
	}
	return ws
}
func addRoute(ws *store.Workspace, p map[string]any, st []any) {
	r := object("HTTPRoute", "r", map[string]any{"parentRefs": []any{p}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "stable", "port": 80}}}}})
	r["status"] = map[string]any{"parents": st}
	put(ws, GatewayGroup, "httproutes", "HTTPRoute", r)
}
func findStep(t *testing.T, r *Investigation, id string) Step {
	t.Helper()
	for _, s := range r.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %s missing", id)
	return Step{}
}

func TestWaypointGatewayParentIsExplainedNotUniversallyRejected(t *testing.T) {
	ws := fixture(t)
	p := map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": "wp"}
	addRoute(ws, p, []any{map[string]any{"parentRef": p, "controllerName": "istio.io/gateway-controller", "conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": 1}, map[string]any{"type": "ResolvedRefs", "status": "True", "observedGeneration": 1}}}})
	r, e := Investigate(ws, "c", "app", "front", "r")
	if e != nil {
		t.Fatal(e)
	}
	s := findStep(t, r, "route-app-r-0")
	if s.State != "warning" || !strings.Contains(s.Detail, "waypoint-wide") || !strings.Contains(s.FixYAML, "kind: Service") {
		t.Fatalf("%+v", s)
	}
	if r.Counts["fail"] != 0 {
		t.Fatalf("a valid Gateway parent was rejected: %+v", r.Steps)
	}
}
func TestRouteRejectionAndStaleStatus(t *testing.T) {
	ws := fixture(t)
	p := map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": "wp"}
	addRoute(ws, p, []any{map[string]any{"parentRef": p, "controllerName": "example/controller", "conditions": []any{map[string]any{"type": "Accepted", "status": "False", "observedGeneration": 1, "reason": "NotAllowedByListeners", "message": "HTTPRoute not accepted by HBONE listener"}}}})
	r, _ := Investigate(ws, "c", "app", "front", "r")
	s := findStep(t, r, "route-app-r-0-status-0-Accepted")
	if s.State != "fail" || !strings.Contains(s.Detail, "NotAllowedByListeners") {
		t.Fatalf("%+v", s)
	}
	o := ws.Get("c", GatewayGroup, "httproutes", "app", "r")
	M(o.Obj["metadata"])["generation"] = 2
	r, _ = Investigate(ws, "c", "app", "front", "r")
	if findStep(t, r, "route-app-r-0-status-0-Accepted").State != "unknown" {
		t.Fatal("stale rejection treated as current")
	}
}
func TestRemovedGatewayCollectionDoesNotProveWaypointMissing(t *testing.T) {
	ws := fixture(t)
	svc := ws.Get("c", "", "services", "app", "front")
	M(svc.Obj["metadata"])["labels"] = map[string]any{"istio.io/use-waypoint": "absent"}
	ws.SetCoverage(bundle.Coverage{ID: "gateways", Cluster: "c", Type: bundle.CoverageResource, Group: GatewayGroup, Version: "v1", Resource: "gateways", State: bundle.StateRemoved})
	r, _ := Investigate(ws, "c", "app", "front", "")
	if findStep(t, r, "waypoint").State != "unknown" {
		t.Fatal("removed evidence treated as missing waypoint")
	}
}
func TestWrongTrafficTypeBackendPortAndL7Selector(t *testing.T) {
	ws := fixture(t)
	M(ws.Get("c", GatewayGroup, "gateways", "app", "wp").Obj["metadata"])["labels"] = map[string]any{"istio.io/waypoint-for": "workload"}
	p := map[string]any{"group": "", "kind": "Service", "name": "front", "port": 8080}
	addRoute(ws, p, nil)
	ap := object("AuthorizationPolicy", "bad", map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}}, "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET"}}}}}}})
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", ap)
	r, _ := Investigate(ws, "c", "app", "front", "r")
	for _, id := range []string{"traffic-type", "route-app-r-0", "policy-bad"} {
		if findStep(t, r, id).State != "fail" {
			t.Fatalf("%s not failed", id)
		}
	}
}
func TestWeightedRoutingAndRateLimitAreCapabilityAware(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"weighted-routing"}, Backends: []Backend{{"stable", 80, 90}, {"canary", 80, 10}}}
	p, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	pr := M(L(Path(p.Objects[0], "spec", "parentRefs"))[0])
	if S(pr["kind"]) != "Service" || S(pr["group"]) != "" {
		t.Fatal(p.YAML)
	}
	req.Backends[0].Weight = 80
	if _, e = Generate(ws, req); e == nil {
		t.Fatal("weights not validated")
	}
	req.Tasks = []string{"rate-limit"}
	req.Requests = 10
	req.Unit = "Seconds"
	if _, e = Generate(ws, req); e == nil {
		t.Fatal("stock Istio falsely supports built-in rate limiting")
	}
	M(ws.Get("c", GatewayGroup, "gateways", "app", "wp").Obj["spec"])["gatewayClassName"] = "enterprise-agentgateway-waypoint"
	p, e = Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if S(p.Objects[0]["kind"]) != "EnterpriseAgentgatewayPolicy" || !strings.Contains(p.YAML, "requests: 10") {
		t.Fatal(p.YAML)
	}
}
func TestFailoverIsNotPretendedWithZeroWeights(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"service-failover"}}
	if _, e := Generate(ws, req); e == nil {
		t.Fatal("unsupported primary/backup failover generated")
	}
	req.Tasks = []string{"endpoint-failover", "load-balancing"}
	req.Algorithm = "LEAST_REQUEST"
	p, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Objects) != 1 || !strings.Contains(p.YAML, "outlierDetection:") || !strings.Contains(p.YAML, "LEAST_REQUEST") {
		t.Fatal(p.YAML)
	}
}

func TestServiceParentRequiresExplicitCoreGroup(t *testing.T) {
	ws := fixture(t)
	addRoute(ws, map[string]any{"kind": "Service", "name": "front"}, nil)
	r, e := Investigate(ws, "c", "app", "front", "r")
	if e != nil {
		t.Fatal(e)
	}
	s := findStep(t, r, "route-app-r-0")
	if s.State != "fail" || !strings.Contains(s.Detail, "Omitted group defaults") {
		t.Fatalf("%+v", s)
	}
}
func TestCrossNamespaceBackendGrantAndMissingEvidence(t *testing.T) {
	ws := fixture(t)
	addRoute(ws, map[string]any{"group": "", "kind": "Service", "name": "front"}, nil)
	backend := object("Service", "other", map[string]any{"ports": []any{map[string]any{"port": 80}}})
	M(backend["metadata"])["namespace"] = "other-ns"
	put(ws, "", "services", "Service", backend)
	rte := ws.Get("c", GatewayGroup, "httproutes", "app", "r")
	M(L(Path(rte.Obj, "spec", "rules"))[0])["backendRefs"] = []any{map[string]any{"name": "other", "namespace": "other-ns", "port": 80}}
	r, _ := Investigate(ws, "c", "app", "front", "r")
	if findStep(t, r, "backend-r-other-ns-other-grant").State != "fail" {
		t.Fatal("missing grant was not identified")
	}
	ws.SetCoverage(bundle.Coverage{ID: "referencegrants", Cluster: "c", Type: bundle.CoverageResource, Group: GatewayGroup, Version: "v1", Resource: "referencegrants", State: bundle.StateRemoved})
	r, _ = Investigate(ws, "c", "app", "front", "r")
	if findStep(t, r, "backend-r-other-ns-other-grant").State != "unknown" {
		t.Fatal("removed grant collection treated as a missing permission")
	}
}
func TestPreviewIDChangesWithServiceSelector(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"l4-default-deny"}}
	a, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	M(Path(ws.Get("c", "", "services", "app", "front").Obj, "spec", "selector"))["app"] = "another-workload"
	b, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if a.ID == b.ID {
		t.Fatal("preview ID did not bind the workload selector")
	}
}
func TestPolicyAttachmentAndStringObservedGeneration(t *testing.T) {
	ws := fixture(t)
	p := object("AuthorizationPolicy", "policy", map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}}})
	p["status"] = map[string]any{"conditions": []any{map[string]any{"type": "WaypointAccepted", "status": "False", "observedGeneration": "1", "reason": "InvalidTarget", "message": "not attached"}}}
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", p)
	r, _ := Investigate(ws, "c", "app", "front", "")
	s := findStep(t, r, "policy-status-policy-WaypointAccepted")
	if s.State != "fail" || !strings.Contains(s.Detail, "InvalidTarget") {
		t.Fatalf("%+v", s)
	}
}
func TestIngressGatewayIsNotAnAmbientWaypoint(t *testing.T) {
	ws := fixture(t)
	gw := ws.Get("c", GatewayGroup, "gateways", "app", "wp")
	M(gw.Obj["spec"])["gatewayClassName"] = "istio"
	M(gw.Obj["spec"])["listeners"] = []any{map[string]any{"name": "http", "port": 80, "protocol": "HTTP"}}
	r, _ := Investigate(ws, "c", "app", "front", "")
	if findStep(t, r, "waypoint").State != "fail" || findStep(t, r, "waypoint-listener").State != "fail" {
		t.Fatal("ingress Gateway passed as a waypoint")
	}
}
