package ambient

import "testing"

func TestAgentgatewayHTTPMeshListenerUsesRenderedHBONE(t *testing.T) {
	ws := fixture(t)
	wp := Target(ws, "c", "Gateway", "app", "wp")
	M(wp.Obj["spec"])["gatewayClassName"] = "enterprise-agentgateway-waypoint"
	M(wp.Obj["spec"])["listeners"] = []any{map[string]any{"name": "mesh", "protocol": "HTTP", "port": 15088}}
	rendered := object("Service", "wp", map[string]any{"ports": []any{map[string]any{"port": 15088}, map[string]any{"port": 15008}}})
	put(ws, "", "services", "Service", rendered)
	r, e := Investigate(ws, "c", "app", "front", "")
	if e != nil {
		t.Fatal(e)
	}
	if findStep(t, r, "waypoint-listener").State != "pass" {
		t.Fatal("valid agentgateway mesh listener was rejected")
	}
	if waypointMeshListener(wp, map[string]any{"protocol": "HTTP", "port": 8080}) {
		t.Fatal("ordinary ingress HTTP listener mistaken for waypoint transport")
	}
}
