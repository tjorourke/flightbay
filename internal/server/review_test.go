package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/sign"
	"github.com/tjorourke/flightbay/internal/store"
)

// smallBundle writes a capture with pods in two namespaces and a log stream.
func smallBundle(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, _ := bundle.NewWriter(f, bundle.WriterOptions{})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.WriteHeader(bundle.Header{BundleID: "cap-1", CaptureStart: time.Unix(0, 0).UTC(), Contexts: []string{"mesh1"}}))
	pods := bundle.ResourcePath("mesh1", "", "v1", "pods", 1)
	must(w.AddJSON(pods, map[string]any{"kind": "List", "x-flightbay": map[string]any{"version": "v1", "resource": "pods", "kind": "Pod", "namespaced": true}, "items": []any{
		map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "web", "namespace": "shop"}},
		map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "db", "namespace": "shop"}},
		map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "model", "namespace": "lab"}},
	}}))
	must(w.Coverage(bundle.Coverage{ID: "pods", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", Chunks: []string{pods}, Items: 3, State: bundle.StateComplete}))
	logs := bundle.LogPath("mesh1", "shop", "web", "app", "current", 1)
	must(w.Add(logs, []byte("2026-09-28T10:00:00Z ok\n2026-09-28T10:00:01Z ERROR card 4111 declined\n2026-09-28T10:00:02Z ok again\n")))
	must(w.Coverage(bundle.Coverage{ID: "logs.web", Cluster: "mesh1", Type: bundle.CoverageLogs, Namespace: "shop", Pod: "web", Container: "app", Stream: "current", Chunks: []string{logs}, Items: 3, State: bundle.StateComplete}))
	must(w.Close(bundle.Manifest{Complete: true}))
}

func importFile(t *testing.T, path string, trusted []sign.PublicKey) *store.Workspace {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	if err := importer.Run(context.Background(), f, ws, importer.Options{Trusted: trusted}); err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestReviewFlow(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "flightbay-cap.tar.zst")
	smallBundle(t, src)
	key, _ := sign.Generate("ACME")
	ws := importFile(t, src, nil)
	if tr := ws.Trust(); tr.Signature == nil || tr.Signature.Signed {
		t.Fatalf("an unsigned capture says so: %+v", tr.Signature)
	}
	s := &Server{WS: ws, Token: tok, WorkDir: dir, Review: &ReviewSession{Source: src, Out: review.OutputName(src, false), Options: review.Options{Signer: key}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	post := func(path string, body any, v any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Origin", srv.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(v)
		return resp.StatusCode
	}

	var st struct {
		Signer   map[string]string `json:"signer"`
		Clusters []rvCluster       `json:"clusters"`
	}
	if get(t, srv, "/api/review", &st) != 200 || st.Signer["keyId"] != key.ID() || len(st.Clusters) != 1 || len(st.Clusters[0].Namespaces) != 2 || len(st.Clusters[0].Streams) != 1 {
		t.Fatalf("inventory: %+v", st)
	}

	var lines struct {
		Total int              `json:"total"`
		Lines []review.LineRef `json:"lines"`
	}
	if get(t, srv, "/api/review/lines?q=4111", &lines) != 200 || lines.Total != 1 || lines.Lines[0].Index != 1 {
		t.Fatalf("a log search becomes line references: %+v", lines)
	}
	if code := get(t, srv, "/api/review/lines", nil); code != 400 {
		t.Fatalf("an unfiltered line search is refused: %d", code)
	}

	plan := review.Plan{Reviewer: "Jane", Remove: review.Removals{
		Namespaces: []review.NamespaceRef{{Namespace: "lab"}},
		Objects:    []review.ObjectRef{{Cluster: "mesh1", Resource: "pods", Namespace: "shop", Name: "db"}},
		LogLines:   lines.Lines,
	}}
	var prev struct {
		Ledger review.Ledger `json:"ledger"`
	}
	if code := post("/api/review/preview", plan, &prev); code != 200 || prev.Ledger.Removed.Objects != 2 || prev.Ledger.Removed.LogLines != 1 {
		t.Fatalf("preview: %d %+v", code, prev.Ledger.Removed)
	}
	if _, err := os.Stat(review.OutputName(src, false)); err == nil {
		t.Fatal("a preview writes nothing")
	}
	var ex reviewExport
	if code := post("/api/review/export", plan, &ex); code != 200 || !ex.Signed || ex.SHA256 == "" {
		t.Fatalf("export: %d %+v", code, ex)
	}

	// The recipient opens the export.
	out := importFile(t, ex.Path, []sign.PublicKey{key.Public()})
	tr := out.Trust()
	if tr.Signature == nil || !tr.Signature.Valid || !tr.Signature.Trusted || len(tr.Ledger) == 0 {
		t.Fatalf("trust: %+v", tr)
	}
	if st := out.CollectionState("mesh1", "", "pods", "Pod", true); st.State != store.StateTrimmed || !strings.Contains(st.Reason, "removed by the customer") {
		t.Fatalf("pods with one removed by name are trimmed: %+v", st)
	}
	if n := len(out.List("mesh1", "", "pods", "", nil)); n != 1 {
		t.Fatalf("one pod left, got %d", n)
	}
	if res, _ := out.SearchLogs(store.LogFilter{Query: "4111"}); res.Total != 0 {
		t.Fatal("the picked line is gone")
	}

	// Outside review mode the endpoints say so.
	s.Review = nil
	if code := get(t, srv, "/api/review", nil); code != http.StatusNotFound {
		t.Fatalf("not in review mode: %d", code)
	}
}
