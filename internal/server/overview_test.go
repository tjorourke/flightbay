package server

import "testing"

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
