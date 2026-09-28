package collect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
	_ "github.com/tjorourke/flightbay/internal/store/duckdb" // log search in the round trip
)

// A live workspace saved with ExportWorkspace and opened again holds the same
// objects, logs and events, and the bundle verifies.
func TestExportRoundTrip(t *testing.T) {
	ws := store.New()
	p, _ := policy.Profile("standard")
	pb, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"kind-live"}, Policy: pb, CaptureStart: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC), Source: store.SourceLive, Collector: "flightbay-desk live test"})
	pods := bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true, ShortNames: []string{"po"}}
	deps := bundle.APIResource{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true}
	ws.RegisterResources("kind-live", []bundle.APIResource{pods, deps})
	ws.SetVersion("kind-live", json.RawMessage(`{"gitVersion":"v1.34.0"}`))
	pod := func(name, phase string) map[string]any {
		return map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": "demo", "labels": map[string]any{"app": "web"}},
			"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox"}}}, "status": map[string]any{"phase": phase}}
	}
	ws.AddObjects("kind-live", pods, []map[string]any{pod("web-1", "Running"), pod("web-2", "Running")}, "live:pods")
	ws.UpsertObject("kind-live", pods, pod("web-3", "Pending"))
	ws.UpsertObject("kind-live", pods, pod("web-2", "Failed"))
	ws.DeleteObject("kind-live", "", "pods", "demo", "web-1")
	ws.UpsertObject("kind-live", deps, map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "demo"}, "spec": map[string]any{"replicas": float64(2)}})
	ws.UpsertEvent("kind-live", map[string]any{"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "web-2.1", "namespace": "demo"}, "reason": "BackOff", "count": float64(3),
		"involvedObject": map[string]any{"kind": "Pod", "name": "web-2", "namespace": "demo"}})
	ws.AddLogLines("kind-live", "demo", "web-2", "app", "current", []string{"2026-09-28T09:00:01.123456789Z hello", "2026-09-28T09:00:02Z level=error msg=boom"}, "live")
	ws.AddLogLines("kind-live", "demo", "web-2", "app", "previous", []string{"2026-09-28T08:59:00Z crashed"}, "live-previous")
	ws.PutCoverage(bundle.Coverage{ID: "kind-live.logs.demo.web-3.app.current", Cluster: "kind-live", Type: bundle.CoverageLogs, Namespace: "demo", Pod: "web-3", Container: "app", Stream: "current",
		State: bundle.StateTruncated, Reason: "not followed: over the cap"})
	ws.SetRedaction(json.RawMessage(`{"total":2,"locations":{"x":{"jwt":2}}}`))
	ws.AddDiagnostic("kind-live", "proxies", "istio-system", "ztunnel-abc", "meta.json", []byte(`{"component":"ztunnel","image":"ztunnel:1.30"}`))
	ws.AddDiagnostic("kind-live", "proxies", "istio-system", "ztunnel-abc", "config_dump.json", []byte(`{"workloads":{}}`))
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseLive })

	var buf bytes.Buffer
	if err := collect.ExportWorkspace(ws, &buf, collect.ExportOptions{Collector: "flightbay-desk live test"}); err != nil {
		t.Fatal(err)
	}
	back := store.New()
	if err := importer.Run(context.Background(), bytes.NewReader(buf.Bytes()), back, importer.Options{Source: "saved"}); err != nil {
		t.Fatalf("saved bundle does not import and verify: %v", err)
	}
	if st := back.State(); !st.Verified || st.Phase != store.PhaseComplete {
		t.Fatalf("state %+v", st)
	}
	if back.IsLive() || back.Header().Source != store.SourceLive || back.Header().Contexts[0] != "kind-live" {
		t.Fatalf("header %+v", back.Header())
	}
	strip := func(o map[string]any) map[string]any {
		b, _ := json.Marshal(o)
		var c map[string]any
		_ = json.Unmarshal(b, &c)
		delete(c["metadata"].(map[string]any), "resourceVersion")
		return c
	}
	for _, r := range []bundle.APIResource{pods, deps} {
		a, b := ws.List("kind-live", r.Group, r.Resource, "", nil), back.List("kind-live", r.Group, r.Resource, "", nil)
		if len(a) != len(b) {
			t.Fatalf("%s: %d objects, %d after the round trip", r.Name(), len(a), len(b))
		}
		for i := range a {
			if !reflect.DeepEqual(strip(a[i].Obj), strip(b[i].Obj)) {
				t.Fatalf("%s/%s differs:\n%v\n%v", r.Name(), a[i].Name, strip(a[i].Obj), strip(b[i].Obj))
			}
		}
	}
	if len(back.List("kind-live", "", "pods", "", nil)) != 2 || back.Get("kind-live", "", "pods", "demo", "web-1") != nil {
		t.Fatal("the deleted pod came back, or a pod is missing")
	}
	if b := back.Get("kind-live", "", "pods", "demo", "web-2"); b.Obj["status"].(map[string]any)["phase"] != "Failed" {
		t.Fatal("the update was lost")
	}
	ea, eb := ws.Events("kind-live"), back.Events("kind-live")
	if len(ea) != 1 || len(eb) != 1 || !reflect.DeepEqual(ea[0].Obj, eb[0].Obj) {
		t.Fatalf("events %v vs %v", ea, eb)
	}
	for _, stream := range []string{"current", "previous"} {
		a, b := ws.LogStream("kind-live", "demo", "web-2", "app", stream), back.LogStream("kind-live", "demo", "web-2", "app", stream)
		if len(a) != len(b) || len(a) == 0 {
			t.Fatalf("%s: %d lines, %d after", stream, len(a), len(b))
		}
		for i := range a {
			if !a[i].TS.Equal(b[i].TS) || a[i].Raw != b[i].Raw || a[i].Level != b[i].Level {
				t.Fatalf("%s line %d: %v %q vs %v %q", stream, i, a[i].TS, a[i].Raw, b[i].TS, b[i].Raw)
			}
		}
	}
	if c, ok := back.CoverageRecord("kind-live.logs.demo.web-3.app.current"); !ok || c.State != bundle.StateTruncated {
		t.Fatalf("coverage for a stream not followed was lost: %+v", c)
	}
	if d := back.Diagnostic("kind-live", "proxies", "istio-system", "ztunnel-abc"); d == nil || d.Component != "ztunnel" || string(d.Files["config_dump.json"]) != `{"workloads":{}}` {
		t.Fatalf("diagnostics: %+v", d)
	}
	if len(back.Version("kind-live")) == 0 || string(back.Redaction()) == "" {
		t.Fatal("version or redaction report missing")
	}
}
