package server

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func TestOverviewCharts(t *testing.T) {
	_, srv := newServer(t, false)
	var got struct {
		Rules []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Count    int    `json:"count"`
		} `json:"rules"`
		Restarts []struct {
			Namespace string `json:"namespace"`
			Restarts  int    `json:"restarts"`
			Pods      int    `json:"pods"`
		} `json:"restarts"`
		Logs struct {
			Total      int   `json:"total"`
			ErrorTotal int   `json:"errorTotal"`
			Histogram  []any `json:"histogram"`
		} `json:"logs"`
	}
	if code := get(t, srv, "/api/overview/charts", &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	var crash bool
	for _, r := range got.Rules {
		if r.Rule == "workload.crashloop" && r.Severity == "critical" && r.Count == 1 {
			crash = true
		}
	}
	if !crash {
		t.Fatalf("crashloop rule missing from %+v", got.Rules)
	}
	if len(got.Restarts) != 1 || got.Restarts[0].Namespace != "payments" || got.Restarts[0].Restarts != 12 || got.Restarts[0].Pods != 1 {
		t.Fatalf("restarts = %+v", got.Restarts)
	}
	if got.Logs.Total != 3 || got.Logs.ErrorTotal != 3 || len(got.Logs.Histogram) == 0 {
		t.Fatalf("logs = %+v", got.Logs)
	}

	var other struct {
		Rules    []any `json:"rules"`
		Restarts []any `json:"restarts"`
	}
	get(t, srv, "/api/overview/charts?cluster=mesh2", &other)
	if len(other.Restarts) != 0 {
		t.Fatalf("mesh2 should have no restarts: %+v", other.Restarts)
	}
}

// Live mode decodes built-in types from protobuf, so counts arrive as int64.
func TestPodRestartsIntTypes(t *testing.T) {
	o := &store.Object{Obj: map[string]any{"status": map[string]any{
		"initContainerStatuses": []any{map[string]any{"restartCount": 2}},
		"containerStatuses":     []any{map[string]any{"restartCount": int64(36)}, map[string]any{"restartCount": float64(4)}},
	}}}
	if n := podRestarts(o); n != 42 {
		t.Fatalf("podRestarts = %d, want 42", n)
	}
}
