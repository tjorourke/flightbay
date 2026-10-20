package ambient

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Case generator for HTTPRoute (plan 7.3) and the "other checks" shown on the
// route's Tests tab.

type RouteHeaderMatch struct {
	Name  string
	Type  string // Exact (default) or RegularExpression
	Value string
}

type RouteMatchSpec struct {
	PathType string // Exact, PathPrefix (default) or RegularExpression
	Path     string
	Method   string
	Headers  []RouteHeaderMatch
	Query    []RouteHeaderMatch
}

type RouteBackend struct {
	Namespace string
	Name      string
	Port      int
	// Weight is the effective weight: 1 when the route sets none.
	Weight int
}

type RouteRuleSpec struct {
	Matches  []RouteMatchSpec
	Backends []RouteBackend
	// Timeout, Retry and Filters record what the rule configures beyond
	// backends, so the generator can say which it did not test.
	Timeout string
	Retry   bool
	Filters []string
}

// BackendState is what the capture says about a backendRef's Service.
type BackendState struct {
	Exists bool
	Ready  int
}

// OtherRoute is another HTTPRoute with the same parent Service.
type OtherRoute struct {
	Ref   Ref
	Rules []RouteRuleSpec
}

// ClusterPresence says whether another cluster has the Service and the route.
type ClusterPresence struct {
	Cluster    string
	HasService bool
	HasRoute   bool
}

type RouteInput struct {
	Route       Ref
	Service     Ref
	Port        int
	DefaultPath string
	Rules       []RouteRuleSpec
	// Backends is keyed "namespace/name".
	Backends map[string]BackendState
	Others   []OtherRoute
	Clusters []ClusterPresence
	// Hostnames on the route, for the notes.
	Hostnames []string
	// Source is the caller the suite assumes; nil means a temporary Pod.
	Source *Workload
}

// ---- matching --------------------------------------------------------------

type routeReq struct {
	Method  string
	Path    string
	Headers map[string]string
	Query   map[string]string
}

func pathMatches(m RouteMatchSpec, path string) tri {
	t := defaultString(m.PathType, "PathPrefix")
	v := m.Path
	if v == "" {
		v = "/"
	}
	switch t {
	case "Exact":
		return b2t(path == v)
	case "PathPrefix":
		if v == "/" {
			return triYes
		}
		v = strings.TrimSuffix(v, "/")
		return b2t(path == v || strings.HasPrefix(path, v+"/"))
	}
	return triUnknown
}

func matchRoute(m RouteMatchSpec, r routeReq) tri {
	parts := []tri{pathMatches(m, r.Path)}
	if m.Method != "" {
		parts = append(parts, b2t(m.Method == r.Method))
	}
	for _, h := range m.Headers {
		if defaultString(h.Type, "Exact") != "Exact" {
			parts = append(parts, triUnknown)
			continue
		}
		v, ok := r.Headers[strings.ToLower(h.Name)]
		parts = append(parts, b2t(ok && v == h.Value))
	}
	for _, q := range m.Query {
		if defaultString(q.Type, "Exact") != "Exact" {
			parts = append(parts, triUnknown)
			continue
		}
		v, ok := r.Query[q.Name]
		parts = append(parts, b2t(ok && v == q.Value))
	}
	return and(parts...)
}

// precedence orders matches the way the Gateway API does: exact path, then
// longest prefix, then method, then most headers, then most query params.
func precedence(m RouteMatchSpec) [5]int {
	var p [5]int
	switch defaultString(m.PathType, "PathPrefix") {
	case "Exact":
		p[0] = 2
	default:
		p[0] = 1
		p[1] = len(strings.TrimSuffix(defaultString(m.Path, "/"), "/"))
	}
	if m.Method != "" {
		p[2] = 1
	}
	p[3], p[4] = len(m.Headers), len(m.Query)
	return p
}

func lessPrec(a, b [5]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// routeWinner returns the rule (0-based) that handles a request, or -1 when
// none match. known is false when a regular expression made it undecidable.
func routeWinner(rules []RouteRuleSpec, r routeReq) (int, bool) {
	best, bestPrec := -1, [5]int{}
	known := true
	for i, rule := range rules {
		ms := rule.Matches
		if len(ms) == 0 {
			ms = []RouteMatchSpec{{}}
		}
		for _, m := range ms {
			switch matchRoute(m, r) {
			case triUnknown:
				known = false
			case triYes:
				if p := precedence(m); best < 0 || lessPrec(bestPrec, p) {
					best, bestPrec = i, p
				}
			}
		}
	}
	return best, known
}

// ---- generator -------------------------------------------------------------

func (r RouteRuleSpec) backendNames() []string {
	out := []string{}
	for _, b := range r.Backends {
		if b.Weight > 0 {
			out = append(out, b.Name)
		}
	}
	return out
}

// percentWeights turns weights into whole percentages that add up to 100.
func percentWeights(bs []RouteBackend) map[string]int {
	total := 0
	for _, b := range bs {
		total += b.Weight
	}
	out := map[string]int{}
	if total == 0 {
		return out
	}
	sum, biggest := 0, ""
	for _, b := range bs {
		out[b.Name] += int(math.Round(100 * float64(b.Weight) / float64(total)))
		if biggest == "" || b.Weight > weightOf(bs, biggest) {
			biggest = b.Name
		}
	}
	for _, v := range out {
		sum += v
	}
	out[biggest] += 100 - sum
	return out
}

func weightOf(bs []RouteBackend, name string) int {
	for _, b := range bs {
		if b.Name == name {
			return b.Weight
		}
	}
	return 0
}

func (m RouteMatchSpec) describe() string {
	t := defaultString(m.PathType, "PathPrefix")
	s := t + " " + defaultString(m.Path, "/")
	if m.Method != "" {
		s += " " + m.Method
	}
	for _, h := range m.Headers {
		s += " header " + h.Name
	}
	return s
}

// GenerateRouteSuite builds the suite and the other checks for one route.
func GenerateRouteSuite(in RouteInput) *Suite {
	s := &Suite{Target: in.Route, Service: in.Service, Cases: []Case{}, Notes: []string{}, Source: defaultSource(in.Source, in.Service)}
	notes := map[string]bool{}
	note := func(msg string) {
		if !notes[msg] {
			notes[msg] = true
			s.Notes = append(s.Notes, msg)
		}
	}
	ids, sigs := map[string]bool{}, map[string]bool{}
	add := func(c Case) {
		c.Generated = true
		sig := caseSig(c)
		if ids[c.ID] || sigs[sig] {
			return
		}
		ids[c.ID], sigs[sig] = true, true
		s.Cases = append(s.Cases, c)
	}
	path := in.DefaultPath
	if path == "" {
		path = "/"
	}
	var firstReq *CaseRequest
	var firstBackend *RouteBackend

	for ri, rule := range in.Rules {
		n := ri + 1
		if rule.Timeout != "" {
			note(fmt.Sprintf("Not generated: rules[%d].timeouts (no backend here can delay a reply on request)", n))
		}
		if rule.Retry {
			note(fmt.Sprintf("Not generated: rules[%d].retry (a backend that fails on request is needed)", n))
		}
		for _, f := range rule.Filters {
			note(fmt.Sprintf("Not generated: rules[%d].filters.%s (the effect is not visible in the reply)", n, f))
		}
		names := rule.backendNames()
		if len(names) == 0 {
			note(fmt.Sprintf("Not generated: rule %d has no backend with weight above 0, so there is nothing to expect", n))
			continue
		}
		matches := rule.Matches
		if len(matches) == 0 {
			matches = []RouteMatchSpec{{}}
		}
		var ruleReq *CaseRequest
		for mi, m := range matches {
			if t := defaultString(m.PathType, "PathPrefix"); t == "RegularExpression" {
				note(fmt.Sprintf("Not generated: rules[%d].matches[%d].path RegularExpression", n, mi+1))
				continue
			}
			unsupported := false
			for _, h := range m.Headers {
				if defaultString(h.Type, "Exact") != "Exact" {
					note(fmt.Sprintf("Not generated: rules[%d].matches[%d].headers RegularExpression", n, mi+1))
					unsupported = true
				}
			}
			for _, q := range m.Query {
				if defaultString(q.Type, "Exact") != "Exact" {
					note(fmt.Sprintf("Not generated: rules[%d].matches[%d].queryParams RegularExpression", n, mi+1))
					unsupported = true
				}
			}
			if unsupported {
				continue
			}
			p := defaultString(m.Path, "/")
			if p == "/" {
				p = path
			}
			req := CaseRequest{Method: defaultString(m.Method, "GET"), Path: p, Port: in.Port, Headers: map[string]string{}}
			rr := routeReq{Method: req.Method, Path: p, Headers: map[string]string{}, Query: map[string]string{}}
			for _, h := range m.Headers {
				req.Headers[h.Name] = h.Value
				rr.Headers[strings.ToLower(h.Name)] = h.Value
			}
			if len(m.Query) > 0 {
				var q []string
				for _, qq := range m.Query {
					q = append(q, qq.Name+"="+qq.Value)
					rr.Query[qq.Name] = qq.Value
				}
				req.Path += "?" + strings.Join(q, "&")
			}
			w, known := routeWinner(in.Rules, rr)
			if !known || w < 0 {
				note(fmt.Sprintf("Not generated: rules[%d].matches[%d], because another route rule uses a regular expression and the winner cannot be predicted", n, mi+1))
				continue
			}
			why := fmt.Sprintf("Rule %d matches", n)
			if w != ri {
				why = fmt.Sprintf("Rule %d is caught by rule %d", n, w+1)
				note(fmt.Sprintf("Rule %d, match %d (%s) is never reached: rule %d is more specific or earlier", n, mi+1, m.describe(), w+1))
			}
			winnerNames := in.Rules[w].backendNames()
			if len(winnerNames) == 0 {
				continue
			}
			exp := CaseExpect{Rule: w + 1, Backends: winnerNames}
			if len(winnerNames) == 1 {
				exp.Backend = winnerNames[0]
			}
			add(Case{ID: fmt.Sprintf("r%d-m%d", n, mi+1), Label: fmt.Sprintf("Rule %d, match %d: %s", n, mi+1, m.describe()), Why: why, Rule: n, Request: req, Expect: exp})
			if ruleReq == nil {
				cp := req
				ruleReq = &cp
			}
			if defaultString(m.PathType, "PathPrefix") == "PathPrefix" && len(m.Headers) == 0 && len(m.Query) == 0 {
				other := strings.TrimSuffix(defaultString(m.Path, "/"), "/") + "/health"
				req2 := req
				req2.Path = other
				rr2 := rr
				rr2.Path = other
				if w2, k2 := routeWinner(in.Rules, rr2); k2 && w2 >= 0 && len(in.Rules[w2].backendNames()) > 0 && other != req.Path {
					e2 := CaseExpect{Rule: w2 + 1, Backends: in.Rules[w2].backendNames()}
					if len(e2.Backends) == 1 {
						e2.Backend = e2.Backends[0]
					}
					add(Case{ID: fmt.Sprintf("r%d-m%d-sub", n, mi+1), Label: fmt.Sprintf("Rule %d, match %d: another path under the prefix", n, mi+1), Why: "Another path under the prefix", Rule: n, Request: req2, Expect: e2})
				}
			}
		}
		if ruleReq != nil && firstReq == nil {
			firstReq = ruleReq
		}
		if firstBackend == nil {
			for i := range rule.Backends {
				if rule.Backends[i].Weight > 0 {
					b := rule.Backends[i]
					firstBackend = &b
					break
				}
			}
		}
		if len(names) > 1 && ruleReq != nil {
			req := *ruleReq
			add(Case{ID: fmt.Sprintf("r%d-weights", n), Label: fmt.Sprintf("Rule %d: traffic split", n), Why: "Traffic split", Rule: n, Request: req,
				Expect: CaseExpect{Weights: percentWeights(rule.Backends), Tolerance: DefaultTolerance, Samples: DefaultSamples}})
		}
	}
	if firstReq != nil && firstBackend != nil && firstBackend.Name != in.Service.Name {
		req := CaseRequest{Method: firstReq.Method, Path: firstReq.Path, Headers: map[string]string{}, Port: firstBackend.Port, Host: firstBackend.Name}
		for k, v := range firstReq.Headers {
			req.Headers[k] = v
		}
		if req.Port == 0 {
			req.Port = in.Port
		}
		add(Case{ID: "not-route", Label: "Straight to " + firstBackend.Name, Why: "Not this route", Request: req, Expect: CaseExpect{NotRoute: true, Backend: firstBackend.Name}})
	}
	s.Checks = RouteChecks(in)
	s.Normalize()
	return s
}

// ---- other checks ----------------------------------------------------------

func matchesOverlap(a, b RouteMatchSpec) bool {
	pa, pb := defaultString(a.Path, "/"), defaultString(b.Path, "/")
	ta, tb := defaultString(a.PathType, "PathPrefix"), defaultString(b.PathType, "PathPrefix")
	if ta == "RegularExpression" || tb == "RegularExpression" {
		return true
	}
	var pathOverlap bool
	switch {
	case ta == "Exact" && tb == "Exact":
		pathOverlap = pa == pb
	case ta == "Exact":
		pathOverlap = pathMatches(b, pa) == triYes
	case tb == "Exact":
		pathOverlap = pathMatches(a, pb) == triYes
	default:
		pathOverlap = pathMatches(a, pb) == triYes || pathMatches(b, pa) == triYes
	}
	if !pathOverlap {
		return false
	}
	if a.Method != "" && b.Method != "" && a.Method != b.Method {
		return false
	}
	for _, ha := range a.Headers {
		for _, hb := range b.Headers {
			if strings.EqualFold(ha.Name, hb.Name) && defaultString(ha.Type, "Exact") == "Exact" && defaultString(hb.Type, "Exact") == "Exact" && ha.Value != hb.Value {
				return false
			}
		}
	}
	return true
}

func rulesOverlap(a, b []RouteRuleSpec) (RouteMatchSpec, bool) {
	flat := func(rs []RouteRuleSpec) []RouteMatchSpec {
		var out []RouteMatchSpec
		for _, r := range rs {
			if len(r.Matches) == 0 {
				out = append(out, RouteMatchSpec{})
			}
			out = append(out, r.Matches...)
		}
		return out
	}
	for _, x := range flat(a) {
		for _, y := range flat(b) {
			if matchesOverlap(x, y) {
				return x, true
			}
		}
	}
	return RouteMatchSpec{}, false
}

// RouteChecks are the "Other checks on this route" rows.
func RouteChecks(in RouteInput) []SuiteCheck {
	out := []SuiteCheck{}
	// 1. backendRefs resolve to Services with ready endpoints.
	seen := map[string]bool{}
	total, good := 0, 0
	var bad []string
	for _, r := range in.Rules {
		for _, b := range r.Backends {
			key := b.Namespace + "/" + b.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			total++
			st := in.Backends[key]
			switch {
			case !st.Exists:
				bad = append(bad, b.Name+" does not exist")
			case st.Ready == 0:
				bad = append(bad, b.Name+" has no ready endpoints")
			default:
				good++
			}
		}
	}
	c := SuiteCheck{ID: "backends", Title: "Every backendRef resolves to a Service with ready endpoints", Status: "pass", Count: fmt.Sprintf("%d / %d", good, total)}
	if total == 0 {
		c.Status, c.Detail = "info", "The route has no backendRefs."
		c.Count = ""
	} else if good < total {
		c.Status, c.Detail = "warn", strings.Join(bad, "; ")+". Requests sent to it can return 503."
	}
	out = append(out, c)

	// 2. No overlapping route.
	port := ""
	if in.Port > 0 {
		port = fmt.Sprintf(" :%d", in.Port)
	}
	ov := SuiteCheck{ID: "overlap", Title: fmt.Sprintf("Only one route targets %s%s, no overlap", in.Service.Name, port), Status: "pass"}
	var clash []string
	for _, o := range in.Others {
		if m, ok := rulesOverlap(in.Rules, o.Rules); ok {
			clash = append(clash, fmt.Sprintf("%s also matches %s", o.Ref.Name, m.describe()))
		}
	}
	if len(clash) > 0 {
		ov.Status, ov.Title = "warn", fmt.Sprintf("Other routes target %s%s with matches that overlap", in.Service.Name, port)
		ov.Detail = strings.Join(clash, "; ") + ". The oldest route wins a tie, so this one may never be reached."
	}
	out = append(out, ov)

	// 3. Present in other clusters.
	for _, cl := range in.Clusters {
		if !cl.HasService {
			continue
		}
		if cl.HasRoute {
			out = append(out, SuiteCheck{ID: "cluster-" + idPart(cl.Cluster), Title: "Present in " + cl.Cluster, Status: "pass"})
			continue
		}
		out = append(out, SuiteCheck{ID: "cluster-" + idPart(cl.Cluster), Title: "Same route missing in " + cl.Cluster, Status: "warn",
			Detail: "Callers there are not routed by this route's rules.",
			Action: &CheckAction{Label: "Copy →", Kind: "copy-to-cluster", Cluster: cl.Cluster}})
	}

	// 4. Timeout or retry hints.
	hasTimeout, hasRetry := false, false
	for _, r := range in.Rules {
		hasTimeout = hasTimeout || r.Timeout != ""
		hasRetry = hasRetry || r.Retry
	}
	if !hasTimeout && !hasRetry {
		out = append(out, SuiteCheck{ID: "resilience", Title: "No timeout or retry on this rule. Add one to get a timeout test", Status: "info", Action: &CheckAction{Label: "+ Add", Kind: "add-timeout"}})
	} else {
		out = append(out, SuiteCheck{ID: "resilience", Title: "Timeout or retry is configured", Status: "info", Detail: "Not tested: it needs a backend that can delay or fail a reply on request."})
	}
	return out
}

// ---- explain ---------------------------------------------------------------

type RouteBackendView struct {
	Name    string `json:"name"`
	Weight  int    `json:"weight"`
	Percent int    `json:"percent"`
}

// RouteRuleView is one rule's card: what it matches and where it sends.
type RouteRuleView struct {
	Rule     int                `json:"rule"`
	Matches  []string           `json:"matches"`
	Backends []RouteBackendView `json:"backends"`
	Timeout  string             `json:"timeout,omitempty"`
	Retry    bool               `json:"retry,omitempty"`
}

type RouteExplain struct {
	InWords string          `json:"inWords"`
	Rules   []RouteRuleView `json:"rules"`
	Notes   []string        `json:"notes"`
}

// ExplainRoute describes the route in words and per rule.
func ExplainRoute(in RouteInput, notes []string) RouteExplain {
	e := RouteExplain{Rules: []RouteRuleView{}, Notes: append([]string{}, notes...)}
	var sentences []string
	for i, r := range in.Rules {
		v := RouteRuleView{Rule: i + 1, Matches: []string{}, Backends: []RouteBackendView{}, Timeout: r.Timeout, Retry: r.Retry}
		ms := r.Matches
		if len(ms) == 0 {
			ms = []RouteMatchSpec{{}}
		}
		for _, m := range ms {
			v.Matches = append(v.Matches, m.describe())
		}
		pct := percentWeights(r.Backends)
		var dest []string
		for _, b := range r.Backends {
			v.Backends = append(v.Backends, RouteBackendView{Name: b.Name, Weight: b.Weight, Percent: pct[b.Name]})
			if b.Weight > 0 {
				if len(r.Backends) > 1 {
					dest = append(dest, fmt.Sprintf("%s (%d%%)", b.Name, pct[b.Name]))
				} else {
					dest = append(dest, b.Name)
				}
			}
		}
		e.Rules = append(e.Rules, v)
		if len(dest) > 0 {
			sentences = append(sentences, fmt.Sprintf("Rule %d: requests matching %s go to %s.", i+1, oxford(v.Matches, "or"), oxford(dest, "and")))
		}
	}
	e.InWords = strings.Join(sentences, " ")
	return e
}

// ---- workspace adapter -----------------------------------------------------

func routeRules(obj map[string]any, routeNS string) []RouteRuleSpec {
	var out []RouteRuleSpec
	for _, rv := range L(Path(obj, "spec", "rules")) {
		rm := M(rv)
		rule := RouteRuleSpec{}
		for _, mv := range L(rm["matches"]) {
			mm := M(mv)
			m := RouteMatchSpec{PathType: S(Path(mm, "path", "type")), Path: S(Path(mm, "path", "value")), Method: S(mm["method"])}
			for _, h := range L(mm["headers"]) {
				hm := M(h)
				m.Headers = append(m.Headers, RouteHeaderMatch{S(hm["name"]), S(hm["type"]), S(hm["value"])})
			}
			for _, q := range L(mm["queryParams"]) {
				qm := M(q)
				m.Query = append(m.Query, RouteHeaderMatch{S(qm["name"]), S(qm["type"]), S(qm["value"])})
			}
			rule.Matches = append(rule.Matches, m)
		}
		for _, bv := range L(rm["backendRefs"]) {
			b := M(bv)
			if S(b["group"]) != "" || defaultString(S(b["kind"]), "Service") != "Service" {
				continue
			}
			w := 1
			if _, ok := b["weight"]; ok {
				w = N(b["weight"])
			}
			rule.Backends = append(rule.Backends, RouteBackend{Namespace: defaultString(S(b["namespace"]), routeNS), Name: S(b["name"]), Port: N(b["port"]), Weight: w})
		}
		rule.Timeout = defaultString(S(Path(rm, "timeouts", "request")), S(Path(rm, "timeouts", "backendRequest")))
		if rule.Timeout == "0s" {
			rule.Timeout = ""
		}
		_, rule.Retry = rm["retry"]
		for _, f := range L(rm["filters"]) {
			t := S(M(f)["type"])
			switch t {
			case "RequestMirror":
				rule.Filters = append(rule.Filters, "requestMirror")
			case "":
			default:
				rule.Filters = append(rule.Filters, strings.ToLower(t[:1])+t[1:])
			}
		}
		out = append(out, rule)
	}
	return out
}

// ServiceForRoute is the Service a route's Service parent names.
func ServiceForRoute(ws *store.Workspace, cluster, routeNS string, spec map[string]any) (*store.Object, int) {
	for _, v := range L(spec["parentRefs"]) {
		p := M(v)
		if defaultString(S(p["kind"]), "Gateway") != "Service" || S(p["group"]) != "" {
			continue
		}
		if svc := ws.Get(cluster, "", "services", defaultString(S(p["namespace"]), routeNS), S(p["name"])); svc != nil {
			return svc, N(p["port"])
		}
	}
	return nil, 0
}

func endpointsReady(ws *store.Workspace, cluster, ns, name string) int {
	ready := 0
	for _, sl := range ws.List(cluster, "discovery.k8s.io", "endpointslices", ns, nil) {
		if sl.Labels()["kubernetes.io/service-name"] != name {
			continue
		}
		for _, ev := range L(sl.Obj["endpoints"]) {
			e := M(ev)
			if Path(e, "conditions", "ready") != false && len(L(e["addresses"])) > 0 {
				ready++
			}
		}
	}
	return ready
}

// RouteInputFor builds the generator input for a route object against its
// Service parent. drafts replace same-named routes in the "other routes" set.
func RouteInputFor(ws *store.Workspace, cluster string, obj map[string]any, drafts []map[string]any) (*RouteInput, error) {
	spec := M(obj["spec"])
	meta := M(obj["metadata"])
	ns := S(meta["namespace"])
	svc, port := ServiceForRoute(ws, cluster, ns, spec)
	if svc == nil {
		// A draft may leave the namespace out; the parent Service decides.
		if ns == "" {
			for _, v := range L(spec["parentRefs"]) {
				if p := M(v); S(p["kind"]) == "Service" {
					ns = S(p["namespace"])
				}
			}
		}
		return nil, fmt.Errorf("route %s has no Service parent in this capture; tests are sent to a Service", S(meta["name"]))
	}
	if ns == "" {
		ns = svc.Namespace
	}
	ref := objectRef(obj, cluster, KindHTTPRoute, ns)
	in := &RouteInput{Route: ref, Service: reference(svc), Port: port, DefaultPath: defaultProbePath(ws, svc), Rules: routeRules(obj, ns), Backends: map[string]BackendState{}}
	for _, h := range L(spec["hostnames"]) {
		in.Hostnames = append(in.Hostnames, S(h))
	}
	in.Source = pickSource(ws, cluster, svc, CaptureWorkloads(ws, cluster))
	if in.Port == 0 {
		if ps := servicePorts(svc); len(ps) > 0 {
			in.Port = ps[0]
		}
	}
	for _, r := range in.Rules {
		for _, b := range r.Backends {
			key := b.Namespace + "/" + b.Name
			if _, ok := in.Backends[key]; ok {
				continue
			}
			bs := BackendState{}
			if bsvc := ws.Get(cluster, "", "services", b.Namespace, b.Name); bsvc != nil {
				bs.Exists, bs.Ready = true, endpointsReady(ws, cluster, b.Namespace, b.Name)
			}
			in.Backends[key] = bs
		}
	}
	draftBy := map[string]map[string]any{}
	for _, d := range drafts {
		if S(d["kind"]) == KindHTTPRoute {
			r := objectRef(d, cluster, KindHTTPRoute, svc.Namespace)
			draftBy[r.Namespace+"/"+r.Name] = d
		}
	}
	seen := map[string]bool{ref.Namespace + "/" + ref.Name: true}
	consider := func(o map[string]any) {
		r := objectRef(o, cluster, KindHTTPRoute, svc.Namespace)
		key := r.Namespace + "/" + r.Name
		if seen[key] {
			return
		}
		seen[key] = true
		osvc, oport := ServiceForRoute(ws, cluster, r.Namespace, M(o["spec"]))
		if osvc == nil || osvc.Namespace != svc.Namespace || osvc.Name != svc.Name || (oport != 0 && port != 0 && oport != port) {
			return
		}
		in.Others = append(in.Others, OtherRoute{Ref: r, Rules: routeRules(o, r.Namespace)})
	}
	for _, o := range ws.List(cluster, GatewayGroup, "httproutes", svc.Namespace, nil) {
		if d, ok := draftBy[o.Namespace+"/"+o.Name]; ok {
			consider(d)
		} else {
			consider(o.Obj)
		}
	}
	for _, d := range draftBy {
		consider(d)
	}
	sort.Slice(in.Others, func(i, j int) bool { return in.Others[i].Ref.Name < in.Others[j].Ref.Name })
	for _, c := range ws.Clusters() {
		if c == cluster {
			continue
		}
		cp := ClusterPresence{Cluster: c}
		cp.HasService = ws.Get(c, "", "services", svc.Namespace, svc.Name) != nil
		cp.HasRoute = ws.Get(c, GatewayGroup, "httproutes", ref.Namespace, ref.Name) != nil
		in.Clusters = append(in.Clusters, cp)
	}
	return in, nil
}
