package collect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
	"github.com/tjorourke/flightbay/internal/store"
)

type liveLogs struct {
	mu    sync.Mutex
	calls []string
}

func (f *liveLogs) Stream(_ context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	f.calls = append(f.calls, ns+"/"+pod+"/"+o.Container)
	f.mu.Unlock()
	if o.Previous {
		return io.NopCloser(strings.NewReader("2026-09-28T09:00:00Z previous run died token=" + canary + "\n")), nil
	}
	return io.NopCloser(strings.NewReader("2026-09-28T09:00:01Z hello\n2026-09-28T09:00:02Z Authorization: Bearer " + canary + "\n")), nil
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLiveMirrorsListWatchAndLogs(t *testing.T) {
	h := newHarness()
	lg := &liveLogs{}
	cl := h.clients()
	cl.Logs = lg
	p, _ := policy.Profile("standard")
	p.Namespaces = []string{"payments"}
	rd, _ := redact.New(nil)
	ws := store.New()
	l := &Live{C: &Collector{Policy: p, Redactor: rd, Version: "test"}, WS: ws, CL: cl,
		O: LiveOptions{Context: "kind-test", FlushEvery: 50 * time.Millisecond}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	waitFor(t, "phase live", func() bool { return ws.State().Phase == store.PhaseLive })
	if !ws.IsLive() || ws.Header().Source != store.SourceLive || ws.Header().Contexts[0] != "kind-test" {
		t.Fatalf("header %+v", ws.Header())
	}
	st := ws.State()
	if st.Live == nil || !st.Live.Live || st.Live.Context != "kind-test" {
		t.Fatalf("live state %+v", st.Live)
	}
	if ws.Get("kind-test", "", "pods", "payments", "checkout-1") == nil || ws.Get("kind-test", "", "pods", "kube-system", "coredns-1") != nil {
		t.Fatal("scope not applied to the initial list")
	}
	if len(ws.Events("kind-test")) != 1 {
		t.Fatalf("events: %d", len(ws.Events("kind-test")))
	}
	// A new pod through the watch.
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	if err := h.dyn.Tracker().Create(pods, crashPod("payments", "checkout-2"), "payments"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "watched pod", func() bool { return ws.Get("kind-test", "", "pods", "payments", "checkout-2") != nil })
	// An update and a delete.
	upd := crashPod("payments", "checkout-2")
	upd.Object["metadata"].(map[string]any)["labels"] = map[string]any{"app": "checkout", "rev": "2"}
	upd.Object["metadata"].(map[string]any)["resourceVersion"] = "2"
	if err := h.dyn.Tracker().Update(pods, upd, "payments"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "update", func() bool {
		o := ws.Get("kind-test", "", "pods", "payments", "checkout-2")
		return o != nil && o.Labels()["rev"] == "2"
	})
	if err := h.dyn.Tracker().Delete(pods, "payments", "checkout-2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "delete", func() bool { return ws.Get("kind-test", "", "pods", "payments", "checkout-2") == nil })
	// A pod outside the scope is ignored.
	_ = h.dyn.Tracker().Create(pods, crashPod("kube-system", "sneaky-1"), "kube-system")
	time.Sleep(200 * time.Millisecond)
	if ws.Get("kind-test", "", "pods", "kube-system", "sneaky-1") != nil {
		t.Fatal("out-of-scope pod mirrored")
	}
	// Logs: the current stream is followed and the restarted container's
	// previous log fetched once; both redacted.
	waitFor(t, "current logs", func() bool { return len(ws.LogStream("kind-test", "payments", "checkout-1", "app", "current")) >= 2 })
	waitFor(t, "previous logs", func() bool { return len(ws.LogStream("kind-test", "payments", "checkout-1", "app", "previous")) >= 1 })
	for _, s := range []string{"current", "previous"} {
		for _, r := range ws.LogStream("kind-test", "payments", "checkout-1", "app", s) {
			if strings.Contains(r.Raw, canary) {
				t.Fatalf("%s log not redacted: %q", s, r.Raw)
			}
		}
	}
	// Redaction of objects on the way in.
	b, _ := json.Marshal(ws.Get("kind-test", "", "pods", "payments", "checkout-1").Obj)
	if strings.Contains(string(b), canary) {
		t.Fatal("pod env value not redacted")
	}
	for _, s := range []*store.Object{ws.Get("kind-test", "", "secrets", "payments", "db")} {
		if s == nil {
			continue
		}
		b, _ := json.Marshal(s.Obj)
		if strings.Contains(string(b), canary) || strings.Contains(string(b), "data\":{\"password\":\"") {
			t.Fatalf("secret value in the workspace: %s", b)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestLiveStreamCapPrefersUnhealthyPods(t *testing.T) {
	p, _ := policy.Profile("standard")
	rd, _ := redact.New(nil)
	l := &Live{C: &Collector{Policy: p, Redactor: rd}, WS: store.New(), CL: Clients{Logs: &liveLogs{}}, O: LiveOptions{Context: "c", MaxLogStreams: 2}}
	l.O.defaults()
	l.cluster = "c"
	m := newLogManager(l)
	pod := func(name string, ready bool, restarts int64) map[string]any {
		return map[string]any{"metadata": map[string]any{"name": name, "namespace": "demo"},
			"spec":   map[string]any{"containers": []any{map[string]any{"name": "app"}}},
			"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app", "ready": ready, "restartCount": restarts}}}}
	}
	for _, n := range []string{"a", "b", "c"} {
		m.podUpdate(pod(n, true, 0))
	}
	m.podUpdate(pod("z-crashy", true, 4))
	m.podUpdate(pod("y-unready", false, 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.reconcile(ctx)
	followed, total, _ := m.counts()
	if followed != 2 || total != 5 {
		t.Fatalf("followed %d of %d", followed, total)
	}
	m.mu.Lock()
	_, crashy := m.followers[streamRef{"demo", "z-crashy", "app"}]
	_, unready := m.followers[streamRef{"demo", "y-unready", "app"}]
	m.mu.Unlock()
	if !crashy || !unready {
		t.Fatal("restarting and unready pods should be followed first")
	}
	c, ok := l.WS.CoverageRecord("c.logs.demo.a.app.current")
	if !ok || c.State != "truncated" || !strings.Contains(c.Reason, "not followed") {
		t.Fatalf("a stream over the cap must say so in coverage: %+v", c)
	}
}

func TestReadOnlyGuard(t *testing.T) {
	g := &ReadOnlyGuard{}
	for _, tc := range []struct {
		method, path, watch, verb string
		ok                        bool
	}{
		{"GET", "/api", "", "discovery", true},
		{"GET", "/apis/apps/v1", "", "discovery", true},
		{"GET", "/api/v1/namespaces/demo/pods", "", "list", true},
		{"GET", "/api/v1/namespaces/demo/pods", "true", "watch", true},
		{"GET", "/api/v1/namespaces/demo/pods/web-1", "", "get", true},
		{"GET", "/api/v1/namespaces/demo/pods/web-1/log", "", "log", true},
		{"GET", "/apis/apps/v1/deployments", "", "list", true},
		{"GET", "/api/v1/namespaces/demo/pods/web-1/exec", "", "", false},
		{"GET", "/api/v1/namespaces/demo/services/web:80/proxy/admin", "", "", false},
		{"POST", "/api/v1/namespaces/demo/pods", "", "", false},
		{"PUT", "/api/v1/namespaces/demo/pods/web-1", "", "", false},
		{"PATCH", "/apis/apps/v1/namespaces/demo/deployments/web", "", "", false},
		{"DELETE", "/api/v1/namespaces/demo/pods/web-1", "", "", false},
		{"POST", "/api/v1/namespaces/demo/pods/web-1/portforward", "", "", false},
	} {
		verb, ok := g.Classify(tc.method, tc.path, tc.watch)
		if ok != tc.ok || verb != tc.verb {
			t.Errorf("%s %s watch=%s: got %q %v, want %q %v", tc.method, tc.path, tc.watch, verb, ok, tc.verb, tc.ok)
		}
	}
	g.AllowPortForward = true
	if v, ok := g.Classify("POST", "/api/v1/namespaces/demo/pods/web-1/portforward", ""); !ok || v != "portforward" {
		t.Fatal("port-forward should pass when diagnostics are allowed")
	}
	if _, ok := g.Classify("DELETE", "/api/v1/namespaces/demo/pods/web-1", ""); ok {
		t.Fatal("port-forward permission must not open writes")
	}
}

// The guard refuses before the request leaves the process.
func TestReadOnlyGuardTransport(t *testing.T) {
	hits := 0
	g := &ReadOnlyGuard{}
	cfg := &rest.Config{Host: "http://127.0.0.1:1"}
	g.Wrap(cfg)
	rt := cfg.WrapTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
	}))
	req, _ := http.NewRequest("DELETE", "http://x/api/v1/namespaces/demo/pods/web-1", nil)
	if _, err := rt.RoundTrip(req); err == nil || hits != 0 {
		t.Fatalf("delete passed the guard: %v, hits %d", err, hits)
	}
	req, _ = http.NewRequest("GET", "http://x/api/v1/namespaces/demo/pods?watch=true", nil)
	if _, err := rt.RoundTrip(req); err != nil || hits != 1 {
		t.Fatalf("watch refused: %v", err)
	}
	c, refused := g.Counts()
	if refused != 1 || c["watch"] != 1 {
		t.Fatalf("counts %v refused %d", c, refused)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
