package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/store"
)

// POST /api/ambient/suites/explain sends one real request through the same
// runner as a suite run and explains it (R7.2): the route rule that takes it,
// worked out from the route's matches; the backend that answered; every
// AuthorizationPolicy on the Service and its waypoint in Istio's order with
// the condition that decided each; the verdict; and whether the waypoint's
// access log confirmed it.

type suiteExplainBody struct {
	Ref     string               `json:"ref"`
	Request ambient.CaseRequest  `json:"request"`
	From    *ambient.CaseFrom    `json:"from"`
	Source  *ambient.SuiteSource `json:"source"`
	Context string               `json:"context"`
}

type explainGot struct {
	Status  int    `json:"status"`
	Backend string `json:"backend"`
	Ms      int64  `json:"ms"`
	Error   string `json:"error,omitempty"`
}

type suiteExplainResponse struct {
	Request    ambient.CaseRequest `json:"request"`
	From       *ambient.CaseFrom   `json:"from,omitempty"`
	Source     string              `json:"source"`
	Cluster    string              `json:"cluster"`
	Context    string              `json:"context"`
	Connection string              `json:"connection"`
	Got        explainGot          `json:"got"`
	// Route is null when no HTTPRoute has the Service as its parent, or the
	// request did not go through the Service.
	Route          *ambient.RouteExplanation `json:"route"`
	Policies       []ambient.PolicyEval      `json:"policies"`
	Verdict        string                    `json:"verdict"`
	DecidedBy      string                    `json:"decidedBy"`
	DecidedByHow   string                    `json:"decidedByHow,omitempty"`
	DecidedByRef   *ambient.Ref              `json:"decidedByRef,omitempty"`
	ConfirmedByLog bool                      `json:"confirmedByLog"`
	LogAvailable   bool                      `json:"logAvailable"`
	LogRef         *ambient.LogRef           `json:"logRef,omitempty"`
	Note           string                    `json:"note,omitempty"`
	Demo           bool                      `json:"demo,omitempty"`
}

func (s *Server) ambientSuitesExplain(w http.ResponseWriter, r *http.Request) {
	if !s.suiteGuard(w) {
		return
	}
	var in suiteExplainBody
	if !suiteDecode(w, r, 1<<20, &in) {
		return
	}
	ref, err := ambient.ParseSuiteRef(in.Ref)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	c := ambient.Case{ID: "explain", Label: "Explain", Request: in.Request, From: in.From, Expect: ambient.CaseExpect{Status: 200}}
	one := ambient.Suite{Target: ref, Cases: []ambient.Case{c}}
	one.Normalize()
	c = one.Cases[0]
	if err := c.Validate(); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	dir, err := s.suiteDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	saved, _ := ambient.LoadSuite(dir, ref)
	g, gerr := ambient.GenerateForRef(s.WS, ref, nil)
	suite := &ambient.Suite{Target: ref}
	switch {
	case saved != nil:
		suite.Source, suite.Service = saved.Source, saved.Service
		if suite.Service.Name == "" && g != nil {
			suite.Service = g.Suite.Service
		}
	case gerr != nil:
		jerr(w, 404, gerr.Error())
		return
	default:
		suite.Source, suite.Service = g.Suite.Source, g.Suite.Service
	}
	if suite.Service.Name == "" {
		jerr(w, 400, "the object does not say which Service it applies to")
		return
	}
	if in.Source != nil {
		suite.Source = *in.Source
	}
	suite.Cases = []ambient.Case{c}
	svc := s.WS.Get(ref.Cluster, "", "services", suite.Service.Namespace, suite.Service.Name)
	if svc == nil {
		jerr(w, 404, fmt.Sprintf("Service %s/%s is not in the capture of %s", suite.Service.Namespace, suite.Service.Name, ref.Cluster))
		return
	}

	out := suiteExplainResponse{Request: c.Request, From: c.From, Cluster: ref.Cluster, Context: in.Context, Policies: []ambient.PolicyEval{}}
	var obs ambient.Observation
	sentAt := time.Now()
	demo := previewDemo()
	if demo {
		out.Demo = true
		obs.Client = suite.Source.Ref.Namespace + "/" + suite.Source.Ref.Name
		obs.Connection = "In-mesh curl from " + obs.Client + " via the Service (demo results, no cluster was contacted)"
	} else {
		if in.Context == "" {
			jerr(w, 400, "choose a cluster connection to send the request in")
			return
		}
		if msg := sourcePreflight(suite.Source); msg != "" {
			jerr(w, 400, msg)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		spec := suiteRunSpec{Context: in.Context, Cluster: ref.Cluster, Service: suite.Service, Source: suite.Source, Suite: suite}
		if g != nil {
			spec.Authz = g.Authz
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
		sentAt = time.Now()
		obs, err = runner.Send(ctx, suiteSendRequest{Case: c, Source: suite.Source})
		var skip skipCase
		switch {
		case errors.As(err, &skip):
			jerr(w, 400, string(skip))
			return
		case err != nil:
			obs.Error = err.Error()
		}
		if decider != nil && obs.Error == "" {
			safeDecide(ctx, decider, suite, c, &obs, sentAt)
		}
	}
	explainRequest(s.WS, ref, g, svc, c, &obs, &out)
	wpNS, wpApp, _ := ambient.Waypoint(s.WS, svc)
	out.LogAvailable = s.waypointLogAvailable(ref.Cluster, wpNS, wpApp, sentAt)
	out.ConfirmedByLog = obs.Log != nil
	out.LogRef = obs.Log
	switch {
	case c.Request.Direct:
		out.Note = "Sent straight to the Pod, so the waypoint and its policies are not on the path; only ztunnel policies apply."
	case wpApp == "":
		out.Note = "The Service has no waypoint, so only ztunnel policies apply and there is no waypoint log."
	case out.ConfirmedByLog:
	case !out.LogAvailable:
		out.Note = "Waypoint access logging is off, so Flightbay predicted this from the policies"
	default:
		out.Note = "No waypoint log line matched this request, so Flightbay predicted this from the policies"
	}
	if out.DecidedBy == "" && obs.DecisionNote != "" {
		out.Note = obs.DecisionNote
	}
	writeJSON(w, 200, out)
}

// explainRequest fills the parts of the explanation that come from the
// configuration: the route match, the policy evaluation and the verdict.
func explainRequest(ws *store.Workspace, ref ambient.Ref, g *ambient.Generated, svc *store.Object, c ambient.Case, obs *ambient.Observation, out *suiteExplainResponse) {
	port := c.Request.Port
	if port == 0 {
		if ps := ambient.L(ambient.Path(svc.Obj, "spec", "ports")); len(ps) > 0 {
			port = ambient.N(ambient.M(ps[0])["port"])
		}
	}
	// Route: the page's own route, else any route in front of the Service.
	if !c.Request.Direct && (c.Request.Host == "" || c.Request.Host == svc.Name) {
		var rref ambient.Ref
		var rules []ambient.RouteRuleSpec
		found := false
		if g != nil && g.Route != nil {
			rref, rules, found = g.Route.Route, g.Route.Rules, true
		} else {
			rref, rules, found = ambient.RouteForService(ws, svc, port)
		}
		if found {
			x := ambient.ExplainRouteRequest(rref, rules, c.Request)
			out.Route = &x
		}
	}
	_, wpApp, _ := ambient.Waypoint(ws, svc)
	caller := ambient.CallerFor(ws, ref.Cluster, obs.Client, c.From)
	pred := ambient.PredictRequest(ambient.ServiceAuthzPolicies(ws, svc), c.Request, port, caller, wpApp != "", ambient.WaypointPrincipal(ws, svc))
	out.Policies = pred.Policies
	if out.Demo {
		obs.Status = pred.Status
		if !pred.Known {
			obs.Status = 200
		}
		if out.Route != nil && out.Route.MatchedRule > 0 && obs.Status == 200 {
			if bs := out.Route.Rules[out.Route.MatchedRule-1].Backends; len(bs) > 0 {
				obs.Backend = bs[0]
			}
		}
	}
	out.Source = obs.Client
	out.Connection = obs.Connection
	out.Got = explainGot{Status: obs.Status, Backend: obs.Backend, Ms: obs.DurationMs, Error: obs.Error}
	if out.Route != nil {
		out.Route.Backend = obs.Backend
	}
	out.DecidedBy, out.DecidedByHow, out.DecidedByRef = obs.DecidedBy, obs.DecidedByHow, obs.DecidedByRef
	status := obs.Status
	switch {
	case obs.Status == 0 && obs.Error != "":
		out.Verdict = "The request did not complete: " + obs.Error
	case obs.DecidedByHow == "waypoint-log" && obs.DecidedByRef != nil:
		out.Verdict = "Denied by " + obs.DecidedByRef.Namespace + "/" + obs.DecidedByRef.Name
		for i := range out.Policies {
			p := &out.Policies[i]
			p.Decided = p.Namespace == obs.DecidedByRef.Namespace && p.Name == obs.DecidedByRef.Name
		}
	case obs.DecidedByHow == "waypoint-log":
		out.Verdict = "Denied: ALLOW policies apply at the waypoint and none matched"
	case predictionAgrees(pred, status):
		out.Verdict = pred.Verdict
		if status != pred.Status {
			who := "the reply"
			if obs.Backend != "" {
				who = obs.Backend
			}
			out.Verdict += fmt.Sprintf("; %s answered %d", who, status)
		}
		if out.DecidedBy == "" {
			out.DecidedBy, out.DecidedByRef, out.DecidedByHow = pred.DecidedBy, pred.By, "predicted"
		}
	case pred.Known:
		out.Verdict = fmt.Sprintf("Got %d, but the policies predict %d (%s)", status, pred.Status, pred.Verdict)
		for i := range out.Policies {
			out.Policies[i].Decided = false
		}
	case pred.Verdict != "":
		out.Verdict = pred.Verdict
	default:
		out.Verdict = "Flightbay cannot tell what decided this request"
	}
}
