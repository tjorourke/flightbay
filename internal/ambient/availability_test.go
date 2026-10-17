package ambient

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func TestAmbientAvailabilityDoesNotConfuseGatewayOrSidecarAPIs(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	ws.SetHeader(&bundle.Header{Contexts: []string{"mesh", "plain"}})
	check := func(want bool) {
		t.Helper()
		for _, a := range AvailabilityByCluster(ws) {
			if a.Cluster == "mesh" && a.Available != want {
				t.Fatalf("availability: %+v", a)
			}
		}
	}
	ws.RegisterResources("mesh", []bundle.APIResource{{Group: GatewayGroup, Version: "v1", Resource: "gateways", Kind: "Gateway"}})
	check(false)
	ws.RegisterResources("mesh", []bundle.APIResource{{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy"}})
	check(false)
	ws.AddObjects("plain", bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace"}, []map[string]any{{"metadata": map[string]any{"name": "app", "labels": map[string]any{"istio.io/dataplane-mode": "ambient"}}}}, "ns")
	check(false)
	ws.AddObjects("mesh", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}, []map[string]any{{"metadata": map[string]any{"name": "ztunnel", "namespace": "istio-system"}, "spec": map[string]any{"containers": []any{map[string]any{"image": "docker.io/istio/ztunnel:1.30.4"}}}}}, "pods")
	check(true)
	for _, a := range AvailabilityByCluster(ws) {
		if a.Cluster == "plain" && a.Available {
			t.Fatal("another cluster's APIs leaked into availability")
		}
	}
}

func TestAmbientNamespaceAndIstioDiscoveryEstablishAvailability(t *testing.T) {
	ws := fixture(t)
	available := AvailabilityByCluster(ws)
	if len(available) != 1 || !available[0].Available {
		t.Fatalf("ambient namespace and Istio APIs not recognised: %+v", available)
	}
	h := ws.Header()
	h.Source = bundle.SourceRepo
	ws.SetHeader(h)
	if AvailabilityByCluster(ws)[0].Available {
		t.Fatal("a declared repository scan was treated as a deployed installation")
	}
}
