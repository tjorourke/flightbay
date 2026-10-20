package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/store"
)

// Test suites for AuthorizationPolicy and HTTPRoute (plan phase 7).
//
//	GET  /api/ambient/suites?ref=cluster/Kind/ns/name   saved suite, else generated (saved:false), with explain
//	POST /api/ambient/suites                            save a suite
//	GET  /api/ambient/suites/service?cluster&namespace&name   suites for a Service (Tests tab and count)
//	POST /api/ambient/suites/preview                    cases a plan would generate (task drawer)
//	POST /api/ambient/suites/run                        run cases, persist lastRun

// suiteDir is the state directory suites live under (ambient-suites/ inside
// it). It is the directory ambient test records use, so a preview harness
// that gives the server a temp AmbientStateDir isolates suites too.
func (s *Server) suiteDir() (string, error) {
	if s.AmbientStateDir != "" {
		return s.AmbientStateDir, nil
	}
	home := os.Getenv("FLIGHTBAY_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(h, ".flightbay")
	}
	return home, nil
}

// suiteResponse is a suite with what the object page needs beside it.
type suiteResponse struct {
	ambient.Suite
	Explain any `json:"explain,omitempty"`
	// Regenerated is true when the cases are a fresh generation merged with
	// the cases a person added.
	Regenerated bool `json:"regenerated,omitempty"`
}

func (s *Server) suiteGuard(w http.ResponseWriter) bool {
	if s.WS == nil {
		jerr(w, http.StatusServiceUnavailable, "no workspace is loaded")
		return false
	}
	return true
}

func explainFor(g *ambient.Generated) any {
	switch {
	case g == nil:
		return nil
	case g.Authz != nil:
		return ambient.ExplainAuthz(*g.Authz, g.Suite.Notes)
	case g.Route != nil:
		return ambient.ExplainRoute(*g.Route, g.Suite.Notes)
	}
	return nil
}

func (s *Server) ambientSuites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.suiteGet(w, r)
	case http.MethodPost:
		s.suiteSave(w, r)
	default:
		jerr(w, 405, "GET or POST only")
	}
}

func (s *Server) suiteGet(w http.ResponseWriter, r *http.Request) {
	if !s.suiteGuard(w) {
		return
	}
	ref, err := ambient.ParseSuiteRef(r.URL.Query().Get("ref"))
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	dir, err := s.suiteDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	saved, err := ambient.LoadSuite(dir, ref)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	regenerate := r.URL.Query().Get("regenerate") == "1"
	// The saved cases stand, but the explanation and the live checks are
	// always read from the capture as it is now.
	g, gerr := ambient.GenerateForRef(s.WS, ref, nil)
	out := suiteResponse{}
	switch {
	case saved != nil && !regenerate:
		out.Suite = *saved
		if g != nil {
			out.Suite.Notes, out.Suite.Checks = g.Suite.Notes, g.Suite.Checks
			out.Suite.Service = g.Suite.Service
		}
	case gerr != nil:
		code := 400
		if strings.Contains(gerr.Error(), "is not in the capture") {
			code = 404
		}
		jerr(w, code, gerr.Error())
		return
	case regenerate:
		out.Suite = *ambient.MergeGenerated(saved, g.Suite)
		out.Regenerated = true
	default:
		out.Suite = *g.Suite
	}
	out.Suite.Normalize()
	if g != nil {
		out.Explain = explainFor(g)
	}
	writeJSON(w, 200, out)
}

func suiteDecode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if r.Method != http.MethodPost {
		jerr(w, 405, "POST only")
		return false
	}
	if !sameOrigin(r) {
		jerr(w, 403, "cross-origin request refused")
		return false
	}
	// Unknown fields are allowed on purpose: the page posts back what a GET
	// or a plan returned, which carries more than the server reads.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		jerr(w, 400, err.Error())
		return false
	}
	return true
}

func (s *Server) suiteSave(w http.ResponseWriter, r *http.Request) {
	var in ambient.Suite
	if !suiteDecode(w, r, 1<<20, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	dir, err := s.suiteDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	// Results come from runs, never from the client.
	prev, err := ambient.LoadSuite(dir, in.Target)
	if err != nil {
		prev = nil
	}
	in.Last = nil
	if prev != nil {
		in.Last = keepResultsFor(prev.Last, in.Cases)
	}
	in.Target = mustRef(in.Target)
	if err := ambient.SaveSuite(dir, &in); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, in)
}

func mustRef(r ambient.Ref) ambient.Ref {
	full, err := ambient.SuiteRef(r.Cluster, r.Kind, r.Namespace, r.Name)
	if err != nil {
		return r
	}
	return full
}

// keepResultsFor drops results of cases that no longer exist.
func keepResultsFor(last *ambient.SuiteRun, cases []ambient.Case) *ambient.SuiteRun {
	if last == nil {
		return nil
	}
	have := map[string]bool{}
	for _, c := range cases {
		have[c.ID] = true
	}
	out := *last
	out.Results = nil
	for _, res := range last.Results {
		if have[res.ID] {
			out.Results = append(out.Results, res)
		}
	}
	if len(out.Results) == 0 {
		return nil
	}
	out.Summary = ambient.Summarize(out.Results, last.Summary.At)
	return &out
}

func (s *Server) ambientSuitesService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	if !s.suiteGuard(w) {
		return
	}
	q := r.URL.Query()
	if q.Get("name") == "" || q.Get("namespace") == "" {
		jerr(w, 400, "namespace and name are required")
		return
	}
	dir, err := s.suiteDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	cluster := q.Get("cluster")
	if cluster == "" {
		if cs := s.WS.Clusters(); len(cs) == 1 {
			cluster = cs[0]
		}
	}
	out, err := ambient.ServiceSuiteList(s.WS, dir, cluster, q.Get("namespace"), q.Get("name"))
	if err != nil {
		jerr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// ---- preview ---------------------------------------------------------------

type suitePreviewBody struct {
	Cluster   string           `json:"cluster"`
	Namespace string           `json:"namespace"`
	Service   string           `json:"service"`
	Objects   []map[string]any `json:"objects"`
	Request   struct {
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
		Service   string `json:"service"`
	} `json:"request"`
}

type previewCase struct {
	Object string      `json:"object"`
	Ref    ambient.Ref `json:"ref"`
	ambient.Case
}

type previewSuite struct {
	Ref    ambient.Ref         `json:"ref"`
	Source ambient.SuiteSource `json:"source"`
	Cases  []ambient.Case      `json:"cases"`
	Notes  []string            `json:"notes"`
}

type previewResponse struct {
	Suites []previewSuite `json:"suites"`
	// Cases is every case in one list, for the "Tests that will run after
	// apply" block.
	Cases []previewCase `json:"cases"`
	Notes []string      `json:"notes"`
}

func (s *Server) ambientSuitesPreview(w http.ResponseWriter, r *http.Request) {
	if !s.suiteGuard(w) {
		return
	}
	var in suitePreviewBody
	if !suiteDecode(w, r, 4<<20, &in) {
		return
	}
	cluster := firstNonEmptyStr(in.Cluster, in.Request.Cluster)
	ns := firstNonEmptyStr(in.Namespace, in.Request.Namespace)
	name := firstNonEmptyStr(in.Service, in.Request.Service)
	if cluster == "" {
		if cs := s.WS.Clusters(); len(cs) == 1 {
			cluster = cs[0]
		}
	}
	out := previewResponse{Suites: []previewSuite{}, Cases: []previewCase{}, Notes: []string{}}
	var svc *store.Object
	if name != "" {
		svc = s.WS.Get(cluster, "", "services", ns, name)
		if svc == nil {
			jerr(w, 400, fmt.Sprintf("Service %s/%s is not in the capture of %s", ns, name, cluster))
			return
		}
	}
	objects := make([]map[string]any, 0, len(in.Objects))
	for _, o := range in.Objects {
		cp := map[string]any{}
		for k, v := range o {
			cp[k] = v
		}
		meta := map[string]any{}
		for k, v := range ambient.M(o["metadata"]) {
			meta[k] = v
		}
		if ambient.S(meta["namespace"]) == "" && ns != "" {
			meta["namespace"] = ns
		}
		cp["metadata"] = meta
		objects = append(objects, cp)
	}
	for _, o := range objects {
		kind := ambient.S(o["kind"])
		if kind != ambient.KindAuthorizationPolicy && kind != ambient.KindHTTPRoute {
			continue
		}
		g, err := ambient.GenerateForObject(s.WS, cluster, o, svc, objects)
		label := kind + " " + ambient.S(ambient.M(o["metadata"])["name"])
		if err != nil {
			out.Notes = append(out.Notes, "Tests not generated for "+label+": "+err.Error())
			continue
		}
		out.Suites = append(out.Suites, previewSuite{Ref: g.Suite.Target, Source: g.Suite.Source, Cases: g.Suite.Cases, Notes: g.Suite.Notes})
		for _, c := range g.Suite.Cases {
			out.Cases = append(out.Cases, previewCase{Object: kind + " " + g.Suite.Target.Namespace + "/" + g.Suite.Target.Name, Ref: g.Suite.Target, Case: c})
		}
		out.Notes = append(out.Notes, g.Suite.Notes...)
	}
	writeJSON(w, 200, out)
}

func firstNonEmptyStr(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---- run -------------------------------------------------------------------

type suiteRunBody struct {
	Ref   string   `json:"ref"`
	Cases []string `json:"cases"`
	All   bool     `json:"all"`
	// Source overrides the suite's source for this run and is saved with it.
	Source *ambient.SuiteSource `json:"source"`
	// Context is the kubeconfig context to run in.
	Context string `json:"context"`
	// Suite runs and saves this suite instead of the saved or generated one
	// (the drawer runs a generated suite before it has ever been saved).
	Suite *ambient.Suite `json:"suite"`
	// Save is true unless explicitly false.
	Save *bool `json:"save"`
}

type suiteRunResponse struct {
	Suite   ambient.Suite        `json:"suite"`
	LastRun *ambient.SuiteRun    `json:"lastRun"`
	Results []ambient.CaseResult `json:"results"`
	Summary ambient.RunSummary   `json:"summary"`
	Demo    bool                 `json:"demo,omitempty"`
}

func (s *Server) ambientSuitesRun(w http.ResponseWriter, r *http.Request) {
	if !s.suiteGuard(w) {
		return
	}
	var in suiteRunBody
	if !suiteDecode(w, r, 1<<20, &in) {
		return
	}
	var ref ambient.Ref
	var err error
	switch {
	case in.Ref != "":
		ref, err = ambient.ParseSuiteRef(in.Ref)
	case in.Suite != nil:
		ref, err = ambient.SuiteRef(in.Suite.Target.Cluster, in.Suite.Target.Kind, in.Suite.Target.Namespace, in.Suite.Target.Name)
	default:
		err = errors.New("ref is required")
	}
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	dir, err := s.suiteDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	saved, err := ambient.LoadSuite(dir, ref)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	var suite *ambient.Suite
	var gen *ambient.Generated
	switch {
	case in.Suite != nil:
		cp := *in.Suite
		cp.Target = ref
		if err := cp.Validate(); err != nil {
			jerr(w, 400, err.Error())
			return
		}
		suite = &cp
		if saved != nil {
			suite.Last = saved.Last
		} else {
			suite.Last = nil
		}
	case saved != nil:
		suite = saved
	}
	// The generated input is also what Decided by and the demo predict from.
	g, gerr := ambient.GenerateForRef(s.WS, ref, nil)
	if gerr == nil {
		gen = g
	}
	if suite == nil {
		if gerr != nil {
			jerr(w, 404, gerr.Error())
			return
		}
		suite = g.Suite
	}
	if suite.Service.Name == "" && gen != nil {
		suite.Service = gen.Suite.Service
	}
	if suite.Service.Name == "" {
		jerr(w, 400, "the suite does not say which Service it tests")
		return
	}
	suite.Normalize()
	if in.Source != nil {
		suite.Source = *in.Source
	}
	ids, err := selectCases(suite, in.Cases, in.All)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	stream := wantsNDJSON(r)
	var run *ambient.SuiteRun
	var out *ndjson
	emit := func(any) {}
	startStream := func() {
		if stream && out == nil {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			out = &ndjson{w: w}
			emit = out.send
		}
	}
	demo := previewDemo()
	if demo {
		run = demoRun(suite, gen, ids)
		startStream()
		if stream {
			emit(runEventStart{Type: "start", Total: len(run.Results), Cases: resultIDs(run.Results)})
			for _, res := range run.Results {
				emit(runEventCase{Type: "case-start", ID: res.ID})
				emit(runEventCaseDone{Type: "case-done", ID: res.ID, Result: res})
			}
		}
	} else {
		if in.Context == "" {
			jerr(w, 400, "choose a cluster connection to run the tests in")
			return
		}
		if msg := sourcePreflight(suite.Source); msg != "" {
			jerr(w, 400, msg)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		spec := suiteRunSpec{Context: in.Context, Cluster: ref.Cluster, Service: suite.Service, Source: suite.Source, Suite: suite}
		if gen != nil {
			spec.Authz = gen.Authz
		}
		runner, decider, err := newSuiteRunner(s, ctx, spec)
		if err != nil {
			code := 502
			var he suiteHTTPError
			if errors.As(err, &he) {
				code = he.Code
			}
			jerr(w, code, err.Error())
			return
		}
		defer runner.Close()
		startStream()
		opt := runOptions{Route: s.routeRulesFor(ref, gen, suite.Service), Emit: nil}
		if stream {
			opt.Emit = emit
		}
		run = runSuiteWith(ctx, suite, ids, runner, decider, time.Now, opt)
		run.Cluster = ref.Cluster
	}
	run.Source = suite.Source
	if r.Context().Err() != nil {
		// The client went away (Stop): keep what finished, never a full run.
		finished := []ambient.CaseResult{}
		for _, res := range run.Results {
			if !stoppedResult(res) {
				finished = append(finished, res)
			}
		}
		if len(finished) == 0 {
			emit(map[string]any{"type": "error", "error": "The run was stopped before any case finished, so nothing was saved."})
			return
		}
		run.Results = finished
		run.Summary = ambient.Summarize(finished, run.At)
		run.Summary.Cancelled = true
	}
	suite.Last = mergeRun(suite.Last, run, suite.Cases)
	suite.Last.Summary.Cancelled = run.Summary.Cancelled
	suite.Normalize()
	if in.Save == nil || *in.Save {
		if err := ambient.SaveSuite(dir, suite); err != nil {
			msg := "the run finished but its results could not be saved: " + err.Error()
			if stream {
				emit(map[string]any{"type": "error", "error": msg})
				return
			}
			jerr(w, 500, msg)
			return
		}
	}
	body := suiteRunResponse{Suite: *suite, LastRun: suite.Last, Results: run.Results, Summary: run.Summary, Demo: demo}
	if stream {
		emit(struct {
			Type string `json:"type"`
			suiteRunResponse
		}{"done", body})
		return
	}
	writeJSON(w, 200, body)
}

// wantsNDJSON is true when the client asks for a streamed run.
func wantsNDJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/x-ndjson")
}

func resultIDs(rs []ambient.CaseResult) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// sourcePreflight rejects sources a run cannot send from.
func sourcePreflight(src ambient.SuiteSource) string {
	switch src.Kind {
	case ambient.SourceWorkload, ambient.SourceEphemeral:
		return ""
	case ambient.SourceExternal:
		return "tests run from a Pod in the mesh or a temporary Pod; choose one of those as the source"
	}
	return "choose a source to send the requests from"
}

// routeRulesFor is the HTTPRoute in front of the suite's Service: the
// suite's own route, else any route whose parent is the Service.
func (s *Server) routeRulesFor(ref ambient.Ref, gen *ambient.Generated, svc ambient.Ref) []ambient.RouteRuleSpec {
	if gen != nil && gen.Route != nil {
		return gen.Route.Rules
	}
	if s.WS == nil {
		return nil
	}
	o := s.WS.Get(ref.Cluster, "", "services", svc.Namespace, svc.Name)
	if o == nil {
		return nil
	}
	_, rules, _ := ambient.RouteForService(s.WS, o, 0)
	return rules
}

// selectCases resolves the case ids to run, in suite order.
func selectCases(s *ambient.Suite, ids []string, all bool) ([]string, error) {
	if all || len(ids) == 0 {
		out := make([]string, 0, len(s.Cases))
		for _, c := range s.Cases {
			out = append(out, c.ID)
		}
		if len(out) == 0 {
			return nil, errors.New("the suite has no cases to run")
		}
		return out, nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []string
	for _, c := range s.Cases {
		if want[c.ID] {
			out = append(out, c.ID)
			delete(want, c.ID)
		}
	}
	if len(want) > 0 {
		var missing []string
		for id := range want {
			missing = append(missing, id)
		}
		return nil, fmt.Errorf("no such case: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// mergeRun folds a run into the previous last run: results for the cases that
// ran replace older ones, the rest stay, so running one case does not wipe
// the others' results.
func mergeRun(prev, run *ambient.SuiteRun, cases []ambient.Case) *ambient.SuiteRun {
	out := *run
	byID := map[string]ambient.CaseResult{}
	if prev != nil {
		for _, r := range prev.Results {
			byID[r.ID] = r
		}
	}
	for _, r := range run.Results {
		byID[r.ID] = r
	}
	out.Results = nil
	for _, c := range cases {
		if r, ok := byID[c.ID]; ok {
			out.Results = append(out.Results, r)
		}
	}
	out.Summary = ambient.Summarize(out.Results, run.At)
	return &out
}
