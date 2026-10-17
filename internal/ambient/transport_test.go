package ambient

import (
	"strings"
	"testing"
)

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

func TestWaypointListenerGuidance(t *testing.T) {
	for _, tc := range []struct {
		name, class, state, host, suggestion string
		meshHTTP, rendered, hbone, action    bool
	}{
		{"missing Istio listener", "istio-waypoint", "fail", "istio.io", "listener configuration", false, false, false, true},
		{"missing Enterprise listener", "enterprise-agentgateway-waypoint", "fail", "docs.solo.io", "listener configuration", false, false, false, true},
		{"rendered port missing", "enterprise-agentgateway-waypoint", "fail", "docs.solo.io", "controller reconciliation", true, true, false, true},
		{"rendered evidence missing", "enterprise-agentgateway-waypoint", "unknown", "docs.solo.io", "Refresh inventory", true, false, false, false},
		{"valid Enterprise transport", "enterprise-agentgateway-waypoint", "pass", "docs.solo.io", "", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			wp := Target(ws, "c", "Gateway", "app", "wp")
			M(wp.Obj["spec"])["gatewayClassName"] = tc.class
			port := 8080
			if tc.meshHTTP {
				port = 15088
			}
			M(wp.Obj["spec"])["listeners"] = []any{map[string]any{"name": "mesh", "protocol": "HTTP", "port": port}}
			if tc.rendered {
				ports := []any{map[string]any{"port": 15088}}
				if tc.hbone {
					ports = append(ports, map[string]any{"port": 15008})
				}
				put(ws, "", "services", "Service", object("Service", "wp", map[string]any{"ports": ports}))
			}
			v, err := NewView(ws, "c", "Service", "app", "front", "")
			if err != nil {
				t.Fatal(err)
			}
			s := findStep(t, v.Investigation, "waypoint-listener")
			if s.State != tc.state || !strings.Contains(s.NextStep, tc.suggestion) {
				t.Fatalf("wrong listener guidance: %+v", s)
			}
			if len(s.Docs) == 0 || !strings.Contains(s.Docs[0].URL, tc.host) {
				t.Fatalf("missing edition-specific guide: %+v", s.Docs)
			}
			if (len(s.Actions) > 0) != tc.action {
				t.Fatalf("unexpected remediation for %s: %+v", s.State, s.Actions)
			}
			if tc.action && s.Actions[0].ID != "waypoint" {
				t.Fatal("remediation does not open the supported waypoint wizard")
			}
			if s.State == "pass" && s.NextStep != "" {
				t.Fatal("healthy waypoint received repair advice")
			}
		})
	}
}
