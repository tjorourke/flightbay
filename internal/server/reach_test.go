package server

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/features"
)

// The list view blanks fix text; it must not blank it in the cached report
// that ?agent= serves afterwards (found by test/e2e/reach.sh).
func TestReachListKeepsFixes(t *testing.T) {
	s, srv := newServer(t, false)
	defer features.Set(nil)
	features.Set(map[string]bool{features.Reach: true})
	s.WS.AddObjects("mesh1", podsRes, []map[string]any{{"kind": "Pod", "metadata": md("payments", "demo-agent", "pa", map[string]any{"app": "demo-agent"}),
		"spec": map[string]any{"containers": []any{map[string]any{"name": "a", "image": "demo-agent:1"}}}, "status": map[string]any{"phase": "Running"}}}, "c")

	type agent struct {
		Findings []struct{ Title, Fix string }
	}
	fixes := func() int {
		var a agent
		if code := get(t, srv, "/api/reach?agent=payments/demo-agent", &a); code != 200 {
			t.Fatalf("agent view: %d", code)
		}
		n := 0
		for _, f := range a.Findings {
			if f.Fix != "" {
				n++
			}
		}
		return n
	}
	before := fixes()
	if before == 0 {
		t.Fatal("expected at least one finding with a fix")
	}
	var list struct{ Agents []agent }
	if code := get(t, srv, "/api/reach", &list); code != 200 || len(list.Agents) == 0 {
		t.Fatalf("list view: %d %d agents", code, len(list.Agents))
	}
	for _, a := range list.Agents {
		for _, f := range a.Findings {
			if f.Fix != "" {
				t.Fatalf("the list view should not carry fixes: %s", f.Title)
			}
		}
	}
	if after := fixes(); after != before {
		t.Fatalf("fixes after the list view: %d, before: %d", after, before)
	}
}
