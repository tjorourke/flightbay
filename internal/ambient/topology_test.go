package ambient

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
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

func TestTopologyGlobalLinksRequirePublishedHostIdentityAndPeering(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	ws.SetHeader(&bundle.Header{Contexts: []string{"one", "two"}})
	var remoteEntry map[string]any
	for i, name := range []string{"one", "two"} {
		other := "two"
		address, remoteAddress := "192.0.2.1", "192.0.2.2"
		if i == 1 {
			other = "one"
			address, remoteAddress = remoteAddress, address
		}
		ws.AddObjects(name, bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace"}, []map[string]any{{"metadata": map[string]any{"name": "istio-system", "labels": map[string]any{"topology.istio.io/cluster": name}}}}, "ns")
		ws.AddObjects(name, bundle.APIResource{Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespaced: true}, []map[string]any{{"metadata": map[string]any{"name": "istio", "namespace": "istio-system"}, "data": map[string]any{"mesh": "trustDomain: " + name + ".trust\n"}}}, "config")
		gateway := func(gatewayName, class, peer, ip string) map[string]any {
			return map[string]any{"metadata": map[string]any{"name": gatewayName, "namespace": "istio-eastwest", "labels": map[string]any{"topology.istio.io/cluster": peer}}, "spec": map[string]any{"gatewayClassName": class, "addresses": []any{map[string]any{"value": ip}}}, "status": map[string]any{"addresses": []any{map[string]any{"value": ip}}}}
		}
		ws.AddObjects(name, bundle.APIResource{Group: GatewayGroup, Version: "v1", Resource: "gateways", Kind: "Gateway", Namespaced: true}, []map[string]any{gateway("eastwest", "istio-eastwest", name, address), gateway("peer", "istio-remote", other, remoteAddress)}, "gateways")
		ws.AddObjects(name, bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}, []map[string]any{{"metadata": map[string]any{"name": "echo", "namespace": "app", "labels": map[string]any{"solo.io/service-scope": "global"}}}}, "service")
		entry := map[string]any{"metadata": map[string]any{"name": "published-echo", "namespace": "istio-system", "labels": map[string]any{"solo.io/service-scope": "global", "solo.io/parent-service": "echo", "solo.io/parent-service-namespace": "app"}}, "spec": map[string]any{"hosts": []any{"echo.app.mesh.internal"}, "subjectAltNames": []any{"spiffe://" + other + ".trust/ns/app/sa/default"}}}
		ws.AddObjects(name, bundle.APIResource{Group: "networking.istio.io", Version: "v1", Resource: "serviceentries", Kind: "ServiceEntry", Namespaced: true}, []map[string]any{entry}, "entry")
		remoteEntry = entry
	}
	top := capturedTopology(ws)
	if len(top.Links) != 3 {
		t.Fatalf("expected two peer links and a global-Service link, got %+v", top.Links)
	}
	for _, link := range top.Links {
		if link.Kind == "global-service" && (link.Label != "echo.app.mesh.internal" || len(link.Evidence) != 4) {
			t.Fatalf("global link lost its evidence: %+v", link)
		}
	}
	M(remoteEntry["spec"])["subjectAltNames"] = []any{"spiffe://unrelated.trust/ns/app/sa/default"}
	if len(capturedTopology(ws).Links) != 2 {
		t.Fatal("same names and export labels invented global membership without matching identities")
	}
	M(remoteEntry["spec"])["subjectAltNames"] = []any{"spiffe://one.trust/ns/app/sa/default"}
	peer := ws.Get("one", GatewayGroup, "gateways", "istio-eastwest", "peer")
	M(peer.Obj["spec"])["addresses"] = []any{}
	if len(capturedTopology(ws).Links) != 1 {
		t.Fatal("global link invented without reciprocal captured peer addresses")
	}
}
