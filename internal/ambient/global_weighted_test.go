package ambient

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func capturedGlobalSplit(t *testing.T) (*store.Workspace, Request) {
	t.Helper()
	ws := fixture(t)
	gc := object("GatewayClass", "istio-waypoint", map[string]any{"controllerName": "istio.io/mesh-controller"})
	M(gc["metadata"])["namespace"] = ""
	put(ws, GatewayGroup, "gatewayclasses", "GatewayClass", gc)
	pilot := object("Pod", "istiod", map[string]any{"containers": []any{map[string]any{"image": "istio/pilot:1.30.4-solo"}}})
	M(pilot["metadata"])["namespace"] = "istio-system"
	M(pilot["metadata"])["labels"] = map[string]any{"app": "istiod"}
	put(ws, "", "pods", "Pod", pilot)
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry")
	put(ws, "admin.solo.io", "segments", "Segment")
	svc := Target(ws, "c", "Service", "app", "front")
	M(svc.Obj["metadata"])["labels"] = map[string]any{"solo.io/service-scope": "global"}
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}, GlobalWeights: map[string]int{"c": 70, "remote": 30}}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range p.Objects {
		M(o["metadata"])["uid"] = "uid-" + S(Path(o, "metadata", "name"))
		if o["kind"] == "HTTPRoute" {
			// Gateway API admission adds this default. Reapplying the split
			// must not keep rewriting it, or erase independently edited fields.
			M(L(Path(o, "spec", "rules"))[0])["matches"] = []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}}}
			M(o["metadata"])["annotations"] = map[string]any{"customer.example/note": "keep"}
			put(ws, GatewayGroup, "httproutes", "HTTPRoute", o)
		} else {
			put(ws, "", "services", "Service", o)
		}
	}
	return ws, req
}

func TestGlobalWeightedSplitReapplyIsNoop(t *testing.T) {
	ws, req := capturedGlobalSplit(t)
	p, err := Generate(ws, req)
	if err != nil || len(p.Objects) != 0 || len(p.Changes) != 0 {
		t.Fatalf("same split must not create an existing HTTPRoute or rewrite defaults: %v %+v", err, p)
	}
}

func TestGlobalWeightedSplitUpdatesExistingRoute(t *testing.T) {
	ws, req := capturedGlobalSplit(t)
	route := Target(ws, req.Cluster, "HTTPRoute", req.Namespace, "front-cluster-split")
	before := Copy(route.Obj)
	req.GlobalWeights = map[string]int{"c": 60, "remote": 40}
	p, err := Generate(ws, req)
	if err != nil || len(p.Objects) != 0 || len(p.Changes) != 1 {
		t.Fatalf("weight edit must update the captured route: %v %+v", err, p)
	}
	ch := p.Changes[0]
	if ch.Delete || ch.Ref.Kind != "HTTPRoute" || ch.UID != "uid-front-cluster-split" || len(ch.Fields) != 1 || ch.Fields[0].Path != "/spec/rules" {
		t.Fatalf("unexpected edit scope: %+v", ch)
	}
	if !Equal(M(L(ch.Fields[0].After)[0])["matches"], M(L(Path(route.Obj, "spec", "rules"))[0])["matches"]) {
		t.Fatal("route matches were lost")
	}
	if !Equal(before, route.Obj) {
		t.Fatal("preview mutated the capture")
	}
}
