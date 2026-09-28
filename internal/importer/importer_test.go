package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

func header() bundle.Header {
	p, _ := policy.Profile("standard")
	b, _ := json.Marshal(p)
	return bundle.Header{Contexts: []string{"mesh1"}, Policy: b, CaptureStart: time.Now()}
}

var podsRes = bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}

func writeObjects(t *testing.T, w *bundle.Writer) {
	t.Helper()
	must(t, w.AddJSON(bundle.DiscoveryPath("mesh1", "version.json"), map[string]string{"gitVersion": "v1.34.1"}))
	must(t, w.AddJSON(bundle.DiscoveryPath("mesh1", "apiresources.json"), []bundle.APIResource{podsRes}))
	p := bundle.ResourcePath("mesh1", "", "v1", "pods", 1)
	must(t, w.AddJSON(p, map[string]any{"items": []any{map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "checkout-1", "namespace": "payments"}}}}))
	must(t, w.Coverage(bundle.Coverage{ID: "mesh1.resource.core.v1.pods", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true, Chunks: []string{p}, Items: 1, State: bundle.StateComplete}))
}

func writeLogs(t *testing.T, w *bundle.Writer) {
	t.Helper()
	l := bundle.LogPath("mesh1", "payments", "checkout-1", "app", "previous", 1)
	must(t, w.Add(l, []byte("2026-09-27T10:07:12Z ERROR database connection timed out\n2026-09-27T10:08:42Z ERROR Process exiting\n")))
	must(t, w.AddJSON(bundle.EventsPath("mesh1", 1), map[string]any{"items": []any{map[string]any{"reason": "BackOff", "involvedObject": map[string]any{"kind": "Pod", "name": "checkout-1", "namespace": "payments"}}}}))
	must(t, w.Add(bundle.CollectionLog, []byte(`{"operation":"list","result":"ok"}`+"\n")))
	must(t, w.Add(bundle.RedactionName, []byte(`{"total":3}`)))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestProgressiveImport(t *testing.T) {
	pr, pw := io.Pipe()
	w, err := bundle.NewWriter(pw, bundle.WriterOptions{Compression: "fastest"})
	must(t, err)
	ws := store.New()
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), pr, ws, Options{Source: "pipe"}) }()

	must(t, w.WriteHeader(header()))
	writeObjects(t, w)
	must(t, w.Flush())

	waitFor(t, "pods before the stream ends", func() bool { return len(ws.List("mesh1", "", "pods", "", nil)) == 1 })
	if st := ws.State(); st.Phase != store.PhaseLoading || st.Verified {
		t.Fatalf("expected loading, unverified: %+v", st)
	}
	res, _ := ws.SearchLogs(store.LogFilter{Query: "database"})
	if res.Total != 0 || !res.Loading || !strings.Contains(res.Message, "loaded so far") {
		t.Fatalf("expected loading message, got %+v", res)
	}
	if string(ws.Version("mesh1")) == "" || ws.CollectionState("mesh1", "", "pods", "Pod", true).State != store.StateComplete {
		t.Fatal("discovery or coverage not applied")
	}

	writeLogs(t, w)
	must(t, w.Close(bundle.Manifest{Complete: true}))
	must(t, pw.Close())
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st := ws.State()
	if st.Phase != store.PhaseComplete || !st.Verified || st.LogLines != 2 || st.Events != 1 || st.BytesReceived == 0 {
		t.Fatalf("unexpected final state %+v", st)
	}
	res, _ = ws.SearchLogs(store.LogFilter{Query: "database"})
	if res.Total != 1 {
		t.Fatalf("log not searchable after import: %+v", res)
	}
	if len(ws.CollectionLog()) != 1 || len(ws.Redaction()) == 0 || ws.Manifest() == nil {
		t.Fatal("reports not imported")
	}
}

func TestTruncatedImportKeepsDataButFails(t *testing.T) {
	var buf bytes.Buffer
	w, _ := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest", FlushEvery: time.Nanosecond})
	must(t, w.WriteHeader(header()))
	writeObjects(t, w)
	must(t, w.Flush())
	cut := buf.Len()
	writeLogs(t, w)
	must(t, w.Close(bundle.Manifest{Complete: true}))
	ws := store.New()
	err := Run(context.Background(), bytes.NewReader(buf.Bytes()[:cut+10]), ws, Options{})
	if err == nil {
		t.Fatal("expected an error for a truncated bundle")
	}
	st := ws.State()
	if st.Phase != store.PhaseFailed || st.Verified || st.Error == "" {
		t.Fatalf("expected failed state, got %+v", st)
	}
	if len(ws.List("mesh1", "", "pods", "", nil)) != 1 {
		t.Fatal("committed data was discarded")
	}
}

func TestEncryptedImport(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	var buf bytes.Buffer
	w, _ := bundle.NewWriter(&buf, bundle.WriterOptions{Recipients: []age.Recipient{id.Recipient()}})
	must(t, w.WriteHeader(header()))
	writeObjects(t, w)
	must(t, w.Close(bundle.Manifest{Complete: true}))
	ws := store.New()
	if err := Run(context.Background(), bytes.NewReader(buf.Bytes()), ws, Options{}); err == nil {
		t.Fatal("expected error without identity")
	}
	ws = store.New()
	opts := Options{}
	opts.Reader.Identities = []age.Identity{id}
	must(t, Run(context.Background(), bytes.NewReader(buf.Bytes()), ws, opts))
	if !ws.State().Verified {
		t.Fatal("encrypted import not verified")
	}
}

func TestCancelledImport(t *testing.T) {
	pr, pw := io.Pipe()
	w, _ := bundle.NewWriter(pw, bundle.WriterOptions{})
	ws := store.New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, pr, ws, Options{}) }()
	must(t, w.WriteHeader(header()))
	waitFor(t, "header", func() bool { return ws.Header() != nil })
	cancel()
	_ = pw.CloseWithError(context.Canceled)
	<-done
	if ws.State().Phase != store.PhaseCancelled {
		t.Fatalf("expected cancelled, got %+v", ws.State())
	}
}

func TestThrottleSlowsImport(t *testing.T) {
	var buf bytes.Buffer
	w, _ := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest"})
	must(t, w.WriteHeader(header()))
	writeObjects(t, w)
	writeLogs(t, w)
	must(t, w.Close(bundle.Manifest{Complete: true}))
	size := int64(buf.Len())
	start := time.Now()
	must(t, Run(context.Background(), bytes.NewReader(buf.Bytes()), store.New(), Options{Throttle: size * 4}))
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("throttle had no effect: %v for %d bytes", time.Since(start), size)
	}
}

func fullBundle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, _ := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest"})
	must(t, w.WriteHeader(header()))
	writeObjects(t, w)
	writeLogs(t, w)
	must(t, w.Close(bundle.Manifest{Complete: true}))
	return buf.Bytes()
}

func TestReimportDoesNotDuplicate(t *testing.T) {
	data := fullBundle(t)
	ws := store.New()
	must(t, Run(context.Background(), bytes.NewReader(data), ws, Options{}))
	must(t, Run(context.Background(), bytes.NewReader(data), ws, Options{}))
	if st := ws.State(); st.LogLines != 2 || st.Events != 1 || st.Objects != 1 || !st.Verified {
		t.Fatalf("re-import duplicated data: %+v", st)
	}
}

func TestDeferredCategoriesLoadLater(t *testing.T) {
	data := fullBundle(t)
	ws := store.New()
	must(t, Run(context.Background(), bytes.NewReader(data), ws, Options{Skip: map[string]bool{"logs": true}}))
	st := ws.State()
	if st.LogLines != 0 || st.Objects != 1 || !st.Verified || len(st.Deferred) != 1 || st.Deferred[0] != "logs" {
		t.Fatalf("logs should be deferred but verified: %+v", st)
	}
	must(t, Run(context.Background(), bytes.NewReader(data), ws, Options{}))
	st = ws.State()
	if st.LogLines != 2 || st.Objects != 1 || len(st.Deferred) != 0 {
		t.Fatalf("deferred logs not loaded later: %+v", st)
	}
}

// A flow log attached at collection is imported once, even when the bundle
// is loaded again (Reload), and is marked as part of the signed bundle.
func TestImportAttachedFlows(t *testing.T) {
	var buf bytes.Buffer
	w, err := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest"})
	must(t, err)
	must(t, w.WriteHeader(header()))
	name := bundle.FlowsPath("mesh1", "aws-vpc", "vpc.log")
	must(t, w.Add(name, []byte("2 123456789010 eni-1235b8ca123456789 172.31.16.139 172.31.16.21 20641 22 6 20 4249 1418530010 1418530070 ACCEPT OK\n")))
	must(t, w.Add(bundle.FlowsPath("mesh1", "bogus", "junk.csv"), []byte("not,a,flow\n")))
	must(t, w.Close(bundle.Manifest{Complete: true}))
	ws := store.New()
	for i := 0; i < 2; i++ {
		must(t, Run(context.Background(), bytes.NewReader(buf.Bytes()), ws, Options{}))
	}
	fs := ws.FlowsFrom("mesh1", "aws-vpc")
	if len(fs) != 1 || fs[0].Count != 1 || fs[0].Origin != store.OriginBundle || fs[0].File != "vpc.log" || fs[0].DstPort != 22 {
		t.Fatalf("%+v", fs)
	}
	if !ws.State().Verified {
		t.Fatalf("a bad flow file must not fail the import: %+v", ws.State())
	}
	if !strings.Contains(string(bytes.Join(func() [][]byte {
		var o [][]byte
		for _, l := range ws.CollectionLog() {
			o = append(o, l)
		}
		return o
	}(), nil)), "import-flows") {
		t.Fatal("bad flow file not recorded in the collection log")
	}
}
