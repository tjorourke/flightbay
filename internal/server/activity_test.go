package server

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/features"
)

func TestActivityTrafficExplainEndpoints(t *testing.T) {
	s, srv := newServer(t, false)
	defer features.Set(nil)
	features.Set(map[string]bool{})
	for _, p := range []string{"/api/activity", "/api/traffic", "/api/explain?from=a/b&to=c/d"} {
		if code := get(t, srv, p, &map[string]any{}); code != 403 {
			t.Fatalf("%s without Enterprise: %d", p, code)
		}
	}
	features.Set(map[string]bool{features.Activity: true, features.Traffic: true})
	s.WS.AddLogLines("mesh1", "agentgateway-system", "ai-1", "agentgateway", "current", []string{
		"2026-09-28T18:30:00Z\tinfo\trequest gateway=agentgateway-system/ai listener=http route=agentgateway-system/github src.addr=10.244.1.50:5 jwt.sub=alice http.method=POST http.status=200 protocol=mcp mcp.method.name=tools/call mcp.target=github gen_ai.tool.name=list_issues duration=5ms",
	}, "l")
	s.WS.WaitLogs()
	var a struct {
		Totals  struct{ ToolCalls int }
		Callers []struct{ Label string }
	}
	if code := get(t, srv, "/api/activity", &a); code != 200 || a.Totals.ToolCalls != 1 || len(a.Callers) != 1 || a.Callers[0].Label != "JWT subject alice" {
		t.Fatalf("activity %d %+v", code, a)
	}
	var tr struct{ Captured bool }
	if code := get(t, srv, "/api/traffic", &tr); code != 200 || tr.Captured {
		t.Fatalf("traffic %d %+v", code, tr)
	}
	s.WS.AddObjects("mesh1", podsRes, []map[string]any{{"kind": "Pod", "metadata": md("payments", "web", "pw", map[string]any{"app": "web"}),
		"spec": map[string]any{"containers": []any{map[string]any{"name": "a", "image": "web:1"}}}, "status": map[string]any{"phase": "Running"}}}, "p")
	var e struct {
		Error       string
		Suggestions []string
	}
	if code := get(t, srv, "/api/explain?from=payments/wbe&to=payments/web", &e); code != 404 || e.Error == "" {
		t.Fatalf("explain with a wrong name: %d %+v", code, e)
	}
	if code := get(t, srv, "/api/explain?from=payments/web", &e); code != 400 {
		t.Fatalf("explain without to: %d", code)
	}
}

func TestReachUsageEndpoint(t *testing.T) {
	s, srv := newServer(t, false)
	defer features.Set(nil)
	features.Set(map[string]bool{})
	if code := get(t, srv, "/api/reach/usage?agent=payments/demo-agent", &map[string]any{}); code != 403 {
		t.Fatalf("without Enterprise: %d", code)
	}
	features.Set(map[string]bool{features.Reach: true, features.Traffic: true, features.Activity: true})
	s.WS.AddObjects("mesh1", podsRes, []map[string]any{{"kind": "Pod", "metadata": md("payments", "demo-agent", "pa", map[string]any{"app": "demo-agent"}),
		"spec": map[string]any{"containers": []any{map[string]any{"name": "a", "image": "demo-agent:1"}}}, "status": map[string]any{"phase": "Running"}}}, "c")
	var u struct {
		Agent    string
		Lockdown struct{ Notes []string }
	}
	if code := get(t, srv, "/api/reach/usage?agent=payments/demo-agent", &u); code != 200 || u.Agent != "payments/demo-agent" || len(u.Lockdown.Notes) == 0 {
		t.Fatalf("usage %d %+v", code, u)
	}
	if code := get(t, srv, "/api/reach/usage?agent=nobody", &map[string]any{}); code != 404 {
		t.Fatalf("unknown agent: %d", code)
	}
}
