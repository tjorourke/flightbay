package server

import (
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
)

func TestErrorRunsFoldSteadyErrors(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	var mins []store.ErrorMinute
	for i := 0; i < 30; i++ {
		c := 4
		if i == 20 {
			c = 60
		}
		mins = append(mins, store.ErrorMinute{Cluster: "c", Namespace: "kube-system", Minute: t0.Add(time.Duration(i) * time.Minute), Count: c})
	}
	mins = append(mins, store.ErrorMinute{Cluster: "c", Namespace: "shop", Minute: t0.Add(5 * time.Minute), Count: 7})
	got := errorRuns(mins)
	if len(got) != 3 {
		t.Fatalf("want one run, one spike and one single minute, got %d: %+v", len(got), got)
	}
	var run, spike bool
	for _, it := range got {
		if it.Namespace == "kube-system" && it.Severity == "warning" && it.To.Sub(it.From) == 30*time.Minute {
			run = true
		}
		if it.Severity == "critical" && it.Time.Equal(t0.Add(20*time.Minute)) {
			spike = true
		}
	}
	if !run || !spike {
		t.Fatalf("run=%v spike=%v %+v", run, spike, got)
	}
}
