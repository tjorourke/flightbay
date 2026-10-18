package reach

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	"sigs.k8s.io/yaml"
)

// Explanation answers "why was this allowed or denied?" for one source and
// one destination: every enforcement point in the order traffic meets it,
// the policies each one read and what it decided, next to what the access
// logs saw. It runs the Agent reach evaluator, so the answer matches the
// reach report and the traffic map.
type Explanation struct {
	FixScope   *RepairScope `json:"fixScope,omitempty"`
	Cluster    string       `json:"cluster"`
	From       ExplainEnd   `json:"from"`
	To         ExplainEnd   `json:"to"`
	Verdict    string       `json:"verdict"` // allowed, denied, unknown
	Layer      string       `json:"layer,omitempty"`
	Methods    []string     `json:"methods,omitempty"` // allowed methods, when an L7 rule limits them
	Paths      []string     `json:"paths,omitempty"`
	Summary    string       `json:"summary"`
	Steps      []Step       `json:"steps"`
	Conditions []string     `json:"conditions,omitempty"`
	Observed   *ExplainSeen `json:"observed,omitempty"`
	Fix        string       `json:"fix,omitempty"`
	FixNote    string       `json:"fixNote,omitempty"`
	Advice     []string     `json:"advice,omitempty"`
}

type RepairScope struct {
	Ports []string `json:"ports,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

// ExplainScoped narrows generated authorization to explicit ports and paths.
// It regenerates the repair from evidence rather than accepting client YAML.
func ExplainScoped(ws *store.Workspace, cluster, from, to string, scope RepairScope) (Explanation, error) {
	x, err := Explain(ws, cluster, from, to)
	if err != nil || x.FixScope == nil {
		return x, err
	}
	var policy map[string]any
	if err := yaml.Unmarshal([]byte(x.Fix), &policy); err != nil {
		return x, err
	}
	rule := m(l(path(policy, "spec", "rules"))[0])
	op := m(m(l(rule["to"])[0])["operation"])
	if len(scope.Ports) > 0 {
		for _, port := range scope.Ports {
			if !has(x.FixScope.Ports, port) {
				return Explanation{}, fmt.Errorf("repair port %q is not a captured destination port", port)
			}
		}
		x.FixScope.Ports = scope.Ports
		op["ports"] = scope.Ports
	}
	if len(scope.Paths) > 0 {
		if len(l(path(policy, "spec", "targetRefs"))) == 0 {
			return Explanation{}, fmt.Errorf("ztunnel repairs cannot enforce HTTP paths")
		}
		for _, p := range scope.Paths {
			if p != "*" && !strings.HasPrefix(p, "/") {
				return Explanation{}, fmt.Errorf("repair paths must start with / or be *")
			}
		}
		x.FixScope.Paths = scope.Paths
		op["paths"] = scope.Paths
	}
	b, err := yaml.Marshal(policy)
	x.Fix = string(b)
	return x, err
}

// ExplainEnd is one end of the explanation.
type ExplainEnd struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Namespace string   `json:"namespace"`
	Principal string   `json:"principal,omitempty"`
	InMesh    bool     `json:"inMesh"`
	Waypoint  string   `json:"waypoint,omitempty"`
	Backends  []string `json:"backends,omitempty"`
	Ref       *Ref     `json:"ref,omitempty"`
}

// ExplainSeen is what the access logs recorded for the pair.
type ExplainSeen struct {
	Connections int      `json:"connections"`
	Denied      int      `json:"denied"`
	HBONE       int      `json:"hbone"`
	Plaintext   int      `json:"plaintext"`
	Methods     []string `json:"methods,omitempty"`
	Agrees      bool     `json:"agrees"`
	Note        string   `json:"note,omitempty"`
}

// ExplainError is a request the explainer cannot answer, with what it knows.
type ExplainError struct {
	Message     string   `json:"error"`
	Suggestions []string `json:"suggestions,omitempty"`
}

func (e *ExplainError) Error() string { return e.Message }

// Explain evaluates source -> destination in a cluster ("" when there is
// only one). from names a workload (ns/name, a pod ns/pod, or a SPIFFE ID);
// to names a Service (ns/name or its host name) or a workload, whose Service
// is used.
func Explain(ws *store.Workspace, cluster, from, to string) (Explanation, error) {
	cls := ws.Clusters()
	if cluster == "" {
		if len(cls) != 1 {
			return Explanation{}, &ExplainError{Message: "the capture has several clusters; say which", Suggestions: cls}
		}
		cluster = cls[0]
	}
	if !has(cls, cluster) {
		return Explanation{}, &ExplainError{Message: "no cluster " + cluster + " in the capture", Suggestions: cls}
	}
	c := newCluster(ws, cluster)
	src := c.findWorkload(from)
	if src == nil {
		return Explanation{}, &ExplainError{Message: "no workload " + from + " in " + cluster, Suggestions: c.workloadNames(from)}
	}
	sv := c.findService(to)
	if sv == nil {
		return Explanation{}, &ExplainError{Message: "no Service " + to + " in " + cluster + " (or none selects that workload)", Suggestions: c.serviceNames(to)}
	}
	h := c.evalHop(src, sv)
	x := Explanation{Cluster: cluster, Verdict: h.verdict, Layer: h.layer, Methods: h.methods, Paths: h.paths, Steps: h.steps, Conditions: h.conds}
	if x.Steps == nil {
		x.Steps = []Step{}
	}
	x.From = ExplainEnd{ID: "w:" + src.id, Label: src.ns + "/" + src.name, Namespace: src.ns, Principal: src.principal(), InMesh: src.inMesh, Ref: refOf(src.obj)}
	x.To = ExplainEnd{ID: "svc:" + sv.id, Label: sv.ns + "/" + sv.name, Namespace: sv.ns, Waypoint: sv.waypoint, Ref: refOf(sv.obj)}
	inMesh := len(sv.backends) > 0
	for _, b := range sv.backends {
		x.To.Backends = append(x.To.Backends, b.ns+"/"+b.name)
		inMesh = inMesh && b.inMesh
	}
	if len(sv.backends) == 0 && c.waypointFor(sv) != nil {
		inMesh = true
	}
	x.To.InMesh = inMesh
	x.Summary = explainSummary(x, h)
	x.Observed = c.observedPair(ws, src, sv, h.verdict)
	x.Fix, x.FixNote, x.Advice = explainFix(c, src, sv, h, trustDomain(ws, c.name))
	if x.Fix != "" {
		var policy map[string]any
		if yaml.Unmarshal([]byte(x.Fix), &policy) == nil && s(policy["kind"]) == "AuthorizationPolicy" {
			ports := []string{}
			l7 := len(l(path(policy, "spec", "targetRefs"))) > 0
			for _, p := range sv.ports {
				v := strconv.Itoa(p.port)
				if !l7 {
					v = p.target
					if _, err := strconv.Atoi(v); err != nil {
						continue
					}
				}
				ports = union(ports, []string{v})
			}
			if len(ports) == 0 {
				x.Fix = ""
				x.Advice = append(x.Advice, "Capture numeric workload ports before generating a scoped L4 repair.")
			} else {
				op := map[string]any{"ports": ports}
				x.FixScope = &RepairScope{Ports: ports}
				if l7 {
					op["paths"] = []string{"*"}
					x.FixScope.Paths = []string{"*"}
					if len(x.Paths) > 0 {
						op["paths"] = x.Paths
						x.FixScope.Paths = x.Paths
					}
				}
				if l7 && len(x.Methods) > 0 {
					op["methods"] = x.Methods
				}
				rule := m(l(path(policy, "spec", "rules"))[0])
				rule["to"] = []any{map[string]any{"operation": op}}
				b, _ := yaml.Marshal(policy)
				x.Fix = string(b)
			}
		}
	}
	return x, nil
}

func (c *cluster) jwtStep(sv *service, h *hop) {
	for _, ra := range c.ws.List(c.name, "security.istio.io", "requestauthentications", "", nil) {
		relevant := false
		refs := append([]any{}, l(path(ra.Obj, "spec", "targetRefs"))...)
		if ref := m(path(ra.Obj, "spec", "targetRef")); ref != nil {
			refs = append(refs, ref)
		}
		for _, v := range refs {
			t := m(v)
			if (s(t["kind"]) == "Service" && s(t["group"]) == "" && ra.Namespace == sv.ns && s(t["name"]) == sv.name) || (s(t["kind"]) == "Gateway" && s(t["group"]) == "gateway.networking.k8s.io" && ra.Namespace == first(sv.waypointNS, sv.ns) && s(t["name"]) == sv.waypoint) {
				relevant = true
			}
		}
		if !relevant || sv.waypoint == "" {
			continue
		}
		b, _ := json.Marshal(path(ra.Obj, "spec", "jwtRules"))
		step := Step{Point: "waypoint JWT authentication", Verdict: Unknown, Detail: "RequestAuthentication " + ra.Namespace + "/" + ra.Name + " validates presented tokens: " + string(b) + ". Token signature and claims are request-dependent; an absent token is permitted unless authorization requires requestPrincipals.", Policies: []Ref{*refOf(ra)}}
		index := len(h.steps)
		for i, st := range h.steps {
			if strings.HasPrefix(st.Point, "waypoint ") && strings.Contains(st.Point, "authorization") {
				index = i
				break
			}
		}
		h.steps = append(h.steps, Step{})
		copy(h.steps[index+1:], h.steps[index:])
		h.steps[index] = step
		h.conds = append(h.conds, "JWT signature, issuer/audience and JWKS retrieval cannot be verified from captured configuration")
		if h.verdict == Allowed {
			h.verdict = Unknown
		}
	}
}

func explainSummary(x Explanation, h hop) string {
	switch h.verdict {
	case Allowed:
		var by []string
		for _, st := range h.steps {
			if st.Verdict == Allowed && len(st.Policies) > 0 {
				by = append(by, st.Point)
			}
		}
		s := x.From.Label + " can reach " + x.To.Label
		if len(h.methods) > 0 {
			s += ", with " + strings.Join(h.methods, ", ") + " only"
		}
		if len(by) == 0 {
			return s + ": nothing on the path refuses it."
		}
		return s + ": allowed at " + strings.Join(by, ", ") + "."
	case Denied:
		for _, st := range h.steps {
			if st.Verdict == Denied {
				return x.From.Label + " cannot reach " + x.To.Label + ": refused at " + st.Point + ". " + capFirst(st.Detail) + "."
			}
		}
		return x.From.Label + " cannot reach " + x.To.Label + "."
	}
	for _, st := range h.steps {
		if st.Verdict == Unknown {
			return "Cannot tell whether " + x.From.Label + " can reach " + x.To.Label + ": " + st.Detail + "."
		}
	}
	return "Cannot tell whether " + x.From.Label + " can reach " + x.To.Label + ": " + strings.Join(h.reasons, "; ") + "."
}

// observedPair reads the access logs for src -> sv.
func (c *cluster) observedPair(ws *store.Workspace, src *workload, sv *service, verdict string) *ExplainSeen {
	seen := &ExplainSeen{}
	any := false
	for _, ae := range ws.AccessEdges(c.name) {
		w := c.resolveSrc(store.Flow{SrcNamespace: ae.SrcNamespace, SrcPod: ae.SrcPod, SrcIdentity: ae.SrcIdentity})
		if w != src {
			continue
		}
		dsv, _ := c.trafficDst(ae)
		if dsv != sv {
			continue
		}
		any = true
		if ae.Connections > 0 {
			seen.Connections += ae.Connections
		} else {
			seen.Connections += ae.Requests
		}
		seen.Denied += ae.Denied
		seen.HBONE += ae.HBONE
		seen.Plaintext += ae.Plaintext
		seen.Methods = union(seen.Methods, ae.Methods)
	}
	if !any {
		if !c.accessLogsCaptured() {
			return nil
		}
		seen.Agrees = true
		seen.Note = "no connection between the two in the captured ztunnel logs"
		return seen
	}
	switch {
	case verdict == Allowed && seen.Denied == seen.Connections:
		seen.Note = "every logged connection was refused, although the captured policy allows it: look for L7 rules, JWT requirements or a policy changed after the capture"
	case verdict == Denied && seen.Connections > seen.Denied:
		seen.Note = fmt.Sprintf("%d connections got through although the captured policy denies them: the policy may be newer than the traffic, or apply differently from how it reads", seen.Connections-seen.Denied)
	default:
		seen.Agrees = true
	}
	return seen
}

// explainFix proposes the smallest change that would allow a denied pair.
// It only writes YAML when an ALLOW rule is missing; a DENY rule or STRICT
// mTLS for a caller outside the mesh needs a decision, not more YAML.
func explainFix(c *cluster, src *workload, sv *service, h hop, td string) (fix, note string, advice []string) {
	if h.verdict != Denied {
		return "", "", nil
	}
	for _, st := range h.steps {
		if st.Verdict != Denied {
			continue
		}
		switch {
		case strings.Contains(st.Detail, "denied by DENY policy"):
			return "", "", []string{"A DENY policy matches this caller. DENY is checked before ALLOW, so adding an ALLOW policy will not help: narrow or remove the DENY rule if the call should work."}
		case strings.HasPrefix(st.Point, "mTLS"):
			return "", "", []string{"Enrol " + src.ns + " in ambient (label the namespace istio.io/dataplane-mode=ambient) so the caller gets an identity, or relax PeerAuthentication for this port if it must stay outside."}
		case strings.HasPrefix(st.Point, "NetworkPolicy"):
			return npAllowYAML(src, sv), "A NetworkPolicy refused it. This adds egress from the caller; the destination may also need an ingress rule. In ambient, mesh traffic arrives on 15008.", nil
		case strings.Contains(st.Point, "authorization"):
			principal := td + "/ns/" + src.ns + "/sa/" + src.sa
			var b strings.Builder
			if strings.HasPrefix(st.Point, "waypoint") {
				fmt.Fprintf(&b, "apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata:\n  name: allow-%s\n  namespace: %s\nspec:\n  targetRefs:\n  - kind: Service\n    group: \"\"\n    name: %s\n  action: ALLOW\n  rules:\n  - from:\n    - source:\n        principals: [\"%s\"]\n", src.name, sv.ns, sv.name, principal)
			} else {
				if len(sv.backends) == 0 {
					return "", "", []string{"Capture the destination workloads before generating a selector-based repair."}
				}
				dst := sv.backends[0]
				fmt.Fprintf(&b, "apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata:\n  name: allow-%s\n  namespace: %s\nspec:\n  selector:\n    matchLabels:\n%s  action: ALLOW\n  rules:\n  - from:\n    - source:\n        principals: [\"%s\"]\n", src.name, dst.ns, yamlLabels(sv.selector, 6), principal)
			}
			note := "Admits " + src.name + " by its service account, in trust domain " + td + " (captured mesh configuration or access logs)."
			if td == "cluster.local" {
				note = "Admits " + src.name + " by its service account. The trust domain is written as cluster.local; use the mesh's own if it differs."
			}
			return b.String(), note, nil
		}
	}
	return "", "", nil
}

func yamlLabels(lb map[string]string, indent int) string {
	keys := make([]string, 0, len(lb))
	for k := range lb {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s%s: %q\n", strings.Repeat(" ", indent), k, lb[k])
	}
	return b.String()
}

func npAllowYAML(src *workload, sv *service) string {
	return fmt.Sprintf("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: allow-%s-to-%s\n  namespace: %s\nspec:\n  podSelector:\n    matchLabels:\n%s  policyTypes: [Egress]\n  egress:\n  - to:\n    - namespaceSelector:\n        matchLabels:\n          kubernetes.io/metadata.name: %s\n      podSelector:\n        matchLabels:\n%s", src.name, sv.name, src.ns, yamlLabels(src.labels, 6), sv.ns, yamlLabels(sv.selector, 10))
}

// trustDomain reads the mesh trust domain from the SPIFFE IDs in the access
// logs, and falls back to cluster.local.
func trustDomain(ws *store.Workspace, cluster string) string {
	for _, cm := range ws.List(cluster, "", "configmaps", "", nil) {
		if cm.Name != "istio" && !strings.HasPrefix(cm.Name, "istio-") {
			continue
		}
		raw := s(path(cm.Obj, "data", "mesh"))
		if raw == "" {
			continue
		}
		var mesh struct {
			TrustDomain string `json:"trustDomain"`
		}
		if yaml.Unmarshal([]byte(raw), &mesh) == nil {
			return first(mesh.TrustDomain, "cluster.local")
		}
	}
	for _, ae := range ws.AccessEdges(cluster) {
		for _, id := range []string{ae.SrcIdentity, ae.DstIdentity} {
			if strings.HasPrefix(id, "spiffe://") {
				if td, _, ok := strings.Cut(strings.TrimPrefix(id, "spiffe://"), "/"); ok && td != "" {
					return td
				}
			}
		}
	}
	return "cluster.local"
}

// findWorkload resolves ns/name (workload or pod), a workload ID, or a SPIFFE ID.
func (c *cluster) findWorkload(q string) *workload {
	q = strings.TrimPrefix(q, "w:")
	if ns, sa, ok := spiffeSA(q); ok {
		var hit *workload
		for _, w := range c.works {
			if w.ns == ns && w.sa == sa {
				if hit != nil {
					return nil
				}
				hit = w
			}
		}
		return hit
	}
	for _, w := range c.works {
		if w.id == q || w.ns+"/"+w.name == q {
			return w
		}
	}
	if w := c.byPod[q]; w != nil {
		return w
	}
	return nil
}

// findService resolves ns/name, a Service ID, a host (name.ns[.svc...]), or a
// workload whose Service is taken.
func (c *cluster) findService(q string) *service {
	q = strings.TrimPrefix(q, "svc:")
	for _, sv := range c.services {
		if sv.id == q {
			return sv
		}
	}
	if sv := c.svcByKey[q]; sv != nil {
		return sv
	}
	host := q
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if p := strings.Split(host, "."); len(p) >= 2 {
		if sv := c.svcByKey[key(p[1], p[0])]; sv != nil {
			return sv
		}
	}
	if w := c.findWorkload(q); w != nil {
		return c.serviceFor(w, 0)
	}
	return nil
}

func (c *cluster) workloadNames(q string) []string {
	var all []string
	for _, w := range c.works {
		all = append(all, w.ns+"/"+w.name)
	}
	return suggest(all, q)
}

func (c *cluster) serviceNames(q string) []string {
	var all []string
	for _, sv := range c.services {
		all = append(all, sv.ns+"/"+sv.name)
	}
	return suggest(all, q)
}

// suggest picks names close to q: containing its last part, else sharing its
// first four letters, else in its namespace.
func suggest(all []string, q string) []string {
	q = strings.ToLower(q)
	name := lastPart(q)
	ns, _, _ := strings.Cut(q, "/")
	var out []string
	for _, pass := range []func(n string) bool{
		func(n string) bool { return q == "" || strings.Contains(strings.ToLower(n), name) },
		func(n string) bool {
			return len(name) >= 4 && strings.HasPrefix(strings.ToLower(lastPart(n)), name[:4])
		},
		func(n string) bool { return strings.Contains(q, "/") && strings.HasPrefix(strings.ToLower(n), ns+"/") },
	} {
		for _, n := range all {
			if pass(n) {
				out = append(out, n)
			}
		}
		if len(out) > 0 {
			break
		}
	}
	sort.Strings(out)
	return firstN(out, 12)
}

func lastPart(q string) string {
	if i := strings.LastIndex(q, "/"); i >= 0 {
		return q[i+1:]
	}
	return q
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
