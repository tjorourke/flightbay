package hosted

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
)

var podsRes = bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}

// makeBundle returns a bundle for caseID and the length of a prefix that
// already holds the header and the objects (the logs come after it).
func makeBundle(t *testing.T, caseID string) ([]byte, int) {
	t.Helper()
	var buf bytes.Buffer
	w, err := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := policy.Profile("standard")
	pb, _ := json.Marshal(p)
	must(t, w.WriteHeader(bundle.Header{Case: caseID, Contexts: []string{"mesh1"}, Policy: pb, CaptureStart: time.Now()}))
	must(t, w.AddJSON(bundle.DiscoveryPath("mesh1", "apiresources.json"), []bundle.APIResource{podsRes}))
	rp := bundle.ResourcePath("mesh1", "", "v1", "pods", 1)
	must(t, w.AddJSON(rp, map[string]any{"items": []any{map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "checkout-1", "namespace": "payments"}}}}))
	must(t, w.Coverage(bundle.Coverage{ID: "mesh1.resource.core.v1.pods", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", Chunks: []string{rp}, Items: 1, State: bundle.StateComplete}))
	must(t, w.Flush())
	prefix := buf.Len()
	must(t, w.Add(bundle.LogPath("mesh1", "payments", "checkout-1", "app", "current", 1), []byte(strings.Repeat("2026-09-27T10:07:12Z ERROR database connection timed out\n", 200))))
	must(t, w.Close(bundle.Manifest{}))
	return buf.Bytes(), prefix
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func (c client) do(method, path, who string, groups string, body []byte, hdr map[string]string) (int, []byte, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, bytes.NewReader(body))
	if who != "" {
		req.Header.Set("X-Forwarded-Email", who)
		req.Header.Set("X-Forwarded-Groups", groups)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func setup(t *testing.T) (*Hosted, client, string) {
	dir := t.TempDir()
	acl := filepath.Join(dir, "acl.yaml")
	must(t, os.WriteFile(acl, []byte("cases:\n  \"0001\":\n    users: [cust-engineer@example.com]\n"), 0o600))
	h, err := New(Config{DataDir: filepath.Join(dir, "data"), UserHeader: "X-Forwarded-Email", GroupsHeader: "X-Forwarded-Groups",
		SupportGroups: []string{"solo-support"}, ACLFile: acl, Retention: 24 * time.Hour})
	must(t, err)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(func() { srv.Close(); h.Close() })
	return h, client{t, srv}, filepath.Join(dir, "data")
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestUploadOpensWhileArrivingAndResumes(t *testing.T) {
	_, c, data := setup(t)
	if code, _, _ := c.do("GET", "/api/bundles", "", "", nil, nil); code != 401 {
		t.Fatalf("no identity accepted: %d", code)
	}
	b, prefix := makeBundle(t, "0001")
	code, body, _ := c.do("POST", "/api/uploads?case=0001&name=x.tar.zst", "alice@solo.io", "solo-support", nil, nil)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var up struct{ ID, Upload, Workspace string }
	_ = json.Unmarshal(body, &up)

	// First chunk: the header and objects only.
	if code, body, _ := c.do("PUT", up.Upload, "alice@solo.io", "solo-support", b[:prefix], map[string]string{"Upload-Offset": "0"}); code != 200 {
		t.Fatalf("chunk 1: %d %s", code, body)
	}
	waitFor(t, "objects browsable while the upload is incomplete", func() bool {
		_, body, _ := c.do("GET", up.Workspace+"api/objects?resource=pods", "alice@solo.io", "solo-support", nil, nil)
		return strings.Contains(string(body), "checkout-1")
	})
	_, ws, _ := c.do("GET", up.Workspace+"api/workspace", "alice@solo.io", "solo-support", nil, nil)
	if !strings.Contains(string(ws), `"phase":"loading"`) {
		t.Fatalf("workspace should still be loading: %s", ws)
	}
	// A chunk at the wrong offset is refused and the offset reported.
	code, _, hdr := c.do("PUT", up.Upload, "alice@solo.io", "solo-support", b[prefix:], map[string]string{"Upload-Offset": "5"})
	if code != 409 || hdr.Get("Upload-Offset") != strconv.Itoa(prefix) {
		t.Fatalf("wrong offset: %d %s", code, hdr.Get("Upload-Offset"))
	}
	_, _, hdr = c.do("HEAD", up.Upload, "alice@solo.io", "solo-support", nil, nil)
	if hdr.Get("Upload-Offset") != strconv.Itoa(prefix) {
		t.Fatalf("HEAD offset %s", hdr.Get("Upload-Offset"))
	}
	if code, body, _ := c.do("PUT", up.Upload, "alice@solo.io", "solo-support", b[prefix:], map[string]string{"Upload-Offset": strconv.Itoa(prefix), "Upload-Complete": "true"}); code != 200 {
		t.Fatalf("chunk 2: %d %s", code, body)
	}
	waitFor(t, "verified after the upload completes", func() bool {
		_, ws, _ := c.do("GET", up.Workspace+"api/workspace", "alice@solo.io", "solo-support", nil, nil)
		return strings.Contains(string(ws), `"verified":true`)
	})
	_, logs, _ := c.do("GET", up.Workspace+"api/logs/search?q=timed", "alice@solo.io", "solo-support", nil, nil)
	if !strings.Contains(string(logs), `"total":200`) {
		t.Fatalf("logs: %.200s", logs)
	}
	if code, _, _ := c.do("GET", up.Workspace+"api/terminal", "alice@solo.io", "solo-support", nil, nil); code != 403 {
		t.Fatalf("terminal must be unavailable in hosted mode, got %d", code)
	}
	// The UI is served under the workspace prefix.
	if _, page, _ := c.do("GET", up.Workspace, "alice@solo.io", "solo-support", nil, nil); !strings.Contains(string(page), `href="static/app.css"`) {
		t.Fatal("workspace UI not served with relative asset paths")
	}
	audit, _ := os.ReadFile(filepath.Join(data, "audit.jsonl"))
	for _, a := range []string{`"action":"upload-start"`, `"action":"upload-complete"`, `"action":"open"`} {
		if !strings.Contains(string(audit), a) {
			t.Fatalf("audit missing %s:\n%s", a, audit)
		}
	}
}

func TestCaseIsolation(t *testing.T) {
	h, c, data := setup(t)
	b, _ := makeBundle(t, "0002")
	_, body, _ := c.do("POST", "/api/uploads?case=0002", "alice@solo.io", "solo-support", nil, nil)
	var up struct{ ID, Upload, Workspace string }
	_ = json.Unmarshal(body, &up)
	c.do("PUT", up.Upload, "alice@solo.io", "solo-support", b, map[string]string{"Upload-Offset": "0", "Upload-Complete": "true"})

	// The customer engineer has case 0001 only.
	if code, _, _ := c.do("POST", "/api/uploads?case=0002", "cust-engineer@example.com", "", nil, nil); code != 403 {
		t.Fatalf("upload to a case without access: %d", code)
	}
	if _, list, _ := c.do("GET", "/api/bundles", "cust-engineer@example.com", "", nil, nil); strings.Contains(string(list), up.ID) {
		t.Fatalf("bundle of another case listed: %s", list)
	}
	for _, p := range []string{up.Workspace, up.Workspace + "api/workspace", "/w/" + up.ID + "/download"} {
		if code, _, _ := c.do("GET", p, "cust-engineer@example.com", "", nil, nil); code != 404 {
			t.Fatalf("%s reachable without access: %d", p, code)
		}
	}
	if code, _, _ := c.do("DELETE", "/api/bundles/"+up.ID, "cust-engineer@example.com", "", nil, nil); code != 404 {
		t.Fatalf("delete without access: %d", code)
	}
	audit, _ := os.ReadFile(filepath.Join(data, "audit.jsonl"))
	if !strings.Contains(string(audit), `"action":"denied-open"`) {
		t.Fatal("denied access not audited")
	}
	// Retention removes it once it is old enough.
	h.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	h.Sweep()
	if _, list, _ := c.do("GET", "/api/bundles", "alice@solo.io", "solo-support", nil, nil); strings.Contains(string(list), up.ID) {
		t.Fatal("retention did not delete the bundle")
	}
	if _, err := os.Stat(filepath.Join(data, "bundles", up.ID+".tar.zst")); !os.IsNotExist(err) {
		t.Fatal("bundle file left behind")
	}
}

func TestCaseMismatchRejected(t *testing.T) {
	_, c, _ := setup(t)
	b, _ := makeBundle(t, "9999")
	_, body, _ := c.do("POST", "/api/uploads?case=0001", "alice@solo.io", "solo-support", nil, nil)
	var up struct{ ID, Upload, Workspace string }
	_ = json.Unmarshal(body, &up)
	c.do("PUT", up.Upload, "alice@solo.io", "solo-support", b, map[string]string{"Upload-Offset": "0", "Upload-Complete": "true"})
	waitFor(t, "rejection", func() bool {
		_, list, _ := c.do("GET", "/api/bundles", "alice@solo.io", "solo-support", nil, nil)
		return strings.Contains(string(list), `"status":"rejected"`)
	})
}

func TestReloadAfterRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir, UserHeader: "X-Forwarded-Email", GroupsHeader: "X-Forwarded-Groups", SupportGroups: []string{"solo-support"}}
	h, err := New(cfg)
	must(t, err)
	srv := httptest.NewServer(h.Handler())
	c := client{t, srv}
	b, _ := makeBundle(t, "0003")
	_, body, _ := c.do("POST", "/api/uploads?case=0003", "alice@solo.io", "solo-support", nil, nil)
	var up struct{ ID, Upload, Workspace string }
	_ = json.Unmarshal(body, &up)
	c.do("PUT", up.Upload, "alice@solo.io", "solo-support", b, map[string]string{"Upload-Offset": "0", "Upload-Complete": "true"})
	srv.Close()
	h.Close()

	h2, err := New(cfg)
	must(t, err)
	srv2 := httptest.NewServer(h2.Handler())
	defer func() { srv2.Close(); h2.Close() }()
	c2 := client{t, srv2}
	waitFor(t, "workspace rebuilt from the stored bundle", func() bool {
		_, ws, _ := c2.do("GET", up.Workspace+"api/workspace", "alice@solo.io", "solo-support", nil, nil)
		return strings.Contains(string(ws), `"verified":true`)
	})
}
