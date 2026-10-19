package resmap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	collectpolicy "github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

func res(group, resource, kind string, namespaced bool) bundle.APIResource {
	return bundle.APIResource{Group: group, Version: "v1", Resource: resource, Kind: kind, Namespaced: namespaced}
}

func obj(kind, ns, name string, spec map[string]any) map[string]any {
	return map[string]any{"kind": kind, "metadata": map[string]any{"name": name, "namespace": ns}, "spec": spec}
}

func complete(ws *store.Workspace, r bundle.APIResource) {
	ws.SetCoverage(bundle.Coverage{ID: "c." + r.Resource, Cluster: "c", Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, State: bundle.StateComplete})
}

func makeWS(t *testing.T) *store.Workspace {
	t.Helper()
	ws := store.New()
	p, _ := collectpolicy.Profile("standard")
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	for _, r := range []bundle.APIResource{
		res("", "pods", "Pod", true),
		res("apps", "deployments", "Deployment", true),
		res("apps", "replicasets", "ReplicaSet", true),
		res("", "services", "Service", true),
		res("discovery.k8s.io", "endpointslices", "EndpointSlice", true),
		res("gateway.networking.k8s.io", "gateways", "Gateway", true),
		res("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true),
		res("networking.k8s.io", "ingresses", "Ingress", true),
		res("", "configmaps", "ConfigMap", true),
		res("", "secrets", "Secret", true),
		res("", "serviceaccounts", "ServiceAccount", true),
	} {
		complete(ws, r)
	}
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete })
	return ws
}

func TestOwnerChain(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("apps", "deployments", "Deployment", true), []map[string]any{obj("Deployment", "shop", "web", map[string]any{})}, "d")
	ws.AddObjects("c", res("apps", "replicasets", "ReplicaSet", true), []map[string]any{
		{"kind": "ReplicaSet", "metadata": map[string]any{"name": "web-rs", "namespace": "shop", "ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "web", "uid": "d1"}}}},
	}, "rs")
	ws.AddObjects("c", res("", "pods", "Pod", true), []map[string]any{
		{"kind": "Pod", "metadata": map[string]any{"name": "web-1", "namespace": "shop", "ownerReferences": []any{map[string]any{"kind": "ReplicaSet", "name": "web-rs", "uid": "rs1"}}}},
	}, "p")
	g := Build(ws, "c", "", nil)
	// expect edges deployment->rs , rs->pod
	if len(g.Edges) < 2 {
		t.Fatalf("want owner edges, got %d", len(g.Edges))
	}
}

func TestServiceEndpointSlice(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("", "services", "Service", true), []map[string]any{obj("Service", "shop", "web", map[string]any{"selector": map[string]any{"app": "web"}})}, "s")
	ws.AddObjects("c", res("discovery.k8s.io", "endpointslices", "EndpointSlice", true), []map[string]any{
		{"kind": "EndpointSlice", "metadata": map[string]any{"name": "web-abc", "namespace": "shop", "labels": map[string]any{"kubernetes.io/service-name": "web"}}},
	}, "es")
	g := Build(ws, "c", "shop", nil)
	found := false
	for _, e := range g.Edges {
		if e.Kind == "endpoints" {
			found = true
		}
	}
	if !found {
		t.Fatal("no service->slice edge")
	}
}

func TestSelectorMatchesNothing(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("", "services", "Service", true), []map[string]any{obj("Service", "shop", "web", map[string]any{"selector": map[string]any{"app": "web"}})}, "s")
	// no pods, services complete
	complete(ws, res("", "services", "Service", true))
	g := Build(ws, "c", "shop", nil)
	foundBroken := false
	for _, e := range g.Edges {
		if e.Broken && e.Reason == "selector matches nothing" {
			foundBroken = true
		}
	}
	if !foundBroken {
		t.Fatal("expected broken selector")
	}
}

func TestMissingBackend(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true), []map[string]any{
		{"kind": "HTTPRoute", "metadata": map[string]any{"name": "r", "namespace": "shop"}, "spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "missing"}}}}}},
	}, "rt")
	complete(ws, res("", "services", "Service", true))
	g := Build(ws, "c", "shop", nil)
	found := false
	for _, e := range g.Edges {
		if e.Reason == "missing backend" && e.Broken {
			found = true
		}
	}
	if !found {
		t.Fatal("missing backend not marked")
	}
}

func TestUnresolvedParentRef(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true), []map[string]any{
		{"kind": "HTTPRoute", "metadata": map[string]any{"name": "r", "namespace": "shop"}, "spec": map[string]any{"parentRefs": []any{map[string]any{"name": "nogw"}}}},
	}, "rt")
	complete(ws, res("gateway.networking.k8s.io", "gateways", "Gateway", true))
	g := Build(ws, "c", "shop", nil)
	found := false
	for _, e := range g.Edges {
		if e.Reason == "unresolved parentRef" {
			found = true
		}
	}
	if !found {
		t.Fatal("unresolved parent not broken")
	}
}

func TestIngressBackend(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("networking.k8s.io", "ingresses", "Ingress", true), []map[string]any{
		{"kind": "Ingress", "metadata": map[string]any{"name": "i", "namespace": "shop"}, "spec": map[string]any{"rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{"backend": map[string]any{"service": map[string]any{"name": "web"}}}}}}}}},
	}, "ing")
	ws.AddObjects("c", res("", "services", "Service", true), []map[string]any{obj("Service", "shop", "web", nil)}, "s")
	g := Build(ws, "c", "shop", nil)
	if len(g.Edges) == 0 {
		t.Fatal("no ingress backend edge")
	}
}

func TestNoTCPRoute(t *testing.T) {
	ws := makeWS(t)
	// add a tcproutes resource to trigger note
	ws.AddObjects("c", res("gateway.networking.k8s.io", "tcproutes", "TCPRoute", true), []map[string]any{obj("TCPRoute", "shop", "t", nil)}, "t")
	g := Build(ws, "c", "", nil)
	found := false
	for _, n := range g.Notes {
		if strings.Contains(n, "TCPRoute") || strings.Contains(n, "not confirmed") {
			found = true
		}
	}
	if !found {
		t.Fatal("tcp note missing")
	}
}

func TestWaypoint(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("", "pods", "Pod", true), []map[string]any{
		{"kind": "Pod", "metadata": map[string]any{"name": "p", "namespace": "shop", "labels": map[string]any{"istio.io/use-waypoint": "wp"}}},
	}, "p")
	ws.AddObjects("c", res("gateway.networking.k8s.io", "gateways", "Gateway", true), []map[string]any{obj("Gateway", "shop", "wp", nil)}, "gw")
	g := Build(ws, "c", "shop", nil)
	found := false
	for _, e := range g.Edges {
		if e.Kind == "waypoint" {
			found = true
		}
	}
	if !found {
		t.Fatal("no waypoint edge")
	}
}

func TestEnterpriseOnlyWhenCRDPresent(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy", true), []map[string]any{obj("EnterpriseAgentgatewayPolicy", "shop", "pol", map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "web"}}})}, "pol")
	// register for ent
	complete(ws, res("enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy", true))
	ws.RegisterResources("c", []bundle.APIResource{res("enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy", true)})
	g := Build(ws, "c", "shop", nil)
	has := false
	for _, n := range g.Nodes {
		if strings.Contains(n.Kind, "Enterprise") {
			has = true
		}
	}
	if !has {
		t.Fatal("should have when CRD registered")
	}
}

func TestAgentgatewaySelector(t *testing.T) {
	ws := makeWS(t)
	complete(ws, res("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true))
	ws.AddObjects("c", res("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true), []map[string]any{
		{"kind": "AgentgatewayPolicy", "metadata": map[string]any{"name": "p", "namespace": "shop"}, "spec": map[string]any{"targetSelectors": []any{map[string]any{"group": "", "kind": "Service", "matchLabels": map[string]any{"app": "web"}}}}},
	}, "pol")
	ws.AddObjects("c", res("", "services", "Service", true), []map[string]any{{"kind": "Service", "metadata": map[string]any{"name": "web", "namespace": "shop", "labels": map[string]any{"app": "web"}}}}, "s")
	g := Build(ws, "c", "shop", nil)
	found := false
	for _, e := range g.Edges {
		if e.Kind == "policy" {
			found = true
		}
	}
	if !found {
		t.Fatal("policy selector not matched")
	}
}

func TestUses(t *testing.T) {
	ws := makeWS(t)
	ws.AddObjects("c", res("apps", "deployments", "Deployment", true), []map[string]any{
		{"kind": "Deployment", "metadata": map[string]any{"name": "d", "namespace": "shop"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"serviceAccountName": "app",
			"volumes": []any{map[string]any{"configMap": map[string]any{"name": "cm1"}}, map[string]any{"secret": map[string]any{"secretName": "sec1"}}},
		}}}},
	}, "d")
	g := Build(ws, "c", "shop", nil)
	if len(g.Edges) == 0 {
		t.Fatal("no uses edges")
	}
}

func TestCollapse(t *testing.T) {
	ws := makeWS(t)
	for i := 0; i < 41; i++ {
		ws.AddObjects("c", res("", "pods", "Pod", true), []map[string]any{obj("Pod", "big", "p"+string(rune('a'+i%26))+string(rune('0'+i/26)), nil)}, "p")
	}
	g := Build(ws, "c", "big", nil)
	for _, b := range g.Namespaces {
		if b.Namespace == "big" && !b.Collapse {
			t.Fatal("41 should collapse")
		}
	}
} 
