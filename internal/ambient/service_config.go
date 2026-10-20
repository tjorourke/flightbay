package ambient

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// ConfigResource is one item row on the Configuration tab: a captured
// resource (or one rule of it) that sets up a task for this Service.
type ConfigResource struct {
	Ref            Ref    `json:"ref"`
	Rule           int    `json:"rule"`
	Summary        string `json:"summary"`
	Label          string `json:"label"`
	Action         string `json:"action"` // the pill: DENY, ALLOW, Split, Inherited ...
	State          string `json:"state"`  // Accepted, Captured, Not enforced
	EditKey        string `json:"editKey"`
	Editable       bool   `json:"editable"`
	ReadOnlyReason string `json:"readOnlyReason,omitempty"`
	ManagedBy      string `json:"managedBy"`
	// Scope is how the resource reaches the Service: service, waypoint,
	// workload, namespace or mesh. Namespace and mesh are inherited.
	Scope string `json:"scope,omitempty"`
	// Group places an "Other configuration" row in the closest card.
	Group string `json:"group,omitempty"`
}

type ConfigTask struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	ShortLabel  string           `json:"shortLabel"`
	Group       string           `json:"group"`
	Layer       string           `json:"layer"`
	Status      string           `json:"status"`
	Reason      string           `json:"reason,omitempty"`
	ShortReason string           `json:"shortReason,omitempty"`
	Resources   []ConfigResource `json:"resources"`
	EnforcedBy  string           `json:"enforcedBy"`
	Hint        string           `json:"hint,omitempty"`
	AddLabel    string           `json:"addLabel"`
	Description string           `json:"description"`
}

// ResourceDrift compares one configured resource with the same resource on
// the other connected clusters: missing, differs or same.
type ResourceDrift struct {
	Ref      Ref               `json:"ref"`
	Tasks    []string          `json:"tasks"`
	Clusters map[string]string `json:"clusters"`
}

type ServiceConfig struct {
	Subject     Ref              `json:"subject"`
	Answers     Answers          `json:"answers"`
	AnswerEmpty map[string]bool  `json:"answerEmpty"`
	Groups      []GroupDef       `json:"groups"`
	Tasks       []ConfigTask     `json:"tasks"`
	Other       []ConfigResource `json:"other"`
	Clusters    []string         `json:"clusters"`
	Header      ServiceHeader    `json:"header"`
	Drift       []ResourceDrift  `json:"drift"`
	// Multicluster is one plain sentence, empty when every connected cluster
	// holds the same configuration.
	Multicluster string `json:"multicluster,omitempty"`
}

// EditKeyFor is the stable key the drawer opens: Kind:namespace/name#rule.
func EditKeyFor(r Ref, rule int) string {
	return fmt.Sprintf("%s:%s/%s#%d", r.Kind, r.Namespace, r.Name, rule)
}

// ManagedBy says what manages an object. A controller owner reference comes
// first ("controller"), then metadata.managedFields manager names (the
// redactor keeps only the names, and only for the ambient policy kinds), then
// labels and annotations.
func ManagedBy(o *store.Object) string {
	code, _ := ManagedByInfo(o)
	return code
}

// ManagedByInfo returns ManagedBy's code and the words to show for it:
// "Flightbay", "Helm", "Argo CD", "Flux", "kubectl", "Unknown", or for a
// controller owner the product and owner kind, such as "kagent (Agent)".
func ManagedByInfo(o *store.Object) (code, label string) {
	if o == nil {
		return "unknown", "Unknown"
	}
	if g, kind := controllerOwner(o); kind != "" {
		return "controller", ownerProduct(g) + " (" + kind + ")"
	}
	code = managedByCode(o)
	return code, managedByLabels[code]
}

var managedByLabels = map[string]string{
	"flightbay": "Flightbay", "helm": "Helm", "argocd": "Argo CD", "flux": "Flux", "kubectl": "kubectl", "unknown": "Unknown",
}

// controllerOwner is the API group and kind of the owner reference marked
// controller: true, if any.
func controllerOwner(o *store.Object) (group, kind string) {
	for _, v := range L(Path(o.Obj, "metadata", "ownerReferences")) {
		m := M(v)
		if c, _ := m["controller"].(bool); !c || S(m["kind"]) == "" {
			continue
		}
		api := S(m["apiVersion"])
		if i := strings.LastIndex(api, "/"); i >= 0 {
			group = api[:i]
		}
		return group, S(m["kind"])
	}
	return "", ""
}

// ownerProduct names the product behind an owner's API group.
func ownerProduct(group string) string {
	switch {
	case group == "kagent.dev" || strings.HasSuffix(group, ".kagent.dev"):
		return "kagent"
	case group == GatewayGroup:
		return "Gateway API"
	case strings.Contains(group, "istio"):
		return "Istio"
	case strings.Contains(group, "agentgateway"):
		return "agentgateway"
	case group == "" || group == "apps" || group == "batch":
		return "Kubernetes"
	}
	return group
}

func managedByCode(o *store.Object) string {
	for _, v := range L(Path(o.Obj, "metadata", "managedFields")) {
		m := strings.ToLower(S(M(v)["manager"]))
		switch {
		case strings.Contains(m, "flightbay"):
			return "flightbay"
		case strings.Contains(m, "helm"):
			return "helm"
		case strings.Contains(m, "argocd"):
			return "argocd"
		case strings.Contains(m, "kustomize-controller"), strings.Contains(m, "helm-controller"), strings.Contains(m, "flux"):
			return "flux"
		}
	}
	lb := o.Labels()
	an := map[string]string{}
	for k, v := range M(Path(o.Obj, "metadata", "annotations")) {
		an[k] = S(v)
	}
	has := func(m map[string]string, k string) bool { _, ok := m[k]; return ok }
	switch {
	case strings.EqualFold(lb["app.kubernetes.io/managed-by"], "flightbay"):
		return "flightbay"
	case strings.EqualFold(lb["app.kubernetes.io/managed-by"], "Helm"), has(an, "meta.helm.sh/release-name"):
		return "helm"
	case has(an, "argocd.argoproj.io/tracking-id"), has(lb, "argocd.argoproj.io/instance"), has(an, "argocd.argoproj.io/instance"):
		return "argocd"
	case has(lb, "kustomize.toolkit.fluxcd.io/name"), has(an, "kustomize.toolkit.fluxcd.io/name"):
		return "flux"
	case has(an, "kubectl.kubernetes.io/last-applied-configuration"):
		return "kubectl"
	}
	return "unknown"
}

type cfgBuilder struct {
	ws       *store.Workspace
	svc      *store.Object
	wpName   string
	editable map[string]Editable // by EditKeyFor
	byTask   map[string][]ConfigResource
	nouns    map[string][]string // ref key -> what to call it in the multicluster sentence
	other    []ConfigResource
	facts    AnswerFacts
	l7Policy bool // an L7 policy enforced by the waypoint exists
	l4Idents bool // a ztunnel policy matches on principals
	wpOnly   bool // an ALLOW restricted to the waypoint identity exists
	wpPrinc  string
	mtls     struct {
		rank  int
		mode  string
		res   *ConfigResource
		scope string
	}
	routeDefaults int
	attached      []string // names of HTTPRoutes attached to the Service
}

// NewServiceConfig builds the payload of the Configuration tab for one
// Service. It composes what NewView composes (editable configurations and the
// policies that reach the Service) with Capabilities() for gating.
func NewServiceConfig(ws *store.Workspace, cluster, ns, name string) (*ServiceConfig, error) {
	svc := Target(ws, cluster, "Service", ns, name)
	if svc == nil {
		return nil, fmt.Errorf("Service %s/%s was not captured", ns, name)
	}
	_, wpName, _ := Waypoint(ws, svc)
	b := &cfgBuilder{ws: ws, svc: svc, wpName: wpName, editable: map[string]Editable{}, byTask: map[string][]ConfigResource{}, nouns: map[string][]string{}, wpPrinc: waypointPrincipal(ws, svc)}
	b.facts.Service = name
	editable := editableConfigurations(ws, svc)
	for _, e := range editable {
		b.editable[EditKeyFor(e.Ref, e.Rule)] = e
	}
	b.collectRoutes(editable)
	b.collectDestinationRules(editable)
	b.collectResilienceFilters(editable)
	b.collectPolicies(servicePolicies(ws, svc))
	b.collectPublication()
	b.finishFacts()

	out := &ServiceConfig{Subject: reference(svc), Groups: TaskGroups, Other: []ConfigResource{}, Clusters: ws.Clusters(), Drift: []ResourceDrift{}}
	out.Header = b.buildHeader()
	out.Answers = BuildAnswers(b.facts)
	out.AnswerEmpty = map[string]bool{"resilience": b.facts.Resilience.none()}
	caps := map[string]Task{}
	for _, c := range Capabilities(ws, cluster, ns, name) {
		caps[c.ID] = c
	}
	out.Other = append(out.Other, b.other...)
	out.Drift = b.drift(out.Clusters)
	driftsOn := map[string][]string{} // task id -> clusters where it differs
	for _, d := range out.Drift {
		for c, st := range d.Clusters {
			if st == "differs" {
				for _, t := range d.Tasks {
					driftsOn[t] = append(driftsOn[t], c)
				}
			}
		}
	}
	for _, def := range TaskTable {
		out.Tasks = append(out.Tasks, b.task(def, caps[def.ID], driftsOn[def.ID]))
	}
	out.Multicluster = b.multicluster(out.Drift)
	return out, nil
}

func (b *cfgBuilder) add(task string, r ConfigResource, o *store.Object) {
	r.ManagedBy = ManagedBy(o)
	if r.EditKey == "" {
		r.EditKey = EditKeyFor(r.Ref, r.Rule)
	}
	if e, ok := b.editable[r.EditKey]; ok {
		own := r.Scope == "" || r.Scope == "service" || r.Scope == "waypoint" || r.Scope == "workload"
		r.Editable = e.Supported && e.Request != nil && own
		if !e.Supported || e.Request == nil {
			r.ReadOnlyReason = e.Reason
		}
		if r.State == "" {
			r.State = e.Status
		}
	}
	if !r.Editable && r.ReadOnlyReason == "" {
		if r.Scope == "namespace" || r.Scope == "mesh" {
			r.ReadOnlyReason = "Inherited from " + map[string]string{"namespace": "the namespace", "mesh": "the mesh"}[r.Scope] + "; open it to review."
		} else {
			r.ReadOnlyReason = "This configuration shape has no editor yet; open it to review."
		}
	}
	if r.State == "" {
		r.State = "Captured"
	}
	if task == "" {
		b.other = append(b.other, r)
		return
	}
	b.byTask[task] = append(b.byTask[task], r)
	key := r.Ref.Kind + "/" + r.Ref.Namespace + "/" + r.Ref.Name
	b.nouns[key] = appendOnce(b.nouns[key], task+"|"+r.Action)
}

func appendOnce(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

// ---- HTTPRoute ------------------------------------------------------------

func routeMatchOf(rule map[string]any) RouteMatchFacts {
	var first *RouteMatchFacts
	for _, mv := range L(rule["matches"]) {
		m := M(mv)
		f := RouteMatchFacts{Path: S(Path(m, "path", "value")), Method: S(m["method"])}
		for _, h := range L(m["headers"]) {
			f.Headers = append(f.Headers, S(M(h)["name"])+": "+S(M(h)["value"]))
		}
		for _, q := range L(m["queryParams"]) {
			f.Query = append(f.Query, S(M(q)["name"])+"="+S(M(q)["value"]))
		}
		if !f.any() {
			return RouteMatchFacts{} // one match covers everything
		}
		if first == nil {
			c := f
			first = &c
		}
	}
	if first == nil {
		return RouteMatchFacts{}
	}
	return *first
}

func matchLabel(m RouteMatchFacts, port int) string {
	var ps []string
	if m.Path != "" && m.Path != "/" {
		ps = append(ps, "Path "+m.Path)
	}
	if m.Method != "" {
		ps = append(ps, "Method "+m.Method)
	}
	for _, h := range m.Headers {
		ps = append(ps, "Header "+strings.Replace(h, ": ", " = ", 1))
	}
	for _, q := range m.Query {
		ps = append(ps, "Query "+strings.Replace(q, "=", " = ", 1))
	}
	s := "All paths"
	if len(ps) > 0 {
		s = strings.Join(ps, ", ")
	}
	if port > 0 {
		s += fmt.Sprintf(" on port %d", port)
	}
	return s
}

func (b *cfgBuilder) collectRoutes(editable []Editable) {
	for _, e := range editable {
		if e.Ref.Kind != "HTTPRoute" {
			continue
		}
		o := b.ws.Get(e.Ref.Cluster, e.Ref.Group, e.Ref.Resource, e.Ref.Namespace, e.Ref.Name)
		if o == nil {
			continue
		}
		rules := L(Path(o.Obj, "spec", "rules"))
		if e.Rule >= len(rules) {
			continue
		}
		rule := M(rules[e.Rule])
		port, attached := 0, false
		for _, pv := range L(Path(o.Obj, "spec", "parentRefs")) {
			p := M(pv)
			g, k, pns, pn := parent(p, o.Namespace)
			if g == "" && k == "Service" && pns == b.svc.Namespace && pn == b.svc.Name {
				attached = true
				if N(p["port"]) > 0 {
					port = N(p["port"])
				}
			}
		}
		base := ConfigResource{Ref: e.Ref, Rule: e.Rule, State: e.Status, Scope: "service"}
		if !attached {
			base.Label, base.Summary, base.Action, base.Scope = "Other configuration", "Rule "+fmt.Sprint(e.Rule+1)+" of a route that is related but not attached to this Service", "Open", "service"
			base.Group = "routing"
			b.add("", base, o)
			continue
		}
		if !contains(b.attached, o.Name) {
			b.attached = append(b.attached, o.Name)
		}
		match := routeMatchOf(rule)
		var backends []Backend
		for _, bv := range L(rule["backendRefs"]) {
			x := M(bv)
			w := 1
			if v, ok := x["weight"]; ok {
				w = N(v)
			}
			backends = append(backends, Backend{Name: S(x["name"]), Port: N(x["port"]), Weight: w})
		}
		b.facts.Routing.Routes = append(b.facts.Routing.Routes, RouteFacts{Backends: backends, Match: match})
		if !match.any() {
			b.routeDefaults++
		}
		scope := matchLabel(match, port)
		tasks := 0
		if len(backends) > 1 {
			r := base
			r.Action, r.Label = "Split", scope
			r.Summary = "Split " + splitText(b.svc.Name, backends)
			b.add("weighted-routing", r, o)
			tasks++
		}
		if ts := M(rule["timeouts"]); len(ts) > 0 {
			r := base
			t := defaultString(S(ts["request"]), S(ts["backendRequest"]))
			r.Action, r.Label, r.Summary = "Timeout", "Time out after "+t, "Request timeout "+t+" on "+strings.ToLower(scope[:1])+scope[1:]
			b.add("timeouts", r, o)
			if b.facts.Resilience.Timeout == "" {
				b.facts.Resilience.Timeout = t
			}
			tasks++
		}
		if match.any() {
			r := base
			names := []string{}
			for _, x := range backends {
				names = append(names, x.Name)
			}
			r.Action, r.Label, r.Summary = "Route", scope, scope+" → "+strings.Join(names, ", ")
			b.add("header-routing", r, o)
			tasks++
		}
		for _, f := range L(rule["filters"]) {
			if S(M(f)["type"]) == "RequestMirror" {
				r := base
				to := S(Path(M(f), "requestMirror", "backendRef", "name"))
				r.Action, r.Label, r.Summary = "Mirror", "Copy requests to "+to, "Mirror "+scope+" to "+to
				b.add("mirroring", r, o)
				tasks++
				break
			}
		}
		if rt := M(rule["retry"]); len(rt) > 0 {
			r := base
			n := N(rt["attempts"])
			r.Action, r.Label, r.Summary = "Retry", fmt.Sprintf("Retry up to %d times", n), fmt.Sprintf("Retry failed requests up to %d times", n)
			b.add("retries", r, o)
			b.facts.Resilience.Retries = n
			tasks++
		}
		if tasks == 0 {
			r := base
			r.Label, r.Action, r.Group = "Other configuration", "Route", "routing"
			r.Summary = scope + " → " + strings.Join(backendNames(backends), ", ")
			b.add("", r, o)
		}
	}
}

func backendNames(bs []Backend) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out
}

func splitText(svc string, bs []Backend) string {
	ps := percents(bs)
	out := []string{}
	for i, b := range bs {
		out = append(out, fmt.Sprintf("%d%% %s", ps[i], b.Name))
	}
	return strings.Join(out, " / ")
}

// ---- DestinationRule ------------------------------------------------------

func (b *cfgBuilder) collectDestinationRules(editable []Editable) {
	for _, e := range editable {
		if e.Ref.Kind != "DestinationRule" {
			continue
		}
		o := b.ws.Get(e.Ref.Cluster, e.Ref.Group, e.Ref.Resource, e.Ref.Namespace, e.Ref.Name)
		if o == nil {
			continue
		}
		base := ConfigResource{Ref: e.Ref, State: e.Status, Scope: "service"}
		tp := M(Path(o.Obj, "spec", "trafficPolicy"))
		tasks := 0
		if lb := M(tp["loadBalancer"]); len(lb) > 0 {
			if loc := M(lb["localityLbSetting"]); len(loc) > 0 {
				r := base
				r.Action, r.Label, r.Summary = "Failover", "Prefer the local region", "Locality failover is configured"
				b.add("locality-failover", r, o)
				b.facts.Resilience.Locality = true
				tasks++
			}
			algo := S(lb["simple"])
			if algo == "" && len(M(lb["consistentHash"])) > 0 {
				algo = "CONSISTENT_HASH"
			}
			if algo != "" {
				r := base
				r.Action, r.Label, r.Summary = "Balance", humanAlgorithm(algo), "Load balancing: "+humanAlgorithm(algo)
				b.add("load-balancing", r, o)
				tasks++
			}
		}
		if cp := M(tp["connectionPool"]); len(cp) > 0 {
			r := base
			r.Action, r.Label, r.Summary = "Limit", "Connection and request limits", "Connection pool limits are set"
			b.add("circuit-breaker", r, o)
			b.facts.Resilience.CircuitBreak = true
			tasks++
		}
		if od := M(tp["outlierDetection"]); len(od) > 0 {
			n := N(od["consecutive5xxErrors"])
			if n == 0 {
				n = N(od["consecutiveGatewayErrors"])
			}
			r := base
			r.Action = "Eject"
			if n > 0 {
				r.Label, r.Summary = fmt.Sprintf("Eject after %d errors", n), fmt.Sprintf("Eject a Pod after %d consecutive errors", n)
			} else {
				r.Label, r.Summary = "Eject failing Pods", "Outlier detection is configured"
			}
			b.add("endpoint-failover", r, o)
			b.facts.Resilience.EjectAfter = n
			tasks++
		}
		if tasks == 0 {
			r := base
			r.Label, r.Action, r.Group, r.Summary = "Other configuration", "Open", "routing", "DestinationRule with no setting Flightbay recognises"
			b.add("", r, o)
		}
	}
}

func humanAlgorithm(a string) string {
	switch a {
	case "ROUND_ROBIN":
		return "Round robin"
	case "LEAST_REQUEST", "LEAST_CONN":
		return "Least request"
	case "RANDOM":
		return "Random"
	case "CONSISTENT_HASH":
		return "Consistent hash"
	}
	return a
}

// ---- Flightbay-owned resilience EnvoyFilters ------------------------------

func (b *cfgBuilder) collectResilienceFilters(editable []Editable) {
	for _, e := range editable {
		if e.Ref.Kind != "EnvoyFilter" || e.Request == nil {
			continue
		}
		o := b.ws.Get(e.Ref.Cluster, e.Ref.Group, e.Ref.Resource, e.Ref.Namespace, e.Ref.Name)
		req := e.Request
		scope := "this Service"
		if r := req.Resilience; r != nil && r.Scope == "api" && r.Match != nil && r.Match.Path != "" {
			scope = r.Match.Path
		}
		for _, t := range req.Tasks {
			r := ConfigResource{Ref: e.Ref, State: e.Status, Scope: "service"}
			switch t {
			case "timeouts":
				r.Action, r.Label, r.Summary = "Timeout", "Time out after "+req.Timeout, "Request timeout "+req.Timeout+" for "+scope
				if b.facts.Resilience.Timeout == "" {
					b.facts.Resilience.Timeout = req.Timeout
				}
			case "retries":
				r.Action, r.Label, r.Summary = "Retry", fmt.Sprintf("Retry up to %d times", req.RetryAttempts), fmt.Sprintf("Retry failed requests up to %d times for %s", req.RetryAttempts, scope)
				b.facts.Resilience.Retries = req.RetryAttempts
			case "rate-limit":
				r.Action, r.Label, r.Summary = "Rate limit", fmt.Sprintf("%d requests a %s", req.Requests, rateUnitWord(req.Unit)), fmt.Sprintf("Local rate limit for %s", scope)
				b.facts.Resilience.RateRequests, b.facts.Resilience.RateUnit = req.Requests, req.Unit
			case "global-rate-limit":
				r.Action, r.Label, r.Summary = "Shared", "Shared rate limit", "Calls an external rate limit service for "+scope
				b.facts.Resilience.GlobalLimiter = true
			case "circuit-breaker":
				r.Action, r.Label, r.Summary = "Limit", "Connection and request limits", "Connection pool limits for "+scope
				b.facts.Resilience.CircuitBreak = true
			case "endpoint-failover":
				r.Action, r.Label, r.Summary = "Eject", fmt.Sprintf("Eject after %d errors", req.FailureThreshold), fmt.Sprintf("Eject a Pod after %d consecutive errors", req.FailureThreshold)
				b.facts.Resilience.EjectAfter = req.FailureThreshold
			default:
				continue
			}
			b.add(t, r, o)
		}
	}
}

// ---- security policies ----------------------------------------------------

func policyInherited(scope string) bool { return scope == "namespace" || scope == "mesh" }

func mtlsRank(scope string) int {
	switch scope {
	case "workload":
		return 3
	case "namespace":
		return 2
	case "mesh":
		return 1
	}
	return 0
}

func (b *cfgBuilder) collectPolicies(policies []PolicyEntry) {
	for _, pe := range policies {
		o := b.ws.Get(pe.Ref.Cluster, pe.Ref.Group, pe.Ref.Resource, pe.Ref.Namespace, pe.Ref.Name)
		if o == nil {
			continue
		}
		base := ConfigResource{Ref: pe.Ref, Scope: pe.Scope}
		if pe.Warning != "" {
			base.State = "Not enforced"
		}
		inherited := policyInherited(pe.Scope)
		enforced := pe.Warning == ""
		switch o.Kind {
		case "AuthorizationPolicy":
			b.authorization(o, pe, base, inherited, enforced)
		case "RequestAuthentication":
			var issuers []string
			for _, j := range L(Path(o.Obj, "spec", "jwtRules")) {
				if i := S(M(j)["issuer"]); i != "" {
					issuers = append(issuers, i)
				}
			}
			r := base
			r.Action = pick(inherited, "Inherited", "JWT")
			r.Label = "Check JWTs"
			if len(issuers) > 0 {
				r.Label = "Check JWTs from " + strings.Join(issuers, ", ")
			}
			r.Summary = r.Label
			b.add("jwt", r, o)
			if enforced {
				b.facts.Access.JWTIssuers = append(b.facts.Access.JWTIssuers, issuers...)
			}
		case "PeerAuthentication":
			b.peerAuthentication(o, pe, base)
		default: // agentgateway policies
			b.agentgatewayPolicy(o, pe, base, inherited, enforced)
		}
	}
}

func pick(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func (b *cfgBuilder) peerAuthentication(o *store.Object, pe PolicyEntry, base ConfigResource) {
	mode := S(Path(o.Obj, "spec", "mtls", "mode"))
	if mode == "" || mode == "UNSET" {
		return
	}
	rank := mtlsRank(pe.Scope)
	if rank <= b.mtls.rank {
		return // a narrower policy already decides
	}
	r := base
	lower := strings.ToLower(mode)
	switch pe.Scope {
	case "mesh":
		r.Label = "mTLS " + lower + ", from the mesh default"
	case "namespace":
		r.Label = "mTLS " + lower + ", from the " + pe.Ref.Namespace + " namespace default"
	default:
		r.Label = "mTLS " + lower
	}
	r.Summary = mode + " mTLS. " + capitalise(pe.Reason)
	r.Action = pick(policyInherited(pe.Scope), "Inherited", mode)
	b.mtls.rank, b.mtls.mode, b.mtls.scope, b.mtls.res = rank, mode, pe.Scope, &r
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func ruleSummary(r AccessRule) string {
	r = r.fold()
	var ps []string
	for _, s := range r.From {
		for _, x := range s.Namespaces {
			ps = append(ps, "caller namespace is "+x)
		}
		for _, x := range s.Principals {
			ps = append(ps, "caller is "+principalName(x))
		}
		for _, x := range s.RequestPrincipals {
			ps = append(ps, "request principal is "+x)
		}
		for _, x := range s.Services {
			ps = append(ps, "caller is "+x)
		}
		for _, x := range s.IPBlocks {
			ps = append(ps, "source IP is in "+x)
		}
	}
	for _, t := range r.To {
		if len(t.Methods) > 0 {
			ps = append(ps, "method is "+strings.Join(t.Methods, " or "))
		}
		if len(t.Paths) > 0 {
			ps = append(ps, "path is "+strings.Join(t.Paths, " or "))
		}
		if len(t.NotMethods) > 0 {
			ps = append(ps, "method is not "+strings.Join(t.NotMethods, " or "))
		}
	}
	for _, w := range r.When {
		if len(w.Values) > 0 {
			ps = append(ps, w.Key+" is "+strings.Join(w.Values, " or "))
		}
		if len(w.NotValues) > 0 {
			ps = append(ps, w.Key+" is not "+strings.Join(w.NotValues, " or "))
		}
	}
	return strings.Join(ps, " and ")
}

// accessLabel is the short row label for an authorization policy.
func accessLabel(action string, rules []AccessRule) string {
	verb := "Allow"
	if action == "DENY" {
		verb = "Block"
	}
	if len(rules) == 0 {
		return pick(action == "DENY", "Block all requests", "Deny all requests")
	}
	r := rules[0].fold()
	var what string
	switch {
	case len(r.When) > 0 && strings.HasPrefix(r.When[0].Key, "request.headers["):
		w := r.When[0]
		name := strings.TrimSuffix(strings.TrimPrefix(w.Key, "request.headers["), "]")
		if len(w.NotValues) > 0 && len(w.Values) == 0 {
			what = "header " + name + " != " + strings.Join(w.NotValues, ", ")
		} else {
			what = "header " + name + " = " + strings.Join(w.Values, ", ")
		}
	case len(r.To) > 0 && len(r.To[0].Methods) > 0:
		what = strings.Join(r.To[0].Methods, ", ")
		if verb == "Allow" {
			what = "only " + what
		}
	case len(r.To) > 0 && len(r.To[0].Paths) > 0:
		what = "paths " + strings.Join(r.To[0].Paths, ", ")
	case len(r.From) > 0 && len(r.From[0].Namespaces) > 0:
		what = "callers in " + strings.Join(r.From[0].Namespaces, ", ")
	case len(r.From) > 0 && len(r.From[0].Principals) > 0:
		what = "caller " + principalName(r.From[0].Principals[0])
	case len(r.From) > 0 && len(r.From[0].RequestPrincipals) > 0:
		what = "callers with a JWT"
	default:
		what = "matching requests"
	}
	out := verb + " " + what
	if len(rules) > 1 {
		out += fmt.Sprintf(" (+%d more)", len(rules)-1)
	}
	return out
}

func (b *cfgBuilder) authorization(o *store.Object, pe PolicyEntry, base ConfigResource, inherited, enforced bool) {
	action := defaultString(S(Path(o.Obj, "spec", "action")), "ALLOW")
	if action != "ALLOW" && action != "DENY" {
		r := base
		r.Action, r.Label, r.Summary = action, "Audit or custom policy", action+" policy"
		b.add("access-rule", r, o)
		return
	}
	acc, structured := accessFromPolicy(o)
	hasTargets := len(L(Path(o.Obj, "spec", "targetRefs"))) > 0
	rawRules := L(Path(o.Obj, "spec", "rules"))
	r := base
	r.Action = pick(inherited, "Inherited", action)
	var rules []AccessRule
	custom := false
	if acc != nil {
		rules = acc.Rules
		custom = acc.RulesYAML != ""
	}
	if !structured && len(rawRules) > 0 {
		custom = true
	}
	task := "access-rule"
	switch {
	case action == "ALLOW" && len(rawRules) == 0:
		if hasTargets {
			r.Label, r.Summary = "Deny all requests", "ALLOW policy with no rules: nothing is allowed"
		} else {
			task = "l4-default-deny"
			r.Label, r.Summary = "Deny by default", "ALLOW policy with no rules: callers need another ALLOW policy"
		}
		if enforced {
			b.facts.Access.DefaultDeny = true
		}
	case action == "ALLOW" && b.isWaypointLock(rules):
		task = "require-waypoint"
		r.Label, r.Summary = "Only the waypoint can reach the Pods", "ALLOW only the waypoint identity "+b.wpPrinc
		b.wpOnly = true
		if enforced {
			b.facts.Access.WaypointOnly = true
		}
	case action == "ALLOW" && b.isJWTRule(rules):
		task = "jwt"
		r.Label, r.Summary = "Require a valid JWT", "ALLOW only callers with a request principal"
		if enforced {
			b.facts.Access.JWTRequired = true
		}
	case action == "ALLOW" && pe.Enforced == "ztunnel" && !hasTargets && oneNamespaceRule(rules):
		task = "l4-allow-namespace"
		ns := rules[0].From[0].Namespaces[0]
		r.Label, r.Summary = "Allow callers in "+ns, "ALLOW callers from namespace "+ns
	case action == "ALLOW" && hasTargets && onlyMethods(rules):
		task = "methods"
		r.Label = accessLabel(action, rules)
		r.Summary = "ALLOW when " + ruleSummary(rules[0])
	default:
		r.Label = accessLabel(action, rules)
		if len(rules) > 0 {
			r.Summary = pick(action == "DENY", "Deny", "Allow") + " when " + ruleSummary(rules[0])
		}
		if custom {
			r.Label = pick(action == "DENY", "Block", "Allow") + " by custom rules"
			r.Summary = fmt.Sprintf("%s policy with %d rule(s) written by hand", action, len(rawRules))
		}
	}
	// Use the editor's own task when there is one, so Edit opens what built it.
	if e, ok := b.editable[EditKeyFor(r.Ref, 0)]; ok && e.Supported && len(e.Tasks) > 0 && contains([]string{"methods", "access-rule", "l4-allow-namespace"}, e.Tasks[0]) {
		task = e.Tasks[0]
	}
	b.add(task, r, o)
	if !enforced {
		return
	}
	if pe.Enforced == "waypoint" {
		b.l7Policy = true
	}
	if pe.Enforced == "ztunnel" && rulesUsePrincipals(rules) {
		b.l4Idents = true
	}
	switch {
	case task == "require-waypoint", task == "jwt", len(rawRules) == 0:
		// carried by their own facts (WaypointOnly, JWTRequired, DefaultDeny)
	case custom:
		b.facts.Access.Custom += len(rawRules)
	case action == "DENY":
		b.facts.Access.Deny = append(b.facts.Access.Deny, rules...)
	default:
		b.facts.Access.Allow = append(b.facts.Access.Allow, rules...)
	}
}

func oneNamespaceRule(rules []AccessRule) bool {
	if len(rules) != 1 || len(rules[0].From) != 1 || len(rules[0].To) != 0 || len(rules[0].When) != 0 {
		return false
	}
	f := rules[0].From[0]
	return len(f.Namespaces) == 1 && len(f.Principals) == 0 && len(f.RequestPrincipals) == 0
}

func onlyMethods(rules []AccessRule) bool {
	if len(rules) != 1 || len(rules[0].From) != 0 || len(rules[0].When) != 0 || len(rules[0].To) != 1 {
		return false
	}
	t := rules[0].To[0]
	return len(t.Methods) > 0 && len(t.Paths) == 0 && len(t.Hosts) == 0 && len(t.Ports) == 0
}

func rulesUsePrincipals(rules []AccessRule) bool {
	for _, r := range rules {
		for _, f := range r.From {
			if len(f.Principals) > 0 || len(f.ServiceAccounts) > 0 || len(f.Services) > 0 {
				return true
			}
		}
	}
	return false
}

func (b *cfgBuilder) isWaypointLock(rules []AccessRule) bool {
	if b.wpPrinc == "" || len(rules) != 1 || len(rules[0].From) != 1 || len(rules[0].To) != 0 || len(rules[0].When) != 0 {
		return false
	}
	p := rules[0].From[0].Principals
	return len(p) == 1 && p[0] == b.wpPrinc
}

func (b *cfgBuilder) isJWTRule(rules []AccessRule) bool {
	if len(rules) == 0 {
		return false
	}
	for _, r := range rules {
		if len(r.From) == 0 {
			return false
		}
		for _, f := range r.From {
			if len(f.RequestPrincipals) == 0 {
				return false
			}
		}
	}
	return true
}

func (b *cfgBuilder) agentgatewayPolicy(o *store.Object, pe PolicyEntry, base ConfigResource, inherited, enforced bool) {
	found := false
	if auth := M(Path(o.Obj, "spec", "traffic", "authorization")); len(auth) > 0 {
		found = true
		action := strings.ToUpper(defaultString(S(auth["action"]), "Allow"))
		r := base
		r.Action = pick(inherited, "Inherited", action)
		exprs := []string{}
		for _, e := range L(Path(auth, "policy", "matchExpressions")) {
			exprs = append(exprs, S(e))
		}
		r.Label = pick(action == "DENY", "Block", "Allow") + " by expression"
		if len(exprs) > 0 {
			r.Label = pick(action == "DENY", "Block", "Allow") + " when " + exprs[0]
			r.Summary = capitalise(strings.ToLower(action)) + " when " + strings.Join(exprs, " or ")
		}
		b.add("access-rule", r, o)
		if enforced {
			b.l7Policy = true
			var rules []AccessRule
			for _, e := range exprs {
				rules = append(rules, AccessRule{When: []AccessCondition{{Key: "cel", Values: []string{e}}}})
			}
			if action == "DENY" {
				b.facts.Access.Deny = append(b.facts.Access.Deny, rules...)
			} else {
				b.facts.Access.Allow = append(b.facts.Access.Allow, rules...)
			}
		}
	}
	if local := L(Path(o.Obj, "spec", "traffic", "rateLimit", "local")); len(local) > 0 {
		found = true
		l := M(local[0])
		r := base
		r.Action = pick(inherited, "Inherited", "Rate limit")
		r.Label = fmt.Sprintf("%d requests a %s", N(l["requests"]), rateUnitWord(S(l["unit"])))
		r.Summary = "Local rate limit: " + r.Label
		b.add("rate-limit", r, o)
		b.facts.Resilience.RateRequests, b.facts.Resilience.RateUnit = N(l["requests"]), S(l["unit"])
	}
	if !found {
		r := base
		r.Label, r.Action, r.Group, r.Summary = "Other configuration", "Open", "resilience", o.Kind+" with no setting Flightbay recognises"
		b.add("", r, o)
	}
}

// ---- global publication ---------------------------------------------------

func (b *cfgBuilder) collectPublication() {
	scope := b.svc.Labels()["solo.io/service-scope"]
	if scope != "global" && scope != "global-only" {
		return
	}
	r := ConfigResource{Ref: reference(b.svc), Action: "Global", Scope: "service", State: "Captured", Label: "Published as a global Service", Summary: "solo.io/service-scope=" + scope, EditKey: EditKeyFor(reference(b.svc), 0)}
	r.ManagedBy = ManagedBy(b.svc)
	r.ReadOnlyReason = "Change publication from the Publish as a global Service task."
	b.byTask["global-service"] = append(b.byTask["global-service"], r)
}

// ---- facts that need every resource seen ------------------------------------

func (b *cfgBuilder) finishFacts() {
	b.facts.Transport.Mode = b.mtls.mode
	if b.mtls.res != nil {
		b.add("strict-mtls", *b.mtls.res, b.ws.Get(b.mtls.res.Ref.Cluster, b.mtls.res.Ref.Group, b.mtls.res.Ref.Resource, b.mtls.res.Ref.Namespace, b.mtls.res.Ref.Name))
	}
	b.facts.Transport.NotInMesh = !b.inAmbient()
}

// inAmbient reports whether the Service's Pods are captured by ztunnel: the
// namespace, Service or any Pod behind it carries istio.io/dataplane-mode=ambient.
func (b *cfgBuilder) inAmbient() bool {
	is := func(o *store.Object) bool { return o != nil && o.Labels()["istio.io/dataplane-mode"] == "ambient" }
	if is(b.svc) || is(b.ws.Get(b.svc.Cluster, "", "namespaces", "", b.svc.Namespace)) {
		return true
	}
	sel := M(Path(b.svc.Obj, "spec", "selector"))
	for _, p := range b.ws.List(b.svc.Cluster, "", "pods", b.svc.Namespace, nil) {
		if len(sel) > 0 && matches(sel, p.Labels()) && is(p) {
			return true
		}
	}
	// A namespace that was not captured says nothing either way.
	return b.ws.Get(b.svc.Cluster, "", "namespaces", "", b.svc.Namespace) == nil
}

// ---- tasks ------------------------------------------------------------------

func (b *cfgBuilder) task(def TaskDef, c Task, differs []string) ConfigTask {
	t := ConfigTask{ID: def.ID, Title: def.Title, ShortLabel: def.ShortLabel, Group: def.Group, Layer: def.Layer, Resources: []ConfigResource{}, AddLabel: def.AddLabelFor(b.svc.Name), Description: def.Description}
	switch def.Layer {
	case "L4":
		t.EnforcedBy = "ztunnel"
	case "L7":
		t.EnforcedBy = "waypoint"
	}
	if res := b.byTask[def.ID]; len(res) > 0 {
		t.Resources = res
	}
	configured, inherited := 0, 0
	for _, r := range t.Resources {
		if policyInherited(r.Scope) {
			inherited++
		} else {
			configured++
		}
	}
	supported := c.ID != "" && c.Supported
	rec, recReason, recHint := b.recommendation(def.ID)
	switch {
	case configured > 0:
		t.Status = "configured"
	case inherited > 0:
		t.Status = "inherited"
		for _, r := range t.Resources {
			t.Reason = capitalise(b.inheritedReason(r))
			break
		}
	case rec && supported:
		t.Status, t.Reason = "recommended", recReason
	case !supported:
		t.Status, t.Reason = "unavailable", c.Reason
		if def.Layer == "L7" && b.wpName == "" {
			t.Status = "needs-waypoint"
			t.ShortReason = "L7 rules need a waypoint"
			t.Hint = "L7 rules need a waypoint. Set one up."
		} else {
			t.ShortReason = shortReason(def.ID, c.Reason)
		}
	default:
		t.Status = "not-set"
	}
	if (t.Status == "configured" || t.Status == "inherited") && rec && supported {
		t.Hint = recHint // configured or inherited, and still weak: say why
		if t.Reason == "" {
			t.Reason = recReason
		}
	}
	if t.Status == "recommended" {
		t.Hint = recHint
	}
	if len(differs) > 0 && t.Status == "configured" {
		sort.Strings(differs)
		t.Status, t.Reason = "differs", "Differs on "+strings.Join(uniq(differs), ", ")
	}
	if t.Status == "not-set" && len(b.attached) > 0 && (def.ID == "header-routing" || def.ID == "mirroring") {
		t.Hint = "Adds a rule to the same HTTPRoute"
	}
	if def.Group == "routing" && b.routeDefaults > 1 && t.Status == "configured" && t.Hint == "" {
		t.Hint = "More than one route rule matches every request to this Service. Only one of them wins."
	}
	return t
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (b *cfgBuilder) inheritedReason(r ConfigResource) string {
	switch r.Scope {
	case "mesh":
		return "set by " + r.Ref.Kind + " " + r.Ref.Namespace + "/" + r.Ref.Name + ", which applies across the mesh"
	case "namespace":
		return "set by " + r.Ref.Kind + " " + r.Ref.Name + ", which applies to every workload in " + r.Ref.Namespace
	}
	return ""
}

// recommendation holds the recommended-rule logic in one place.
func (b *cfgBuilder) recommendation(id string) (ok bool, reason, hint string) {
	switch id {
	case "require-waypoint":
		// 1. an L7 policy enforced by a waypoint, and no ztunnel ALLOW that
		//    restricts callers to the waypoint identity.
		if b.wpName != "" && b.l7Policy && !b.wpOnly {
			return true, "Your waypoint policies can be bypassed: callers that reach the Pods directly skip them.", "Callers can skip this rule by going straight to the Pods."
		}
	case "strict-mtls":
		// 2. a ztunnel policy matches on principals while mTLS is PERMISSIVE.
		if b.l4Idents && b.mtls.mode != "STRICT" {
			return true, "A policy matches on caller identity while mTLS is PERMISSIVE, so callers outside the mesh are still accepted.", "Identity rules only protect traffic that carries an identity. Require mTLS to be sure."
		}
	}
	return false, "", ""
}

func shortReason(id, full string) string {
	switch id {
	case "retries":
		return "Retry is not in the installed Gateway API version"
	case "rate-limit":
		return "Needs an agentgateway waypoint or Solo Enterprise Istio"
	case "global-rate-limit":
		return "Needs Solo Enterprise Istio and a rate limit service"
	case "service-failover":
		return "Gateway API has no priority failover"
	case "locality-failover":
		return "Endpoints are in one region only"
	case "l4-allow-namespace":
		return "Not used when a waypoint is in place. Use Allow or deny requests"
	}
	return ""
}

// ---- multicluster -----------------------------------------------------------

// comparable is the spec the clusters are compared on. Status, metadata noise
// and other clusters' defaults are not part of it.
func (b *cfgBuilder) drift(clusters []string) []ResourceDrift {
	out := []ResourceDrift{}
	seen := map[string]bool{}
	var others []string
	for _, c := range clusters {
		if c == b.svc.Cluster {
			continue
		}
		if b.ws.Get(c, "", "services", b.svc.Namespace, b.svc.Name) != nil {
			others = append(others, c)
		}
	}
	if len(others) == 0 {
		return out
	}
	var tasks []string
	for _, def := range TaskTable {
		tasks = append(tasks, def.ID)
	}
	for _, id := range tasks {
		for _, r := range b.byTask[id] {
			if policyInherited(r.Scope) || r.Ref.Kind == "Service" {
				continue
			}
			key := r.Ref.Kind + "/" + r.Ref.Namespace + "/" + r.Ref.Name
			if seen[key] {
				for i := range out {
					if out[i].Ref == r.Ref && !contains(out[i].Tasks, id) {
						out[i].Tasks = append(out[i].Tasks, id)
					}
				}
				continue
			}
			seen[key] = true
			mine := b.ws.Get(r.Ref.Cluster, r.Ref.Group, r.Ref.Resource, r.Ref.Namespace, r.Ref.Name)
			d := ResourceDrift{Ref: r.Ref, Tasks: []string{id}, Clusters: map[string]string{}}
			for _, c := range others {
				theirs := b.ws.Get(c, r.Ref.Group, r.Ref.Resource, r.Ref.Namespace, r.Ref.Name)
				switch {
				case theirs == nil:
					d.Clusters[c] = "missing"
				case mine != nil && configurationFingerprint(mine) == configurationFingerprint(theirs):
					d.Clusters[c] = "same"
				default:
					d.Clusters[c] = "differs"
				}
			}
			out = append(out, d)
		}
	}
	return out
}

var taskNouns = map[string]string{
	"weighted-routing": "the split", "header-routing": "the route rule", "timeouts": "the timeout", "retries": "the retry settings",
	"mirroring": "the mirror", "load-balancing": "the load balancing", "endpoint-failover": "the endpoint ejection", "circuit-breaker": "the circuit breaker",
	"locality-failover": "the regional failover", "rate-limit": "the rate limit", "global-rate-limit": "the shared rate limit", "strict-mtls": "the mTLS setting",
	"l4-default-deny": "the default deny", "l4-allow-namespace": "the namespace allow", "require-waypoint": "the waypoint lock", "jwt": "the JWT check",
}

func (b *cfgBuilder) noun(d ResourceDrift) string {
	var parts []string
	for _, tn := range b.nouns[d.Ref.Kind+"/"+d.Ref.Namespace+"/"+d.Ref.Name] {
		task, action, _ := strings.Cut(tn, "|")
		switch {
		case (task == "access-rule" || task == "methods") && (action == "DENY" || action == "ALLOW"):
			parts = append(parts, "the "+action)
		case taskNouns[task] != "":
			parts = append(parts, taskNouns[task])
		default:
			parts = append(parts, "the "+d.Ref.Kind)
		}
	}
	return strings.Join(uniq(parts), " and ")
}

func (b *cfgBuilder) multicluster(drift []ResourceDrift) string {
	if len(drift) == 0 {
		return ""
	}
	clusters := map[string]bool{}
	for _, d := range drift {
		for c := range d.Clusters {
			clusters[c] = true
		}
	}
	names := []string{}
	for c := range clusters {
		names = append(names, c)
	}
	sort.Strings(names)
	var out []string
	for _, c := range names {
		var missing, differs []string
		for _, d := range drift {
			switch d.Clusters[c] {
			case "missing":
				missing = append(missing, b.noun(d))
			case "differs":
				differs = append(differs, b.noun(d))
			}
		}
		missing, differs = uniq(missing), uniq(differs)
		if len(missing) > 0 {
			verb := "only exists"
			if len(missing) > 1 {
				verb = "only exist"
			}
			s := capitalise(joinWords(missing, "and")) + " " + verb + " on " + b.svc.Cluster + "."
			if len(missing) == len(drift) {
				s += " On " + c + ", " + b.svc.Name + " has no configuration."
			}
			out = append(out, s)
		}
		if len(differs) > 0 {
			verb := "is"
			if len(differs) > 1 {
				verb = "are"
			}
			out = append(out, capitalise(joinWords(differs, "and"))+" "+verb+" different on "+c+".")
		}
	}
	return strings.Join(out, " ")
}
