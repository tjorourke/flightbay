package reach

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// Ports decode as float64 from a bundle's JSON and as int64 from the API
// clients in live mode. A policy allowing only 8080 must not allow 9090
// either way (int64 used to read as "no port", which allowed every port).
func TestPortMatchesNumberTypes(t *testing.T) {
	for _, port := range []any{float64(8080), int64(8080), int(8080), "8080"} {
		rule := []any{map[string]any{"port": port}}
		if !portMatches(rule, []string{"8080"}) {
			t.Errorf("%T port: 8080 should match", port)
		}
		if portMatches(rule, []string{"9090"}) {
			t.Errorf("%T port: 9090 must not match a rule for 8080", port)
		}
	}
	for _, end := range []any{float64(8090), int64(8090)} {
		rule := []any{map[string]any{"port": int64(8080), "endPort": end}}
		if !portMatches(rule, []string{"8085"}) || portMatches(rule, []string{"8091"}) {
			t.Errorf("%T endPort range", end)
		}
	}
}

func TestServicePortsNumberTypes(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	res := bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}
	ws.AddObjects("c", res, []map[string]any{
		{"kind": "Service", "metadata": map[string]any{"name": "live", "namespace": "shop"},
			"spec": map[string]any{"selector": map[string]any{"app": "a"}, "ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8080)}}}},
		{"kind": "Service", "metadata": map[string]any{"name": "bundle", "namespace": "shop"},
			"spec": map[string]any{"selector": map[string]any{"app": "a"}, "ports": []any{map[string]any{"port": float64(80), "targetPort": float64(8080)}}}},
	}, "c1")
	c := newCluster(ws, "c")
	for _, sv := range c.services {
		if len(sv.ports) != 1 || sv.ports[0].port != 80 || sv.ports[0].target != "8080" {
			t.Errorf("%s: ports %+v", sv.name, sv.ports)
		}
	}
}
