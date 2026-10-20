package ambient

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Explaining one request (R7.2): which route rule takes it, and what every
// AuthorizationPolicy on the Service and its waypoint says about it, in the
// order Istio evaluates them. Both are worked out from the configuration in
// the capture; the server adds what actually came back.

// ---- route rules -----------------------------------------------------------

// RouteRuleRow is one rule of a route, and whether its matches take the
// request.
type RouteRuleRow struct {
	Rule     int      `json:"rule"` // 1-based
	Matches  []string `json:"matches"`
	Matched  bool     `json:"matched"`
	Unknown  bool     `json:"unknown,omitempty"`
	Reason   string   `json:"reason"`
	Backends []string `json:"backends,omitempty"`
}

// RouteExplanation is the static route match for one request.
type RouteExplanation struct {
	Ref         Ref            `json:"ref"`
	Name        string         `json:"name"`
	Namespace   string         `json:"namespace"`
	MatchedRule int            `json:"matchedRule"` // 0 when no rule takes it
	Rules       []RouteRuleRow `json:"rules"`
	// Backend is the backend that answered, filled in by the caller from the
	// reply; empty when it is not known.
	Backend string `json:"backend"`
	Note    string `json:"note,omitempty"`
}

func routeRequest(req CaseRequest) routeReq {
	r := routeReq{Method: strings.ToUpper(defaultString(req.Method, "GET")), Path: defaultString(req.Path, "/"), Headers: map[string]string{}, Query: map[string]string{}}
	if i := strings.Index(r.Path, "?"); i >= 0 {
		if q, err := url.ParseQuery(r.Path[i+1:]); err == nil {
			for k, v := range q {
				if len(v) > 0 {
					r.Query[k] = v[0]
				}
			}
		}
		r.Path = r.Path[:i]
	}
	for k, v := range req.Headers {
		r.Headers[strings.ToLower(k)] = v
	}
	return r
}

// fullMatch is Envoy's safe_regex match: the whole value must match (RE2).
func fullMatch(pattern, v string) tri {
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return triUnknown
	}
	return b2t(re.MatchString(v))
}

// matchWhy evaluates one HTTPRouteMatch and says why, in a phrase.
func matchWhy(m RouteMatchSpec, r routeReq) (tri, string) {
	t := defaultString(m.PathType, "PathPrefix")
	v := defaultString(m.Path, "/")
	var pathT tri
	var pathWhy string
	switch t {
	case "Exact":
		pathT = b2t(r.Path == v)
		if pathT == triYes {
			pathWhy = fmt.Sprintf("path %s is exactly %s", r.Path, v)
		} else {
			pathWhy = fmt.Sprintf("path %s is not exactly %s", r.Path, v)
		}
	case "PathPrefix":
		pathT = pathMatches(m, r.Path)
		if pathT == triYes {
			pathWhy = fmt.Sprintf("path %s starts with %s", r.Path, v)
		} else {
			pathWhy = fmt.Sprintf("path %s does not start with %s", r.Path, v)
		}
	case "RegularExpression":
		pathT = fullMatch(v, r.Path)
		switch pathT {
		case triYes:
			pathWhy = fmt.Sprintf("path %s matches the regular expression %s", r.Path, v)
		case triNo:
			pathWhy = fmt.Sprintf("path %s does not match the regular expression %s", r.Path, v)
		default:
			pathWhy = fmt.Sprintf("the regular expression %s cannot be read", v)
		}
	default:
		pathT, pathWhy = triUnknown, "path match type "+t+" is not one Flightbay evaluates"
	}
	if pathT != triYes {
		return pathT, pathWhy
	}
	yes := []string{pathWhy}
	if m.Method != "" {
		if !strings.EqualFold(m.Method, r.Method) {
			return triNo, fmt.Sprintf("method %s is not %s", r.Method, m.Method)
		}
		yes = append(yes, "method is "+m.Method)
	}
	kv := func(kind string, h RouteHeaderMatch, v string, present bool) (tri, string) {
		if !present {
			return triNo, fmt.Sprintf("%s %s is absent", kind, h.Name)
		}
		switch defaultString(h.Type, "Exact") {
		case "Exact":
			if v == h.Value {
				return triYes, fmt.Sprintf("%s %s is %s", kind, h.Name, h.Value)
			}
			return triNo, fmt.Sprintf("%s %s is %s, not %s", kind, h.Name, v, h.Value)
		case "RegularExpression":
			switch fullMatch(h.Value, v) {
			case triYes:
				return triYes, fmt.Sprintf("%s %s matches %s", kind, h.Name, h.Value)
			case triNo:
				return triNo, fmt.Sprintf("%s %s is %s, which does not match %s", kind, h.Name, v, h.Value)
			}
		}
		return triUnknown, fmt.Sprintf("%s %s uses a match Flightbay cannot evaluate", kind, h.Name)
	}
	unknown := ""
	for _, h := range m.Headers {
		v, ok := r.Headers[strings.ToLower(h.Name)]
		switch t, why := kv("header", h, v, ok); t {
		case triNo:
			return triNo, why
		case triUnknown:
			unknown = why
		default:
			yes = append(yes, why)
		}
	}
	for _, q := range m.Query {
		v, ok := r.Query[q.Name]
		switch t, why := kv("query parameter", q, v, ok); t {
		case triNo:
			return triNo, why
		case triUnknown:
			unknown = why
		default:
			yes = append(yes, why)
		}
	}
	if unknown != "" {
		return triUnknown, unknown
	}
	return triYes, strings.Join(yes, ", ")
}

func (m RouteMatchSpec) label() string {
	s := defaultString(m.PathType, "PathPrefix") + " " + defaultString(m.Path, "/")
	if m.Method != "" {
		s = m.Method + " " + s
	}
	for _, h := range m.Headers {
		op := "="
		if h.Type == "RegularExpression" {
			op = "~"
		}
		s += " header " + h.Name + op + h.Value
	}
	for _, q := range m.Query {
		op := "="
		if q.Type == "RegularExpression" {
			op = "~"
		}
		s += " query " + q.Name + op + q.Value
	}
	return s
}

// ExplainRouteRequest works out, from the route's matches alone, which rule
// takes the request and why the others do not.
func ExplainRouteRequest(ref Ref, rules []RouteRuleSpec, req CaseRequest) RouteExplanation {
	r := routeRequest(req)
	out := RouteExplanation{Ref: ref, Name: ref.Name, Namespace: ref.Namespace, Rules: []RouteRuleRow{}}
	best, bestPrec := -1, [5]int{}
	unknown := false
	for i, rule := range rules {
		row := RouteRuleRow{Rule: i + 1, Matches: []string{}, Backends: rule.backendNames()}
		ms := rule.Matches
		if len(ms) == 0 {
			row.Matches = append(row.Matches, "PathPrefix /")
			row.Matched, row.Reason = true, "the rule has no matches, so it takes every request"
			if p := precedence(RouteMatchSpec{}); best < 0 || lessPrec(bestPrec, p) {
				best, bestPrec = i, p
			}
			out.Rules = append(out.Rules, row)
			continue
		}
		var no, unk []string
		for j, m := range ms {
			row.Matches = append(row.Matches, m.label())
			t, why := matchWhy(m, r)
			if len(ms) > 1 {
				why = fmt.Sprintf("match %d: %s", j+1, why)
			}
			switch t {
			case triYes:
				if !row.Matched {
					row.Matched, row.Reason = true, why
				}
				if p := precedence(m); best < 0 || lessPrec(bestPrec, p) {
					best, bestPrec = i, p
				}
			case triUnknown:
				unk = append(unk, why)
			default:
				no = append(no, why)
			}
		}
		if !row.Matched {
			if len(unk) > 0 {
				row.Unknown, unknown = true, true
				row.Reason = strings.Join(unk, "; ")
			} else {
				row.Reason = strings.Join(no, "; ")
			}
		}
		out.Rules = append(out.Rules, row)
	}
	if best >= 0 {
		out.MatchedRule = best + 1
		for i := range out.Rules {
			if out.Rules[i].Matched && i != best {
				out.Rules[i].Reason += fmt.Sprintf("; rule %d also matches and its match is more specific, so it takes the request", best+1)
			}
		}
	}
	switch {
	case unknown:
		out.Note = "A rule uses a match Flightbay cannot evaluate, so another rule may take the request."
	case best < 0 && len(rules) > 0:
		out.Note = "No rule matches, so the waypoint answers 404 for this route."
	}
	return out
}

// MatchRouteRule is the 1-based rule that takes the request, 0 when none
// does or it cannot be told.
func MatchRouteRule(rules []RouteRuleSpec, req CaseRequest) int {
	x := ExplainRouteRequest(Ref{}, rules, req)
	if x.Note != "" && x.MatchedRule == 0 {
		return 0
	}
	for _, row := range x.Rules {
		if row.Unknown {
			return 0
		}
	}
	return x.MatchedRule
}

// RouteForService finds the HTTPRoute that has the Service as its parent
// (on the given port, or any port), for the explain page of any object.
func RouteForService(ws *store.Workspace, svc *store.Object, port int) (Ref, []RouteRuleSpec, bool) {
	routes := ws.List(svc.Cluster, GatewayGroup, "httproutes", "", nil)
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Namespace+"/"+routes[i].Name < routes[j].Namespace+"/"+routes[j].Name
	})
	for _, o := range routes {
		for _, v := range L(Path(o.Obj, "spec", "parentRefs")) {
			p := M(v)
			if defaultString(S(p["kind"]), "Gateway") != "Service" || S(p["group"]) != "" {
				continue
			}
			if defaultString(S(p["namespace"]), o.Namespace) != svc.Namespace || S(p["name"]) != svc.Name {
				continue
			}
			if pp := N(p["port"]); pp != 0 && port != 0 && pp != port {
				continue
			}
			return reference(o), routeRules(o.Obj, o.Namespace), true
		}
	}
	return Ref{}, nil, false
}

// ---- authorization ---------------------------------------------------------

// AppliedPolicy is an AuthorizationPolicy that applies to a Service, with
// where it is enforced and why it applies.
type AppliedPolicy struct {
	Policy AuthzPolicy
	Scope  string // a phrase: "Service reviews", "waypoint bookinfo/bookinfo-waypoint"
	// NotEnforced says why an accepted policy has no effect, e.g. it binds to
	// a waypoint that does not implement Istio policy.
	NotEnforced string
}

// Caller is who sent a request, as far as policy evaluation is concerned.
type Caller struct {
	Namespace string
	Principal string
	Known     bool
}

// ServiceAuthzPolicies lists every Istio AuthorizationPolicy that reaches the
// Service: targetRefs to it or its waypoint, a selector on its Pods, its
// namespace, or the root namespace.
func ServiceAuthzPolicies(ws *store.Workspace, svc *store.Object) []AppliedPolicy {
	out := []AppliedPolicy{}
	wpNS, wpName, _ := Waypoint(ws, svc)
	for _, e := range servicePolicies(ws, svc) {
		if e.Ref.Kind != KindAuthorizationPolicy || e.Ref.Group != SecurityGroup {
			continue
		}
		o := ws.Get(svc.Cluster, SecurityGroup, "authorizationpolicies", e.Ref.Namespace, e.Ref.Name)
		if o == nil {
			continue
		}
		ref := e.Ref
		if full, err := SuiteRef(ref.Cluster, ref.Kind, ref.Namespace, ref.Name); err == nil {
			ref = full
		}
		ap := AppliedPolicy{Policy: ParseAuthzPolicy(ref, M(o.Obj["spec"]), e.Enforced), NotEnforced: e.Warning}
		switch e.Scope {
		case "service":
			ap.Scope = "Service " + svc.Name
		case "waypoint":
			ap.Scope = "waypoint " + wpNS + "/" + wpName
		case "workload":
			ap.Scope = "Pods of " + svc.Name + " (selector)"
		case "namespace":
			ap.Scope = "namespace " + e.Ref.Namespace
		case "mesh":
			ap.Scope = "mesh-wide (root namespace " + e.Ref.Namespace + ")"
		default:
			ap.Scope = e.Scope
		}
		out = append(out, ap)
	}
	return out
}

// CallerFor works out the identity a request is sent with: the identity a
// case asks for, else the client Pod ("namespace/pod"). A Pod that is not in
// the capture (a temporary Pod) runs as its namespace's default account.
func CallerFor(ws *store.Workspace, cluster, client string, from *CaseFrom) Caller {
	td := ""
	if ws != nil {
		td = clusterTrustDomain(ws, cluster)
	}
	if td == "" {
		td = "cluster.local"
	}
	if from != nil && from.Namespace != "" {
		sa := defaultString(from.ServiceAccount, "default")
		return Caller{Namespace: from.Namespace, Principal: defaultString(from.Principal, td+"/ns/"+from.Namespace+"/sa/"+sa), Known: true}
	}
	ns, pod, ok := strings.Cut(client, "/")
	if !ok || ns == "" {
		return Caller{}
	}
	sa := "default"
	if ws != nil {
		if o := ws.Get(cluster, "", "pods", ns, pod); o != nil {
			sa = defaultString(S(Path(o.Obj, "spec", "serviceAccountName")), "default")
		}
	}
	return Caller{Namespace: ns, Principal: td + "/ns/" + ns + "/sa/" + sa, Known: true}
}

// WaypointPrincipal is the identity the Service's waypoint presents to the
// destination ztunnel; empty when the Service has no waypoint or it cannot
// be told.
func WaypointPrincipal(ws *store.Workspace, svc *store.Object) string {
	return waypointPrincipal(ws, svc)
}

// PolicyEval is one policy's part in a decision.
type PolicyEval struct {
	Ref       Ref    `json:"ref"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Action    string `json:"action"`
	// Order is Istio's evaluation order: 1 CUSTOM, 2 DENY, 3 ALLOW, 4 AUDIT.
	Order    int    `json:"order"`
	Matched  bool   `json:"matched"`
	Unknown  bool   `json:"unknown,omitempty"`
	Reason   string `json:"reason"`
	Scope    string `json:"scope"`
	Enforced string `json:"enforced"` // waypoint, ztunnel, or "not enforced"
	// Decided marks the policy that made the decision.
	Decided bool `json:"decided,omitempty"`
}

// PolicyPrediction is what the policies together say about one request.
type PolicyPrediction struct {
	Status int
	Known  bool
	By     *Ref
	// DecidedBy follows the suite result's wording: a policy name, "no ALLOW
	// matched" or "no policy refused it".
	DecidedBy string
	Verdict   string
	Policies  []PolicyEval
}

func actionOrder(a string) int {
	switch a {
	case "CUSTOM":
		return 1
	case "DENY":
		return 2
	case "ALLOW":
		return 3
	}
	return 4
}

// PredictRequest evaluates every policy for one request. A request sent
// straight to a Pod (req.Direct) is seen by ztunnel only. hasWaypoint says the
// capture shows a waypoint for the Service; waypointPrincipal is the identity
// ztunnel sees on traffic the waypoint forwards. When the capture shows no
// waypoint, waypoint policies are still evaluated (the waypoint may simply
// not be captured) and their reason says so.
func PredictRequest(policies []AppliedPolicy, req CaseRequest, port int, caller Caller, hasWaypoint bool, waypointPrincipal string) PolicyPrediction {
	viaWaypoint := !req.Direct
	r := reqModel{Method: strings.ToUpper(defaultString(req.Method, "GET")), Path: defaultString(req.Path, "/"), Port: req.Port, Headers: map[string]string{}}
	if r.Port == 0 {
		r.Port = port
	}
	if i := strings.Index(r.Path, "?"); i >= 0 {
		r.Path = r.Path[:i]
	}
	for k, v := range req.Headers {
		r.Headers[strings.ToLower(k)] = v
	}
	if caller.Known {
		r.Namespace, r.Principal, r.SourceKnown = caller.Namespace, caller.Principal, true
	}
	ordered := append([]AppliedPolicy(nil), policies...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i].Policy, ordered[j].Policy
		if actionOrder(a.Action) != actionOrder(b.Action) {
			return actionOrder(a.Action) < actionOrder(b.Action)
		}
		return a.Ref.Namespace+"/"+a.Ref.Name < b.Ref.Namespace+"/"+b.Ref.Name
	})
	out := PolicyPrediction{Policies: []PolicyEval{}}
	var wp, zt []int
	for i, ap := range ordered {
		p := ap.Policy
		ev := PolicyEval{Ref: p.Ref, Namespace: p.Ref.Namespace, Name: p.Ref.Name, Action: p.Action, Order: actionOrder(p.Action), Scope: ap.Scope, Enforced: p.Enforced}
		switch {
		case ap.NotEnforced != "":
			ev.Enforced, ev.Reason = "not enforced", ap.NotEnforced
		case p.Enforced == "waypoint" && !viaWaypoint:
			ev.Reason = "enforced by the waypoint, and this request was sent straight to the Pod, so it never saw it"
		case p.Action == "AUDIT":
			t, why := policyWhy(p, r)
			ev.Matched, ev.Unknown, ev.Reason = t == triYes, t == triUnknown, why+" (AUDIT only logs)"
		case p.Enforced == "waypoint":
			t, why := policyWhy(p, r)
			if !hasWaypoint {
				why += " (the capture shows no waypoint for this Service, so check the policy is bound to one)"
			}
			ev.Matched, ev.Unknown, ev.Reason = t == triYes, t == triUnknown, why
			wp = append(wp, i)
		default:
			zr := r.clone()
			if viaWaypoint && hasWaypoint && waypointPrincipal != "" {
				// ztunnel at the destination sees the waypoint, not the caller.
				zr.Principal, zr.Namespace, zr.SourceKnown = waypointPrincipal, principalNamespace(waypointPrincipal), true
			} else if viaWaypoint && hasWaypoint {
				zr.SourceKnown = false
			}
			t, why := policyWhy(p, zr)
			if l7 := l7Fields(p); l7 != "" && t != triNo {
				t, why = triUnknown, "uses "+l7+", which ztunnel cannot see"
			}
			if viaWaypoint && hasWaypoint && t != triUnknown && waypointPrincipal != "" && hasSourceFields(p) {
				why += " (ztunnel checks the waypoint's identity " + waypointPrincipal + ")"
			}
			ev.Matched, ev.Unknown, ev.Reason = t == triYes, t == triUnknown, why
			zt = append(zt, i)
		}
		out.Policies = append(out.Policies, ev)
	}
	layer := func(idx []int) (status int, decided int, known bool, why string) {
		decided = -1
		for _, i := range idx {
			ev := &out.Policies[i]
			if ev.Action == "CUSTOM" && (ev.Matched || ev.Unknown) {
				return 0, i, false, "Sent to an external authorizer by " + ev.Namespace + "/" + ev.Name + "; Flightbay cannot predict its answer"
			}
		}
		unk := -1
		for _, i := range idx {
			ev := &out.Policies[i]
			if ev.Action != "DENY" {
				continue
			}
			if ev.Matched {
				return 403, i, true, ""
			}
			if ev.Unknown && unk < 0 {
				unk = i
			}
		}
		if unk >= 0 {
			ev := out.Policies[unk]
			return 0, unk, false, "Flightbay cannot tell whether DENY " + ev.Namespace + "/" + ev.Name + " matches: " + ev.Reason
		}
		allows := 0
		for _, i := range idx {
			ev := &out.Policies[i]
			if ev.Action != "ALLOW" {
				continue
			}
			allows++
			if ev.Matched {
				return 200, i, true, ""
			}
			if ev.Unknown && unk < 0 {
				unk = i
			}
		}
		if allows == 0 {
			return 200, -1, true, ""
		}
		if unk >= 0 {
			ev := out.Policies[unk]
			return 0, unk, false, "Flightbay cannot tell whether ALLOW " + ev.Namespace + "/" + ev.Name + " matches: " + ev.Reason
		}
		return 403, -1, true, ""
	}
	name := func(i int) string { return out.Policies[i].Namespace + "/" + out.Policies[i].Name }
	if viaWaypoint {
		st, by, known, why := layer(wp)
		if !known {
			out.Verdict = why
			return out
		}
		if st == 403 {
			out.Status, out.Known = 403, true
			if by >= 0 {
				out.Policies[by].Decided = true
				ref := out.Policies[by].Ref
				out.By, out.DecidedBy, out.Verdict = &ref, out.Policies[by].Name, "Denied by "+name(by)
			} else {
				out.DecidedBy, out.Verdict = "no ALLOW matched", "Denied: ALLOW policies apply at the waypoint and none matched"
			}
			return out
		}
		zst, zby, zknown, zwhy := layer(zt)
		if !zknown {
			out.Verdict = zwhy
			return out
		}
		if zst == 403 && !hasWaypoint {
			// No waypoint in between: ztunnel resets the caller's connection.
			out.Status, out.Known = 0, true
			if zby >= 0 {
				out.Policies[zby].Decided = true
				ref := out.Policies[zby].Ref
				out.By, out.DecidedBy, out.Verdict = &ref, out.Policies[zby].Name, "Refused by ztunnel ("+name(zby)+"): the connection is reset"
			} else {
				out.DecidedBy, out.Verdict = "no ALLOW matched", "Refused by ztunnel: ALLOW policies apply and none matched, so the connection is reset"
			}
			return out
		}
		if zst == 403 {
			// ztunnel resets the waypoint's connection; the waypoint answers 503.
			out.Status, out.Known = 503, true
			if zby >= 0 {
				out.Policies[zby].Decided = true
				ref := out.Policies[zby].Ref
				out.By, out.DecidedBy, out.Verdict = &ref, out.Policies[zby].Name, "Refused by ztunnel ("+name(zby)+"), so the waypoint answers 503"
			} else {
				out.DecidedBy, out.Verdict = "no ALLOW matched", "Refused by ztunnel: ALLOW policies apply there and none admits the waypoint, so the waypoint answers 503"
			}
			return out
		}
		out.Status, out.Known = 200, true
		switch {
		case by >= 0:
			out.Policies[by].Decided = true
			ref := out.Policies[by].Ref
			out.By, out.DecidedBy, out.Verdict = &ref, out.Policies[by].Name, "Allowed by "+name(by)
		case len(out.Policies) == 0:
			out.DecidedBy, out.Verdict = "no policy refused it", "Allowed: no AuthorizationPolicy applies"
		default:
			out.DecidedBy, out.Verdict = "no policy refused it", "Allowed: no DENY matched and no ALLOW policy applies"
		}
		return out
	}
	st, by, known, why := layer(zt)
	if !known {
		out.Verdict = why
		return out
	}
	out.Known = true
	if st == 403 {
		// ztunnel resets the connection: curl sees no HTTP status.
		out.Status = 0
		if by >= 0 {
			out.Policies[by].Decided = true
			ref := out.Policies[by].Ref
			out.By, out.DecidedBy, out.Verdict = &ref, out.Policies[by].Name, "Refused by ztunnel ("+name(by)+"): the connection is reset"
		} else {
			out.DecidedBy, out.Verdict = "no ALLOW matched", "Refused by ztunnel: ALLOW policies apply and none matched, so the connection is reset"
		}
		return out
	}
	out.Status = 200
	switch {
	case by >= 0:
		out.Policies[by].Decided = true
		ref := out.Policies[by].Ref
		out.By, out.DecidedBy, out.Verdict = &ref, out.Policies[by].Name, "Allowed by "+name(by)+" at ztunnel"
	case len(zt) == 0:
		out.DecidedBy, out.Verdict = "no policy refused it", "Allowed: no policy on the path applies"
	default:
		out.DecidedBy, out.Verdict = "no policy refused it", "Allowed: no DENY matched and no ALLOW policy applies at ztunnel"
	}
	return out
}

func principalNamespace(p string) string {
	parts := strings.Split(p, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "ns" {
			return parts[i+1]
		}
	}
	return ""
}

func hasSourceFields(p AuthzPolicy) bool {
	for _, r := range p.Rules {
		if len(r.From) > 0 {
			return true
		}
	}
	return false
}

// l7Fields names the HTTP fields of a policy, which ztunnel cannot evaluate.
func l7Fields(p AuthzPolicy) string {
	var out []string
	add := func(s string) {
		for _, x := range out {
			if x == s {
				return
			}
		}
		out = append(out, s)
	}
	for _, r := range p.Rules {
		for _, o := range r.To {
			if len(o.Methods)+len(o.NotMethods) > 0 {
				add("methods")
			}
			if len(o.Paths)+len(o.NotPaths) > 0 {
				add("paths")
			}
			if len(o.Hosts)+len(o.NotHosts) > 0 {
				add("hosts")
			}
		}
		for _, c := range r.When {
			if _, ok := condHeader(c.Key); ok {
				add("request headers")
			}
		}
		for _, s := range r.From {
			if len(s.RequestPrincipals)+len(s.NotRequestPrincipals) > 0 {
				add("request principals")
			}
		}
	}
	return strings.Join(out, ", ")
}

func joinList(xs []string) string { return strings.Join(xs, ", ") }

// policyWhy is policyMatch with the condition that decided it.
func policyWhy(p AuthzPolicy, r reqModel) (tri, string) {
	if len(p.Rules) == 0 {
		if p.HasRulesKey {
			return triNo, "its rules list is empty, so it matches no request"
		}
		return triNo, "it has no rules, so it matches no request"
	}
	var no, unk []string
	for i, rule := range p.Rules {
		t, why := ruleWhy(rule, r)
		prefix := ""
		if len(p.Rules) > 1 {
			prefix = fmt.Sprintf("rule %d: ", i+1)
		}
		switch t {
		case triYes:
			return triYes, prefix + why
		case triUnknown:
			unk = append(unk, prefix+why)
		default:
			no = append(no, prefix+why)
		}
	}
	if len(unk) > 0 {
		return triUnknown, strings.Join(unk, "; ")
	}
	return triNo, strings.Join(no, "; ")
}

func ruleWhy(rule AccessRule, r reqModel) (tri, string) {
	var yes []string
	unknown := ""
	add := func(t tri, why string) bool {
		switch t {
		case triNo:
			return true
		case triUnknown:
			if unknown == "" {
				unknown = why
			}
		default:
			yes = append(yes, why)
		}
		return false
	}
	if len(rule.From) > 0 {
		if t, why := anyWhy(len(rule.From), func(i int) (tri, string) { return sourceWhy(rule.From[i], r) }); add(t, why) {
			return triNo, why
		}
	}
	if len(rule.To) > 0 {
		if t, why := anyWhy(len(rule.To), func(i int) (tri, string) { return opWhy(rule.To[i], r) }); add(t, why) {
			return triNo, why
		}
	}
	for _, c := range rule.When {
		if t, why := condWhy(c, r); add(t, why) {
			return triNo, why
		}
	}
	if unknown != "" {
		return triUnknown, unknown
	}
	if len(yes) == 0 {
		return triYes, "the rule has no conditions, so it matches every request"
	}
	return triYes, strings.Join(yes, ", ")
}

func anyWhy(n int, f func(int) (tri, string)) (tri, string) {
	var no []string
	unknown := ""
	for i := 0; i < n; i++ {
		t, why := f(i)
		switch t {
		case triYes:
			return triYes, why
		case triUnknown:
			if unknown == "" {
				unknown = why
			}
		default:
			no = append(no, why)
		}
	}
	if unknown != "" {
		return triUnknown, unknown
	}
	return triNo, strings.Join(no, " and ")
}

func sourceWhy(s AccessSource, r reqModel) (tri, string) {
	for _, f := range []struct {
		name string
		v    []string
	}{{"ipBlocks", s.IPBlocks}, {"notIpBlocks", s.NotIPBlocks}, {"remoteIpBlocks", s.RemoteIPBlocks}, {"notRemoteIpBlocks", s.NotRemoteIPBlocks}, {"requestPrincipals", s.RequestPrincipals}, {"notRequestPrincipals", s.NotRequestPrincipals}, {"serviceAccounts", s.ServiceAccounts}, {"notServiceAccounts", s.NotServiceAccounts}} {
		if len(f.v) > 0 {
			return triUnknown, "uses from.source." + f.name + ", which Flightbay cannot evaluate"
		}
	}
	for _, v := range s.Services {
		return triUnknown, "uses from.source." + strings.TrimPrefix(v, "?") + ", which Flightbay cannot evaluate"
	}
	if len(s.Principals)+len(s.NotPrincipals)+len(s.Namespaces)+len(s.NotNamespaces) > 0 && !r.SourceKnown {
		return triUnknown, "it checks the caller's identity, which is not known for this request"
	}
	var yes []string
	if len(s.Principals) > 0 {
		if !anyMatch(s.Principals, r.Principal) {
			return triNo, fmt.Sprintf("caller %s is not in principals %s", r.Principal, joinList(s.Principals))
		}
		yes = append(yes, "caller "+r.Principal+" is in principals")
	}
	if len(s.NotPrincipals) > 0 {
		if anyMatch(s.NotPrincipals, r.Principal) {
			return triNo, fmt.Sprintf("caller %s is in notPrincipals", r.Principal)
		}
		yes = append(yes, "caller "+r.Principal+" is not in notPrincipals")
	}
	if len(s.Namespaces) > 0 {
		if !anyMatch(s.Namespaces, r.Namespace) {
			return triNo, fmt.Sprintf("caller namespace %s is not in %s", r.Namespace, joinList(s.Namespaces))
		}
		yes = append(yes, "caller namespace "+r.Namespace+" is in namespaces")
	}
	if len(s.NotNamespaces) > 0 {
		if anyMatch(s.NotNamespaces, r.Namespace) {
			return triNo, fmt.Sprintf("caller namespace %s is in notNamespaces", r.Namespace)
		}
		yes = append(yes, "caller namespace "+r.Namespace+" is not in notNamespaces")
	}
	if len(yes) == 0 {
		return triYes, "any caller"
	}
	return triYes, strings.Join(yes, ", ")
}

func opWhy(o AccessOperation, r reqModel) (tri, string) {
	if len(o.Hosts)+len(o.NotHosts) > 0 {
		for _, h := range append(append([]string{}, o.Hosts...), o.NotHosts...) {
			if strings.HasPrefix(h, "?") {
				return triUnknown, "uses to.operation." + strings.TrimPrefix(h, "?") + ", which Flightbay cannot evaluate"
			}
		}
		return triUnknown, "uses to.operation.hosts, which Flightbay cannot evaluate"
	}
	var yes []string
	check := func(in bool, pats []string, v, field string) (bool, string) {
		if len(pats) == 0 {
			return true, ""
		}
		m := anyMatch(pats, v)
		if in && !m {
			return false, fmt.Sprintf("%s %s is not in %s", field, v, joinList(pats))
		}
		if !in && m {
			return false, fmt.Sprintf("%s %s is excluded by not%s%s", field, v, strings.ToUpper(field[:1]), field[1:]+"s")
		}
		if in {
			yes = append(yes, fmt.Sprintf("%s %s is in %s", field, v, joinList(pats)))
		}
		return true, ""
	}
	port := strconv.Itoa(r.Port)
	for _, c := range []struct {
		in    bool
		pats  []string
		v     string
		field string
	}{{true, o.Methods, r.Method, "method"}, {false, o.NotMethods, r.Method, "method"}, {true, o.Paths, r.Path, "path"}, {false, o.NotPaths, r.Path, "path"}, {true, o.Ports, port, "port"}, {false, o.NotPorts, port, "port"}} {
		if ok, why := check(c.in, c.pats, c.v, c.field); !ok {
			return triNo, why
		}
	}
	if len(yes) == 0 {
		return triYes, "any operation"
	}
	return triYes, strings.Join(yes, ", ")
}

func condWhy(c AccessCondition, r reqModel) (tri, string) {
	name, ok := condHeader(c.Key)
	if !ok {
		return triUnknown, "uses when " + c.Key + ", which Flightbay cannot evaluate"
	}
	v, present := r.Headers[name]
	if len(c.Values) > 0 {
		if !present {
			return triNo, "header " + name + " is absent"
		}
		if !anyMatch(c.Values, v) {
			return triNo, fmt.Sprintf("header %s is %s, not %s", name, v, joinList(c.Values))
		}
	}
	if len(c.NotValues) > 0 && present && anyMatch(c.NotValues, v) {
		return triNo, fmt.Sprintf("header %s is %s, which notValues excludes", name, v)
	}
	if !present {
		return triYes, "header " + name + " is absent, which notValues allows"
	}
	return triYes, fmt.Sprintf("header %s is %s", name, v)
}
