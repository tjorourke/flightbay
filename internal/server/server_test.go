package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

const tok = "desk-token"

var (
	podsRes   = bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}
	svcRes    = bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}
	rsRes     = bundle.APIResource{Group: "apps", Version: "v1", Resource: "replicasets", Kind: "ReplicaSet", Namespaced: true}
	deployRes = bundle.APIResource{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true}
	routeRes  = bundle.APIResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true}
)

func md(ns, name, uid string, labels map[string]any, owners ...map[string]any) map[string]any {
	m := map[string]any{"name": name, "namespace": ns, "uid": uid, "labels": labels, "creationTimestamp": "2026-09-27T09:56:00Z"}
	if len(owners) > 0 {
		var o []any
		for _, x := range owners {
			o = append(o, x)
		}
		m["ownerReferences"] = o
	}
	return m
}

func workspace(t *testing.T) *store.Workspace {
	t.Helper()
	ws := store.New()
	p, _ := policy.Profile("standard")
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{BundleID: "b1", Case: "01234567", Contexts: []string{"mesh1", "mesh2"}, Policy: b, CaptureStart: time.Date(2026, 9, 27, 10, 12, 0, 0, time.UTC), Collector: "flightbay test"})
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseLoading })
	ws.SetVersion("mesh1", json.RawMessage(`{"gitVersion":"v1.34.1"}`))
	ws.AddObjects("mesh1", deployRes, []map[string]any{{"kind": "Deployment", "metadata": md("payments", "checkout", "d1", map[string]any{"app": "checkout"}), "spec": map[string]any{"replicas": float64(2)}}}, "c")
	ws.AddObjects("mesh1", rsRes, []map[string]any{{"kind": "ReplicaSet", "metadata": md("payments", "checkout-6d8b9c", "rs1", nil, map[string]any{"kind": "Deployment", "name": "checkout", "uid": "d1"})}}, "c")
	ws.AddObjects("mesh1", podsRes, []map[string]any{{"kind": "Pod",
		"metadata": md("payments", "checkout-6d8b9c-f2k7p", "p1", map[string]any{"app": "checkout"}, map[string]any{"kind": "ReplicaSet", "name": "checkout-6d8b9c", "uid": "rs1"}),
		"spec":     map[string]any{"nodeName": "worker-2", "containers": []any{map[string]any{"name": "app"}}},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app", "restartCount": float64(12), "ready": false,
			"state":     map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
			"lastState": map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1), "finishedAt": "2026-09-27T10:08:42Z"}}}}}}}, "c")
	ws.AddObjects("mesh1", svcRes, []map[string]any{{"kind": "Service", "metadata": md("payments", "checkout", "s1", nil), "spec": map[string]any{"selector": map[string]any{"app": "checkout"}}}}, "c")
	route := func(backend string) map[string]any {
		return map[string]any{"kind": "HTTPRoute", "metadata": md("payments", "checkout-route", "r-"+backend, nil),
			"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": backend, "port": float64(8080)}}}}}}
	}
	ws.AddObjects("mesh1", routeRes, []map[string]any{route("checkout-v2")}, "c")
	ws.AddObjects("mesh2", routeRes, []map[string]any{route("checkout")}, "c")
	ws.AddEvents("mesh1", []map[string]any{{"kind": "Event", "metadata": map[string]any{"name": "e1", "namespace": "payments"}, "type": "Warning", "reason": "BackOff",
		"message": "Back-off restarting failed container", "lastTimestamp": "2026-09-27T10:08:42Z",
		"involvedObject": map[string]any{"kind": "Pod", "name": "checkout-6d8b9c-f2k7p", "namespace": "payments"}}})
	ws.AddLogLines("mesh1", "payments", "checkout-6d8b9c-f2k7p", "app", "previous", []string{
		"2026-09-27T10:07:12Z ERROR database connection timed out trace_id=a4e98f02",
		"2026-09-27T10:07:13Z ERROR database connection timed out trace_id=a4e98f02",
		"2026-09-27T10:07:14Z ERROR database connection timed out trace_id=a4e98f02",
	}, "l")
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.core.v1.pods", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", State: bundle.StateComplete, Items: 1})
	return ws
}

func newServer(t *testing.T, terminal bool) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{WS: workspace(t), Token: tok, WorkDir: t.TempDir(), Terminal: terminal, Shell: "/bin/bash", DeskBinary: "/bin/echo"}
	srv := httptest.NewServer(s.Handler())
	s.BaseURL = srv.URL
	t.Cleanup(func() { srv.Close(); s.Close() })
	return s, srv
}

func get(t *testing.T, srv *httptest.Server, path string, v any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil {
		_ = json.NewDecoder(resp.Body).Decode(v)
	}
	return resp.StatusCode
}

func TestAuthCookieFlow(t *testing.T) {
	_, srv := newServer(t, false)
	resp, _ := http.Get(srv.URL + "/api/workspace")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request allowed: %d", resp.StatusCode)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(srv.URL + "/?token=" + tok)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("token login failed: %v %d", err, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "flightbay") {
		t.Fatal("UI not served")
	}
	u, _ := url.Parse(srv.URL)
	if len(jar.Cookies(u)) == 0 {
		t.Fatal("no session cookie")
	}
	resp, _ = c.Get(srv.URL + "/api/workspace")
	if resp.StatusCode != 200 {
		t.Fatalf("cookie not accepted: %d", resp.StatusCode)
	}
	resp, _ = c.Get(srv.URL + "/?token=wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token accepted: %d", resp.StatusCode)
	}
	for _, asset := range []string{"/static/app.js", "/static/app.css", "/static/vendor/xterm.js"} {
		resp, _ = c.Get(srv.URL + asset)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", asset, resp.StatusCode)
		}
	}
}

func TestWorkspaceSummary(t *testing.T) {
	_, srv := newServer(t, false)
	var w map[string]any
	if code := get(t, srv, "/api/workspace", &w); code != 200 {
		t.Fatalf("code %d", code)
	}
	if w["case"] != "01234567" || len(w["clusters"].([]any)) != 2 || w["state"].(map[string]any)["phase"] != "loading" {
		t.Fatalf("workspace %v", w)
	}
	c0 := w["clusters"].([]any)[0].(map[string]any)
	if c0["name"] != "mesh1" || c0["version"] != "v1.34.1" || c0["pods"].(float64) != 1 {
		t.Fatalf("cluster summary %v", c0)
	}
	if w["findings"].(map[string]any)["critical"].(float64) < 1 {
		t.Fatalf("findings summary %v", w["findings"])
	}
}

func TestResourcesAndObjects(t *testing.T) {
	_, srv := newServer(t, false)
	var types []map[string]any
	get(t, srv, "/api/resources?cluster=mesh1", &types)
	var pods map[string]any
	for _, ty := range types {
		if ty["resource"] == "pods" {
			pods = ty
		}
	}
	if pods == nil || pods["count"].(float64) != 1 || pods["state"] != "complete" {
		t.Fatalf("resource types %v", types)
	}
	var list map[string]any
	get(t, srv, "/api/objects?cluster=mesh1&group=&resource=pods", &list)
	cols := list["columns"].([]any)
	rows := list["rows"].([]any)
	if cols[0] != "Name" || len(rows) != 1 || rows[0].(map[string]any)["cells"].([]any)[2] != "CrashLoopBackOff" {
		t.Fatalf("objects %v", list)
	}
	get(t, srv, "/api/objects?cluster=&resource=httproutes&group=gateway.networking.k8s.io", &list)
	if len(list["rows"].([]any)) != 2 {
		t.Fatalf("all-cluster listing %v", list)
	}
	get(t, srv, "/api/objects?cluster=mesh1&resource=pods&group=&q=nomatch", &list)
	if len(list["rows"].([]any)) != 0 {
		t.Fatal("filter ignored")
	}
}

func TestObjectDetailRelated(t *testing.T) {
	_, srv := newServer(t, false)
	var d map[string]any
	code := get(t, srv, "/api/object?cluster=mesh1&group=&resource=pods&namespace=payments&name=checkout-6d8b9c-f2k7p", &d)
	if code != 200 || !strings.Contains(d["yaml"].(string), "CrashLoopBackOff") {
		t.Fatalf("object detail %d %v", code, d)
	}
	rel := map[string]bool{}
	for _, r := range d["related"].([]any) {
		rm := r.(map[string]any)
		rel[rm["relation"].(string)+":"+rm["kind"].(string)+"/"+rm["name"].(string)] = true
	}
	for _, want := range []string{"owner:ReplicaSet/checkout-6d8b9c", "owner:Deployment/checkout", "selected by:Service/checkout"} {
		if !rel[want] {
			t.Fatalf("related missing %s: %v", want, rel)
		}
	}
	if len(d["events"].([]any)) != 1 || len(d["streams"].([]any)) != 1 {
		t.Fatalf("events/streams %v %v", d["events"], d["streams"])
	}
	if get(t, srv, "/api/object?cluster=mesh1&group=&resource=pods&namespace=payments&name=nope", nil) != 404 {
		t.Fatal("expected 404")
	}
	get(t, srv, "/api/object?cluster=mesh1&group=&resource=services&namespace=payments&name=checkout", &d)
	found := false
	for _, r := range d["related"].([]any) {
		rm := r.(map[string]any)
		found = found || (rm["relation"] == "selects" && rm["kind"] == "Pod")
	}
	if !found {
		t.Fatalf("service should select the pod: %v", d["related"])
	}
}

func TestLogsFindingsTimelineDiffSearch(t *testing.T) {
	_, srv := newServer(t, false)
	var res map[string]any
	get(t, srv, "/api/logs/search?q=trace_id:a4e98f02&stream=previous", &res)
	if res["total"].(float64) != 3 || !res["loading"].(bool) {
		t.Fatalf("log search %v", res)
	}
	id := res["records"].([]any)[0].(map[string]any)["id"].(float64)
	var ctx []any
	get(t, srv, "/api/logs/context?n=1&id="+strings.TrimSuffix(strings.TrimSuffix(jsonNum(id), ".0"), ".00"), &ctx)
	if len(ctx) == 0 {
		t.Fatal("no context")
	}
	if get(t, srv, "/api/logs/search?q=%22bad", nil) != 400 {
		t.Fatal("bad query not rejected")
	}
	var findings []map[string]any
	get(t, srv, "/api/findings", &findings)
	rules := map[string]bool{}
	for _, f := range findings {
		rules[f["rule"].(string)] = true
	}
	if !rules["workload.crashloop"] || !rules["logs.error-signature"] {
		t.Fatalf("findings %v", rules)
	}
	var tl []map[string]any
	get(t, srv, "/api/timeline?cluster=mesh1", &tl)
	kinds := map[string]bool{}
	for _, i := range tl {
		kinds[i["kind"].(string)] = true
	}
	if !kinds["event"] || !kinds["termination"] || !kinds["error-spike"] {
		t.Fatalf("timeline kinds %v", kinds)
	}
	var diff map[string]any
	get(t, srv, "/api/diff?group=gateway.networking.k8s.io&resource=httproutes&namespace=payments&name=checkout-route", &diff)
	sides := diff["clusters"].([]any)
	if len(sides) != 2 || !strings.Contains(sides[0].(map[string]any)["yaml"].(string), "checkout-v2") || strings.Contains(sides[0].(map[string]any)["yaml"].(string), "uid:") {
		t.Fatalf("diff %v", diff)
	}
	var hits []map[string]any
	get(t, srv, "/api/search?q=checkout-route", &hits)
	if len(hits) != 2 {
		t.Fatalf("search hits %v", hits)
	}
	var cov map[string]any
	get(t, srv, "/api/coverage", &cov)
	if len(cov["coverage"].([]any)) != 1 {
		t.Fatalf("coverage %v", cov)
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func TestStateStream(t *testing.T) {
	s, srv := newServer(t, false)
	req, _ := http.NewRequest("GET", srv.URL+"/api/state/stream", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	readEvent := func() map[string]any {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				var v map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v)
				return v
			}
		}
	}
	first := readEvent()
	s.WS.SetState(func(st *store.ImportState) { st.Phase = store.PhaseComplete; st.Verified = true })
	for i := 0; i < 5; i++ {
		ev := readEvent()
		if ev["phase"] == "complete" {
			return
		}
	}
	t.Fatalf("state change not streamed (first %v)", first)
}

func TestKubeconfigAndHelpersWritten(t *testing.T) {
	s, srv := newServer(t, false)
	if err := s.Prepare(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.WorkDir, "kubeconfig"))
	if err != nil || !strings.Contains(string(b), srv.URL+"/k8s/clusters/mesh1") || !strings.Contains(string(b), "mesh2@snapshot") {
		t.Fatalf("kubeconfig %s %v", b, err)
	}
	for _, h := range []string{"fbq", "fbfind", "fbpc"} {
		if st, err := os.Stat(filepath.Join(s.WorkDir, "bin", h)); err != nil || st.Mode()&0o100 == 0 {
			t.Fatalf("helper %s missing or not executable", h)
		}
	}
	var v map[string]any
	req, _ := http.NewRequest("GET", srv.URL+"/k8s/clusters/mesh1/version", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&v)
	if v["gitVersion"] != "v1.34.1" {
		t.Fatalf("fake apiserver not mounted: %v", v)
	}
}

func TestTerminalWebsocket(t *testing.T) {
	s, srv := newServer(t, true)
	if err := s.Prepare(); err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/terminal"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}, "Origin": {"http://evil.example"}}}); err == nil {
		t.Fatal("cross-origin terminal allowed")
	}
	dial := func() *websocket.Conn {
		c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}, "Origin": {srv.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	readUntil := func(c *websocket.Conn, want string) string {
		var acc strings.Builder
		for !strings.Contains(acc.String(), want) {
			_, b, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("read: %v (so far %q)", err, acc.String())
			}
			acc.Write(b)
		}
		return acc.String()
	}
	c := dial()
	msg, _ := json.Marshal(map[string]any{"type": "input", "data": "echo KC=$KUBECONFIG; echo done-$((40+2))\n"})
	_ = c.Write(ctx, websocket.MessageText, msg)
	out := readUntil(c, "done-42")
	if !strings.Contains(out, filepath.Join(s.WorkDir, "kubeconfig")) {
		t.Fatalf("KUBECONFIG not set in shell: %q", out)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	c2 := dial()
	defer c2.Close(websocket.StatusNormalClosure, "")
	if replay := readUntil(c2, "done-42"); !strings.Contains(replay, "done-42") {
		t.Fatal("scrollback not replayed on reconnect")
	}
}

func TestTerminalDisabled(t *testing.T) {
	_, srv := newServer(t, false)
	if code := get(t, srv, "/api/terminal", nil); code != http.StatusNotFound {
		t.Fatalf("terminal should be disabled: %d", code)
	}
}

func TestLoadDeferred(t *testing.T) {
	s, srv := newServer(t, false)
	called := make(chan bool, 1)
	s.Reload = func() error { called <- true; return nil }
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/load", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	if code := post("http://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-origin load allowed: %d", code)
	}
	if code := post(srv.URL); code != http.StatusAccepted {
		t.Fatalf("load: %d", code)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("reload not started")
	}
	if get(t, srv, "/api/load", nil) != http.StatusMethodNotAllowed {
		t.Fatal("GET should not trigger a load")
	}
}

func TestProxiesAPI(t *testing.T) {
	s, srv := newServer(t, false)
	ws := s.WS
	ws.AddDiagnostic("mesh1", "proxies", "istio-system", "ztunnel-a", "meta.json", []byte(`{"component":"ztunnel","container":"istio-proxy","image":"x/ztunnel:1.30.4","version":"1.30.4","node":"w1"}`))
	ws.AddDiagnostic("mesh1", "proxies", "istio-system", "ztunnel-a", "config_dump.json", []byte(`{"workloads":[{"name":"checkout","namespace":"payments"}],"services":[],"certificates":[{"identity":"spiffe://c/ns/payments/sa/x","certChain":[{"expirationTime":"2026-09-26T00:00:00Z"}]}]}`))
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.proxies.istio-system.ztunnel-a.config_dump", Cluster: "mesh1", Type: bundle.CoverageDiagnostic, Namespace: "istio-system", Pod: "ztunnel-a", Container: "ztunnel", Resource: "config_dump.json", State: bundle.StateComplete, Reason: "3 entries naming namespaces outside the selection removed"})
	// A component whose port-forward failed has coverage but no files.
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.control-plane.istio-system.istiod-a.syncz", Cluster: "mesh1", Type: bundle.CoverageDiagnostic, Namespace: "istio-system", Pod: "istiod-a", Container: "istiod", Resource: "syncz.json", State: bundle.StateFailed, Reason: "port-forward failed: connection refused"})

	var list struct {
		Components []diagRow `json:"components"`
	}
	if code := get(t, srv, "/api/proxies", &list); code != 200 || len(list.Components) != 2 {
		t.Fatalf("list %d %+v", code, list)
	}
	var zt, id diagRow
	for _, c := range list.Components {
		if c.Pod == "ztunnel-a" {
			zt = c
		} else {
			id = c
		}
	}
	if zt.Component != "ztunnel" || zt.Counts["workloads"] != 1 || zt.Expired != 1 || zt.Files[0].Reason == "" {
		t.Fatalf("ztunnel row %+v", zt)
	}
	if id.Component != "istiod" || id.Kind != "control-plane" || len(id.Files) != 1 || id.Files[0].State != bundle.StateFailed {
		t.Fatalf("failed istiod must still be listed with its reason: %+v", id)
	}
	var detail struct {
		Summary struct {
			Tables []struct{ Title string } `json:"tables"`
		} `json:"summary"`
	}
	if code := get(t, srv, "/api/proxy?cluster=mesh1&kind=proxies&namespace=istio-system&pod=ztunnel-a", &detail); code != 200 || len(detail.Summary.Tables) == 0 {
		t.Fatalf("detail %d %+v", code, detail)
	}
	var raw map[string]any
	if code := get(t, srv, "/api/proxy/file?cluster=mesh1&kind=proxies&namespace=istio-system&pod=ztunnel-a&file=config_dump.json", &raw); code != 200 || raw["workloads"] == nil {
		t.Fatalf("file %d %v", code, raw)
	}
	if code := get(t, srv, "/api/proxy/file?cluster=mesh1&kind=proxies&namespace=istio-system&pod=ztunnel-a&file=../../etc/passwd", &raw); code != 404 {
		t.Fatalf("unknown file should 404, got %d", code)
	}
}

// Two desks on the same host keep separate sessions: cookies ignore the port.
func TestTwoDesksOneBrowser(t *testing.T) {
	_, a := newServer(t, false)
	sb := &Server{WS: workspace(t), Token: "other-token", WorkDir: t.TempDir()}
	b := httptest.NewServer(sb.Handler())
	t.Cleanup(func() { b.Close(); sb.Close() })
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	for _, login := range []string{a.URL + "/?token=" + tok, b.URL + "/?token=other-token"} {
		if resp, err := c.Get(login); err != nil || resp.StatusCode != 200 {
			t.Fatalf("login %s: %v", login, err)
		}
	}
	for _, u := range []string{a.URL, b.URL} {
		if resp, _ := c.Get(u + "/api/workspace"); resp.StatusCode != 200 {
			t.Fatalf("%s lost its session after the other desk signed in: %d", u, resp.StatusCode)
		}
	}
}

func TestRBACAndResMapRoutes(t *testing.T) {
	_, srv := newServer(t, false)
	var ex map[string]any
	if code := get(t, srv, "/api/rbac/serviceaccount?cluster=mesh1&namespace=payments&name=checkout", &ex); code != 200 {
		t.Fatalf("sa code %d", code)
	}
	if code := get(t, srv, "/api/rbac/who?cluster=mesh1&namespace=payments&verb=get&resource=pods", &ex); code != 200 {
		t.Fatalf("who code %d", code)
	}
	var m map[string]any
	if code := get(t, srv, "/api/resources/map?cluster=mesh1", &m); code != 200 {
		t.Fatalf("map code %d", code)
	}
	if code := get(t, srv, "/api/rbac/serviceaccount", nil); code != 400 {
		t.Fatal("missing query not 400")
	}
	// post not allowed
	req, _ := http.NewRequest("POST", srv.URL+"/api/rbac/serviceaccount", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 405 && resp.StatusCode != 401 {
		t.Fatalf("post rbac got %d want 405", resp.StatusCode)
	}
}
