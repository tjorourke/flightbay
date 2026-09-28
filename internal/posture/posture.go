// Package posture scores a snapshot against Solo's best practices for the
// agentic stack (agentgateway, kagent, agentregistry, MCP) and for Istio
// ambient, and says what to change. The checks follow the defence-in-depth
// guidance in the Solo field guides: authenticate every MCP and LLM route,
// authorise per tool, rate-limit and guard AI traffic, keep workloads in the
// mesh with STRICT mTLS and default-deny, and make L7 policy actually run.
//
// Every check works from captured objects only. Where the customer excluded
// the data a check needs, or nothing it applies to exists, the check is
// marked not applicable with the reason, and never counted as a failure.
package posture

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Status of a check or of one object within it.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
	NA   Status = "na"
)

// Item is one object a check looked at.
type Item struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    Status `json:"status"`
	Detail    string `json:"detail"`
}

// Check is one best practice and its result.
type Check struct {
	ID       string  `json:"id"`
	Area     string  `json:"area"` // agentic or ambient
	Category string  `json:"category"`
	Severity string  `json:"severity"`
	Title    string  `json:"title"`
	Why      string  `json:"why"`
	Fix      string  `json:"fix"`
	Source   string  `json:"source"`         // the Solo field guide
	Docs     string  `json:"docs,omitempty"` // the product documentation page (docs.solo.io)
	Status   Status  `json:"status"`
	Reason   string  `json:"reason,omitempty"`
	Items    []Item  `json:"items"`
	Passed   int     `json:"passed"`
	Total    int     `json:"total"`
	Score    float64 `json:"score"`            // 0..1
	Points   float64 `json:"points,omitempty"` // area points recovered if fixed (Enterprise)
}

// AreaScore summarises one area.
type AreaScore struct {
	Area       string         `json:"area"`
	Title      string         `json:"title"`
	Present    bool           `json:"present"`
	Reason     string         `json:"reason,omitempty"`
	Score      int            `json:"score"`
	Grade      string         `json:"grade"`
	Categories map[string]int `json:"categories"`
	Counts     map[Status]int `json:"counts"`
}

// Report is the full result.
type Report struct {
	Areas      []AreaScore `json:"areas"`
	Checks     []Check     `json:"checks"`
	Priorities []Priority  `json:"priorities,omitempty"` // Enterprise: fix-first ranking
}

// WithPriorities fills in points recovered per check and the fix-first
// ranking (the Flightbay Enterprise priority view). top <= 0 keeps them all.
func (r Report) WithPriorities(top int) Report {
	for i := range r.Checks {
		r.Checks[i].Points = PointsIfFixed(r, r.Checks[i])
	}
	r.Priorities = Priorities(r, top)
	if r.Priorities == nil {
		r.Priorities = []Priority{}
	}
	return r
}

var weight = map[string]float64{"critical": 10, "high": 6, "medium": 3, "low": 1}

// ---------- JSON helpers ----------

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }
func s(v any) string         { x, _ := v.(string); return x }
func path(v any, keys ...string) any {
	for _, k := range keys {
		v = m(v)[k]
	}
	return v
}
func labels(o *store.Object) map[string]string { return o.Labels() }

var systemNS = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true, "istio-system": true, "local-path-storage": true}

func isSystem(ns string) bool { return systemNS[ns] || strings.HasPrefix(ns, "kube-") }

// ---------- per-cluster context ----------

type env struct {
	ws      *store.Workspace
	cluster string
}

func (e env) list(group, resource string) []*store.Object {
	return e.ws.List(e.cluster, group, resource, "", nil)
}

// evaluable reports whether a type was captured completely enough for a
// check to trust the absence of an object, and why not otherwise.
func (e env) evaluable(group, resource, kind string, namespaced bool) (bool, string) {
	if e.declaredOnly() {
		// A repo scan holds everything the scanned paths declare, so absence
		// there means the repository does not declare it. Pods, nodes, events
		// and the like are made by the cluster and never judged.
		if runtimeOnlyType(group, resource) {
			return false, DeclaredWhy + ": " + resource + " exist only in a running cluster"
		}
		if group == "" && resource == "secrets" {
			return false, DeclaredWhy + ": Secrets are normally created outside the repository (sealed, external or by hand), so a missing one proves nothing"
		}
		return true, ""
	}
	st := e.ws.CollectionState(e.cluster, group, resource, kind, namespaced)
	switch st.State {
	case store.StateComplete, store.StateTruncated:
		return true, ""
	case store.StateNotCollected:
		// The type is not in discovery at all: the CRD is not installed, so
		// there are no such objects. That is evaluable.
		if _, known := e.ws.FindResource(e.cluster, resource+"."+group); !known {
			if _, known2 := e.ws.FindResource(e.cluster, resource); !known2 || group != "" {
				if pol := e.ws.Policy(); pol == nil || pol.Plan().Selected("objects") {
					return true, ""
				}
			}
		}
	}
	name := resource
	if group != "" {
		name += "." + group
	}
	return false, fmt.Sprintf("%s was not captured (%s)", name, strings.TrimSpace(st.State+" "+st.Reason))
}

func (e env) item(o *store.Object, st Status, detail string) Item {
	if t := m(o.Obj[templateKey]); t != nil {
		// A repo scan's stand-in pod: report on the workload that declares it.
		return Item{Cluster: e.cluster, Group: s(t["group"]), Resource: s(t["resource"]), Kind: s(t["kind"]), Namespace: o.Namespace, Name: o.Name, Status: st, Detail: detail}
	}
	return Item{Cluster: e.cluster, Group: o.Group, Resource: o.Resource, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Status: st, Detail: detail}
}

// namespaces returns real Namespace objects, or ok=false when only
// synthesised ones exist (their labels are unknown).
func (e env) namespaces() ([]*store.Object, bool) {
	ns := e.list("", "namespaces")
	if e.declaredOnly() {
		// Only the Namespace objects the repository declares have known labels.
		var real []*store.Object
		for _, n := range ns {
			if ann := m(path(n.Obj, "metadata", "annotations")); s(ann[store.SynthesisedAnnotation]) != "true" {
				real = append(real, n)
			}
		}
		return real, len(real) > 0
	}
	for _, n := range ns {
		if ann := m(path(n.Obj, "metadata", "annotations")); s(ann[store.SynthesisedAnnotation]) == "true" {
			return nil, false
		}
	}
	return ns, true
}

func (e env) nsLabels() (map[string]map[string]string, bool) {
	ns, ok := e.namespaces()
	if !ok {
		return nil, false
	}
	out := map[string]map[string]string{}
	for _, n := range ns {
		out[n.Name] = labels(n)
	}
	return out, true
}

// ---------- the agentic model: policies, backends, routes ----------

type ref struct{ group, kind, name string }

type policy struct {
	o       *store.Object
	spec    map[string]any
	targets []ref
}

type backend struct {
	o      *store.Object
	spec   map[string]any
	mcp    map[string]any // spec.mcp or spec.entMcp
	ai     map[string]any
	routes []*store.Object
}

type model struct {
	e        env
	policies []policy
	backends []*backend
	routes   []*store.Object
	gateways map[string]*store.Object // ns/name
}

var backendKinds = map[string]bool{"AgentgatewayBackend": true, "EnterpriseAgentgatewayBackend": true}

func newModel(e env) *model {
	md := &model{e: e, gateways: map[string]*store.Object{}}
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, o := range e.list(g.group, g.res) {
			p := policy{o: o, spec: m(o.Obj["spec"])}
			for _, t := range l(p.spec["targetRefs"]) {
				tm := m(t)
				p.targets = append(p.targets, ref{s(tm["group"]), s(tm["kind"]), s(tm["name"])})
			}
			md.policies = append(md.policies, p)
		}
	}
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}} {
		for _, o := range e.list(g.group, g.res) {
			sp := m(o.Obj["spec"])
			b := &backend{o: o, spec: sp, mcp: m(sp["mcp"]), ai: m(sp["ai"])}
			if b.mcp == nil {
				b.mcp = m(sp["entMcp"])
			}
			md.backends = append(md.backends, b)
		}
	}
	for _, gw := range e.list("gateway.networking.k8s.io", "gateways") {
		md.gateways[gw.Namespace+"/"+gw.Name] = gw
	}
	md.routes = e.list("gateway.networking.k8s.io", "httproutes")
	for _, r := range md.routes {
		for _, rule := range l(path(r.Obj, "spec", "rules")) {
			for _, br := range l(m(rule)["backendRefs"]) {
				bm := m(br)
				if !backendKinds[s(bm["kind"])] {
					continue
				}
				ns := s(bm["namespace"])
				if ns == "" {
					ns = r.Namespace
				}
				for _, b := range md.backends {
					if b.o.Name == s(bm["name"]) && b.o.Namespace == ns {
						b.routes = append(b.routes, r)
					}
				}
			}
		}
	}
	return md
}

func (md *model) targets(p policy, group, kind, ns, name string) bool {
	if p.o.Namespace != ns {
		return false
	}
	for _, t := range p.targets {
		if t.kind == kind && t.name == name && (t.group == group || t.group == "") {
			return true
		}
	}
	return false
}

// applicable returns the policies that apply to a backend: attached to the
// backend, to a route that sends to it, or to a Gateway that route hangs off.
func (md *model) applicable(b *backend) []policy {
	var out []policy
	seen := map[*store.Object]bool{}
	add := func(p policy) {
		if !seen[p.o] {
			seen[p.o] = true
			out = append(out, p)
		}
	}
	for _, p := range md.policies {
		if !policyActive(p) {
			continue // not accepted or not attached: enforces nothing
		}
		if md.targets(p, b.o.Group, b.o.Kind, b.o.Namespace, b.o.Name) {
			add(p)
		}
		for _, r := range b.routes {
			if md.targets(p, "gateway.networking.k8s.io", "HTTPRoute", r.Namespace, r.Name) {
				add(p)
			}
			for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
				pm := m(pr)
				kind := s(pm["kind"])
				if kind == "" {
					kind = "Gateway"
				}
				ns := s(pm["namespace"])
				if ns == "" {
					ns = r.Namespace
				}
				if md.targets(p, "gateway.networking.k8s.io", kind, ns, s(pm["name"])) {
					add(p)
				}
			}
		}
	}
	return out
}

func (md *model) exposure(b *backend) string {
	var parts []string
	for _, r := range b.routes {
		var parents []string
		for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
			parents = append(parents, s(m(pr)["name"]))
		}
		parts = append(parts, fmt.Sprintf("HTTPRoute %s/%s on %s", r.Namespace, r.Name, strings.Join(parents, ",")))
	}
	return strings.Join(parts, "; ")
}

func hasAuthn(ps []policy) (bool, bool) {
	authn, meshOnly := false, false
	for _, p := range ps {
		t := m(p.spec["traffic"])
		if len(l(path(t, "jwtAuthentication", "providers"))) > 0 || t["extAuth"] != nil || t["entExtAuth"] != nil ||
			authnEnforcing(m(t["apiKeyAuthentication"])) || authnEnforcing(m(t["basicAuthentication"])) ||
			path(p.spec, "backend", "mcp", "authentication") != nil || path(p.spec, "backend", "extAuth") != nil {
			authn = true
		}
		for _, a := range []any{t["authorization"], path(p.spec, "backend", "mcp", "authorization")} {
			if a != nil && strings.Contains(fmt.Sprint(a), "source.identity") {
				meshOnly = true
			}
		}
	}
	return authn, meshOnly
}

// authnEnforcing: an api-key or basic-auth block counts as authentication
// unless its mode lets requests without a credential through.
func authnEnforcing(a map[string]any) bool {
	if a == nil {
		return false
	}
	mode := s(a["mode"])
	return mode != "Optional" && mode != "Permissive"
}

// eastWest reports whether every route to the backend hangs off a waypoint
// (a Service parent or a waypoint-class Gateway): in-mesh traffic, where the
// caller's SPIFFE identity is the right thing to authorise.
func (md *model) eastWest(b *backend) bool {
	if len(b.routes) == 0 {
		return false
	}
	for _, r := range b.routes {
		for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
			pm := m(pr)
			kind := s(pm["kind"])
			if kind == "Service" {
				continue
			}
			ns := s(pm["namespace"])
			if ns == "" {
				ns = r.Namespace
			}
			gw := md.gateways[ns+"/"+s(pm["name"])]
			if gw == nil || !strings.Contains(s(path(gw.Obj, "spec", "gatewayClassName")), "waypoint") {
				return false
			}
		}
	}
	return true
}

func policyNames(ps []policy) string {
	var n []string
	for _, p := range ps {
		n = append(n, p.o.Kind+" "+p.o.Namespace+"/"+p.o.Name)
	}
	if len(n) == 0 {
		return "no policy applies"
	}
	return strings.Join(n, ", ")
}

func clusterLocal(host string) bool {
	return strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local") || strings.HasSuffix(host, ".mesh.internal") || !strings.Contains(host, ".")
}

var sensitiveEnv = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|credential)`)

// settingEnv matches names that configure how a secret is handled rather than
// holding one (TOKEN_PASSTHROUGH, SECRET_NAME, API_KEY_FILE, TOKEN_URL, ...).
var settingEnv = regexp.MustCompile(`(?i)(passthrough|enabled?|disabled?|_mode|_file|_path|_dir|_url|_uri|_endpoint|_header|_name|_ref|_ttl|_expiry|_timeout|_type|_audience|_issuer)$`)
var keyLike = regexp.MustCompile(`^(Bearer |gh[pousr]_|sk-|AKIA)|\[REDACTED`)

// mcpTargets returns each static MCP target. A target naming a Service by
// backendRef is returned with host set to its cluster-local name.
func mcpTargets(b *backend) []map[string]any {
	var out []map[string]any
	for _, t := range l(b.mcp["targets"]) {
		st := m(m(t)["static"])
		if st == nil {
			continue
		}
		if s(st["host"]) == "" {
			if br := m(st["backendRef"]); br != nil {
				ns := s(br["namespace"])
				if ns == "" {
					ns = b.o.Namespace
				}
				cp := map[string]any{}
				for k, v := range st {
					cp[k] = v
				}
				cp["host"] = s(br["name"]) + "." + ns + ".svc.cluster.local"
				if cp["port"] == nil {
					cp["port"] = br["port"]
				}
				st = cp
			}
		}
		out = append(out, st)
	}
	return out
}

func (md *model) agentgatewayGateways() []*store.Object {
	var out []*store.Object
	for _, gw := range md.gateways {
		cls := s(path(gw.Obj, "spec", "gatewayClassName"))
		if strings.Contains(cls, "agentgateway") && !strings.Contains(cls, "waypoint") {
			out = append(out, gw)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace+out[i].Name < out[j].Namespace+out[j].Name })
	return out
}

func (md *model) gatewayPolicies(gw *store.Object) []policy {
	var out []policy
	for _, p := range md.policies {
		if policyActive(p) && md.targets(p, "gateway.networking.k8s.io", "Gateway", gw.Namespace, gw.Name) {
			out = append(out, p)
		}
	}
	return out
}
