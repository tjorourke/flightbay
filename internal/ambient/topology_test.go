package ambient

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

func TestTopologyPreservesContextsAndOnlyResolvesObservedPeerIdentity(t *testing.T) {
	ws := fixture(t)
	namespaces := bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace"}
	services := bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}
	gateways := bundle.APIResource{Group: GatewayGroup, Version: "v1", Resource: "gateways", Kind: "Gateway", Namespaced: true}
	for _, c := range []string{"c", "remote-context"} {
		identity := "mesh1"
		if c == "remote-context" {
			identity = "mesh2"
		}
		ws.AddObjects(c, namespaces, []map[string]any{{"metadata": map[string]any{"name": "istio-system", "labels": map[string]any{"topology.istio.io/cluster": identity, "topology.istio.io/network": identity}}}}, "ns")
		ws.AddObjects(c, services, []map[string]any{{"metadata": map[string]any{"name": "shared", "namespace": "app", "labels": map[string]any{"solo.io/service-scope": "global", "solo.io/service-takeover": "true"}}}}, "svc")
	}
	peer := map[string]any{"metadata": map[string]any{"name": "peer", "namespace": "istio-eastwest", "generation": 2, "labels": map[string]any{"topology.istio.io/cluster": "mesh2"}}, "spec": map[string]any{"gatewayClassName": "istio-remote", "addresses": []any{map[string]any{"value": "192.0.2.2"}}, "listeners": []any{map[string]any{"port": 15012}}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": 1}}}}
	ws.AddObjects("c", gateways, []map[string]any{peer}, "gw")
	top := capturedTopology(ws)
	if len(top.Clusters) != 2 {
		t.Fatalf("contexts were lost: %+v", top)
	}
	var local TopologyCluster
	for _, c := range top.Clusters {
		if c.Context == "c" {
			local = c
		}
		found := false
		for _, s := range c.Services {
			if s.Name == "shared" {
				found = true
				if s.Cluster != c.Context || s.Scope != "global" || !s.Takeover {
					t.Fatalf("service provenance or export labels lost: %+v", s)
				}
			}
		}
		if !found {
			t.Fatalf("same-named service lost from context %s", c.Context)
		}
	}
	if len(local.Gateways) != 1 || local.Gateways[0].PeerContext != "remote-context" || local.Gateways[0].State != "unknown" || local.Gateways[0].Addresses[0] != "192.0.2.2" {
		t.Fatalf("wrong peer resolution or stale status: %+v", local.Gateways)
	}
	M(peer["metadata"])["labels"] = map[string]any{"topology.istio.io/cluster": "uncollected-mesh"}
	top = capturedTopology(ws)
	for _, c := range top.Clusters {
		for _, g := range c.Gateways {
			if g.PeerContext != "" {
				t.Fatal("invented context for an uncaptured mesh identity")
			}
		}
	}
}
