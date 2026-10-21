package ambient

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Interaction is something the planned change does together with what is
// already configured. Level is "info" or "warn". When FollowUpTask is set the
// UI offers a button (FollowUpLabel) that opens that task.
type Interaction struct {
	Level         string `json:"level"`
	Text          string `json:"text"`
	FollowUpTask  string `json:"followUpTask,omitempty"`
	FollowUpLabel string `json:"followUpLabel,omitempty"`
}

// AnnotatePlan fills p.Effects and p.Interactions. It is a no-op for plans
// that are not Service configuration (demo, scorecard and explain repairs).
func AnnotatePlan(ws *store.Workspace, p *Plan) {
	if p == nil {
		return
	}
	p.Effects, p.Interactions = Effects(ws, p.Request, p)
}

var standardMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

// l7Tasks only take effect when traffic passes through a waypoint.
var l7Tasks = map[string]bool{"methods": true, "access-rule": true, "jwt": true, "rate-limit": true, "weighted-routing": true, "timeouts": true, "retries": true, "mirroring": true, "header-routing": true, "load-balancing": true, "endpoint-failover": true, "locality-failover": true, "circuit-breaker": true}

// Effects describes, in plain sentences, what the request does to the
// Service, and how it interacts with the configuration already captured. p
// may be nil; when set its changes are used to describe edits to routes.
func Effects(ws *store.Workspace, req Request, p *Plan) (effects []string, interactions []Interaction) {
	effects, interactions = []string{}, []Interaction{}
	if ws == nil || req.Demo != nil || req.Explain != nil || req.Scorecard != nil {
		return
	}
	if contains(req.Tasks, "mesh-strict-mtls") {
		effects = append(effects, "Sets the cluster-wide mesh default to STRICT mTLS in istio-system. More-specific namespace, workload and port policies remain effective.")
		if p != nil {
			for _, note := range p.Notes {
				if strings.Contains(note, "override") {
					interactions = append(interactions, Interaction{Level: "warn", Text: note})
				}
			}
		}
		return
	}
	svc := ws.Get(req.Cluster, "", "services", req.Namespace, req.Service)
	if svc == nil {
		if req.Waypoint != nil && p != nil {
			effects = append(effects, "Sets the selected namespace defaults to waypoint "+req.Waypoint.Name+". Preserved Service overrides remain effective.")
			for _, note := range p.Notes {
				if strings.Contains(note, "downstream ztunnel") {
					interactions = append(interactions, Interaction{Level: "warn", Text: note})
				}
			}
		}
		return
	}
	e := &effectBuilder{ws: ws, req: req, svc: svc, plan: p, policies: servicePolicies(ws, svc)}
	_, e.wpName, e.wp = Waypoint(ws, svc)
	for _, task := range req.Tasks {
		e.task(task)
	}
	e.interact()
	return e.effects, e.interactions
}

type effectBuilder struct {
	ws           *store.Workspace
	req          Request
	svc          *store.Object
	plan         *Plan
	policies     []PolicyEntry
	wpName       string
	wp           *store.Object
	effects      []string
	interactions []Interaction
}

func (e *effectBuilder) say(format string, args ...any) {
	e.effects = append(e.effects, fmt.Sprintf(format, args...))
}

func (e *effectBuilder) tell(level, text string) {
	e.interactions = append(e.interactions, Interaction{Level: level, Text: text})
}

func (e *effectBuilder) has(task string) bool { return contains(e.req.Tasks, task) }

// plain turns the <code> markup used by the answer sentences into backticks.
func plain(s string) string {
	s = strings.NewReplacer("<code>", "`", "</code>", "`").Replace(s)
	return html.UnescapeString(s)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func andList(xs []string) string { return joinWords(xs, "and") }

func verbFor(n int, plural, singular string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func without(all, drop []string) []string {
	out := []string{}
	for _, m := range all {
		if !contains(drop, m) {
			out = append(out, m)
		}
	}
	return out
}

func upperAll(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, strings.ToUpper(x))
	}
	return out
}

func (e *effectBuilder) task(task string) {
	r, s := e.req, e.req.Service
	switch task {
	case "methods":
		e.methods()
	case "access-rule":
		e.accessRule()
	case "weighted-routing":
		e.weighted()
	case "timeouts":
		if r.Timeout == "" {
			return
		}
		if r.Timeout == "0s" || r.Timeout == "0" {
			e.say("The request timeout is disabled, so slow requests wait for the backend.")
			return
		}
		e.say("Requests that take longer than %s fail with 504.", r.Timeout)
		if r.BackendTimeout != "" {
			e.say("Each attempt to a backend is cut off after %s.", r.BackendTimeout)
		}
	case "retries":
		e.say("A failed request to %s is retried up to %d %s before the caller sees the error.", s, r.RetryAttempts, plural(r.RetryAttempts, "time", "times"))
		e.say("The attempts happen in the waypoint, so callers need no change.")
	case "mirroring":
		if r.Mirror != nil {
			e.say("Every request to %s is also sent to %s.", s, r.Mirror.Name)
			e.say("The mirror's responses are dropped, so callers only see what %s answers.", s)
		}
	case "header-routing":
		e.headerRouting()
	case "load-balancing":
		switch r.Algorithm {
		case "ROUND_ROBIN":
			e.say("Each waypoint sends requests to the endpoints of %s in turn.", s)
		case "LEAST_REQUEST":
			e.say("Each waypoint sends the next request to the endpoint of %s with the fewest active requests.", s)
		case "RANDOM":
			e.say("Each waypoint picks an endpoint of %s at random for every request.", s)
		default:
			e.say("The load-balancing algorithm for %s is left as it is.", s)
		}
	case "endpoint-failover", "locality-failover":
		n, pct := r.FailureThreshold, r.MaxEjectionPercent
		if n == 0 {
			n = 3
		}
		if pct == 0 {
			pct = 100
		}
		dur := defaultString(r.EjectionDuration, "30s")
		e.say("An endpoint of %s that returns %d 5xx errors in a row is taken out of rotation for %s.", s, n, dur)
		if pct < 100 {
			e.say("At most %d%% of the endpoints can be out at once.", pct)
		}
		if task == "locality-failover" && r.FromRegion != "" && r.ToRegion != "" {
			e.say("When endpoints in %s fail, requests go to %s.", r.FromRegion, r.ToRegion)
		}
	case "circuit-breaker":
		if b := r.CircuitBreaker; b != nil {
			e.say("Each waypoint allows %d concurrent requests and %d pending requests to %s.", b.MaxRequests, b.MaxPendingRequests, s)
			e.say("Requests over the limit get 503 straight away instead of queueing.")
		}
	case "rate-limit":
		unit := strings.ToLower(strings.TrimSuffix(r.Unit, "s"))
		e.say("Requests to %s above %d per %s are refused with 429.", s, r.Requests, unit)
		e.say("Each waypoint proxy counts for itself, so the limit is not shared between replicas.")
	case "strict-mtls":
		if r.MTLSMode == "PERMISSIVE" {
			e.say("%s accepts both mTLS and plain-text callers.", s)
			e.say("Callers outside the mesh keep working.")
		} else {
			e.say("Callers outside the mesh are refused.")
			e.say("Callers in the mesh already use mTLS, so they keep working.")
		}
	case "l4-default-deny":
		e.say("Callers are refused unless another ALLOW policy admits them.")
		e.say("This applies to every Service that shares the Pods of %s.", s)
	case "l4-allow-namespace":
		e.say("Callers in %s can reach %s.", defaultString(r.SourceNamespace, "the chosen namespace"), s)
		e.say("Once an ALLOW exists on these Pods, callers it does not match are refused.")
	case "l4-allow-service-account":
		e.say("This L4 ALLOW admits service account %s without a waypoint. Other ALLOW policies remain additive.", r.SourceServiceAccount)
	case "require-waypoint":
		e.say("The waypoint can call the Pods of %s. Other ALLOW policies remain additive.", s)
		e.say("Direct Pod-IP callers are refused only when no other ALLOW rule admits them. Remove or narrow conflicting caller-allowing rules.")
		for _, pe := range e.policies {
			if pe.Action == "ALLOW" && pe.Enforced == "ztunnel" {
				e.tell("warn", "Review conflicting ALLOW policy "+pe.Ref.Namespace+"/"+pe.Ref.Name+"; remove or narrow rules that admit direct callers.")
			}
		}
	case "jwt":
		e.say("This ALLOW branch admits JWT subjects %s from %s.", strings.Join(r.RequestPrincipals, ", "), r.Issuer)
		if len(r.JWTPrincipals) > 0 {
			e.say("The same request must also have source principal %s.", strings.Join(r.JWTPrincipals, ", "))
		}
		if len(r.JWTServiceAccounts) > 0 {
			e.say("The same request must also come from service account %s.", strings.Join(r.JWTServiceAccounts, ", "))
		}
		e.say("The policy targets all requests to %s through the waypoint, constrained by explicit matches: paths %s; methods %s; headers %s.", s, defaultString(strings.Join(r.Paths, ", "), "any"), defaultString(strings.Join(r.Methods, ", "), "any"), stringJSON(r.Headers))
		e.say("Requests without a matching validated JWT cannot use this ALLOW branch. Other ALLOW rules may admit them; DENY rules still run first. Invalid tokens are rejected by matching RequestAuthentication.")
	case "global-service":
		e.globalService()
	case "waypoint":
		e.waypoint()
	case "enrol-namespace":
		e.say("Pods in %s join the ambient mesh: their traffic gets mTLS from ztunnel.", r.Namespace)
		e.say("The Pods do not restart and nothing is injected.")
	}
}

func (e *effectBuilder) methods() {
	r := e.req
	ms := upperAll(r.Methods)
	if len(ms) == 0 {
		return
	}
	if defaultString(r.MethodAction, "ALLOW") == "DENY" {
		e.say("%s %s 403 from the waypoint.", andListComma(ms), verbFor(len(ms), "return", "returns"))
		e.say("Everything else still works.")
		return
	}
	e.say("%s %s working.", andList(ms), verbFor(len(ms), "keep", "keeps"))
	if blocked := without(standardMethods, ms); len(blocked) > 0 {
		e.say("%s %s 403 from the waypoint.", strings.Join(blocked, ", "), verbFor(len(blocked), "return", "returns"))
	}
	e.existingDenies()
}

// andListComma keeps method lists as "POST, PUT, PATCH, DELETE".
func andListComma(xs []string) string {
	if len(xs) <= 2 {
		return andList(xs)
	}
	return strings.Join(xs, ", ")
}

// accessPhrases describes the request's rules without markup.
func accessPhrases(a *AccessRequest, deny bool) []string {
	if a == nil {
		return nil
	}
	rules := make([]AccessRule, 0, len(a.Rules))
	for _, r := range a.Rules {
		rules = append(rules, r)
	}
	out := []string{}
	for _, p := range phrases(rules, deny) {
		out = append(out, plain(p))
	}
	return out
}

func (e *effectBuilder) accessRule() {
	a := e.req.Access
	if a == nil {
		return
	}
	action := defaultString(strings.ToUpper(a.Action), "ALLOW")
	ps := accessPhrases(a, action == "DENY")
	switch action {
	case "DENY":
		if len(ps) == 0 {
			e.say("Requests that match your rules get 403 from the waypoint.")
		} else {
			e.say("%s get 403 from the waypoint.", upperFirst(andList(ps)))
		}
		e.say("Everything else still works.")
	case "ALLOW":
		if len(ps) == 0 {
			e.say("Only requests that match your rules are allowed. Everything else gets 403.")
		} else {
			e.say("Only %s can reach %s.", joinWords(ps, "or"), e.req.Service)
			e.say("Everything else gets 403 from the waypoint.")
		}
	default:
		e.say("Matching requests are passed to the %s check. Nothing is blocked by this policy alone.", action)
	}
	if action == "ALLOW" {
		e.existingDenies()
	}
}

// existingDenies names each enforced DENY, because DENY is evaluated before ALLOW.
func (e *effectBuilder) existingDenies() {
	n := 0
	for _, pe := range e.policies {
		if pe.Action != "DENY" || pe.Warning != "" || pe.Enforced != "waypoint" || pe.Ref.Kind == "" {
			continue
		}
		if n++; n > 3 {
			return
		}
		o := e.ws.Get(pe.Ref.Cluster, pe.Ref.Group, pe.Ref.Resource, pe.Ref.Namespace, pe.Ref.Name)
		what := ""
		if o != nil && o.Kind == "AuthorizationPolicy" {
			if acc, ok := accessFromPolicy(o); ok && acc != nil {
				if ps := accessPhrases(acc, true); len(ps) > 0 {
					what = upperFirst(joinWords(ps, "or"))
				}
			}
		}
		if what == "" {
			what = "Requests that DENY policy " + pe.Ref.Name + " matches"
		}
		e.say("%s still get 403: your existing DENY runs first.", what)
	}
}

func (e *effectBuilder) weighted() {
	r := e.req
	if len(r.Backends) == 0 {
		return
	}
	names := make([]string, 0, len(r.Backends))
	for _, b := range r.Backends {
		names = append(names, b.Name)
	}
	switch defaultString(r.TrafficMode, "weighted") {
	case "equal":
		e.say("Requests are shared equally between %s.", andList(names))
	case "default":
		e.say("Requests go to %s as before.", r.Service)
	default:
		total := 0
		for _, b := range r.Backends {
			total += b.Weight
		}
		parts := []string{}
		for i, b := range r.Backends {
			if total <= 0 {
				break
			}
			pct := (b.Weight*100 + total/2) / total
			if i == 0 {
				parts = append(parts, fmt.Sprintf("%d%% of requests go to %s", pct, b.Name))
			} else {
				parts = append(parts, fmt.Sprintf("%d%% to %s", pct, b.Name))
			}
		}
		if len(parts) > 0 {
			e.say("%s.", strings.Join(parts, ", "))
		}
	}
	for _, b := range r.Backends {
		if b.Weight == 0 && defaultString(r.TrafficMode, "weighted") == "weighted" {
			e.say("%s gets no traffic until its weight is above zero.", b.Name)
		}
	}
}

func (e *effectBuilder) headerRouting() {
	r := e.req
	who := ""
	if len(r.Backends) > 0 {
		who = r.Backends[0].Name
	}
	var conds []string
	if m := r.RouteMatch; m != nil {
		if m.Path != "" {
			switch m.PathType {
			case "Exact":
				conds = append(conds, "path `"+m.Path+"`")
			case "RegularExpression":
				conds = append(conds, "a path matching `"+m.Path+"`")
			default:
				conds = append(conds, "a path starting `"+m.Path+"`")
			}
		}
		if m.Method != "" {
			conds = append(conds, "method is "+m.Method)
		}
		for _, h := range m.Headers {
			conds = append(conds, "`"+h.Name+": "+h.Value+"`")
		}
		for _, q := range m.Query {
			conds = append(conds, "query `"+q.Name+"="+q.Value+"`")
		}
	} else {
		keys := []string{}
		for k := range r.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			conds = append(conds, "`"+k+": "+r.Headers[k]+"`")
		}
	}
	if len(conds) == 0 || who == "" {
		return
	}
	e.say("Requests with %s go to %s.", andList(conds), who)
	e.say("Everything else still goes to %s.", r.Service)
}

func (e *effectBuilder) globalService() {
	r, s := e.req, e.req.Service
	switch {
	case r.Unpublish:
		e.say("%s stops being shared with other clusters.", s)
		e.say("Callers in this cluster are not affected.")
	case len(r.GlobalWeights) > 0:
		total := 0
		names := []string{}
		for c, w := range r.GlobalWeights {
			total += w
			names = append(names, c)
		}
		sort.Strings(names)
		parts := []string{}
		for _, c := range names {
			if total > 0 {
				parts = append(parts, fmt.Sprintf("%d%% to %s", (r.GlobalWeights[c]*100+total/2)/total, c))
			}
		}
		e.say("Requests to %s are split across clusters: %s.", s, strings.Join(parts, ", "))
	case r.GlobalDistribution != "":
		if r.GlobalDistribution == "default" {
			e.say("Requests to %s prefer endpoints on the caller's network.", s)
		} else {
			e.say("The endpoints of %s are chosen with %s across clusters.", s, r.GlobalDistribution)
		}
	case r.GlobalTakeover != nil:
		if *r.GlobalTakeover {
			e.say("The plain name %s.%s resolves to the global Service as well, so callers need no change.", s, r.Namespace)
		} else {
			e.say("The plain name %s.%s goes back to the local Service only.", s, r.Namespace)
		}
	default:
		e.say("Endpoints of %s become reachable from the other connected clusters.", s)
		e.say("Callers in this cluster keep using the local Service.")
	}
}

func (e *effectBuilder) waypoint() {
	w := e.req.Waypoint
	if w == nil {
		return
	}
	name := defaultString(w.Name, "the waypoint")
	switch w.Scope {
	case "namespace":
		e.say("Every Service in %s sends its traffic through %s.", e.req.Namespace, name)
	case "shared":
		e.say("Services in %s share %s.", andList(w.Namespaces), name)
	default:
		e.say("%s sends its traffic through %s.", defaultString(e.req.TargetName, e.req.Service), name)
	}
	e.say("L7 policies and routes now apply to that traffic.")
	if e.plan != nil {
		for _, note := range e.plan.Notes {
			if strings.Contains(note, "downstream ztunnel") {
				e.tell("warn", note)
			}
		}
	}
}

// ---- interactions ---------------------------------------------------------

func (e *effectBuilder) interact() {
	r, s := e.req, e.req.Service
	l7 := false
	for _, t := range r.Tasks {
		l7 = l7 || l7Tasks[t]
	}
	policy := e.has("methods") || e.has("access-rule") || e.has("jwt") || e.has("rate-limit")

	// The Service does not use a waypoint, so its waypoint rules are not applied.
	if l7 && e.wpName == "" {
		e.interactions = append(e.interactions, Interaction{Level: "warn", Text: s + " does not use a waypoint, so this " + noun(policy) + " is not applied to its traffic. Set up a waypoint for it first.", FollowUpTask: "waypoint", FollowUpLabel: "Set up a waypoint"})
	} else if l7 && e.wp == nil {
		e.tell("warn", fmt.Sprintf("%s points at the waypoint %s, but that Gateway was not captured, so this %s may not be applied.", s, e.wpName, noun(policy)))
	}
	if e.has("jwt") {
		for _, pe := range e.policies {
			if pe.Action == "ALLOW" && pe.Enforced == "waypoint" {
				e.tell("warn", "Review additive ALLOW policy "+pe.Ref.Namespace+"/"+pe.Ref.Name+"; requests without a matching JWT may use its other branches.")
			}
		}
	}

	// First ALLOW on the Service.
	if r.Edit == nil && e.newAllow() && !e.hasWaypointAllow() {
		e.tell("warn", fmt.Sprintf("This is the first ALLOW on %s. Once it exists, anything it does not match is denied. You do not need \"Deny by default\" as well for L7 traffic.", s))
	}

	// L7 policy without the ztunnel bypass block.
	if policy && e.wpName != "" && !e.has("require-waypoint") && !e.bypassBlocked() {
		// The drawer explains it when require-waypoint is not available for this Service.
		i := Interaction{Level: "info", Text: "Callers that reach pod IPs directly skip the waypoint and skip this rule.", FollowUpTask: "require-waypoint", FollowUpLabel: `Also add "Block traffic that skips the waypoint"`}
		e.interactions = append(e.interactions, i)
	}

	e.routing()
}

func noun(policy bool) string {
	if policy {
		return "policy"
	}
	return "route"
}

// newAllow is true when the request adds an ALLOW that is evaluated at the waypoint.
func (e *effectBuilder) newAllow() bool {
	if e.has("methods") && defaultString(e.req.MethodAction, "ALLOW") == "ALLOW" {
		return true
	}
	if e.has("access-rule") && e.req.Access != nil && defaultString(strings.ToUpper(e.req.Access.Action), "ALLOW") == "ALLOW" {
		return true
	}
	return false
}

func (e *effectBuilder) hasWaypointAllow() bool {
	for _, pe := range e.policies {
		if pe.Action == "ALLOW" && pe.Enforced == "waypoint" && pe.Warning == "" && pe.Ref.Kind != "RequestAuthentication" {
			return true
		}
	}
	return false
}

// bypassBlocked reports an enforced ztunnel ALLOW that admits only the waypoint identity.
func (e *effectBuilder) bypassBlocked() bool {
	return serviceWaypointLock(e.ws, e.svc)
}

func (e *effectBuilder) routing() {
	r, s := e.req, e.req.Service
	routed := e.has("weighted-routing") || e.has("timeouts") || e.has("retries") || e.has("mirroring") || e.has("header-routing")
	if !routed {
		return
	}
	// Adding a rule to a route that already exists.
	if e.plan != nil {
		for _, c := range e.plan.Changes {
			if c.Ref.Kind != "HTTPRoute" {
				continue
			}
			for _, f := range c.Fields {
				if f.Path == "/spec/rules" && len(L(f.After)) > len(L(f.Before)) {
					e.say("Adds rule %d to %s.", len(L(f.After)), c.Ref.Name)
				}
			}
		}
	}
	if r.Edit != nil && r.Edit.Ref.Kind == "HTTPRoute" {
		if o := Target(e.ws, r.Cluster, "HTTPRoute", r.Edit.Ref.Namespace, r.Edit.Ref.Name); o != nil && r.Edit.Rule >= len(L(Path(o.Obj, "spec", "rules"))) {
			e.say("Adds rule %d to %s.", r.Edit.Rule+1, o.Name)
		}
	}
	// A new route next to routes that already attach to this Service.
	if r.Edit == nil && e.plan != nil {
		created := map[string]bool{}
		for _, o := range e.plan.Objects {
			if S(o["kind"]) == "HTTPRoute" {
				created[S(Path(o, "metadata", "name"))] = true
			}
		}
		if len(created) > 0 {
			wpNS, _, _ := Waypoint(e.ws, e.svc)
			for _, o := range e.ws.List(r.Cluster, GatewayGroup, "httproutes", r.Namespace, nil) {
				if created[o.Name] || !related(o, e.svc, wpNS, e.wpName) {
					continue
				}
				e.tell("warn", fmt.Sprintf("The route %s/%s already applies to %s. If both match a request, the more specific match wins, then the older route, so the existing route may take precedence.", o.Namespace, o.Name, s))
			}
		}
	}
	// Backends that cannot take traffic.
	names := []string{}
	for _, b := range r.Backends {
		names = append(names, b.Name)
	}
	if r.Mirror != nil {
		names = append(names, r.Mirror.Name)
	}
	if hasResource(e.ws, r.Cluster, "discovery.k8s.io", "endpointslices") {
		for _, n := range names {
			if n != s || len(names) == 1 {
				if backend := e.ws.Get(r.Cluster, "", "services", r.Namespace, n); backend != nil && readyEndpoints(e.ws, backend) == 0 {
					e.tell("warn", fmt.Sprintf("%s has no ready endpoints, so requests sent to it will fail.", n))
				}
			}
		}
	}
}

// readyEndpoints counts the endpoints of a Service that are not marked not-ready.
func readyEndpoints(ws *store.Workspace, svc *store.Object) int {
	n := 0
	for _, ep := range ws.List(svc.Cluster, "discovery.k8s.io", "endpointslices", svc.Namespace, nil) {
		if ep.Labels()["kubernetes.io/service-name"] != svc.Name {
			continue
		}
		for _, v := range L(ep.Obj["endpoints"]) {
			ready, set := M(M(v)["conditions"])["ready"]
			if !set || ready != false {
				n++
			}
		}
	}
	return n
}
