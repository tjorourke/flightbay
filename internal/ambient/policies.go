package ambient

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Policies & routes: one inventory of every ambient policy and route in the
// connected clusters. Each object is one row, with a state per cluster.
// Waypoint Gateways are infrastructure and live on the Map; they are only
// counted here (counts.waypoints).

// Types are the Type filter on the page. A row can carry several.
const (
	PolicyTypeAccess     = "Access"
	PolicyTypeRouting    = "Routing"
	PolicyTypeResilience = "Resilience"
	PolicyTypeTransport  = "Transport"
	PolicyTypeOther      = "Other"
)

var policyTypes = []string{PolicyTypeAccess, PolicyTypeRouting, PolicyTypeResilience, PolicyTypeTransport, PolicyTypeOther}

// Per-cluster states. Live means the enforcing component accepted the object;
// applied means it is present but no condition confirms it yet.
const (
	PolicyStateLive        = "live"
	PolicyStateApplied     = "applied"
	PolicyStateFailed      = "failed"
	PolicyStateNotEnforced = "not-enforced"
	PolicyStateDiffers     = "differs"
	PolicyStateNotHere     = "not-here"
)

// PolicyService names a Service a row reaches, for Open and Edit links.
type PolicyService struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// PolicyAppliesTo says what an object hits, in words and as structure.
// Kind is service, waypoint, pods, namespace, mesh or nothing. Reason is set
// for nothing ("Service reviews does not exist").
type PolicyAppliesTo struct {
	Phrase  string `json:"phrase"`
	Kind    string `json:"kind"`
	Targets []Ref  `json:"targets"`
	Reason  string `json:"reason,omitempty"`
}

// PolicyEffect is what the object does: a short phrase and the action pill.
type PolicyEffect struct {
	Phrase string `json:"phrase"`
	Action string `json:"action"`
}

type PolicyRow struct {
	Ref        Ref             `json:"ref"`
	Kind       string          `json:"kind"`
	AppliesTo  PolicyAppliesTo `json:"appliesTo"`
	Effect     PolicyEffect    `json:"effect"`
	Type       string          `json:"type"` // Types[0], kept for older clients
	Types      []string        `json:"types"`
	EnforcedBy string          `json:"enforcedBy"`
	// PerCluster is one of the PolicyState* values per connected cluster.
	PerCluster map[string]string `json:"perCluster"`
	// PerClusterDetail is the raw condition summary behind each state, for
	// the tooltip ("Accepted=True, ResolvedRefs=True").
	PerClusterDetail map[string]string `json:"perClusterDetail"`
	ManagedBy        string            `json:"managedBy"`
	ManagedByLabel   string            `json:"managedByLabel"`
	Services         []string          `json:"services"`
	Problems         []string          `json:"problems"`
	Drift            bool              `json:"drift"`
	CanTest          bool              `json:"canTest"`
	Editable         bool              `json:"editable"`
	EditKey          string            `json:"editKey"`
	Svc              *PolicyService    `json:"svc,omitempty"`

	groupKey string
	nsNames  []string // namespaces of the Services reached, for the namespace filter
}

type PolicyGroup struct {
	Key   string         `json:"key"`
	Title string         `json:"title"`
	Kind  string         `json:"kind"` // service, namespace, mesh
	Svc   *PolicyService `json:"svc,omitempty"`
	Rows  []PolicyRow    `json:"rows"`
}

type PolicyNamespaceCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// PolicyCounts are taken before any filter, so the chips show what each
// filter would leave. Shown is the size of the filtered set.
type PolicyCounts struct {
	All      int `json:"all"`
	Shown    int `json:"shown"`
	Problems int `json:"problems"`
	Drift    int `json:"drift"`
	// Waypoints counts the waypoint Gateways in the namespace scope. They are
	// not rows: the page links to the Map for them.
	Waypoints  int                    `json:"waypoints"`
	ByType     map[string]int         `json:"byType"`
	Namespaces []PolicyNamespaceCount `json:"namespaces"`
}

type PoliciesResponse struct {
	Rows     []PolicyRow   `json:"rows"`
	Groups   []PolicyGroup `json:"groups"`
	Counts   PolicyCounts  `json:"counts"`
	Clusters []string      `json:"clusters"`
}

// PolicyFilter holds the query parameters. Empty means no restriction.
type PolicyFilter struct {
	Namespaces []string
	Types      []string
	ManagedBy  []string
	Problems   bool
	Drift      bool
	Group      string // object (default) or service
	Q          string
}

// ---- building ----------------------------------------------------------------

type polObs struct {
	obj        *store.Object
	types      []string
	applies    PolicyAppliesTo
	effect     PolicyEffect
	enforcedBy string
	state      string
	detail     string // raw condition summary for the tooltip
	problems   []string
	services   []*store.Object
	primary    *store.Object // the Service the row groups under
	grouped    bool          // placed in a Service group rather than a namespace group
}

type polCtx struct {
	ws       *store.Workspace
	clusters []string
	reach    map[string]map[string][]*store.Object // cluster -> kind/ns/name -> Services reached
	warn     map[string]map[string][]string        // cluster -> kind/ns/name -> not-enforced warnings
	bound    map[string]map[string][]*store.Object // cluster -> ns/gateway -> Services bound
	editable map[string][]Editable                 // cluster/ns/svc -> editable configurations
}

func objKey(kind, ns, name string) string { return kind + "/" + ns + "/" + name }

func svcRef(o *store.Object) *PolicyService {
	return &PolicyService{Cluster: o.Cluster, Namespace: o.Namespace, Name: o.Name}
}

// NewPolicies builds the inventory.
func NewPolicies(ws *store.Workspace, f PolicyFilter) *PoliciesResponse {
	inv := buildPolicyInventory(ws, true)
	all := inv.rows

	out := &PoliciesResponse{Clusters: inv.clusters, Groups: []PolicyGroup{}}
	if out.Clusters == nil {
		out.Clusters = []string{}
	}
	out.Counts = PolicyCounts{ByType: map[string]int{}, Namespaces: []PolicyNamespaceCount{}}
	for _, t := range policyTypes {
		out.Counts.ByType[t] = 0
	}
	nsCount := map[string]int{}
	for _, r := range all {
		out.Counts.All++
		for _, t := range r.Types {
			out.Counts.ByType[t]++
		}
		nsCount[r.Ref.Namespace]++
		if r.hasProblem() {
			out.Counts.Problems++
		}
		if r.Drift {
			out.Counts.Drift++
		}
	}
	for _, ns := range inv.waypoints {
		if len(f.Namespaces) == 0 || contains(f.Namespaces, ns) {
			out.Counts.Waypoints++
		}
	}
	for n, k := range nsCount {
		out.Counts.Namespaces = append(out.Counts.Namespaces, PolicyNamespaceCount{n, k})
	}
	sort.Slice(out.Counts.Namespaces, func(i, j int) bool { return out.Counts.Namespaces[i].Name < out.Counts.Namespaces[j].Name })

	out.Rows = []PolicyRow{}
	for _, r := range all {
		if f.keep(r) {
			out.Rows = append(out.Rows, r)
		}
	}
	out.Counts.Shown = len(out.Rows)
	if f.Group == "service" {
		out.Groups = groupRows(out.Rows)
	}
	return out
}

// PolicyInventorySummary is what the Ambient Overview shows for Policies & routes:
// the number of objects (equal to counts.all of the inventory with no
// filter) and how many of them drift between clusters (counts.drift). Both
// come from the same rows as the Policies & routes page.
func PolicyInventorySummary(ws *store.Workspace) (objects, drift int) {
	for _, r := range buildPolicyInventory(ws, false).rows {
		objects++
		if r.Drift {
			drift++
		}
	}
	return objects, drift
}

type policyInventory struct {
	clusters  []string
	rows      []PolicyRow
	waypoints map[string]string // Gateway/ns/name -> namespace, across clusters
}

// buildPolicyInventory merges every covered object across clusters into one
// row each. edit also works out editability, which only the page needs.
func buildPolicyInventory(ws *store.Workspace, edit bool) *policyInventory {
	c := &polCtx{ws: ws, clusters: ws.Clusters(), reach: map[string]map[string][]*store.Object{}, warn: map[string]map[string][]string{}, bound: map[string]map[string][]*store.Object{}, editable: map[string][]Editable{}}
	for _, cl := range c.clusters {
		c.index(cl)
	}
	inv := &policyInventory{clusters: c.clusters, waypoints: map[string]string{}}
	type held struct {
		cluster string
		obs     polObs
	}
	order := []string{}
	byKey := map[string][]held{}
	for _, cl := range c.clusters {
		for _, gw := range c.ws.List(cl, GatewayGroup, "gateways", "", nil) {
			if c.isWaypoint(cl, gw) {
				inv.waypoints[objKey("Gateway", gw.Namespace, gw.Name)] = gw.Namespace
			}
		}
		for _, o := range c.inventory(cl) {
			ob := c.describe(cl, o)
			key := objKey(o.Kind, o.Namespace, o.Name)
			if _, ok := byKey[key]; !ok {
				order = append(order, key)
			}
			byKey[key] = append(byKey[key], held{cl, ob})
		}
	}
	for _, key := range order {
		hs := byKey[key]
		p := hs[0].obs
		code, label := ManagedByInfo(p.obj)
		row := PolicyRow{Ref: reference(p.obj), Kind: p.obj.Kind, AppliesTo: p.applies, Effect: p.effect, Types: p.types, EnforcedBy: p.enforcedBy, ManagedBy: code, ManagedByLabel: label, PerCluster: map[string]string{}, PerClusterDetail: map[string]string{}, Problems: []string{}, Services: []string{}}
		if len(row.Types) == 0 {
			row.Types = []string{PolicyTypeOther}
		}
		row.Type = row.Types[0]
		row.groupKey = c.groupKey(p)
		seen := map[string]bool{}
		holders := map[string]bool{}
		refSpec := policySpecKey(p.obj)
		for i, h := range hs {
			holders[h.cluster] = true
			st, detail := h.obs.state, h.obs.detail
			if i > 0 && policySpecKey(h.obs.obj) != refSpec {
				row.Drift = true
				if st == PolicyStateLive || st == PolicyStateApplied {
					st = PolicyStateDiffers
				}
				detail = strings.TrimPrefix(detail+"; spec differs from "+hs[0].cluster, "; ")
			}
			row.PerCluster[h.cluster] = st
			row.PerClusterDetail[h.cluster] = detail
			for _, pr := range h.obs.problems {
				if i > 0 {
					pr = "On " + h.cluster + ": " + pr
				}
				if !seen[pr] {
					seen[pr] = true
					row.Problems = append(row.Problems, pr)
				}
			}
			for _, s := range h.obs.services {
				n := s.Name
				if s.Namespace != p.obj.Namespace {
					n = s.Namespace + "/" + s.Name
				}
				if !contains(row.Services, n) {
					row.Services = append(row.Services, n)
				}
				if !contains(row.nsNames, s.Namespace) {
					row.nsNames = append(row.nsNames, s.Namespace)
				}
			}
		}
		for _, cl := range c.clusters {
			if holders[cl] {
				continue
			}
			row.PerCluster[cl] = PolicyStateNotHere
			row.PerClusterDetail[cl] = "Not on this cluster"
			// Absence is drift only when a Service the object applies to
			// runs on both clusters. Otherwise it is a different estate.
			for _, h := range hs {
				if c.servesOnBoth(h.obs, cl) {
					row.Drift = true
					break
				}
			}
		}
		sort.Strings(row.Services)
		if p.primary != nil {
			row.Svc = svcRef(p.primary)
		} else if len(p.services) > 0 {
			row.Svc = svcRef(p.services[0])
		}
		row.CanTest = (row.Kind == "AuthorizationPolicy" || row.Kind == "HTTPRoute") && row.Svc != nil && p.applies.Kind == "service"
		row.EditKey = EditKeyFor(row.Ref, 0)
		if edit {
			row.Editable = c.editability(&row, p)
		}
		inv.rows = append(inv.rows, row)
	}
	sortRows(inv.rows)
	return inv
}

// servesOnBoth is true when one of the Services the object applies to has
// ready endpoints on the holder's cluster and on cluster other.
func (c *polCtx) servesOnBoth(ob polObs, other string) bool {
	cands := ob.services
	if ob.primary != nil {
		cands = []*store.Object{ob.primary}
	}
	for _, s := range cands {
		if readyEndpoints(c.ws, s) == 0 {
			continue
		}
		if o := c.ws.Get(other, "", "services", s.Namespace, s.Name); o != nil && readyEndpoints(c.ws, o) > 0 {
			return true
		}
	}
	return false
}

func (r PolicyRow) hasProblem() bool {
	if len(r.Problems) > 0 {
		return true
	}
	for _, s := range r.PerCluster {
		if s == PolicyStateNotEnforced || s == PolicyStateFailed {
			return true
		}
	}
	return false
}

func (f PolicyFilter) keep(r PolicyRow) bool {
	if len(f.Namespaces) > 0 {
		ok := r.AppliesTo.Kind == "mesh" || contains(f.Namespaces, r.Ref.Namespace)
		for _, n := range r.nsNames {
			ok = ok || contains(f.Namespaces, n)
		}
		if !ok {
			return false
		}
	}
	if len(f.Types) > 0 {
		ok := false
		for _, t := range f.Types {
			for _, rt := range r.Types {
				ok = ok || strings.EqualFold(t, rt)
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.ManagedBy) > 0 {
		ok := false
		for _, m := range f.ManagedBy {
			ok = ok || strings.EqualFold(m, r.ManagedBy) || strings.EqualFold(m, r.ManagedByLabel)
		}
		if !ok {
			return false
		}
	}
	if f.Problems && !r.hasProblem() {
		return false
	}
	if f.Drift && !r.Drift {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Q)); q != "" {
		hay := []string{r.Ref.Name, r.Ref.Namespace, r.Kind, r.AppliesTo.Phrase, r.AppliesTo.Reason, r.Effect.Phrase, r.Effect.Action, r.EnforcedBy, r.ManagedByLabel}
		hay = append(hay, r.Types...)
		if r.AppliesTo.Kind != "mesh" && r.AppliesTo.Kind != "namespace" {
			hay = append(hay, r.Services...) // a wide policy would match every Service name
		}
		for _, t := range r.AppliesTo.Targets {
			hay = append(hay, t.Name)
		}
		hit := false
		for _, h := range hay {
			hit = hit || strings.Contains(strings.ToLower(h), q)
		}
		if !hit {
			return false
		}
	}
	return true
}

var kindOrder = map[string]int{"Gateway": 0, "HTTPRoute": 1, "DestinationRule": 2, "AuthorizationPolicy": 3, "RequestAuthentication": 4, "PeerAuthentication": 5}

func sortRows(rows []PolicyRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Ref.Namespace != b.Ref.Namespace {
			return a.Ref.Namespace < b.Ref.Namespace
		}
		if a.Kind != b.Kind {
			ka, oka := kindOrder[a.Kind]
			kb, okb := kindOrder[b.Kind]
			if oka != okb {
				return oka
			}
			if ka != kb {
				return ka < kb
			}
			return a.Kind < b.Kind
		}
		return a.Ref.Name < b.Ref.Name
	})
}

// groupKey places a row: a Service group when it is about one Service, the
// namespace group for namespace-wide and loose objects, the mesh group last.
func (c *polCtx) groupKey(p polObs) string {
	switch {
	case p.applies.Kind == "mesh":
		return "mesh"
	case p.applies.Kind == "namespace":
		return "namespace:" + p.obj.Namespace
	case p.grouped && p.primary != nil:
		return "service:" + p.primary.Namespace + "/" + p.primary.Name
	}
	return "namespace:" + p.obj.Namespace
}

func groupRows(rows []PolicyRow) []PolicyGroup {
	by := map[string]*PolicyGroup{}
	keys := []string{}
	for _, r := range rows {
		g := by[r.groupKey]
		if g == nil {
			g = &PolicyGroup{Key: r.groupKey, Rows: []PolicyRow{}}
			switch {
			case r.groupKey == "mesh":
				g.Title, g.Kind = "Whole mesh", "mesh"
			case strings.HasPrefix(r.groupKey, "service:"):
				g.Title, g.Kind = strings.TrimPrefix(r.groupKey, "service:"), "service"
				g.Svc = r.Svc
			default:
				g.Title, g.Kind = "Namespace "+strings.TrimPrefix(r.groupKey, "namespace:"), "namespace"
			}
			by[r.groupKey] = g
			keys = append(keys, r.groupKey)
		}
		g.Rows = append(g.Rows, r)
	}
	rank := func(k string) int {
		switch {
		case strings.HasPrefix(k, "service:"):
			return 0
		case k == "mesh":
			return 2
		}
		return 1
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	out := make([]PolicyGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, *by[k])
	}
	return out
}

// ---- per-cluster indexes -----------------------------------------------------

func isWaypointService(s *store.Object) bool {
	return s.Labels()["gateway.networking.k8s.io/gateway-name"] != ""
}

// index runs the existing per-Service classification once for every Service,
// to learn which Services each policy reaches and whether it is enforced.
func (c *polCtx) index(cl string) {
	c.reach[cl], c.warn[cl], c.bound[cl] = map[string][]*store.Object{}, map[string][]string{}, map[string][]*store.Object{}
	for _, svc := range c.ws.List(cl, "", "services", "", nil) {
		if isWaypointService(svc) {
			continue
		}
		for _, e := range servicePolicies(c.ws, svc) {
			k := objKey(e.Ref.Kind, e.Ref.Namespace, e.Ref.Name)
			c.reach[cl][k] = append(c.reach[cl][k], svc)
			if e.Warning != "" {
				c.warn[cl][k] = append(c.warn[cl][k], e.Warning)
			}
		}
		if ns, name, _ := Waypoint(c.ws, svc); name != "" {
			k := ns + "/" + name
			c.bound[cl][k] = append(c.bound[cl][k], svc)
		}
	}
}

func (c *polCtx) isWaypoint(cl string, gw *store.Object) bool {
	return gw.Labels()["istio.io/waypoint-for"] != "" || strings.Contains(S(Path(gw.Obj, "spec", "gatewayClassName")), "waypoint") || len(c.bound[cl][gw.Namespace+"/"+gw.Name]) > 0
}

// policyKinds are the kinds the inventory covers, with where they are stored.
// Waypoint Gateways are deliberately absent: they are on the Map.
var policyKinds = []struct{ kind, group, resource string }{
	{"AuthorizationPolicy", "security.istio.io", "authorizationpolicies"},
	{"PeerAuthentication", "security.istio.io", "peerauthentications"},
	{"RequestAuthentication", "security.istio.io", "requestauthentications"},
	{"HTTPRoute", GatewayGroup, "httproutes"},
	{"DestinationRule", "networking.istio.io", "destinationrules"},
	{"AgentgatewayPolicy", "agentgateway.dev", "agentgatewaypolicies"},
	{"EnterpriseAgentgatewayPolicy", "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"},
	{"EnvoyFilter", "networking.istio.io", "envoyfilters"},
}

// inPolicyInventory says whether an object of a covered kind is listed.
// Agentgateway policies are kept only when aimed at a waypoint; EnvoyFilters
// only when Flightbay wrote them for a Service's resilience settings.
func (c *polCtx) inPolicyInventory(cl string, o *store.Object) bool {
	switch o.Kind {
	case "AgentgatewayPolicy", "EnterpriseAgentgatewayPolicy":
		return c.targetsWaypoint(cl, o)
	case "EnvoyFilter":
		return flightbayOwned(o) && o.Labels()["flightbay.dev/resilience-service"] != ""
	}
	return true
}

// inventory lists the objects of every covered kind in one cluster.
func (c *polCtx) inventory(cl string) []*store.Object {
	var out []*store.Object
	for _, k := range policyKinds {
		for _, o := range c.ws.List(cl, k.group, k.resource, "", nil) {
			if c.inPolicyInventory(cl, o) {
				out = append(out, o)
			}
		}
	}
	return out
}

// targetsWaypoint keeps agentgateway policies aimed at a waypoint (or at a
// Service, which reaches its waypoint) and drops those for ingress gateways.
// A policy whose Gateway does not exist is kept so it can be flagged.
func (c *polCtx) targetsWaypoint(cl string, o *store.Object) bool {
	for _, t := range policyTargetRefs(o) {
		switch S(t["kind"]) {
		case "Gateway":
			gw := c.ws.Get(cl, GatewayGroup, "gateways", o.Namespace, S(t["name"]))
			if gw == nil || c.isWaypoint(cl, gw) {
				return true
			}
		case "Service":
			if c.ws.Get(cl, "", "services", o.Namespace, S(t["name"])) != nil {
				return true
			}
		}
	}
	return false
}

func policyTargetRefs(o *store.Object) []map[string]any {
	var out []map[string]any
	for _, t := range L(Path(o.Obj, "spec", "targetRefs")) {
		if m := M(t); m != nil {
			out = append(out, m)
		}
	}
	if tr := M(Path(o.Obj, "spec", "targetRef")); tr != nil {
		out = append(out, tr)
	}
	return out
}

// absent is true only when the capture is complete enough to say the object
// does not exist, not merely that it was not collected.
func (c *polCtx) absent(cl, g, res, kind, ns string) bool {
	return missing(c.ws, cl, g, res, kind, ns) == "fail"
}

// ---- describing --------------------------------------------------------------

func (c *polCtx) describe(cl string, o *store.Object) polObs {
	ob := polObs{obj: o, state: PolicyStateApplied, applies: PolicyAppliesTo{Targets: []Ref{}}}
	switch o.Kind {
	case "AuthorizationPolicy", "RequestAuthentication", "PeerAuthentication":
		c.describeSecurity(cl, &ob)
	case "HTTPRoute":
		c.describeRoute(cl, &ob)
	case "DestinationRule":
		c.describeDestinationRule(cl, &ob)
	case "EnvoyFilter":
		c.describeEnvoyFilter(cl, &ob)
	default:
		c.describeAgentgateway(cl, &ob)
	}
	if ob.detail == "" {
		ob.detail = "No status conditions"
	}
	k := objKey(o.Kind, o.Namespace, o.Name)
	switch o.Kind {
	case "AuthorizationPolicy", "RequestAuthentication", "PeerAuthentication", "AgentgatewayPolicy", "EnterpriseAgentgatewayPolicy":
		ob.services = c.reach[cl][k]
		if ws := c.warn[cl][k]; len(ws) > 0 && len(ws) == len(ob.services) && ob.state != PolicyStateFailed {
			ob.state = PolicyStateNotEnforced
			ob.problems = append(ob.problems, ws[0])
		}
	}
	// An object that targets nothing is not enforced. appliesTo carries the
	// reason, so no separate problem repeats it (one marker in the UI).
	if ob.applies.Kind == "nothing" {
		ob.state = PolicyStateNotEnforced
	}
	if ob.applies.Kind == "service" || ob.applies.Kind == "pods" {
		if len(ob.services) > 0 && ob.primary == nil {
			ob.primary = ob.services[0]
		}
		ob.grouped = ob.primary != nil
	}
	return ob
}

func (c *polCtx) ref(cl, g, res, kind, ns, name string) Ref {
	return Ref{Cluster: cl, Group: g, Resource: res, Kind: kind, Namespace: ns, Name: name}
}

type targetResult struct {
	targets []Ref
	missing []string
	first   *store.Object // first Service target that exists
	svcs    []*store.Object
	gws     []*store.Object
}

// resolve checks each targetRef-like (kind, name) in the object's namespace.
func (c *polCtx) resolve(cl, ns string, refs []map[string]any) targetResult {
	var r targetResult
	for _, t := range refs {
		kind, name := S(t["kind"]), S(t["name"])
		switch kind {
		case "Service":
			tr := c.ref(cl, "", "services", "Service", ns, name)
			r.targets = append(r.targets, tr)
			if s := c.ws.Get(cl, "", "services", ns, name); s != nil {
				r.svcs = append(r.svcs, s)
				if r.first == nil {
					r.first = s
				}
			} else if c.absent(cl, "", "services", "Service", ns) {
				r.missing = append(r.missing, "Service "+name+" does not exist")
			}
		case "Gateway":
			tr := c.ref(cl, GatewayGroup, "gateways", "Gateway", ns, name)
			r.targets = append(r.targets, tr)
			if g := c.ws.Get(cl, GatewayGroup, "gateways", ns, name); g != nil {
				r.gws = append(r.gws, g)
			} else if c.absent(cl, GatewayGroup, "gateways", "Gateway", ns) {
				r.missing = append(r.missing, "Gateway "+name+" does not exist")
			}
		}
	}
	return r
}

func (c *polCtx) setTargets(cl string, ob *polObs, refs []map[string]any) {
	o := ob.obj
	r := c.resolve(cl, o.Namespace, refs)
	ob.applies.Targets = r.targets
	switch {
	case len(r.targets) > 0 && len(r.missing) == len(r.targets):
		ob.applies.Kind, ob.applies.Reason = "nothing", strings.Join(r.missing, "; ")
		ob.applies.Phrase = "Targets nothing"
	case len(r.gws) > 0 && len(r.svcs) == 0:
		ob.applies.Kind = "waypoint"
		ob.applies.Phrase = "Waypoint " + r.gws[0].Name
	default:
		ob.applies.Kind = "service"
		names := []string{}
		for _, t := range r.targets {
			names = append(names, t.Name)
		}
		ob.applies.Phrase = "Service " + strings.Join(names, ", ")
		ob.primary = r.first
	}
	for _, m := range r.missing {
		if ob.applies.Kind != "nothing" {
			ob.problems = append(ob.problems, m)
		}
	}
}

func (c *polCtx) describeSecurity(cl string, ob *polObs) {
	o := ob.obj
	root := rootNamespace(c.ws, cl)
	targets := policyTargetRefs(o)
	sel := M(Path(o.Obj, "spec", "selector", "matchLabels"))
	switch {
	case len(targets) > 0:
		c.setTargets(cl, ob, targets)
		ob.enforcedBy = "waypoint"
	case len(sel) > 0:
		lbl := []string{}
		for k, v := range sel {
			lbl = append(lbl, k+"="+S(v))
		}
		sort.Strings(lbl)
		ob.applies.Kind, ob.applies.Phrase, ob.enforcedBy = "pods", "Pods "+strings.Join(lbl, ","), "ztunnel"
		hit, any := false, false
		for _, p := range c.ws.List(cl, "", "pods", o.Namespace, nil) {
			any = true
			if matches(sel, p.Labels()) {
				hit = true
				break
			}
		}
		if any && !hit {
			ob.applies.Kind, ob.applies.Reason = "nothing", "Selector "+strings.Join(lbl, ",")+" matches no Pods in "+o.Namespace
			ob.applies.Phrase = "Targets nothing"
		}
	case o.Namespace == root:
		ob.applies.Kind, ob.applies.Phrase, ob.enforcedBy = "mesh", "Whole mesh", "ztunnel"
	default:
		ob.applies.Kind, ob.applies.Phrase, ob.enforcedBy = "namespace", "Namespace "+o.Namespace, "ztunnel"
	}
	switch o.Kind {
	case "AuthorizationPolicy":
		ob.types = []string{PolicyTypeAccess}
		ob.effect = authzEffect(o)
	case "RequestAuthentication":
		ob.types = []string{PolicyTypeAccess}
		var issuers []string
		for _, r := range L(Path(o.Obj, "spec", "jwtRules")) {
			issuers = append(issuers, S(M(r)["issuer"]))
		}
		ph := "Validate JWTs"
		if len(issuers) > 0 {
			ph += " from " + issuers[0]
			if len(issuers) > 1 {
				ph += fmt.Sprintf(" (+%d more)", len(issuers)-1)
			}
		}
		ob.effect = PolicyEffect{Phrase: ph, Action: "JWT"}
	case "PeerAuthentication":
		ob.types = []string{PolicyTypeTransport}
		mode := defaultString(S(Path(o.Obj, "spec", "mtls", "mode")), "UNSET")
		ph := "mTLS " + mode
		if n := len(M(Path(o.Obj, "spec", "portLevelMtls"))); n > 0 {
			ph += fmt.Sprintf(" (+%d port settings)", n)
		}
		ob.effect = PolicyEffect{Phrase: ph, Action: mode}
		ob.enforcedBy = "ztunnel"
	}
	// Istio reports WaypointAccepted on policies a waypoint enforces and
	// ZtunnelAccepted on those ztunnel enforces.
	conds := L(Path(o.Obj, "status", "conditions"))
	ob.detail = condSummary([][]any{conds}, "WaypointAccepted", "ZtunnelAccepted")
	wa, war, wam := condStatus(conds, "WaypointAccepted")
	za, zar, zam := condStatus(conds, "ZtunnelAccepted")
	switch {
	case wa == "False":
		ob.state = PolicyStateFailed
		ob.problems = append(ob.problems, strings.TrimSpace("Not accepted by the waypoint: "+war+" "+wam))
	case za == "False":
		ob.state = PolicyStateFailed
		ob.problems = append(ob.problems, strings.TrimSpace("Not accepted by ztunnel: "+zar+" "+zam))
	case ob.enforcedBy == "waypoint" && wa != "True":
		ob.state = PolicyStateApplied
	default:
		ob.state = PolicyStateLive
	}
}

func authzEffect(o *store.Object) PolicyEffect {
	action := defaultString(S(Path(o.Obj, "spec", "action")), "ALLOW")
	rules := L(Path(o.Obj, "spec", "rules"))
	e := PolicyEffect{Action: action}
	switch {
	case action == "CUSTOM":
		e.Phrase = "Check with " + defaultString(S(Path(o.Obj, "spec", "provider", "name")), "an external provider")
	case len(rules) == 0 && action == "ALLOW":
		e.Phrase = "nothing is allowed (default deny)"
	case len(rules) == 0:
		e.Phrase = "no rules, matches nothing"
	default:
		if a, ok := accessFromPolicy(o); ok && len(a.Rules) > 0 {
			l := accessLabel(action, a.Rules)
			for _, p := range []string{"Block ", "Allow "} {
				l = strings.TrimPrefix(l, p)
			}
			e.Phrase = l
		} else {
			e.Phrase = fmt.Sprintf("%d rules", len(rules))
		}
	}
	return e
}

func (c *polCtx) describeRoute(cl string, ob *polObs) {
	o := ob.obj
	ob.types, ob.enforcedBy = []string{PolicyTypeRouting}, "waypoint"
	for _, rule := range L(Path(o.Obj, "spec", "rules")) {
		if len(M(M(rule)["timeouts"])) > 0 || len(M(M(rule)["retry"])) > 0 {
			ob.types = append(ob.types, PolicyTypeResilience)
			break
		}
	}
	var refs []map[string]any
	var ports []int
	for _, v := range L(Path(o.Obj, "spec", "parentRefs")) {
		g, k, ns, n := parent(M(v), o.Namespace)
		if g == "" && k == "Service" {
			refs = append(refs, map[string]any{"kind": "Service", "name": n})
			ports = append(ports, N(M(v)["port"]))
			_ = ns
		} else if g == GatewayGroup && k == "Gateway" {
			refs = append(refs, map[string]any{"kind": "Gateway", "name": n})
			ports = append(ports, 0)
		}
	}
	// Parents in another namespace are resolved there.
	r := targetResult{}
	for i, t := range refs {
		pns := o.Namespace
		for _, v := range L(Path(o.Obj, "spec", "parentRefs")) {
			if _, _, ns, n := parent(M(v), o.Namespace); n == S(t["name"]) {
				pns = ns
			}
		}
		x := c.resolve(cl, pns, []map[string]any{t})
		r.targets = append(r.targets, x.targets...)
		r.missing = append(r.missing, x.missing...)
		r.svcs = append(r.svcs, x.svcs...)
		r.gws = append(r.gws, x.gws...)
		if r.first == nil {
			r.first = x.first
		}
		_ = i
	}
	ob.applies.Targets = r.targets
	switch {
	case len(r.targets) == 0:
		ob.applies.Kind, ob.applies.Reason, ob.applies.Phrase = "nothing", "The route has no Service or Gateway parent", "Targets nothing"
	case len(r.missing) == len(r.targets):
		ob.applies.Kind, ob.applies.Reason, ob.applies.Phrase = "nothing", strings.Join(r.missing, "; "), "Targets nothing"
	case len(r.svcs) == 0 && len(r.gws) > 0:
		ob.applies.Kind, ob.applies.Phrase = "waypoint", "Waypoint "+r.gws[0].Name
		for _, g := range r.gws {
			ob.services = append(ob.services, c.bound[cl][g.Namespace+"/"+g.Name]...)
		}
	case len(r.svcs) == 0:
		ob.applies.Kind, ob.applies.Phrase = "service", "Service "+r.targets[0].Name
	default:
		ob.applies.Kind = "service"
		ob.applies.Phrase = "Service " + r.svcs[0].Name
		if len(r.svcs) > 1 {
			ob.applies.Phrase = fmt.Sprintf("Services %s (+%d)", r.svcs[0].Name, len(r.svcs)-1)
		}
		if ports[0] > 0 && len(r.svcs) == 1 {
			ob.applies.Phrase += fmt.Sprintf(" :%d", ports[0])
		}
		ob.primary = r.first
		ob.services = append(ob.services, r.svcs...)
	}
	if ob.applies.Kind != "nothing" {
		for _, m := range r.missing {
			ob.problems = append(ob.problems, m)
		}
	}
	// Status the controller wrote: live once every parent reports Accepted
	// and ResolvedRefs True, failed when either is False.
	parents := L(Path(o.Obj, "status", "parents"))
	var groups [][]any
	confirmed := len(parents) > 0
	failed := false
	for _, p := range parents {
		conds := L(M(p)["conditions"])
		groups = append(groups, conds)
		acc, ar, am := condStatus(conds, "Accepted")
		res, rr, rm := condStatus(conds, "ResolvedRefs")
		if acc == "False" {
			failed = true
			ob.problems = append(ob.problems, strings.TrimSpace("Not accepted: "+ar+" "+am))
		}
		if res == "False" {
			failed = true
			ob.problems = append(ob.problems, strings.TrimSpace("Backends not resolved: "+rr+" "+rm))
		}
		if acc != "True" || res != "True" {
			confirmed = false
		}
	}
	ob.detail = condSummary(groups, "Accepted", "ResolvedRefs")
	switch {
	case failed:
		ob.state = PolicyStateFailed
	case confirmed:
		ob.state = PolicyStateLive
	default:
		ob.state = PolicyStateApplied
	}
	// A Service-parent route is only enforced by a waypoint.
	if ob.applies.Kind == "service" && ob.state != PolicyStateFailed {
		for _, s := range r.svcs {
			if policyAmbient(c.ws, s) {
				if _, wp, _ := Waypoint(c.ws, s); wp == "" {
					ob.state = PolicyStateNotEnforced
					ob.problems = append(ob.problems, "Service "+s.Name+" has no waypoint, so nothing enforces this route")
					break
				}
			}
		}
	}
	// Backends that do not exist.
	for _, rule := range L(Path(o.Obj, "spec", "rules")) {
		for _, b := range L(M(rule)["backendRefs"]) {
			bm := M(b)
			if S(bm["group"]) != "" || defaultString(S(bm["kind"]), "Service") != "Service" {
				continue
			}
			ns := defaultString(S(bm["namespace"]), o.Namespace)
			if c.ws.Get(cl, "", "services", ns, S(bm["name"])) == nil && c.absent(cl, "", "services", "Service", ns) {
				p := "Backend Service " + S(bm["name"]) + " does not exist"
				if !contains(ob.problems, p) {
					ob.problems = append(ob.problems, p)
				}
			}
		}
	}
	ob.effect = routeEffect(o)
}

func routeEffect(o *store.Object) PolicyEffect {
	rules := L(Path(o.Obj, "spec", "rules"))
	var split, mirror, timeout, retry, route PolicyEffect
	for _, rv := range rules {
		rule := M(rv)
		var bs []Backend
		for _, bv := range L(rule["backendRefs"]) {
			x := M(bv)
			w := 1
			if v, ok := x["weight"]; ok {
				w = N(v)
			}
			bs = append(bs, Backend{Name: S(x["name"]), Port: N(x["port"]), Weight: w})
		}
		if len(bs) > 1 && split.Action == "" {
			ps := percents(bs)
			parts := []string{}
			for i, b := range bs {
				parts = append(parts, fmt.Sprintf("%s %d", b.Name, ps[i]))
			}
			split = PolicyEffect{Action: "Split", Phrase: "Split " + strings.Join(parts, " / ")}
		}
		for _, f := range L(rule["filters"]) {
			if S(M(f)["type"]) == "RequestMirror" && mirror.Action == "" {
				mirror = PolicyEffect{Action: "Mirror", Phrase: "Mirror to " + S(Path(M(f), "requestMirror", "backendRef", "name"))}
			}
		}
		if ts := M(rule["timeouts"]); len(ts) > 0 && timeout.Action == "" {
			t := defaultString(S(ts["request"]), S(ts["backendRequest"]))
			timeout = PolicyEffect{Action: "Timeout", Phrase: "Request timeout " + t}
		}
		if rt := M(rule["retry"]); len(rt) > 0 && retry.Action == "" {
			retry = PolicyEffect{Action: "Retry", Phrase: fmt.Sprintf("Retry up to %d times", N(rt["attempts"]))}
		}
		if route.Action == "" {
			names := backendNames(bs)
			ph := "Route"
			if m := routeMatchOf(rule); m.any() {
				ph = matchLabel(m, 0)
			}
			route = PolicyEffect{Action: "Route", Phrase: ph + " → " + strings.Join(names, ", ")}
		}
	}
	for _, e := range []PolicyEffect{split, mirror, timeout, retry, route} {
		if e.Action != "" {
			if n := len(rules); n > 1 {
				e.Phrase += fmt.Sprintf(" (%d rules)", n)
			}
			return e
		}
	}
	return PolicyEffect{Action: "Route", Phrase: "No rules"}
}

// hostService resolves a DestinationRule host to a Service in the cluster.
// external is true for a host that is clearly not a cluster Service.
func (c *polCtx) hostService(cl, ns, host string) (svc *store.Object, external bool) {
	parts := strings.Split(host, ".")
	name, sns := parts[0], ns
	if len(parts) > 1 {
		sns = parts[1]
		if len(parts) > 2 && parts[2] != "svc" {
			return nil, true
		}
	}
	return c.ws.Get(cl, "", "services", sns, name), false
}

func (c *polCtx) describeDestinationRule(cl string, ob *polObs) {
	o := ob.obj
	host := S(Path(o.Obj, "spec", "host"))
	tp := M(Path(o.Obj, "spec", "trafficPolicy"))
	feats := []PolicyEffect{}
	// Every feature present adds its type.
	add := func(t string) {
		if !contains(ob.types, t) {
			ob.types = append(ob.types, t)
		}
	}
	if len(M(tp["outlierDetection"])) > 0 {
		od := M(tp["outlierDetection"])
		n := N(od["consecutive5xxErrors"])
		if n == 0 {
			n = N(od["consecutiveGatewayErrors"])
		}
		ph := "Eject failing Pods"
		if n > 0 {
			ph = fmt.Sprintf("Eject a Pod after %d errors", n)
		}
		feats = append(feats, PolicyEffect{Action: "outlier", Phrase: ph})
	}
	if len(M(tp["connectionPool"])) > 0 {
		feats = append(feats, PolicyEffect{Action: "Limit", Phrase: "Connection and request limits"})
	}
	if lb := M(tp["loadBalancer"]); len(lb) > 0 {
		algo := S(lb["simple"])
		if algo == "" && len(M(lb["consistentHash"])) > 0 {
			algo = "CONSISTENT_HASH"
		}
		if algo != "" {
			feats = append(feats, PolicyEffect{Action: "Balance", Phrase: "Load balancing: " + humanAlgorithm(algo)})
		}
		if len(M(lb["localityLbSetting"])) > 0 {
			feats = append(feats, PolicyEffect{Action: "Failover", Phrase: "Regional failover"})
		}
	}
	if mode := S(Path(tp, "tls", "mode")); mode != "" {
		feats = append(feats, PolicyEffect{Action: "TLS", Phrase: "Sets backend TLS (" + mode + ")"})
	}
	subsets := len(L(Path(o.Obj, "spec", "subsets")))
	if subsets > 0 && len(feats) == 0 {
		feats = append(feats, PolicyEffect{Action: "Subsets", Phrase: fmt.Sprintf("%d subsets", subsets)})
	}
	ob.effect = PolicyEffect{Action: "Rule", Phrase: "No settings"}
	if len(feats) > 0 {
		ob.effect = feats[0]
		ph := []string{}
		for _, f := range feats {
			ph = append(ph, f.Phrase)
		}
		ob.effect.Phrase = strings.Join(ph, "; ")
	}
	for _, f := range feats {
		switch f.Action {
		case "outlier", "Limit", "Failover":
			add(PolicyTypeResilience)
		case "TLS":
			add(PolicyTypeTransport)
		default:
			add(PolicyTypeRouting)
		}
	}
	if subsets > 0 || len(ob.types) == 0 {
		add(PolicyTypeRouting) // subsets route even next to other settings
	}
	switch {
	case strings.Contains(host, "*"):
		ob.applies.Kind, ob.applies.Phrase = "mesh", "Hosts matching "+host
		ob.enforcedBy = "waypoint"
		return
	}
	svc, external := c.hostService(cl, o.Namespace, host)
	switch {
	case svc != nil:
		ob.applies.Kind, ob.applies.Phrase = "service", "Service "+svc.Name
		ob.applies.Targets = []Ref{reference(svc)}
		ob.primary = svc
		ob.services = []*store.Object{svc}
		if _, wp, _ := Waypoint(c.ws, svc); wp != "" {
			ob.enforcedBy = "waypoint"
		} else if policyAmbient(c.ws, svc) {
			ob.state = PolicyStateNotEnforced
			ob.problems = append(ob.problems, "Service "+svc.Name+" has no waypoint, so ztunnel ignores this DestinationRule")
		}
	case external:
		ob.applies.Kind, ob.applies.Phrase = "service", "Host "+host
	default:
		name := strings.Split(host, ".")[0]
		ob.applies.Kind, ob.applies.Phrase, ob.applies.Reason = "service", "Host "+host, ""
		sns := o.Namespace
		if p := strings.Split(host, "."); len(p) > 1 {
			sns = p[1]
		}
		ob.applies.Targets = []Ref{c.ref(cl, "", "services", "Service", sns, name)}
		if c.absent(cl, "", "services", "Service", sns) {
			ob.applies.Kind, ob.applies.Phrase = "nothing", "Targets nothing"
			ob.applies.Reason = "Service " + name + " does not exist"
		}
	}
}

func policyGatewayProduct(ws *store.Workspace, cl string, gw *store.Object) (product, class string) {
	class = S(Path(gw.Obj, "spec", "gatewayClassName"))
	g := ws.Get(cl, GatewayGroup, "gatewayclasses", "", class)
	if g == nil {
		return "", class
	}
	ctrl := S(Path(g.Obj, "spec", "controllerName"))
	switch {
	case strings.HasPrefix(ctrl, "istio.io/"):
		return "istio", class
	case strings.Contains(ctrl, "agentgateway"):
		return "agentgateway", class
	}
	return ctrl, class
}

func (c *polCtx) describeAgentgateway(cl string, ob *polObs) {
	o := ob.obj
	ob.enforcedBy = "waypoint"
	c.setTargets(cl, ob, policyTargetRefs(o))
	ob.types, ob.effect = agentgatewayClassify(M(Path(o.Obj, "spec")))
	// The agentgateway controller reports Accepted per ancestor.
	var groups [][]any
	confirmed, failed := false, false
	for _, an := range L(Path(o.Obj, "status", "ancestors")) {
		conds := L(M(an)["conditions"])
		groups = append(groups, conds)
		switch st, _, msg := condStatus(conds, "Accepted"); st {
		case "False":
			failed = true
			ob.problems = append(ob.problems, strings.TrimSpace("Not accepted: "+msg))
		case "True":
			confirmed = true
		}
	}
	ob.detail = condSummary(groups, "Accepted")
	switch {
	case failed:
		ob.state = PolicyStateFailed
	case confirmed:
		ob.state = PolicyStateLive
	}
	// An Istio waypoint ignores agentgateway policy.
	for _, t := range ob.applies.Targets {
		if t.Kind != "Gateway" {
			continue
		}
		if gw := c.ws.Get(cl, GatewayGroup, "gateways", t.Namespace, t.Name); gw != nil {
			if p, class := policyGatewayProduct(c.ws, cl, gw); p == "istio" {
				if ob.state != PolicyStateFailed {
					ob.state = PolicyStateNotEnforced
				}
				ob.problems = append(ob.problems, "Not enforced: "+t.Namespace+"/"+t.Name+" is an Istio waypoint ("+class+"), which does not apply "+o.Kind+".")
			}
		}
	}
	if ob.applies.Kind == "waypoint" {
		for _, t := range ob.applies.Targets {
			ob.services = append(ob.services, c.bound[cl][t.Namespace+"/"+t.Name]...)
		}
		if n := len(ob.services); n > 0 {
			ob.applies.Phrase += fmt.Sprintf(" (%d Services)", n)
		}
	}
}

func (c *polCtx) describeEnvoyFilter(cl string, ob *polObs) {
	o := ob.obj
	ob.types, ob.enforcedBy = []string{PolicyTypeResilience}, "waypoint"
	name := o.Labels()["flightbay.dev/resilience-service"]
	ob.effect = PolicyEffect{Action: "Resilience", Phrase: "Flightbay resilience settings"}
	var req Request
	if json.Unmarshal([]byte(S(Path(o.Obj, "metadata", "annotations", "flightbay.dev/resilience-request"))), &req) == nil {
		var ph []string
		for i, t := range req.Tasks {
			var a, p string
			switch t {
			case "timeouts":
				a, p = "Timeout", "Request timeout "+req.Timeout
			case "retries":
				a, p = "Retry", fmt.Sprintf("Retry up to %d times", req.RetryAttempts)
			case "rate-limit":
				a, p = "Rate limit", fmt.Sprintf("%d requests a %s", req.Requests, rateUnitWord(req.Unit))
			case "global-rate-limit":
				a, p = "Shared", "Shared rate limit"
			case "circuit-breaker":
				a, p = "Limit", "Connection and request limits"
			case "endpoint-failover":
				a, p = "outlier", fmt.Sprintf("Eject a Pod after %d errors", req.FailureThreshold)
			default:
				continue
			}
			if i == 0 || ob.effect.Action == "Resilience" {
				ob.effect.Action = a
			}
			ph = append(ph, p)
		}
		if len(ph) > 0 {
			ob.effect.Phrase = strings.Join(ph, "; ")
		}
	}
	svc := c.ws.Get(cl, "", "services", o.Namespace, name)
	if svc == nil {
		ob.applies.Kind, ob.applies.Phrase = "nothing", "Targets nothing"
		ob.applies.Reason = "Service " + name + " does not exist"
		ob.applies.Targets = []Ref{c.ref(cl, "", "services", "Service", o.Namespace, name)}
		return
	}
	ob.applies.Kind, ob.applies.Phrase = "service", "Service "+name
	ob.applies.Targets = []Ref{reference(svc)}
	ob.primary, ob.services = svc, []*store.Object{svc}
}

// policyAmbient says whether a Service is in the ambient mesh, so a missing
// waypoint matters. A namespace that was not captured says nothing either way.
func policyAmbient(ws *store.Workspace, svc *store.Object) bool {
	is := func(o *store.Object) bool { return o != nil && o.Labels()["istio.io/dataplane-mode"] == "ambient" }
	ns := ws.Get(svc.Cluster, "", "namespaces", "", svc.Namespace)
	if is(svc) || is(ns) {
		return true
	}
	sel := M(Path(svc.Obj, "spec", "selector"))
	for _, p := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
		if len(sel) > 0 && matches(sel, p.Labels()) && is(p) {
			return true
		}
	}
	return false
}

// editability finds the Service-scoped editable entry for the row, which
// also fixes the rule index in editKey.
func (c *polCtx) editability(row *PolicyRow, p polObs) bool {
	own := p.applies.Kind == "service" || p.applies.Kind == "waypoint" || p.applies.Kind == "pods"
	cands := []*store.Object{}
	if p.primary != nil {
		cands = append(cands, p.primary)
	}
	cands = append(cands, p.services...)
	if len(cands) > 6 {
		cands = cands[:6]
	}
	found := false
	for _, svc := range cands {
		k := svc.Cluster + "/" + svc.Namespace + "/" + svc.Name
		es, ok := c.editable[k]
		if !ok {
			es = editableConfigurations(c.ws, svc)
			c.editable[k] = es
		}
		for _, e := range es {
			if e.Ref != row.Ref {
				continue
			}
			if !found {
				row.EditKey, found = EditKeyFor(e.Ref, e.Rule), true
			}
			if e.Supported && e.Request != nil {
				row.EditKey = EditKeyFor(e.Ref, e.Rule)
				return own
			}
		}
	}
	return false
}
