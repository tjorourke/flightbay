package server

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func TestFindingsCacheFollowsRevision(t *testing.T) {
	ws := workspace(t)
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete })
	s := &Server{WS: ws}
	first := s.findingsList()
	if &first[0] != &s.findingsList()[0] {
		t.Fatal("the same revision reuses the list")
	}
	n := len(first)
	ws.AddObjects("mesh1", podsRes, []map[string]any{{"kind": "Pod",
		"metadata": md("payments", "new-crash", "p9", nil),
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app", "restartCount": float64(40),
			"state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}}}}}}, "c2")
	if got := len(s.findingsList()); got <= n {
		t.Fatalf("a new revision is analysed again: %d findings before, %d after", n, got)
	}
}
