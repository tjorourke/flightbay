package reach

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

func TestReadMCPRule(t *testing.T) {
	type want struct{ who, tools string }
	cases := []struct {
		name, expr string
		want       []want
	}{
		{"deny everything", `false`, nil},
		{"allow everything", `true`, []want{{"any caller", "*"}}},
		{"one agent, namespace pinned", `source.identity.namespace == "kagent" && source.identity.serviceAccount == "tom2-agent" && mcp.tool.name in ["list_issues", "search_code"]`,
			[]want{{"kagent/tom2-agent", "list_issues,search_code"}}},
		{"two agents in one rule, as the approvals page writes it", `(source.identity.serviceAccount == "a"
               && mcp.tool.name in ["x"]) || (source.identity.serviceAccount == "b" && mcp.tool.name == "y")`,
			[]want{{"service account a", "x"}, {"service account b", "y"}}},
		{"or inside brackets", `(source.identity.serviceAccount == "a" || source.identity.serviceAccount == "b") && mcp.tool.name in ["x"]`,
			[]want{{"service account a", "x"}, {"service account b", "x"}}},
		{"service accounts in a list", `source.identity.serviceAccount in ["a", "b"] && source.identity.namespace == "ns" && mcp.tool.name == "x"`,
			[]want{{"ns/a", "x"}, {"ns/b", "x"}}},
		{"caller with every tool", `source.identity.serviceAccount == "admin" && source.identity.namespace == "ops"`,
			[]want{{"ops/admin", "*"}}},
		{"namespace only", `source.identity.namespace == "kagent" && mcp.tool.name == "x"`, []want{{"namespace kagent", "x"}}},
		{"jwt subject", `jwt.sub == "alice" && mcp.tool.name == "x"`, []want{{"JWT subject alice", "x"}}},
		{"jwt claim", `jwt.team == "sre" && mcp.tool.name == "x"`, []want{{"JWT team sre", "x"}}},
		{"condition it cannot decide", `source.identity.serviceAccount == "a" && jwt.groups.exists(g, g == "admins") && mcp.tool.name == "x"`,
			[]want{{`service account a (if jwt.groups.exists(g, g == "admins"))`, "x"}}},
		{"negation is a condition", `!(mcp.tool.name in ["delete_repo"]) && source.identity.serviceAccount == "a"`,
			[]want{{`service account a (if !(mcp.tool.name in ["delete_repo"]))`, "*"}}},
		{"contradiction never holds", `mcp.tool.name == "x" && mcp.tool.name == "y"`, nil},
		{"false clause drops out", `false || (jwt.sub == "bob" && mcp.tool.name == "x")`, []want{{"JWT subject bob", "x"}}},
		{"single quotes", `source.identity.serviceAccount == 'a' && mcp.tool.name in ['x']`, []want{{"service account a", "x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := readMCPRule(tc.expr)
			if !ok {
				t.Fatalf("not read: %s", tc.expr)
			}
			var g []want
			for _, c := range got {
				tools := "*"
				if c.tools != nil {
					tools = strings.Join(c.tools, ",")
				}
				for _, w := range c.who {
					g = append(g, want{w, tools})
				}
			}
			if len(g) != len(tc.want) {
				t.Fatalf("got %+v want %+v", g, tc.want)
			}
			for i := range g {
				if g[i] != tc.want[i] {
					t.Fatalf("got %+v want %+v", g, tc.want)
				}
			}
		})
	}
	if _, ok := readMCPRule(`(source.identity.serviceAccount == "a"`); ok {
		t.Fatal("unbalanced brackets are not read")
	}
	if c, _ := readMCPRule(`source.identity.serviceAccount == "a" && mcp.tool.name == "x"`); len(c) != 1 || strings.Join(c[0].saNoNS, ",") != "a" {
		t.Fatalf("a service account without a namespace is flagged: %+v", c)
	}
}

// The approvals flow: a deny-all rule guards the server, so the agent's
// configured tools there are not allowed and not reported as unenforced;
// once its clause is added, its tools are allowed. A rule without the
// namespace is a finding; a policy that was not accepted is left out.
func TestActivityMCPRuleGuardsTheServer(t *testing.T) {
	build := func(expr string, status map[string]any) Activity {
		ws := activityWS(t, "Lists", "list_issues", "get_issue")
		body := map[string]any{"spec": map[string]any{
			"targetRefs": []any{map[string]any{"kind": "Service", "name": "github-mcp"}},
			"backend":    map[string]any{"mcp": map[string]any{"authorization": map[string]any{"action": "Allow", "policy": map[string]any{"matchExpressions": []any{expr}}}}}}}
		if status != nil {
			body["status"] = status
		}
		ws.AddObjects("c", rAgwPol, []map[string]any{o("AgentgatewayPolicy", "kagent", "github-per-agent", nil, body)}, "pa")
		return EvaluateActivity(ws, nil, "")
	}
	server := func(a Activity, n string) *ServerInfo {
		for i := range a.Servers {
			if a.Servers[i].Name == n {
				return &a.Servers[i]
			}
		}
		return nil
	}
	tool := func(a Activity, n string) *ToolInfo {
		for i := range a.Tools {
			if a.Tools[i].Server == "kagent/github" && a.Tools[i].Name == n {
				return &a.Tools[i]
			}
		}
		return nil
	}

	a := build("false", nil)
	if si := server(a, "kagent/github"); si == nil || !si.Guarded || si.Allowed != 0 {
		t.Fatalf("a false rule guards the server and allows nothing: %+v", si)
	}
	if strings.Contains(findingTitles(a.Findings), "An agent's tool list is not enforced") {
		t.Fatalf("a guarded server's tool list is enforced: %s", findingTitles(a.Findings))
	}

	a = build(`source.identity.namespace == "kagent" && source.identity.serviceAccount == "triage" && mcp.tool.name in ["github_get_issue", "get_issue"]`, nil)
	if gi := tool(a, "get_issue"); gi == nil || strings.Join(gi.AllowedFor, ",") != "kagent/triage" {
		t.Fatalf("approved: get_issue is allowed for kagent/triage: %+v", gi)
	}
	if strings.Contains(findingTitles(a.Findings), "without its namespace") {
		t.Fatalf("the namespace is pinned: %s", findingTitles(a.Findings))
	}

	a = build(`source.identity.serviceAccount == "triage" && mcp.tool.name == "get_issue"`, nil)
	if !strings.Contains(findingTitles(a.Findings), "medium: An MCP allowlist names a service account without its namespace") {
		t.Fatalf("no namespace: %s", findingTitles(a.Findings))
	}

	a = build(`source.identity.serviceAccount == "triage"`, nil)
	if si := server(a, "kagent/github"); si == nil || strings.Join(si.AllowAll, ",") != "service account triage" {
		t.Fatalf("a clause with no tool gives every tool: %+v", si)
	}
	if li := tool(a, "list_issues"); li == nil || strings.Join(li.AllowedFor, ",") != "service account triage" {
		t.Fatalf("every known tool is allowed for it: %+v", li)
	}

	rejected := map[string]any{"ancestors": []any{map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "Invalid", "message": "target not found"}}}}}
	a = build(`source.identity.serviceAccount == "triage" && mcp.tool.name == "get_issue"`, rejected)
	if si := server(a, "kagent/github"); si != nil && si.Guarded {
		t.Fatalf("a policy that was not accepted guards nothing: %+v", si)
	}
	if !strings.Contains(strings.Join(a.Notes, "\n"), "not applied") { // Accepted False is noted
		t.Fatalf("the note says why it is left out: %v", a.Notes)
	}
}

// An agent's configured MCP Service is evaluated for reach: with no
// NetworkPolicy it is reachable; a default-deny egress policy blocks it.
func TestActivityConfiguredReach(t *testing.T) {
	build := func(deny bool) Activity {
		ws := activityWS(t, "Lists", "list_issues")
		ws.AddObjects("c", rSvc, []map[string]any{svc("kagent", "kagent-tools", map[string]any{"app": "tools"}, 8084, nil)}, "s2")
		tools := pod("kagent", "kagent-tools", "kagent-tools", map[string]any{"app": "tools"}, true, nil)
		ws.AddObjects("c", rPod, []map[string]any{tools}, "p2")
		ws.AddObjects("c", rKAgent, []map[string]any{o("Agent", "kagent", "triage", nil, map[string]any{"spec": map[string]any{"byo": map[string]any{"deployment": map[string]any{"env": []any{
			map[string]any{"name": "MCP_SERVERS_CONFIG", "value": `[{"name":"k8s-sre","url":"http://kagent-tools.kagent.svc.cluster.local:8084/mcp"}]`},
			map[string]any{"name": "ALLOWED_TOOLS", "value": `["k8s_get_events"]`}}}}}})}, "a2")
		if deny {
			ws.SetCoverage(bundle.Coverage{ID: "c.np", Cluster: "c", Type: bundle.CoverageResource, Group: rNP.Group, Version: rNP.Version, Resource: rNP.Resource, State: bundle.StateComplete})
			ws.AddObjects("c", rNP, []map[string]any{o("NetworkPolicy", "kagent", "deny-egress", nil, map[string]any{"spec": map[string]any{
				"podSelector": map[string]any{"matchLabels": map[string]any{"app": "triage"}}, "policyTypes": []any{"Egress"}}})}, "np")
		}
		return EvaluateActivity(ws, nil, "")
	}
	id := "svc:c/kagent/svc/kagent-tools"
	if r := build(false).Reach["kagent/triage"][id]; r.Verdict != Allowed {
		t.Fatalf("no policy: reachable: %+v", r)
	}
	if r := build(true).Reach["kagent/triage"][id]; r.Verdict != Denied {
		t.Fatalf("default-deny egress: blocked: %+v", r)
	}
}

// A newly deployed agent that has not called anything yet is a caller with
// the Services its configuration reaches its MCP servers at, so the map
// draws its servers and tools before its first call.
func TestActivityConfiguredAgentWithoutCalls(t *testing.T) {
	ws := activityWS(t, "Lists", "list_issues")
	ws.AddObjects("c", rSvc, []map[string]any{svc("kagent", "quiz-mcp", map[string]any{"app": "quiz"}, 80, nil)}, "s2")
	ws.AddObjects("c", rPod, []map[string]any{pod("kagent", "quiz-host", "quiz-host", map[string]any{"app": "quiz-host"}, true, nil)}, "p2")
	ws.AddObjects("c", rKAgent, []map[string]any{o("Agent", "kagent", "quiz-host", nil, map[string]any{"spec": map[string]any{"byo": map[string]any{"deployment": map[string]any{"env": []any{
		map[string]any{"name": "MCP_SERVERS_CONFIG", "value": `[{"name":"it-pub-quiz","url":"http://quiz-mcp.kagent.svc.cluster.local/"}]`},
		map[string]any{"name": "ALLOWED_TOOLS", "value": `["start_quiz"]`}}}}}})}, "a2")
	a := EvaluateActivity(ws, nil, "")
	for _, ca := range a.Callers {
		if ca.Label == "kagent/quiz-host" {
			if ca.Kind != "workload" || !ca.Agentic || ca.Requests != 0 || ca.Anonymous {
				t.Fatalf("an agent with no calls: %+v", ca)
			}
			if got := strings.Join(ca.ServerServices["it-pub-quiz"], ","); got != "svc:c/kagent/svc/quiz-mcp" {
				t.Fatalf("its configured Service: %v", ca.ServerServices)
			}
			return
		}
	}
	t.Fatalf("kagent/quiz-host is not a caller: %+v", a.Callers)
}

func TestFoldAliases(t *testing.T) {
	got := strings.Join(foldAliases([]string{"list_places", "site_daylight_list_places", "work_window", "site_daylight_work_window", "moon_phase_x"}), ",")
	if got != "list_places,work_window,moon_phase_x" {
		t.Fatalf("got %s", got)
	}
}

// An agent's config names the server its own way (site-daylight) at a
// Service whose waypoint route sends to a backend target (daylight): its
// configured tools are filed under the target, where the allowlist is.
func TestActivityConfigResolvesThroughServiceRoute(t *testing.T) {
	ws := activityWS(t, "Lists", "list_issues")
	for _, r := range []bundle.APIResource{rAgwBe, rRoute} {
		ws.SetCoverage(bundle.Coverage{ID: "c." + r.Resource, Cluster: "c", Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, State: bundle.StateComplete})
	}
	ws.AddObjects("c", rSvc, []map[string]any{svc("kagent", "daylight-mcp", nil, 80, map[string]any{"istio.io/use-waypoint": "github-mcp-waypoint"})}, "s3")
	ws.AddObjects("c", rAgwBe, []map[string]any{o("AgentgatewayBackend", "kagent", "daylight-mcp", nil, map[string]any{"spec": map[string]any{
		"mcp": map[string]any{"targets": []any{map[string]any{"name": "daylight", "static": map[string]any{"host": "site-daylight.mcp-servers.svc.cluster.local"}}}}}})}, "be3")
	ws.AddObjects("c", rRoute, []map[string]any{o("HTTPRoute", "kagent", "daylight-mcp", nil, map[string]any{"spec": map[string]any{
		"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "daylight-mcp"}},
		"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"group": "agentgateway.dev", "kind": "AgentgatewayBackend", "name": "daylight-mcp"}}}}}})}, "rt3")
	ws.AddObjects("c", rAgwPol, []map[string]any{o("AgentgatewayPolicy", "kagent", "daylight-per-agent", nil, map[string]any{"spec": map[string]any{
		"targetRefs": []any{map[string]any{"kind": "AgentgatewayBackend", "name": "daylight-mcp"}},
		"backend": map[string]any{"mcp": map[string]any{"authorization": map[string]any{"action": "Allow", "policy": map[string]any{"matchExpressions": []any{
			`source.identity.namespace == "kagent" && source.identity.serviceAccount == "climb" && mcp.tool.name in ["work_window", "site_daylight_work_window"]`}}}}}}})}, "pol3")
	ws.AddObjects("c", rKAgent, []map[string]any{o("Agent", "kagent", "climb", nil, map[string]any{"spec": map[string]any{"byo": map[string]any{"deployment": map[string]any{"env": []any{
		map[string]any{"name": "MCP_SERVERS_CONFIG", "value": `[{"name":"site-daylight","url":"http://daylight-mcp.kagent.svc.cluster.local/"}]`},
		map[string]any{"name": "ALLOWED_TOOLS", "value": `["work_window"]`}}}}}})}, "a3")
	a := EvaluateActivity(ws, nil, "")
	for _, x := range a.Tools {
		if x.Server == "site-daylight" {
			t.Fatalf("filed under the agent's own name for the server: %+v", x)
		}
		if strings.HasPrefix(x.Name, "site_daylight_") {
			t.Fatalf("the prefixed spelling folds into the tool: %+v", x)
		}
		if x.Server == "daylight" && x.Name == "work_window" && (strings.Join(x.AllowedFor, ",") != "kagent/climb" || strings.Join(x.ConfiguredOn, ",") != "kagent/climb") {
			t.Fatalf("work_window is allowed and configured on one server: %+v", x)
		}
	}
	if strings.Contains(findingTitles(a.Findings), "not enforced") && strings.Contains(findingTitles(a.Findings), "climb") {
		t.Fatalf("the allowlist covers it: %s", findingTitles(a.Findings))
	}
}
