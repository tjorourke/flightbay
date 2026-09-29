package reach

import (
	"strings"
	"testing"
)

// The incident agent used the registry (through its waypoint) and was
// refused by postgres. Locked down to what it used, it keeps the registry
// and loses the internet, and nothing it used is cut.
func TestAgentUsageAndLockdown(t *testing.T) {
	ws := trafficWS(t)
	a := agentNamed(t, Evaluate(ws), "eval-agent")
	tr := EvaluateTraffic(ws)
	u := AgentUsage(ws, a, &tr, nil)
	var reg, pg *UseDest
	for i := range u.Destinations {
		switch u.Destinations[i].Label {
		case "tools/artifactory":
			reg = &u.Destinations[i]
		case "tools/postgres":
			pg = &u.Destinations[i]
		}
	}
	if reg == nil || !reg.Used || !reg.Allowed || reg.Connections != 2 || reg.TLS != "mtls" {
		t.Fatalf("registry %+v", reg)
	}
	if pg == nil || pg.Used || !pg.Tried || pg.Allowed {
		t.Fatalf("postgres was tried and refused, and policy denies it: %+v", pg)
	}
	if u.Totals.Used != 1 || u.Totals.Unused == 0 || u.Totals.Tried != 1 {
		t.Fatalf("totals %+v", u.Totals)
	}
	if len(u.Destinations) == 0 || !u.Destinations[0].Used {
		t.Fatal("used destinations come first")
	}
	if u.Timeline.From.IsZero() || len(u.Timeline.Buckets) == 0 || len(u.Timeline.Events) == 0 {
		t.Fatalf("timeline %+v", u.Timeline)
	}
	ld := u.Lockdown
	if !ld.Evaluated || ld.Before.Internet != Allowed || ld.After.Internet != Denied {
		t.Fatalf("lockdown before %+v after %+v notes %v", ld.Before, ld.After, ld.Notes)
	}
	if strings.Join(ld.StillUsed, ",") != "tools/artifactory" || len(ld.Cut) != 0 {
		t.Fatalf("still used %v cut %v", ld.StillUsed, ld.Cut)
	}
	if !strings.Contains(ld.Fix, "kind: NetworkPolicy") || !strings.Contains(ld.Fix, "gateway-name: waypoint") || strings.Contains(ld.Fix, "postgres") {
		t.Fatalf("fix:\n%s", ld.Fix)
	}
	if ld.After.Direct > ld.Before.Direct || ld.After.Critical >= ld.Before.Critical {
		t.Fatalf("reach should not grow and the critical finding should go: %+v -> %+v", ld.Before, ld.After)
	}
}

// Tool allowlists keep every caller seen on the route, each with its own tools.
func TestLockdownMCPAllowlist(t *testing.T) {
	ws := activityWS(t, "Lists", "list_issues", "delete_repository")
	a := agentNamed(t, Evaluate(ws), "triage")
	act := EvaluateActivity(ws, nil, "")
	u := AgentUsage(ws, a, nil, &act)
	fix := u.Lockdown.Fix
	if !strings.Contains(fix, `source.identity.serviceAccount == "triage" && mcp.tool.name in ["delete_repository", "list_issues"]`) {
		t.Fatalf("allowlist:\n%s", fix)
	}
	if !strings.Contains(fix, "<mcp backend behind kagent/github-mcp>") {
		t.Fatalf("an unknown backend is left for the reader to name:\n%s", fix)
	}
}
