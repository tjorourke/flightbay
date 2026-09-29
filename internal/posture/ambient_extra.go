package posture

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/solodocs"
	"github.com/tjorourke/flightbay/internal/store"
)

// ---------- shared mesh helpers (prefixed mx to keep clear of other files) ----------

// mxRef is one policy targetRef, resolved to the policy's namespace.
type mxRef struct{ group, kind, name, ns string }

// mxTargets returns spec.targetRefs plus the deprecated spec.targetRef.
func mxTargets(o *store.Object) []mxRef {
	sp := m(o.Obj["spec"])
	raw := l(sp["targetRefs"])
	if t := m(sp["targetRef"]); t != nil {
		raw = append(raw, t)
	}
	var out []mxRef
	for _, t := range raw {
		tm := m(t)
		ns := s(tm["namespace"])
		if ns == "" {
			ns = o.Namespace
		}
		out = append(out, mxRef{s(tm["group"]), s(tm["kind"]), s(tm["name"]), ns})
	}
	return out
}

// mxNum reads a JSON number (or a numeric string).
func mxNum(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

// mxAction is an AuthorizationPolicy's action, defaulting to ALLOW.
func mxAction(ap *store.Object) string {
	if a := s(path(ap.Obj, "spec", "action")); a != "" {
		return a
	}
	return "ALLOW"
}

// mxSources returns every rules[].from[].source of a policy.
func mxSources(ap *store.Object) []map[string]any {
	var out []map[string]any
	for _, r := range l(path(ap.Obj, "spec", "rules")) {
		for _, f := range l(m(r)["from"]) {
			if src := m(m(f)["source"]); src != nil {
				out = append(out, src)
			}
		}
	}
	return out
}

func mxStrings(v any) []string {
	var out []string
	for _, x := range l(v) {
		if str := s(x); str != "" {
			out = append(out, str)
		}
	}
	return out
}

// mxPrincipals returns principals and notPrincipals named by a policy.
func mxPrincipals(ap *store.Object) []string {
	var out []string
	for _, src := range mxSources(ap) {
		out = append(out, mxStrings(src["principals"])...)
		out = append(out, mxStrings(src["notPrincipals"])...)
	}
	return out
}

// mxHasSourceConstraint reports whether an ALLOW policy has rules that all
// constrain the caller.
func mxHasSourceConstraint(ap *store.Object) bool {
	rules := l(path(ap.Obj, "spec", "rules"))
	if len(rules) == 0 {
		return false
	}
	for _, r := range rules {
		from := l(m(r)["from"])
		if len(from) == 0 {
			return false
		}
		for _, f := range from {
			src := m(m(f)["source"])
			if len(src) == 0 {
				return false
			}
			str := fmt.Sprint(src)
			if strings.Contains(str, "principals:[*]") || strings.Contains(str, "namespaces:[*]") || strings.Contains(str, "0.0.0.0/0") {
				return false
			}
		}
	}
	return true
}

func mxDecode(b []byte) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

// mxLabelMap turns a JSON label map into strings.
func mxLabelMap(v any) map[string]string {
	out := map[string]string{}
	for k, x := range m(v) {
		out[k] = s(x)
	}
	return out
}

// mxEnv looks up an environment variable across a workload's containers.
func mxEnv(o *store.Object, name string) (string, bool) {
	for _, ct := range containers(podSpec(o)) {
		for _, ev := range l(ct.c["env"]) {
			em := m(ev)
			if s(em["name"]) == name {
				return s(em["value"]), true
			}
		}
	}
	return "", false
}

// mxIstiods returns the istiod Deployments (label app=istiod).
func mxIstiods(e env) []*store.Object {
	var out []*store.Object
	for _, d := range e.list("apps", "deployments") {
		if labels(d)["app"] == "istiod" || podTemplateLabels(d)["app"] == "istiod" {
			out = append(out, d)
		}
	}
	return out
}

// mxReplicas is max(spec.replicas, HPA minReplicas) and a description.
func mxReplicas(e env, d *store.Object) (int, string) {
	n := 1
	if v, ok := mxNum(path(d.Obj, "spec", "replicas")); ok {
		n = int(v)
	}
	desc := fmt.Sprintf("%d replica(s)", n)
	for _, h := range e.ws.List(e.cluster, "autoscaling", "horizontalpodautoscalers", d.Namespace, nil) {
		ref := m(path(h.Obj, "spec", "scaleTargetRef"))
		if s(ref["kind"]) == d.Kind && s(ref["name"]) == d.Name {
			min := 1
			if v, ok := mxNum(path(h.Obj, "spec", "minReplicas")); ok {
				min = int(v)
			}
			desc = fmt.Sprintf("%d replica(s), HPA %s minReplicas %d", n, h.Name, min)
			if min > n {
				n = min
			}
		}
	}
	return n, desc
}

// mxPDBs returns the PodDisruptionBudgets selecting a workload's pods.
func mxPDBs(e env, d *store.Object) []*store.Object {
	lb := podTemplateLabels(d)
	var out []*store.Object
	for _, p := range e.ws.List(e.cluster, "policy", "poddisruptionbudgets", d.Namespace, nil) {
		sel := m(path(p.Obj, "spec", "selector"))
		if sel != nil && len(lb) > 0 && selects(sel, lb) {
			out = append(out, p)
		}
	}
	return out
}

// mxPDBBlocks reports whether a PDB allows no voluntary eviction at the
// given replica count.
func mxPDBBlocks(p *store.Object, replicas int) bool {
	sp := m(p.Obj["spec"])
	if v, ok := num(sp["minAvailable"]); ok && int(v) >= replicas {
		return true
	}
	if s(sp["minAvailable"]) == "100%" {
		return true
	}
	if v, ok := num(sp["maxUnavailable"]); ok && v == 0 {
		return true
	}
	return s(sp["maxUnavailable"]) == "0%"
}

// mxImageTag returns an image's tag, or "" for digests and untagged images.
func mxImageTag(image string) string {
	if strings.Contains(image, "@") {
		return ""
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return ""
}

// mxPod builds a reference to a pod named in a diagnostic.
func mxPod(ns, name string) *store.Object {
	return &store.Object{Resource: "pods", Kind: "Pod", Namespace: ns, Name: name}
}

// mxGatewaysByRole returns Gateway API gateways of a role.
func mxGatewaysByRole(e env, roles ...string) []*store.Object {
	cls := gwClassControllers(e)
	var out []*store.Object
	for _, gw := range e.list("gateway.networking.k8s.io", "gateways") {
		r := gwRole(gw, cls)
		for _, want := range roles {
			if r == want {
				out = append(out, gw)
			}
		}
	}
	return out
}

// mxTrustDomains resolves the mesh trust domain and aliases, and where it
// came from. ok=false when nothing in the capture says.
func mxTrustDomains(e env) (tds []string, src string, ok bool) {
	for _, d := range e.ws.Diagnostics(e.cluster) {
		if d.Component != "istiod" {
			continue
		}
		mesh := m(mxDecode(d.Files["mesh.json"]))
		if td := s(mesh["trustDomain"]); td != "" {
			tds = append([]string{td}, mxStrings(mesh["trustDomainAliases"])...)
			return tds, "istiod mesh config (" + d.Pod + ")", true
		}
	}
	for _, smc := range e.list("operator.gloo.solo.io", "servicemeshcontrollers") {
		sp := m(smc.Obj["spec"])
		if td := s(sp["trustDomain"]); td != "" {
			return []string{td}, "ServiceMeshController " + smc.Name + " spec.trustDomain", true
		}
		if cl := s(sp["cluster"]); cl != "" {
			return []string{cl}, "ServiceMeshController " + smc.Name + " spec.cluster", true
		}
	}
	seen := map[string]bool{}
	for _, d := range e.ws.Diagnostics(e.cluster) {
		for _, id := range mxSpiffeIDs(d) {
			if td := mxTrustDomainOf(strings.TrimPrefix(id, "spiffe://")); td != "" && !seen[td] {
				seen[td] = true
				tds = append(tds, td)
			}
		}
	}
	if len(tds) > 0 {
		sort.Strings(tds)
		return tds, "SPIFFE identities in proxy certificates", true
	}
	return nil, "", false
}

// mxSpiffeIDs returns the SPIFFE URIs in a proxy's certificates.
func mxSpiffeIDs(d store.Diagnostic) []string {
	var out []string
	switch d.Component {
	case "ztunnel":
		for _, c := range l(m(mxDecode(d.Files["config_dump.json"]))["certificates"]) {
			if id := s(m(c)["identity"]); strings.HasPrefix(id, "spiffe://") {
				out = append(out, id)
			}
		}
	case "envoy":
		for _, c := range l(m(mxDecode(d.Files["certs.json"]))["certificates"]) {
			for _, cc := range l(m(c)["cert_chain"]) {
				for _, san := range l(m(cc)["subject_alt_names"]) {
					if u := s(m(san)["uri"]); strings.HasPrefix(u, "spiffe://") {
						out = append(out, u)
					}
				}
			}
		}
	}
	return out
}

// mxTrustDomainOf returns the trust domain of td/ns/x/sa/y ("" if not SPIFFE-shaped).
func mxTrustDomainOf(principal string) string {
	if i := strings.Index(principal, "/ns/"); i >= 0 {
		return principal[:i]
	}
	if i := strings.Index(principal, "/"); i > 0 {
		return principal[:i]
	}
	return ""
}

// mxTDMatch applies Istio's trust-domain matching: "*" matches any, a
// leading or trailing * is a suffix or prefix match, and cluster.local always
// refers to the local trust domain.
//
// ztunnel compares principals literally, so for policies it enforces
// (clusterLocalAlias=false) cluster.local only matches when it really is the
// trust domain. Waypoints rewrite it to the local trust domain.
func mxTDMatch(td string, tds []string, clusterLocalAlias bool) bool {
	if td == "" || td == "*" || (td == "cluster.local" && clusterLocalAlias) {
		return true
	}
	for _, t := range tds {
		switch {
		case strings.HasPrefix(td, "*") && strings.HasSuffix(t, td[1:]):
			return true
		case strings.HasSuffix(td, "*") && strings.HasPrefix(t, td[:len(td)-1]):
			return true
		case t == td:
			return true
		}
	}
	return false
}

// mxCertIDs returns the root and intermediate serials seen in a proxy.
func mxCertIDs(d store.Diagnostic) (roots, inters map[string]bool) {
	roots, inters = map[string]bool{}, map[string]bool{}
	switch d.Component {
	case "ztunnel":
		for _, c := range l(m(mxDecode(d.Files["config_dump.json"]))["certificates"]) {
			cm := m(c)
			for _, r := range l(cm["rootCerts"]) {
				if sn := s(m(r)["serialNumber"]); sn != "" {
					roots[sn] = true
				}
			}
			chain := l(cm["certChain"])
			for i := 1; i < len(chain); i++ {
				if sn := s(m(chain[i])["serialNumber"]); sn != "" {
					inters[sn] = true
				}
			}
		}
	case "envoy":
		for _, c := range l(m(mxDecode(d.Files["certs.json"]))["certificates"]) {
			cm := m(c)
			for _, r := range l(cm["ca_cert"]) {
				if sn := s(m(r)["serial_number"]); sn != "" {
					roots[sn] = true
				}
			}
			chain := l(cm["cert_chain"])
			for i := 1; i < len(chain); i++ {
				if sn := s(m(chain[i])["serial_number"]); sn != "" {
					inters[sn] = true
				}
			}
		}
	}
	for sn := range roots {
		delete(inters, sn) // a chain may end in the root itself
	}
	return roots, inters
}

// mxMetrics sums Prometheus text-format samples per metric name.
func mxMetrics(b []byte) map[string]float64 {
	out := map[string]float64{}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		name := ln
		if i := strings.IndexAny(ln, "{ "); i >= 0 {
			name = ln[:i]
		}
		f := strings.Fields(ln[strings.LastIndex(ln, "}")+1:])
		if len(f) == 0 {
			continue
		}
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			out[name] += v
		}
	}
	return out
}

func mxSetKeys(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mxShort(set map[string]bool) string {
	ks := mxSetKeys(set)
	for i, k := range ks {
		if len(k) > 12 {
			ks[i] = k[:12] + "..."
		}
	}
	return strings.Join(ks, ",")
}

// ---------- the mesh context ----------

type meshCtx struct {
	e         env
	nsl       map[string]map[string]string
	nsKnown   bool
	ambientNS map[string]bool
	gwClass   map[string]string
	gws       map[string]*store.Object // ns/name
	aps, ras  []*store.Object
	svcs      map[string]*store.Object // ns/name
	ses       []*store.Object
	root      string
	// l7 maps Kind|ns|name (and GatewayClass||name) to the policies that target it.
	l7 map[string][]string
}

func newMeshCtx(e env) *meshCtx {
	x := &meshCtx{e: e, ambientNS: map[string]bool{}, gws: map[string]*store.Object{}, svcs: map[string]*store.Object{}, l7: map[string][]string{}, root: "istio-system"}
	x.nsl, x.nsKnown = e.nsLabels()
	for ns, lb := range x.nsl {
		if lb["istio.io/dataplane-mode"] == "ambient" {
			x.ambientNS[ns] = true
		}
	}
	x.gwClass = gwClassControllers(e)
	for _, gw := range e.list("gateway.networking.k8s.io", "gateways") {
		x.gws[gw.Namespace+"/"+gw.Name] = gw
	}
	for _, sv := range e.list("", "services") {
		x.svcs[sv.Namespace+"/"+sv.Name] = sv
	}
	x.aps = e.list("security.istio.io", "authorizationpolicies")
	x.ras = e.list("security.istio.io", "requestauthentications")
	x.ses = e.list("networking.istio.io", "serviceentries")
	var pols []*store.Object
	pols = append(pols, x.aps...)
	pols = append(pols, x.ras...)
	pols = append(pols, e.list("extensions.istio.io", "wasmplugins")...)
	pols = append(pols, e.list("agentgateway.dev", "agentgatewaypolicies")...)
	pols = append(pols, e.list("enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies")...)
	for _, p := range pols {
		for _, t := range mxTargets(p) {
			k := t.kind + "|" + t.ns + "|" + t.name
			if t.kind == "GatewayClass" {
				k = "GatewayClass||" + t.name
			}
			x.l7[k] = append(x.l7[k], p.Kind+" "+p.Namespace+"/"+p.Name)
		}
	}
	return x
}

func (x *meshCtx) role(gw *store.Object) string { return gwRole(gw, x.gwClass) }

// waypointOf resolves the istio.io/use-waypoint label on an object, falling
// back to its namespace. none=true when it opts out explicitly.
func (x *meshCtx) waypointOf(o *store.Object) (ns, name string, none bool) {
	pick := func(lb map[string]string, defNS string) (string, string, bool, bool) {
		wp := lb["istio.io/use-waypoint"]
		if wp == "" {
			return "", "", false, false
		}
		n := lb["istio.io/use-waypoint-namespace"]
		if n == "" {
			n = defNS
		}
		return n, wp, wp == "none", true
	}
	if a, b, c, ok := pick(labels(o), o.Namespace); ok {
		return a, b, c
	}
	if a, b, c, ok := pick(x.nsl[o.Namespace], o.Namespace); ok {
		return a, b, c
	}
	return "", "", false
}

// waypointGW returns the waypoint Gateway an object uses, or nil.
func (x *meshCtx) waypointGW(o *store.Object) *store.Object {
	ns, name, none := x.waypointOf(o)
	if name == "" || none {
		return nil
	}
	return x.gws[ns+"/"+name]
}

// l7For returns the L7 policies that target a Service or its waypoint.
func (x *meshCtx) l7For(sv *store.Object) []string {
	out := append([]string{}, x.l7["Service|"+sv.Namespace+"|"+sv.Name]...)
	if wp := x.waypointGW(sv); wp != nil {
		out = append(out, x.l7["Gateway|"+wp.Namespace+"|"+wp.Name]...)
		out = append(out, x.l7["GatewayClass||"+gatewayClass(wp)]...)
	}
	return out
}

// podsOfService returns the captured pods a Service selects.
func (x *meshCtx) podsOfService(sv *store.Object) []*store.Object {
	sel := mxLabelMap(path(sv.Obj, "spec", "selector"))
	if len(sel) == 0 {
		return nil
	}
	return x.e.podsIn(sv.Namespace, sel)
}

// apSelects reports whether an AuthorizationPolicy applies to a Service's
// workloads: by targetRefs to the Service, by selector, or namespace-wide.
func (x *meshCtx) apSelects(ap, sv *store.Object) bool {
	if ap.Namespace != sv.Namespace && ap.Namespace != x.root {
		return false
	}
	tr := mxTargets(ap)
	if len(tr) > 0 {
		for _, t := range tr {
			if t.kind == "Service" && t.name == sv.Name && ap.Namespace == sv.Namespace {
				return true
			}
		}
		return false
	}
	sel := m(path(ap.Obj, "spec", "selector"))
	if sel == nil {
		return true
	}
	pods := x.podsOfService(sv)
	for _, p := range pods {
		if selects(sel, labels(p)) {
			return true
		}
	}
	if len(pods) == 0 {
		return selects(sel, mxLabelMap(path(sv.Obj, "spec", "selector")))
	}
	return false
}

// waypointSA is the service account a waypoint's proxies run as.
func (x *meshCtx) waypointSA(wp *store.Object) (string, string) {
	for _, p := range x.e.gatewayPods(wp) {
		if sa := s(path(p.Obj, "spec", "serviceAccountName")); sa != "" {
			return sa, "from pod " + p.Name
		}
	}
	if sa := s(m(path(wp.Obj, "metadata", "annotations"))["gateway.istio.io/service-account"]); sa != "" {
		return sa, "from the gateway.istio.io/service-account annotation"
	}
	return wp.Name, "assumed: no waypoint pods captured"
}

// multicluster reports whether the cluster peers with others.
func (x *meshCtx) multicluster() (bool, string) {
	if gws := mxGatewaysByRole(x.e, "eastwest", "remote"); len(gws) > 0 {
		return true, "east-west or remote Gateway " + gws[0].Namespace + "/" + gws[0].Name
	}
	for _, sv := range x.svcs {
		if labels(sv)["solo.io/service-scope"] != "" {
			return true, "Service " + sv.Namespace + "/" + sv.Name + " has solo.io/service-scope"
		}
	}
	for ns, lb := range x.nsl {
		if lb["solo.io/service-scope"] != "" {
			return true, "namespace " + ns + " has solo.io/service-scope"
		}
	}
	return false, ""
}

func (x *meshCtx) isGatewayPod(p *store.Object) bool {
	lb := labels(p)
	return lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["gateway.istio.io/managed"] != "" || lb["istio.io/gateway-name"] != "" ||
		lb["istio"] == "ingressgateway" || lb["istio"] == "eastwestgateway" || lb["istio"] == "egressgateway"
}

func sortedObjs(os []*store.Object) []*store.Object {
	sort.Slice(os, func(i, j int) bool {
		return os[i].Namespace+"/"+os[i].Name < os[j].Namespace+"/"+os[j].Name
	})
	return os
}

// ---------- the checks ----------

// ambientExtra holds the Istio ambient, control-plane and multicluster checks
// added after the first release.
func ambientExtra(e env) []Check {
	var out []Check
	add := func(sp spec, eval func(*Check)) {
		c := sp.check("ambient")
		eval(&c)
		out = append(out, c)
	}
	na := func(c *Check, why string) { c.Status, c.Reason = NA, why }
	x := newMeshCtx(e)
	noNS := e.noNSWhy()
	apOK, apWhy := e.evaluable("security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true)
	detailOK, detailWhy := e.fullDetail()

	add(spec{"ambient.policy-enforced", "Authorization", "high", "Mesh policies are bound to something that enforces them",
		"In ambient a policy that targets a Service or ServiceEntry is only enforced by that Service's waypoint. Without one, or when istiod reports the policy as not bound, the rules you wrote do nothing.",
		"# give the target a waypoint\nkubectl label service <svc> istio.io/use-waypoint=waypoint\n# then check the policy status\nkubectl get authorizationpolicy <name> -o jsonpath='{.status.conditions}'",
		meshBase + "whose-waypoint-l7-multicluster/", solodocs.Istio + "security/authorization/"}, func(c *Check) {
		if !detailOK {
			na(c, detailWhy)
			return
		}
		var pols []*store.Object
		pols = append(pols, x.aps...)
		pols = append(pols, x.ras...)
		pols = append(pols, e.list("telemetry.istio.io", "telemetries")...)
		for _, p := range sortedObjs(pols) {
			var bad, good []string
			if p.Kind == "AuthorizationPolicy" {
				st := m(p.Obj["status"])
				lists := [][]any{l(st["conditions"])}
				for _, a := range l(st["ancestors"]) {
					lists = append(lists, l(m(a)["conditions"]))
				}
				for _, cs := range lists {
					for _, cd := range cs {
						cm := m(cd)
						typ, stat, reason := s(cm["type"]), s(cm["status"]), s(cm["reason"])
						if typ != "ZtunnelAccepted" && typ != "WaypointAccepted" {
							continue
						}
						switch {
						case stat == "False" || reason == "PartiallyInvalid" || reason == "AncestorNotBound" || reason == "TargetNotFound":
							msg := typ + " " + stat + " (" + reason + ")"
							if mm := s(cm["message"]); mm != "" {
								msg += ": " + mm
							}
							bad = append(bad, msg)
						default:
							good = append(good, typ)
						}
					}
				}
			}
			targets := mxTargets(p)
			for _, t := range targets {
				switch t.kind {
				case "Service", "ServiceEntry":
					var tgt *store.Object
					if t.kind == "Service" {
						tgt = x.svcs[t.ns+"/"+t.name]
					} else {
						tgt = e.ws.Get(e.cluster, "networking.istio.io", "serviceentries", t.ns, t.name)
					}
					if tgt == nil {
						if ok, _ := e.absenceOK(map[string]string{"Service": "", "ServiceEntry": "networking.istio.io"}[t.kind],
							map[string]string{"Service": "services", "ServiceEntry": "serviceentries"}[t.kind], t.kind, true); ok {
							bad = append(bad, t.kind+" "+t.name+" does not exist")
						}
						continue
					}
					_, wp, none := x.waypointOf(tgt)
					switch {
					case wp == "":
						bad = append(bad, t.kind+" "+t.name+" has no istio.io/use-waypoint, so nothing enforces this")
					case none:
						bad = append(bad, t.kind+" "+t.name+" opts out with istio.io/use-waypoint=none")
					default:
						good = append(good, t.kind+" "+t.name+" via waypoint "+wp)
					}
				case "Gateway":
					if x.gws[t.ns+"/"+t.name] == nil {
						if ok, _ := e.absenceOK("gateway.networking.k8s.io", "gateways", "Gateway", true); ok {
							bad = append(bad, "Gateway "+t.name+" does not exist")
						}
					} else {
						good = append(good, "Gateway "+t.name)
					}
				default:
					good = append(good, t.kind+" "+t.name)
				}
			}
			switch {
			case len(bad) > 0:
				c.Items = append(c.Items, e.item(p, Fail, strings.Join(bad, "; ")))
			case len(good) > 0:
				c.Items = append(c.Items, e.item(p, Pass, "bound: "+strings.Join(good, ", ")))
			}
		}
		if len(c.Items) == 0 {
			if !apOK {
				na(c, apWhy)
			} else {
				na(c, "no mesh policies with targetRefs or binding status")
			}
		}
	})

	add(spec{"ambient.ingress-use-waypoint", "Authorization", "high", "Ingress traffic goes through the waypoint that holds the policy",
		"An ingress gateway sends straight to the pods of a backend Service unless the Service opts in with istio.io/ingress-use-waypoint. Any L7 policy on its waypoint is then skipped for traffic from outside.",
		"kubectl label service <svc> istio.io/ingress-use-waypoint=true\n# or for the whole namespace\nkubectl label namespace <ns> istio.io/ingress-use-waypoint=true",
		meshBase + "istio-ambient-migration-kind/", solodocs.Istio + "waypoints/configuration/"}, func(c *Check) {
		ingress := map[string]bool{}
		for _, gw := range x.gws {
			if x.role(gw) == "ingress" {
				ingress[gw.Namespace+"/"+gw.Name] = true
			}
		}
		if len(ingress) == 0 {
			na(c, "no ingress Gateways")
			return
		}
		seen := map[string]bool{}
		var routes []*store.Object
		routes = append(routes, e.list("gateway.networking.k8s.io", "httproutes")...)
		routes = append(routes, e.list("gateway.networking.k8s.io", "grpcroutes")...)
		for _, r := range routes {
			onIngress := ""
			for _, pr := range gwParents(r) {
				if ingress[pr] {
					onIngress = pr
				}
			}
			if onIngress == "" {
				continue
			}
			for _, br := range gwBackendRefs(r) {
				if br.kind != "Service" || br.group != "" {
					continue
				}
				k := br.ns + "/" + br.name
				sv := x.svcs[k]
				if sv == nil || seen[k] {
					continue
				}
				seen[k] = true
				wp := x.waypointGW(sv)
				pols := x.l7For(sv)
				if wp == nil || len(pols) == 0 {
					continue
				}
				if labels(sv)["istio.io/ingress-use-waypoint"] == "true" || x.nsl[sv.Namespace]["istio.io/ingress-use-waypoint"] == "true" {
					c.Items = append(c.Items, e.item(sv, Pass, "ingress "+onIngress+" goes through waypoint "+wp.Name))
				} else {
					c.Items = append(c.Items, e.item(sv, Fail, fmt.Sprintf("reached from ingress %s via %s %s/%s, but ingress skips waypoint %s, so %s does not apply", onIngress, r.Kind, r.Namespace, r.Name, wp.Name, strings.Join(pols, ", "))))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no Service behind an ingress Gateway has a waypoint with L7 policy")
		}
	})

	add(spec{"ambient.waypoint-no-bypass", "Authorization", "medium", "Workloads behind a waypoint only accept traffic from it",
		"A waypoint enforces L7 policy only on traffic that goes through it. A caller that dials the pod IP directly skips it, unless the workload itself only allows the waypoint's identity.",
		"apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: allow-waypoint, namespace: <ns>}\nspec:\n  selector: {matchLabels: {app: <app>}}\n  action: ALLOW\n  rules:\n  - from:\n    - source:\n        principals: [\"cluster.local/ns/<ns>/sa/<waypoint>\"]",
		meshBase + "whose-waypoint-l7-multicluster/", solodocs.Istio + "security/authorization/waypoint-authz/"}, func(c *Check) {
		if !apOK {
			na(c, apWhy)
			return
		}
		keys := make([]string, 0, len(x.svcs))
		for k := range x.svcs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sv := x.svcs[k]
			wp := x.waypointGW(sv)
			if wp == nil || len(x.l7For(sv)) == 0 {
				continue
			}
			sa, how := x.waypointSA(wp)
			found := ""
			for _, ap := range x.aps {
				if mxAction(ap) != "ALLOW" || !x.apSelects(ap, sv) {
					continue
				}
				for _, src := range mxSources(ap) {
					for _, p := range mxStrings(src["principals"]) {
						if strings.HasSuffix(p, "/sa/"+sa) && (strings.Contains(p, "/ns/"+wp.Namespace+"/") || strings.Contains(p, "/ns/*/")) {
							found = ap.Name
						}
					}
				}
			}
			if found != "" {
				c.Items = append(c.Items, e.item(sv, Pass, "ALLOW "+found+" admits waypoint service account "+sa))
			} else {
				c.Items = append(c.Items, e.item(sv, Warn, fmt.Sprintf("uses waypoint %s with L7 policy, but no ALLOW policy on its pods limits callers to the waypoint service account %s (%s)", wp.Name, sa, how)))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no Services with a waypoint and L7 policy")
		}
	})

	add(spec{"ambient.principal-trust-domain", "Authorization", "high", "Principals use the mesh trust domain",
		"A principal is matched on its full SPIFFE ID. One written with another trust domain never matches: an ALLOW rule then admits no-one, and a DENY rule blocks nothing.",
		"rules:\n- from:\n  - source:\n      principals: [\"<trust-domain>/ns/<ns>/sa/<sa>\"]   # cluster.local also means the local trust domain",
		meshBase + "istio-ambient-cert-identity-kind/", solodocs.Istio + "security/workload-identity/"}, func(c *Check) {
		if !apOK {
			na(c, apWhy)
			return
		}
		tds, src, ok := mxTrustDomains(e)
		for _, ap := range sortedObjs(append([]*store.Object{}, x.aps...)) {
			ps := mxPrincipals(ap)
			if len(ps) == 0 {
				continue
			}
			if !ok {
				continue
			}
			// Policies with targetRefs run in a waypoint, which treats
			// cluster.local as the local trust domain; the rest run in ztunnel.
			waypoint := len(mxTargets(ap)) > 0
			var bad []string
			for _, p := range ps {
				if p == "*" {
					continue
				}
				if td := mxTrustDomainOf(p); !mxTDMatch(td, tds, waypoint) {
					bad = append(bad, p)
				}
			}
			if len(bad) > 0 {
				why := ""
				if !waypoint && strings.Contains(strings.Join(bad, ","), "cluster.local/") {
					why = "; ztunnel enforces this policy and matches principals literally, so cluster.local is not rewritten"
				}
				c.Items = append(c.Items, e.item(ap, Fail, fmt.Sprintf("principals %s are not in trust domain %s (%s)%s", strings.Join(bad, ", "), strings.Join(tds, ","), src, why)))
			} else {
				c.Items = append(c.Items, e.item(ap, Pass, "trust domain "+strings.Join(tds, ",")))
			}
		}
		if len(c.Items) == 0 {
			if !ok {
				na(c, "the mesh trust domain is unknown: capture istiod diagnostics (mesh.json) or proxy certificates")
			} else {
				na(c, "no policies name principals")
			}
		}
	})

	add(spec{"ambient.authz-references-resolve", "Authorization", "medium", "Authorization policies refer to things that exist",
		"A selector that matches no pod, a namespace that does not exist or a service account nobody runs as all make a rule silently match nothing. For a DENY rule that means nothing is denied.",
		"kubectl get pods -n <ns> -l <selector>\nkubectl get serviceaccount -n <ns> <sa>", meshBase + "istio-ambient-crds/", solodocs.Istio + "security/authorization/"}, func(c *Check) {
		if !apOK {
			na(c, apWhy)
			return
		}
		if !detailOK {
			na(c, detailWhy)
			return
		}
		podsOK, _ := e.absenceOK("", "pods", "Pod", true)
		saOK, _ := e.absenceOK("", "serviceaccounts", "ServiceAccount", true)
		for _, ap := range sortedObjs(append([]*store.Object{}, x.aps...)) {
			st, notes := Pass, []string{}
			worse := func(s Status, msg string) {
				if statusRank[s] < statusRank[st] {
					st = s
				}
				notes = append(notes, msg)
			}
			if ml := mxLabelMap(path(ap.Obj, "spec", "selector", "matchLabels")); len(ml) > 0 && podsOK {
				ns := ap.Namespace
				if ns == x.root {
					ns = ""
				}
				if len(e.podsIn(ns, ml)) == 0 {
					worse(Warn, fmt.Sprintf("selector %v matches no pod", ml))
				}
			}
			for _, src := range mxSources(ap) {
				if x.nsKnown {
					for _, n := range append(mxStrings(src["namespaces"]), mxStrings(src["notNamespaces"])...) {
						if !strings.Contains(n, "*") {
							if _, ok := x.nsl[n]; !ok {
								worse(Warn, "namespace "+n+" does not exist")
							}
						}
					}
				}
				if !saOK {
					continue
				}
				for _, p := range append(mxStrings(src["principals"]), mxStrings(src["notPrincipals"])...) {
					i := strings.Index(p, "/ns/")
					if i < 0 || strings.Contains(p, "*") {
						continue
					}
					parts := strings.Split(p[i+1:], "/")
					if len(parts) != 4 || parts[2] != "sa" {
						continue
					}
					if _, captured := x.nsl[parts[1]]; !captured && x.nsKnown {
						continue
					}
					if e.ws.Get(e.cluster, "", "serviceaccounts", parts[1], parts[3]) == nil && len(e.ws.List(e.cluster, "", "serviceaccounts", parts[1], nil)) > 0 {
						if mxAction(ap) == "DENY" {
							worse(Fail, "DENY names service account "+parts[1]+"/"+parts[3]+", which does not exist, so it denies nothing")
						} else {
							worse(Warn, "service account "+parts[1]+"/"+parts[3]+" does not exist")
						}
					}
				}
			}
			if st == Pass {
				c.Items = append(c.Items, e.item(ap, Pass, "references resolve"))
			} else {
				c.Items = append(c.Items, e.item(ap, st, strings.Join(notes, "; ")))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no AuthorizationPolicies")
		}
	})

	add(spec{"ambient.dedicated-service-accounts", "Identity", "medium", "Workloads that policies name have their own service account",
		"The mesh identity is the service account. Allowing sa/default, or an account several workloads share, admits every workload that runs as it.",
		"apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: <app>, namespace: <ns>}\n---\n# in the Deployment\nspec:\n  template:\n    spec:\n      serviceAccountName: <app>",
		meshBase + "istio-ambient-cert-identity-kind/", solodocs.Istio + "security/workload-identity/"}, func(c *Check) {
		if !x.nsKnown {
			na(c, noNS)
			return
		}
		if len(x.ambientNS) == 0 {
			na(c, "no namespaces are enrolled in ambient")
			return
		}
		if !detailOK {
			na(c, detailWhy)
			return
		}
		named := map[string]string{} // ns/sa -> policy
		for _, ap := range x.aps {
			if mxAction(ap) != "ALLOW" {
				continue
			}
			for _, src := range mxSources(ap) {
				for _, p := range mxStrings(src["principals"]) {
					if i := strings.Index(p, "/ns/"); i >= 0 {
						if parts := strings.Split(p[i+1:], "/"); len(parts) == 4 && parts[2] == "sa" {
							named[parts[1]+"/"+parts[3]] = ap.Namespace + "/" + ap.Name
						}
					}
				}
			}
		}
		saOf := func(o *store.Object) string {
			if sa := s(podSpec(o)["serviceAccountName"]); sa != "" {
				return sa
			}
			return "default"
		}
		users := map[string][]string{}
		for _, w := range e.workloads() {
			if !x.ambientNS[w.Namespace] || w.Kind == "CronJob" || w.Kind == "Job" {
				continue
			}
			if lb := podTemplateLabels(w); lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["gateway.istio.io/managed"] != "" || lb["istio.io/gateway-name"] != "" {
				continue
			}
			k := w.Namespace + "/" + saOf(w)
			users[k] = append(users[k], w.Kind+" "+w.Name)
		}
		for _, ns := range mxSetKeys(x.ambientNS) {
			var notes []string
			st := Pass
			if u := users[ns+"/default"]; len(u) > 0 {
				if pol, ok := named[ns+"/default"]; ok {
					st = Warn
					notes = append(notes, fmt.Sprintf("%s run as sa/default, and %s allows sa/default", strings.Join(u, ", "), pol))
				} else {
					notes = append(notes, fmt.Sprintf("%d workload(s) run as sa/default; no policy allows it", len(u)))
				}
			}
			var shared []string
			for k, u := range users {
				if strings.HasPrefix(k, ns+"/") && len(u) > 1 && !strings.HasSuffix(k, "/default") {
					if pol, ok := named[k]; ok {
						shared = append(shared, fmt.Sprintf("%s is shared by %s and allowed by %s", k, strings.Join(u, ", "), pol))
					}
				}
			}
			sort.Strings(shared)
			if len(shared) > 0 {
				st = Warn
				notes = append(notes, shared...)
			}
			if len(notes) == 0 {
				notes = append(notes, "every workload has its own identity")
			}
			c.Items = append(c.Items, e.item(nsObj(ns), st, strings.Join(notes, "; ")))
		}
	})

	add(spec{"ambient.workloads-captured", "Encryption", "critical", "Pods in ambient namespaces are captured by ztunnel",
		"A pod in an ambient namespace is only in the mesh once istio-cni has redirected its traffic. Pods on the host network, or ones the CNI missed, send and accept plaintext with no identity.",
		"# restart pods that started before the CNI was ready\nkubectl rollout restart deployment -n <ns>\n# check\nkubectl get pods -n <ns> -o jsonpath='{range .items[*]}{.metadata.name} {.metadata.annotations.ambient\\.istio\\.io/redirection}{\"\\n\"}{end}'",
		meshBase + "istio-ambient-poc-eks/", solodocs.Istio + "troubleshooting/service-mesh/ambient/"}, func(c *Check) {
		if e.runtimeNA(c, "ztunnel redirection (a pod annotation set by istio-cni)") {
			return
		}
		if !x.nsKnown {
			na(c, noNS)
			return
		}
		for _, p := range sortedObjs(e.pods()) {
			lb := labels(p)
			if !(x.ambientNS[p.Namespace] || lb["istio.io/dataplane-mode"] == "ambient") || lb["istio.io/dataplane-mode"] == "none" || x.isGatewayPod(p) {
				continue
			}
			if isSystem(p.Namespace) || istioSystemWorkload(p) != "" {
				continue
			}
			if ph := s(path(p.Obj, "status", "phase")); ph != "Running" {
				continue
			}
			ann := m(path(p.Obj, "metadata", "annotations"))
			hostNet, _ := path(p.Obj, "spec", "hostNetwork").(bool)
			switch {
			case hostNet:
				c.Items = append(c.Items, e.item(p, Fail, "hostNetwork pod: ztunnel cannot capture it"))
			case s(ann["ambient.istio.io/redirection"]) != "enabled":
				why := "no ambient.istio.io/redirection annotation: istio-cni has not captured it"
				if v := s(ann["ambient.istio.io/redirection"]); v != "" {
					why = "ambient.istio.io/redirection is " + v
				}
				c.Items = append(c.Items, e.item(p, Fail, why))
			default:
				c.Items = append(c.Items, e.item(p, Pass, "redirection enabled"))
			}
		}
		if len(c.Items) == 0 {
			if ok, why := e.evaluable("", "pods", "Pod", true); !ok {
				na(c, why)
			} else {
				na(c, "no running application pods in ambient namespaces")
			}
		}
	})

	add(spec{"ambient.serviceentry-waypoint", "Isolation", "high", "External ServiceEntries are bound to a waypoint",
		"ztunnel cannot apply L7 policy, TLS origination or host-based rules to external hosts. A ServiceEntry for an external API should go through an egress waypoint, and a wildcard one needs one to work at all.",
		"kubectl label serviceentry <name> istio.io/use-waypoint=egress-waypoint istio.io/use-waypoint-namespace=istio-egress",
		meshBase + "ztunnel-native-egress/", solodocs.Istio + "traffic-management/egress/egress/"}, func(c *Check) {
		for _, se := range sortedObjs(append([]*store.Object{}, x.ses...)) {
			var bad []string
			if st, _, msg, ok := condition(se, "istio.io/WaypointMissing"); ok && st == "True" {
				bad = append(bad, "WaypointMissing: "+msg)
			}
			if st, reason, msg, ok := condition(se, "istio.io/WaypointBound"); ok && st == "False" {
				bad = append(bad, "WaypointBound False ("+reason+"): "+msg)
			}
			external := s(path(se.Obj, "spec", "location")) == "MESH_EXTERNAL"
			_, wp, none := x.waypointOf(se)
			switch {
			case len(bad) > 0:
				c.Items = append(c.Items, e.item(se, Fail, strings.Join(bad, "; ")))
			case !external:
				continue
			case !x.ambientNS[se.Namespace]:
				continue
			case wp == "" || none:
				c.Items = append(c.Items, e.item(se, Warn, "MESH_EXTERNAL in an ambient namespace with no istio.io/use-waypoint"))
			default:
				c.Items = append(c.Items, e.item(se, Pass, "egress through waypoint "+wp))
			}
		}
		if len(c.Items) == 0 {
			if ok, why := e.evaluable("networking.istio.io", "serviceentries", "ServiceEntry", true); !ok {
				na(c, why)
			} else {
				na(c, "no external ServiceEntries in ambient namespaces, and none report waypoint problems")
			}
		}
	})

	add(spec{"ambient.egress-default-deny", "Isolation", "high", "The ztunnel egress gateway starts from default-deny",
		"An egress gateway only controls outbound traffic if it denies by default. Without an empty ALLOW policy on it, every destination is reachable.",
		"apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: egress-default-deny, namespace: <ns>}\nspec:\n  targetRefs:\n  - {kind: Gateway, group: gateway.networking.k8s.io, name: <egress-gateway>}\n  action: ALLOW",
		meshBase + "ztunnel-native-egress/", solodocs.Istio + "traffic-management/egress/ztunnel-egress/"}, func(c *Check) {
		var egress []*store.Object
		for _, gw := range x.gws {
			if gatewayClass(gw) == "solo-ztunnel-egress" {
				egress = append(egress, gw)
			}
		}
		if len(egress) == 0 {
			na(c, "no solo-ztunnel-egress Gateways")
			return
		}
		if !apOK {
			na(c, apWhy)
			return
		}
		for _, gw := range sortedObjs(egress) {
			deny := ""
			for _, ap := range e.ws.List(e.cluster, "security.istio.io", "authorizationpolicies", gw.Namespace, nil) {
				if mxAction(ap) != "ALLOW" || len(l(path(ap.Obj, "spec", "rules"))) > 0 {
					continue
				}
				for _, t := range mxTargets(ap) {
					if t.kind == "Gateway" && t.name == gw.Name {
						deny = ap.Name
					}
				}
			}
			if deny != "" {
				c.Items = append(c.Items, e.item(gw, Pass, "default-deny "+deny))
			} else {
				c.Items = append(c.Items, e.item(gw, Fail, "no empty ALLOW policy targets this egress gateway: all destinations are open"))
			}
		}
	})

	add(spec{"ambient.waypoint-earns-keep", "Cost", "low", "Every waypoint is used",
		"An unused waypoint consumes resources. Check its Service bindings and L7 policies or routes before deciding whether to remove it.",
		"kubectl delete gateway -n <ns> <waypoint>\n# or use it\nkubectl label service <svc> istio.io/use-waypoint=<waypoint>", meshBase + "whose-waypoint-l7-multicluster/", solodocs.Istio + "waypoints/overview/"}, func(c *Check) {
		var wps []*store.Object
		for _, gw := range x.gws {
			if x.role(gw) == "waypoint" {
				wps = append(wps, gw)
			}
		}
		if len(wps) == 0 {
			na(c, "no waypoints")
			return
		}
		users := map[string][]*store.Object{}
		addUser := func(o *store.Object, ns string, lb map[string]string) {
			wp := lb["istio.io/use-waypoint"]
			if wp == "" || wp == "none" {
				return
			}
			if n := lb["istio.io/use-waypoint-namespace"]; n != "" {
				ns = n
			}
			users[ns+"/"+wp] = append(users[ns+"/"+wp], o)
		}
		for ns, lb := range x.nsl {
			addUser(nsObj(ns), ns, lb)
		}
		for _, sv := range x.svcs {
			addUser(sv, sv.Namespace, labels(sv))
		}
		for _, se := range x.ses {
			addUser(se, se.Namespace, labels(se))
		}
		for _, p := range e.pods() {
			addUser(p, p.Namespace, labels(p))
		}
		var routes []*store.Object
		routes = append(routes, e.list("gateway.networking.k8s.io", "httproutes")...)
		routes = append(routes, e.list("gateway.networking.k8s.io", "grpcroutes")...)
		routes = append(routes, e.list("gateway.networking.k8s.io", "tcproutes")...)
		vss := e.list("networking.istio.io", "virtualservices")
		for _, wp := range sortedObjs(wps) {
			k := wp.Namespace + "/" + wp.Name
			us := users[k]
			if len(us) == 0 {
				c.Items = append(c.Items, e.item(wp, Warn, "nothing uses this waypoint through istio.io/use-waypoint"))
				continue
			}
			var reasons []string
			if ps := append(append([]string{}, x.l7["Gateway|"+wp.Namespace+"|"+wp.Name]...), x.l7["GatewayClass||"+gatewayClass(wp)]...); len(ps) > 0 {
				reasons = append(reasons, "policy "+ps[0])
			}
			svcUses := map[string]bool{}
			for _, u := range us {
				switch u.Kind {
				case "Service":
					svcUses[u.Namespace+"/"+u.Name] = true
					if ps := x.l7["Service|"+u.Namespace+"|"+u.Name]; len(ps) > 0 {
						reasons = append(reasons, "policy "+ps[0])
					}
				case "Namespace":
					for _, sv := range x.svcs {
						if sv.Namespace == u.Name && labels(sv)["istio.io/use-waypoint"] == "" {
							svcUses[sv.Namespace+"/"+sv.Name] = true
						}
					}
				}
			}
			for _, r := range routes {
				for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
					pm := m(pr)
					ns := s(pm["namespace"])
					if ns == "" {
						ns = r.Namespace
					}
					if (s(pm["kind"]) == "Service" && svcUses[ns+"/"+s(pm["name"])]) || (s(pm["kind"]) != "Service" && ns+"/"+s(pm["name"]) == k) {
						reasons = append(reasons, r.Kind+" "+r.Name)
					}
				}
			}
			for _, vs := range vss {
				for _, h := range mxStrings(path(vs.Obj, "spec", "hosts")) {
					if sv := gwHostService(x, h, vs.Namespace); sv != nil && svcUses[sv.Namespace+"/"+sv.Name] {
						reasons = append(reasons, "VirtualService "+vs.Name)
					}
				}
			}
			if len(reasons) == 0 {
				c.Items = append(c.Items, e.item(wp, Warn, fmt.Sprintf("used by %d object(s), but no L7 policy, route or VirtualService needs it: ztunnel alone gives the same L4 security", len(us))))
			} else {
				c.Items = append(c.Items, e.item(wp, Pass, "used by "+reasons[0]))
			}
		}
	})

	add(spec{"ambient.vs-source-match", "Routing", "medium", "VirtualServices on waypoint hosts do not match on the source",
		"A waypoint serves the destination, so it cannot see which workload sent the request. sourceLabels and sourceNamespace matches are ignored and the route applies to everyone.",
		"# match on something the waypoint can see instead\nhttp:\n- match:\n  - headers:\n      x-caller: {exact: frontend}", meshBase + "whose-waypoint-l7-multicluster/", solodocs.Istio + "waypoints/configuration/"}, func(c *Check) {
		for _, vs := range sortedObjs(e.list("networking.istio.io", "virtualservices")) {
			bound := ""
			for _, h := range mxStrings(path(vs.Obj, "spec", "hosts")) {
				if sv := gwHostService(x, h, vs.Namespace); sv != nil && x.waypointGW(sv) != nil {
					bound = h
				}
			}
			if bound == "" {
				continue
			}
			bad := ""
			for _, hr := range l(path(vs.Obj, "spec", "http")) {
				for _, mt := range l(m(hr)["match"]) {
					mm := m(mt)
					if mm["sourceLabels"] != nil || s(mm["sourceNamespace"]) != "" {
						bad = "matches on sourceLabels or sourceNamespace"
					}
				}
			}
			if bad != "" {
				c.Items = append(c.Items, e.item(vs, Fail, bad+" for waypoint host "+bound+": ignored"))
			} else {
				c.Items = append(c.Items, e.item(vs, Pass, "no source matches"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no VirtualServices for hosts behind a waypoint")
		}
	})

	add(spec{"ambient.ztunnel-hardening", "Resilience", "medium", "ztunnel and istio-cni run as node-critical with requests",
		"If ztunnel or the CNI agent is evicted or not scheduled on a node, every ambient pod on that node loses its traffic. They need system-node-critical priority and resource requests.",
		"spec:\n  template:\n    spec:\n      priorityClassName: system-node-critical\n      containers:\n      - name: istio-proxy\n        resources:\n          requests: {cpu: 200m, memory: 512Mi}",
		meshBase + "istio-ambient-gke-autopilot/", solodocs.Istio + "setup/requirements/"}, func(c *Check) {
		if !detailOK {
			na(c, detailWhy)
			return
		}
		for _, ds := range sortedObjs(e.list("apps", "daemonsets")) {
			kind := istioSystemWorkload(ds)
			if kind != "ztunnel" && kind != "istio-cni" {
				continue
			}
			ps := podSpec(ds)
			var fails, warns []string
			if pc := s(ps["priorityClassName"]); pc != "system-node-critical" {
				if pc == "" {
					pc = "unset"
				}
				fails = append(fails, "priorityClassName is "+pc+", not system-node-critical")
			}
			for _, ct := range containers(ps) {
				if ct.kind == "container" && len(m(path(ct.c, "resources", "requests"))) == 0 {
					warns = append(warns, "container "+s(ct.c["name"])+" has no resource requests")
				}
			}
			ready, _ := mxNum(path(ds.Obj, "status", "numberReady"))
			want, ok := mxNum(path(ds.Obj, "status", "desiredNumberScheduled"))
			if ok && ready < want {
				fails = append(fails, fmt.Sprintf("%d of %d pods ready", int(ready), int(want)))
			}
			switch {
			case len(fails) > 0:
				c.Items = append(c.Items, e.item(ds, Fail, strings.Join(append(fails, warns...), "; ")))
			case len(warns) > 0:
				c.Items = append(c.Items, e.item(ds, Warn, strings.Join(warns, "; ")))
			default:
				if !ok {
					c.Items = append(c.Items, e.item(ds, Pass, "system-node-critical, requests set"))
				} else {
					c.Items = append(c.Items, e.item(ds, Pass, fmt.Sprintf("system-node-critical, requests set, %d/%d ready", int(ready), int(want))))
				}
			}
		}
		if len(c.Items) == 0 {
			if ok, why := e.evaluable("apps", "daemonsets", "DaemonSet", true); !ok {
				na(c, why)
			} else {
				na(c, "no ztunnel or istio-cni DaemonSets")
			}
		}
	})

	add(spec{"mesh.istiod-ha", "Resilience", "high", "istiod runs with at least two replicas",
		"Workloads keep their config while istiod is down, but new pods get no certificates or config. With one replica, and a PodDisruptionBudget that forbids evicting it, node drains hang.",
		"# ServiceMeshController\nspec:\n  scalingProfile: Default   # not Demo\n# or Helm\npilot:\n  autoscaleMin: 2\n  replicaCount: 2",
		meshBase + "gloo-operator-clusters/", solodocs.Istio + "setup/production-readiness/"}, func(c *Check) {
		if !detailOK {
			na(c, detailWhy)
			return
		}
		pdbOK, _ := e.evaluable("policy", "poddisruptionbudgets", "PodDisruptionBudget", true)
		for _, d := range sortedObjs(mxIstiods(e)) {
			n, desc := mxReplicas(e, d)
			blocking := ""
			if pdbOK {
				for _, p := range mxPDBs(e, d) {
					if mxPDBBlocks(p, n) {
						blocking = p.Name
					}
				}
			}
			switch {
			case n < 2 && blocking != "":
				c.Items = append(c.Items, e.item(d, Fail, desc+", and PodDisruptionBudget "+blocking+" allows no eviction: node drains block"))
			case n < 2:
				c.Items = append(c.Items, e.item(d, Warn, desc+": a single istiod"))
			default:
				c.Items = append(c.Items, e.item(d, Pass, desc))
			}
		}
		for _, smc := range e.list("operator.gloo.solo.io", "servicemeshcontrollers") {
			if s(path(smc.Obj, "spec", "scalingProfile")) == "Demo" {
				c.Items = append(c.Items, e.item(smc, Warn, "scalingProfile Demo: sized for demos, not production"))
			}
		}
		if len(c.Items) == 0 {
			if ok, why := e.evaluable("apps", "deployments", "Deployment", true); !ok {
				na(c, why)
			} else {
				na(c, "no istiod Deployment in the capture")
			}
		}
	})

	add(spec{"mesh.revision-resolves", "Config hygiene", "high", "istio.io/rev labels name a revision that exists",
		"A namespace labelled with a revision or tag that no istiod serves gets no injection or config from the control plane you think it has.",
		"istioctl tag list\nkubectl label namespace <ns> istio.io/rev=<existing revision or tag> --overwrite", meshBase + "istio-ambient-migration-kind/", solodocs.Istio + "operations/upgrade/gwapi-canary/"}, func(c *Check) {
		if !x.nsKnown {
			na(c, noNS)
			return
		}
		revs := map[string]bool{}
		for _, p := range e.pods() {
			if lb := labels(p); lb["app"] == "istiod" {
				r := lb["istio.io/rev"]
				if r == "" {
					r = "default"
				}
				revs[r] = true
			}
		}
		tags := map[string]bool{}
		mwcOK, mwcWhy := e.evaluable("admissionregistration.k8s.io", "mutatingwebhookconfigurations", "MutatingWebhookConfiguration", false)
		for _, w := range e.list("admissionregistration.k8s.io", "mutatingwebhookconfigurations") {
			if t := labels(w)["istio.io/tag"]; t != "" {
				tags[t] = true
			}
		}
		names := make([]string, 0, len(x.nsl))
		for ns := range x.nsl {
			names = append(names, ns)
		}
		sort.Strings(names)
		for _, ns := range names {
			rev := x.nsl[ns]["istio.io/rev"]
			if rev == "" {
				continue
			}
			switch {
			case revs[rev]:
				c.Items = append(c.Items, e.item(nsObj(ns), Pass, "istiod revision "+rev))
			case tags[rev]:
				c.Items = append(c.Items, e.item(nsObj(ns), Pass, "revision tag "+rev))
			case !mwcOK:
				c.Items = append(c.Items, e.item(nsObj(ns), NA, "no istiod pod has revision "+rev+", and it may be a tag, but webhooks were not captured ("+mwcWhy+")"))
			default:
				c.Items = append(c.Items, e.item(nsObj(ns), Fail, "no istiod revision or tag "+rev))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no namespace uses istio.io/rev")
		} else if !mwcOK {
			c.Reason = "no istiod pod matches and webhooks were not captured, so tags cannot be checked (" + mwcWhy + ")"
		}
	})

	add(spec{"mesh.solo-distribution", "Supply chain", "high", "Mesh components run Solo's Istio build",
		"Multicluster peering, service scope and the Solo features depend on the Solo distribution of istiod, ztunnel and the CNI, and on images that are patched and signed.",
		"# Helm\nglobal:\n  hub: us-docker.pkg.dev/soloio-img/istio\n  tag: 1.30.4-solo", meshBase + "istio-ambient-poc-eks/", solodocs.Istio + "about/images/"}, func(c *Check) {
		if !detailOK {
			na(c, detailWhy)
			return
		}
		mc, why := x.multicluster()
		for _, w := range sortedObjs(append(append([]*store.Object{}, e.list("apps", "deployments")...), e.list("apps", "daemonsets")...)) {
			kind := istioSystemWorkload(w)
			if kind != "istiod" && kind != "ztunnel" && kind != "istio-cni" {
				continue
			}
			var bad []string
			var img string
			for _, ct := range containers(podSpec(w)) {
				if ct.kind != "container" {
					continue
				}
				img = s(ct.c["image"])
				if !strings.Contains(mxImageTag(img), "-solo") {
					bad = append(bad, img)
				}
			}
			switch {
			case len(bad) == 0:
				c.Items = append(c.Items, e.item(w, Pass, img))
			case mc:
				c.Items = append(c.Items, e.item(w, Fail, strings.Join(bad, ", ")+" is not a -solo build, and this cluster is multicluster ("+why+")"))
			default:
				note := ""
				if v := podTemplateLabels(w)["app.kubernetes.io/version"]; strings.Contains(v, "-solo") {
					note = " (the chart is " + v + ", but the image comes from another registry)"
				}
				c.Items = append(c.Items, e.item(w, Warn, strings.Join(bad, ", ")+" is not a -solo build"+note))
			}
		}
		for _, smc := range e.list("operator.gloo.solo.io", "servicemeshcontrollers") {
			if v, _ := path(smc.Obj, "spec", "repository", "insecureSkipVerify").(bool); v {
				c.Items = append(c.Items, e.item(smc, Fail, "spec.repository.insecureSkipVerify is true: images are pulled without TLS verification"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no istiod, ztunnel or istio-cni workloads in the capture")
		}
	})

	add(spec{"mesh.plugged-in-ca", "Identity", "high", "The mesh uses a plugged-in CA",
		"Without cacerts istiod makes a self-signed root that lives only in this cluster. It cannot be rotated from a real PKI, and clusters that must trust each other will not.",
		"kubectl create secret generic cacerts -n istio-system \\\n  --from-file=ca-cert.pem --from-file=ca-key.pem \\\n  --from-file=root-cert.pem --from-file=cert-chain.pem",
		meshBase + "istio-ambient-poc-eks/", solodocs.Istio + "multicluster/install/default/manual/"}, func(c *Check) {
		if ok, why := e.absenceOK("", "secrets", "Secret", true); !ok {
			na(c, why)
			return
		}
		ns := x.root
		keys, exists, known := e.secretKeys(ns, "cacerts")
		if exists {
			o := e.ws.Get(e.cluster, "", "secrets", ns, "cacerts")
			if !known {
				c.Items = append(c.Items, e.item(o, NA, "cacerts exists but its keys were not captured"))
				c.Reason = "cacerts exists but its keys were not captured"
				return
			}
			var missing []string
			for _, k := range []string{"ca-cert.pem", "ca-key.pem", "root-cert.pem", "cert-chain.pem"} {
				if keys[k] == 0 {
					missing = append(missing, k)
				}
			}
			if len(missing) > 0 {
				c.Items = append(c.Items, e.item(o, Fail, "cacerts is missing or has empty "+strings.Join(missing, ", ")))
			} else {
				c.Items = append(c.Items, e.item(o, Pass, "plugged-in CA: ca-cert, ca-key, root-cert and cert-chain present"))
			}
			return
		}
		if o := e.ws.Get(e.cluster, "", "secrets", ns, "istio-ca-secret"); o != nil {
			if rem := mxGatewaysByRole(e, "remote"); len(rem) > 0 {
				c.Items = append(c.Items, e.item(o, Fail, "self-signed istiod root, yet this cluster peers through "+rem[0].Namespace+"/"+rem[0].Name+": peers cannot share a root"))
			} else {
				c.Items = append(c.Items, e.item(o, Warn, "self-signed istiod root (istio-ca-secret), no cacerts"))
			}
			return
		}
		na(c, "neither cacerts nor istio-ca-secret in "+ns+": the CA is probably external (istio-csr, Vault)")
	})

	add(spec{"mesh.shared-root", "Identity", "critical", "Peered clusters share a root and have their own intermediates",
		"Cross-cluster mTLS only works when every cluster chains to the same root. Each cluster should still have its own intermediate so one can be revoked without the others.",
		"# one root, one intermediate per cluster\nkubectl create secret generic cacerts -n istio-system --from-file=<cluster>/ca-cert.pem ...", meshBase + "hbone-east-west/", solodocs.Istio + "multicluster/install/default/manual/"}, func(c *Check) {
		clusters := e.ws.Clusters()
		if len(clusters) < 2 {
			na(c, "single-cluster capture")
			return
		}
		if e.cluster != clusters[0] {
			na(c, "evaluated once, on cluster "+clusters[0])
			return
		}
		type cc struct {
			roots, inters map[string]bool
			peered        bool
		}
		data := map[string]*cc{}
		for _, cl := range clusters {
			d := &cc{roots: map[string]bool{}, inters: map[string]bool{}}
			for _, dg := range e.ws.Diagnostics(cl) {
				r, i := mxCertIDs(dg)
				for k := range r {
					d.roots[k] = true
				}
				for k := range i {
					d.inters[k] = true
				}
			}
			if len(d.roots) == 0 {
				continue
			}
			d.peered = len(mxGatewaysByRole(env{ws: e.ws, cluster: cl}, "remote")) > 0
			data[cl] = d
		}
		if len(data) < 2 {
			na(c, "proxy certificates were captured for fewer than two clusters")
			return
		}
		var names []string
		for cl := range data {
			names = append(names, cl)
		}
		sort.Strings(names)
		for _, cl := range names {
			d := data[cl]
			st, notes := Pass, []string{"root " + mxShort(d.roots)}
			for _, other := range names {
				if other == cl {
					continue
				}
				o := data[other]
				common := false
				for k := range d.roots {
					if o.roots[k] {
						common = true
					}
				}
				if !common && (d.peered || o.peered) {
					st = Fail
					notes = append(notes, "no root in common with peer "+other)
				}
				for k := range d.inters {
					if o.inters[k] {
						if st == Pass {
							st = Warn
						}
						notes = append(notes, "same intermediate as "+other)
						break
					}
				}
			}
			c.Items = append(c.Items, Item{Cluster: cl, Resource: "namespaces", Kind: "Namespace", Name: x.root, Status: st, Detail: strings.Join(notes, "; ")})
		}
	})

	add(spec{"mesh.peer-gateway-config", "Multicluster", "high", "Peer gateways are fully described",
		"An istio-remote Gateway tells istiod how to reach and verify a peer. Without an address and service account the peer is unreachable or unverifiable, and without cluster, network and trust domain the endpoints are mislabelled.",
		"metadata:\n  annotations:\n    gateway.istio.io/service-account: istio-eastwest\n    gateway.istio.io/trust-domain: <peer trust domain>\n  labels:\n    topology.istio.io/cluster: <peer>\n    topology.istio.io/network: <peer network>\nspec:\n  gatewayClassName: istio-remote\n  addresses: [{type: IPAddress, value: <peer east-west address>}]",
		meshBase + "manual-vs-solo-mgmt-plane/", solodocs.Istio + "multicluster/best-practices/peering-recs/"}, func(c *Check) {
		remotes := mxGatewaysByRole(e, "remote")
		for _, gw := range sortedObjs(remotes) {
			ann := m(path(gw.Obj, "metadata", "annotations"))
			lb := labels(gw)
			var fails, warns []string
			if s(ann["gateway.istio.io/service-account"]) == "" {
				fails = append(fails, "no gateway.istio.io/service-account annotation")
			}
			if len(l(path(gw.Obj, "spec", "addresses"))) == 0 {
				fails = append(fails, "no spec.addresses")
			}
			if s(ann["gateway.istio.io/trust-domain"]) == "" {
				warns = append(warns, "no gateway.istio.io/trust-domain annotation")
			}
			for _, k := range []string{"topology.istio.io/cluster", "topology.istio.io/network"} {
				if lb[k] == "" {
					warns = append(warns, "no "+k+" label")
				}
			}
			switch {
			case len(fails) > 0:
				c.Items = append(c.Items, e.item(gw, Fail, strings.Join(append(fails, warns...), "; ")))
			case len(warns) > 0:
				c.Items = append(c.Items, e.item(gw, Warn, strings.Join(warns, "; ")))
			default:
				c.Items = append(c.Items, e.item(gw, Pass, "address, service account, trust domain, cluster and network set"))
			}
		}
		if len(remotes) > 0 || len(mxGatewaysByRole(e, "eastwest")) > 0 {
			for _, d := range mxIstiods(e) {
				if v, _ := mxEnv(d, "PEERING_AUTOMATIC_LOCAL_GATEWAY"); v == "true" {
					c.Items = append(c.Items, e.item(d, Warn, "PEERING_AUTOMATIC_LOCAL_GATEWAY=true: istiod creates peer gateways itself"))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no istio-remote Gateways")
		}
	})

	add(spec{"mesh.no-remote-secrets", "Multicluster", "high", "Peering does not also use remote secrets",
		"With east-west peering istiod does not need kubeconfigs for other clusters. A remote secret gives this cluster's istiod credentials to another API server, which is a wider blast radius than peering needs.",
		"kubectl delete secret -n istio-system -l istio/multiCluster=true\n# istiod\nenv:\n- {name: DISABLE_LEGACY_MULTICLUSTER, value: \"true\"}", meshBase + "manual-vs-solo-mgmt-plane/", solodocs.Istio + "multicluster/best-practices/peering-recs/"}, func(c *Check) {
		if len(mxGatewaysByRole(e, "eastwest", "remote")) == 0 {
			na(c, "no east-west or remote Gateways")
			return
		}
		if ok, why := e.evaluable("", "secrets", "Secret", true); ok {
			found := false
			for _, sec := range e.ws.List(e.cluster, "", "secrets", x.root, map[string]string{"istio/multiCluster": "true"}) {
				found = true
				c.Items = append(c.Items, e.item(sec, Fail, "remote secret alongside peering"))
			}
			if !found {
				c.Items = append(c.Items, e.item(nsObj(x.root), Pass, "no remote secrets"))
			}
		} else {
			c.Reason = why
		}
		for _, d := range mxIstiods(e) {
			if v, _ := mxEnv(d, "DISABLE_LEGACY_MULTICLUSTER"); v == "true" {
				c.Items = append(c.Items, e.item(d, Pass, "DISABLE_LEGACY_MULTICLUSTER=true"))
			} else {
				c.Items = append(c.Items, e.item(d, Warn, "DISABLE_LEGACY_MULTICLUSTER is not true: remote secrets would still be honoured"))
			}
		}
	})

	add(spec{"mesh.mesh-ports-not-public", "Network exposure", "high", "Control-plane and admin ports are not exposed",
		"istiod's plaintext xDS (15010), monitoring (15014) and debug (8080) ports, and the Envoy admin port (15000), give away config or control. None should be on a LoadBalancer or NodePort.",
		"# keep istiod ClusterIP, and expose only 15012/15017 through the east-west gateway\nspec:\n  type: ClusterIP\n# east-west gateway\nspec:\n  loadBalancerSourceRanges: [<peer CIDRs>]",
		meshBase + "hbone-east-west/", solodocs.Istio + "setup/production-readiness/"}, func(c *Check) {
		pods := e.pods()
		svcKeys := make([]string, 0, len(x.svcs))
		for k := range x.svcs {
			svcKeys = append(svcKeys, k)
		}
		sort.Strings(svcKeys)
		eastwest := map[*store.Object]bool{}
		for _, gw := range mxGatewaysByRole(e, "eastwest") {
			if sv := e.gatewayService(gw); sv != nil {
				eastwest[sv] = true
			}
		}
		for _, k := range svcKeys {
			sv := x.svcs[k]
			if !exposedService(sv) {
				continue
			}
			sel := mxLabelMap(path(sv.Obj, "spec", "selector"))
			if len(sel) == 0 {
				continue
			}
			istiod, proxy := false, false
			for _, p := range pods {
				if p.Namespace != sv.Namespace || !selects(m(path(sv.Obj, "spec", "selector")), labels(p)) {
					continue
				}
				switch istioSystemWorkload(p) {
				case "istiod":
					istiod = true
				case "gateway", "ztunnel":
					proxy = true
				}
			}
			if !istiod && !proxy {
				continue
			}
			var bad []string
			for _, pt := range l(path(sv.Obj, "spec", "ports")) {
				pm := m(pt)
				port, _ := mxNum(pm["port"])
				tgt, ok := mxNum(pm["targetPort"])
				if !ok {
					tgt = port
				}
				for _, n := range []float64{port, tgt} {
					if (istiod && (n == 15010 || n == 15014 || n == 8080)) || n == 15000 {
						bad = append(bad, fmt.Sprintf("%v", pm["port"]))
						break
					}
				}
			}
			st, detail := Pass, "no control-plane or admin ports"
			if len(bad) > 0 {
				st, detail = Fail, s(path(sv.Obj, "spec", "type"))+" exposes port(s) "+strings.Join(bad, ", ")
			}
			if eastwest[sv] && st == Pass && s(path(sv.Obj, "spec", "type")) == "LoadBalancer" && len(l(path(sv.Obj, "spec", "loadBalancerSourceRanges"))) == 0 && !gwInternalLB(sv) {
				st, detail = Warn, "east-west gateway is a public LoadBalancer with no loadBalancerSourceRanges"
			}
			c.Items = append(c.Items, e.item(sv, st, detail))
		}
		if len(c.Items) == 0 {
			na(c, "no exposed Services select istiod or mesh proxies")
		}
	})

	add(spec{"mesh.global-service-authz", "Authorization", "high", "Global services restrict who may call them",
		"solo.io/service-scope=global publishes a Service to every peered cluster. Without an ALLOW policy that names callers, any workload in any peer can reach it.",
		"apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: <svc>-callers, namespace: <ns>}\nspec:\n  targetRefs: [{kind: Service, group: \"\", name: <svc>}]\n  action: ALLOW\n  rules:\n  - from:\n    - source: {principals: [\"cluster.local/ns/<caller-ns>/sa/<caller>\"]}",
		meshBase + "istio-ambient-segments/", solodocs.Istio + "multicluster/multi-apps/policy-attachment/"}, func(c *Check) {
		peered := len(mxGatewaysByRole(e, "eastwest", "remote")) > 0
		keys := make([]string, 0, len(x.svcs))
		for k := range x.svcs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sv := x.svcs[k]
			if labels(sv)["solo.io/service-scope"] != "global" && x.nsl[sv.Namespace]["solo.io/service-scope"] != "global" {
				continue
			}
			if !apOK {
				c.Items = append(c.Items, e.item(sv, NA, apWhy))
				continue
			}
			allow := ""
			for _, ap := range x.aps {
				if mxAction(ap) == "ALLOW" && mxHasSourceConstraint(ap) && x.apSelects(ap, sv) {
					allow = ap.Namespace + "/" + ap.Name
				}
			}
			where := ""
			if x.nsKnown && !x.ambientNS[sv.Namespace] {
				where = ", in a namespace outside ambient,"
			}
			chart := s(m(path(sv.Obj, "metadata", "annotations"))["meta.helm.sh/release-name"])
			solo := chart != "" && (strings.HasPrefix(sv.Name, "solo-") || strings.HasPrefix(sv.Name, "gloo-"))
			switch {
			case allow != "":
				c.Items = append(c.Items, e.item(sv, Pass, "ALLOW "+allow+" constrains callers"))
			case !peered:
				c.Items = append(c.Items, e.item(sv, Warn, "global"+where+" with no ALLOW policy; no east-west or remote Gateway yet, so no peer can reach it, but it will be open once peering is added"))
			case solo:
				c.Items = append(c.Items, e.item(sv, Warn, "global"+where+" with no ALLOW policy; published by the Solo chart "+chart+", so restrict callers rather than removing the scope"))
			default:
				c.Items = append(c.Items, e.item(sv, Fail, "global"+where+" with no ALLOW policy that constrains callers: any peer can call it"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no global Services")
		}
	})

	add(spec{"mesh.jwks-fetch-restricted", "Authentication", "medium", "istiod cannot be pointed at internal JWKS URLs",
		"istiod fetches the jwksUri of every RequestAuthentication. Anyone who can create one can make istiod request internal addresses, such as cloud metadata, unless those ranges are blocked.",
		"# istiod\nenv:\n- name: BLOCKED_CIDRS_IN_JWKS_URIS\n  value: \"169.254.169.254/32,10.0.0.0/8\"", meshBase + "istio-ambient-crds/", solodocs.Istio + "reference/istiod-env-vars/"}, func(c *Check) {
		if len(x.ras) == 0 {
			na(c, "no RequestAuthentication")
			return
		}
		for _, d := range sortedObjs(mxIstiods(e)) {
			if v, ok := mxEnv(d, "BLOCKED_CIDRS_IN_JWKS_URIS"); ok && v != "" {
				c.Items = append(c.Items, e.item(d, Pass, "BLOCKED_CIDRS_IN_JWKS_URIS="+v))
			} else {
				c.Items = append(c.Items, e.item(d, Warn, fmt.Sprintf("%d RequestAuthentication(s) and no BLOCKED_CIDRS_IN_JWKS_URIS", len(x.ras))))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no istiod Deployment in the capture")
		}
	})

	add(spec{"mesh.serviceentry-scope", "Isolation", "medium", "ServiceEntries are scoped tightly",
		"A TCP ServiceEntry with no address captures every connection on that port. Wildcard or passthrough external entries open far more than the one API you meant.",
		"spec:\n  hosts: [api.example.com]\n  location: MESH_EXTERNAL\n  resolution: DNS\n  exportTo: [\".\"]\n  ports: [{number: 443, name: tls, protocol: TLS}]", meshBase + "ztunnel-native-egress/", solodocs.Istio + "traffic-management/serviceentry-visibility/"}, func(c *Check) {
		for _, se := range sortedObjs(append([]*store.Object{}, x.ses...)) {
			sp := m(se.Obj["spec"])
			var fails, warns, notes []string
			hasAddr := len(l(sp["addresses"])) > 0 || len(l(path(se.Obj, "status", "addresses"))) > 0
			for _, pt := range l(sp["ports"]) {
				pr := strings.ToUpper(s(m(pt)["protocol"]))
				if (pr == "" || pr == "TCP") && !hasAddr {
					fails = append(fails, fmt.Sprintf("TCP port %v with no address: matches all traffic on that port", m(pt)["number"]))
				}
			}
			external := s(sp["location"]) == "MESH_EXTERNAL"
			if external {
				for _, h := range mxStrings(sp["hosts"]) {
					if h == "*" || (strings.HasPrefix(h, "*.") && !strings.Contains(h[2:], ".")) {
						warns = append(warns, "host "+h+" is too broad")
					}
				}
				if s(sp["resolution"]) == "NONE" {
					warns = append(warns, "resolution NONE: any destination IP is passed through")
				}
			}
			if ex := mxStrings(sp["exportTo"]); len(ex) == 0 || (len(ex) == 1 && ex[0] == "*") {
				notes = append(notes, "visible to all namespaces (exportTo unset or *)")
			}
			switch {
			case len(fails) > 0:
				c.Items = append(c.Items, e.item(se, Fail, strings.Join(append(fails, warns...), "; ")))
			case len(warns) > 0:
				c.Items = append(c.Items, e.item(se, Warn, strings.Join(warns, "; ")))
			default:
				c.Items = append(c.Items, e.item(se, Pass, strings.Join(append([]string{"scoped"}, notes...), "; ")))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no ServiceEntries")
		}
	})

	add(spec{"mesh.control-plane-metrics-clean", "Health", "medium", "istiod reports no config conflicts",
		"istiod counts listener conflicts, clusters with no endpoints and endpoints with no pod. Non-zero values mean some config is being dropped or traffic has nowhere to go.",
		"istioctl analyze -A\nkubectl exec -n istio-system deploy/istiod -- curl -s localhost:15014/metrics | grep -E 'pilot_conflict|no_instances|endpoint_no_pod'",
		meshBase + "istio-ambient-crds/", solodocs.Istio + "troubleshooting/service-mesh/istio/"}, func(c *Check) {
		for _, d := range e.ws.Diagnostics(e.cluster) {
			if d.Component != "istiod" || len(d.Files["metrics.prom"]) == 0 {
				continue
			}
			mt := mxMetrics(d.Files["metrics.prom"])
			var bad []string
			for _, k := range []string{"pilot_conflict_inbound_listener", "pilot_conflict_outbound_listener_tcp_over_current_tcp", "pilot_eds_no_instances", "endpoint_no_pod"} {
				if mt[k] > 0 {
					bad = append(bad, fmt.Sprintf("%s %g", k, mt[k]))
				}
			}
			if len(bad) > 0 {
				c.Items = append(c.Items, e.item(mxPod(d.Namespace, d.Pod), Warn, strings.Join(bad, ", ")))
			} else {
				c.Items = append(c.Items, e.item(mxPod(d.Namespace, d.Pod), Pass, "no conflicts or empty clusters"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "istiod metrics were not captured")
		}
	})
	return out
}
