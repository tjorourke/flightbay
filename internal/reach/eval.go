package reach

import (
	"net"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Verdict of one hop.
const (
	Allowed = "allowed"
	Denied  = "denied"
	Unknown = "unknown"
)

// hop is the result of evaluating source -> destination.
type hop struct {
	verdict string
	methods []string // nil: every method; non-nil: only these (L7 rule at a waypoint)
	paths   []string
	layer   string   // "L4" or "L7"
	reasons []string // why, one line each, naming the policies
	conds   []string // conditions we could not evaluate (JWT claims, when: clauses)
}

func (h *hop) add(r string) { h.reasons = append(h.reasons, r) }

// ---------- Kubernetes NetworkPolicy ----------

func npTypes(np *store.Object) (ingress, egress bool) {
	sp := m(np.Obj["spec"])
	types := l(sp["policyTypes"])
	if len(types) == 0 {
		// Default: Ingress always; Egress when egress rules are present.
		return true, sp["egress"] != nil
	}
	for _, t := range types {
		switch s(t) {
		case "Ingress":
			ingress = true
		case "Egress":
			egress = true
		}
	}
	return
}

func (c *cluster) policiesSelecting(ns string, lb map[string]string, egress bool) []*store.Object {
	var out []*store.Object
	for _, np := range c.netpols {
		if np.Namespace != ns {
			continue
		}
		in, eg := npTypes(np)
		if (egress && !eg) || (!egress && !in) {
			continue
		}
		if selectorMatches(m(path(np.Obj, "spec", "podSelector")), lb) {
			out = append(out, np)
		}
	}
	return out
}

// peerMatches evaluates one NetworkPolicy peer (podSelector, namespaceSelector,
// ipBlock) against a pod in namespace ns with labels lb, or an IP address.
func (c *cluster) peerMatches(peer map[string]any, policyNS, ns string, lb map[string]string, ip net.IP) bool {
	if ib := m(peer["ipBlock"]); ib != nil {
		if ip == nil {
			// Pod traffic can match an ipBlock only by pod IP; a 0.0.0.0/0
			// block covers every pod, anything narrower we cannot judge.
			return s(ib["cidr"]) == "0.0.0.0/0" && len(l(ib["except"])) == 0
		}
		_, cidr, err := net.ParseCIDR(s(ib["cidr"]))
		if err != nil || !cidr.Contains(ip) {
			return false
		}
		for _, e := range l(ib["except"]) {
			if _, ex, err := net.ParseCIDR(s(e)); err == nil && ex.Contains(ip) {
				return false
			}
		}
		return true
	}
	if ip != nil {
		return false // pod and namespace selectors never match external addresses
	}
	nsSel, hasNS := peer["namespaceSelector"]
	podSel, hasPod := peer["podSelector"]
	if hasNS {
		if !selectorMatches(m(nsSel), c.nsLabels[ns]) {
			return false
		}
	} else if ns != policyNS {
		return false
	}
	if hasPod && !selectorMatches(m(podSel), lb) {
		return false
	}
	return hasNS || hasPod
}

func portMatches(rulePorts []any, ports []string) bool {
	if len(rulePorts) == 0 {
		return true
	}
	for _, rp := range rulePorts {
		rpm := m(rp)
		want := strings.TrimSuffix(strings.TrimSuffix(s(rpm["port"]), ".0"), ".0")
		if f, ok := rpm["port"].(float64); ok {
			want = strconv.Itoa(int(f))
		}
		if want == "" {
			return true
		}
		end, _ := rpm["endPort"].(float64)
		for _, p := range ports {
			if p == want {
				return true
			}
			if n, err := strconv.Atoi(p); err == nil && end > 0 {
				if w, err := strconv.Atoi(want); err == nil && n >= w && n <= int(end) {
					return true
				}
			}
		}
	}
	return false
}

// npEgress: may pods of src send to (dstNS, dstLabels) on ports, or to ip?
func (c *cluster) npEgress(src *workload, dstNS string, dstLabels map[string]string, ports []string, ip net.IP) (string, string) {
	if !c.npKnown {
		return Unknown, "NetworkPolicies not captured"
	}
	pols := c.policiesSelecting(src.ns, src.labels, true)
	if len(pols) == 0 {
		return Allowed, "no egress NetworkPolicy selects " + src.name
	}
	for _, np := range pols {
		for _, r := range l(path(np.Obj, "spec", "egress")) {
			rm := m(r)
			to := l(rm["to"])
			peerOK := len(to) == 0
			for _, p := range to {
				if c.peerMatches(m(p), np.Namespace, dstNS, dstLabels, ip) {
					peerOK = true
				}
			}
			if peerOK && (ip != nil || portMatches(l(rm["ports"]), ports)) {
				return Allowed, "egress allowed by NetworkPolicy " + np.Namespace + "/" + np.Name
			}
		}
	}
	return Denied, "egress denied by NetworkPolicy " + names(pols)
}

// npIngress: may (srcNS, srcLabels) reach dst pods on ports?
func (c *cluster) npIngress(src *workload, dst *workload, ports []string) (string, string) {
	if !c.npKnown {
		return Unknown, "NetworkPolicies not captured"
	}
	pols := c.policiesSelecting(dst.ns, dst.labels, false)
	if len(pols) == 0 {
		return Allowed, ""
	}
	for _, np := range pols {
		for _, r := range l(path(np.Obj, "spec", "ingress")) {
			rm := m(r)
			from := l(rm["from"])
			peerOK := len(from) == 0
			for _, p := range from {
				if c.peerMatches(m(p), np.Namespace, src.ns, src.labels, nil) {
					peerOK = true
				}
			}
			if peerOK && portMatches(l(rm["ports"]), ports) {
				return Allowed, "ingress allowed by NetworkPolicy " + np.Namespace + "/" + np.Name
			}
		}
	}
	return Denied, "ingress denied by NetworkPolicy " + names(pols) + " on " + dst.name
}

func pairOf(v, why string) [2]string { return [2]string{v, why} }

func names(os []*store.Object) string {
	var n []string
	for _, o := range os {
		n = append(n, o.Namespace+"/"+o.Name)
	}
	return strings.Join(n, ", ")
}

// ---------- Istio AuthorizationPolicy ----------

// principalMatch compares a policy principal ("td/ns/x/sa/y", with * as a
// prefix or suffix wildcard) against the workload's ns/x/sa/y, ignoring the
// trust domain, which the capture does not always tell us.
func principalMatch(pattern, principal string) bool {
	if pattern == "*" {
		return true
	}
	if i := strings.Index(pattern, "/ns/"); i >= 0 {
		pattern = pattern[i+1:]
	}
	switch {
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(principal, strings.TrimPrefix(pattern, "*"))
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(principal, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == principal
}

func globMatch(pattern, v string) bool {
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

func anyMatch(list []any, f func(string) bool) bool {
	for _, x := range list {
		if f(s(x)) {
			return true
		}
	}
	return false
}

// sourceMatches evaluates rule.from against the caller. unknown is set when
// the rule depends on something we cannot see (ipBlocks, request principals).
func sourceMatches(from []any, src *workload) (match, unknown bool, cond string) {
	if len(from) == 0 {
		return true, false, ""
	}
	for _, f := range from {
		so := m(m(f)["source"])
		ok := true
		p := src.principal()
		if v := l(so["principals"]); len(v) > 0 {
			ok = ok && src.inMesh && anyMatch(v, func(x string) bool { return principalMatch(x, p) })
		}
		if v := l(so["notPrincipals"]); len(v) > 0 {
			ok = ok && !(src.inMesh && anyMatch(v, func(x string) bool { return principalMatch(x, p) }))
		}
		if v := l(so["namespaces"]); len(v) > 0 {
			ok = ok && src.inMesh && anyMatch(v, func(x string) bool { return globMatch(x, src.ns) })
		}
		if v := l(so["notNamespaces"]); len(v) > 0 {
			ok = ok && !(src.inMesh && anyMatch(v, func(x string) bool { return globMatch(x, src.ns) }))
		}
		if len(l(so["requestPrincipals"])) > 0 {
			cond = "needs a JWT (requestPrincipals)"
		}
		if len(l(so["ipBlocks"])) > 0 || len(l(so["remoteIpBlocks"])) > 0 {
			unknown = true
		}
		if ok {
			return true, unknown, cond
		}
	}
	return false, unknown, cond
}

type opMatch struct {
	match   bool
	l7      bool
	methods []string // nil = any
	paths   []string
}

func operationMatches(to []any, ports []string) opMatch {
	if len(to) == 0 {
		return opMatch{match: true}
	}
	var out opMatch
	for _, t := range to {
		op := m(m(t)["operation"])
		if v := l(op["ports"]); len(v) > 0 && !anyMatch(v, func(x string) bool {
			for _, p := range ports {
				if x == p {
					return true
				}
			}
			return false
		}) {
			continue
		}
		if v := l(op["notPorts"]); len(v) > 0 && anyMatch(v, func(x string) bool {
			for _, p := range ports {
				if x == p {
					return true
				}
			}
			return false
		}) {
			continue
		}
		out.match = true
		ms, ps := l(op["methods"]), l(op["paths"])
		if len(ms) > 0 || len(ps) > 0 || len(l(op["hosts"])) > 0 || len(l(op["notMethods"])) > 0 || len(l(op["notPaths"])) > 0 {
			out.l7 = true
		}
		if len(ms) == 0 {
			if len(l(op["notMethods"])) == 0 {
				out.methods = nil
				out.paths = append(out.paths, strList(ps)...)
				return out // one rule allows every method: nothing narrower matters
			}
			out.methods = union(out.methods, []string{"all except " + strings.Join(strList(l(op["notMethods"])), ",")})
		} else {
			out.methods = union(out.methods, strList(ms))
		}
		out.paths = union(out.paths, strList(ps))
	}
	return out
}

func strList(v []any) []string {
	var out []string
	for _, x := range v {
		out = append(out, s(x))
	}
	return out
}

// authzFor returns the AuthorizationPolicies enforced for traffic to dst via
// service sv: at ztunnel (selector or namespace-wide, plus mesh-wide in the
// root namespace) and at the waypoint, if the service uses one.
func (c *cluster) authzFor(dst *workload, sv *service) (ztunnel, waypoint []*store.Object) {
	for _, ap := range c.authz {
		sp := m(ap.Obj["spec"])
		refs := l(sp["targetRefs"])
		if tr := m(sp["targetRef"]); tr != nil {
			refs = append(refs, tr)
		}
		if len(refs) > 0 {
			if sv == nil || sv.waypoint == "" {
				continue
			}
			for _, r := range refs {
				rm := m(r)
				switch s(rm["kind"]) {
				case "Service":
					if ap.Namespace == sv.ns && s(rm["name"]) == sv.name {
						waypoint = append(waypoint, ap)
					}
				case "Gateway":
					if ap.Namespace == sv.ns && s(rm["name"]) == sv.waypoint {
						waypoint = append(waypoint, ap)
					}
				case "GatewayClass":
					if ap.Namespace == c.rootNS {
						waypoint = append(waypoint, ap)
					}
				}
			}
			continue
		}
		if ap.Namespace != dst.ns && ap.Namespace != c.rootNS {
			continue
		}
		if sel := m(sp["selector"]); sel != nil && !labelsMatch(dst.labels, strMap(sel["matchLabels"])) {
			continue
		}
		ztunnel = append(ztunnel, ap)
	}
	return
}

// evalAuthz applies DENY then ALLOW for one enforcement point. atZtunnel
// marks L4-only enforcement, where an ALLOW rule with L7 attributes matches
// nothing and a DENY rule with them denies at L4.
func evalAuthz(pols []*store.Object, src *workload, ports []string, atZtunnel bool, h *hop, where string) bool {
	var allows []*store.Object
	for _, ap := range pols {
		sp := m(ap.Obj["spec"])
		action := s(sp["action"])
		if action == "" {
			action = "ALLOW"
		}
		switch action {
		case "DENY":
			for _, r := range l(sp["rules"]) {
				rm := m(r)
				sm, _, _ := sourceMatches(l(rm["from"]), src)
				om := operationMatches(l(rm["to"]), ports)
				if sm && om.match && (atZtunnel || !om.l7 || len(om.methods) == 0) {
					h.add("denied by DENY policy " + ap.Namespace + "/" + ap.Name + " at the " + where)
					return false
				}
			}
		case "ALLOW":
			allows = append(allows, ap)
		case "CUSTOM":
			h.conds = append(h.conds, "external authorisation (CUSTOM) "+ap.Namespace+"/"+ap.Name+" at the "+where)
		}
	}
	if len(allows) == 0 {
		return true
	}
	matched := false
	var methods []string
	anyMethod := false
	for _, ap := range allows {
		rules := l(path(ap.Obj, "spec", "rules"))
		for _, r := range rules {
			rm := m(r)
			sm, unk, cond := sourceMatches(l(rm["from"]), src)
			if unk && !sm {
				h.conds = append(h.conds, "ALLOW policy "+ap.Namespace+"/"+ap.Name+" uses ipBlocks, which cannot be evaluated from a snapshot")
			}
			if !sm {
				continue
			}
			om := operationMatches(l(rm["to"]), ports)
			if !om.match {
				continue
			}
			if atZtunnel && om.l7 {
				h.conds = append(h.conds, "ALLOW policy "+ap.Namespace+"/"+ap.Name+" has HTTP rules but is enforced by ztunnel, where they match nothing")
				continue
			}
			if cond != "" {
				h.conds = append(h.conds, cond+" ("+ap.Namespace+"/"+ap.Name+")")
			}
			matched = true
			if om.methods == nil {
				anyMethod = true
			} else {
				methods = union(methods, om.methods)
				h.paths = union(h.paths, om.paths)
			}
			h.add("allowed by " + ap.Namespace + "/" + ap.Name + " at the " + where)
		}
	}
	if !matched {
		h.add("no ALLOW rule at the " + where + " admits " + src.principal() + " (" + names(allows) + ")")
		return false
	}
	if !atZtunnel && !anyMethod {
		h.methods, h.layer = methods, "L7"
	}
	return true
}

// ---------- one hop: workload -> service ----------

func (c *cluster) evalHop(src *workload, sv *service) hop {
	h := hop{layer: "L4"}
	if len(sv.backends) == 0 {
		h.verdict = Unknown
		h.add("no captured pods behind the Service")
		return h
	}
	ports := []string{}
	for _, p := range sv.ports {
		ports = append(ports, p.target, strconv.Itoa(p.port))
		if p.name != "" {
			ports = append(ports, p.name)
		}
	}
	verdict := Denied
	unknown := false
	for _, dst := range sv.backends {
		bh := hop{layer: "L4"}
		// In ambient, traffic between two mesh pods is HBONE on 15008.
		npPorts := ports
		var wp *workload
		if src.inMesh && dst.inMesh && !src.sidecar && !dst.sidecar {
			npPorts = []string{"15008"}
			// With a waypoint, the agent's ztunnel opens HBONE to the waypoint
			// pod, not to the destination: NetworkPolicy sees agent -> waypoint
			// and waypoint -> destination, both on 15008.
			wp = c.waypointFor(sv)
		}
		var eg, egWhy, in, inWhy string
		if wp != nil {
			eg, egWhy = c.npEgress(src, wp.ns, wp.labels, npPorts, nil)
			if eg == Denied {
				egWhy += " (to waypoint " + wp.ns + "/" + wp.name + ", which carries traffic for " + sv.name + ")"
			}
			// The rest of the path: into the waypoint, out of it, into dst.
			in = Allowed
			var why []string
			for _, step := range [][2]string{
				pairOf(c.npIngress(src, wp, npPorts)),
				pairOf(c.npEgress(wp, dst.ns, dst.labels, npPorts, nil)),
				pairOf(c.npIngress(wp, dst, npPorts)),
			} {
				if step[0] == Denied || (step[0] == Unknown && in == Allowed) {
					in = step[0]
				}
				if step[1] != "" && !strings.HasPrefix(step[1], "no egress NetworkPolicy") {
					why = append(why, step[1])
				}
			}
			inWhy = strings.Join(why, "; ")
		} else {
			eg, egWhy = c.npEgress(src, dst.ns, dst.labels, npPorts, nil)
			in, inWhy = c.npIngress(src, dst, npPorts)
		}
		for _, v := range []struct{ v, why string }{{eg, egWhy}, {in, inWhy}} {
			if v.why != "" {
				bh.add(v.why)
			}
		}
		if eg == Denied || in == Denied {
			h.reasons = append(h.reasons, bh.reasons...)
			continue
		}
		if eg == Unknown || in == Unknown {
			unknown = true
		}
		ok := true
		if dst.inMesh {
			if !c.apKnown {
				unknown = true
				bh.add("AuthorizationPolicies not captured")
			} else {
				zt, wp := c.authzFor(dst, sv)
				if !src.inMesh && c.strictFor(dst) {
					ok = false
					bh.add("mTLS is STRICT for " + dst.ns + " and " + src.name + " is not in the mesh")
				}
				if ok && !evalAuthz(zt, src, ports, true, &bh, "ztunnel") {
					ok = false
				}
				if ok && sv.waypoint != "" && !evalAuthz(wp, src, ports, false, &bh, "waypoint "+sv.waypoint) {
					ok = false
				}
				if ok && sv.waypoint == "" && len(wp) > 0 {
					bh.add("waypoint policies exist for " + sv.name + " but it has no waypoint, so they do not apply")
				}
			}
		} else {
			bh.add(dst.name + " is not in the mesh, so no Istio policy applies to it")
		}
		if ok {
			verdict = Allowed
			h.reasons, h.methods, h.paths, h.layer, h.conds = bh.reasons, bh.methods, bh.paths, bh.layer, bh.conds
			break
		}
		h.reasons = append(h.reasons, bh.reasons...)
	}
	if verdict == Denied && unknown {
		verdict = Unknown
	}
	if verdict == Allowed && unknown {
		h.conds = append(h.conds, "some policy types were not captured")
	}
	h.verdict = verdict
	return h
}

// strictFor reports whether mTLS is STRICT for dst (workload, namespace or
// mesh-wide PeerAuthentication; the Istio default is PERMISSIVE).
func (c *cluster) strictFor(dst *workload) bool {
	mode := ""
	rank := -1
	for _, pa := range c.peerAuth {
		sp := m(pa.Obj["spec"])
		r := -1
		switch {
		case pa.Namespace == dst.ns && sp["selector"] != nil && labelsMatch(dst.labels, strMap(path(sp, "selector", "matchLabels"))):
			r = 2
		case pa.Namespace == dst.ns && sp["selector"] == nil:
			r = 1
		case pa.Namespace == c.rootNS && sp["selector"] == nil:
			r = 0
		}
		if r > rank {
			if md := s(path(sp, "mtls", "mode")); md != "" && md != "UNSET" {
				rank, mode = r, md
			}
		}
	}
	return mode == "STRICT"
}

// egressTo evaluates a workload reaching an external IP (the internet or the
// cloud metadata endpoint). Only NetworkPolicy can stop this: ambient does not
// police traffic to destinations outside the mesh unless it goes through an
// egress gateway, which is reported separately.
func (c *cluster) egressTo(src *workload, ip net.IP) (string, string) {
	return c.npEgress(src, "", nil, nil, ip)
}
