package ambient

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func routeSchema(ws *store.Workspace, fields ...[]string) {
	root := map[string]any{"type": "object", "properties": map[string]any{}}
	for _, path := range fields {
		n := root
		for _, k := range append([]string{"spec", "rules"}, path...) {
			props := M(n["properties"])
			if props[k] == nil {
				props[k] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			n = M(props[k])
			if k == "rules" || k == "filters" || k == "matches" || k == "headers" {
				n["type"] = "array"
				if n["items"] == nil {
					n["items"] = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				n = M(n["items"])
			}
		}
	}
	crd := object("CustomResourceDefinition", "httproutes.gateway.networking.k8s.io", map[string]any{"versions": []any{map[string]any{"name": "v1", "served": true, "schema": map[string]any{"openAPIV3Schema": root}}}})
	M(crd["metadata"])["namespace"] = ""
	ws.AddCRD("c", crd)
}

func TestP1RouteGeneratorsRequireInstalledSchema(t *testing.T) {
	for _, tc := range []struct {
		id   string
		path []string
		req  Request
		want string
	}{
		{"retries", []string{"retry", "attempts"}, Request{RetryAttempts: 2}, "attempts: 2"},
		{"mirroring", []string{"filters", "requestMirror", "backendRef", "name"}, Request{Mirror: &Backend{Name: "canary", Port: 80}}, "type: RequestMirror"},
		{"header-routing", []string{"matches", "headers", "value"}, Request{Headers: map[string]string{"x-version": "canary"}, Backends: []Backend{{Name: "canary", Port: 80}}}, "type: Exact"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			ws := fixture(t)
			req := tc.req
			req.Cluster = "c"
			req.Namespace = "app"
			req.Service = "front"
			req.Port = 80
			req.Tasks = []string{tc.id}
			if _, err := Generate(ws, req); err == nil {
				t.Fatal("missing schema enabled task")
			}
			routeSchema(ws, tc.path)
			p, err := Generate(ws, req)
			if err != nil || !strings.Contains(p.YAML, tc.want) {
				t.Fatalf("%v %+v", err, p)
			}
			wp := Target(ws, "c", "Gateway", "app", "wp")
			M(wp.Obj["spec"])["gatewayClassName"] = "enterprise-agentgateway-waypoint"
			if tc.id != "header-routing" {
				if _, err := Generate(ws, req); err == nil {
					t.Fatal("unverified agentgateway task enabled")
				}
			}
		})
	}
}

func TestRequireWaypointNeedsCapturedIdentityAndTrustDomain(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"require-waypoint"}}
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("invented waypoint principal")
	}
	pod := object("Pod", "wp-1", map[string]any{"serviceAccountName": "waypoint-sa"})
	M(pod["metadata"])["labels"] = map[string]any{"gateway.networking.k8s.io/gateway-name": "wp"}
	put(ws, "", "pods", "Pod", pod)
	cm := object("ConfigMap", "istio", nil)
	cm["data"] = map[string]any{"mesh": "trustDomain: mesh.example\n"}
	put(ws, "", "configmaps", "ConfigMap", cm)
	p, err := Generate(ws, req)
	if err != nil || !strings.Contains(p.YAML, "mesh.example/ns/app/sa/waypoint-sa") || strings.Contains(p.YAML, "targetRefs") {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestServiceWaypointBoundAndRunbook(t *testing.T) {
	ws := fixture(t)
	svc := Target(ws, "c", "Service", "app", "front")
	svc.Obj["status"] = map[string]any{"conditions": []any{map[string]any{"type": "istio.io/WaypointBound", "status": "False", "observedGeneration": 1, "reason": "WaypointNotFound"}}}
	r, err := NewView(ws, "c", "Service", "app", "front", "")
	if err != nil {
		t.Fatal(err)
	}
	s := findStep(t, r.Investigation, "waypoint-bound-istio.io/WaypointBound")
	if s.State != "fail" || s.SuggestedFix == nil || s.SuggestedFix.ID != "waypoint-bound" || len(s.Runbook) == 0 {
		t.Fatalf("%+v", s)
	}
	M(svc.Obj["metadata"])["generation"] = 2
	r, _ = NewView(ws, "c", "Service", "app", "front", "")
	if findStep(t, r.Investigation, s.ID).State != "unknown" {
		t.Fatal("stale binding failed")
	}
}

func TestJWTGeneratorAndInvestigation(t *testing.T) {
	ws := fixture(t)
	put(ws, "security.istio.io", "requestauthentications", "RequestAuthentication")
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"jwt"}, Issuer: "https://issuer.example", JWKSURI: "https://issuer.example/keys", RequestPrincipals: []string{"https://issuer.example/alice"}, Paths: []string{"/private/*"}, Methods: []string{"GET"}, Headers: map[string]string{"x-team": "sre"}}
	p, err := Generate(ws, req)
	if err != nil || len(p.Objects) != 2 || !strings.Contains(p.YAML, "request.headers[x-team]") || !strings.Contains(p.YAML, "/private/*") {
		t.Fatalf("%v %+v", err, p)
	}
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", p.Objects[1])
	r, _ := Investigate(ws, "c", "app", "front", "")
	if findStep(t, r, "jwt-principals-"+S(Path(p.Objects[1], "metadata", "name"))).State != "fail" {
		t.Fatal("missing validator not diagnosed")
	}
	put(ws, "security.istio.io", "requestauthentications", "RequestAuthentication", p.Objects[0])
	r, _ = Investigate(ws, "c", "app", "front", "")
	if findStep(t, r, "jwt-principals-"+S(Path(p.Objects[1], "metadata", "name"))).State != "warning" {
		t.Fatal("JWT config incorrectly proved request validity")
	}
	req.RequestPrincipals = []string{"https://other.example/*"}
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("mismatched issuer accepted")
	}
}

func TestEnterpriseChoicesRequireCorrespondingAPIs(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	gw := object("Gateway", "enterprise", map[string]any{"gatewayClassName": "enterprise-agentgateway-waypoint"})
	put(ws, GatewayGroup, "gateways", "Gateway", gw)
	pilot := object("Pod", "istiod", map[string]any{"containers": []any{map[string]any{"image": "istio/pilot:1.30.4-solo"}}})
	put(ws, "", "pods", "Pod", pilot)
	e := DetectEnvironment(ws, "c", nil)
	if len(e.Classes) != 0 || e.Edition == "enterprise" {
		t.Fatalf("Enterprise choices inferred without corresponding APIs: %+v", e)
	}
	put(ws, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy")
	put(ws, "admin.solo.io", "segments", "Segment")
	e = DetectEnvironment(ws, "c", nil)
	if len(e.Classes) != 1 || e.Edition != "enterprise" {
		t.Fatalf("installed Enterprise evidence not recognised: %+v", e)
	}
}
