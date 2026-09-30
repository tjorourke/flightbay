package posture

import "testing"

func TestPrioritiesRankByPointsRecovered(t *testing.T) {
	r := Report{Checks: []Check{
		{ID: "a.crit", Area: "a", Severity: "critical", Status: Fail, Score: 0.8, Items: []Item{{Name: "x", Status: Fail}}},
		{ID: "a.high", Area: "a", Severity: "high", Status: Fail, Score: 0, Items: []Item{{Name: "y", Status: Fail}, {Name: "z", Status: Fail}}},
		{ID: "a.low", Area: "a", Severity: "low", Status: Warn, Score: 0.5, Items: []Item{{Name: "w", Status: Warn}}},
		{ID: "a.ok", Area: "a", Severity: "critical", Status: Pass, Score: 1},
		{ID: "a.na", Area: "a", Severity: "critical", Status: NA},
	}}
	// Area weight = 10 + 6 + 1 + 10 = 27 (N/A excluded).
	if got := PointsIfFixed(r, r.Checks[1]); got != 22.2 {
		t.Fatalf("high fully failing: want 22.2, got %v", got)
	}
	if got := PointsIfFixed(r, r.Checks[0]); got != 7.4 {
		t.Fatalf("critical 80%% passing: want 7.4, got %v", got)
	}
	p := Priorities(r, 0)
	// impact: high 6*1=6, critical 10*0.2=2, low 1*0.5=0.5
	if len(p) != 3 || p[0].ID != "a.high" || p[1].ID != "a.crit" || p[2].ID != "a.low" {
		t.Fatalf("order %+v", p)
	}
	if p[0].Rank != 1 || p[0].Failing != 2 || len(p[0].Objects) != 2 {
		t.Fatalf("detail %+v", p[0])
	}
	if len(Priorities(r, 1)) != 1 {
		t.Fatal("limit")
	}
}

// Fixing every priority in an area must bring that area to 100.
func TestPointsAddUp(t *testing.T) {
	ws := fixture(t)
	r := Evaluate(ws)
	for _, a := range r.Areas {
		if !a.Present || a.Grade == "" {
			continue
		}
		sum := float64(a.Score)
		for _, p := range Priorities(r, 0) {
			if p.Area == a.Area {
				sum += p.Points
			}
		}
		if sum < 99 || sum > 101 {
			t.Errorf("%s: score %d plus recoverable points = %.1f, want about 100", a.Area, a.Score, sum)
		}
	}
}

// A critical check in a big area outranks a high check in a small one.
func TestImpactIgnoresAreaSize(t *testing.T) {
	r := Report{Checks: []Check{{ID: "big.crit", Area: "big", Severity: "critical", Status: Fail, Score: 0.2}}}
	for i := 0; i < 40; i++ {
		r.Checks = append(r.Checks, Check{ID: "big.pass", Area: "big", Severity: "high", Status: Pass, Score: 1})
	}
	r.Checks = append(r.Checks, Check{ID: "small.high", Area: "small", Severity: "high", Status: Fail, Score: 0})
	p := Priorities(r, 0)
	if p[0].ID != "big.crit" {
		t.Fatalf("want the critical first, got %s (points %.1f vs %.1f)", p[0].ID, p[0].Points, p[1].Points)
	}
	if p[0].Points >= p[1].Points {
		t.Fatal("test premise: the critical recovers fewer area points")
	}
}

func TestReachChecksAreEnterprise(t *testing.T) {
	t.Setenv("FLIGHTBAY_ENTERPRISE", "0") // reach is on by default
	ws := fixture(t)
	for _, c := range Evaluate(ws).Checks {
		switch c.ID {
		case "agentic.agent-internet-reach", "agentic.agent-transitive-fetcher", "agentic.agent-write-sensitive", "agentic.agent-no-shared-channel", "agentic.agent-least-reach":
			t.Fatalf("%s shown without the reach feature", c.ID)
		}
	}
}

func TestOnlyKeepsOneAreaAndRenumbers(t *testing.T) {
	r := Report{Areas: []AreaScore{{Area: "agentic"}, {Area: "ambient"}},
		Checks:     []Check{{ID: "a", Area: "agentic"}, {ID: "b", Area: "ambient"}},
		Priorities: []Priority{{Rank: 1, ID: "b", Area: "ambient"}, {Rank: 2, ID: "a", Area: "agentic"}}}
	o := r.Only("agentic")
	if len(o.Areas) != 1 || len(o.Checks) != 1 || len(o.Priorities) != 1 || o.Priorities[0].Rank != 1 {
		t.Fatalf("%+v", o)
	}
}
