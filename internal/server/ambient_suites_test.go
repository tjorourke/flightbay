package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/flows"
	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/reach"
	"github.com/tjorourke/flightbay/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	denyRef  = "c1/AuthorizationPolicy/bookinfo/deny-red"
	routeRef = "c1/HTTPRoute/bookinfo/split"
)

func addObjs(ws *store.Workspace, group, resource, kind string, namespaced bool, objs ...map[string]any) {
	ws.AddObjects("c1", bundle.APIResource{Group: group, Version: "v1", Resource: resource, Kind: kind, Namespaced: namespaced}, objs, "fx-"+resource)
}

func k8sObj(kind, ns, name string, spec map[string]any, labels map[string]any) map[string]any {
	meta := map[string]any{"name": name, "uid": "uid-" + name}
	if ns != "" {
		meta["namespace"] = ns
	}
	if labels != nil {
		meta["labels"] = labels
	}
	o := map[string]any{"apiVersion": "v1", "kind": kind, "metadata": meta}
	if spec != nil {
		o["spec"] = spec
	}
	return o
}

func suiteWorkspace(t *testing.T) *store.Workspace {
	t.Helper()
	return suiteWorkspaceNS(t, map[string]any{"istio.io/dataplane-mode": "ambient"})
}

// suiteWorkspaceNS is suiteWorkspace with the bookinfo namespace's labels
// given, e.g. to bind it to a waypoint.
func suiteWorkspaceNS(t *testing.T, nsLabels map[string]any) *store.Workspace {
	t.Helper()
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	ws.SetHeader(&bundle.Header{Contexts: []string{"c1"}})
	addObjs(ws, "", "namespaces", "Namespace", false, k8sObj("Namespace", "", "bookinfo", nil, nsLabels))
	svc := func(name string, sel map[string]any) map[string]any {
		return k8sObj("Service", "bookinfo", name, map[string]any{"selector": sel, "ports": []any{map[string]any{"port": int64(9080), "targetPort": int64(9080), "protocol": "TCP"}}}, nil)
	}
	addObjs(ws, "", "services", "Service", true, svc("reviews", map[string]any{"app": "reviews"}), svc("reviews-v1", map[string]any{"app": "reviews", "version": "v1"}), svc("reviews-v2", map[string]any{"app": "reviews", "version": "v2"}))
	pod := func(name, sa, image string, labels map[string]any) map[string]any {
		p := k8sObj("Pod", "bookinfo", name, map[string]any{"serviceAccountName": sa, "containers": []any{map[string]any{"name": "main", "image": image}}}, labels)
		p["status"] = map[string]any{"phase": "Running", "podIP": "10.0.0.7"}
		return p
	}
	addObjs(ws, "", "pods", "Pod", true,
		pod("client-1", "bookinfo-client", "curlimages/curl:8.12.1", map[string]any{"app": "client"}),
		pod("reviews-v1-abc", "bookinfo-reviews", "reviews:1", map[string]any{"app": "reviews", "version": "v1"}))
	addObjs(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true,
		k8sObj("AuthorizationPolicy", "bookinfo", "deny-red", map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "reviews"}},
			"rules": []any{map[string]any{"when": []any{map[string]any{"key": "request.headers[color]", "values": []any{"red"}}}}}}, nil),
		k8sObj("AuthorizationPolicy", "bookinfo", "reviews-methods", map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "reviews"}},
			"rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET", "HEAD"}}}}}}}, nil))
	addObjs(ws, "security.istio.io", "peerauthentications", "PeerAuthentication", true)
	addObjs(ws, "gateway.networking.k8s.io", "httproutes", "HTTPRoute", true,
		k8sObj("HTTPRoute", "bookinfo", "split", map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "reviews", "port": int64(9080)}},
			"rules": []any{map[string]any{"matches": []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}}},
				"backendRefs": []any{map[string]any{"name": "reviews-v1", "port": int64(9080), "weight": int64(80)}, map[string]any{"name": "reviews-v2", "port": int64(9080), "weight": int64(20)}}}}}, nil))
	return ws
}

func suiteServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{WS: suiteWorkspace(t), Token: tok, WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	return s, srv
}

func suiteCall(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", srv.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func caseIDs(m map[string]any) []string {
	var out []string
	for _, c := range m["cases"].([]any) {
		out = append(out, c.(map[string]any)["id"].(string))
	}
	return out
}

func TestSuitesGetGenerated(t *testing.T) {
	_, srv := suiteServer(t)
	code, out := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if out["saved"] != false {
		t.Errorf("a generated suite is saved:false: %v", out["saved"])
	}
	want := []string{"r1-match", "r1-value-color", "r1-absent-color", "r1-case-color", "x-bookinfo-reviews-methods", "bypass"}
	if got := caseIDs(out); !reflect.DeepEqual(got, want) {
		t.Errorf("ids %v, want %v", got, want)
	}
	ex := out["explain"].(map[string]any)
	if ex["inWords"] != "Deny any request to reviews when header color equals red." {
		t.Errorf("inWords %v", ex["inWords"])
	}
	others := ex["others"].([]any)
	if len(others) != 1 || others[0].(map[string]any)["name"] != "reviews-methods" || others[0].(map[string]any)["summary"] != "GET, HEAD only" || others[0].(map[string]any)["action"] != "ALLOW" {
		t.Errorf("others %v", others)
	}
	if out["source"].(map[string]any)["kind"] != "workload" || out["source"].(map[string]any)["ref"].(map[string]any)["name"] != "client-1" {
		t.Errorf("source %v", out["source"])
	}
	if out["notes"] == nil {
		t.Error("notes must be a list")
	}
	// Route.
	code, out = suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+routeRef, nil)
	if code != 200 || len(out["checks"].([]any)) < 3 {
		t.Fatalf("%d %v", code, out)
	}
	if got := caseIDs(out); !reflect.DeepEqual(got, []string{"r1-m1", "r1-m1-sub", "r1-weights", "not-route"}) {
		t.Errorf("route ids %v", got)
	}
	rules := out["explain"].(map[string]any)["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["backends"].([]any)[0].(map[string]any)["percent"] != float64(80) {
		t.Errorf("rules %v", rules)
	}
}

func TestSuitesGetErrors(t *testing.T) {
	_, srv := suiteServer(t)
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/ambient/suites", 400},
		{"/api/ambient/suites?ref=nonsense", 400},
		{"/api/ambient/suites?ref=c1/Service/bookinfo/reviews", 400},
		{"/api/ambient/suites?ref=c1/AuthorizationPolicy/bookinfo/missing", 404},
	} {
		if code, out := suiteCall(t, srv, "GET", tc.path, nil); code != tc.code || out["error"] == nil {
			t.Errorf("%s: %d %v", tc.path, code, out)
		}
	}
	// Unauthenticated.
	resp, err := http.Get(srv.URL + "/api/ambient/suites?ref=" + denyRef)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("unauthenticated %d", resp.StatusCode)
	}
	if code, _ := suiteCall(t, srv, "DELETE", "/api/ambient/suites?ref="+denyRef, nil); code != 405 {
		t.Errorf("DELETE %d", code)
	}
}

func TestSuitesSaveRoundTrip(t *testing.T) {
	s, srv := suiteServer(t)
	_, gen := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil)
	// A person adds a case and drops the bypass.
	cases := gen["cases"].([]any)
	var keep []any
	for _, c := range cases {
		if c.(map[string]any)["id"] != "bypass" {
			keep = append(keep, c)
		}
	}
	keep = append(keep, map[string]any{"id": "mine", "label": "mine", "generated": false, "why": "I said so",
		"request": map[string]any{"method": "delete", "path": "/reviews/9", "port": 9080, "direct": false}, "expect": map[string]any{"status": 200}})
	gen["cases"] = keep
	// Results sent by a client are ignored.
	gen["lastRun"] = map[string]any{"summary": map[string]any{"passed": 99}}
	code, saved := suiteCall(t, srv, "POST", "/api/ambient/suites", gen)
	if code != 200 || saved["saved"] != true {
		t.Fatalf("%d %v", code, saved)
	}
	if saved["lastRun"] != nil {
		t.Errorf("client-supplied results must be dropped: %v", saved["lastRun"])
	}
	p, _ := ambient.SuitePath(s.AmbientStateDir, mustRefT(t, denyRef))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("not at the documented path: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(p), "/ambient-suites/c1/security.istio.io/AuthorizationPolicy/bookinfo/deny-red.json") {
		t.Errorf("path %s", p)
	}
	code, again := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil)
	if code != 200 || again["saved"] != true {
		t.Fatalf("%d %v", code, again)
	}
	ids := caseIDs(again)
	if ids[len(ids)-1] != "mine" || len(ids) != len(keep) {
		t.Errorf("saved cases must stand: %v", ids)
	}
	if m := again["cases"].([]any)[len(ids)-1].(map[string]any); m["request"].(map[string]any)["method"] != "DELETE" || m["expect"].(map[string]any)["text"] != "200" {
		t.Errorf("normalised %v", m)
	}
	// Regenerate keeps the person's case, restores the bypass, and is not saved.
	code, regen := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef+"&regenerate=1", nil)
	if code != 200 || regen["regenerated"] != true || regen["saved"] != false {
		t.Fatalf("%d %v", code, regen)
	}
	rids := caseIDs(regen)
	if rids[len(rids)-1] != "mine" || !contains(rids, "bypass") {
		t.Errorf("regenerated %v", rids)
	}
	if _, again := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil); len(caseIDs(again)) != len(keep) {
		t.Error("regenerate must not save")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func mustRefT(t *testing.T, s string) ambient.Ref {
	t.Helper()
	r, err := ambient.ParseSuiteRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSuitesSaveValidation(t *testing.T) {
	_, srv := suiteServer(t)
	_, gen := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil)
	mut := func(f func(m map[string]any)) map[string]any {
		b, _ := json.Marshal(gen)
		var cp map[string]any
		_ = json.Unmarshal(b, &cp)
		f(cp)
		return cp
	}
	first := func(m map[string]any) map[string]any { return m["cases"].([]any)[0].(map[string]any) }
	for name, body := range map[string]map[string]any{
		"unsupported kind": mut(func(m map[string]any) { m["target"].(map[string]any)["kind"] = "Service" }),
		"no id":            mut(func(m map[string]any) { first(m)["id"] = "" }),
		"bad method":       mut(func(m map[string]any) { first(m)["request"].(map[string]any)["method"] = "BREW" }),
		"absolute path":    mut(func(m map[string]any) { first(m)["request"].(map[string]any)["path"] = "http://x/" }),
		"no expectation":   mut(func(m map[string]any) { first(m)["expect"] = map[string]any{} }),
		"bad source":       mut(func(m map[string]any) { m["source"].(map[string]any)["kind"] = "laptop" }),
	} {
		if code, out := suiteCall(t, srv, "POST", "/api/ambient/suites", body); code != 400 || out["error"] == nil {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// Cross-origin writes are refused.
	b, _ := json.Marshal(gen)
	req, _ := http.NewRequest("POST", srv.URL+"/api/ambient/suites", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-origin %d", resp.StatusCode)
	}
	if code, _ := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil); code != 200 {
		t.Error("nothing was saved by the failures")
	}
}

func TestSuitesService(t *testing.T) {
	_, srv := suiteServer(t)
	code, out := suiteCall(t, srv, "GET", "/api/ambient/suites/service?cluster=c1&namespace=bookinfo&name=reviews", nil)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	suites := out["suites"].([]any)
	if len(suites) != 3 {
		t.Fatalf("want deny-red, reviews-methods and the route: %v", suites)
	}
	if out["neverRun"] != float64(3) || out["cases"].(float64) < 10 {
		t.Errorf("%v", out)
	}
	for _, s := range suites {
		m := s.(map[string]any)
		if m["saved"] != false || m["lastRun"] != nil {
			t.Errorf("%v", m)
		}
	}
	// Cluster defaults when the capture has one.
	if code, _ := suiteCall(t, srv, "GET", "/api/ambient/suites/service?namespace=bookinfo&name=reviews", nil); code != 200 {
		t.Errorf("cluster default %d", code)
	}
	for _, p := range []string{"?cluster=c1&namespace=bookinfo", "?cluster=c1&name=reviews"} {
		if code, _ := suiteCall(t, srv, "GET", "/api/ambient/suites/service"+p, nil); code != 400 {
			t.Errorf("%s %d", p, code)
		}
	}
	if code, _ := suiteCall(t, srv, "GET", "/api/ambient/suites/service?cluster=c1&namespace=bookinfo&name=ghost", nil); code != 404 {
		t.Errorf("ghost %d", code)
	}
}

func TestSuitesPreview(t *testing.T) {
	_, srv := suiteServer(t)
	plan := map[string]any{
		"request": map[string]any{"cluster": "c1", "namespace": "bookinfo", "service": "reviews"},
		"objects": []any{
			map[string]any{"apiVersion": "security.istio.io/v1", "kind": "AuthorizationPolicy", "metadata": map[string]any{"name": "block-delete"},
				"spec": map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"kind": "Service", "name": "reviews"}},
					"rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"DELETE"}}}}}}}},
			map[string]any{"kind": "ConfigMap", "metadata": map[string]any{"name": "ignored"}},
		},
		"yaml": "ignored", "notes": []any{}, "evidence": []any{},
	}
	code, out := suiteCall(t, srv, "POST", "/api/ambient/suites/preview", plan)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	suites := out["suites"].([]any)
	if len(suites) != 1 {
		t.Fatalf("%v", suites)
	}
	cases := out["cases"].([]any)
	first := cases[0].(map[string]any)
	if first["object"] != "AuthorizationPolicy bookinfo/block-delete" || first["id"] != "r1-match" || first["expect"].(map[string]any)["status"] != float64(403) || first["request"].(map[string]any)["method"] != "DELETE" {
		t.Errorf("%v", first)
	}
	// The drafted policy is tested against the captured policies on the Service:
	// DELETE already breaks the ALLOW, so that is not repeated, but the captured
	// DENY on a header gets an interaction case.
	found := false
	for _, c := range cases {
		found = found || c.(map[string]any)["id"] == "x-bookinfo-deny-red"
	}
	if !found {
		t.Errorf("want an interaction case with deny-red: %v", caseIDsFlat(cases))
	}
	// Nothing was written.
	if code, _ := suiteCall(t, srv, "GET", "/api/ambient/suites?ref=c1/AuthorizationPolicy/bookinfo/block-delete", nil); code != 404 {
		t.Errorf("preview must not save: %d", code)
	}
	// A route draft, with the namespace on top level instead.
	route := map[string]any{"cluster": "c1", "namespace": "bookinfo", "service": "reviews", "objects": []any{
		map[string]any{"kind": "HTTPRoute", "metadata": map[string]any{"name": "new-split"}, "spec": map[string]any{
			"parentRefs": []any{map[string]any{"kind": "Service", "name": "reviews", "port": 9080}},
			"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": "reviews-v1", "port": 9080, "weight": 50}, map[string]any{"name": "reviews-v2", "port": 9080, "weight": 50}}}}}}}}
	code, out = suiteCall(t, srv, "POST", "/api/ambient/suites/preview", route)
	if code != 200 || len(out["suites"].([]any)) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	for _, c := range out["cases"].([]any) {
		m := c.(map[string]any)
		if m["id"] == "r1-weights" && (m["expect"].(map[string]any)["weights"].(map[string]any)["reviews-v1"] != float64(50)) {
			t.Errorf("%v", m)
		}
	}
	// Unknown Service; an object that cannot be tested becomes a note.
	if code, _ := suiteCall(t, srv, "POST", "/api/ambient/suites/preview", map[string]any{"cluster": "c1", "namespace": "bookinfo", "service": "ghost", "objects": []any{}}); code != 400 {
		t.Errorf("ghost %d", code)
	}
	code, out = suiteCall(t, srv, "POST", "/api/ambient/suites/preview", map[string]any{"cluster": "c1", "namespace": "bookinfo", "objects": []any{
		map[string]any{"kind": "AuthorizationPolicy", "metadata": map[string]any{"name": "lost"}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "nothing"}}}}}})
	if code != 200 || len(out["suites"].([]any)) != 0 || len(out["notes"].([]any)) != 1 || !strings.Contains(out["notes"].([]any)[0].(string), "Tests not generated for AuthorizationPolicy lost") {
		t.Errorf("%d %v", code, out)
	}
	if code, _ := suiteCall(t, srv, "GET", "/api/ambient/suites/preview", nil); code != 405 {
		t.Errorf("GET %d", code)
	}
}

func caseIDsFlat(cases []any) []string {
	var out []string
	for _, c := range cases {
		out = append(out, c.(map[string]any)["id"].(string))
	}
	return out
}

// ---- run -------------------------------------------------------------------

type fakeRunner struct {
	mu     sync.Mutex
	sent   []suiteSendRequest
	script func(req suiteSendRequest) (ambient.Observation, error)
	closed bool
}

func (f *fakeRunner) Send(ctx context.Context, req suiteSendRequest) (ambient.Observation, error) {
	f.mu.Lock()
	f.sent = append(f.sent, req)
	f.mu.Unlock()
	return f.script(req)
}
func (f *fakeRunner) Close() { f.closed = true }

type fakeDecider struct {
	calls []string
	panic bool
}

func (f *fakeDecider) Decide(ctx context.Context, suite *ambient.Suite, c ambient.Case, obs *ambient.Observation, sentAt time.Time) {
	f.calls = append(f.calls, c.ID)
	if f.panic {
		panic("decider blew up")
	}
	if obs.Status == 403 {
		obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "deny-red", "policy", "waypoint-log"
	}
}

func withFakeRunner(t *testing.T, r *fakeRunner, d suiteDecider) *[]suiteRunSpec {
	t.Helper()
	var specs []suiteRunSpec
	old := newSuiteRunner
	newSuiteRunner = func(s *Server, ctx context.Context, spec suiteRunSpec) (suiteRunner, suiteDecider, error) {
		specs = append(specs, spec)
		return r, d, nil
	}
	t.Cleanup(func() { newSuiteRunner = old })
	return &specs
}

// echo answers the way the cluster would if everything behaved as expected,
// except the cases named in override.
func echo(override map[string]ambient.Observation) func(req suiteSendRequest) (ambient.Observation, error) {
	return func(req suiteSendRequest) (ambient.Observation, error) {
		if o, ok := override[req.Case.ID]; ok {
			return o, nil
		}
		o := ambient.Observation{Status: req.Case.Expect.Status, Connection: "fake"}
		if req.Case.Expect.Rule > 0 && len(req.Case.Expect.Backends) > 0 {
			o.Status, o.Backend = 200, req.Case.Expect.Backends[0]
		}
		if req.Case.Expect.NotRoute {
			o.Status, o.Backend = 200, req.Case.Expect.Backend
		}
		for k, v := range req.Case.Expect.Weights {
			for i := 0; i < v*req.Samples/100; i++ {
				o.Samples = append(o.Samples, ambient.Sample{Status: 200, Backend: k, DurationMs: 5})
			}
		}
		return o, nil
	}
}

func TestSuitesRunPersistsAndMerges(t *testing.T) {
	_, srv := suiteServer(t)
	runner := &fakeRunner{script: echo(map[string]ambient.Observation{
		"r1-case-color":   {Status: 403, Connection: "fake"}, // expected 200: a failure
		"r1-absent-color": {Error: "connection refused"},
	})}
	dec := &fakeDecider{}
	specs := withFakeRunner(t, runner, dec)

	code, out := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": denyRef, "all": true, "context": "kind-c1"})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if !runner.closed {
		t.Error("runner must be closed")
	}
	if len(*specs) != 1 || (*specs)[0].Context != "kind-c1" || (*specs)[0].Service.Name != "reviews" || (*specs)[0].Authz == nil {
		t.Errorf("spec %+v", *specs)
	}
	byID := map[string]map[string]any{}
	for _, r := range out["results"].([]any) {
		m := r.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if byID["r1-match"]["status"] != "pass" || byID["r1-match"]["decidedBy"] != "deny-red" || byID["r1-match"]["decidedByHow"] != "waypoint-log" {
		t.Errorf("match %v", byID["r1-match"])
	}
	if byID["r1-case-color"]["status"] != "fail" || byID["r1-case-color"]["message"] != "Expected 200, got 403." {
		t.Errorf("case %v", byID["r1-case-color"])
	}
	if byID["r1-absent-color"]["status"] != "error" || byID["r1-absent-color"]["message"] != "connection refused" {
		t.Errorf("absent %v", byID["r1-absent-color"])
	}
	if byID["r1-value-color"]["decidedBy"] != "" {
		t.Errorf("a case nothing could attribute has no decidedBy, never \"unknown\": %v", byID["r1-value-color"])
	}
	sum := out["summary"].(map[string]any)
	if sum["failed"] != float64(1) || sum["errored"] != float64(1) || sum["passed"] != float64(4) || sum["total"] != float64(6) {
		t.Errorf("summary %v", sum)
	}
	if out["demo"] != nil {
		t.Error("a real run is not demo")
	}
	// Persisted: a later GET carries the run, and the suite is now saved.
	_, got := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil)
	if got["saved"] != true || got["lastRun"] == nil || len(got["lastRun"].(map[string]any)["results"].([]any)) != 6 {
		t.Fatalf("%v", got["lastRun"])
	}
	// The sender is the generated default source.
	if runner.sent[0].Source.Ref.Name != "client-1" {
		t.Errorf("source %+v", runner.sent[0].Source)
	}

	// Running one case again replaces just its result.
	runner2 := &fakeRunner{script: echo(nil)}
	withFakeRunner(t, runner2, &fakeDecider{})
	code, out = suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": denyRef, "cases": []string{"r1-case-color"}, "context": "kind-c1"})
	if code != 200 || len(out["results"].([]any)) != 1 || len(runner2.sent) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	last := out["lastRun"].(map[string]any)
	if len(last["results"].([]any)) != 6 || last["summary"].(map[string]any)["failed"] != float64(0) || last["summary"].(map[string]any)["errored"] != float64(1) {
		t.Errorf("merged lastRun %v", last["summary"])
	}
	_, got = suiteCall(t, srv, "GET", "/api/ambient/suites/service?cluster=c1&namespace=bookinfo&name=reviews", nil)
	rows := got["suites"].([]any)
	var row map[string]any
	for _, r := range rows {
		if r.(map[string]any)["name"] == "deny-red" {
			row = r.(map[string]any)
		}
	}
	if row["saved"] != true || row["status"] != "fail" || row["lastRun"].(map[string]any)["passed"] != float64(5) {
		t.Errorf("service row %v", row)
	}
	if got["passed"] != float64(5) || got["failed"] != float64(1) || got["neverRun"] != float64(2) {
		t.Errorf("service totals %v", got)
	}
}

func TestSuitesRunRoute(t *testing.T) {
	_, srv := suiteServer(t)
	runner := &fakeRunner{script: echo(map[string]ambient.Observation{"not-route": {Status: 200, Backend: "reviews-v2"}})}
	withFakeRunner(t, runner, nil)
	code, out := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": routeRef, "all": true, "context": "kind-c1",
		"source": map[string]any{"kind": "ephemeral", "ref": map[string]any{"namespace": "bookinfo"}}})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	res := map[string]map[string]any{}
	for _, r := range out["results"].([]any) {
		res[r.(map[string]any)["id"].(string)] = r.(map[string]any)
	}
	w := res["r1-weights"]
	if w["status"] != "pass" || len(w["weights"].([]any)) != 2 || w["got"].(map[string]any)["samples"] != float64(200) {
		t.Errorf("weights %v", w)
	}
	if runner.sent[0].Source.Kind != "ephemeral" {
		t.Errorf("source override %+v", runner.sent[0].Source)
	}
	var sampleReq *suiteSendRequest
	for i := range runner.sent {
		if runner.sent[i].Case.ID == "r1-weights" {
			sampleReq = &runner.sent[i]
		}
	}
	if sampleReq == nil || sampleReq.Samples != 200 {
		t.Errorf("samples %+v", sampleReq)
	}
	if n := res["not-route"]; n["status"] != "fail" || !strings.Contains(n["message"].(string), "Expected reviews-v1 to answer directly") {
		t.Errorf("not-route %v", n)
	}
	if res["r1-m1"]["status"] != "pass" || res["r1-m1"]["got"].(map[string]any)["text"] != "rule 1 → reviews-v1" {
		t.Errorf("rule case %v", res["r1-m1"])
	}
	// The source override was saved with the suite.
	_, got := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+routeRef, nil)
	if got["source"].(map[string]any)["kind"] != "ephemeral" {
		t.Errorf("source not saved: %v", got["source"])
	}
}

func TestSuitesRunRequests(t *testing.T) {
	_, srv := suiteServer(t)
	runner := &fakeRunner{script: echo(nil)}
	withFakeRunner(t, runner, nil)
	post := func(body map[string]any) (int, map[string]any) {
		return suiteCall(t, srv, "POST", "/api/ambient/suites/run", body)
	}
	for name, tc := range map[string]struct {
		body map[string]any
		code int
		msg  string
	}{
		"no ref":       {map[string]any{"context": "x"}, 400, "ref is required"},
		"bad ref":      {map[string]any{"ref": "x", "context": "x"}, 400, "not cluster/Kind"},
		"no context":   {map[string]any{"ref": denyRef, "all": true}, 400, "choose a cluster connection"},
		"external":     {map[string]any{"ref": denyRef, "context": "x", "source": map[string]any{"kind": "external"}}, 400, "choose one of those"},
		"unknown case": {map[string]any{"ref": denyRef, "context": "x", "cases": []string{"nope"}}, 400, "no such case: nope"},
		"unknown ref":  {map[string]any{"ref": "c1/AuthorizationPolicy/bookinfo/ghost", "context": "x"}, 404, ""},
		"no source":    {map[string]any{"ref": denyRef, "context": "x", "source": map[string]any{"kind": ""}}, 400, "choose a source"},
	} {
		code, out := post(tc.body)
		if code != tc.code || (tc.msg != "" && !strings.Contains(out["error"].(string), tc.msg)) {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if len(runner.sent) != 0 {
		t.Errorf("a rejected request must send nothing: %d", len(runner.sent))
	}
	// A run can carry its own suite (a generated one from the drawer) and
	// save:false leaves nothing behind.
	_, gen := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil)
	code, out := post(map[string]any{"suite": gen, "cases": []string{"r1-match"}, "context": "x", "save": false})
	if code != 200 || len(out["results"].([]any)) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	if code, got := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil); code != 200 || got["saved"] != false {
		t.Errorf("save:false saved anyway: %v", got["saved"])
	}
	// A runner that cannot be built reports why.
	old := newSuiteRunner
	newSuiteRunner = func(*Server, context.Context, suiteRunSpec) (suiteRunner, suiteDecider, error) {
		return nil, nil, suiteHTTPError{409, "Service identity differs"}
	}
	t.Cleanup(func() { newSuiteRunner = old })
	if code, out := post(map[string]any{"ref": denyRef, "context": "x"}); code != 409 || out["error"] != "Service identity differs" {
		t.Errorf("%d %v", code, out)
	}
	newSuiteRunner = func(*Server, context.Context, suiteRunSpec) (suiteRunner, suiteDecider, error) {
		return nil, nil, errors.New("dial tcp: refused")
	}
	if code, _ := post(map[string]any{"ref": denyRef, "context": "x"}); code != 502 {
		t.Errorf("generic runner failure %d", code)
	}
}

// ---- runSuite --------------------------------------------------------------

func miniSuite() *ambient.Suite {
	s := &ambient.Suite{Target: ambient.Ref{Kind: ambient.KindAuthorizationPolicy}, Cases: []ambient.Case{
		{ID: "a", Expect: ambient.CaseExpect{Status: 403}},
		{ID: "b", Expect: ambient.CaseExpect{Status: 200}},
		{ID: "w", Expect: ambient.CaseExpect{Weights: map[string]int{"x": 100}, Samples: 10}},
		{ID: "c", Expect: ambient.CaseExpect{Status: 200}},
	}}
	s.Normalize()
	return s
}

func TestRunSuiteLoop(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(time.Second); return clock }

	t.Run("outcomes, skipped, errors and decider rules", func(t *testing.T) {
		runner := &fakeRunner{script: func(req suiteSendRequest) (ambient.Observation, error) {
			switch req.Case.ID {
			case "a":
				return ambient.Observation{Status: 403}, nil
			case "b":
				return ambient.Observation{}, skipCase("no client with that identity")
			case "w":
				return ambient.Observation{Samples: []ambient.Sample{{Status: 200, Backend: "x", DurationMs: 3}}}, nil
			}
			return ambient.Observation{}, errors.New("exec failed")
		}}
		dec := &fakeDecider{}
		run := runSuite(context.Background(), miniSuite(), []string{"a", "b", "w", "c"}, runner, dec, now)
		var st []string
		for _, r := range run.Results {
			st = append(st, r.ID+":"+r.Status)
		}
		if !reflect.DeepEqual(st, []string{"a:pass", "b:skipped", "w:pass", "c:error"}) {
			t.Errorf("%v", st)
		}
		if !reflect.DeepEqual(dec.calls, []string{"a"}) {
			t.Errorf("decider runs for answered, non-weights cases only: %v", dec.calls)
		}
		if run.Summary.Passed != 2 || run.Summary.Skipped != 1 || run.Summary.Errored != 1 || run.Summary.OK() {
			t.Errorf("%+v", run.Summary)
		}
		if run.Results[1].Message != "no client with that identity" {
			t.Errorf("%+v", run.Results[1])
		}
		if runner.sent[2].Samples != 10 || runner.sent[0].Samples != 0 {
			t.Errorf("samples %d %d", runner.sent[2].Samples, runner.sent[0].Samples)
		}
	})
	t.Run("a decider that panics does not lose the result", func(t *testing.T) {
		runner := &fakeRunner{script: echo(nil)}
		run := runSuite(context.Background(), miniSuite(), []string{"a"}, runner, &fakeDecider{panic: true}, now)
		if len(run.Results) != 1 || run.Results[0].Status != ambient.StatusPass || run.Results[0].DecidedBy != "" {
			t.Errorf("%+v", run.Results)
		}
	})
	t.Run("ids keep the order given and unknown ids are dropped", func(t *testing.T) {
		runner := &fakeRunner{script: echo(nil)}
		run := runSuite(context.Background(), miniSuite(), []string{"c", "zzz", "a"}, runner, nil, now)
		if len(run.Results) != 2 || run.Results[0].ID != "c" || run.Results[1].ID != "a" {
			t.Errorf("%+v", run.Results)
		}
	})
	t.Run("a stopped context skips the rest", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		runner := &fakeRunner{script: func(req suiteSendRequest) (ambient.Observation, error) {
			cancel()
			return ambient.Observation{Status: req.Case.Expect.Status}, nil
		}}
		run := runSuite(ctx, miniSuite(), []string{"a", "b", "c"}, runner, nil, now)
		if run.Results[0].Status != "pass" || run.Results[1].Status != "skipped" || run.Results[2].Status != "skipped" || len(runner.sent) != 1 {
			t.Errorf("%+v", run.Results)
		}
		if !strings.Contains(run.Results[1].Message, "stopped") {
			t.Errorf("%q", run.Results[1].Message)
		}
	})
}

func TestSelectCasesMergeAndKeep(t *testing.T) {
	s := miniSuite()
	if ids, err := selectCases(s, nil, false); err != nil || !reflect.DeepEqual(ids, []string{"a", "b", "w", "c"}) {
		t.Errorf("%v %v", ids, err)
	}
	if ids, err := selectCases(s, []string{"c", "a"}, false); err != nil || !reflect.DeepEqual(ids, []string{"a", "c"}) {
		t.Errorf("suite order: %v %v", ids, err)
	}
	if ids, err := selectCases(s, []string{"c"}, true); err != nil || len(ids) != 4 {
		t.Errorf("all wins: %v %v", ids, err)
	}
	if _, err := selectCases(&ambient.Suite{}, nil, true); err == nil {
		t.Error("an empty suite cannot run")
	}
	at := time.Now()
	prev := &ambient.SuiteRun{Results: []ambient.CaseResult{{ID: "a", Status: "pass"}, {ID: "gone", Status: "pass"}, {ID: "b", Status: "fail"}}}
	run := &ambient.SuiteRun{At: at, Results: []ambient.CaseResult{{ID: "b", Status: "pass"}}}
	merged := mergeRun(prev, run, s.Cases)
	if len(merged.Results) != 2 || merged.Results[1].Status != "pass" || merged.Summary.Passed != 2 || merged.Summary.Failed != 0 {
		t.Errorf("%+v", merged)
	}
	kept := keepResultsFor(prev, s.Cases[:1])
	if len(kept.Results) != 1 || kept.Results[0].ID != "a" || kept.Summary.Total != 1 {
		t.Errorf("%+v", kept)
	}
	if keepResultsFor(prev, nil) != nil || keepResultsFor(nil, s.Cases) != nil {
		t.Error("nothing to keep")
	}
}

// ---- real runner pieces ----------------------------------------------------

func TestDirectTarget(t *testing.T) {
	pod := func(name, ip string, ready bool, labels map[string]string) corev1.Pod {
		p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Status: corev1.PodStatus{PodIP: ip}}
		if ready {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		return p
	}
	svc := func(sel map[string]string, target intstr.IntOrString) *corev1.Service {
		return &corev1.Service{Spec: corev1.ServiceSpec{Selector: sel, Ports: []corev1.ServicePort{{Port: 9080, TargetPort: target}}}}
	}
	app := map[string]string{"app": "reviews"}
	tests := []struct {
		name   string
		svc    *corev1.Service
		pods   []corev1.Pod
		ip     string
		tp     int
		pod    string
		errStr string
	}{
		{"first ready pod by name", svc(app, intstr.FromInt(9080)), []corev1.Pod{pod("b", "10.0.0.2", true, app), pod("a", "10.0.0.1", true, app)}, "10.0.0.1", 9080, "a", ""},
		{"skips unready, no IP and other labels", svc(app, intstr.FromInt(9080)), []corev1.Pod{pod("a", "10.0.0.1", false, app), pod("b", "", true, app), pod("c", "10.0.0.3", true, map[string]string{"app": "other"}), pod("d", "10.0.0.4", true, app)}, "10.0.0.4", 9080, "d", ""},
		{"target port differs", svc(app, intstr.FromInt(8080)), []corev1.Pod{pod("a", "10.0.0.1", true, app)}, "10.0.0.1", 8080, "a", ""},
		{"no ready pod", svc(app, intstr.FromInt(9080)), []corev1.Pod{pod("a", "10.0.0.1", false, app)}, "", 0, "", "no ready Pod"},
		{"no selector", svc(nil, intstr.FromInt(9080)), nil, "", 0, "", "no selector"},
	}
	for _, tc := range tests {
		ip, tp, p, err := directTarget(tc.svc, tc.pods, 9080)
		if tc.errStr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errStr) {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err != nil || ip != tc.ip || tp != tc.tp || p != tc.pod {
			t.Errorf("%s: %s %d %s %v", tc.name, ip, tp, p, err)
		}
	}
	// A named target port is read off the Pod's container.
	named := pod("a", "10.0.0.1", true, app)
	named.Spec.Containers = []corev1.Container{{Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8888}}}}
	if _, tp, _, err := directTarget(svc(app, intstr.FromString("http")), []corev1.Pod{named}, 9080); err != nil || tp != 8888 {
		t.Errorf("named port %d %v", tp, err)
	}
}

func TestSuiteBackends(t *testing.T) {
	s := &ambient.Suite{Cases: []ambient.Case{
		{Expect: ambient.CaseExpect{Rule: 1, Backends: []string{"v1", "v2"}}},
		{Expect: ambient.CaseExpect{NotRoute: true, Backend: "v1"}},
		{Expect: ambient.CaseExpect{Weights: map[string]int{"v3": 100}}},
		{Expect: ambient.CaseExpect{Status: 403}},
	}}
	want := map[string]bool{"ns/v1": true, "ns/v2": true, "ns/v3": true}
	if got := suiteBackends(s, "ns"); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if len(suiteBackends(nil, "ns")) != 0 {
		t.Error("nil suite")
	}
}

func TestMatchWaypointLine(t *testing.T) {
	sent := time.Date(2026, 10, 20, 11, 52, 9, 0, time.UTC)
	text := func(method, path string, code int, details, cluster string) string {
		return `[2026-10-20T11:52:09.1Z] "` + method + ` ` + path + ` HTTP/1.1" ` + itoa(code) + ` - ` + details + ` - "-" 0 19 1 - "-" "curl/8.12.1" "req-1" "reviews.bookinfo.svc.cluster.local:9080" "10.0.0.7:9080" ` + cluster + ` 10.0.0.9:15008 10.0.0.9:15008 10.0.0.8:41000 - -`
	}
	deny := text("GET", "/reviews/0?x=1", 403, "rbac_access_denied_matched_policy[ns[bookinfo]-policy[deny-red]-rule[0]]", "inbound-vip|9080|http|reviews.bookinfo.svc.cluster.local")
	ok := text("GET", "/reviews/0", 200, "via_upstream", "outbound|9080||reviews-v2.bookinfo.svc.cluster.local")
	lines := []suiteLogLine{
		{ID: 1, At: sent.Add(-30 * time.Second), Pod: "bookinfo-waypoint-abc", Raw: deny},               // too early
		{ID: 2, At: sent.Add(2 * time.Second), Pod: "other-pod-abc", Raw: deny},                         // not the waypoint
		{ID: 3, At: sent.Add(3 * time.Second), Pod: "bookinfo-waypoint-abc", Raw: deny},                 // the match
		{ID: 4, At: sent.Add(-1 * time.Second), Pod: "bookinfo-waypoint-abc", Raw: deny},                // closer: wins
		{ID: 5, At: sent, Pod: "bookinfo-waypoint-abc", Raw: text("POST", "/reviews/0", 403, "x", "-")}, // method differs
		{ID: 6, At: sent, Pod: "bookinfo-waypoint-abc", Raw: "not an access log"},
	}
	c := ambient.Case{Request: ambient.CaseRequest{Method: "GET", Path: "/reviews/0"}}
	det, line, found := matchWaypointLine(lines, c, 403, sent, "bookinfo-waypoint", 5*time.Second)
	if !found || line.ID != 4 || det.Policy != "deny-red" || det.Namespace != "bookinfo" || det.Rule != "0" || det.Backend != "reviews" {
		t.Fatalf("%v %+v %+v", found, line, det)
	}
	if _, _, found := matchWaypointLine(lines, c, 200, sent, "bookinfo-waypoint", 5*time.Second); found {
		t.Error("status must match")
	}
	// An allowed request names its backend from the upstream cluster.
	det, line, found = matchWaypointLine([]suiteLogLine{{ID: 9, At: sent, Pod: "bookinfo-waypoint-1", Raw: ok}}, c, 200, sent, "bookinfo-waypoint", 5*time.Second)
	if !found || det.Backend != "reviews-v2" || det.Policy != "" || line.ID != 9 {
		t.Errorf("%v %+v", found, det)
	}
	if _, ok := flows.EnvoyFields(ok); !ok {
		t.Fatal("the fixture line must parse as an Envoy line")
	}
}

func TestDecisionFromExplain(t *testing.T) {
	pol := reach.Ref{Cluster: "c", Group: "security.istio.io", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespace: "bookinfo", Name: "reviews-methods"}
	x := func(verdict string, steps ...reach.Step) reach.Explanation {
		return reach.Explanation{Verdict: verdict, Steps: steps}
	}
	tests := []struct {
		name   string
		x      reach.Explanation
		status int
		want   string
		ok     bool
	}{
		{"allowed by a policy", x(reach.Allowed, reach.Step{Point: "NetworkPolicy", Verdict: "none"}, reach.Step{Point: "waypoint authorization", Verdict: reach.Allowed, Policies: []reach.Ref{pol}}), 200, "reviews-methods", true},
		{"denied by a policy", x(reach.Denied, reach.Step{Point: "waypoint authorization", Verdict: reach.Denied, Policies: []reach.Ref{pol}}), 403, "reviews-methods", true},
		{"explanation disagrees with the status", x(reach.Allowed, reach.Step{Verdict: reach.Allowed, Policies: []reach.Ref{pol}}), 403, "", false},
		{"unknown verdict", x(reach.Unknown), 200, "", false},
		{"a 5xx says nothing about policy", x(reach.Allowed, reach.Step{Verdict: reach.Allowed, Policies: []reach.Ref{pol}}), 503, "", false},
		{"allowed with no policy named", x(reach.Allowed, reach.Step{Verdict: reach.Allowed}), 200, "", false},
	}
	for _, tc := range tests {
		name, ref, kind, ok := decisionFromExplain(tc.x, tc.status)
		if ok != tc.ok || name != tc.want {
			t.Errorf("%s: %q %v", tc.name, name, ok)
		}
		if ok && (ref == nil || ref.Name != "reviews-methods" || ref.Kind != "AuthorizationPolicy" || kind != "policy") {
			t.Errorf("%s: %+v %s", tc.name, ref, kind)
		}
	}
}

func TestMeshDeciderDirectAndPrediction(t *testing.T) {
	s := &Server{WS: suiteWorkspace(t), AmbientStateDir: t.TempDir()}
	g, err := ambient.GenerateForRef(s.WS, mustRefT(t, denyRef), nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := suiteRunSpec{Cluster: "c1", Service: g.Suite.Service, Authz: g.Authz}
	d := newMeshDecider(s, spec)
	ctx := context.Background()

	direct := ambient.Case{Request: ambient.CaseRequest{Method: "GET", Path: "/reviews/0", Direct: true}}
	obs := ambient.Observation{Status: 200}
	d.Decide(ctx, g.Suite, direct, &obs, time.Now())
	if obs.DecidedBy != "ztunnel (no L7)" || obs.DecidedByKind != "ztunnel" {
		t.Errorf("%+v", obs)
	}
	obs = ambient.Observation{Status: 403}
	d.Decide(ctx, g.Suite, direct, &obs, time.Now())
	if obs.DecidedBy != "ztunnel" {
		t.Errorf("%+v", obs)
	}
	// Nothing is logged in this workspace and the sender is not explainable
	// as a workload, so the evaluator's prediction is used when it agrees.
	match := *caseByIDT(t, g.Suite, "r1-match")
	obs = ambient.Observation{Status: 403}
	d.Decide(ctx, g.Suite, match, &obs, time.Now())
	if obs.DecidedBy != "deny-red" || obs.DecidedByHow != "predicted" || obs.DecidedByRef == nil {
		t.Errorf("%+v", obs)
	}
	// It disagrees with what happened, so nothing is claimed.
	obs = ambient.Observation{Status: 200}
	d.Decide(ctx, g.Suite, match, &obs, time.Now())
	if obs.DecidedBy != "" {
		t.Errorf("a prediction that contradicts the observation must not be used: %+v", obs)
	}
	post := *caseByIDT(t, g.Suite, "x-bookinfo-reviews-methods")
	obs = ambient.Observation{Status: 403}
	d.Decide(ctx, g.Suite, post, &obs, time.Now())
	if obs.DecidedBy != "deny-red" {
		t.Errorf("%+v", obs)
	}
	clean := *caseByIDT(t, g.Suite, "r1-value-color")
	obs = ambient.Observation{Status: 200}
	d.Decide(ctx, g.Suite, clean, &obs, time.Now())
	if obs.DecidedBy != "reviews-methods" || obs.DecidedByKind != "policy" {
		t.Errorf("%+v", obs)
	}
}

func caseByIDT(t *testing.T, s *ambient.Suite, id string) *ambient.Case {
	t.Helper()
	for i := range s.Cases {
		if s.Cases[i].ID == id {
			return &s.Cases[i]
		}
	}
	t.Fatalf("no case %s", id)
	return nil
}

// ---- demo mode -------------------------------------------------------------

func TestPreviewDemoOnlyWhenEnvIsOne(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes", "1 ", "on"} {
		t.Setenv(previewDemoEnv, v)
		if previewDemo() {
			t.Errorf("%q must not enable demo mode", v)
		}
	}
	t.Setenv(previewDemoEnv, "1")
	if !previewDemo() {
		t.Error("1 enables demo mode")
	}
}

// Without the variable a run never returns canned results: with no cluster
// connection it is refused, and with a runner it reports what the runner saw.
func TestDemoNeverActiveWithoutEnv(t *testing.T) {
	t.Setenv(previewDemoEnv, "")
	_, srv := suiteServer(t)
	code, out := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": denyRef, "all": true})
	if code != 400 || out["demo"] != nil || out["results"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	runner := &fakeRunner{script: func(req suiteSendRequest) (ambient.Observation, error) {
		return ambient.Observation{Status: 418}, nil
	}}
	withFakeRunner(t, runner, nil)
	code, out = suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": denyRef, "all": true, "context": "x"})
	if code != 200 || out["demo"] != nil || len(runner.sent) != 6 {
		t.Fatalf("%d %v", code, out)
	}
	for _, r := range out["results"].([]any) {
		if r.(map[string]any)["got"].(map[string]any)["status"] != float64(418) {
			t.Errorf("results must come from the runner: %v", r)
		}
	}
	for _, v := range []string{"0", "true"} {
		t.Setenv(previewDemoEnv, v)
		if code, out := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": denyRef, "all": true}); code != 400 || out["demo"] != nil {
			t.Errorf("%q: %d %v", v, code, out)
		}
	}
}

func TestDemoRunAuthz(t *testing.T) {
	t.Setenv(previewDemoEnv, "1")
	_, srv := suiteServer(t)
	runner := &fakeRunner{script: echo(nil)}
	withFakeRunner(t, runner, nil)
	code, out := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": denyRef, "all": true})
	if code != 200 || out["demo"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if len(runner.sent) != 0 {
		t.Error("demo mode must not use a runner or a cluster")
	}
	sum := out["summary"].(map[string]any)
	if sum["total"] != float64(6) || sum["passed"] != float64(5) || sum["failed"] != float64(1) {
		t.Errorf("one failing case, as in mockup 04: %v", sum)
	}
	var failed map[string]any
	for _, r := range out["results"].([]any) {
		if r.(map[string]any)["status"] == "fail" {
			failed = r.(map[string]any)
		}
	}
	if failed["id"] != "r1-case-color" || failed["got"].(map[string]any)["status"] != float64(200) || !strings.Contains(failed["message"].(string), `color: Red" got through`) {
		t.Errorf("%v", failed)
	}
	// The suite the page re-renders expects 403 for it, and keeps it as a person's case.
	for _, c := range out["suite"].(map[string]any)["cases"].([]any) {
		m := c.(map[string]any)
		if m["id"] == "r1-case-color" && (m["expect"].(map[string]any)["status"] != float64(403) || m["generated"] != false || m["request"].(map[string]any)["headers"].(map[string]any)["color"] != "Red") {
			t.Errorf("%v", m)
		}
	}
	// The bypass case is decided by ztunnel; the deny case by this policy.
	by := map[string]string{}
	for _, r := range out["results"].([]any) {
		m := r.(map[string]any)
		by[m["id"].(string)] = m["decidedBy"].(string)
	}
	if by["bypass"] != "ztunnel (no L7)" || by["r1-match"] != "deny-red" || by["r1-value-color"] != "reviews-methods" {
		t.Errorf("%v", by)
	}
	// Deterministic: a second run is identical, and it persisted.
	_, again := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": denyRef, "all": true})
	a, _ := json.Marshal(out["results"])
	b, _ := json.Marshal(again["results"])
	if string(a) != string(b) {
		t.Errorf("demo results must be deterministic:\n%s\n%s", a, b)
	}
	_, got := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+denyRef, nil)
	if got["saved"] != true || got["lastRun"].(map[string]any)["demo"] != true || got["lastRun"].(map[string]any)["at"] != "2026-10-20T11:52:09Z" {
		t.Errorf("%v", got["lastRun"])
	}
}

func TestDemoRunRouteWeights(t *testing.T) {
	t.Setenv(previewDemoEnv, "1")
	_, srv := suiteServer(t)
	code, out := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": routeRef, "all": true})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	var w map[string]any
	for _, r := range out["results"].([]any) {
		if r.(map[string]any)["id"] == "r1-weights" {
			w = r.(map[string]any)
		}
	}
	rows := w["weights"].([]any)
	r1, r2 := rows[0].(map[string]any), rows[1].(map[string]any)
	if w["status"] != "pass" || r1["backend"] != "reviews-v1" || r1["expected"] != float64(80) || r1["observed"] != float64(79) || r2["observed"] != float64(21) {
		t.Errorf("want 79/21 against 80/20: %v", w)
	}
	got := w["got"].(map[string]any)
	if got["samples"] != float64(200) || got["p50Ms"] != float64(9) || got["p95Ms"] != float64(31) || got["statuses"].(map[string]any)["200"] != float64(200) {
		t.Errorf("%v", got)
	}
	if s := out["summary"].(map[string]any); s["failed"] != float64(0) || s["passed"] != float64(4) {
		t.Errorf("%v", s)
	}
}

// ---- real data -------------------------------------------------------------

func TestSuitesOnPreviewBundle(t *testing.T) {
	files, _ := filepath.Glob("../../.preview/flightbay-nocase-*.tar.zst")
	if len(files) == 0 {
		t.Skip("no preview bundle")
	}
	f, err := os.Open(files[len(files)-1])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ws := store.New()
	defer ws.Close()
	if err := importer.Run(context.Background(), f, ws, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(previewDemoEnv, "1")
	s := &Server{WS: ws, Token: tok, WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	srv := httptest.NewServer(s.Handler())
	defer func() { srv.Close(); s.Close() }()

	code, list := suiteCall(t, srv, "GET", "/api/ambient/suites/service?cluster=kind-mesh2&namespace=bookinfo&name=reviews", nil)
	if code != 200 || len(list["suites"].([]any)) != 2 {
		t.Fatalf("%d %v", code, list)
	}
	var authz, route string
	for _, v := range list["suites"].([]any) {
		m := v.(map[string]any)
		ref := m["ref"].(map[string]any)
		id := "kind-mesh2/" + ref["kind"].(string) + "/" + ref["namespace"].(string) + "/" + ref["name"].(string)
		if ref["kind"] == "AuthorizationPolicy" {
			authz = id
		} else {
			route = id
		}
	}
	code, out := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+authz, nil)
	if code != 200 || out["explain"].(map[string]any)["inWords"] != "Deny any request to reviews when header color equals red." {
		t.Fatalf("%d %v", code, out)
	}
	code, run := suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": authz, "all": true})
	if code != 200 || run["summary"].(map[string]any)["failed"] != float64(1) {
		t.Fatalf("%d %v", code, run)
	}
	code, run = suiteCall(t, srv, "POST", "/api/ambient/suites/run", map[string]any{"ref": route, "all": true})
	if code != 200 || run["summary"].(map[string]any)["failed"] != float64(0) {
		t.Fatalf("%d %v", code, run)
	}
	// The bundle's route is 90/10, so the canned split reads 89/11.
	for _, r := range run["results"].([]any) {
		if r.(map[string]any)["id"] == "r1-weights" {
			rows := r.(map[string]any)["weights"].([]any)
			if rows[0].(map[string]any)["expected"] != float64(90) || rows[0].(map[string]any)["observed"] != float64(89) {
				t.Errorf("%v", rows)
			}
		}
	}
}
