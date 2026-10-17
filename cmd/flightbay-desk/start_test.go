package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/server"
)

func envValue(env []string, k string) (string, bool) {
	for _, kv := range env {
		if kk, v, ok := strings.Cut(kv, "="); ok && kk == k {
			return v, true
		}
	}
	return "", false
}

func flagValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func TestHandoffCommand(t *testing.T) {
	wd := t.TempDir()
	token := strings.Repeat("ab", 24)
	o := startOptions{workdir: wd, identities: []string{"/keys/support.key"}, keep: true}
	t.Setenv(envFixedToken, "stale")

	argv, env := handoffCommand("/bin/flightbay-desk", server.StartAction{Kind: "open", Path: "/b/flightbay-X.tar.zst.age", Identity: "/keys/other.key"}, o, "127.0.0.1:61234", token, true)
	want := []string{"/bin/flightbay-desk", "open", "/b/flightbay-X.tar.zst.age", "--listen", "127.0.0.1:61234", "--workdir", wd, "--no-browser", "--keep", "-i", "/keys/support.key", "-i", "/keys/other.key"}
	if !slices.Equal(argv, want) {
		t.Errorf("open argv:\n got %q\nwant %q", argv, want)
	}
	if v, _ := envValue(env, envFixedToken); v != token {
		t.Errorf("token not handed over in the environment: %q", v)
	}
	if n := strings.Count(strings.Join(env, "\n"), envFixedToken+"="); n != 1 {
		t.Errorf("%s appears %d times", envFixedToken, n)
	}
	if v, _ := envValue(env, envTempWork); v != "1" {
		t.Error("temporary workdir not marked")
	}
	for _, a := range argv {
		if strings.Contains(a, token) {
			t.Fatal("the token is on the command line")
		}
	}

	// Review of an uploaded bundle (inside the workspace) exports to the
	// directory the user is in, not into the workspace that is removed.
	up := filepath.Join(wd, "uploads", "flightbay-X.tar.zst")
	argv, env = handoffCommand("/bin/flightbay-desk", server.StartAction{Kind: "review", Path: up}, startOptions{workdir: wd}, "127.0.0.1:8484", token, false)
	cwd, _ := os.Getwd()
	if argv[1] != "open" || argv[2] != "--review" || argv[3] != up || flagValue(argv, "--review-out") != filepath.Join(cwd, "flightbay-X-reviewed.tar.zst") || !slices.Contains(argv, "--no-browser") {
		t.Errorf("review argv: %q", argv)
	}
	if _, ok := envValue(env, envTempWork); ok {
		t.Error("a workdir the user named must not be marked temporary")
	}
	argv, _ = handoffCommand("/bin/flightbay-desk", server.StartAction{Kind: "review", Path: "/elsewhere/flightbay-X.tar.zst"}, startOptions{workdir: wd}, "127.0.0.1:8484", token, false)
	if slices.Contains(argv, "--review-out") {
		t.Errorf("review of a file outside the workspace keeps the default output: %q", argv)
	}

	argv, _ = handoffCommand("/bin/flightbay-desk", server.StartAction{Kind: "live", Live: &server.LiveRequest{Kubeconfig: "/k/config", Context: "kind-mesh1",
		Namespaces: "kagent,istio-*", ExcludeNamespaces: "kube-system", ClusterScoped: true, Diagnostics: true}}, startOptions{workdir: wd, noTerminal: true}, "127.0.0.1:8484", token, false)
	want = []string{"/bin/flightbay-desk", "live", "--listen", "127.0.0.1:8484", "--workdir", wd, "--no-browser", "--no-terminal",
		"--kubeconfig", "/k/config", "--context", "kind-mesh1", "--namespaces", "kagent,istio-*", "--exclude-namespaces", "kube-system", "--cluster-scoped", "--diagnostics"}
	if !slices.Equal(argv, want) {
		t.Errorf("live argv:\n got %q\nwant %q", argv, want)
	}
	// Every flag the hand-off passes exists on the command it runs.
	for _, sub := range []*cobra.Command{openCmd(), liveCmd()} {
		for _, f := range []string{"listen", "workdir", "no-browser", "keep", "no-terminal", "allow-remote", "shell"} {
			if sub.Flags().Lookup(f) == nil {
				t.Errorf("%s has no --%s", sub.Name(), f)
			}
		}
	}
	for _, f := range []string{"kubeconfig", "context", "namespaces", "exclude-namespaces", "cluster-scoped", "diagnostics"} {
		if liveCmd().Flags().Lookup(f) == nil {
			t.Errorf("live has no --%s", f)
		}
	}
	for _, f := range []string{"review", "review-out", "identity"} {
		if openCmd().Flags().Lookup(f) == nil {
			t.Errorf("open has no --%s", f)
		}
	}
}

func TestDeskTokenFromEnvironment(t *testing.T) {
	tok := strings.Repeat("cd", 24)
	t.Setenv(envFixedToken, tok)
	if got := deskToken(); got != tok {
		t.Fatalf("deskToken = %q", got)
	}
	if _, ok := os.LookupEnv(envFixedToken); ok {
		t.Error("the handed-over token stays in the environment of shells the desk starts")
	}
	if got := deskToken(); got == tok || len(got) != 48 {
		t.Errorf("without the variable a new random token is made: %q", got)
	}
	t.Setenv(envFixedToken, "short")
	if deskToken() == "short" {
		t.Error("a short token is accepted")
	}
}

func TestLiveArgsPreservesMultipleContextAndConfigSelections(t *testing.T) {
	a := liveArgs(server.LiveRequest{Contexts: []string{"mesh1", "mesh2"}, Kubeconfigs: []string{"/tmp/one", "/tmp/two"}})
	if flagValue(a, "--context") != "mesh1,mesh2" || flagValue(a, "--kubeconfig") != "/tmp/one"+string(os.PathListSeparator)+"/tmp/two" {
		t.Fatalf("multiple selections lost during handoff: %v", a)
	}
}

// fakeAPIServer serves just enough discovery for a dry run.
func fakeAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	res := func(gv string, rs ...string) string {
		var items []string
		for _, r := range rs {
			name, kind, _ := strings.Cut(r, ":")
			namespaced := kind != "Namespace" && kind != "Node"
			items = append(items, `{"name":"`+name+`","singularName":"","namespaced":`+map[bool]string{true: "true", false: "false"}[namespaced]+`,"kind":"`+kind+`","verbs":["get","list","watch"]}`)
		}
		return `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"` + gv + `","resources":[` + strings.Join(items, ",") + `]}`
	}
	routes := map[string]string{
		"/version":      `{"major":"1","minor":"31","gitVersion":"v1.31.0"}`,
		"/api":          `{"kind":"APIVersions","versions":["v1"],"serverAddressByClientCIDRs":[{"clientCIDR":"0.0.0.0/0","serverAddress":"127.0.0.1"}]}`,
		"/api/v1":       res("v1", "pods:Pod", "services:Service", "configmaps:ConfigMap", "events:Event", "namespaces:Namespace", "pods/log:Pod"),
		"/apis":         `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"apps","versions":[{"groupVersion":"apps/v1","version":"v1"}],"preferredVersion":{"groupVersion":"apps/v1","version":"v1"}}]}`,
		"/apis/apps/v1": res("apps/v1", "deployments:Deployment"),
	}
	var writes int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
		}
		b, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b))
	}))
	t.Cleanup(func() {
		ts.Close()
		if writes > 0 {
			t.Errorf("the dry run made %d non-GET requests", writes)
		}
	})
	return ts
}

func TestLivePlanFakeCluster(t *testing.T) {
	ts := fakeAPIServer(t)
	kc := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(kc, []byte(`apiVersion: v1
kind: Config
current-context: fake
clusters: [{name: fake, cluster: {server: `+ts.URL+`}}]
contexts: [{name: fake, context: {cluster: fake, user: me}}]
users: [{name: me, user: {token: abc}}]
`), 0o600)
	raw, err := livePlan(context.Background(), server.LiveRequest{Kubeconfig: kc, Context: "fake", Namespaces: "kagent"})
	if err != nil {
		t.Fatal(err)
	}
	var pl collect.DryRunPlan
	if err := json.Unmarshal(raw, &pl); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	if pl.Cluster != "fake" || !slices.Equal(pl.Namespaces, []string{"kagent"}) || !slices.Contains(pl.Resources, "pods") || !slices.Contains(pl.Resources, "deployments.apps") {
		t.Errorf("plan: %+v", pl)
	}
	if !strings.Contains(pl.RBAC, "watch") || !strings.Contains(pl.RBAC, "namespace: kagent") {
		t.Errorf("live RBAC should add watch and be scoped to kagent:\n%s", pl.RBAC)
	}
	// Through the start server, as the Preview button calls it.
	ss := &server.StartServer{Token: strings.Repeat("ef", 24), WorkDir: t.TempDir(), LivePlan: livePlan}
	hs := httptest.NewServer(ss.Handler())
	defer hs.Close()
	req, _ := http.NewRequest("POST", hs.URL+"/api/start/live-plan", strings.NewReader(`{"kubeconfig":"`+kc+`","namespaces":"kagent"}`))
	req.Header.Set("Authorization", "Bearer "+ss.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Plan collect.DryRunPlan `json:"plan"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Plan.Cluster != "fake" || out.Plan.RBAC == "" {
		t.Errorf("live-plan endpoint: %d %+v", resp.StatusCode, out.Plan)
	}
}

func TestLivePlanUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens here
	kc := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(kc, []byte("apiVersion: v1\nkind: Config\ncurrent-context: gone\nclusters: [{name: gone, cluster: {server: http://"+addr+"}}]\ncontexts: [{name: gone, context: {cluster: gone, user: me}}]\nusers: [{name: me, user: {token: abc}}]\n"), 0o600)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := livePlan(ctx, server.LiveRequest{Kubeconfig: kc, Context: "gone"}); err == nil {
		t.Fatal("a plan for an unreachable cluster")
	}
}

// TestStartHandsOverOnTheSameAddress runs the start page for real and
// captures the exec: the next desk gets the address the page listened on,
// the page's token, and the listener has been released for it.
func TestStartHandsOverOnTheSameAddress(t *testing.T) {
	t.Setenv("FLIGHTBAY_HOME", t.TempDir())
	type call struct{ argv, env []string }
	got := make(chan call, 1)
	old := execDesk
	execDesk = func(_ string, argv, env []string) error { got <- call{argv, env}; return nil }
	defer func() { execDesk = old }()

	wd := t.TempDir()
	bundle := filepath.Join(t.TempDir(), "flightbay-DEMO-20260927T1842Z.tar.zst")
	b, _ := os.ReadFile("../../examples/sample-bundle.tar.zst")
	_ = os.WriteFile(bundle, b, 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetErr(&lockedWriter{}) // the page and its hand-off both write progress
	done := make(chan error, 1)
	go func() {
		done <- runStart(cmd, startOptions{listen: "127.0.0.1:0", workdir: wd, noBrowser: true})
	}()
	var info deskInfo
	deadline := time.Now().Add(10 * time.Second)
	for info.URL == "" && time.Now().Before(deadline) {
		if raw, err := os.ReadFile(filepath.Join(wd, "desk.json")); err == nil {
			_ = json.Unmarshal(raw, &info)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if info.URL == "" || info.Bundle != "start" {
		t.Fatalf("desk.json: %+v", info)
	}
	req, _ := http.NewRequest("POST", info.URL+"/api/start/open", strings.NewReader(`{"path":"`+bundle+`"}`))
	req.Header.Set("Authorization", "Bearer "+info.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("open: %d", resp.StatusCode)
	}
	var c call
	select {
	case c = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("no exec")
	}
	addr := strings.TrimPrefix(info.URL, "http://")
	if c.argv[1] != "open" || c.argv[2] != bundle || flagValue(c.argv, "--listen") != addr || flagValue(c.argv, "--workdir") != wd || !slices.Contains(c.argv, "--no-browser") {
		t.Errorf("argv: %q", c.argv)
	}
	if v, _ := envValue(c.env, envFixedToken); v != info.Token {
		t.Errorf("token handed over: %q, want the page's %q", v, info.Token)
	}
	// The start page has let go of the address for the next desk.
	if ln, err := net.Listen("tcp", addr); err != nil {
		t.Errorf("the address is still held: %v", err)
	} else {
		ln.Close()
	}
	cancel()
	if err := <-done; err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(wd); err != nil {
		t.Error("a workdir the user named was removed")
	}
}

// lockedWriter is a strings.Builder safe for the start page and its hand-off
// goroutine to write at once (stderr is, in real use).
type lockedWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
