package store

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

var livePods = bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}

func TestUpsertObjectBumpsRevisionAndMirror(t *testing.T) {
	ws := New()
	ws.SetHeader(&bundle.Header{BundleID: "b", Contexts: []string{"mesh1"}, Policy: pol(t, nil)})
	ch, cancel := ws.Subscribe()
	defer cancel()
	r0 := ws.Revision()
	if !ws.UpsertObject("mesh1", livePods, obj("Pod", "demo", "web-1", map[string]any{"app": "web"})) {
		t.Fatal("first upsert should create")
	}
	select {
	case <-ch:
	default:
		t.Fatal("subscribers were not signalled")
	}
	o1 := ws.Get("mesh1", "", "pods", "demo", "web-1")
	if o1 == nil || ws.Revision() <= r0 || o1.RV <= r0 {
		t.Fatalf("object %v, revision %d -> %d", o1, r0, ws.Revision())
	}
	if n := ws.objectRows("mesh1", "", "pods", "demo", "web-1"); n != 1 {
		t.Fatalf("SQL rows after insert: %d", n)
	}
	// MODIFIED: new labels, a new resource version, still one SQL row.
	if ws.UpsertObject("mesh1", livePods, obj("Pod", "demo", "web-1", map[string]any{"app": "web", "tier": "front"})) {
		t.Fatal("second upsert should replace, not create")
	}
	o2 := ws.Get("mesh1", "", "pods", "demo", "web-1")
	if o2.RV <= o1.RV || o2.Labels()["tier"] != "front" {
		t.Fatalf("not replaced: rv %d -> %d, labels %v", o1.RV, o2.RV, o2.Labels())
	}
	if o1.Labels()["tier"] != "" {
		t.Fatal("the earlier object was mutated; readers holding it would see a torn update")
	}
	if n := ws.objectRows("mesh1", "", "pods", "demo", "web-1"); n != 1 {
		t.Fatalf("SQL rows after update: %d", n)
	}
	if got := len(ws.List("mesh1", "", "pods", "", nil)); got != 1 || ws.State().Objects != 1 {
		t.Fatalf("list %d, state objects %d", got, ws.State().Objects)
	}
	res, err := ws.SearchObjects("label:tier=front", "", 10)
	if err != nil || res.Total != 1 {
		t.Fatalf("search after update: %+v %v", res, err)
	}
	sq, err := ws.SQL("SELECT count(*) FROM objects WHERE name = 'web-1' AND labels['tier'] = 'front'", 10)
	if err != nil || sq.Rows[0][0] != int64(1) {
		t.Fatalf("SQL sees the update: %+v %v", sq, err)
	}
}

func TestDeleteObject(t *testing.T) {
	ws := New()
	ws.SetHeader(&bundle.Header{BundleID: "b", Contexts: []string{"mesh1"}, Policy: pol(t, nil)})
	ws.AddObjects("mesh1", livePods, []map[string]any{obj("Pod", "demo", "a", nil), obj("Pod", "demo", "b", nil)}, "chunk")
	r0 := ws.Revision()
	if !ws.DeleteObject("mesh1", "", "pods", "demo", "a") {
		t.Fatal("delete reported absent")
	}
	if ws.DeleteObject("mesh1", "", "pods", "demo", "a") {
		t.Fatal("second delete reported present")
	}
	if ws.Revision() <= r0 {
		t.Fatal("revision did not move")
	}
	if ws.Get("mesh1", "", "pods", "demo", "a") != nil || len(ws.List("mesh1", "", "pods", "", nil)) != 1 || ws.State().Objects != 1 {
		t.Fatal("object still visible")
	}
	if n := ws.objectRows("mesh1", "", "pods", "demo", "a"); n != 0 {
		t.Fatalf("SQL rows after delete: %d", n)
	}
	res, err := ws.SearchObjects("name:a kind:pod", "", 10)
	if err != nil || res.Total != 0 {
		t.Fatalf("search still finds the deleted pod: %+v %v", res, err)
	}
	res, _ = ws.SearchObjects("kind:pod", "", 10)
	if res.Total != 1 || res.Results[0].Name != "b" {
		t.Fatalf("remaining pods: %+v", res)
	}
}

func TestUpsertAndDeleteEvent(t *testing.T) {
	ws := New()
	ev := func(count float64) map[string]any {
		return map[string]any{"metadata": map[string]any{"name": "web.1", "namespace": "demo"}, "reason": "BackOff", "type": "Warning",
			"message": "back-off", "count": count, "involvedObject": map[string]any{"kind": "Pod", "name": "web"}}
	}
	ws.UpsertEvent("mesh1", ev(1))
	ws.UpsertEvent("mesh1", ev(4))
	evs := ws.Events("mesh1")
	if len(evs) != 1 || evs[0].Obj["count"] != float64(4) || ws.State().Events != 1 {
		t.Fatalf("events after repeat: %+v", evs)
	}
	if rows := ws.eventRows("mesh1", "demo", "web.1"); len(rows) != 1 || !strings.Contains(rows[0], `"count":4`) {
		t.Fatalf("SQL events: %v", rows)
	}
	if !ws.DeleteEvent("mesh1", "demo", "web.1") || len(ws.Events("mesh1")) != 0 || len(ws.eventRows("mesh1", "demo", "web.1")) != 0 {
		t.Fatal("event not deleted")
	}
}

func TestTrimLogsKeepsNewest(t *testing.T) {
	ws := New()
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "2026-09-27T10:00:"+twoDigits(i)+"Z line "+twoDigits(i))
	}
	ws.AddLogLines("mesh1", "demo", "web", "app", "current", lines, "live")
	if n := ws.TrimLogs(20); n != 30 {
		t.Fatalf("dropped %d", n)
	}
	recs := ws.LogStream("mesh1", "demo", "web", "app", "current")
	if len(recs) != 20 || recs[0].Raw != "line 30" || ws.State().LogLines != 20 {
		t.Fatalf("kept %d, first %q, state %d", len(recs), recs[0].Raw, ws.State().LogLines)
	}
	// Appending after a trim continues the stream.
	ws.AddLogLines("mesh1", "demo", "web", "app", "current", []string{"2026-09-27T10:01:00Z next"}, "live")
	if r := ws.LogStreamFrom("mesh1", "demo", "web", "app", "current", 50); len(r) != 1 || r[0].Raw != "next" {
		t.Fatalf("after trim: %+v", r)
	}
}

func twoDigits(i int) string { return string(rune('0'+i/10)) + string(rune('0'+i%10)) }

func TestIsLive(t *testing.T) {
	ws := New()
	ws.SetHeader(&bundle.Header{Contexts: []string{"kind-x"}, Source: SourceLive})
	if !ws.IsLive() {
		t.Fatal("live header without manifest should be live")
	}
	ws.SetManifest(&bundle.Manifest{})
	if ws.IsLive() {
		t.Fatal("a saved live bundle is a snapshot")
	}
}
