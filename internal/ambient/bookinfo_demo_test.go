package ambient

import (
	"github.com/tjorourke/flightbay/internal/store"
	"strings"
	"testing"
)

func demoFixture(t *testing.T) (*store.Workspace, Request) {
	ws := fixture(t)
	a := object("Namespace", "kube-system", nil)
	M(a["metadata"])["namespace"] = ""
	put(ws, "", "namespaces", "Namespace", a)
	gc := object("GatewayClass", "istio-waypoint", map[string]any{"controllerName": "istio.io/mesh-controller"})
	M(gc["metadata"])["namespace"] = ""
	put(ws, GatewayGroup, "gatewayclasses", "GatewayClass", gc)
	return ws, Request{Cluster: "c", Namespace: "flightbay-bookinfo-test", Tasks: []string{"bookinfo-demo"}, Demo: &BookinfoRequest{Stage: "namespace", WaypointClass: "istio-waypoint", Traffic: true}}
}
func TestBookinfoPackagedPlansAndOwnership(t *testing.T) {
	ws, q := demoFixture(t)
	p, e := GenerateBookinfo(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Objects) != 1 || S(p.Objects[0]["kind"]) != "Namespace" {
		t.Fatal("namespace stage must create only the namespace")
	}
	n := M(Copy(p.Objects[0]))
	M(n["metadata"])["uid"] = "demo-ns-uid"
	put(ws, "", "namespaces", "Namespace", n)
	if _, e = GenerateBookinfo(ws, q); e == nil {
		t.Fatal("existing namespace was adopted")
	}
	q.Demo.Stage = "workloads"
	p, e = GenerateBookinfo(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Objects) != 18 {
		t.Fatalf("expected waypoint, apps and traffic generator (no HTTPRoute), got %d", len(p.Objects))
	}
	for _, o := range p.Objects {
		if S(Path(o, "metadata", "namespace")) != q.Namespace {
			t.Fatal("demo escaped target namespace")
		}
		if S(o["kind"]) == "HTTPRoute" {
			parent := M(L(Path(o, "spec", "parentRefs"))[0])
			if S(parent["kind"]) != "Service" || S(parent["name"]) != "reviews" || N(parent["port"]) != 9080 {
				t.Fatal(parent)
			}
		}
	}
	if strings.Contains(p.YAML, "wrong-listener") || strings.Contains(p.YAML, "missing-gateway") {
		t.Fatal("packaged demo contains an intentional fault")
	}
	if !strings.Contains(p.YAML, "docker.io/istio/examples-bookinfo-productpage-v1:1.20.2") {
		t.Fatal("image sources are not explicit")
	}
	q.Namespace = "kube-system"
	if _, e = GenerateBookinfo(ws, q); e == nil {
		t.Fatal("system namespace accepted")
	}
}
func TestBookinfoRejectsUnsupportedGlobalPublication(t *testing.T) {
	ws, q := demoFixture(t)
	q.Demo.Global = true
	if _, e := GenerateBookinfo(ws, q); e == nil {
		t.Fatal("global publication offered without captured Enterprise Istio/peering")
	}
}

func TestBookinfoDedicatedWaypointsBindOnlyMainServices(t *testing.T) {
	ws, q := demoFixture(t)
	q.Namespace = "bookinfo-demo"
	q.Demo.WaypointScope = "service"
	q.Demo.WaypointName = "dedicated"
	p, e := GenerateBookinfo(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	if S(Path(p.Objects[0], "metadata", "labels", "istio.io/use-waypoint")) != "" {
		t.Fatal("service scope must not set a namespace default")
	}
	n := M(Copy(p.Objects[0]))
	M(n["metadata"])["uid"] = "dedicated-ns"
	put(ws, "", "namespaces", "Namespace", n)
	q.Demo.Stage = "workloads"
	p, e = GenerateBookinfo(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	gateways := map[string]bool{}
	for _, o := range p.Objects {
		if S(o["kind"]) == "Gateway" {
			gateways[S(Path(o, "metadata", "name"))] = true
		}
		if S(o["kind"]) == "Service" {
			name := S(Path(o, "metadata", "name"))
			wp := S(Path(o, "metadata", "labels", "istio.io/use-waypoint"))
			if name == "reviews-v1" || name == "reviews-v2" {
				if wp != "none" {
					t.Fatal("version backend would re-enter a waypoint")
				}
			} else if wp != "dedicated-"+name {
				t.Fatalf("missing dedicated binding for %s: %s", name, wp)
			}
		}
	}
	if len(gateways) != 4 || len(p.Objects) != 21 || len(p.Changes) != 0 {
		t.Fatalf("wrong dedicated plan: gateways=%v objects=%d changes=%v", gateways, len(p.Objects), p.Changes)
	}
	q.Demo.WaypointName = strings.Repeat("a", 60)
	if _, e = GenerateBookinfo(ws, q); e == nil {
		t.Fatal("overlong generated waypoint names accepted")
	}
}

func TestBookinfoSharedBindingIsReviewedInWorkloadStage(t *testing.T) {
	ws, q := demoFixture(t)
	q.Demo.WaypointName = "my-shared-waypoint"
	p, e := GenerateBookinfo(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	n := M(Copy(p.Objects[0]))
	if S(Path(n, "metadata", "labels", "istio.io/use-waypoint")) != "" {
		t.Fatal("namespace was bound before waypoint creation")
	}
	M(n["metadata"])["uid"] = "shared-ns"
	put(ws, "", "namespaces", "Namespace", n)
	q.Demo.Stage = "workloads"
	p, e = GenerateBookinfo(ws, q)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Changes) != 1 || p.Changes[0].Ref.Kind != "Namespace" || p.Changes[0].Fields[0].After != "my-shared-waypoint" {
		t.Fatal("namespace binding is not in the reviewed diff")
	}
}
