package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/logs"
	"github.com/tjorourke/flightbay/internal/policy"
)

func pol(t *testing.T, mutate func(*policy.Policy)) json.RawMessage {
	p, _ := policy.Profile("standard")
	if mutate != nil {
		mutate(p)
	}
	b, _ := json.Marshal(p)
	return b
}

func obj(kind, ns, name string, labels map[string]any) map[string]any {
	md := map[string]any{"name": name, "namespace": ns, "labels": labels, "uid": ns + "/" + name}
	return map[string]any{"apiVersion": "v1", "kind": kind, "metadata": md}
}

func TestObjectsCommitAtomicallyWithRevisions(t *testing.T) {
	ws := New()
	ws.SetHeader(&bundle.Header{BundleID: "b", Contexts: []string{"mesh1"}, Policy: pol(t, nil)})
	r0 := ws.Revision()
	ws.AddObjects("mesh1", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}, []map[string]any{
		obj("Pod", "payments", "a", map[string]any{"app": "checkout"}),
		obj("Pod", "payments", "b", map[string]any{"app": "other"}),
		obj("Pod", "default", "c", nil),
	}, "chunk1")
	if ws.Revision() <= r0 {
		t.Fatal("revision did not advance")
	}
	all := ws.List("mesh1", "", "pods", "", nil)
	if len(all) != 3 {
		t.Fatalf("got %d objects", len(all))
	}
	sel := ws.List("mesh1", "", "pods", "payments", map[string]string{"app": "checkout"})
	if len(sel) != 1 || sel[0].Name != "a" {
		t.Fatalf("label/namespace filter wrong: %v", sel)
	}
	if o := ws.Get("mesh1", "", "pods", "payments", "b"); o == nil || o.RV == 0 {
		t.Fatal("get failed or resource version missing")
	}
	if ws.Get("mesh1", "", "pods", "payments", "zzz") != nil {
		t.Fatal("unexpected object")
	}
	if len(ws.Resources("mesh1")) != 1 {
		t.Fatalf("resources not registered: %v", ws.Resources("mesh1"))
	}
}

func TestCollectionStates(t *testing.T) {
	ws := New()
	ws.SetHeader(&bundle.Header{BundleID: "b", Contexts: []string{"mesh1"}, Policy: pol(t, func(p *policy.Policy) { p.ExcludeResources = []string{"configmaps"} })})
	ws.SetState(func(s *ImportState) { s.Phase = PhaseLoading })
	if st := ws.CollectionState("mesh1", "", "configmaps", "ConfigMap", true); st.State != StateExcluded {
		t.Fatalf("expected excluded, got %+v", st)
	}
	if st := ws.CollectionState("mesh1", "", "pods", "Pod", true); st.State != StateLoading {
		t.Fatalf("expected loading, got %+v", st)
	}
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.core.v1.pods", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", State: bundle.StateComplete, Items: 3})
	if st := ws.CollectionState("mesh1", "", "pods", "Pod", true); st.State != StateComplete {
		t.Fatalf("expected complete, got %+v", st)
	}
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.core.v1.secrets", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "secrets", State: bundle.StateFailed, Reason: "forbidden"})
	if st := ws.CollectionState("mesh1", "", "secrets", "Secret", true); st.State != StateFailed || st.Reason != "forbidden" {
		t.Fatalf("expected failed, got %+v", st)
	}
	ws.SetState(func(s *ImportState) { s.Phase = PhaseComplete })
	if st := ws.CollectionState("mesh1", "", "services", "Service", true); st.State != StateNotCollected {
		t.Fatalf("expected not-collected after completion, got %+v", st)
	}
}

func logLine(ts string, msg string) string { return ts + " " + msg }

func TestLogSearchFacetsHistogramAndMessages(t *testing.T) {
	ws := New()
	ws.SetHeader(&bundle.Header{BundleID: "b", Contexts: []string{"mesh1"}, Policy: pol(t, nil)})
	ws.SetState(func(s *ImportState) { s.Phase = PhaseLoading })
	ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "previous", []string{
		logLine("2026-09-27T10:07:12Z", "ERROR database connection timed out trace_id=a4e98f02"),
		logLine("2026-09-27T10:08:42Z", "ERROR Process exiting trace_id=a4e98f02"),
	}, "c1")
	ws.AddLogLines("mesh1", "istio-system", "ztunnel-1", "istio-proxy", "current", []string{
		logLine("2026-09-27T10:07:13Z", `{"level":"error","msg":"connection refused","trace_id":"a4e98f02"}`),
		logLine("2026-09-27T10:09:00Z", `{"level":"info","msg":"ok"}`),
	}, "c2")
	res, err := ws.SearchLogs(LogFilter{Query: "trace_id:a4e98f02", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || len(res.Records) != 3 || res.Records[0].TS.Before(res.Records[2].TS) {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.Facets["pod"]["checkout-1"] != 2 || res.Facets["level"]["error"] != 3 || res.Facets["stream"]["previous"] != 2 {
		t.Fatalf("facets wrong: %v", res.Facets)
	}
	if len(res.Histogram) == 0 {
		t.Fatal("histogram empty")
	}
	res, _ = ws.SearchLogs(LogFilter{Query: "level:error", Namespace: "payments", Stream: "previous", Limit: 10})
	if res.Total != 2 {
		t.Fatalf("filters wrong: %d", res.Total)
	}
	res, _ = ws.SearchLogs(LogFilter{Query: `"no such phrase"`})
	if res.Total != 0 || res.Message != "No matches in the data loaded so far." {
		t.Fatalf("expected loading message, got %q", res.Message)
	}
	ws.SetState(func(s *ImportState) { s.Phase = PhaseComplete; s.Verified = true })
	res, _ = ws.SearchLogs(LogFilter{Query: `"no such phrase"`})
	if res.Message != "No matches in the captured logs." {
		t.Fatalf("expected complete message, got %q", res.Message)
	}
	res, _ = ws.SearchLogs(LogFilter{From: time.Date(2026, 9, 27, 10, 8, 0, 0, time.UTC), To: time.Date(2026, 9, 27, 10, 9, 30, 0, time.UTC)})
	if res.Total != 2 {
		t.Fatalf("time range wrong: %d", res.Total)
	}
	if _, err := ws.SearchLogs(LogFilter{Query: `"broken`}); err == nil {
		t.Fatal("expected query error")
	}
	ctx := ws.LogContext(res.Records[0].ID, 1)
	if len(ctx) == 0 {
		t.Fatal("no context lines")
	}
}

func TestLogsExcludedMessage(t *testing.T) {
	ws := New()
	ws.SetHeader(&bundle.Header{BundleID: "b", Policy: pol(t, func(p *policy.Policy) { p.Categories = []policy.Category{policy.CatObjects} })})
	ws.SetState(func(s *ImportState) { s.Phase = PhaseComplete })
	res, _ := ws.SearchLogs(LogFilter{})
	if res.Message != "Logs were excluded by the customer's collection policy." {
		t.Fatalf("got %q", res.Message)
	}
}

func TestLogRecordsParsed(t *testing.T) {
	ws := New()
	ws.AddLogLines("c", "ns", "p", "app", "current", []string{"2026-09-27T10:00:00Z level=warn msg=slow request_id=r1"}, "x")
	res, _ := ws.SearchLogs(LogFilter{Query: "request_id:r1"})
	if res.Total != 1 || res.Records[0].Level != "warn" || res.Records[0].Msg != "slow" {
		t.Fatalf("got %+v", res.Records)
	}
	var _ = logs.Record{}
}

func TestSubscribersNotified(t *testing.T) {
	ws := New()
	ch, cancel := ws.Subscribe()
	defer cancel()
	ws.AddObjects("mesh1", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}, []map[string]any{obj("Pod", "a", "b", nil)}, "c")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no notification")
	}
}
