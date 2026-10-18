package ambient

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func pathFixture(t *testing.T) (*store.Workspace, PathRequest) {
	ws := fixture(t)
	src := Target(ws, "c", "Pod", "app", "front-1")
	M(src.Obj["spec"])["nodeName"] = "node-a"
	M(src.Obj["spec"])["serviceAccountName"] = "caller"
	src.Obj["status"] = map[string]any{"podIP": "10.1.0.2", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	zt := object("Pod", "ztunnel-a", map[string]any{"nodeName": "node-a", "containers": []any{map[string]any{"name": "ztunnel", "image": "istio/ztunnel:1.30"}}})
	M(zt["metadata"])["labels"] = map[string]any{"app": "ztunnel"}
	zt["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	put(ws, "", "pods", "Pod", zt)
	relay := object("Pod", "eastwest-relay", map[string]any{"nodeName": "node-a", "containers": []any{map[string]any{"name": "relay", "image": "istio/ztunnel:1.30"}}})
	M(relay["metadata"])["labels"] = map[string]any{"gateway.networking.k8s.io/gateway-name": "eastwest"}
	relay["status"] = zt["status"]
	put(ws, "", "pods", "Pod", relay)
	return ws, PathRequest{Source: reference(src), Destination: reference(Target(ws, "c", "Service", "app", "front")), Port: 80}
}
func TestRequestPathConfiguredDoesNotMeanObserved(t *testing.T) {
	ws, q := pathFixture(t)
	p, e := TraceRequestPath(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	if p.Observed.Records != 0 || !strings.Contains(p.Observed.Note, "not evidence") {
		t.Fatal(p.Observed)
	}
	found := map[string]string{}
	for _, h := range p.Hops {
		found[h.ID] = h.State
		if h.ID == "source-ztunnel" {
			for _, ref := range h.Refs {
				if ref.Name == "eastwest-relay" {
					t.Fatal("east-west relay mistaken for a node ztunnel")
				}
			}
		}
	}
	if found["source-ztunnel"] != "pass" || found["waypoint"] != "pass" || found["backend"] != "pass" {
		t.Fatalf("unexpected configuration: %v", found)
	}
	if len(p.Principals) != 1 || !strings.Contains(p.Principals[0], "/sa/caller") {
		t.Fatal(p.Principals)
	}
	q.Port = 999
	if _, e = TraceRequestPath(ws, q); e == nil {
		t.Fatal("container/unknown port accepted")
	}
}
func TestRequestPathTrafficFiltersDoNotInventHTTPOrCaller(t *testing.T) {
	ws, q := pathFixture(t)
	flows := []store.Flow{
		{Cluster: "c", Source: "generic", SrcNamespace: "app", SrcPod: "front-1", DstHost: "front.app.svc.cluster.local", DstPort: 8080, Method: "GET", Path: "/pets", Verdict: "allowed", Count: 3},
		{Cluster: "c", Source: "ztunnel", SrcNamespace: "app", SrcPod: "front-1", DstHost: "front.app.svc.cluster.local", DstPort: 80, Count: 7},
		{Cluster: "c", Source: "generic", SrcNamespace: "app", SrcPod: "front-other", DstHost: "front.app.svc.cluster.local", DstPort: 80, Method: "GET", Path: "/pets", Count: 100},
		{Cluster: "c", Source: "coredns", SrcNamespace: "app", SrcPod: "front-1", DstHost: "front.app.svc.cluster.local", Method: "GET", Path: "/pets", Count: 100},
	}
	if e := ws.AddFlows(flows); e != nil {
		t.Fatal(e)
	}
	q.Method = "GET"
	q.Path = "/pets?ignored=query"
	p, e := TraceRequestPath(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	if p.Observed.Records != 3 || p.Observed.WithoutHTTP != 7 {
		t.Fatalf("wrong evidence: %+v", p.Observed)
	}
	q.Method = "DELETE"
	p, _ = TraceRequestPath(ws, q)
	if p.Observed.Records != 0 {
		t.Fatal("GET traffic counted for DELETE")
	}
}
func TestRequestPathMissingWaypointAndCrossClusterAreNotGreen(t *testing.T) {
	ws, q := pathFixture(t)
	M(Target(ws, "c", "Namespace", "", "app").Obj["metadata"])["labels"] = map[string]any{"istio.io/dataplane-mode": "ambient", "istio.io/use-waypoint": "absent"}
	p, e := TraceRequestPath(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	for _, h := range p.Hops {
		if h.ID == "waypoint" && h.State != "fail" {
			t.Fatal(h)
		}
	}
	svc := object("Service", "front", map[string]any{"ports": []any{map[string]any{"port": 80}}})
	ws.AddObjects("remote", bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}, []map[string]any{svc}, "remote")
	q.Destination.Cluster = "remote"
	p, e = TraceRequestPath(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	seen := false
	for _, h := range p.Hops {
		if h.ID == "eastwest" {
			seen = true
			if h.State != "unknown" {
				t.Fatal("invented cross-cluster link")
			}
		}
	}
	addressSeen := false
	for _, e := range p.Configuration {
		if e.ID == "address" {
			addressSeen = true
			if e.State != "warning" {
				t.Fatal("local DNS implies remote reachability")
			}
		}
	}
	if !addressSeen {
		t.Fatal("address evidence was lost")
	}
	if !seen {
		t.Fatal("missing cross-cluster evidence gap")
	}
}

func TestRequestPathNumbersOnlyDataplaneComponents(t *testing.T) {
	ws, q := pathFixture(t)
	addRoute(ws, map[string]any{"group": "", "kind": "Service", "name": "front", "port": 80}, nil)
	p, err := TraceRequestPath(ws, q)
	if err != nil {
		t.Fatal(err)
	}
	configuration := map[string]bool{}
	for _, e := range p.Configuration {
		configuration[e.ID] = true
	}
	if !configuration["address"] || !configuration["routing"] {
		t.Fatalf("supporting evidence lost: %+v", p.Configuration)
	}
	for _, h := range p.Hops {
		if h.ID == "address" || h.ID == "routing" {
			t.Fatalf("configuration shown as traffic hop: %+v", h)
		}
		if h.ID == "caller" || h.ID == "backend" {
			found := false
			for _, ref := range h.Refs {
				found = found || ref.Kind == "Pod"
			}
			if !found {
				t.Fatalf("application hop lacks captured Pod references: %+v", h)
			}
		}
	}
}

func TestGlobalPathUsesTheCapturedSourceWaypointBeforeCrossClusterTransport(t *testing.T) {
	ws, q := pathFixture(t)
	ns := object("Namespace", "app", nil)
	M(ns["metadata"])["namespace"] = ""
	M(ns["metadata"])["labels"] = map[string]any{"istio.io/dataplane-mode": "ambient", "istio.io/use-waypoint": "remote-wp"}
	svc := object("Service", "front", map[string]any{"ports": []any{map[string]any{"port": 80}}, "selector": map[string]any{"app": "front"}})
	remoteWP := M(Copy(Target(ws, "c", "Gateway", "app", "wp").Obj))
	M(remoteWP["metadata"])["name"] = "remote-wp"
	for _, v := range []struct {
		api     bundle.APIResource
		objects []map[string]any
	}{
		{bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace"}, []map[string]any{ns}},
		{bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}, []map[string]any{svc}},
		{bundle.APIResource{Group: GatewayGroup, Version: "v1", Resource: "gateways", Kind: "Gateway", Namespaced: true}, []map[string]any{remoteWP}},
	} {
		ws.AddObjects("remote", v.api, v.objects, "remote-path-"+v.api.Resource)
	}
	alias := object("ServiceEntry", "autogen.app.front", map[string]any{"hosts": []any{"front.app.mesh.internal"}})
	M(alias["metadata"])["namespace"] = "istio-system"
	M(alias["metadata"])["labels"] = map[string]any{"solo.io/parent-service": "front", "solo.io/parent-service-namespace": "app", "solo.io/service-scope": "global"}
	alias["status"] = map[string]any{"conditions": []any{map[string]any{"type": "istio.io/WaypointBound", "status": "True"}}}
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry", alias)
	q.Destination.Cluster = "remote"
	q.Host = "front.app.mesh.internal"
	p, err := TraceRequestPath(ws, q)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, h := range p.Hops {
		positions[h.ID] = i
		if h.ID == "waypoint" {
			t.Fatal("destination waypoint inserted a second time after source waypoint processing")
		}
	}
	if _, ok := positions["source-waypoint"]; !ok || positions["source-waypoint"] >= positions["eastwest"] {
		t.Fatalf("source waypoint missing or misplaced: %+v", p.Hops)
	}
	found := false
	for _, e := range p.Configuration {
		found = found || e.ID == "destination-waypoint-binding"
	}
	if !found {
		t.Fatal("destination binding evidence was discarded")
	}
}
func TestRequestPathCallerChoicesAndSidecar(t *testing.T) {
	ws, q := pathFixture(t)
	opts := RequestPathOptions(ws)
	if len(opts.Callers) < 2 || len(opts.Destinations) != 3 {
		t.Fatal(opts)
	}
	pod := callerObject(ws, q.Source)
	M(pod.Obj["spec"])["containers"] = []any{map[string]any{"name": "istio-proxy"}}
	p, _ := TraceRequestPath(ws, q)
	for _, h := range p.Hops {
		if h.ID == "source-ztunnel" {
			t.Fatal("sidecar caller described as ztunnel")
		}
	}
	q.Source.Cluster = "absent"
	if _, e := TraceRequestPath(ws, q); e == nil {
		t.Fatal("missing caller accepted")
	}
}

func TestOrdinaryServiceDoesNotNeedOptionalRouteOrDiagnosticRepair(t *testing.T) {
	ws, q := pathFixture(t)
	r, e := Investigate(ws, "c", "app", "front", "")
	if e != nil {
		t.Fatal(e)
	}
	if s := findStep(t, r, "routes"); s.State != "na" {
		t.Fatalf("optional route absence shown as a problem: %+v", s)
	}
	p, e := TraceRequestPath(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range p.Checks {
		if s.ID == "routes" || s.ID == "dataplane" {
			t.Fatalf("optional/diagnostic evidence presented as a repair: %+v", s)
		}
	}
	r, e = Investigate(ws, "c", "app", "front", "explicitly-requested-missing-route")
	if e != nil {
		t.Fatal(e)
	}
	if s := findStep(t, r, "routes"); s.State != "fail" {
		t.Fatalf("explicit missing route must still fail with complete capture: %+v", s)
	}
	ws.SetCoverage(bundle.Coverage{ID: "httproutes", Cluster: "c", Type: bundle.CoverageResource, Group: GatewayGroup, Version: "v1", Resource: "httproutes", State: bundle.StateExcluded})
	r, _ = Investigate(ws, "c", "app", "front", "")
	if s := findStep(t, r, "routes"); s.State != "unknown" {
		t.Fatal("missing collection was hidden as not-applicable")
	}
}
