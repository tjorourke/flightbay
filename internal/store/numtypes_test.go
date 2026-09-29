package store

import "testing"

// Event counts decode as float64 from a bundle and int64 in live mode; both
// reach the events table.
func TestEventCountNumberTypes(t *testing.T) {
	ws, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.AddEvents("c", []map[string]any{
		{"metadata": map[string]any{"name": "a", "namespace": "ns"}, "count": int64(36), "reason": "BackOff"},
		{"metadata": map[string]any{"name": "b", "namespace": "ns"}, "count": float64(4), "reason": "BackOff"},
	})
	var sum int64
	if err := ws.ldb.db.QueryRow(`SELECT sum(count) FROM events`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != 40 {
		t.Fatalf("event counts summed to %d, want 40", sum)
	}
}
