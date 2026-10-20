package ambient

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Badge is one chip in the Ambient Overview services table. Tone is one of
// ok, bad, info, warn, mut.
type Badge struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

// PolicyFact is the part of an Istio security policy that decides its badge.
type PolicyFact struct {
	Kind     string // AuthorizationPolicy, PeerAuthentication, RequestAuthentication
	Action   string // AuthorizationPolicy action
	Enforced string // waypoint or ztunnel
	MTLS     string // PeerAuthentication mode
	Warning  bool   // accepted but not enforced
	Scope    string // service, waypoint, workload, namespace, mesh
}

// RouteFact describes one HTTPRoute as it relates to a Service.
type RouteFact struct {
	// Frontend is true when the route names the Service as a parent. A route
	// that only lists the Service as a backendRef is not a frontend.
	Frontend    bool
	Of          string // for a backend-only route: what the Service is a backend of
	Weights     []int  // relative backend weights of the first split rule
	HeaderMatch bool
	Mirror      bool
	Timeout     bool
	Retry       bool
}

// ServiceFacts is everything the badge derivation needs, with no store access.
type ServiceFacts struct {
	Policies       []PolicyFact
	Routes         []RouteFact
	Outlier        bool
	CircuitBreaker bool
	Retries        bool
	RateLimit      bool
	// Attached counts objects attached to this Service specifically: routes,
	// DestinationRules, resilience EnvoyFilters, agentgateway policies and
	// security policies other than mesh-wide ones.
	Attached int
}

// Configured reports whether a Service has anything set up for it: a badge
// or an attached object. Mesh-wide policies alone do not count.
func (f ServiceFacts) Configured() bool {
	if f.Attached > 0 {
		return true
	}
	_, routing, resilience := DeriveBadges(f)
	return len(routing) > 0 || len(resilience) > 0
}

// DeriveBadges is pure: facts in, badges out. Empty slices mean "nothing
// configured" and the UI shows "open" for access.
func DeriveBadges(f ServiceFacts) (access, routing, resilience []Badge) {
	access, routing, resilience = []Badge{}, []Badge{}, []Badge{}
	deny, allow, l4, jwt, unenforced := 0, 0, false, false, false
	for _, p := range f.Policies {
		if p.Warning {
			unenforced = true
		}
		switch p.Kind {
		case "AuthorizationPolicy":
			switch {
			case p.Enforced == "ztunnel":
				l4 = true
			case strings.EqualFold(p.Action, "DENY"):
				deny++
			case strings.EqualFold(p.Action, "ALLOW") || p.Action == "":
				allow++
			}
		case "PeerAuthentication":
			if p.MTLS == "STRICT" {
				l4 = true
			}
		case "RequestAuthentication":
			jwt = true
		}
	}
	tone := func(ok string) string {
		if unenforced {
			return "warn"
		}
		return ok
	}
	if deny > 0 {
		access = append(access, Badge{fmt.Sprintf("%d DENY", deny), tone("bad")})
	}
	if allow > 0 {
		access = append(access, Badge{fmt.Sprintf("%d ALLOW", allow), tone("ok")})
	}
	if l4 {
		access = append(access, Badge{"L4 lock", tone("info")})
	}
	if jwt {
		access = append(access, Badge{"JWT", tone("info")})
	}

	var split []int
	header, mirror, timeout, retry := false, false, false, false
	backendOf := ""
	for _, r := range f.Routes {
		if !r.Frontend {
			if backendOf == "" {
				backendOf = r.Of
			}
			continue
		}
		if split == nil && len(r.Weights) > 1 {
			split = r.Weights
		}
		header = header || r.HeaderMatch
		mirror = mirror || r.Mirror
		timeout = timeout || r.Timeout
		retry = retry || r.Retry
	}
	if split != nil {
		routing = append(routing, Badge{"split " + splitLabel(split), "info"})
	}
	if header {
		routing = append(routing, Badge{"header match", "info"})
	}
	if mirror {
		routing = append(routing, Badge{"mirror", "info"})
	}
	if timeout {
		routing = append(routing, Badge{"timeout", "info"})
	}
	if len(routing) == 0 && backendOf != "" {
		routing = append(routing, Badge{"backend of " + backendOf, "mut"})
	}

	if f.Outlier {
		resilience = append(resilience, Badge{"outlier", "ok"})
	}
	if f.CircuitBreaker {
		resilience = append(resilience, Badge{"circuit breaker", "ok"})
	}
	if f.Retries || retry {
		resilience = append(resilience, Badge{"retries", "ok"})
	}
	if f.RateLimit {
		resilience = append(resilience, Badge{"rate limit", "ok"})
	}
	return
}

// splitLabel renders weights as percentages that add up to 100, "80/20".
func splitLabel(w []int) string {
	total := 0
	for _, v := range w {
		total += v
	}
	parts := make([]string, len(w))
	if total <= 0 {
		for i := range w {
			parts[i] = fmt.Sprint(100 / len(w))
		}
		return strings.Join(parts, "/")
	}
	left := 100
	for i, v := range w {
		p := (v*100 + total/2) / total
		if i == len(w)-1 {
			p = left
		}
		if p > left {
			p = left
		}
		left -= p
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, "/")
}

// ---- response shapes ----

type CheckCounts struct {
	Fail int `json:"fail"`
	Warn int `json:"warn"`
}
type ClusterCell struct {
	Present        bool        `json:"present"`
	EndpointsReady bool        `json:"endpointsReady"`
	Waypoint       string      `json:"waypoint"`
	Checks         CheckCounts `json:"checks"`
	Differs        bool        `json:"differs"`
	NoEndpoints    bool        `json:"noEndpoints"`
}
type ServiceRow struct {
	Ref        Ref                    `json:"ref"`
	Waypoint   string                 `json:"waypoint"`
	Access     []Badge                `json:"access"`
	Routing    []Badge                `json:"routing"`
	Resilience []Badge                `json:"resilience"`
	PerCluster map[string]ClusterCell `json:"perCluster"`
	Drift      bool                   `json:"drift"`
	Checks     CheckCounts            `json:"checks"`
	// Configured is true when the Service has any access, routing or
	// resilience badge, or a policy or route attached to it. Mesh-wide
	// policies from the root namespace do not count: they reach every
	// Service and would make the flag meaningless.
	Configured bool `json:"configured"`
	// InAmbient is true when the namespace or the Service is enrolled in
	// ambient on any cluster where the Service exists.
	InAmbient bool `json:"inAmbient"`
}

// AttentionCheck is one failed or warning check behind an attention entry.
type AttentionCheck struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	State   string `json:"state"` // fail | warn
	Cluster string `json:"cluster"`
	HasFix  bool   `json:"hasFix"`
}

// AttentionRow is one Needs attention entry: one object (cluster-independent
// kind/namespace/name), every cluster it has a problem on, and its checks.
type AttentionRow struct {
	Ref      Ref      `json:"ref"` // Cluster is empty: the entry spans clusters
	Clusters []string `json:"clusters"`
	Cluster  string   `json:"cluster"` // first of Clusters, kept for older clients
	State    string   `json:"state"`   // fail | warn: fail when any check fails
	Detail   string   `json:"detail"`  // sentence of the most important check
	CheckID  string   `json:"checkId"` // that check's step ID
	HasFix   bool     `json:"hasFix"`
	// More is the number of other distinct checks (by step ID) across
	// clusters. The same check failing on two clusters is one check.
	More   int              `json:"more"`
	Checks []AttentionCheck `json:"checks"`
}
type NamespaceRow struct {
	Name          string `json:"name"`
	InAmbient     bool   `json:"inAmbient"`
	Waypoint      string `json:"waypoint"`
	WaypointScope string `json:"waypointScope"` // "namespace" (all Services), "services" (some), or ""
	Services      int    `json:"services"`      // rows in the Services table
	Configured    int    `json:"configured"`    // of those, rows with Configured
	System        bool   `json:"system"`        // platform namespace, hidden by default
}

// systemNamespaces are hidden by default in the scope picker. Any istio-*
// namespace is treated the same way.
var systemNamespaces = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"local-path-storage": true, "istio-system": true, "istio-eastwest": true,
}

// IsSystemNamespace reports a platform namespace: the explicit list above or
// the istio-* prefix.
func IsSystemNamespace(ns string) bool {
	return systemNamespaces[ns] || strings.HasPrefix(ns, "istio-")
}

type OverviewSummary struct {
	Clusters struct {
		Count           int      `json:"count"`
		Names           []string `json:"names"`
		AmbientOnAll    bool     `json:"ambientOnAll"`
		AmbientClusters []string `json:"ambientClusters"`
	} `json:"clusters"`
	Namespaces struct {
		InAmbient    int `json:"inAmbient"`
		Total        int `json:"total"`
		WithWaypoint int `json:"withWaypoint"`
	} `json:"namespaces"`
	PoliciesRoutes struct {
		Objects int `json:"objects"`
		Differ  int `json:"differ"`
	} `json:"policiesRoutes"`
	// NeedsAttention counts the entries in ServicesOverview.NeedsAttention,
	// so the card and the panel use the same unit. Services = Fail + Warn.
	NeedsAttention struct {
		Services int `json:"services"` // Service entries
		Fail     int `json:"fail"`     // Service entries with a failed check
		Warn     int `json:"warn"`     // Service entries with warnings only
		Other    int `json:"other"`    // HTTPRoute / Gateway entries
	} `json:"needsAttention"`
}
type ServicesOverview struct {
	Summary        OverviewSummary `json:"summary"`
	Services       []ServiceRow    `json:"services"`
	NeedsAttention []AttentionRow  `json:"needsAttention"`
	Namespaces     []NamespaceRow  `json:"namespaces"`
}

// ---- collection from the workspace ----

// configObjects are the kinds compared between clusters for drift.
var configObjects = []struct{ group, resource string }{
	{GatewayGroup, "httproutes"},
	{"security.istio.io", "authorizationpolicies"},
	{"security.istio.io", "peerauthentications"},
	{"security.istio.io", "requestauthentications"},
	{"networking.istio.io", "destinationrules"},
}

func specSignature(o *store.Object) string {
	b, _ := json.Marshal(o.Obj["spec"]) // map keys marshal sorted; metadata and status are excluded
	return o.Kind + "/" + o.Namespace + "/" + o.Name + "=" + string(b)
}

func collectFacts(ws *store.Workspace, svc *store.Object) (ServiceFacts, string) {
	var f ServiceFacts
	var sigs []string
	for _, p := range servicePolicies(ws, svc) {
		pf := PolicyFact{Kind: p.Ref.Kind, Action: p.Action, Enforced: p.Enforced, Warning: p.Warning != "", Scope: p.Scope}
		if p.Scope != "mesh" {
			f.Attached++
		}
		if o := ws.Get(p.Ref.Cluster, p.Ref.Group, p.Ref.Resource, p.Ref.Namespace, p.Ref.Name); o != nil {
			pf.MTLS = S(Path(o.Obj, "spec", "mtls", "mode"))
			sigs = append(sigs, specSignature(o))
		}
		f.Policies = append(f.Policies, pf)
	}
	wn, wp, _ := Waypoint(ws, svc)
	for _, o := range ws.List(svc.Cluster, GatewayGroup, "httproutes", "", nil) {
		if !related(o, svc, wn, wp) {
			continue
		}
		rf, ok := routeFact(o, svc)
		if !ok {
			continue
		}
		sigs = append(sigs, specSignature(o))
		f.Routes = append(f.Routes, rf)
		f.Attached++
	}
	for _, o := range ws.List(svc.Cluster, "networking.istio.io", "destinationrules", svc.Namespace, nil) {
		if !serviceHost(S(Path(o.Obj, "spec", "host")), svc) {
			continue
		}
		sigs = append(sigs, specSignature(o))
		f.Attached++
		tps := []any{Path(o.Obj, "spec", "trafficPolicy")}
		for _, v := range L(Path(o.Obj, "spec", "trafficPolicy", "portLevelSettings")) {
			tps = append(tps, v)
		}
		for _, v := range tps {
			tp := M(v)
			f.Outlier = f.Outlier || len(M(tp["outlierDetection"])) > 0
			f.CircuitBreaker = f.CircuitBreaker || len(M(tp["connectionPool"])) > 0
		}
	}
	for _, o := range ws.List(svc.Cluster, "networking.istio.io", "envoyfilters", svc.Namespace, nil) {
		if o.Labels()["flightbay.dev/resilience-service"] != svc.Name {
			continue
		}
		sigs = append(sigs, specSignature(o))
		f.Attached++
		b, _ := json.Marshal(o.Obj["spec"])
		text := string(b)
		f.RateLimit = f.RateLimit || strings.Contains(text, "ratelimit")
		f.Retries = f.Retries || strings.Contains(text, "retry")
	}
	for _, g := range []struct{ g, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, o := range ws.List(svc.Cluster, g.g, g.res, svc.Namespace, nil) {
			for _, t := range L(Path(o.Obj, "spec", "targetRefs")) {
				if S(M(t)["kind"]) == "Service" && S(M(t)["name"]) == svc.Name {
					sigs = append(sigs, specSignature(o))
					f.Attached++
					f.RateLimit = f.RateLimit || len(M(Path(o.Obj, "spec", "traffic", "rateLimit"))) > 0
				}
			}
		}
	}
	sort.Strings(sigs)
	return f, strings.Join(sigs, "\n")
}

// routeFact returns false when the route only reaches the Service through a
// waypoint-wide parent and does not name it, so it says nothing about it.
func routeFact(route, svc *store.Object) (RouteFact, bool) {
	var rf RouteFact
	parentName := ""
	for _, v := range L(Path(route.Obj, "spec", "parentRefs")) {
		p := M(v)
		_, k, ns, n := parent(p, route.Namespace)
		if k == "Service" && ns == svc.Namespace && n == svc.Name {
			rf.Frontend = true
		} else if parentName == "" {
			parentName = n
		}
	}
	isBackend := false
	for _, v := range L(Path(route.Obj, "spec", "rules")) {
		rule := M(v)
		for _, x := range L(rule["backendRefs"]) {
			b := M(x)
			if defaultString(S(b["namespace"]), route.Namespace) == svc.Namespace && S(b["name"]) == svc.Name {
				isBackend = true
			}
		}
		if !rf.Frontend {
			continue
		}
		if backends := L(rule["backendRefs"]); len(backends) > 1 && rf.Weights == nil {
			for _, x := range backends {
				w := 1
				if v, ok := M(x)["weight"]; ok {
					w = N(v)
				}
				rf.Weights = append(rf.Weights, w)
			}
		}
		for _, m := range L(rule["matches"]) {
			if len(L(M(m)["headers"])) > 0 {
				rf.HeaderMatch = true
			}
		}
		for _, fl := range L(rule["filters"]) {
			if S(M(fl)["type"]) == "RequestMirror" {
				rf.Mirror = true
			}
		}
		if len(M(rule["timeouts"])) > 0 {
			rf.Timeout = true
		}
		if len(M(rule["retry"])) > 0 || len(M(rule["retries"])) > 0 {
			rf.Retry = true
		}
	}
	if !rf.Frontend {
		if !isBackend {
			return rf, false
		}
		rf.Of = defaultString(parentName, route.Name)
	}
	return rf, true
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// namespaceFilter parses the optional namespace query: empty means every
// namespace, otherwise a comma-separated list.
func namespaceFilter(q string) map[string]bool {
	set := map[string]bool{}
	for _, n := range strings.Split(q, ",") {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// checkRank orders the checks of one entry so the headline is the root
// cause: data plane first (no Pods, no endpoints), then the waypoint, routes,
// backends and policy. Lower is more important.
func checkRank(id string) int {
	switch {
	case id == "workloads":
		return 0
	case id == "endpoints":
		return 1
	case id == "takeover-control-plane":
		return 2
	case strings.HasPrefix(id, "waypoint-status-"), strings.HasPrefix(id, "gateway-status-"), id == "waypoint", id == "waypoint-listener":
		return 3
	case id == "traffic-type", id == "waypoint-namespace", strings.HasPrefix(id, "waypoint-bound-"):
		return 4
	case strings.HasPrefix(id, "route-"):
		return 5
	case strings.HasPrefix(id, "backend-"):
		return 6
	case strings.HasPrefix(id, "policy-"), strings.HasPrefix(id, "agent-policy-"), strings.HasPrefix(id, "mtls-"):
		return 7
	case id == "require-waypoint", strings.HasPrefix(id, "jwt-"):
		return 8
	}
	return 9
}

func attentionState(stepState string) string {
	if stepState == "warning" || stepState == "warn" {
		return "warn"
	}
	return "fail"
}

// BuildAttention groups the issues list into one entry per object
// (kind/namespace/name, across clusters). Order: fail before warn, then more
// checks first, then namespace/name. namespace is an optional comma list.
func BuildAttention(issues []AmbientIssue, namespace string) []AttentionRow {
	filter := namespaceFilter(namespace)
	out := []AttentionRow{}
	index := map[string]int{}
	for _, is := range issues {
		if filter != nil && !filter[is.Subject.Namespace] {
			continue
		}
		detail := firstLine(is.Step.Detail)
		if detail == "" {
			detail = is.Step.Title
		}
		hasFix := is.Step.SuggestedFix != nil || is.Step.FixYAML != "" || len(is.Step.Actions) > 0
		check := AttentionCheck{ID: is.Step.ID, Title: is.Step.Title, Detail: detail, State: attentionState(is.Step.State), Cluster: is.Subject.Cluster, HasFix: hasFix}
		ref := is.Subject
		ref.Cluster = ""
		key := ref.Kind + "/" + ref.Namespace + "/" + ref.Name
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, AttentionRow{Ref: ref, Clusters: []string{}, Checks: []AttentionCheck{}})
		}
		out[i].Checks = append(out[i].Checks, check)
	}
	for i := range out {
		row := &out[i]
		sort.SliceStable(row.Checks, func(a, b int) bool {
			x, y := row.Checks[a], row.Checks[b]
			if x.State != y.State {
				return x.State == "fail"
			}
			if rx, ry := checkRank(x.ID), checkRank(y.ID); rx != ry {
				return rx < ry
			}
			if x.ID != y.ID {
				return x.ID < y.ID
			}
			return x.Cluster < y.Cluster
		})
		clusters, ids := map[string]bool{}, map[string]bool{}
		for _, c := range row.Checks {
			if !clusters[c.Cluster] {
				clusters[c.Cluster] = true
				row.Clusters = append(row.Clusters, c.Cluster)
			}
			ids[c.ID] = true
		}
		sort.Strings(row.Clusters)
		top := row.Checks[0]
		row.State, row.Detail, row.CheckID, row.HasFix = top.State, top.Detail, top.ID, top.HasFix
		row.Cluster = row.Clusters[0]
		row.More = len(ids) - 1
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return out[i].State == "fail"
		}
		if len(out[i].Checks) != len(out[j].Checks) {
			return len(out[i].Checks) > len(out[j].Checks)
		}
		a, b := out[i].Ref, out[j].Ref
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Kind < b.Kind
	})
	return out
}

// markDifferences sets Differs on every cell of a Service whose configuration
// signature is not identical on every cluster that has it. Single-cluster
// Services never differ.
func markDifferences(cells map[string]ClusterCell, sigs map[string]string) (map[string]ClusterCell, bool) {
	distinct := map[string]bool{}
	for _, s := range sigs {
		distinct[s] = true
	}
	drift := len(sigs) > 1 && len(distinct) > 1
	for c := range sigs {
		cell := cells[c]
		cell.Differs = drift
		cells[c] = cell
	}
	return cells, drift
}

// Overview builds the Ambient Overview payload. namespace (optional, a comma
// list) filters the services table and the attention list. The summary cards
// stay workspace-wide, except summary.needsAttention, which counts the
// returned attention entries so the card and the panel always agree.
func Overview(ws *store.Workspace, namespace string) ServicesOverview {
	out := ServicesOverview{Services: []ServiceRow{}, NeedsAttention: []AttentionRow{}, Namespaces: []NamespaceRow{}}
	filter := namespaceFilter(namespace)
	clusters := ws.Clusters()
	sort.Strings(clusters)
	issues := Issues(ws)
	topo := capturedTopology(ws)

	// Services, merged across clusters.
	type acc struct {
		row    *ServiceRow
		facts  []ServiceFacts
		sigs   map[string]string
		wpName string
	}
	rows := map[string]*acc{}
	order := []string{}
	svcPerNS := map[string]map[string]bool{}
	configuredPerNS := map[string]map[string]bool{}
	for _, tc := range topo.Clusters {
		for _, ts := range tc.Services {
			if ts.Infrastructure {
				continue
			}
			key := ts.Namespace + "/" + ts.Name
			if svcPerNS[ts.Namespace] == nil {
				svcPerNS[ts.Namespace] = map[string]bool{}
			}
			svcPerNS[ts.Namespace][ts.Name] = true
			svcObj := ws.Get(tc.Context, "", "services", ts.Namespace, ts.Name)
			var facts *ServiceFacts
			sig := ""
			if svcObj != nil {
				f, sg := collectFacts(ws, svcObj)
				facts, sig = &f, sg
				if f.Configured() {
					if configuredPerNS[ts.Namespace] == nil {
						configuredPerNS[ts.Namespace] = map[string]bool{}
					}
					configuredPerNS[ts.Namespace][ts.Name] = true
				}
			}
			if filter != nil && !filter[ts.Namespace] {
				continue
			}
			a := rows[key]
			if a == nil {
				a = &acc{row: &ServiceRow{Ref: Ref{Group: "", Resource: "services", Kind: "Service", Namespace: ts.Namespace, Name: ts.Name}, PerCluster: map[string]ClusterCell{}, Access: []Badge{}, Routing: []Badge{}, Resilience: []Badge{}}, sigs: map[string]string{}}
				for _, c := range clusters {
					a.row.PerCluster[c] = ClusterCell{}
				}
				rows[key] = a
				order = append(order, key)
			}
			wp := ""
			if ts.Waypoint != "" {
				wp = ts.Waypoint
				if ns, name, ok := strings.Cut(ts.Waypoint, "/"); ok && ns == ts.Namespace {
					wp = name
				}
			}
			cell := ClusterCell{Present: true, EndpointsReady: ts.Ready > 0, Waypoint: wp, NoEndpoints: ts.State == "fail"}
			for _, is := range issues {
				if is.Subject.Kind == "Service" && is.Subject.Cluster == tc.Context && is.Subject.Namespace == ts.Namespace && is.Subject.Name == ts.Name {
					if is.Step.State == "warning" || is.Step.State == "warn" {
						cell.Checks.Warn++
					} else {
						cell.Checks.Fail++
					}
				}
			}
			a.row.PerCluster[tc.Context] = cell
			a.row.Checks.Fail += cell.Checks.Fail
			a.row.Checks.Warn += cell.Checks.Warn
			if a.row.Waypoint == "" {
				a.row.Waypoint = wp
			}
			if facts != nil {
				a.facts = append(a.facts, *facts)
				a.sigs[tc.Context] = sig
				a.row.Configured = a.row.Configured || facts.Configured()
			}
			if serviceInAmbient(ws, tc.Context, svcObj, ts.Namespace) {
				a.row.InAmbient = true
			}
		}
	}
	sort.Strings(order)
	for _, key := range order {
		a := rows[key]
		a.row.Access, a.row.Routing, a.row.Resilience = mergeBadges(a.facts)
		a.row.PerCluster, a.row.Drift = markDifferences(a.row.PerCluster, a.sigs)
		out.Services = append(out.Services, *a.row)
	}
	out.NeedsAttention = BuildAttention(issues, namespace)

	// Namespaces.
	type nsAcc struct {
		inAmbient bool
		waypoint  string
		scope     string
	}
	nss := map[string]*nsAcc{}
	for _, c := range clusters {
		for _, n := range ws.List(c, "", "namespaces", "", nil) {
			a := nss[n.Name]
			if a == nil {
				a = &nsAcc{}
				nss[n.Name] = a
			}
			if n.Labels()["istio.io/dataplane-mode"] == "ambient" {
				a.inAmbient = true
			}
			if w := n.Labels()["istio.io/use-waypoint"]; w != "" && w != "none" {
				a.waypoint, a.scope = w, "namespace"
			}
		}
		for _, s := range ws.List(c, "", "services", "", nil) {
			a := nss[s.Namespace]
			if a == nil {
				continue
			}
			if w := s.Labels()["istio.io/use-waypoint"]; w != "" && w != "none" && a.scope == "" {
				a.waypoint, a.scope = w, "services"
			}
		}
	}
	names := []string{}
	for n := range nss {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := nss[n]
		out.Namespaces = append(out.Namespaces, NamespaceRow{Name: n, InAmbient: a.inAmbient, Waypoint: a.waypoint, WaypointScope: a.scope, Services: len(svcPerNS[n]), Configured: len(configuredPerNS[n]), System: IsSystemNamespace(n)})
		out.Summary.Namespaces.Total++
		if a.inAmbient {
			out.Summary.Namespaces.InAmbient++
		}
		if a.waypoint != "" {
			out.Summary.Namespaces.WithWaypoint++
		}
	}

	// Summary cards.
	s := &out.Summary
	s.Clusters.Count = len(clusters)
	s.Clusters.Names = append([]string{}, clusters...)
	s.Clusters.AmbientClusters = []string{}
	for _, a := range AvailabilityByCluster(ws) {
		if a.Available {
			s.Clusters.AmbientClusters = append(s.Clusters.AmbientClusters, a.Cluster)
		}
	}
	s.Clusters.AmbientOnAll = len(clusters) > 0 && len(s.Clusters.AmbientClusters) == len(clusters)
	s.PoliciesRoutes.Objects, s.PoliciesRoutes.Differ = countConfigObjects(ws, clusters)
	for _, row := range out.NeedsAttention {
		switch {
		case row.Ref.Kind != "Service":
			s.NeedsAttention.Other++
		case row.State == "fail":
			s.NeedsAttention.Fail++
		default:
			s.NeedsAttention.Warn++
		}
	}
	s.NeedsAttention.Services = s.NeedsAttention.Fail + s.NeedsAttention.Warn
	return out
}

// serviceInAmbient reports ambient enrolment of a Service on one cluster:
// its namespace or the Service itself carries istio.io/dataplane-mode=ambient.
func serviceInAmbient(ws *store.Workspace, cluster string, svc *store.Object, namespace string) bool {
	if svc != nil && svc.Labels()["istio.io/dataplane-mode"] == "ambient" {
		return true
	}
	n := ws.Get(cluster, "", "namespaces", "", namespace)
	return n != nil && n.Labels()["istio.io/dataplane-mode"] == "ambient"
}

// mergeBadges unions the badges of the same Service across clusters, keeping
// the first label order seen. Equal-label badges collapse.
func mergeBadges(facts []ServiceFacts) (access, routing, resilience []Badge) {
	access, routing, resilience = []Badge{}, []Badge{}, []Badge{}
	seen := [3]map[string]bool{{}, {}, {}}
	add := func(i int, dst *[]Badge, src []Badge) {
		for _, b := range src {
			if !seen[i][b.Label] {
				seen[i][b.Label] = true
				*dst = append(*dst, b)
			}
		}
	}
	for _, f := range facts {
		a, r, z := DeriveBadges(f)
		add(0, &access, a)
		add(1, &routing, r)
		add(2, &resilience, z)
	}
	return
}

// countConfigObjects is the Policies & routes summary: the same rows, and the
// same drift rule, as /api/ambient/policies with no filter (counts.all and
// counts.drift), so the two pages never disagree. clusters is always
// ws.Clusters() here, which is what the inventory uses.
func countConfigObjects(ws *store.Workspace, _ []string) (objects, differ int) {
	return PolicyInventorySummary(ws)
}
