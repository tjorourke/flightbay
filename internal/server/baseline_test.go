package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/store"
)

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, srv string, path, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", srv+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestBaselineCandidatesAndLoad(t *testing.T) {
	s, srv := newServer(t, false)
	dir := t.TempDir()
	open, before := filepath.Join(dir, "flightbay-open.tar.zst"), filepath.Join(dir, "flightbay-before.tar.zst")
	copyFile(t, "../../examples/sample-bundle.tar.zst", open)
	copyFile(t, "../../examples/sample-bundle.tar.zst", before)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.BundlePath, s.BaselineDirs = open, []string{dir}

	var noLoad map[string]any
	get(t, srv, "/api/baseline", &noLoad)
	if noLoad["available"] != false || noLoad["canLoad"] != false {
		t.Fatalf("baseline before any load: %v", noLoad)
	}
	if code := postJSON(t, srv.URL, "/api/baseline/load", `{"path":"`+before+`"}`); code != http.StatusConflict {
		t.Fatalf("load without a loader: %d", code)
	}

	var cands struct {
		Candidates []candidate `json:"candidates"`
	}
	get(t, srv, "/api/baseline/candidates", &cands)
	var got *candidate
	for i, c := range cands.Candidates {
		if c.Path == open {
			t.Fatal("the open bundle is listed as a candidate")
		}
		if strings.HasSuffix(c.Path, "notes.txt") {
			t.Fatal("non-bundle file listed")
		}
		if c.Path == before {
			got = &cands.Candidates[i]
		}
	}
	if got == nil {
		t.Fatalf("before bundle not listed: %+v", cands.Candidates)
	}
	if got.Case != "DEMO-0001" || len(got.Clusters) != 1 || got.Captured.IsZero() || got.Size == 0 || got.SameCluster {
		t.Fatalf("candidate header: %+v", got)
	}

	release := make(chan struct{})
	s.LoadBaseline = func(p string) error {
		go func() {
			<-release
			f, err := os.Open(p)
			if err != nil {
				s.BaselineFailed(p, err)
				return
			}
			defer f.Close()
			ws := store.New()
			if err := importer.Run(context.Background(), f, ws, importer.Options{Source: p}); err != nil {
				s.BaselineFailed(p, err)
				return
			}
			s.SetBaseline(ws, p)
		}()
		return nil
	}
	for body, want := range map[string]int{
		`{"path":"` + filepath.Join(dir, "missing.tar.zst") + `"}`: http.StatusBadRequest,
		`{"path":"` + open + `"}`:                                  http.StatusBadRequest,
		`{"path":"` + filepath.Join(dir, "notes.txt") + `"}`:       http.StatusBadRequest,
		`{}`: http.StatusBadRequest,
	} {
		if code := postJSON(t, srv.URL, "/api/baseline/load", body); code != want {
			t.Fatalf("load %s: %d, want %d", body, code, want)
		}
	}
	if code := postJSON(t, srv.URL, "/api/baseline/load", `{"path":"`+before+`"}`); code != http.StatusAccepted {
		t.Fatalf("load: %d", code)
	}
	var st map[string]any
	get(t, srv, "/api/baseline", &st)
	if st["loading"] != true || st["available"] != false || st["loadingPath"] != before {
		t.Fatalf("while loading: %v", st)
	}
	close(release)
	deadline := time.Now().Add(20 * time.Second)
	for {
		st = nil
		get(t, srv, "/api/baseline", &st)
		if st["available"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("baseline never became available: %v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st["path"] != before || st["loading"] != false || st["report"] == nil {
		t.Fatalf("after load: %v", st)
	}
	get(t, srv, "/api/baseline/candidates", &cands)
	for _, c := range cands.Candidates {
		if c.Path == before && !c.Current {
			t.Fatal("loaded baseline not marked current")
		}
	}
}

func TestSuggestAPI(t *testing.T) {
	_, srv := newServer(t, false)
	var r struct {
		Field  string             `json:"field"`
		Values []store.Suggestion `json:"values"`
	}
	get(t, srv, "/api/suggest?scope=objects&field=kind", &r)
	if len(r.Values) == 0 || r.Values[0].Value == "" {
		t.Fatalf("kinds: %+v", r)
	}
	get(t, srv, "/api/suggest?scope=objects&field=label&prefix=app%3D&q=kind%3Apod", &r)
	if len(r.Values) != 1 || r.Values[0].Value != "app=checkout" {
		t.Fatalf("label values: %+v", r.Values)
	}
	get(t, srv, "/api/suggest?scope=logs&field=ns", &r)
	if len(r.Values) != 1 || r.Values[0].Value != "payments" || r.Values[0].Count != 3 {
		t.Fatalf("log namespaces: %+v", r.Values)
	}
	get(t, srv, "/api/suggest?scope=logs&field=", &r)
	if len(r.Values) < 5 {
		t.Fatalf("log keys: %+v", r.Values)
	}
	if code := get(t, srv, "/api/suggest?scope=bogus&field=x", nil); code != 400 {
		t.Fatalf("bad scope: %d", code)
	}
}
