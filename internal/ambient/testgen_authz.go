package ambient

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Case generator for AuthorizationPolicy (plan 7.2).
//
// The generator builds requests from the rule, then predicts the outcome by
// evaluating every AuthorizationPolicy on the same target the way Istio does:
// any DENY match refuses, then if any ALLOW policy exists one of them must
// match. When a policy uses a field the evaluator does not model, the answer
// is "unknown" and the case is left out rather than guessed, with a note.

// AuthzPolicy is an AuthorizationPolicy reduced to what the generator reads.
type AuthzPolicy struct {
	Ref      Ref
	Action   string // ALLOW, DENY, CUSTOM, AUDIT
	Rules    []AccessRule
	Enforced string // waypoint or ztunnel
	// Unsupported lists fields the generator and evaluator do not model.
	Unsupported []UnsupportedField
	// HasRulesKey is false for a policy with no rules list at all.
	HasRulesKey bool
}

type UnsupportedField struct {
	Rule  int // 1-based
	Field string
}

// Workload is a captured caller the generator may pick as a source.
type Workload struct {
	Ref            Ref // the Pod
	Namespace      string
	ServiceAccount string
	Principal      string
	InMesh         bool
	// Curl is true when the Pod runs a curl client, so it can send requests.
	Curl bool
}

// AuthzInput is everything the generator needs. It holds no store, so tests
// can state it directly.
type AuthzInput struct {
	Policy  AuthzPolicy
	Service Ref
	// Port is the Service port requests go to; Ports lists all of them.
	Port        int
	Ports       []int
	DefaultPath string
	Others      []AuthzPolicy
	// Peer lists mTLS settings that also apply to the Service, for the
	// "other policies" panel. They do not affect the generated cases.
	Peer      []OtherPolicy
	Workloads []Workload
	// Source is the caller the suite is sent from. Rules that look at the
	// caller are evaluated against it.
	Source *Workload
	// WaypointLock is true when a ztunnel policy refuses traffic that does not
	// come from the waypoint, so a request straight to a Pod is refused.
	WaypointLock bool
}

// ---- parsing ---------------------------------------------------------------

func strs(v any) []string {
	out := []string{}
	for _, x := range L(v) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case float64, int, int64:
			out = append(out, fmt.Sprint(t))
		}
	}
	return out
}

var (
	sourceFields    = map[string]bool{"principals": true, "notPrincipals": true, "namespaces": true, "notNamespaces": true}
	operationFields = map[string]bool{"methods": true, "notMethods": true, "paths": true, "notPaths": true, "ports": true, "notPorts": true}
)

// ParseAuthzPolicy reads a raw AuthorizationPolicy spec.
func ParseAuthzPolicy(ref Ref, spec map[string]any, enforced string) AuthzPolicy {
	p := AuthzPolicy{Ref: ref, Action: strings.ToUpper(S(spec["action"])), Enforced: enforced}
	if p.Action == "" {
		p.Action = "ALLOW"
	}
	_, p.HasRulesKey = spec["rules"]
	for i, rv := range L(spec["rules"]) {
		rm := M(rv)
		var rule AccessRule
		bad := func(field string) {
			p.Unsupported = append(p.Unsupported, UnsupportedField{i + 1, field})
		}
		for _, fv := range L(rm["from"]) {
			src := M(M(fv)["source"])
			s := AccessSource{Principals: strs(src["principals"]), NotPrincipals: strs(src["notPrincipals"]), Namespaces: strs(src["namespaces"]), NotNamespaces: strs(src["notNamespaces"])}
			for _, k := range sortedKeys(src) {
				if !sourceFields[k] {
					bad("from.source." + k)
					// Kept so the evaluator treats this source as undecidable.
					switch k {
					case "ipBlocks":
						s.IPBlocks = strs(src[k])
					case "remoteIpBlocks":
						s.RemoteIPBlocks = strs(src[k])
					case "requestPrincipals":
						s.RequestPrincipals = strs(src[k])
					default:
						s.Services = append(s.Services, "?"+k)
					}
				}
			}
			rule.From = append(rule.From, s)
		}
		for _, tv := range L(rm["to"]) {
			op := M(M(tv)["operation"])
			o := AccessOperation{Methods: strs(op["methods"]), NotMethods: strs(op["notMethods"]), Paths: strs(op["paths"]), NotPaths: strs(op["notPaths"]), Ports: strs(op["ports"]), NotPorts: strs(op["notPorts"])}
			for _, k := range sortedKeys(op) {
				if !operationFields[k] {
					bad("to.operation." + k)
					o.Hosts = append(o.Hosts, "?"+k)
				}
			}
			rule.To = append(rule.To, o)
		}
		for _, wv := range L(rm["when"]) {
			w := M(wv)
			c := AccessCondition{Key: S(w["key"]), Values: strs(w["values"]), NotValues: strs(w["notValues"])}
			if !strings.HasPrefix(c.Key, "request.headers[") {
				bad("when " + c.Key)
			}
			rule.When = append(rule.When, c)
		}
		p.Rules = append(p.Rules, rule)
	}
	return p
}

func (p AuthzPolicy) unsupportedIn(rule int) []string {
	out := []string{}
	for _, u := range p.Unsupported {
		if u.Rule == rule {
			out = append(out, u.Field)
		}
	}
	return out
}

// ---- evaluator -------------------------------------------------------------

type tri int

const (
	triNo tri = iota
	triYes
	triUnknown
)

func and(xs ...tri) tri {
	out := triYes
	for _, x := range xs {
		if x == triNo {
			return triNo
		}
		if x == triUnknown {
			out = triUnknown
		}
	}
	return out
}

func or(xs ...tri) tri {
	out := triNo
	for _, x := range xs {
		if x == triYes {
			return triYes
		}
		if x == triUnknown {
			out = triUnknown
		}
	}
	return out
}

func not(x tri) tri {
	switch x {
	case triYes:
		return triNo
	case triNo:
		return triYes
	}
	return triUnknown
}

func b2t(b bool) tri {
	if b {
		return triYes
	}
	return triNo
}

// reqModel is what the evaluator knows about a request.
type reqModel struct {
	Method      string
	Path        string
	Port        int
	Headers     map[string]string // lower-case names
	Namespace   string
	Principal   string
	SourceKnown bool
}

func (r reqModel) clone() reqModel {
	c := r
	c.Headers = map[string]string{}
	for k, v := range r.Headers {
		c.Headers[k] = v
	}
	return c
}

// strMatch is Istio's StringMatch: "*", "prefix*", "*suffix" or exact.
func strMatch(pattern, v string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(v, pattern[1:])
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(v, pattern[:len(pattern)-1])
	}
	return pattern == v
}

func anyMatch(patterns []string, v string) bool {
	for _, p := range patterns {
		if strMatch(p, v) {
			return true
		}
	}
	return false
}

func sourceMatch(s AccessSource, r reqModel) tri {
	if len(s.IPBlocks)+len(s.RemoteIPBlocks)+len(s.NotIPBlocks)+len(s.NotRemoteIPBlocks)+len(s.RequestPrincipals)+len(s.NotRequestPrincipals)+len(s.Services)+len(s.ServiceAccounts)+len(s.NotServiceAccounts) > 0 {
		return triUnknown
	}
	if len(s.Principals)+len(s.NotPrincipals)+len(s.Namespaces)+len(s.NotNamespaces) > 0 && !r.SourceKnown {
		return triUnknown
	}
	parts := []tri{}
	if len(s.Principals) > 0 {
		parts = append(parts, b2t(anyMatch(s.Principals, r.Principal)))
	}
	if len(s.NotPrincipals) > 0 {
		parts = append(parts, b2t(!anyMatch(s.NotPrincipals, r.Principal)))
	}
	if len(s.Namespaces) > 0 {
		parts = append(parts, b2t(anyMatch(s.Namespaces, r.Namespace)))
	}
	if len(s.NotNamespaces) > 0 {
		parts = append(parts, b2t(!anyMatch(s.NotNamespaces, r.Namespace)))
	}
	return and(parts...)
}

func opMatch(o AccessOperation, r reqModel) tri {
	if len(o.Hosts)+len(o.NotHosts) > 0 {
		return triUnknown
	}
	parts := []tri{}
	if len(o.Methods) > 0 {
		parts = append(parts, b2t(anyMatch(o.Methods, r.Method)))
	}
	if len(o.NotMethods) > 0 {
		parts = append(parts, b2t(!anyMatch(o.NotMethods, r.Method)))
	}
	if len(o.Paths) > 0 {
		parts = append(parts, b2t(anyMatch(o.Paths, r.Path)))
	}
	if len(o.NotPaths) > 0 {
		parts = append(parts, b2t(!anyMatch(o.NotPaths, r.Path)))
	}
	port := strconv.Itoa(r.Port)
	if len(o.Ports) > 0 {
		parts = append(parts, b2t(anyMatch(o.Ports, port)))
	}
	if len(o.NotPorts) > 0 {
		parts = append(parts, b2t(!anyMatch(o.NotPorts, port)))
	}
	return and(parts...)
}

func condHeader(key string) (string, bool) {
	if strings.HasPrefix(key, "request.headers[") && strings.HasSuffix(key, "]") {
		return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(key, "request.headers["), "]")), true
	}
	return "", false
}

func condMatch(c AccessCondition, r reqModel) tri {
	name, ok := condHeader(c.Key)
	if !ok {
		return triUnknown
	}
	v, present := r.Headers[name]
	parts := []tri{}
	if len(c.Values) > 0 {
		parts = append(parts, b2t(present && anyMatch(c.Values, v)))
	}
	if len(c.NotValues) > 0 {
		parts = append(parts, b2t(!(present && anyMatch(c.NotValues, v))))
	}
	return and(parts...)
}

func ruleMatch(rule AccessRule, r reqModel) tri {
	parts := []tri{}
	if len(rule.From) > 0 {
		xs := []tri{}
		for _, s := range rule.From {
			xs = append(xs, sourceMatch(s, r))
		}
		parts = append(parts, or(xs...))
	}
	if len(rule.To) > 0 {
		xs := []tri{}
		for _, o := range rule.To {
			xs = append(xs, opMatch(o, r))
		}
		parts = append(parts, or(xs...))
	}
	for _, c := range rule.When {
		parts = append(parts, condMatch(c, r))
	}
	return and(parts...)
}

func policyMatch(p AuthzPolicy, r reqModel) tri {
	xs := []tri{}
	for _, rule := range p.Rules {
		xs = append(xs, ruleMatch(rule, r))
	}
	return or(xs...)
}

// verdict is the outcome of every policy together.
type verdict struct {
	Status int
	By     *AuthzPolicy // nil when no policy decided (nothing denied, no ALLOW)
	Known  bool
	// Blocker names a policy whose fields could not be evaluated.
	Blocker string
}

func decide(policies []AuthzPolicy, r reqModel) verdict {
	var unknownBy *AuthzPolicy
	for i := range policies {
		p := &policies[i]
		if p.Action != "DENY" {
			continue
		}
		switch policyMatch(*p, r) {
		case triYes:
			return verdict{Status: 403, By: p, Known: true}
		case triUnknown:
			unknownBy = p
		}
	}
	if unknownBy != nil {
		return verdict{Blocker: unknownBy.Ref.Name}
	}
	allows := 0
	for i := range policies {
		p := &policies[i]
		if p.Action != "ALLOW" {
			continue
		}
		allows++
	}
	if allows == 0 {
		return verdict{Status: 200, Known: true}
	}
	for i := range policies {
		p := &policies[i]
		if p.Action != "ALLOW" {
			continue
		}
		switch policyMatch(*p, r) {
		case triYes:
			return verdict{Status: 200, By: p, Known: true}
		case triUnknown:
			if unknownBy == nil {
				unknownBy = p
			}
		}
	}
	if unknownBy != nil {
		return verdict{Blocker: unknownBy.Ref.Name}
	}
	return verdict{Status: 403, Known: true}
}

// ---- generator -------------------------------------------------------------

type authzGen struct {
	in    AuthzInput
	all   []AuthzPolicy
	suite *Suite
	ids   map[string]bool
	sigs  map[string]bool
	notes map[string]bool
}

func idPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func (g *authzGen) note(s string) {
	if !g.notes[s] {
		g.notes[s] = true
		g.suite.Notes = append(g.suite.Notes, s)
	}
}

func caseSig(c Case) string {
	keys := sortedKeys(c.Request.Headers)
	var hs []string
	for _, k := range keys {
		hs = append(hs, strings.ToLower(k)+"="+c.Request.Headers[k])
	}
	from := ""
	if c.From != nil {
		from = c.From.Namespace + "/" + c.From.ServiceAccount
	}
	if len(c.Expect.Weights) > 0 {
		from += "|weights"
	}
	return strings.Join([]string{c.Request.Method, c.Request.Path, fmt.Sprint(c.Request.Port), fmt.Sprint(c.Request.Direct), c.Request.Host, strings.Join(hs, "&"), from}, "|")
}

func (g *authzGen) add(c Case) {
	c.Generated = true
	if g.ids[c.ID] {
		return
	}
	sig := caseSig(c)
	if g.sigs[sig] {
		return
	}
	g.ids[c.ID], g.sigs[sig] = true, true
	g.suite.Cases = append(g.suite.Cases, c)
}

func (g *authzGen) toCase(id, label, why string, r reqModel, from *CaseFrom, v verdict, rule int) Case {
	headers := map[string]string{}
	for k, val := range r.Headers {
		headers[g.headerCase(k)] = val
	}
	return Case{ID: id, Label: label, Why: why, Rule: rule, From: from,
		Request: CaseRequest{Method: r.Method, Path: r.Path, Headers: headers, Port: r.Port},
		Expect:  CaseExpect{Status: v.Status}}
}

// headerCase restores the header name as the policy wrote it.
func (g *authzGen) headerCase(lower string) string {
	for _, p := range g.all {
		for _, rule := range p.Rules {
			for _, c := range rule.When {
				if n, ok := condHeader(c.Key); ok && n == lower {
					return strings.TrimSuffix(strings.TrimPrefix(c.Key, "request.headers["), "]")
				}
			}
		}
	}
	return lower
}

func (g *authzGen) base() reqModel {
	r := reqModel{Method: "GET", Path: g.in.DefaultPath, Port: g.in.Port, Headers: map[string]string{}}
	if r.Path == "" {
		r.Path = "/"
	}
	if g.in.Source != nil {
		r.Namespace, r.Principal, r.SourceKnown = g.in.Source.Namespace, g.in.Source.Principal, true
	}
	return r
}

// concretize turns a pattern into a string it matches.
func concretize(p string) string {
	switch {
	case p == "*":
		return "0"
	case strings.HasPrefix(p, "*"):
		return "0" + p[1:]
	case strings.HasSuffix(p, "*"):
		return p[:len(p)-1] + "0"
	}
	return p
}

func concretePath(p string) string {
	c := concretize(p)
	if !strings.HasPrefix(c, "/") {
		c = "/" + c
	}
	return c
}

// nonMatching returns a value none of the patterns match.
func nonMatching(patterns []string, candidates ...string) string {
	for _, c := range append(candidates, "other", "flightbay-other", "x-flightbay") {
		if !anyMatch(patterns, c) {
			return c
		}
	}
	return "flightbay-other-value"
}

func firstNotIn(patterns []string, from []string) string {
	for _, m := range from {
		if !anyMatch(patterns, m) {
			return m
		}
	}
	return ""
}

var methodPool = []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}

// apply makes r satisfy rule: it overwrites what the rule constrains and
// leaves the rest. ok is false when no such request can be built.
func (g *authzGen) apply(rule AccessRule, r reqModel) (reqModel, *CaseFrom, bool) {
	r = r.clone()
	var from *CaseFrom
	if len(rule.To) > 0 {
		op := rule.To[0]
		switch {
		case len(op.Methods) > 0 && op.Methods[0] != "*":
			r.Method = concretize(op.Methods[0])
		case len(op.NotMethods) > 0 && anyMatch(op.NotMethods, r.Method):
			if m := firstNotIn(op.NotMethods, methodPool); m != "" {
				r.Method = m
			}
		}
		switch {
		case len(op.Paths) > 0:
			r.Path = concretePath(op.Paths[0])
		case len(op.NotPaths) > 0 && anyMatch(op.NotPaths, r.Path):
			r.Path = nonMatching(op.NotPaths, "/flightbay-other")
		}
		switch {
		case len(op.Ports) > 0:
			if n, err := strconv.Atoi(op.Ports[0]); err == nil {
				r.Port = n
			}
		case len(op.NotPorts) > 0 && anyMatch(op.NotPorts, strconv.Itoa(r.Port)):
			for _, p := range g.in.Ports {
				if !anyMatch(op.NotPorts, strconv.Itoa(p)) {
					r.Port = p
					break
				}
			}
		}
	}
	for _, c := range rule.When {
		name, ok := condHeader(c.Key)
		if !ok {
			return r, nil, false
		}
		switch {
		case len(c.Values) > 0:
			r.Headers[name] = concretize(c.Values[0])
		case len(c.NotValues) > 0:
			if v, present := r.Headers[name]; present && anyMatch(c.NotValues, v) {
				r.Headers[name] = nonMatching(c.NotValues)
			}
		}
	}
	if len(rule.From) > 0 {
		entry := rule.From[0]
		if sourceMatch(entry, r) != triYes {
			var pick *Workload
			for _, e := range rule.From {
				for i := range g.in.Workloads {
					w := g.in.Workloads[i]
					probe := r
					probe.Namespace, probe.Principal, probe.SourceKnown = w.Namespace, w.Principal, true
					if sourceMatch(e, probe) == triYes {
						pick = &g.in.Workloads[i]
						break
					}
				}
				if pick != nil {
					break
				}
			}
			if pick == nil {
				return r, nil, false
			}
			r.Namespace, r.Principal, r.SourceKnown = pick.Namespace, pick.Principal, true
			wref := pick.Ref
			from = &CaseFrom{Namespace: pick.Namespace, ServiceAccount: pick.ServiceAccount, Principal: pick.Principal, Workload: &wref}
		}
	}
	if ruleMatch(rule, r) != triYes {
		return r, nil, false
	}
	return r, from, true
}

func (g *authzGen) ruleLabel(n int, what string) string { return fmt.Sprintf("Rule %d: %s", n, what) }

// verdictOrNote returns the verdict, and notes why a case was dropped when it
// cannot be predicted.
func (g *authzGen) verdictOrNote(r reqModel, why string) (verdict, bool) {
	v := decide(g.all, r)
	if !v.Known {
		g.note(fmt.Sprintf("Not generated: %q, because the result depends on a field of %s that Flightbay cannot evaluate", why, v.Blocker))
		return v, false
	}
	return v, true
}

// GenerateAuthzSuite builds the suite for one AuthorizationPolicy.
func GenerateAuthzSuite(in AuthzInput) *Suite {
	g := &authzGen{in: in, ids: map[string]bool{}, sigs: map[string]bool{}, notes: map[string]bool{}}
	g.all = append([]AuthzPolicy{in.Policy}, in.Others...)
	g.suite = &Suite{Target: in.Policy.Ref, Service: in.Service, Cases: []Case{}, Notes: []string{}, Source: defaultSource(in.Source, in.Service)}
	p := in.Policy

	switch p.Action {
	case "CUSTOM", "AUDIT":
		g.note("Not generated: action " + p.Action + " (an external provider decides, so the outcome cannot be predicted)")
		return g.finish()
	}
	if len(p.Rules) == 0 {
		if p.Action == "ALLOW" {
			// An ALLOW policy with no rules matches nothing: everything is refused.
			r := g.base()
			if v, ok := g.verdictOrNote(r, "Nothing is allowed"); ok {
				g.add(g.toCase("deny-all", "Nothing is allowed", "No rules, so nothing is allowed", r, nil, v, 0))
			}
		} else {
			g.note("Not generated: this DENY policy has no rules, so it never denies anything")
		}
		g.waypointBypass(g.base(), 0)
		return g.finish()
	}

	var firstMatch *reqModel
	firstRule := 0
	for i, rule := range p.Rules {
		n := i + 1
		if bad := p.unsupportedIn(n); len(bad) > 0 {
			for _, f := range bad {
				g.note(fmt.Sprintf("Not generated: %s (rule %d)", f, n))
			}
			continue
		}
		req, from, ok := g.apply(rule, g.base())
		if !ok {
			if len(rule.From) > 0 && sourceMatch(rule.From[0], g.base()) != triYes {
				g.note(fmt.Sprintf("Not generated: no captured workload matches from.source (rule %d)", n))
			} else {
				g.note(fmt.Sprintf("Not generated: could not build a request that matches rule %d", n))
			}
			continue
		}
		if firstMatch == nil {
			cp := req.clone()
			firstMatch, firstRule = &cp, n
		}
		g.matchCases(n, rule, req, from)
		g.nearMisses(n, rule, req)
	}
	if firstMatch != nil {
		g.interactions(*firstMatch, firstRule)
		g.waypointBypass(*firstMatch, firstRule)
	}
	return g.finish()
}

func (g *authzGen) finish() *Suite {
	g.suite.Normalize()
	return g.suite
}

func (g *authzGen) matchCases(n int, rule AccessRule, req reqModel, from *CaseFrom) {
	v, ok := g.verdictOrNote(req, "Rule matches")
	if !ok {
		return
	}
	g.add(g.toCase(fmt.Sprintf("r%d-match", n), g.ruleLabel(n, "request that matches"), "Rule matches", req, from, v, n))
}

func (g *authzGen) nearMisses(n int, rule AccessRule, match reqModel) {
	emit := func(slug, why, label string, r reqModel, from *CaseFrom) {
		if ruleMatch(rule, r) != triNo {
			return
		}
		v, ok := g.verdictOrNote(r, why)
		if !ok {
			return
		}
		g.add(g.toCase(fmt.Sprintf("r%d-%s", n, slug), g.ruleLabel(n, label), why, r, from, v, n))
	}
	for _, c := range rule.When {
		name, ok := condHeader(c.Key)
		if !ok {
			continue
		}
		disp := strings.TrimSuffix(strings.TrimPrefix(c.Key, "request.headers["), "]")
		part := idPart(name)
		if len(c.Values) > 0 {
			other := match.clone()
			other.Headers[name] = nonMatching(c.Values)
			emit("value-"+part, "Different value", "header "+disp+" has a different value", other, nil)

			absent := match.clone()
			delete(absent.Headers, name)
			emit("absent-"+part, "Header absent", "header "+disp+" is missing", absent, nil)

			if swapped := swapCase(match.Headers[name]); swapped != match.Headers[name] && !anyMatch(c.Values, swapped) {
				sw := match.clone()
				sw.Headers[name] = swapped
				emit("case-"+part, "Case of value", "header "+disp+" differs only by letter case (values match exactly)", sw, nil)
			}
		}
		if len(c.NotValues) > 0 && len(c.Values) == 0 {
			bad := match.clone()
			bad.Headers[name] = concretize(c.NotValues[0])
			emit("excluded-"+part, "Excluded value", "header "+disp+" has the excluded value", bad, nil)
		}
	}
	if len(rule.To) > 0 {
		op := rule.To[0]
		if len(op.Methods) > 0 {
			if m := firstNotIn(op.Methods, methodPool); m != "" {
				other := match.clone()
				other.Method = m
				emit("method", "Other method", "method is "+m+" instead of "+strings.Join(op.Methods, " or "), other, nil)
			}
		}
		if len(op.Paths) > 0 {
			other := match.clone()
			other.Path = nonMatching(op.Paths, "/flightbay-other")
			emit("path", "Other path", "path is "+other.Path+" instead of "+strings.Join(op.Paths, " or "), other, nil)
		}
		if len(op.Ports) > 0 {
			for _, p := range g.in.Ports {
				if !anyMatch(op.Ports, strconv.Itoa(p)) {
					other := match.clone()
					other.Port = p
					emit("port", "Other port", fmt.Sprintf("port is %d instead of %s", p, strings.Join(op.Ports, " or ")), other, nil)
					break
				}
			}
		}
	}
	if len(rule.From) > 0 {
		for i := range g.in.Workloads {
			w := g.in.Workloads[i]
			probe := match.clone()
			probe.Namespace, probe.Principal, probe.SourceKnown = w.Namespace, w.Principal, true
			if ruleMatch(rule, probe) == triNo {
				wref := w.Ref
				from := &CaseFrom{Namespace: w.Namespace, ServiceAccount: w.ServiceAccount, Principal: w.Principal, Workload: &wref}
				emit("caller", "Other caller", "caller is "+w.Namespace+"/"+w.ServiceAccount+", which the rule does not name", probe, from)
				break
			}
		}
	}
}

// swapCase changes only the letter case: lower-case values gain a capital
// ("red" to "Red"), anything with capitals is lower-cased.
func swapCase(s string) string {
	if s == "" {
		return s
	}
	if strings.ToLower(s) != s {
		return strings.ToLower(s)
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func policyName(p AuthzPolicy) string { return p.Ref.Name }

// interactions adds one case per other policy on the same target: a request
// the policy under test matches, shaped so the other policy matches or does
// not, whichever shows how the two combine.
func (g *authzGen) interactions(match reqModel, n int) {
	rule := g.in.Policy.Rules[n-1]
	self := g.in.Policy
	for _, other := range g.in.Others {
		if other.Ref == self.Ref || (other.Action != "ALLOW" && other.Action != "DENY") {
			continue
		}
		var cand *reqModel
		var from *CaseFrom
		switch other.Action {
		case "ALLOW":
			cand = g.violating(other, rule, match)
		case "DENY":
			for _, orule := range other.Rules {
				r, f, ok := g.apply(orule, match)
				if ok && ruleMatch(rule, r) == triYes {
					cand, from = &r, f
					break
				}
			}
		}
		if cand == nil {
			continue
		}
		v, ok := g.verdictOrNote(*cand, "interaction with "+policyName(other))
		if !ok {
			continue
		}
		why := fmt.Sprintf("Interaction with %s", policyName(other))
		switch {
		case v.By != nil && v.By.Ref == self.Ref && v.Status == 403:
			why = fmt.Sprintf("%s wins over %s", self.Action, policyName(other))
		case v.By != nil && v.By.Ref == self.Ref:
			why = fmt.Sprintf("Allowed here although %s does not match", policyName(other))
		case v.By != nil && v.Status == 403:
			why = fmt.Sprintf("%s %s refuses it first", other.Action, policyName(other))
		case v.By != nil:
			why = fmt.Sprintf("%s allows it", policyName(other))
		case v.Status == 403:
			why = fmt.Sprintf("No ALLOW matches, including %s", policyName(other))
		}
		g.add(g.toCase("x-"+idPart(other.Ref.Namespace+"-"+other.Ref.Name), "With "+policyName(other)+": "+why, why, *cand, from, v, n))
	}
}

// violating finds a request the policy under test still matches but the
// other policy does not, changing one thing about it.
func (g *authzGen) violating(other AuthzPolicy, rule AccessRule, match reqModel) *reqModel {
	try := func(r reqModel) *reqModel {
		if ruleMatch(rule, r) == triYes && policyMatch(other, r) == triNo {
			return &r
		}
		return nil
	}
	for _, m := range methodPool {
		c := match.clone()
		c.Method = m
		if r := try(c); r != nil {
			return r
		}
	}
	c := match.clone()
	c.Path = "/flightbay-other"
	if r := try(c); r != nil {
		return r
	}
	for _, orule := range other.Rules {
		for _, cond := range orule.When {
			name, ok := condHeader(cond.Key)
			if !ok {
				continue
			}
			c := match.clone()
			c.Headers[name] = nonMatching(append(append([]string{}, cond.Values...), cond.NotValues...))
			if r := try(c); r != nil {
				return r
			}
			c = match.clone()
			delete(c.Headers, name)
			if r := try(c); r != nil {
				return r
			}
		}
	}
	return nil
}

func (g *authzGen) waypointBypass(match reqModel, n int) {
	if g.in.Policy.Enforced != "waypoint" {
		return
	}
	status := 200
	if g.in.WaypointLock {
		// A ztunnel policy refuses anything that is not the waypoint.
		status = 403
	}
	c := g.toCase("bypass", "Straight to a Pod, skipping the waypoint", "Waypoint bypass", match, nil, verdict{Status: status, Known: true}, n)
	c.Request.Direct = true
	g.add(c)
}

// ---- explain ---------------------------------------------------------------

// OtherPolicy is a policy that also decides for the Service.
type OtherPolicy struct {
	Ref      Ref    `json:"ref"`
	Name     string `json:"name"`
	Action   string `json:"action"`
	Summary  string `json:"summary"`
	Enforced string `json:"enforced,omitempty"`
}

// AuthzExplain is the left column of the AuthorizationPolicy Tests tab.
type AuthzExplain struct {
	InWords string        `json:"inWords"`
	Where   string        `json:"where"`
	Others  []OtherPolicy `json:"others"`
	Notes   []string      `json:"notes"`
}

func oxford(xs []string, word string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " " + word + " " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " " + word + " " + xs[len(xs)-1]
}

// RuleInWords is "Deny any request to reviews when header color equals red."
func RuleInWords(p AuthzPolicy, service string) string {
	verb := map[string]string{"ALLOW": "Allow", "DENY": "Deny"}[p.Action]
	if verb == "" {
		return p.Action + " rules are decided by an external provider, so there is no sentence for them."
	}
	if len(p.Rules) == 0 {
		if p.Action == "ALLOW" {
			return "Allow nothing. With no rules, every request to " + service + " is refused."
		}
		return "Deny nothing. With no rules, this policy never refuses a request."
	}
	var sentences []string
	for _, r := range p.Rules {
		var subject string
		var methods, notMethods, paths, notPaths, ports []string
		for _, o := range r.To {
			methods = append(methods, o.Methods...)
			notMethods = append(notMethods, o.NotMethods...)
			paths = append(paths, o.Paths...)
			notPaths = append(notPaths, o.NotPaths...)
			ports = append(ports, o.Ports...)
		}
		switch {
		case len(methods) > 0:
			subject = oxford(methods, "or") + " requests"
		case len(notMethods) > 0:
			subject = "requests that are not " + oxford(notMethods, "or")
		default:
			subject = "any request"
		}
		s := subject + " to " + service
		if len(paths) > 0 {
			s += " for " + oxford(paths, "or")
		}
		if len(notPaths) > 0 {
			s += " except " + oxford(notPaths, "or")
		}
		if len(ports) > 0 {
			s += " on port " + oxford(ports, "or")
		}
		var who []string
		for _, f := range r.From {
			if len(f.Namespaces) > 0 {
				who = append(who, "namespace "+oxford(f.Namespaces, "or"))
			}
			for _, pr := range f.Principals {
				who = append(who, principalName(pr))
			}
			if len(f.NotNamespaces) > 0 {
				who = append(who, "anything outside "+oxford(f.NotNamespaces, "or"))
			}
		}
		if len(who) > 0 {
			s += " from " + oxford(who, "or")
		}
		var when []string
		for _, c := range r.When {
			name := c.Key
			noun := "key "
			if h, ok := condHeader(c.Key); ok {
				name, noun = strings.TrimSuffix(strings.TrimPrefix(c.Key, "request.headers["), "]"), "header "
				_ = h
			}
			if len(c.Values) > 0 {
				w := "equals"
				if len(c.Values) > 1 {
					w = "is one of"
				}
				when = append(when, noun+name+" "+w+" "+oxford(c.Values, "or"))
			}
			if len(c.NotValues) > 0 {
				w := "does not equal"
				if len(c.NotValues) > 1 {
					w = "is none of"
				}
				when = append(when, noun+name+" "+w+" "+oxford(c.NotValues, "or"))
			}
		}
		if len(when) > 0 {
			s += " when " + strings.Join(when, " and ")
		}
		sentences = append(sentences, s)
	}
	if len(sentences) == 1 {
		return verb + " " + sentences[0] + "."
	}
	return verb + " " + strings.Join(sentences, ", or ") + "."
}

// PolicySummary is the short line next to another policy's name.
func PolicySummary(p AuthzPolicy) string {
	if len(p.Rules) == 0 {
		if p.Action == "ALLOW" {
			return "allows nothing"
		}
		return "denies nothing"
	}
	onlyMethods := true
	var methods []string
	for _, r := range p.Rules {
		if len(r.From) > 0 || len(r.When) > 0 || len(r.To) == 0 {
			onlyMethods = false
			break
		}
		for _, o := range r.To {
			if len(o.Methods) == 0 || len(o.NotMethods)+len(o.Paths)+len(o.NotPaths)+len(o.Ports)+len(o.NotPorts)+len(o.Hosts) > 0 {
				onlyMethods = false
			}
			methods = append(methods, o.Methods...)
		}
	}
	if onlyMethods && len(methods) > 0 {
		if p.Action == "DENY" {
			return strings.Join(methods, ", ") + " refused"
		}
		return strings.Join(methods, ", ") + " only"
	}
	var parts []string
	for _, r := range p.Rules {
		if s := ruleSummary(r); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d rules", len(p.Rules))
	}
	return strings.Join(parts, "; ")
}

// ExplainAuthz builds the words, the other policies and the layer note.
func ExplainAuthz(in AuthzInput, suiteNotes []string) AuthzExplain {
	e := AuthzExplain{InWords: RuleInWords(in.Policy, in.Service.Name), Others: []OtherPolicy{}, Notes: append([]string{}, suiteNotes...)}
	switch in.Policy.Enforced {
	case "waypoint":
		if in.Policy.Action == "DENY" {
			e.Where = "Evaluated at the waypoint, before ALLOW policies. Traffic that bypasses the waypoint is not checked."
		} else {
			e.Where = "Evaluated at the waypoint. Traffic that bypasses the waypoint is not checked."
		}
	case "ztunnel":
		e.Where = "Evaluated by ztunnel on the destination node. It can check who the caller is and which port, not methods, paths or headers."
	}
	for _, o := range in.Others {
		e.Others = append(e.Others, OtherPolicy{Ref: o.Ref, Name: o.Ref.Name, Action: o.Action, Summary: PolicySummary(o), Enforced: o.Enforced})
	}
	e.Others = append(e.Others, in.Peer...)
	return e
}

// PredictDecision says which policy Flightbay's own evaluator expects to
// decide a case, and the status it expects. ok is false when a policy uses a
// field the evaluator cannot read.
func PredictDecision(in AuthzInput, c Case) (by *Ref, status int, ok bool) {
	r := reqModel{Method: c.Request.Method, Path: c.Request.Path, Port: c.Request.Port, Headers: map[string]string{}}
	if i := strings.Index(r.Path, "?"); i >= 0 {
		r.Path = r.Path[:i]
	}
	for k, v := range c.Request.Headers {
		r.Headers[strings.ToLower(k)] = v
	}
	switch {
	case c.From != nil:
		r.Namespace, r.Principal, r.SourceKnown = c.From.Namespace, c.From.Principal, true
	case in.Source != nil:
		r.Namespace, r.Principal, r.SourceKnown = in.Source.Namespace, in.Source.Principal, true
	}
	v := decide(append([]AuthzPolicy{in.Policy}, in.Others...), r)
	if !v.Known {
		return nil, 0, false
	}
	if v.By != nil {
		ref := v.By.Ref
		return &ref, v.Status, true
	}
	return nil, v.Status, true
}

// ---- workspace adapter -----------------------------------------------------

func defaultProbePath(ws *store.Workspace, svc *store.Object) string {
	switch svc.Name {
	case "reviews", "ratings", "details":
		for _, p := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
			if p.Labels()["flightbay.dev/demo"] == "bookinfo" || p.Labels()["app"] == svc.Name {
				return "/" + svc.Name + "/0"
			}
		}
	}
	return "/"
}

func servicePorts(svc *store.Object) []int {
	out := []int{}
	for _, v := range L(Path(svc.Obj, "spec", "ports")) {
		if n := N(M(v)["port"]); n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// CaptureWorkloads lists the callers a generator can choose from: Running
// application Pods, one per namespace and service account.
func CaptureWorkloads(ws *store.Workspace, cluster string) []Workload {
	td := clusterTrustDomain(ws, cluster)
	if td == "" {
		td = "cluster.local"
	}
	seen := map[string]bool{}
	out := []Workload{}
	pods := ws.List(cluster, "", "pods", "", nil)
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].Namespace+"/"+pods[i].Name < pods[j].Namespace+"/"+pods[j].Name
	})
	nsMesh := map[string]bool{}
	for _, p := range pods {
		if _, ok := nsMesh[p.Namespace]; !ok {
			n := ws.Get(cluster, "", "namespaces", "", p.Namespace)
			nsMesh[p.Namespace] = n != nil && n.Labels()["istio.io/dataplane-mode"] == "ambient"
		}
		lb := p.Labels()
		if S(Path(p.Obj, "status", "phase")) != "Running" || lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["flightbay.dev/purpose"] == "http-test" || lb["flightbay.dev/purpose"] != "" && lb["app"] == "flightbay-http" {
			continue
		}
		if strings.HasPrefix(p.Namespace, "kube-") || p.Namespace == "istio-system" {
			continue
		}
		sa := defaultString(S(Path(p.Obj, "spec", "serviceAccountName")), "default")
		key := p.Namespace + "/" + sa
		curl := false
		for _, v := range L(Path(p.Obj, "spec", "containers")) {
			img := S(M(v)["image"])
			curl = curl || strings.Contains(img, "curlimages/curl")
		}
		inMesh := nsMesh[p.Namespace] || S(Path(p.Obj, "metadata", "annotations", "ambient.istio.io/redirection")) == "enabled"
		if seen[key] {
			// Prefer a Pod that can send requests for the same identity.
			if curl {
				for i := range out {
					if out[i].Namespace+"/"+out[i].ServiceAccount == key && !out[i].Curl {
						out[i].Ref, out[i].Curl = reference(p), true
					}
				}
			}
			continue
		}
		seen[key] = true
		out = append(out, Workload{Ref: reference(p), Namespace: p.Namespace, ServiceAccount: sa, Principal: td + "/ns/" + p.Namespace + "/sa/" + sa, InMesh: inMesh, Curl: curl})
	}
	return out
}

// pickSource prefers a client Pod already in the mesh. With none, requests
// come from a temporary Pod running as the namespace's default account.
func pickSource(ws *store.Workspace, cluster string, svc *store.Object, workloads []Workload) *Workload {
	for i := range workloads {
		if workloads[i].Curl && workloads[i].InMesh {
			return &workloads[i]
		}
	}
	for i := range workloads {
		if workloads[i].Curl {
			return &workloads[i]
		}
	}
	td := clusterTrustDomain(ws, cluster)
	if td == "" {
		td = "cluster.local"
	}
	n := ws.Get(cluster, "", "namespaces", "", svc.Namespace)
	return &Workload{Ref: Ref{Cluster: cluster, Resource: "pods", Kind: "Pod", Namespace: svc.Namespace}, Namespace: svc.Namespace, ServiceAccount: "default",
		Principal: td + "/ns/" + svc.Namespace + "/sa/default", InMesh: n != nil && n.Labels()["istio.io/dataplane-mode"] == "ambient"}
}

// policyEnforcedAt reads where a raw policy is enforced.
func policyEnforcedAt(spec map[string]any) string {
	if len(L(spec["targetRefs"])) > 0 || M(spec["targetRef"]) != nil {
		return "waypoint"
	}
	return "ztunnel"
}

func objectRef(obj map[string]any, cluster, kind string, fallbackNS string) Ref {
	meta := M(obj["metadata"])
	ref, _ := SuiteRef(cluster, kind, defaultString(S(meta["namespace"]), fallbackNS), S(meta["name"]))
	if ref.Name == "" {
		ref = Ref{Cluster: cluster, Kind: kind, Namespace: defaultString(S(meta["namespace"]), fallbackNS), Name: S(meta["name"])}
		if kind == KindAuthorizationPolicy {
			ref.Group, ref.Resource = SecurityGroup, "authorizationpolicies"
		} else {
			ref.Group, ref.Resource = GatewayGroup, "httproutes"
		}
	}
	return ref
}

// ServiceForPolicy finds the Service an AuthorizationPolicy is tested
// against: the one a targetRef names, or the first Service behind the
// waypoint or matching the selector.
func ServiceForPolicy(ws *store.Workspace, cluster, ns string, spec map[string]any) *store.Object {
	targets := L(spec["targetRefs"])
	if tr := M(spec["targetRef"]); tr != nil {
		targets = append(targets, tr)
	}
	services := ws.List(cluster, "", "services", "", nil)
	sort.Slice(services, func(i, j int) bool {
		return services[i].Namespace+"/"+services[i].Name < services[j].Namespace+"/"+services[j].Name
	})
	for _, t := range targets {
		tm := M(t)
		switch S(tm["kind"]) {
		case "Service":
			if s := ws.Get(cluster, "", "services", ns, S(tm["name"])); s != nil {
				return s
			}
		case "Gateway":
			for _, s := range services {
				if wn, w, _ := Waypoint(ws, s); w == S(tm["name"]) && wn == ns {
					return s
				}
			}
		}
	}
	if sel := M(Path(spec, "selector", "matchLabels")); len(sel) > 0 {
		for _, s := range services {
			if s.Namespace == ns && matches(sel, stringMap(M(Path(s.Obj, "spec", "selector")))) {
				return s
			}
		}
	}
	return nil
}

// AuthzInputFor builds the generator input for a policy object (saved or a
// draft) against a Service. drafts override or add policies from a plan.
func AuthzInputFor(ws *store.Workspace, cluster string, obj map[string]any, svc *store.Object, drafts []map[string]any) (*AuthzInput, error) {
	if svc == nil {
		return nil, fmt.Errorf("could not tell which Service %s applies to", S(M(obj["metadata"])["name"]))
	}
	spec := M(obj["spec"])
	ref := objectRef(obj, cluster, KindAuthorizationPolicy, svc.Namespace)
	in := &AuthzInput{Policy: ParseAuthzPolicy(ref, spec, policyEnforcedAt(spec)), Service: reference(svc)}
	in.Ports = servicePorts(svc)
	if len(in.Ports) > 0 {
		in.Port = in.Ports[0]
	}
	in.DefaultPath = defaultProbePath(ws, svc)
	in.Workloads = CaptureWorkloads(ws, cluster)
	in.Source = pickSource(ws, cluster, svc, in.Workloads)
	draftByKey := map[string]map[string]any{}
	for _, d := range drafts {
		if S(d["kind"]) == KindAuthorizationPolicy {
			r := objectRef(d, cluster, KindAuthorizationPolicy, svc.Namespace)
			draftByKey[r.Namespace+"/"+r.Name] = d
		}
	}
	seen := map[string]bool{ref.Namespace + "/" + ref.Name: true}
	for _, e := range servicePolicies(ws, svc) {
		if e.Ref.Kind != KindAuthorizationPolicy || e.Ref.Group != SecurityGroup {
			continue
		}
		key := e.Ref.Namespace + "/" + e.Ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		var raw map[string]any
		if d, ok := draftByKey[key]; ok {
			raw = d
		} else if o := ws.Get(cluster, SecurityGroup, "authorizationpolicies", e.Ref.Namespace, e.Ref.Name); o != nil {
			raw = o.Obj
		}
		if raw != nil {
			in.Others = append(in.Others, ParseAuthzPolicy(e.Ref, M(raw["spec"]), e.Enforced))
		}
	}
	wn, wp, _ := Waypoint(ws, svc)
	for key, d := range draftByKey {
		if seen[key] {
			continue
		}
		dspec := M(d["spec"])
		for _, t := range L(dspec["targetRefs"]) {
			tm := M(t)
			dns := defaultString(S(M(d["metadata"])["namespace"]), svc.Namespace)
			if (S(tm["kind"]) == "Service" && S(tm["name"]) == svc.Name && dns == svc.Namespace) || (S(tm["kind"]) == "Gateway" && wp != "" && S(tm["name"]) == wp && dns == wn) {
				in.Others = append(in.Others, ParseAuthzPolicy(objectRef(d, cluster, KindAuthorizationPolicy, svc.Namespace), dspec, "waypoint"))
				break
			}
		}
	}
	sort.SliceStable(in.Others, func(i, j int) bool { return in.Others[i].Ref.Name < in.Others[j].Ref.Name })
	for _, e := range servicePolicies(ws, svc) {
		if e.Ref.Kind != "PeerAuthentication" {
			continue
		}
		mode := "PERMISSIVE"
		if o := ws.Get(cluster, SecurityGroup, "peerauthentications", e.Ref.Namespace, e.Ref.Name); o != nil {
			mode = defaultString(S(Path(o.Obj, "spec", "mtls", "mode")), "PERMISSIVE")
		}
		name := e.Ref.Name
		if e.Ref.Namespace != svc.Namespace {
			name = e.Ref.Namespace + "/" + e.Ref.Name
		}
		in.Peer = append(in.Peer, OtherPolicy{Ref: e.Ref, Name: name, Action: "mTLS", Summary: mode, Enforced: e.Enforced})
	}
	in.WaypointLock = hasWaypointLock(ws, svc, append([]AuthzPolicy{in.Policy}, in.Others...))
	return in, nil
}

// hasWaypointLock reports a ztunnel ALLOW policy that admits only the
// waypoint's identity.
func hasWaypointLock(ws *store.Workspace, svc *store.Object, policies []AuthzPolicy) bool {
	principal := waypointPrincipal(ws, svc)
	if principal == "" {
		return false
	}
	for _, p := range policies {
		if p.Action != "ALLOW" || p.Enforced != "ztunnel" || len(p.Rules) != 1 {
			continue
		}
		r := p.Rules[0]
		if len(r.To) == 0 && len(r.When) == 0 && len(r.From) == 1 && len(r.From[0].Principals) == 1 && r.From[0].Principals[0] == principal {
			return true
		}
	}
	return false
}

// defaultSource is the workload the suite is sent from, or a temporary Pod in
// the Service's namespace when no client Pod can be picked.
func defaultSource(w *Workload, svc Ref) SuiteSource {
	if w != nil && w.Curl {
		return SuiteSource{Kind: SourceWorkload, Ref: w.Ref}
	}
	return SuiteSource{Kind: SourceEphemeral, Ref: Ref{Cluster: svc.Cluster, Resource: "pods", Kind: "Pod", Namespace: svc.Namespace}}
}
