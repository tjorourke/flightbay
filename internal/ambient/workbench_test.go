package ambient

import (
	"strings"
	"testing"
)

func TestObjectEntryPointsAndAttachmentMap(t *testing.T) {
	ws := fixture(t)
	p := map[string]any{"group": "", "kind": "Service", "name": "front", "port": 80}
	addRoute(ws, p, nil)
	route := Target(ws, "c", "HTTPRoute", "app", "r")
	M(L(Path(route.Obj, "spec", "rules"))[0])["backendRefs"] = []any{map[string]any{"name": "stable", "port": 80, "weight": 90}, map[string]any{"name": "canary", "port": 80, "weight": 10}}
	inv := List(ws)
	kinds := map[string]bool{}
	for _, o := range inv.Objects {
		kinds[o.Kind] = true
	}
	for _, k := range []string{"Namespace", "Service", "Pod", "Gateway", "HTTPRoute"} {
		if !kinds[k] {
			t.Fatal("missing picker kind " + k)
		}
	}
	v, e := NewView(ws, "c", "Service", "app", "front", "")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, g := range v.Graph.Groups {
		for _, n := range g.Nodes {
			if n.Ref.Name == "stable" && strings.Contains(strings.Join(n.Notes, " "), "90.0%") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("graph did not show actual backend weight")
	}
	if len(v.Editable) != 1 || !v.Editable[0].Supported || v.Editable[0].Request.Backends[0].Weight != 90 {
		t.Fatalf("edit defaults: %+v", v.Editable)
	}
	for _, k := range []string{"Namespace", "Pod", "Gateway", "HTTPRoute"} {
		ns, name := "app", "app"
		switch k {
		case "Namespace":
			ns = ""
		case "Pod":
			name = "front-1"
		case "Gateway":
			name = "wp"
		case "HTTPRoute":
			name = "r"
		}
		v, e := NewView(ws, "c", k, ns, name, "")
		if e != nil {
			t.Fatal(e)
		}
		if v.Subject.Kind != k {
			t.Fatal(v.Subject)
		}
	}
}
func TestEqualWeightsAndExplicitOptions(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"weighted-routing"}, TrafficMode: "equal", Backends: []Backend{{"stable", 80, 0}, {"canary", 80, 0}}}
	p, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range L(M(L(Path(p.Objects[0], "spec", "rules"))[0])["backendRefs"]) {
		if _, ok := M(b)["weight"]; ok {
			t.Fatal("equal mode should omit weight")
		}
	}
	req.Tasks = []string{"endpoint-failover", "load-balancing"}
	req.Algorithm = "LEAST_REQUEST"
	req.FailureThreshold = 7
	req.EjectionInterval = "8s"
	req.EjectionDuration = "45s"
	req.MaxEjectionPercent = 60
	p, e = Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if N(Path(p.Objects[0], "spec", "trafficPolicy", "outlierDetection", "consecutive5xxErrors")) != 7 || !strings.Contains(p.YAML, "45s") {
		t.Fatal(p.YAML)
	}
	req.Tasks = []string{"load-balancing"}
	req.Algorithm = "DEFAULT"
	p, e = Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Objects) != 0 || !strings.Contains(p.YAML, "No configuration change") {
		t.Fatal(p.YAML)
	}
}
func TestExistingRouteEditPreservesOtherConfiguration(t *testing.T) {
	ws := fixture(t)
	addRoute(ws, map[string]any{"group": "", "kind": "Service", "name": "front", "port": 80}, nil)
	r := Target(ws, "c", "HTTPRoute", "app", "r")
	rules := L(Path(r.Obj, "spec", "rules"))
	M(rules[0])["matches"] = []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/api"}}}
	M(rules[0])["filters"] = []any{map[string]any{"type": "RequestHeaderModifier", "requestHeaderModifier": map[string]any{"add": []any{map[string]any{"name": "x-demo", "value": "preserve"}}}}}
	M(r.Obj["spec"])["rules"] = append(rules, map[string]any{"backendRefs": []any{map[string]any{"name": "canary", "port": 80}}})
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"weighted-routing"}, Backends: []Backend{{"stable", 80, 75}, {"canary", 80, 25}}, Edit: &EditTarget{Ref: reference(r), Rule: 0}}
	p, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Objects) != 0 || len(p.Changes) != 1 {
		t.Fatalf("must edit the original rather than create a competing route: %+v", p)
	}
	after := L(p.Changes[0].Fields[0].After)
	if len(after) != 2 || !Equal(M(after[0])["matches"], M(rules[0])["matches"]) || !Equal(M(after[0])["filters"], M(rules[0])["filters"]) {
		t.Fatal(p.YAML)
	}
	if N(M(L(M(after[0])["backendRefs"])[1])["weight"]) != 25 {
		t.Fatal(p.YAML)
	}
}
func TestSharedWaypointIsExplicitAndScoped(t *testing.T) {
	ws := fixture(t)
	for _, name := range []string{"infra", "other"} {
		n := object("Namespace", name, nil)
		M(n["metadata"])["namespace"] = ""
		M(n["metadata"])["labels"] = map[string]any{"kubernetes.io/metadata.name": name}
		put(ws, "", "namespaces", "Namespace", n)
	}
	req := Request{Cluster: "c", Namespace: "app", TargetKind: "Namespace", TargetName: "app", Tasks: []string{"waypoint"}, Waypoint: &WaypointRequest{Mode: "new", Scope: "shared", Namespace: "infra", Name: "shared", Class: "istio-waypoint", Namespaces: []string{"app", "other"}, EnrolAmbient: true}}
	p, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Objects) != 1 || len(p.Changes) != 2 {
		t.Fatal(p.YAML)
	}
	if strings.Contains(p.YAML, "from: All") || !strings.Contains(p.YAML, "kubernetes.io/metadata.name") {
		t.Fatal("shared waypoint must name namespaces explicitly")
	}
	for _, ch := range p.Changes {
		if ch.Ref.Kind != "Namespace" || ch.UID == "" {
			t.Fatalf("unscoped change: %+v", ch)
		}
	}
}
func TestEndpointFailureOpensEvidenceAwareRecovery(t *testing.T) {
	ws := fixture(t)
	svc := Target(ws, "c", "Service", "app", "front")
	M(Path(svc.Obj, "spec", "selector"))["app"] = "wrong"
	v, e := NewView(ws, "c", "Service", "app", "front", "")
	if e != nil {
		t.Fatal(e)
	}
	if v.Recovery == nil || len(v.Recovery.Pods) == 0 {
		t.Fatal("missing recovery context")
	}
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"service-selector"}, SelectedPod: "front-1", Selector: map[string]string{"app": "front"}}
	p, e := Generate(ws, req)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Changes) != 1 || p.Changes[0].Fields[0].Path != "/spec/selector" {
		t.Fatal(p.YAML)
	}
	req.Selector = map[string]string{"app": "unrelated"}
	if _, e = Generate(ws, req); e == nil {
		t.Fatal("a selector that does not match the chosen workload was accepted")
	}
}
func TestPolicyCardLoadsItsActualMethodConfiguration(t *testing.T) {
	ws := fixture(t)
	policy := object("AuthorizationPolicy", "existing-methods", map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}}, "action": "DENY", "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"POST"}, "paths": []any{"/admin"}}}}}}})
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", policy)
	v, e := NewView(ws, "c", "Service", "app", "front", "")
	if e != nil {
		t.Fatal(e)
	}
	shown := false
	for _, g := range v.Graph.Groups {
		for _, n := range g.Nodes {
			if n.Ref.Name == "existing-methods" {
				shown = true
			}
		}
	}
	if !shown {
		t.Fatal("attached policy missing from map")
	}
	var req *Request
	for _, entry := range v.Editable {
		if entry.Ref.Name == "existing-methods" {
			req = entry.Request
		}
	}
	if req == nil || req.MethodAction != "DENY" || req.Methods[0] != "POST" {
		t.Fatal("policy editor did not load actual values")
	}
	req.Methods = []string{"PUT"}
	p, e := Generate(ws, *req)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Objects) > 0 || !strings.Contains(p.YAML, "/admin") || !strings.Contains(p.YAML, "PUT") {
		t.Fatal(p.YAML)
	}
}
