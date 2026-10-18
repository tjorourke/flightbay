package analyze

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func TestAmbientFindingsMatchInvestigationAndResolve(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	ws.SetHeader(&bundle.Header{Contexts: []string{"mesh1", "mesh2"}})
	security := bundle.APIResource{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespaced: true}
	for _, cluster := range []string{"mesh1", "mesh2"} {
		ws.AddObjects(cluster, security, nil, "security")
		ws.AddObjects(cluster, nsRes, []map[string]any{{"kind": "Namespace", "metadata": map[string]any{"name": "app", "labels": map[string]any{"istio.io/dataplane-mode": "ambient"}}}}, "ns")
		ws.AddObjects(cluster, services, []map[string]any{{"kind": "Service", "metadata": map[string]any{"name": "petstore", "namespace": "app"}, "spec": map[string]any{"ports": []any{map[string]any{"port": 8080}}}}}, "svc")
		name := "missing-gateway"
		if cluster == "mesh2" {
			name = "ingress"
		}
		ws.AddObjects(cluster, routes, []map[string]any{{"kind": "HTTPRoute", "metadata": map[string]any{"name": "petstore", "namespace": "app"}, "spec": map[string]any{"parentRefs": []any{map[string]any{"name": name}}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "petstore", "port": 8080}}}}}}}, "route")
		ws.AddObjects(cluster, gateways, []map[string]any{{"kind": "Gateway", "metadata": map[string]any{"name": "ingress", "namespace": "app"}, "spec": map[string]any{"listeners": []any{map[string]any{"name": "http", "port": 80, "protocol": "HTTP"}}}}}, "gw")
		ws.SetCoverage(bundle.Coverage{ID: cluster + "-gateways", Cluster: cluster, Type: bundle.CoverageResource, Group: gateways.Group, Version: "v1", Resource: "gateways", State: bundle.StateComplete})
	}
	count := 0
	for _, f := range Run(ws) {
		if f.Rule == "ambient.route-parent" {
			count++
			if f.Cluster != "mesh1" || f.Ambient == nil || f.Ambient.Subject.Name != "petstore" || f.Ambient.Step != "route-app-petstore-0" || f.NextStep == "" {
				t.Fatalf("wrong ambient finding: %+v", f)
			}
		}
	}
	if count != 1 {
		t.Fatalf("wanted only mesh1's missing parent, got %d", count)
	}
	ws.SetCoverage(bundle.Coverage{ID: "mesh1-gateways", Cluster: "mesh1", Type: bundle.CoverageResource, Group: gateways.Group, Version: "v1", Resource: "gateways", State: bundle.StateExcluded})
	for _, f := range Run(ws) {
		if f.Rule == "ambient.route-parent" {
			t.Fatal("uncaptured parent promoted to a confirmed finding")
		}
	}
}
