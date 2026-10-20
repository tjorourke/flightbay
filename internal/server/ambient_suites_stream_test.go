package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
)

// streamCall posts with Accept: application/x-ndjson and returns every event.
func streamCall(t *testing.T, ctx context.Context, srv *httptest.Server, body any, onEvent func(map[string]any) bool) (int, string, []map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/ambient/suites/run", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		ev := map[string]any{}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("not one JSON object per line: %q", sc.Text())
		}
		events = append(events, ev)
		if onEvent != nil && !onEvent(ev) {
			break
		}
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), events
}

// sampleRunner reports each sample of a weights case as it finishes.
func sampleRunner(override map[string]ambient.Observation) *fakeRunner {
	base := echo(override)
	return &fakeRunner{script: func(req suiteSendRequest) (ambient.Observation, error) {
		o, err := base(req)
		if req.OnSample != nil {
			for _, s := range o.Samples {
				req.OnSample(s)
			}
		}
		return o, err
	}}
}

func ruleCaseID(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	_, gen := suiteCall(t, srv, "GET", "/api/ambient/suites?ref="+routeRef, nil)
	for _, c := range gen["cases"].([]any) {
		m := c.(map[string]any)
		if e := m["expect"].(map[string]any); e["rule"] != nil && e["rule"].(float64) > 0 {
			return m["id"].(string), m["request"].(map[string]any)["path"].(string)
		}
	}
	t.Fatal("the generated route suite has no rule case")
	return "", ""
}

func TestSuitesRunStreams(t *testing.T) {
	_, srv := suiteServer(t)
	ruleID, _ := ruleCaseID(t, srv)
	// The rule case comes back without a backend, as GET /health did.
	runner := sampleRunner(map[string]ambient.Observation{ruleID: {Status: 200, Connection: "fake"}})
	withFakeRunner(t, runner, nil)
	body := map[string]any{"ref": routeRef, "all": true, "context": "kind-c1", "save": false, "source": map[string]any{"kind": "ephemeral", "ref": map[string]any{"namespace": "bookinfo"}}}
	code, ctype, events := streamCall(t, context.Background(), srv, body, nil)
	if code != 200 || ctype != "application/x-ndjson" {
		t.Fatalf("%d %s", code, ctype)
	}
	if events[0]["type"] != "start" {
		t.Fatalf("first event %v", events[0])
	}
	ids := events[0]["cases"].([]any)
	if int(events[0]["total"].(float64)) != len(ids) || len(ids) < 3 {
		t.Fatalf("start %v", events[0])
	}
	last := events[len(events)-1]
	if last["type"] != "done" || last["suite"] == nil || last["lastRun"] == nil || last["summary"] == nil {
		t.Fatalf("last event %v", last)
	}
	// Per case: case-start, samples for a weights case, then case-done.
	cur, seen, samples := "", 0, 0
	lastCompleted := 0.0
	for _, ev := range events[1 : len(events)-1] {
		switch ev["type"] {
		case "case-start":
			if cur != "" {
				t.Fatalf("case-start %v before case-done of %s", ev["id"], cur)
			}
			cur, lastCompleted = ev["id"].(string), 0
			if cur != ids[seen] {
				t.Errorf("cases out of order: %s, want %s", cur, ids[seen])
			}
		case "sample":
			samples++
			if ev["id"] != cur {
				t.Errorf("sample for %v during %s", ev["id"], cur)
			}
			c := ev["completed"].(float64)
			if c <= lastCompleted || ev["requested"].(float64) != 200 {
				t.Errorf("samples must be cumulative: %v", ev)
			}
			lastCompleted = c
			total := 0.0
			for _, v := range ev["backends"].(map[string]any) {
				total += v.(float64)
			}
			if total != c || ev["statuses"].(map[string]any)["200"].(float64) != c || ev["p50Ms"].(float64) != 5 {
				t.Errorf("sample counts %v", ev)
			}
		case "case-done":
			if ev["id"] != cur || ev["result"].(map[string]any)["id"] != cur {
				t.Errorf("case-done %v during %s", ev["id"], cur)
			}
			if cur == ruleID {
				res := ev["result"].(map[string]any)
				got := res["got"].(map[string]any)
				if res["status"] != "unknown" || got["text"] != ambient.GotUnknownBackend || got["rule"] != float64(1) {
					t.Errorf("a rule case with no backend is unknown and names the matched rule: %v", res)
				}
			}
			cur = ""
			seen++
		default:
			t.Errorf("unexpected event %v", ev)
		}
	}
	if seen != len(ids) || samples < 20 || lastCompleted != 0 && lastCompleted != 200 {
		t.Errorf("seen %d of %d, %d sample events", seen, len(ids), samples)
	}
	// done carries exactly the non-streaming body.
	_, plain := suiteCall(t, srv, "POST", "/api/ambient/suites/run", body)
	delete(last, "type")
	for k := range plain {
		if _, ok := last[k]; !ok {
			t.Errorf("done lacks %q", k)
		}
	}
	for k := range last {
		if _, ok := plain[k]; !ok {
			t.Errorf("done has extra %q", k)
		}
	}
	if len(last["results"].([]any)) != len(plain["results"].([]any)) || last["summary"].(map[string]any)["unknown"] != float64(1) {
		t.Errorf("done %v vs %v", last["summary"], plain["summary"])
	}
}

// blockingRunner answers the first case at once and holds the second until
// the request is cancelled.
type blockingRunner struct {
	sent   atomic.Int32
	closed chan struct{}
	first  bool // block on the first case instead
}

func (b *blockingRunner) Send(ctx context.Context, req suiteSendRequest) (ambient.Observation, error) {
	n := b.sent.Add(1)
	if n == 1 && !b.first {
		return ambient.Observation{Status: req.Case.Expect.Status, Connection: "fake"}, nil
	}
	<-ctx.Done()
	return ambient.Observation{Connection: "fake"}, nil
}

func (b *blockingRunner) Close() { close(b.closed) }

func TestSuitesRunCancelKeepsFinishedCases(t *testing.T) {
	s, srv := suiteServer(t)
	run := &blockingRunner{closed: make(chan struct{})}
	withFakeRunner(t, nil, nil)
	newSuiteRunner = func(*Server, context.Context, suiteRunSpec) (suiteRunner, suiteDecider, error) { return run, nil, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var firstDone string
	streamCall(t, ctx, srv, map[string]any{"ref": denyRef, "all": true, "context": "kind-c1"}, func(ev map[string]any) bool {
		if ev["type"] == "case-done" {
			firstDone = ev["id"].(string)
			cancel()
			return false
		}
		return true
	})
	select {
	case <-run.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not stop when the client went away")
	}
	dir, _ := s.suiteDir()
	var saved *ambient.Suite
	for i := 0; i < 50 && (saved == nil || saved.Last == nil); i++ {
		time.Sleep(20 * time.Millisecond)
		saved, _ = ambient.LoadSuite(dir, mustRefT(t, denyRef))
	}
	if saved == nil || saved.Last == nil {
		t.Fatal("a cancelled run with a finished case saves that case")
	}
	if !saved.Last.Summary.Cancelled || len(saved.Last.Results) != 1 || saved.Last.Results[0].ID != firstDone {
		t.Errorf("saved %+v", saved.Last)
	}
	if n := run.sent.Load(); n != 2 {
		t.Errorf("no case is sent after the stop: %d sent", n)
	}

	// Stopped before anything finished: nothing is saved.
	ref2 := "c1/AuthorizationPolicy/bookinfo/reviews-methods"
	run2 := &blockingRunner{closed: make(chan struct{}), first: true}
	newSuiteRunner = func(*Server, context.Context, suiteRunSpec) (suiteRunner, suiteDecider, error) { return run2, nil, nil }
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	streamCall(t, ctx2, srv, map[string]any{"ref": ref2, "all": true, "context": "kind-c1"}, func(ev map[string]any) bool {
		if ev["type"] == "case-start" {
			cancel2()
			return false
		}
		return true
	})
	select {
	case <-run2.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not stop")
	}
	time.Sleep(100 * time.Millisecond)
	if got, _ := ambient.LoadSuite(dir, mustRefT(t, ref2)); got != nil {
		t.Errorf("a run stopped before any case finished must not be saved: %+v", got.Last)
	}
}

func TestSampleConcurrentlyStopsLaunching(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var launched atomic.Int32
	var reported []ambient.Sample
	res := sampleConcurrently(ctx, 200, 4, func() httpProbeResult {
		launched.Add(1)
		return httpProbeResult{Status: 200, DurationMS: 3}
	}, func(r httpProbeResult) ambient.Sample {
		return ambient.Sample{Status: r.Status, DurationMs: r.DurationMS}
	}, func(s ambient.Sample) {
		reported = append(reported, s)
		if len(reported) == 10 {
			cancel()
		}
	})
	if n := int(launched.Load()); n >= 200 || n < 10 {
		t.Errorf("launched %d", n)
	}
	if len(res) != len(reported) {
		t.Errorf("every result is reported once: %d vs %d", len(res), len(reported))
	}
	// Requests cut short by the stop are dropped, not counted as failures.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	res = sampleConcurrently(ctx2, 10, 2, func() httpProbeResult { return httpProbeResult{} }, func(httpProbeResult) ambient.Sample { return ambient.Sample{} }, nil)
	if len(res) != 0 {
		t.Errorf("%d", len(res))
	}
	all := sampleConcurrently(context.Background(), 30, 8, func() httpProbeResult { return httpProbeResult{Status: 503} }, func(r httpProbeResult) ambient.Sample { return ambient.Sample{Status: r.Status} }, nil)
	if len(all) != 30 {
		t.Errorf("%d", len(all))
	}
}

func TestSampleTallyThrottles(t *testing.T) {
	var evs []runEventSample
	tl := newSampleTally("w", 25, time.Hour, func(e any) { evs = append(evs, e.(runEventSample)) })
	for i := 0; i < 25; i++ {
		b := "v1"
		if i%5 == 0 {
			b = "v2"
		}
		tl.add(ambient.Sample{Status: 200, Backend: b, DurationMs: int64(i)})
	}
	tl.flush()
	if len(evs) != 3 || evs[0].Completed != 10 || evs[1].Completed != 20 || evs[2].Completed != 25 {
		t.Fatalf("%+v", evs)
	}
	if evs[2].Backends["v1"] != 20 || evs[2].Backends["v2"] != 5 || evs[0].Backends["v2"] != 2 || evs[2].P50Ms != 12 || evs[2].P95Ms != 23 {
		t.Errorf("%+v", evs[2])
	}
}

// ---- decided by across all policies -----------------------------------------

func TestDeciderOnRoutePageUsesEveryPolicy(t *testing.T) {
	s := &Server{WS: suiteWorkspace(t), AmbientStateDir: t.TempDir()}
	g, err := ambient.GenerateForRef(s.WS, mustRefT(t, routeRef), nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Authz != nil {
		t.Fatal("a route suite has no policy input of its own")
	}
	d := newMeshDecider(s, suiteRunSpec{Cluster: "c1", Service: g.Suite.Service})
	ctx := context.Background()
	red := ambient.Case{Request: ambient.CaseRequest{Method: "GET", Path: "/", Headers: map[string]string{"color": "red"}}}
	obs := ambient.Observation{Status: 403, Client: "bookinfo/client-1"}
	d.Decide(ctx, g.Suite, red, &obs, time.Now())
	if obs.DecidedBy != "deny-red" || obs.DecidedByHow != "predicted" || obs.DecidedByRef == nil || obs.DecidedByRef.Name != "deny-red" {
		t.Errorf("%+v", obs)
	}
	plain := ambient.Case{Request: ambient.CaseRequest{Method: "GET", Path: "/"}}
	obs = ambient.Observation{Status: 404, Client: "bookinfo/client-1"}
	d.Decide(ctx, g.Suite, plain, &obs, time.Now())
	if obs.DecidedBy != "reviews-methods" {
		t.Errorf("an application 404 is still a request the policies let through: %+v", obs)
	}
	obs = ambient.Observation{Status: 403, Client: "bookinfo/client-1"}
	d.Decide(ctx, g.Suite, plain, &obs, time.Now())
	if obs.DecidedBy != "" || !strings.Contains(obs.DecisionNote, "no policy predicts 403") {
		t.Errorf("%+v", obs)
	}
	res := ambient.Evaluate(ambient.Case{ID: "x", Expect: ambient.CaseExpect{Status: 200}}, obs, time.Now())
	if res.DecidedBy != "" || res.DecisionNote == "" {
		t.Errorf("%+v", res)
	}
}

func TestDecisionNoteWording(t *testing.T) {
	if n := decisionNote(true, true, ambient.PolicyPrediction{Known: true, Status: 200}, 403); n != "No waypoint log line matched and no policy predicts 403 (the policies predict 200)" {
		t.Errorf("%q", n)
	}
	if n := decisionNote(true, false, ambient.PolicyPrediction{Verdict: "Flightbay cannot tell whether DENY a/b matches"}, 403); !strings.HasPrefix(n, "Waypoint access logging is off") || !strings.Contains(n, "cannot tell") {
		t.Errorf("%q", n)
	}
}

// ---- explain ------------------------------------------------------------------

func explainCall(t *testing.T, srv *httptest.Server, body map[string]any) (int, map[string]any) {
	t.Helper()
	return suiteCall(t, srv, "POST", "/api/ambient/suites/explain", body)
}

func TestSuitesExplain(t *testing.T) {
	_, srv := suiteServer(t)
	runner := &fakeRunner{script: func(req suiteSendRequest) (ambient.Observation, error) {
		o := ambient.Observation{Client: "bookinfo/client-1", Connection: "In-mesh curl from bookinfo/client-1 via the Service", DurationMs: 12}
		switch {
		case req.Case.Request.Headers["color"] == "red":
			o.Status = 403
		case req.Case.Request.Path == "/":
			o.Status, o.Backend = 404, "reviews-v1"
		default:
			o.Status, o.Backend = 200, "reviews-v2"
		}
		return o, nil
	}}
	specs := withFakeRunner(t, runner, nil)
	src := map[string]any{"kind": "workload", "ref": map[string]any{"namespace": "bookinfo", "name": "client-1"}}

	code, out := explainCall(t, srv, map[string]any{"ref": routeRef, "context": "kind-c1", "source": src, "request": map[string]any{"method": "GET", "path": "/", "headers": map[string]any{"color": "red"}}})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if len(*specs) != 1 || (*specs)[0].Context != "kind-c1" || (*specs)[0].Source.Ref.Name != "client-1" || (*specs)[0].Service.Name != "reviews" {
		t.Errorf("spec %+v", *specs)
	}
	got := out["got"].(map[string]any)
	if got["status"] != float64(403) || got["ms"] != float64(12) || out["source"] != "bookinfo/client-1" || out["cluster"] != "c1" {
		t.Errorf("got %v source %v", got, out["source"])
	}
	route := out["route"].(map[string]any)
	rules := route["rules"].([]any)
	if route["matchedRule"] != float64(1) || route["name"] != "split" || len(rules) != 1 || rules[0].(map[string]any)["reason"] != "path / starts with /" || rules[0].(map[string]any)["matches"].([]any)[0] != "PathPrefix /" {
		t.Errorf("route %v", route)
	}
	pols := out["policies"].([]any)
	if len(pols) != 2 {
		t.Fatalf("policies %v", pols)
	}
	deny, allow := pols[0].(map[string]any), pols[1].(map[string]any)
	if deny["name"] != "deny-red" || deny["action"] != "DENY" || deny["order"] != float64(2) || deny["matched"] != true || deny["decided"] != true || !strings.HasPrefix(deny["reason"].(string), "header color is red") || deny["scope"] != "Service reviews" {
		t.Errorf("deny %v", deny)
	}
	if allow["name"] != "reviews-methods" || allow["order"] != float64(3) || allow["matched"] != true {
		t.Errorf("allow %v", allow)
	}
	if out["verdict"] != "Denied by bookinfo/deny-red" || out["decidedBy"] != "deny-red" || out["confirmedByLog"] != false || out["logAvailable"] != false {
		t.Errorf("verdict %v decidedBy %v", out["verdict"], out["decidedBy"])
	}
	if !strings.Contains(out["note"].(string), "no waypoint") {
		t.Errorf("note %v", out["note"])
	}

	// No header: allowed by the ALLOW, and the 404 came from the backend.
	_, out = explainCall(t, srv, map[string]any{"ref": routeRef, "context": "kind-c1", "source": src, "request": map[string]any{"method": "GET", "path": "/"}})
	if out["verdict"] != "Allowed by bookinfo/reviews-methods; reviews-v1 answered 404" || out["decidedBy"] != "reviews-methods" {
		t.Errorf("%v %v", out["verdict"], out["decidedBy"])
	}
	if r := out["policies"].([]any)[0].(map[string]any); r["matched"] != false || !strings.HasPrefix(r["reason"].(string), "header color is absent") {
		t.Errorf("%v", r)
	}
	if out["route"].(map[string]any)["backend"] != "reviews-v1" {
		t.Errorf("%v", out["route"])
	}

	// Straight to the Pod: no route, and the waypoint policies are off the path.
	_, out = explainCall(t, srv, map[string]any{"ref": denyRef, "context": "kind-c1", "source": src, "request": map[string]any{"method": "GET", "path": "/reviews/0", "direct": true}})
	if out["route"] != nil || !strings.Contains(out["note"].(string), "straight to the Pod") {
		t.Errorf("%v %v", out["route"], out["note"])
	}

	for name, tc := range map[string]struct {
		body map[string]any
		msg  string
	}{
		"body on GET": {map[string]any{"ref": routeRef, "context": "x", "request": map[string]any{"method": "GET", "path": "/", "body": "x"}}, "cannot carry a body"},
		"no context":  {map[string]any{"ref": routeRef, "request": map[string]any{"path": "/"}}, "choose a cluster connection"},
		"external":    {map[string]any{"ref": routeRef, "context": "x", "source": map[string]any{"kind": "external"}, "request": map[string]any{"path": "/"}}, "choose one of those"},
		"bad ref":     {map[string]any{"ref": "nope", "context": "x"}, "not cluster/Kind"},
	} {
		code, out := explainCall(t, srv, tc.body)
		if code != 400 || !strings.Contains(out["error"].(string), tc.msg) {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}

func TestSuitesExplainWaypointLogNote(t *testing.T) {
	s := &Server{WS: suiteWorkspaceNS(t, map[string]any{"istio.io/dataplane-mode": "ambient", "istio.io/use-waypoint": "waypoint"}), Token: tok, WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	old := decideLogWait
	decideLogWait = 0
	t.Cleanup(func() { decideLogWait = old })
	runner := &fakeRunner{script: func(req suiteSendRequest) (ambient.Observation, error) {
		return ambient.Observation{Status: 200, Client: "bookinfo/client-1"}, nil
	}}
	withFakeRunner(t, runner, nil)
	_, out := explainCall(t, srv, map[string]any{"ref": routeRef, "context": "kind-c1", "request": map[string]any{"method": "GET", "path": "/reviews/0"}})
	if out["note"] != "Waypoint access logging is off, so Flightbay predicted this from the policies" || out["logAvailable"] != false {
		t.Errorf("%v", out["note"])
	}
}
