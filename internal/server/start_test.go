package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const startTok = "0123456789abcdef0123456789abcdef0123456789abcdef"

const sampleBundle = "../../examples/sample-bundle.tar.zst"

type startEnv struct {
	s        *StartServer
	url      string
	launched chan StartAction
	dir      string
}

func newStart(t *testing.T) *startEnv {
	t.Helper()
	e := &startEnv{dir: t.TempDir(), launched: make(chan StartAction, 4)}
	e.s = &StartServer{Token: startTok, WorkDir: t.TempDir(), Dirs: []string{e.dir}, Version: "test",
		Launch: func(a StartAction) error { e.launched <- a; return nil }}
	ts := httptest.NewServer(e.s.Handler())
	t.Cleanup(ts.Close)
	e.url = ts.URL
	return e
}

func (e *startEnv) do(t *testing.T, method, path, body string, authed bool) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.url+path, strings.NewReader(body))
	if authed {
		req.Header.Set("Authorization", "Bearer "+startTok)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *startEnv) wantLaunch(t *testing.T) StartAction {
	t.Helper()
	select {
	case a := <-e.launched:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("Launch was not called")
	}
	return StartAction{}
}

func TestStartNeedsTheToken(t *testing.T) {
	e := newStart(t)
	for _, p := range []string{"/", "/static/start.js", "/api/start", "/api/start/contexts", "/api/start/recent"} {
		if resp, _ := e.do(t, "GET", p, "", false); resp.StatusCode != 401 {
			t.Errorf("GET %s without the token: %d, want 401", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/api/start/live-plan", "/api/start/live", "/api/start/upload?name=a.tar.zst", "/api/start/open", "/api/start/scan"} {
		if resp, _ := e.do(t, "POST", p, "{}", false); resp.StatusCode != 401 {
			t.Errorf("POST %s without the token: %d, want 401", p, resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "GET", "/?token=wrong", "", false); resp.StatusCode != 401 {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	// The token in the URL sets the per-port cookie, as the desk does.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(e.url + "/?token=" + startTok)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || len(resp.Cookies()) != 1 || !strings.HasPrefix(resp.Cookies()[0].Name, CookieName) {
		t.Fatalf("token login: %d %v", resp.StatusCode, resp.Cookies())
	}
	req, _ := http.NewRequest("GET", e.url+"/", nil)
	req.AddCookie(resp.Cookies()[0])
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<h1>Open a workspace</h1>") || !strings.Contains(string(body), "static/start.js") {
		t.Fatalf("start page not served: %.200s", body)
	}
	// There is no workspace: the desk's APIs do not exist here, so the
	// page's poll only succeeds once the next desk answers.
	for _, p := range []string{"/api/workspace", "/api/objects", "/k8s/api", "/mcp"} {
		if resp, _ := e.do(t, "GET", p, "", true); resp.StatusCode != 404 {
			t.Errorf("GET %s on the start page: %d, want 404", p, resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "GET", "/favicon.svg", "", false); resp.StatusCode != 200 {
		t.Errorf("favicon: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/docs/getting-started/", "", false); resp.StatusCode != 200 {
		t.Errorf("docs: %d", resp.StatusCode)
	}
}

func TestStartRefusesCrossOrigin(t *testing.T) {
	e := newStart(t)
	req, _ := http.NewRequest("POST", e.url+"/api/start/open", strings.NewReader(`{"path":"x"}`))
	req.Header.Set("Authorization", "Bearer "+startTok)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin POST: %d", resp.StatusCode)
	}
}

func TestStartRecentListsBundles(t *testing.T) {
	e := newStart(t)
	copyFile(t, sampleBundle, filepath.Join(e.dir, "flightbay-DEMO-20260927T1842Z.tar.zst"))
	_ = os.WriteFile(filepath.Join(e.dir, "other.tar.zst"), []byte("not ours"), 0o600)      // not flightbay-*
	_ = os.WriteFile(filepath.Join(e.dir, "flightbay-x.tar.zst.age"), []byte("age"), 0o600) // listed, unreadable
	_ = os.WriteFile(filepath.Join(e.dir, "notes.txt"), []byte("x"), 0o600)
	resp, b := e.do(t, "GET", "/api/start/recent", "", true)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var out struct {
		Files []candidate `json:"files"`
		Dirs  []string    `json:"dirs"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	names := map[string]candidate{}
	for _, f := range out.Files {
		names[f.Name] = f
	}
	demo, ok := names["flightbay-DEMO-20260927T1842Z.tar.zst"]
	if !ok || len(names) != 2 {
		t.Fatalf("recent files: %v", names)
	}
	if demo.Unreadable != "" || len(demo.Clusters) == 0 || demo.Captured.IsZero() || demo.Size == 0 {
		t.Errorf("header not read: %+v", demo)
	}
	if _, ok := names["flightbay-x.tar.zst.age"]; !ok {
		t.Error("encrypted file not listed")
	}
	if len(out.Dirs) != 2 || !strings.HasSuffix(out.Dirs[1], "uploads") {
		t.Errorf("dirs: %v", out.Dirs)
	}
}

func upload(t *testing.T, e *startEnv, name string, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.url+"/api/start/upload?name="+name, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+startTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestStartUpload(t *testing.T) {
	e := newStart(t)
	sample, err := os.ReadFile(sampleBundle)
	if err != nil {
		t.Fatal(err)
	}
	if code, b := upload(t, e, "notes.txt", []byte("x")); code != 400 || !strings.Contains(b, "only Flightbay bundles") {
		t.Errorf("other extension: %d %s", code, b)
	}
	if code, b := upload(t, e, "flightbay-junk.tar.zst", []byte("not zstd")); code != 400 || !strings.Contains(b, "not a Flightbay bundle") {
		t.Errorf("junk bundle: %d %s", code, b)
	}
	code, b := upload(t, e, "../../flightbay-DEMO.tar.zst", sample)
	if code != 200 {
		t.Fatalf("upload: %d %s", code, b)
	}
	var out struct{ File candidate }
	_ = json.Unmarshal([]byte(b), &out)
	if out.File.Path != filepath.Join(e.s.WorkDir, "uploads", "flightbay-DEMO.tar.zst") || len(out.File.Clusters) == 0 {
		t.Errorf("uploaded file: %+v", out.File)
	}
	if st, err := os.Stat(out.File.Path); err != nil || st.Size() != int64(len(sample)) {
		t.Errorf("uploaded file on disk: %v", err)
	}
	// It shows up as a recent file.
	if _, rb := e.do(t, "GET", "/api/start/recent", "", true); !strings.Contains(string(rb), "flightbay-DEMO.tar.zst") {
		t.Error("the upload is not listed as recent")
	}
	// Over the limit: refused from Content-Length, and while streaming.
	e.s.MaxUpload = 1000
	if code, b := upload(t, e, "flightbay-big.tar.zst", sample); code != 413 {
		t.Errorf("oversize: %d %s", code, b)
	}
	req, _ := http.NewRequest("POST", e.url+"/api/start/upload?name=flightbay-big2.tar.zst", io.MultiReader(bytes.NewReader(sample)))
	req.Header.Set("Authorization", "Bearer "+startTok)
	req.ContentLength = -1 // chunked: the limit applies while reading
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Errorf("oversize, chunked: %d", resp.StatusCode)
	}
	ents, _ := os.ReadDir(filepath.Join(e.s.WorkDir, "uploads"))
	if len(ents) != 1 {
		t.Errorf("refused uploads left files behind: %v", ents)
	}
}

func TestStartOpenHandsOff(t *testing.T) {
	e := newStart(t)
	p := filepath.Join(e.dir, "flightbay-DEMO-20260927T1842Z.tar.zst")
	copyFile(t, sampleBundle, p)
	for _, bad := range []string{`{"path":"/no/such/file.tar.zst"}`, `{"path":"` + e.dir + `"}`, `{}`} {
		if resp, b := e.do(t, "POST", "/api/start/open", bad, true); resp.StatusCode != 400 {
			t.Errorf("%s: %d %s", bad, resp.StatusCode, b)
		}
	}
	junk := filepath.Join(e.dir, "flightbay-junk.tar.zst")
	_ = os.WriteFile(junk, []byte("junk"), 0o600)
	if resp, b := e.do(t, "POST", "/api/start/open", `{"path":"`+junk+`"}`, true); resp.StatusCode != 400 || !strings.Contains(string(b), "not a readable") {
		t.Errorf("junk: %d %s", resp.StatusCode, b)
	}
	resp, b := e.do(t, "POST", "/api/start/open", `{"path":"`+p+`","review":true}`, true)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"opening":true`) {
		t.Fatalf("open: %d %s", resp.StatusCode, b)
	}
	if a := e.wantLaunch(t); a.Kind != "review" || a.Path != p {
		t.Errorf("action: %+v", a)
	}
	// One hand-off at a time.
	if resp, _ := e.do(t, "POST", "/api/start/open", `{"path":"`+p+`"}`, true); resp.StatusCode != 409 {
		t.Errorf("second open while the first is launching: %d", resp.StatusCode)
	}
}

func writeKubeconfig(t *testing.T, server string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config")
	kc := `apiVersion: v1
kind: Config
current-context: fake
clusters:
- name: fake
  cluster: {server: ` + server + `}
- name: other
  cluster: {server: https://other.example:6443}
contexts:
- name: fake
  context: {cluster: fake, user: me, namespace: kagent}
- name: other
  context: {cluster: other, user: me}
users:
- name: me
  user: {token: abc}
`
	if err := os.WriteFile(p, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartContextsAndLive(t *testing.T) {
	e := newStart(t)
	kc := writeKubeconfig(t, "https://127.0.0.1:1")
	resp, b := e.do(t, "GET", "/api/start/contexts?kubeconfig="+kc, "", true)
	var out struct {
		Contexts []kubeContext `json:"contexts"`
		Current  string        `json:"current"`
		Files    []string      `json:"files"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(b, &out) != nil || out.Current != "fake" || len(out.Contexts) != 2 || out.Contexts[0].Server != "https://127.0.0.1:1" || out.Files[0] != kc {
		t.Fatalf("contexts: %d %s", resp.StatusCode, b)
	}
	if resp, b := e.do(t, "POST", "/api/start/live", `{"kubeconfig":"`+kc+`","context":"nope"}`, true); resp.StatusCode != 400 || !strings.Contains(string(b), "not in the kubeconfig") {
		t.Errorf("unknown context: %d %s", resp.StatusCode, b)
	}
	if resp, b := e.do(t, "POST", "/api/start/live-plan", `{"kubeconfig":"`+kc+`","context":"fake"}`, true); resp.StatusCode != 501 {
		t.Errorf("live-plan without a planner: %d %s", resp.StatusCode, b)
	}
	resp, b = e.do(t, "POST", "/api/start/live", `{"kubeconfig":"`+kc+`","namespaces":"kagent, istio-*","soloDiagnostics":true}`, true)
	if resp.StatusCode != 200 {
		t.Fatalf("live: %d %s", resp.StatusCode, b)
	}
	a := e.wantLaunch(t)
	if a.Kind != "live" || a.Live == nil || a.Live.Context != "fake" || a.Live.Namespaces != "kagent,istio-*" || !a.Live.SoloDiagnostics {
		t.Errorf("live action: %+v %+v", a, a.Live)
	}
}

func readNDJSON(t *testing.T, body io.Reader) (logs []string, last map[string]any) {
	t.Helper()
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not NDJSON: %q", sc.Text())
		}
		if l, ok := m["log"].(string); ok {
			logs = append(logs, l)
		} else {
			last = m
		}
	}
	return logs, last
}

func TestStartScan(t *testing.T) {
	e := newStart(t)
	repo, _ := filepath.Abs("../scan/testdata/repo-sample")
	req, _ := http.NewRequest("POST", e.url+"/api/start/scan", strings.NewReader(`{"source":"`+repo+`"}`))
	req.Header.Set("Authorization", "Bearer "+startTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	logs, last := readNDJSON(t, resp.Body)
	resp.Body.Close()
	if last == nil || last["done"] != true || last["opening"] != true {
		t.Fatalf("scan result: %v (logs %v)", last, logs)
	}
	if n, _ := last["objects"].(float64); n < 5 {
		t.Errorf("objects: %v", last["objects"])
	}
	bp, _ := last["bundle"].(string)
	if filepath.Dir(bp) != e.s.WorkDir {
		t.Errorf("bundle not in the workspace: %s", bp)
	}
	if _, err := os.Stat(bp); err != nil {
		t.Error(err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "Scanning") {
		t.Errorf("no progress: %v", logs)
	}
	if a := e.wantLaunch(t); a.Kind != "open" || a.Path != bp || !a.Scanned {
		t.Errorf("action: %+v", a)
	}
}

func TestStartScanErrors(t *testing.T) {
	e := newStart(t)
	e.s.Launch = nil
	scanOf := func(src string) map[string]any {
		req, _ := http.NewRequest("POST", e.url+"/api/start/scan", strings.NewReader(`{"source":"`+src+`"}`))
		req.Header.Set("Authorization", "Bearer "+startTok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, last := readNDJSON(t, resp.Body)
		return last
	}
	if m := scanOf(t.TempDir()); !strings.Contains(m["error"].(string), "no Kubernetes manifests") {
		t.Errorf("empty dir: %v", m)
	}
	if m := scanOf("/no/such/dir"); !strings.Contains(m["error"].(string), "no such directory") {
		t.Errorf("missing dir: %v", m)
	}
	// A chart that does not render: the error lists it from coverage.
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "charts/broken/templates"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "charts/broken/Chart.yaml"), []byte("apiVersion: v2\nname: broken\nversion: 0.1.0\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "charts/broken/templates/d.yaml"), []byte("kind: ConfigMap\napiVersion: v1\nmetadata:\n  name: {{ required \"name is required\" .Values.name }}\n"), 0o644)
	m := scanOf(dir)
	failed, _ := m["failed"].([]any)
	if !strings.Contains(m["error"].(string), "1 input failed") || len(failed) != 1 || !strings.Contains(failed[0].(map[string]any)["reason"].(string), "name is required") {
		t.Errorf("broken chart: %v", m)
	}
	if m := scanOf("file:///no/such/repo.git"); !strings.Contains(m["error"].(string), "could not clone") {
		t.Errorf("unreachable URL: %v", m)
	}
}

func TestIsGitURL(t *testing.T) {
	for s, want := range map[string]bool{
		"https://github.com/org/repo": true, "git@github.com:org/repo.git": true, "ssh://git@host/r": true,
		"/home/me/repo": false, "~/code/deploy": false, "./deploy": false, "C:/deploy": false, "--upload-pack=x": false,
	} {
		if IsGitURL(s) != want {
			t.Errorf("IsGitURL(%q) = %v", s, !want)
		}
	}
}

func TestStartLiveAccess(t *testing.T) {
	e := newStart(t)
	kc := writeKubeconfig(t, "https://127.0.0.1:1")
	body := `{"kubeconfig":"` + kc + `","context":"fake","namespaces":"shop"}`
	if resp, _ := e.do(t, "POST", "/api/start/live-access", body, true); resp.StatusCode != 501 {
		t.Errorf("without a checker: %d", resp.StatusCode)
	}
	var got LiveRequest
	e.s.LiveAccess = func(_ context.Context, r LiveRequest) (LiveAccess, error) {
		got = r
		return LiveAccess{ClusterScoped: true, SoloDiagnostics: false, SoloDiagnosticsWhy: "cannot port-forward"}, nil
	}
	resp, b := e.do(t, "POST", "/api/start/live-access", body, true)
	var a LiveAccess
	if resp.StatusCode != 200 || json.Unmarshal(b, &a) != nil || !a.ClusterScoped || a.SoloDiagnostics || a.SoloDiagnosticsWhy == "" {
		t.Fatalf("access: %d %s", resp.StatusCode, b)
	}
	if got.Context != "fake" || got.Namespaces != "shop" {
		t.Errorf("the checker gets the form: %+v", got)
	}
	if resp, _ := e.do(t, "POST", "/api/start/live-access", body, false); resp.StatusCode != 401 {
		t.Errorf("needs the token: %d", resp.StatusCode)
	}
}
