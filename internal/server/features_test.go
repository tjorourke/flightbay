package server

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/features"
)

func TestPriorityViewIsGated(t *testing.T) {
	_, srv := newServer(t, false)
	defer features.Set(nil)

	features.Set(map[string]bool{})
	var off map[string]any
	if code := get(t, srv, "/api/posture", &off); code != 200 {
		t.Fatalf("posture without Enterprise: %d", code)
	}
	if _, ok := off["priorities"]; ok {
		t.Fatal("priorities leaked without the feature")
	}
	if code := get(t, srv, "/api/posture?top=5", nil); code != 403 {
		t.Fatalf("top=5 without Enterprise must be refused, got %d", code)
	}
	var ws map[string]any
	get(t, srv, "/api/workspace", &ws)
	if ws["features"].(map[string]any)["priorities"] != false {
		t.Fatalf("workspace must report the feature off: %v", ws["features"])
	}

	features.Set(map[string]bool{features.Priorities: true})
	var on struct {
		Priorities []struct {
			Rank   int
			Points float64
		}
	}
	if code := get(t, srv, "/api/posture?top=3", &on); code != 200 {
		t.Fatalf("posture with Enterprise: %d", code)
	}
	if on.Priorities == nil || len(on.Priorities) > 3 {
		t.Fatalf("want up to 3 priorities, got %+v", on.Priorities)
	}
	for i, p := range on.Priorities {
		if p.Rank != i+1 {
			t.Fatalf("ranks %+v", on.Priorities)
		}
	}
}

// A log search that matches nothing returns records: [] (the UI maps over it).
func TestLogSearchNoMatchIsEmptyList(t *testing.T) {
	_, srv := newServer(t, false)
	var res map[string]any
	if code := get(t, srv, "/api/logs/search?q=zzzz-no-such-line", &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if recs, ok := res["records"].([]any); !ok || len(recs) != 0 {
		t.Fatalf("records must be an empty list, got %#v", res["records"])
	}
}
