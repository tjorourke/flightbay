package posture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	collectpolicy "github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

// Resources used by the agentic fixtures.
var (
	rBackend  = res("agentgateway.dev", "agentgatewaybackends", "AgentgatewayBackend", true)
	rPolicy   = res("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true)
	rGateway  = res("gateway.networking.k8s.io", "gateways", "Gateway", true)
	rRoute    = res("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true)
	rService  = res("", "services", "Service", true)
	rSecret   = res("", "secrets", "Secret", true)
	rAgent    = res("kagent.dev", "agents", "Agent", true)
	rModelCfg = res("kagent.dev", "modelconfigs", "ModelConfig", true)
	rRemote   = res("kagent.dev", "remotemcpservers", "RemoteMCPServer", true)
)

// xws is an empty, completely captured workspace with one agentgateway
// Gateway "edge" in namespace ai. exclude lists resources the customer left out.
func xws(t *testing.T, exclude ...string) *store.Workspace {
	t.Helper()
	ws := store.New()
	p, _ := collectpolicy.Profile("standard")
	p.ExcludeResources = exclude
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	all := []bundle.APIResource{rBackend, rPolicy, rGateway, rRoute, rService, rSecret, rAgent, rModelCfg, rRemote}
	ws.RegisterResources("c", all)
	for _, r := range all {
		excluded := false
		for _, x := range exclude {
			if x == r.Resource+"."+r.Group || x == r.Resource {
				excluded = true
			}
		}
		if !excluded {
			complete(ws, r)
		}
	}
	ws.AddObjects("c", rGateway, []map[string]any{obj("Gateway", "ai", "edge", nil, map[string]any{"gatewayClassName": "agentgateway",
		"listeners": []any{map[string]any{"name": "http", "port": 80, "protocol": "HTTP"}}})}, "gw")
	return ws
}

func add(ws *store.Workspace, r bundle.APIResource, objs ...map[string]any) {
	ws.AddObjects("c", r, objs, r.Resource+"-"+objs[0]["metadata"].(map[string]any)["name"].(string))
}

func withStatus(o map[string]any, st map[string]any) map[string]any { o["status"] = st; return o }

func tref(group, kind, name string) []any {
	return []any{map[string]any{"group": group, "kind": kind, "name": name}}
}

func routeTo(name, kind, group, backend string) map[string]any {
	return obj("HTTPRoute", "ai", name, nil, map[string]any{"parentRefs": []any{map[string]any{"name": "edge"}},
		"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"group": group, "kind": kind, "name": backend}}}}})
}

var jwtTraffic = map[string]any{"jwtAuthentication": map[string]any{"mode": "Strict", "providers": []any{map[string]any{"issuer": "idp", "audiences": []any{"a"}}}}}

func run(t *testing.T, ws *store.Workspace, id string) (Check, map[string]Status) {
	t.Helper()
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete; s.Verified = true })
	c := find(Evaluate(ws), id)
	if c.ID == "" {
		t.Fatalf("check %s not produced", id)
	}
	got := map[string]Status{}
	for _, it := range c.Items {
		got[it.Name] = it.Status
	}
	return c, got
}

func expect(t *testing.T, id string, got map[string]Status, want map[string]Status) {
	t.Helper()
	for n, st := range want {
		if got[n] != st {
			t.Errorf("%s: %s = %q, want %q (all: %v)", id, n, got[n], st, got)
		}
	}
}

func TestA2AAuth(t *testing.T) {
	ws := xws(t)
	a2aSvc := func(name string) map[string]any {
		return obj("Service", "ai", name, nil, map[string]any{"ports": []any{map[string]any{"port": 8080, "appProtocol": "kgateway.dev/a2a"}}})
	}
	add(ws, rService, a2aSvc("open-agent"), a2aSvc("safe-agent"), a2aSvc("rejected-agent"), a2aSvc("mesh-agent"))
	add(ws, rRoute, routeTo("r-open", "Service", "", "open-agent"), routeTo("r-safe", "Service", "", "safe-agent"), routeTo("r-rej", "Service", "", "rejected-agent"),
		obj("HTTPRoute", "ai", "r-mesh", nil, map[string]any{"parentRefs": []any{map[string]any{"kind": "Service", "name": "mesh-agent"}},
			"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "mesh-agent"}}}}}))
	notAccepted := map[string]any{"ancestors": []any{map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "Invalid"}}}}}
	add(ws, rPolicy,
		obj("AgentgatewayPolicy", "ai", "jwt", nil, map[string]any{"targetRefs": tref("gateway.networking.k8s.io", "HTTPRoute", "r-safe"), "traffic": jwtTraffic}),
		withStatus(obj("AgentgatewayPolicy", "ai", "jwt-rejected", nil, map[string]any{"targetRefs": tref("gateway.networking.k8s.io", "HTTPRoute", "r-rej"), "traffic": jwtTraffic}), notAccepted))
	c, got := run(t, ws, "agentic.a2a-auth-required")
	expect(t, c.ID, got, map[string]Status{"open-agent": Fail, "safe-agent": Pass, "rejected-agent": Fail})
	if _, ok := got["mesh-agent"]; ok {
		t.Error("an in-mesh (Service parent) route is not north-south exposure")
	}
	// The rejected policy is reported by the status check.
	c, got = run(t, ws, "agentic.policy-accepted-and-attached")
	expect(t, c.ID, got, map[string]Status{"jwt": Pass, "jwt-rejected": Fail})
}

func TestA2ANotApplicable(t *testing.T) {
	ws := xws(t)
	add(ws, rAgent, obj("Agent", "ai", "a", nil, map[string]any{"type": "BYO"}))
	if c, _ := run(t, ws, "agentic.a2a-auth-required"); c.Status != NA {
		t.Fatalf("nothing exposed: want n/a, got %+v", c)
	}
}

func TestAuthModeEnforcing(t *testing.T) {
	ws := xws(t)
	add(ws, rPolicy,
		obj("AgentgatewayPolicy", "ai", "lax", nil, map[string]any{"traffic": map[string]any{"apiKeyAuthentication": map[string]any{"mode": "Permissive", "secretRef": map[string]any{"name": "k"}}}}),
		obj("AgentgatewayPolicy", "ai", "strict", nil, map[string]any{"traffic": map[string]any{"apiKeyAuthentication": map[string]any{"secretRef": map[string]any{"name": "k"}}}}))
	c, got := run(t, ws, "agentic.auth-mode-enforcing")
	expect(t, c.ID, got, map[string]Status{"lax": Fail, "strict": Pass})
}

func TestAuthModeExcludedIsNA(t *testing.T) {
	ws := xws(t, "agentgatewaypolicies.agentgateway.dev")
	add(ws, rBackend, obj("AgentgatewayBackend", "ai", "m", nil, map[string]any{"mcp": map[string]any{}}))
	for _, id := range []string{"agentic.auth-mode-enforcing", "agentic.policy-accepted-and-attached", "agentic.cors-safe", "agentic.enforcement-fails-closed"} {
		if c, _ := run(t, ws, id); c.Status != NA || !strings.Contains(c.Reason, "not captured") {
			t.Errorf("%s: excluded policies must make it n/a: %+v", id, c)
		}
	}
}

func mcpBackend(name string, targetPolicies map[string]any) map[string]any {
	st := map[string]any{"host": "tools.example.com", "port": 443}
	if targetPolicies != nil {
		st["policies"] = targetPolicies
	}
	return obj("AgentgatewayBackend", "ai", name, nil, map[string]any{"mcp": map[string]any{"targets": []any{map[string]any{"name": "t", "static": st}}}})
}

func TestNoTokenPassthrough(t *testing.T) {
	ws := xws(t)
	add(ws, rBackend, mcpBackend("pass", map[string]any{"auth": map[string]any{"passthrough": map[string]any{}}}),
		mcpBackend("keyed", map[string]any{"auth": map[string]any{"secretRef": map[string]any{"name": "pat"}}}))
	c, got := run(t, ws, "agentic.no-token-passthrough")
	expect(t, c.ID, got, map[string]Status{"pass": Fail, "keyed": Pass})
}

func TestNoInlineCRDCredentials(t *testing.T) {
	ws := xws(t)
	add(ws, rBackend,
		obj("AgentgatewayBackend", "ai", "inline", nil, map[string]any{"ai": map[string]any{}, "policies": map[string]any{"auth": map[string]any{"key": "[REDACTED]"}}}),
		obj("AgentgatewayBackend", "ai", "ref", nil, map[string]any{"ai": map[string]any{}, "policies": map[string]any{"auth": map[string]any{"secretRef": map[string]any{"name": "k"}}}}))
	add(ws, rRemote, obj("RemoteMCPServer", "ai", "hdr", nil, map[string]any{"url": "https://x", "headersFrom": []any{map[string]any{"name": "Authorization", "value": "Bearer [REDACTED]"}}}))
	c, got := run(t, ws, "agentic.no-inline-crd-credentials")
	expect(t, c.ID, got, map[string]Status{"inline": Fail, "ref": Pass, "hdr": Fail})
}

func secret(ns, name string, keys ...string) map[string]any {
	var ks []any
	for _, k := range keys {
		ks = append(ks, map[string]any{"name": k, "bytes": 10})
	}
	return map[string]any{"kind": "Secret", "metadata": map[string]any{"name": name, "namespace": ns}, "x-flightbay": map[string]any{"keys": ks}}
}

func TestCredentialRefsResolve(t *testing.T) {
	ws := xws(t)
	add(ws, rSecret, secret("ai", "good", "Authorization"), secret("ai", "llm", "OPENAI_KEY"))
	ref := func(name, sec, key string) map[string]any {
		sr := map[string]any{"name": sec}
		if key != "" {
			sr["key"] = key
		}
		return obj("AgentgatewayBackend", "ai", name, nil, map[string]any{"ai": map[string]any{}, "policies": map[string]any{"auth": map[string]any{"secretRef": sr}}})
	}
	add(ws, rBackend, ref("ok", "good", ""), ref("gone", "missing", ""), ref("nokey", "good", "token"))
	add(ws, rModelCfg,
		obj("ModelConfig", "ai", "mc-redacted", nil, map[string]any{"provider": "OpenAI", "apiKeySecret": "[REDACTED]", "apiKeySecretKey": "OPENAI_KEY"}),
		obj("ModelConfig", "ai", "mc-missing", nil, map[string]any{"provider": "OpenAI", "apiKeySecret": "nope", "apiKeySecretKey": "OPENAI_KEY"}))
	c, got := run(t, ws, "agentic.credential-refs-resolve")
	expect(t, c.ID, got, map[string]Status{"ok": Pass, "gone": Fail, "nokey": Fail, "mc-redacted": Pass, "mc-missing": Fail})

	ws = xws(t, "secrets")
	add(ws, rBackend, ref("ok", "good", ""))
	if c, _ := run(t, ws, "agentic.credential-refs-resolve"); c.Status != NA {
		t.Fatalf("secrets not captured: want n/a, got %+v", c)
	}
}

func TestPolicyAcceptedAndAttached(t *testing.T) {
	ws := xws(t)
	add(ws, rRoute, routeTo("r", "Service", "", "svc"))
	add(ws, rPolicy,
		obj("AgentgatewayPolicy", "ai", "dangling", nil, map[string]any{"targetRefs": tref("gateway.networking.k8s.io", "HTTPRoute", "no-such-route")}),
		obj("AgentgatewayPolicy", "ai", "fine", nil, map[string]any{"targetRefs": tref("gateway.networking.k8s.io", "HTTPRoute", "r")}),
		withStatus(obj("AgentgatewayPolicy", "ai", "detached", nil, map[string]any{"targetRefs": tref("gateway.networking.k8s.io", "HTTPRoute", "r")}),
			map[string]any{"ancestors": []any{map[string]any{"conditions": []any{map[string]any{"type": "Attached", "status": "False"}}}}}))
	c, got := run(t, ws, "agentic.policy-accepted-and-attached")
	expect(t, c.ID, got, map[string]Status{"dangling": Fail, "fine": Pass, "detached": Fail})
}

func TestAgentReferencesResolve(t *testing.T) {
	ws := xws(t)
	add(ws, rModelCfg, obj("ModelConfig", "ai", "mc", nil, map[string]any{"provider": "Anthropic"}))
	add(ws, rRemote, withStatus(obj("RemoteMCPServer", "ai", "tools", nil, map[string]any{"url": "http://tools.ai:8084/mcp"}),
		map[string]any{"discoveredTools": []any{map[string]any{"name": "get_pods"}}}))
	tool := func(names ...any) []any {
		return []any{map[string]any{"type": "McpServer", "mcpServer": map[string]any{"kind": "RemoteMCPServer", "name": "tools", "toolNames": names}}}
	}
	add(ws, rAgent,
		obj("Agent", "ai", "good", nil, map[string]any{"declarative": map[string]any{"modelConfig": "mc", "tools": tool("get_pods")}}),
		obj("Agent", "ai", "no-model", nil, map[string]any{"declarative": map[string]any{"modelConfig": "missing"}}),
		obj("Agent", "ai", "no-tool", nil, map[string]any{"declarative": map[string]any{"modelConfig": "mc", "tools": tool("delete_pods")}}))
	c, got := run(t, ws, "agentic.agent-references-resolve")
	expect(t, c.ID, got, map[string]Status{"good": Pass, "no-model": Fail, "no-tool": Fail})
}

func TestCORSSafe(t *testing.T) {
	ws := xws(t)
	add(ws, rPolicy,
		obj("AgentgatewayPolicy", "ai", "open", nil, map[string]any{"traffic": map[string]any{"cors": map[string]any{"allowOrigins": []any{"*"}, "allowCredentials": true}}}),
		obj("AgentgatewayPolicy", "ai", "named", nil, map[string]any{"traffic": map[string]any{"cors": map[string]any{"allowOrigins": []any{"https://portal.example.com"}, "allowCredentials": true}}}))
	c, got := run(t, ws, "agentic.cors-safe")
	expect(t, c.ID, got, map[string]Status{"open": Fail, "named": Pass})
}

func TestEnforcementFailsClosed(t *testing.T) {
	ws := xws(t)
	ext := func(mode string) map[string]any {
		return map[string]any{"extAuth": map[string]any{"backendRef": map[string]any{"name": "authz"}, "grpc": map[string]any{}, "failureMode": mode}}
	}
	add(ws, rPolicy, obj("AgentgatewayPolicy", "ai", "open", nil, map[string]any{"traffic": ext("FailOpen")}),
		obj("AgentgatewayPolicy", "ai", "closed", nil, map[string]any{"traffic": ext("FailClosed")}))
	c, got := run(t, ws, "agentic.enforcement-fails-closed")
	expect(t, c.ID, got, map[string]Status{"open": Fail, "closed": Pass})
}

func TestUpstreamTLSVerified(t *testing.T) {
	ws := xws(t)
	add(ws, rBackend, obj("AgentgatewayBackend", "ai", "skip", nil, map[string]any{"ai": map[string]any{}, "policies": map[string]any{"tls": map[string]any{"insecureSkipVerify": "All"}}}),
		obj("AgentgatewayBackend", "ai", "verified", nil, map[string]any{"ai": map[string]any{}, "policies": map[string]any{"tls": map[string]any{"sni": "api.openai.com"}}}))
	add(ws, rModelCfg, obj("ModelConfig", "ai", "mc", nil, map[string]any{"provider": "OpenAI", "tls": map[string]any{"disableVerify": true}}))
	c, got := run(t, ws, "agentic.upstream-tls-verified")
	expect(t, c.ID, got, map[string]Status{"skip": Fail, "verified": Pass, "mc": Fail})
}

func TestLLMSpendControlled(t *testing.T) {
	ws := xws(t)
	ai := map[string]any{"ai": map[string]any{"provider": map[string]any{"openai": map[string]any{}}}}
	add(ws, rBackend, obj("AgentgatewayBackend", "ai", "capped", nil, ai), obj("AgentgatewayBackend", "ai", "open", nil, ai))
	add(ws, rRoute, routeTo("r-capped", "AgentgatewayBackend", "agentgateway.dev", "capped"), routeTo("r-open", "AgentgatewayBackend", "agentgateway.dev", "open"))
	add(ws, rPolicy, obj("AgentgatewayPolicy", "ai", "tokens", nil, map[string]any{"targetRefs": tref("gateway.networking.k8s.io", "HTTPRoute", "r-capped"),
		"traffic": map[string]any{"rateLimit": map[string]any{"local": []any{map[string]any{"tokens": 1000, "unit": "Minutes"}}}}}))
	c, got := run(t, ws, "agentic.llm-spend-controlled")
	expect(t, c.ID, got, map[string]Status{"capped": Pass, "open": Fail})
	// The token limit also counts for the older rate-limit check.
	if _, rl := run(t, ws, "agentic.rate-limit"); rl["capped"] != Pass || rl["open"] != Fail {
		t.Errorf("rate-limit: %v", rl)
	}
}

func TestDynamicForwardProxy(t *testing.T) {
	ws := xws(t)
	dfp := map[string]any{"dynamicForwardProxy": map[string]any{}}
	add(ws, rBackend, obj("AgentgatewayBackend", "ai", "open-proxy", nil, dfp), obj("AgentgatewayBackend", "ai", "egress", nil, dfp))
	add(ws, rRoute, routeTo("r-open", "AgentgatewayBackend", "agentgateway.dev", "open-proxy"), routeTo("r-egress", "AgentgatewayBackend", "agentgateway.dev", "egress"))
	add(ws, rPolicy, obj("AgentgatewayPolicy", "ai", "egress", nil, map[string]any{"targetRefs": tref("gateway.networking.k8s.io", "HTTPRoute", "r-egress"),
		"traffic": map[string]any{"jwtAuthentication": jwtTraffic["jwtAuthentication"], "authorization": map[string]any{"action": "Allow",
			"policy": map[string]any{"matchExpressions": []any{`request.host in ["api.openai.com"]`}}}}}))
	c, got := run(t, ws, "agentic.dynamic-forward-proxy-restricted")
	expect(t, c.ID, got, map[string]Status{"open-proxy": Fail, "egress": Pass})
}

func TestAccessPolicyNAWhenNotInstalled(t *testing.T) {
	ws := xws(t)
	add(ws, rAgent, obj("Agent", "ai", "a", nil, map[string]any{"type": "BYO"}))
	if c, _ := run(t, ws, "agentic.kagent-accesspolicy-baseline"); c.Status != NA {
		t.Fatalf("AccessPolicy CRD absent: want n/a, got %+v", c)
	}
}

// richFixture has something for every area, so Evaluate produces its full
// set of checks.
func richFixture(t *testing.T) *store.Workspace {
	ws := fixture(t)
	add(ws, rAgent, obj("Agent", "ai", "a", nil, map[string]any{"declarative": map[string]any{"modelConfig": "mc"}}))
	add(ws, rModelCfg, obj("ModelConfig", "ai", "mc", nil, map[string]any{"provider": "OpenAI"}))
	add(ws, rRemote, obj("RemoteMCPServer", "ai", "tools", nil, map[string]any{"url": "http://tools.ai:8084/mcp"}))
	return ws
}

// Every guide a check links to must exist in the solo-demos catalogue.
func TestGuideSlugsExist(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, "code", "solo", "solo-demos")
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		t.Skip("solo-demos is not checked out")
	}
	r := Evaluate(richFixture(t))
	if len(r.Checks) == 0 {
		t.Fatal("no checks produced")
	}
	for _, c := range r.Checks {
		slug := ""
		for _, base := range []string{kbBase, meshBase} {
			if strings.HasPrefix(c.Source, base) {
				slug = strings.Trim(strings.TrimPrefix(c.Source, base), "/")
			}
		}
		if slug == "" {
			// Upstream references are allowed where no Solo guide covers it.
			if !strings.HasPrefix(c.Source, "https://kubernetes.io/") && !strings.HasPrefix(c.Source, "https://istio.io/") &&
				!strings.HasPrefix(c.Source, "https://helm.sh/") && !strings.HasPrefix(c.Source, "https://cert-manager.io/") {
				t.Errorf("%s: source %q is neither a guide link nor an upstream reference", c.ID, c.Source)
			}
			continue
		}
		if st, err := os.Stat(filepath.Join(root, slug)); err != nil || !st.IsDir() {
			t.Errorf("%s: guide %q is not a directory in solo-demos", c.ID, slug)
		}
	}
}
