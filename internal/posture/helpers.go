package posture

import (
	"fmt"
	"strings"
	"sync"

	collectpolicy "github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

// Shared helpers for the checks. Keep them small and data-driven: every
// helper works from captured objects only.

// capturePolicy is the collection policy recorded in the bundle, or nil.
func (e env) capturePolicy() *collectpolicy.Policy { return e.ws.Policy() }

// fullDetail reports whether objects were captured with their spec and status.
// Under --object-detail metadata every spec-based check must be N/A.
func (e env) fullDetail() (bool, string) {
	if p := e.capturePolicy(); p != nil && p.ObjectDetail == collectpolicy.DetailMetadata {
		return false, "objects were captured as metadata only; re-capture with --object-detail full (the standard profile)"
	}
	return true, ""
}

// selectorScoped reports whether the capture used a label selector, in which
// case the absence of an object (no PDB, no NetworkPolicy) proves nothing.
func (e env) selectorScoped() (bool, string) {
	if p := e.capturePolicy(); p != nil && p.LabelSelector != "" {
		return true, "the capture was limited by the label selector " + p.LabelSelector + ", so a missing object may simply not have been collected"
	}
	return false, ""
}

// absenceOK combines evaluable, fullDetail and selectorScoped for checks that
// fail when something is missing.
func (e env) absenceOK(group, resource, kind string, namespaced bool) (bool, string) {
	if ok, why := e.evaluable(group, resource, kind, namespaced); !ok {
		return false, why
	}
	if sc, why := e.selectorScoped(); sc {
		return false, why
	}
	return true, ""
}

// secretKeys returns the key names of a captured Secret and whether the
// Secret exists and its keys are known. Values are never captured.
func (e env) secretKeys(ns, name string) (keys map[string]int, exists, keysKnown bool) {
	o := e.ws.Get(e.cluster, "", "secrets", ns, name)
	if o == nil {
		return nil, false, false
	}
	info := m(o.Obj["x-flightbay"])
	list, ok := info["keys"].([]any)
	if !ok {
		return nil, true, false
	}
	keys = map[string]int{}
	for _, k := range list {
		km := m(k)
		n := 0
		switch b := km["bytes"].(type) {
		case float64:
			n = int(b)
		case int:
			n = b
		case int64:
			n = int(b)
		}
		keys[s(km["name"])] = n
	}
	return keys, true, true
}

// secretType is the Secret's type, or "" when it was not captured.
func (e env) secretType(ns, name string) string {
	if o := e.ws.Get(e.cluster, "", "secrets", ns, name); o != nil {
		return s(o.Obj["type"])
	}
	return ""
}

// condition returns the status and reason of a status condition, looking in
// status.conditions and status.ancestors[].conditions / status.parents[].conditions.
func condition(o *store.Object, typ string) (status, reason, message string, found bool) {
	st := m(o.Obj["status"])
	lists := [][]any{l(st["conditions"])}
	for _, key := range []string{"ancestors", "parents"} {
		for _, a := range l(st[key]) {
			lists = append(lists, l(m(a)["conditions"]))
		}
	}
	for _, cs := range lists {
		for _, c := range cs {
			cm := m(c)
			if s(cm["type"]) == typ {
				if found && s(cm["status"]) != "False" {
					continue // keep the worst of several ancestors
				}
				status, reason, message, found = s(cm["status"]), s(cm["reason"]), s(cm["message"]), true
			}
		}
	}
	return
}

// podSpec returns the pod spec of a Pod or of a workload's template.
func podSpec(o *store.Object) map[string]any {
	if o.Kind == "Pod" {
		return m(o.Obj["spec"])
	}
	if o.Kind == "CronJob" {
		return m(path(o.Obj, "spec", "jobTemplate", "spec", "template", "spec"))
	}
	return m(path(o.Obj, "spec", "template", "spec"))
}

// podTemplateLabels returns the pod labels of a Pod or a workload template.
func podTemplateLabels(o *store.Object) map[string]string {
	var raw map[string]any
	if o.Kind == "Pod" {
		raw = m(path(o.Obj, "metadata", "labels"))
	} else {
		raw = m(path(o.Obj, "spec", "template", "metadata", "labels"))
	}
	out := map[string]string{}
	for k, v := range raw {
		out[k] = s(v)
	}
	return out
}

// containers returns every container of a pod spec with its kind
// (container, init, ephemeral).
func containers(ps map[string]any) []struct {
	c    map[string]any
	kind string
} {
	var out []struct {
		c    map[string]any
		kind string
	}
	for _, k := range []struct{ key, kind string }{{"initContainers", "init"}, {"containers", "container"}, {"ephemeralContainers", "ephemeral"}} {
		for _, c := range l(ps[k.key]) {
			out = append(out, struct {
				c    map[string]any
				kind string
			}{m(c), k.kind})
		}
	}
	return out
}

// workloads returns one object per controller: Deployments, StatefulSets,
// DaemonSets, CronJobs, Jobs without a CronJob, and bare Pods (no owner).
// Items report on the controller rather than on each replica.
func (e env) workloads() []*store.Object {
	var out []*store.Object
	for _, res := range []string{"deployments", "statefulsets", "daemonsets"} {
		out = append(out, e.list("apps", res)...)
	}
	out = append(out, e.list("batch", "cronjobs")...)
	for _, j := range e.list("batch", "jobs") {
		if k, _ := ownerOf(j); k != "CronJob" {
			out = append(out, j)
		}
	}
	for _, p := range e.list("", "pods") { // real pods only: templatePods is built from this list
		if k, _ := ownerOf(p); k == "" {
			out = append(out, p)
		}
	}
	return out
}

func ownerOf(o *store.Object) (kind, name string) {
	for _, r := range l(path(o.Obj, "metadata", "ownerReferences")) {
		rm := m(r)
		if c, _ := rm["controller"].(bool); c || kind == "" {
			kind, name = s(rm["kind"]), s(rm["name"])
		}
	}
	return
}

// podsOf returns the captured pods a workload selects (by its selector's
// matchLabels) in its namespace.
func (e env) podsOf(o *store.Object) []*store.Object {
	if o.Kind == "Pod" {
		return []*store.Object{o}
	}
	sel := map[string]string{}
	for k, v := range m(path(o.Obj, "spec", "selector", "matchLabels")) {
		sel[k] = s(v)
	}
	if len(sel) == 0 {
		return nil
	}
	return e.podsIn(o.Namespace, sel)
}

// selects reports whether a label selector (matchLabels only, which is what
// PDBs, NetworkPolicies and Services use in practice) matches labels.
func selects(selector map[string]any, lb map[string]string) bool {
	ml := m(selector["matchLabels"])
	if ml == nil {
		ml = selector // a Service selector is a bare map
	}
	if len(ml) == 0 && len(l(selector["matchExpressions"])) == 0 {
		return true
	}
	for k, v := range ml {
		if lb[k] != s(v) {
			return false
		}
	}
	return true
}

// gatewayClass returns spec.gatewayClassName.
func gatewayClass(gw *store.Object) string { return s(path(gw.Obj, "spec", "gatewayClassName")) }

// gatewayPods are the proxy pods a Gateway runs (both Istio and kgateway
// label them with gateway.networking.k8s.io/gateway-name).
func (e env) gatewayPods(gw *store.Object) []*store.Object {
	return e.podsIn(gw.Namespace, map[string]string{"gateway.networking.k8s.io/gateway-name": gw.Name})
}

// gatewayService is the Service fronting a Gateway's proxies, or nil.
func (e env) gatewayService(gw *store.Object) *store.Object {
	for _, sv := range e.ws.List(e.cluster, "", "services", gw.Namespace, map[string]string{"gateway.networking.k8s.io/gateway-name": gw.Name}) {
		return sv
	}
	return nil
}

// exposedService reports whether a Service is reachable from outside the
// cluster (LoadBalancer or NodePort, or externalIPs).
func exposedService(sv *store.Object) bool {
	if sv == nil {
		return false
	}
	t := s(path(sv.Obj, "spec", "type"))
	return t == "LoadBalancer" || t == "NodePort" || len(l(path(sv.Obj, "spec", "externalIPs"))) > 0
}

// istioSystemWorkload names Istio node agents and proxies whose privileges
// are expected (see the exception tables in the Kubernetes checks).
func istioSystemWorkload(o *store.Object) string {
	lb := podTemplateLabels(o)
	switch {
	case o.Name == "ztunnel" || lb["app"] == "ztunnel" || strings.HasPrefix(o.Name, "ztunnel-"):
		return "ztunnel"
	case o.Name == "istio-cni-node" || lb["k8s-app"] == "istio-cni-node" || strings.HasPrefix(o.Name, "istio-cni-node"):
		return "istio-cni"
	case lb["app"] == "istiod" || lb["istio"] == "pilot":
		return "istiod"
	case lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["gateway.istio.io/managed"] != "" || lb["istio"] == "ingressgateway" || lb["istio"] == "eastwestgateway":
		return "gateway"
	}
	return ""
}

// platformNS are provider and cluster-infrastructure namespaces whose
// workloads are privileged by design and are not the customer's to change.
func platformNS(ns string) bool {
	switch ns {
	case "kube-system", "kube-public", "kube-node-lease", "local-path-storage", "gmp-system", "calico-system", "tigera-operator", "kube-flannel", "metallb-system":
		return true
	}
	return strings.HasPrefix(ns, "gke-") || strings.HasPrefix(ns, "aks-") || strings.HasPrefix(ns, "openshift-")
}

func objName(o *store.Object) string {
	if o.Namespace == "" {
		return o.Kind + " " + o.Name
	}
	return fmt.Sprintf("%s %s/%s", o.Kind, o.Namespace, o.Name)
}

// ---------- declared state (flightbay scan) ----------

// DeclaredWhy is the reason a check is not applicable on a repo scan: the
// bundle holds what the repository declares, with no status and no pods.
const DeclaredWhy = "declared state only (repo scan)"

// declaredOnly reports whether the workspace is a repo scan rather than a
// capture of a running cluster.
func (e env) declaredOnly() bool { return e.ws.Header().Declared() }

// runtimeNA marks c not applicable on a repo scan and says what it needed.
// It returns true when it did, so a check can return straight away.
func (e env) runtimeNA(c *Check, needs string) bool {
	if !e.declaredOnly() {
		return false
	}
	c.Status, c.Reason, c.Items = NA, DeclaredWhy+": "+needs+" exists only in a running cluster", nil
	return true
}

// templateKey marks a pod made from a workload's template on a repo scan.
const templateKey = "x-flightbay-template"

var tplCache struct {
	sync.Mutex
	ws      *store.Workspace
	cluster string
	rev     int64
	pods    []*store.Object
}

// templatePods stands in for pods on a repo scan: one per workload, with the
// template's labels, annotations and spec, owned by the workload. Items
// about them are reported on the workload (see item).
func (e env) templatePods() []*store.Object {
	rev := e.ws.Revision()
	tplCache.Lock()
	defer tplCache.Unlock()
	if tplCache.ws == e.ws && tplCache.cluster == e.cluster && tplCache.rev == rev {
		return tplCache.pods
	}
	var out []*store.Object
	for _, w := range e.workloads() {
		if w.Kind == "Pod" {
			continue
		}
		tpl := m(path(w.Obj, "spec", "template"))
		if w.Kind == "CronJob" {
			tpl = m(path(w.Obj, "spec", "jobTemplate", "spec", "template"))
		}
		if tpl == nil {
			continue
		}
		md := map[string]any{"name": w.Name, "namespace": w.Namespace}
		tm := m(tpl["metadata"])
		if lb := m(tm["labels"]); lb != nil {
			md["labels"] = lb
		}
		ann := map[string]any{}
		for k, v := range m(tm["annotations"]) {
			ann[k] = v
		}
		for k, v := range m(path(w.Obj, "metadata", "annotations")) {
			if strings.HasPrefix(k, "flightbay.dev/") {
				ann[k] = v
			}
		}
		md["annotations"] = ann
		md["ownerReferences"] = []any{map[string]any{"apiVersion": s(w.Obj["apiVersion"]), "kind": w.Kind, "name": w.Name, "controller": true}}
		out = append(out, &store.Object{Cluster: w.Cluster, Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true, Namespace: w.Namespace, Name: w.Name,
			Obj: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": md, "spec": m(tpl["spec"]),
				templateKey: map[string]any{"group": w.Group, "resource": w.Resource, "kind": w.Kind}}})
	}
	tplCache.ws, tplCache.cluster, tplCache.rev, tplCache.pods = e.ws, e.cluster, rev, out
	return out
}

// pods returns the captured pods, or on a repo scan the declared bare Pods
// plus one stand-in per workload template.
func (e env) pods() []*store.Object { return e.podsIn("", nil) }

// podsIn is pods limited to a namespace ("" for all) and a label selector.
func (e env) podsIn(ns string, sel map[string]string) []*store.Object {
	real := e.ws.List(e.cluster, "", "pods", ns, sel)
	if !e.declaredOnly() {
		return real
	}
	out := append([]*store.Object{}, real...)
	for _, p := range e.templatePods() {
		if ns != "" && p.Namespace != ns {
			continue
		}
		lb := p.Labels()
		ok := true
		for k, v := range sel {
			if lb[k] != v {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// isTemplatePod reports whether o is a stand-in made by templatePods.
func isTemplatePod(o *store.Object) bool { return o != nil && o.Obj[templateKey] != nil }

func runtimeOnlyType(group, resource string) bool {
	switch group + "/" + resource {
	case "/pods", "/nodes", "/events", "/endpoints", "discovery.k8s.io/endpointslices", "apps/replicasets", "apps/controllerrevisions",
		"coordination.k8s.io/leases", "events.k8s.io/events", "certificates.k8s.io/certificatesigningrequests":
		return true
	}
	return false
}

// noNSWhy is why namespace labels are unknown.
func (e env) noNSWhy() string {
	if e.declaredOnly() {
		return "the scanned paths declare no Namespace objects, so namespace labels are unknown (scan the directory that declares them)"
	}
	return "namespace labels were not captured (re-capture with this version of flightbay, which records the Namespace objects in scope)"
}
