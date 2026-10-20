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
}
type AttentionRow struct {
	State   string `json:"state"` // fail | warn
	Ref     Ref    `json:"ref"`
	Cluster string `json:"cluster"`
	Detail  string `json:"detail"`
	CheckID string `json:"checkId"`
	HasFix  bool   `json:"hasFix"`
}
type NamespaceRow struct {
	Name          string `json:"name"`
	InAmbient     bool   `json:"inAmbient"`
	Waypoint      string `json:"waypoint"`
	WaypointScope string `json:"waypointScope"` // "namespace" (all Services), "services" (some), or ""
	Services      int    `json:"services"`
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
	NeedsAttention struct {
		Services int `json:"services"`
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
		pf := PolicyFact{Kind: p.Ref.Kind, Action: p.Action, Enforced: p.Enforced, Warning: p.Warning != ""}
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
	}
	for _, o := range ws.List(svc.Cluster, "networking.istio.io", "destinationrules", svc.Namespace, nil) {
		if !serviceHost(S(Path(o.Obj, "spec", "host")), svc) {
			continue
		}
		sigs = append(sigs, specSignature(o))
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

// BuildAttention reshapes the issues list: fail before warn, then by subject.
func BuildAttention(issues []AmbientIssue, namespace string) []AttentionRow {
	out := []AttentionRow{}
	for _, is := range issues {
		if namespace != "" && is.Subject.Namespace != namespace {
			continue
		}
		state := "fail"
		if is.Step.State == "warning" || is.Step.State == "warn" {
			state = "warn"
		}
		detail := firstLine(is.Step.Detail)
		if detail == "" {
			detail = is.Step.Title
		}
		hasFix := is.Step.SuggestedFix != nil || is.Step.FixYAML != "" || len(is.Step.Actions) > 0
		out = append(out, AttentionRow{state, is.Subject, is.Subject.Cluster, detail, is.Step.ID, hasFix})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return out[i].State == "fail"
		}
		a, b := out[i].Ref, out[j].Ref
		return a.Namespace+"/"+a.Name+"/"+a.Cluster < b.Namespace+"/"+b.Name+"/"+b.Cluster
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

// Overview builds the Ambient Overview payload. namespace filters the services
// table and the attention list; the summary cards stay workspace-wide.
func Overview(ws *store.Workspace, namespace string) ServicesOverview {
	out := ServicesOverview{Services: []ServiceRow{}, NeedsAttention: []AttentionRow{}, Namespaces: []NamespaceRow{}}
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
			if namespace != "" && ts.Namespace != namespace {
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
			if svc := ws.Get(tc.Context, "", "services", ts.Namespace, ts.Name); svc != nil {
				f, sig := collectFacts(ws, svc)
				a.facts = append(a.facts, f)
				a.sigs[tc.Context] = sig
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
		out.Namespaces = append(out.Namespaces, NamespaceRow{Name: n, InAmbient: a.inAmbient, Waypoint: a.waypoint, WaypointScope: a.scope, Services: len(svcPerNS[n])})
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
	attention := map[string]bool{}
	for _, is := range issues {
		if is.Subject.Kind == "Service" {
			attention[is.Subject.Namespace+"/"+is.Subject.Name] = true
		}
	}
	s.NeedsAttention.Services = len(attention)
	return out
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

// countConfigObjects counts distinct policy/route objects (by kind, namespace
// and name across clusters) and how many exist on 2+ clusters with different
// specs.
func countConfigObjects(ws *store.Workspace, clusters []string) (objects, differ int) {
	sigs := map[string]map[string]bool{}
	seen := map[string]int{}
	for _, c := range clusters {
		for _, k := range configObjects {
			for _, o := range ws.List(c, k.group, k.resource, "", nil) {
				key := o.Kind + "/" + o.Namespace + "/" + o.Name
				if sigs[key] == nil {
					sigs[key] = map[string]bool{}
				}
				sigs[key][specSignature(o)] = true
				seen[key]++
			}
		}
	}
	for key, n := range seen {
		if n > 1 && len(sigs[key]) > 1 {
			differ++
		}
	}
	return len(seen), differ
}
