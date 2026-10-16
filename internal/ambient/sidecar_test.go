package ambient

import "testing"

func TestSidecarL7PolicyIsNotMisdiagnosedAsZtunnelFailure(t *testing.T) {
	ws := fixture(t)
	pod := ws.Get("c", "", "pods", "app", "front-1")
	M(pod.Obj["spec"])["containers"] = []any{map[string]any{"name": "istio-proxy"}}
	p := object("AuthorizationPolicy", "sidecar", map[string]any{
		"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}},
		"rules":    []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET"}}}}}},
	})
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", p)
	r, _ := Investigate(ws, "c", "app", "front", "")
	if findStep(t, r, "policy-sidecar").State != "warning" {
		t.Fatal("a sidecar policy was misdiagnosed as a ztunnel failure")
	}
}
