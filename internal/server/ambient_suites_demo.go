package server

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
)

// Demo mode is for the browser preview harness only. With
// FLIGHTBAY_AMBIENT_PREVIEW_DEMO=1 in the environment, POST
// /api/ambient/suites/run returns canned, deterministic results instead of
// touching a cluster, so the Tests screens can be screenshotted offline.
// Without the variable nothing here runs.

const previewDemoEnv = "FLIGHTBAY_AMBIENT_PREVIEW_DEMO"

func previewDemo() bool { return os.Getenv(previewDemoEnv) == "1" }

// demoTime is fixed so two runs produce identical results (mockup 04 reads
// "Last run 11:52:09").
var demoTime = time.Date(2026, 10, 20, 11, 52, 9, 0, time.UTC)

// demoRun builds the canned run for the cases in ids and edits the suite to
// match where the mockups need it:
//
//   - the AuthorizationPolicy case that differs from the rule only by letter
//     case is expected to be blocked (the expectation a person would write)
//     but gets through, so one case fails as in mockup 04. The case becomes a
//     person's case (generated:false) in the returned suite.
//   - a weights case observes its expected split moved one point from the
//     first backend to the second: 79/21 against 80/20 as in mockup 05.
func demoRun(suite *ambient.Suite, gen *ambient.Generated, ids []string) *ambient.SuiteRun {
	run := &ambient.SuiteRun{At: demoTime, Source: suite.Source, Cluster: suite.Target.Cluster, Demo: true, Results: []ambient.CaseResult{}}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	from := suite.Source.Ref.Namespace + "/" + suite.Source.Ref.Name
	if suite.Source.Ref.Name == "" {
		from = "a temporary Pod in " + suite.Source.Ref.Namespace
	}
	rule := 0
	for i := range suite.Cases {
		c := &suite.Cases[i]
		if !want[c.ID] {
			continue
		}
		obs := ambient.Observation{Connection: "In-mesh curl from " + from + " via the Service (demo results, no cluster was contacted)"}
		var failMsg string
		switch {
		case len(c.Expect.Weights) > 0:
			obs.Samples = demoSamples(c.Expect)
		case c.Expect.NotRoute:
			obs.Status, obs.Backend = 200, c.Expect.Backend
		case c.Expect.Rule > 0:
			rule++
			obs.Status = 200
			if len(c.Expect.Backends) > 0 {
				obs.Backend = c.Expect.Backends[(rule-1)%len(c.Expect.Backends)]
			}
		default:
			obs.Status = c.Expect.Status
			if suite.Target.Kind == ambient.KindAuthorizationPolicy && strings.Contains(c.ID, "-case-") {
				// A person expects "Red" to be treated like "red". Istio matches
				// header values exactly, so the request is not blocked.
				c.Generated = false
				c.Expect = ambient.CaseExpect{Status: 403}
				obs.Status = 200
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = suite.Target.Name+" did not match", "policy", "demo"
				failMsg = "Header values are matched exactly. If you meant any case, change the rule to a regex or add both values."
			}
			if obs.DecidedBy == "" {
				demoDecidedBy(&obs, suite, gen, *c)
			}
		}
		res := ambient.Evaluate(*c, obs, demoTime)
		if failMsg != "" && res.Status == ambient.StatusFail {
			res.Message = fmt.Sprintf("%q got through. %s", demoHeaders(c.Request.Headers), failMsg)
		}
		run.Results = append(run.Results, res)
	}
	run.Summary = ambient.Summarize(run.Results, demoTime)
	suite.Normalize()
	return run
}

func demoHeaders(h map[string]string) string {
	for k, v := range h {
		return k + ": " + v
	}
	return "request"
}

func demoDecidedBy(obs *ambient.Observation, suite *ambient.Suite, gen *ambient.Generated, c ambient.Case) {
	if c.Request.Direct {
		if obs.Status >= 200 && obs.Status < 400 {
			obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "ztunnel (no L7)", "ztunnel", "demo"
		} else {
			obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "ztunnel", "ztunnel", "demo"
		}
		return
	}
	if gen != nil && gen.Authz != nil {
		if by, status, ok := ambient.PredictDecision(*gen.Authz, c); ok && status == obs.Status {
			if by != nil {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow, obs.DecidedByRef = by.Name, "policy", "demo", by
				return
			}
			obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "no ALLOW matched", "none", "demo"
			if status == 200 {
				obs.DecidedBy = "no policy refused it"
			}
			return
		}
	}
	if obs.Status == 403 {
		obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = suite.Target.Name, "policy", "demo"
	}
}

// demoSamples are 200 replies spread as expected, except that one point moves
// from the first backend (in name order) to the second: 80/20 reads 79/21.
func demoSamples(e ambient.CaseExpect) []ambient.Sample {
	n := e.Samples
	if n == 0 {
		n = ambient.DefaultSamples
	}
	names := []string{}
	for k, v := range e.Weights {
		if v > 0 {
			names = append(names, k)
		}
	}
	sortStrings(names)
	counts := map[string]int{}
	total := 0
	for _, k := range names {
		counts[k] = e.Weights[k] * n / 100
		total += counts[k]
	}
	if len(names) > 0 {
		counts[names[0]] += n - total
	}
	if len(names) > 1 && counts[names[0]] > 0 {
		shift := n / 100
		counts[names[0]] -= shift
		counts[names[1]] += shift
	}
	var out []ambient.Sample
	i := 0
	for _, k := range names {
		for j := 0; j < counts[k]; j++ {
			ms := int64(9)
			if i%18 == 0 {
				ms = 31
			}
			out = append(out, ambient.Sample{Status: 200, Backend: k, DurationMs: ms})
			i++
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ambientPreviewDemo reports whether the browser preview harness enabled canned
// demo data, so the UI can render states (drawer phase 3, test runs) offline.
func (s *Server) ambientPreviewDemo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"demo": previewDemo()})
}
