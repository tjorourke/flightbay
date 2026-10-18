package reach

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

func TestTrafficDoesNotInventWaypointTraversal(t *testing.T) {
	ws := incident(t, "")
	ws.AddLogLines("c", "istio-system", "ztunnel-1", "istio-proxy", "current", []string{ztLine("eval-agent", "spiffe://cluster.local/ns/agents/sa/eval-agent", "artifactory.tools.svc.cluster.local", "artifactory", "tools", "10.244.0.9:15008", "")}, "bypass")
	ws.WaitLogs()
	ed := trafficEdge(t, EvaluateTraffic(ws), "agents/eval-agent", "tools/artifactory")
	if ed.Via != "" || ed.Bypass == 0 || ed.ExpectedWaypoint != "tools/waypoint" || !has(ed.Flags, "waypoint-bypass") || len(ed.Runbook) == 0 {
		t.Fatalf("%+v", ed)
	}
}

func TestJWTPrincipalIsNotWorkloadIdentity(t *testing.T) {
	for _, action := range []string{"ALLOW", "DENY"} {
		t.Run(action, func(t *testing.T) {
			ap := o("AuthorizationPolicy", "tools", "jwt", nil, map[string]any{"spec": map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "artifactory"}}, "action": action, "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"requestPrincipals": []any{"https://issuer.example/*"}}}}}}}})
			ws := incidentWith(t, "", []map[string]any{ap})
			x, err := Explain(ws, "c", "agents/eval-agent", "tools/artifactory")
			if err != nil || x.Verdict != Unknown || x.Fix != "" {
				t.Fatalf("%v %+v", err, x)
			}
		})
	}
}

func TestJWTSourceAlternativesKeepUnconditionalAllowsAndIgnoreKnownMismatches(t *testing.T) {
	c := newCluster(incident(t, ""), "c")
	src := c.findWorkload("agents/eval-agent")
	jwt := map[string]any{"source": map[string]any{"requestPrincipals": []any{"https://issuer.example/*"}}}
	namespace := map[string]any{"source": map[string]any{"namespaces": []any{"agents"}}}
	matched, unknown, _ := sourceMatches([]any{jwt, namespace}, src)
	if !matched || unknown {
		t.Fatal("unconditional source alternative became JWT-dependent")
	}
	matched, unknown, _ = sourceMatches([]any{map[string]any{"source": map[string]any{"requestPrincipals": []any{"*"}, "namespaces": []any{"other"}}}}, src)
	if matched || unknown {
		t.Fatal("known namespace mismatch was treated as a potential JWT match")
	}
}

func TestRequestAuthenticationExplainsOptionalToken(t *testing.T) {
	ws := incident(t, "")
	ra := o("RequestAuthentication", "tools", "jwt", nil, map[string]any{"spec": map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "artifactory"}}, "jwtRules": []any{map[string]any{"issuer": "https://issuer.example", "jwksUri": "https://issuer.example/keys"}}}})
	ws.AddObjects("c", bundle.APIResource{Group: "security.istio.io", Version: "v1", Resource: "requestauthentications", Kind: "RequestAuthentication", Namespaced: true}, []map[string]any{ra}, "jwt")
	x, err := Explain(ws, "c", "agents/eval-agent", "tools/artifactory")
	if err != nil || x.Verdict != Unknown {
		t.Fatalf("%v %+v", err, x)
	}
	jwtIndex, authzIndex := -1, -1
	for i, step := range x.Steps {
		if step.Point == "waypoint JWT authentication" && strings.Contains(step.Detail, "absent token is permitted") {
			jwtIndex = i
		}
		if strings.HasPrefix(step.Point, "waypoint ") && strings.Contains(step.Point, "authorization") {
			authzIndex = i
		}
	}
	if jwtIndex < 0 || authzIndex < jwtIndex {
		t.Fatal(x.Steps)
	}
}

func TestExplainerRepairScopeRejectsUncapturedPortsAndL4Paths(t *testing.T) {
	ws := trafficWS(t)
	for _, scope := range []RepairScope{{Ports: []string{"9999"}}, {Paths: []string{"/private/*"}}} {
		if _, err := ExplainScoped(ws, "c", "agents/eval-agent", "tools/postgres", scope); err == nil {
			t.Fatal("unsafe L4 repair scope was accepted")
		}
	}
	x, err := ExplainScoped(ws, "c", "agents/eval-agent", "tools/postgres", RepairScope{Ports: []string{"5432"}})
	if err != nil || x.FixScope == nil || !strings.Contains(x.Fix, `"5432"`) {
		t.Fatalf("%v %+v", err, x)
	}
	ap := o("AuthorizationPolicy", "tools", "deny-unless-allowed", nil, map[string]any{"spec": map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "artifactory"}}}})
	wp := incidentWith(t, "", []map[string]any{ap})
	x, err = ExplainScoped(wp, "c", "agents/eval-agent", "tools/artifactory", RepairScope{Ports: []string{"8081"}, Paths: []string{"/private/*"}})
	if err != nil || !strings.Contains(x.Fix, "/private/*") || !strings.Contains(x.Fix, "targetRefs:") {
		t.Fatalf("%v %+v", err, x)
	}
}
