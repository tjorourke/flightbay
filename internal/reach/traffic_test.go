package reach

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

// ztLine is a ztunnel access log line (upstream text format).
func ztLine(src, srcID, dstSvc, dstPod, dstNS, dstAddr, extra string) string {
	l := "2026-09-28T11:06:10.074538Z 2026-09-28T11:06:10.074538Z\tinfo\taccess\tconnection complete\tsrc.workload=\"" + src + "\" src.namespace=\"agents\""
	if srcID != "" {
		l += " src.identity=\"" + srcID + "\""
	}
	l += " dst.addr=" + dstAddr + " dst.service=\"" + dstSvc + "\" dst.workload=\"" + dstPod + "\" dst.namespace=\"" + dstNS + "\" direction=\"outbound\""
	if strings.HasSuffix(dstAddr, ":15008") {
		l += " dst.hbone_addr=10.244.0.9:5432"
	}
	return l + " " + extra
}

// trafficWS is the incident with postgres locked to the registry (db-deny),
// plus access logs: the agent reached the registry over HBONE, the second
// agent reached postgres in plaintext, and the agent was refused by postgres.
func trafficWS(t *testing.T) *store.Workspace {
	ws := incident(t, "db-deny")
	ws.AddLogLines("c", "istio-system", "ztunnel-1", "istio-proxy", "current", []string{
		ztLine("eval-agent", "spiffe://cluster.local/ns/agents/sa/eval-agent", "artifactory.tools.svc.cluster.local", "waypoint", "tools", "10.244.0.20:15008", ""),
		ztLine("eval-agent", "spiffe://cluster.local/ns/agents/sa/eval-agent", "artifactory.tools.svc.cluster.local", "waypoint", "tools", "10.244.0.20:15008", ""),
		ztLine("eval-agent-2", "", "postgres.tools.svc.cluster.local", "postgres", "tools", "10.244.0.9:5432", ""),
		ztLine("eval-agent", "spiffe://cluster.local/ns/agents/sa/eval-agent", "postgres.tools.svc.cluster.local", "postgres", "tools", "10.244.0.9:15008", "error=\"connection closed due to policy rejection\""),
	}, "zt")
	ws.WaitLogs()
	return ws
}

func trafficEdge(t *testing.T, tr Traffic, from, to string) TEdge {
	t.Helper()
	lab := map[string]string{}
	for _, n := range tr.Nodes {
		lab[n.ID] = n.Label
	}
	for _, e := range tr.Edges {
		if lab[e.From] == from && lab[e.To] == to {
			return e
		}
	}
	t.Fatalf("no edge %s -> %s in %+v", from, to, tr.Edges)
	return TEdge{}
}

func TestTrafficMap(t *testing.T) {
	tr := EvaluateTraffic(trafficWS(t))
	if !tr.Captured || tr.Totals.Edges != 3 {
		t.Fatalf("totals %+v notes %v", tr.Totals, tr.Notes)
	}
	reg := trafficEdge(t, tr, "agents/eval-agent", "tools/artifactory")
	if reg.TLS != "mtls" || reg.Connections != 2 || reg.Via != "tools/waypoint" || reg.Verdict != Allowed || reg.Risk == "red" {
		t.Fatalf("registry edge %+v", reg)
	}
	pt := trafficEdge(t, tr, "agents/eval-agent-2", "tools/postgres")
	if pt.TLS != "plaintext" || !has(pt.Flags, "plaintext") || pt.Risk != "red" {
		t.Fatalf("plaintext between two mesh pods is red: %+v", pt)
	}
	if pt.Verdict != Denied || !has(pt.Flags, "mismatch") {
		t.Fatalf("policy denies eval-agent-2 -> postgres, yet a connection got through: %+v", pt)
	}
	den := trafficEdge(t, tr, "agents/eval-agent", "tools/postgres")
	if den.Denied != 1 || den.Verdict != Denied || !den.Policed || has(den.Flags, "mismatch") || has(den.Flags, "unpoliced") {
		t.Fatalf("refused and policy agrees: %+v", den)
	}
	ft := findingTitles(tr.Findings)
	if !strings.Contains(ft, "critical: Plaintext between workloads that are both in the mesh") || !strings.Contains(ft, "The access logs and the captured policy disagree") {
		t.Fatalf("findings:\n%s", ft)
	}
}

func TestExplainDenied(t *testing.T) {
	x, err := Explain(trafficWS(t), "", "agents/eval-agent", "tools/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if x.Verdict != Denied || !strings.Contains(x.Summary, "refused at ztunnel authorization") {
		t.Fatalf("verdict %s summary %q", x.Verdict, x.Summary)
	}
	var zt *Step
	for i := range x.Steps {
		if strings.HasPrefix(x.Steps[i].Point, "ztunnel authorization") {
			zt = &x.Steps[i]
		}
	}
	if zt == nil || zt.Verdict != Denied || len(zt.Policies) != 1 || zt.Policies[0].Name != "postgres-callers" {
		t.Fatalf("the ztunnel step names the policy that refused it: %+v", x.Steps)
	}
	if x.Steps[0].Point != "NetworkPolicy egress from eval-agent" || x.Steps[0].Verdict != "none" {
		t.Fatalf("steps run in the order traffic meets them: %+v", x.Steps[0])
	}
	if !strings.Contains(x.Fix, "cluster.local/ns/agents/sa/eval-agent") || !strings.Contains(x.Fix, "app: postgres") || !strings.Contains(x.Fix, `"5432"`) {
		t.Fatalf("fix:\n%s", x.Fix)
	}
	if x.Observed == nil || x.Observed.Denied != 1 || !x.Observed.Agrees {
		t.Fatalf("observed %+v", x.Observed)
	}
}

func TestExplainAllowedThroughWaypoint(t *testing.T) {
	x, err := Explain(incident(t, "read-only"), "c", "agents/eval-agent", "artifactory.tools.svc.cluster.local")
	if err != nil {
		t.Fatal(err)
	}
	if x.Verdict != Allowed || x.Layer != "L7" || strings.Join(x.Methods, ",") != "GET,HEAD" || x.Fix != "" {
		t.Fatalf("%+v", x)
	}
	wp := false
	for _, st := range x.Steps {
		if strings.HasPrefix(st.Point, "waypoint waypoint authorization") && st.Verdict == Allowed && len(st.Policies) == 1 {
			wp = true
		}
	}
	if !wp {
		t.Fatalf("the waypoint step allows it by artifactory-read-only: %+v", x.Steps)
	}
}

func TestExplainDenyPolicyGetsAdviceNotYAML(t *testing.T) {
	deny := o("AuthorizationPolicy", "tools", "no-agents", nil, map[string]any{"spec": map[string]any{
		"selector": map[string]any{"matchLabels": map[string]any{"app": "postgres"}}, "action": "DENY",
		"rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"namespaces": []any{"agents"}}}}}}}})
	x, err := Explain(incidentWith(t, "", []map[string]any{deny}), "", "agents/eval-agent", "tools/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if x.Verdict != Denied || x.Fix != "" || len(x.Advice) == 0 || !strings.Contains(x.Advice[0], "DENY is checked before ALLOW") {
		t.Fatalf("%+v", x)
	}
}

func TestExplainUnknownNames(t *testing.T) {
	_, err := Explain(incident(t, ""), "", "agents/eval-agnt", "tools/postgres")
	ee, ok := err.(*ExplainError)
	if !ok || len(ee.Suggestions) == 0 {
		t.Fatalf("a wrong name gets suggestions: %v", err)
	}
}

// A Service with no pods whose waypoint answers for it (an agentgateway
// waypoint in front of an external MCP server) is evaluated at the waypoint
// instead of coming out unknown.
func TestWaypointOnlyService(t *testing.T) {
	ws := incident(t, "")
	ws.AddObjects("c", rSvc, []map[string]any{svc("tools", "github-mcp", nil, 80, map[string]any{"istio.io/use-waypoint": "waypoint"})}, "s2")
	x, err := Explain(ws, "", "agents/eval-agent", "tools/github-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if x.Verdict != Allowed || len(x.Steps) < 3 || !strings.Contains(x.Steps[len(x.Steps)-1].Point, "waypoint waypoint authorization") {
		t.Fatalf("%s: %+v", x.Summary, x.Steps)
	}
}
