package posture

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	collectpolicy "github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

func res(group, resource, kind string, namespaced bool) bundle.APIResource {
	return bundle.APIResource{Group: group, Version: "v1", Resource: resource, Kind: kind, Namespaced: namespaced}
}

func obj(kind, ns, name string, lbl map[string]any, spec map[string]any) map[string]any {
	return map[string]any{"kind": kind, "metadata": map[string]any{"name": name, "namespace": ns, "labels": lbl}, "spec": spec}
}

func complete(ws *store.Workspace, r bundle.APIResource) {
	ws.SetCoverage(bundle.Coverage{ID: "c." + r.Resource, Cluster: "c", Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, State: bundle.StateComplete})
}

func fixture(t *testing.T) *store.Workspace {
	ws := store.New()
	p, _ := collectpolicy.Profile("standard")
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	be := res("agentgateway.dev", "agentgatewaybackends", "AgentgatewayBackend", true)
	pol := res("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true)
	gw := res("gateway.networking.k8s.io", "gateways", "Gateway", true)
	rt := res("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true)
	ns := res("", "namespaces", "Namespace", false)
	pods := res("", "pods", "Pod", true)
	pa := res("security.istio.io", "peerauthentications", "PeerAuthentication", true)
	ap := res("security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true)
	for _, r := range []bundle.APIResource{be, pol, gw, rt, ns, pods, pa, ap} {
		complete(ws, r)
	}
	ws.AddObjects("c", ns, []map[string]any{obj("Namespace", "", "ai", map[string]any{"istio.io/dataplane-mode": "ambient"}, nil), obj("Namespace", "", "shop", nil, nil)}, "n")
	ws.AddObjects("c", pods, []map[string]any{
		obj("Pod", "istio-system", "ztunnel-x", map[string]any{"app": "ztunnel"}, nil),
		obj("Pod", "ai", "agent-1", nil, nil), obj("Pod", "shop", "cart-1", nil, nil),
		obj("Pod", "ai", "wp-1", map[string]any{"istio.io/dataplane-mode": "none", "gateway.networking.k8s.io/gateway-name": "wp"}, nil),
	}, "p")
	ws.AddObjects("c", gw, []map[string]any{obj("Gateway", "ai", "edge", nil, map[string]any{"gatewayClassName": "agentgateway"}),
		obj("Gateway", "ai", "wp", nil, map[string]any{"gatewayClassName": "enterprise-agentgateway-waypoint"})}, "g")
	mcp := map[string]any{"mcp": map[string]any{"targets": []any{map[string]any{"name": "t", "static": map[string]any{"host": "tools.ai.svc.cluster.local", "port": 80}}}}}
	ws.AddObjects("c", be, []map[string]any{obj("AgentgatewayBackend", "ai", "open-mcp", nil, mcp), obj("AgentgatewayBackend", "ai", "safe-mcp", nil, mcp),
		obj("AgentgatewayBackend", "ai", "mesh-mcp", nil, mcp), obj("AgentgatewayBackend", "ai", "idle-mcp", nil, mcp)}, "b")
	route := func(name, backend, parentKind, parent string) map[string]any {
		return obj("HTTPRoute", "ai", name, nil, map[string]any{"parentRefs": []any{map[string]any{"kind": parentKind, "name": parent}},
			"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"group": "agentgateway.dev", "kind": "AgentgatewayBackend", "name": backend}}}}})
	}
	ws.AddObjects("c", rt, []map[string]any{route("r-open", "open-mcp", "Gateway", "edge"), route("r-safe", "safe-mcp", "Gateway", "edge"), route("r-mesh", "mesh-mcp", "Service", "tools")}, "r")
	ref := func(kind, name string) []any {
		return []any{map[string]any{"group": map[string]string{"HTTPRoute": "gateway.networking.k8s.io", "AgentgatewayBackend": "agentgateway.dev"}[kind], "kind": kind, "name": name}}
	}
	ws.AddObjects("c", pol, []map[string]any{
		obj("AgentgatewayPolicy", "ai", "jwt", nil, map[string]any{"targetRefs": ref("HTTPRoute", "r-safe"),
			"traffic": map[string]any{"jwtAuthentication": map[string]any{"mode": "Strict", "providers": []any{map[string]any{"issuer": "idp", "audiences": []any{"mcp"}}}}}}),
		obj("AgentgatewayPolicy", "ai", "per-agent", nil, map[string]any{"targetRefs": ref("AgentgatewayBackend", "mesh-mcp"),
			"backend": map[string]any{"mcp": map[string]any{"authorization": map[string]any{"action": "Allow", "policy": map[string]any{"matchExpressions": []any{`source.identity.serviceAccount == "a" && mcp.tool.name in ["x"]`}}}}}}),
	}, "pl")
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete; s.Verified = true })
	return ws
}

func find(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestPosture(t *testing.T) {
	r := Evaluate(fixture(t))
	auth := find(r, "agentic.mcp-auth-required")
	got := map[string]Status{}
	for _, it := range auth.Items {
		got[it.Name] = it.Status
	}
	if got["open-mcp"] != Fail || got["safe-mcp"] != Pass || got["mesh-mcp"] != Pass || auth.Status != Fail {
		t.Fatalf("mcp auth: %+v", got)
	}
	if _, listed := got["idle-mcp"]; listed {
		t.Fatal("a backend no route exposes was judged as open")
	}
	if !strings.Contains(auth.Items[0].Detail, "OPEN") {
		t.Fatalf("open endpoint not called out first: %+v", auth.Items[0])
	}
	if c := find(r, "agentic.mcp-tool-authorization"); c.Passed != 1 || c.Total != 4 {
		t.Fatalf("tool authz %d/%d", c.Passed, c.Total)
	}
	if c := find(r, "agentic.prompt-guard-request"); c.Status != NA {
		t.Fatalf("no AI backends: prompt guard must be n/a, got %s", c.Status)
	}
	enrolled := find(r, "ambient.namespace-enrolled")
	if enrolled.Passed != 1 || enrolled.Total != 2 {
		t.Fatalf("enrolled %d/%d %+v", enrolled.Passed, enrolled.Total, enrolled.Items)
	}
	if c := find(r, "ambient.no-opt-out"); c.Status != Pass {
		t.Fatalf("waypoint pods opt out by design: %+v", c.Items)
	}
	if c := find(r, "ambient.mtls-strict"); c.Status != Fail || !strings.Contains(c.Items[0].Detail, "PERMISSIVE") {
		t.Fatalf("mtls: %+v", c)
	}
	if c := find(r, "ambient.default-deny"); c.Status != Fail {
		t.Fatalf("default deny: %+v", c)
	}
	for _, a := range r.Areas {
		if !a.Present || a.Score <= 0 || a.Score >= 100 || a.Grade == "" {
			t.Fatalf("area %+v", a)
		}
	}
}

func TestFixesRaiseTheScore(t *testing.T) {
	ws := fixture(t)
	before := Evaluate(ws).Areas[1].Score
	pa := res("security.istio.io", "peerauthentications", "PeerAuthentication", true)
	ap := res("security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true)
	ws.AddObjects("c", pa, []map[string]any{obj("PeerAuthentication", "istio-system", "default", nil, map[string]any{"mtls": map[string]any{"mode": "STRICT"}})}, "pa")
	ws.AddObjects("c", ap, []map[string]any{obj("AuthorizationPolicy", "ai", "default-deny", nil, map[string]any{})}, "ap")
	r := Evaluate(ws)
	if find(r, "ambient.mtls-strict").Status != Pass || find(r, "ambient.default-deny").Status != Pass || r.Areas[1].Score <= before {
		t.Fatalf("fixes did not register: %d -> %d", before, r.Areas[1].Score)
	}
}

func TestExcludedDataIsNotAFailure(t *testing.T) {
	ws := store.New()
	p, _ := collectpolicy.Profile("standard")
	p.ExcludeResources = []string{"agentgatewaypolicies.agentgateway.dev"}
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	be := res("agentgateway.dev", "agentgatewaybackends", "AgentgatewayBackend", true)
	ws.RegisterResources("c", []bundle.APIResource{res("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true)})
	ws.AddObjects("c", be, []map[string]any{obj("AgentgatewayBackend", "ai", "m", nil, map[string]any{"mcp": map[string]any{}})}, "b")
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete })
	c := find(Evaluate(ws), "agentic.mcp-tool-authorization")
	if c.Status != NA || !strings.Contains(c.Reason, "not captured") {
		t.Fatalf("excluded policies must make the check n/a: %+v", c)
	}
}

func TestSettingEnvIsNotACredential(t *testing.T) {
	for _, n := range []string{"TOKEN_PASSTHROUGH", "API_KEY_FILE", "SECRET_NAME", "TOKEN_URL", "AUTH_TOKEN_ENABLED"} {
		if !settingEnv.MatchString(n) {
			t.Errorf("%s should be treated as a setting", n)
		}
	}
	for _, n := range []string{"MODEL_API_KEY", "GITHUB_TOKEN", "DB_PASSWORD", "CLIENT_SECRET"} {
		if !sensitiveEnv.MatchString(n) || settingEnv.MatchString(n) {
			t.Errorf("%s should be treated as a credential", n)
		}
	}
}
