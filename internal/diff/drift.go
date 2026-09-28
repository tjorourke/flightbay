package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/tjorourke/flightbay/internal/store"
)

// Labels for the two sides when a repo scan is compared with a capture.
const (
	DeclaredLabel = "Declared (repo)"
	LiveLabel     = "Live (cluster)"
)

// DefaultedFields are filled in by the API server or a controller when the
// manifest leaves them out. A path is ignored when the declared object does
// not set it, and compared as usual when it does. "[]" matches every list
// element. Extend this list when a new default shows up as noise.
var DefaultedFields = []string{
	// every object
	"metadata.namespace", // cluster-scoped objects carry none; namespaced ones are filled by the scan
	"metadata.finalizers",
	"metadata.labels.kubernetes.io/metadata.name",
	"metadata.annotations.deployment.kubernetes.io/revision",
	"metadata.annotations.meta.helm.sh/release-name",
	"metadata.annotations.meta.helm.sh/release-namespace",
	"metadata.labels.app.kubernetes.io/managed-by",
	// Deployment, StatefulSet, DaemonSet, ReplicaSet
	"spec.revisionHistoryLimit",
	"spec.progressDeadlineSeconds",
	"spec.strategy",
	"spec.updateStrategy",
	"spec.podManagementPolicy",
	"spec.persistentVolumeClaimRetentionPolicy",
	"spec.minReadySeconds",
	"spec.replicas",
	// Job, CronJob
	"spec.backoffLimit", "spec.completionMode", "spec.completions", "spec.parallelism", "spec.suspend", "spec.podReplacementPolicy",
	"spec.manualSelector", "spec.selector", "spec.template.metadata.labels.batch.kubernetes.io/controller-uid",
	"spec.template.metadata.labels.batch.kubernetes.io/job-name", "spec.template.metadata.labels.controller-uid", "spec.template.metadata.labels.job-name",
	"spec.concurrencyPolicy", "spec.failedJobsHistoryLimit", "spec.successfulJobsHistoryLimit",
	// pod templates (Deployments and friends, and CronJobs one level deeper)
	"spec.template.metadata.creationTimestamp",
	"spec.template.spec.dnsPolicy",
	"spec.template.spec.schedulerName",
	"spec.template.spec.restartPolicy",
	"spec.template.spec.securityContext",
	"spec.template.spec.terminationGracePeriodSeconds",
	"spec.template.spec.serviceAccount",
	"spec.template.spec.enableServiceLinks",
	"spec.template.spec.containers[].terminationMessagePath",
	"spec.template.spec.containers[].terminationMessagePolicy",
	"spec.template.spec.containers[].imagePullPolicy",
	"spec.template.spec.containers[].resources",
	"spec.template.spec.containers[].ports[].protocol",
	"spec.template.spec.containers[].livenessProbe.failureThreshold", "spec.template.spec.containers[].livenessProbe.periodSeconds",
	"spec.template.spec.containers[].livenessProbe.successThreshold", "spec.template.spec.containers[].livenessProbe.timeoutSeconds",
	"spec.template.spec.containers[].readinessProbe.failureThreshold", "spec.template.spec.containers[].readinessProbe.periodSeconds",
	"spec.template.spec.containers[].readinessProbe.successThreshold", "spec.template.spec.containers[].readinessProbe.timeoutSeconds",
	"spec.template.spec.containers[].livenessProbe.httpGet.scheme", "spec.template.spec.containers[].readinessProbe.httpGet.scheme",
	"spec.template.spec.initContainers[].terminationMessagePath",
	"spec.template.spec.initContainers[].terminationMessagePolicy",
	"spec.template.spec.initContainers[].imagePullPolicy",
	"spec.template.spec.initContainers[].resources",
	"spec.template.spec.volumes[].configMap.defaultMode",
	"spec.template.spec.volumes[].secret.defaultMode",
	"spec.template.spec.volumes[].projected.defaultMode",
	"spec.jobTemplate.spec.template.spec.dnsPolicy",
	"spec.jobTemplate.spec.template.spec.schedulerName",
	"spec.jobTemplate.spec.template.spec.securityContext",
	"spec.jobTemplate.spec.template.spec.terminationGracePeriodSeconds",
	"spec.jobTemplate.spec.template.spec.containers[].terminationMessagePath",
	"spec.jobTemplate.spec.template.spec.containers[].terminationMessagePolicy",
	"spec.jobTemplate.spec.template.spec.containers[].imagePullPolicy",
	"spec.jobTemplate.spec.template.spec.containers[].resources",
	"spec.jobTemplate.metadata.creationTimestamp",
	"spec.jobTemplate.spec.template.metadata.creationTimestamp",
	// Pod (declared bare pods)
	"spec.dnsPolicy", "spec.schedulerName", "spec.restartPolicy", "spec.securityContext", "spec.terminationGracePeriodSeconds",
	"spec.enableServiceLinks", "spec.preemptionPolicy", "spec.priority", "spec.tolerations", "spec.nodeName", "spec.serviceAccount",
	"spec.containers[].terminationMessagePath", "spec.containers[].terminationMessagePolicy", "spec.containers[].imagePullPolicy",
	"spec.containers[].resources", "spec.containers[].volumeMounts",
	"spec.volumes",
	// Service
	"spec.clusterIP", "spec.clusterIPs", "spec.ipFamilies", "spec.ipFamilyPolicy", "spec.internalTrafficPolicy",
	"spec.sessionAffinity", "spec.type", "spec.ports[].protocol", "spec.ports[].targetPort", "spec.externalTrafficPolicy",
	"spec.allocateLoadBalancerNodePorts", "spec.ports[].nodePort",
	// Namespace, ServiceAccount, PVC, webhooks, CRDs
	"spec.finalizers",
	"secrets",
	"spec.volumeMode", "spec.volumeName", "spec.storageClassName",
	"webhooks[].timeoutSeconds", "webhooks[].matchPolicy", "webhooks[].namespaceSelector", "webhooks[].objectSelector",
	"webhooks[].reinvocationPolicy", "webhooks[].clientConfig.service.port",
	"spec.conversion",
	// Gateway API and others
	"spec.infrastructure",
	"spec.rules[].matches",
	"spec.rules[].backendRefs[].weight",
	"spec.rules[].backendRefs[].group",
	"spec.rules[].backendRefs[].kind",
	"spec.parentRefs[].group",
	"spec.parentRefs[].kind",
	"spec.listeners[].allowedRoutes",
	// HPA
	"spec.behavior",
}

// alwaysIgnored differ on every live object and are never drift.
var alwaysIgnored = map[string]bool{
	"status": true, "metadata.uid": true, "metadata.resourceVersion": true, "metadata.creationTimestamp": true,
	"metadata.generation": true, "metadata.managedFields": true, "metadata.selfLink": true, "metadata.ownerReferences": true,
	"metadata.annotations.kubectl.kubernetes.io/last-applied-configuration": true,
}

// FieldDiff is one differing field.
type FieldDiff struct {
	Path     string `json:"path"`
	Declared string `json:"declared"`
	Live     string `json:"live"`
}

// DriftObject is one object in a drift report.
type DriftObject struct {
	Group     string      `json:"group"`
	Kind      string      `json:"kind"`
	Namespace string      `json:"namespace,omitempty"`
	Name      string      `json:"name"`
	Source    string      `json:"source,omitempty"` // the file that declares it
	Fields    []FieldDiff `json:"fields,omitempty"`
}

// ID is Kind ns/name.
func (o DriftObject) ID() string {
	if o.Namespace == "" {
		return o.Kind + " " + o.Name
	}
	return o.Kind + " " + o.Namespace + "/" + o.Name
}

// DriftReport compares declared state (a repo scan) with live state (a
// capture of the cluster).
type DriftReport struct {
	Declared      Side           `json:"declared"`
	Live          Side           `json:"live"`
	Labels        [2]string      `json:"labels"`
	InGitOnly     []DriftObject  `json:"inGitNotInCluster"`
	InClusterOnly []DriftObject  `json:"inClusterNotInGit"`
	Differs       []DriftObject  `json:"differs"`
	Same          int            `json:"same"`
	Scope         DriftScope     `json:"scope"`
	NotComparable []string       `json:"notComparable"`
	Summary       map[string]int `json:"summary"`
}

// DriftScope is where "in the cluster, not in Git" was looked for: the
// namespaces and kinds the repository declares.
type DriftScope struct {
	Namespaces []string `json:"namespaces"`
	Kinds      []string `json:"kinds"`
}

func compareDrift(before, after *store.Workspace) Report {
	declared, live := before, after
	if IsDeclared(after) {
		declared, live = after, before
	}
	d := Drift(declared, live)
	r := Report{Before: d.Declared, After: d.Live, Summary: map[string]int{}, Drift: &d, Versions: []VersionChange{}, NotComparable: d.NotComparable}
	r.Before.Label, r.After.Label = DeclaredLabel, LiveLabel
	d.Declared.Label, d.Live.Label = DeclaredLabel, LiveLabel
	add := func(o DriftObject, change string) {
		c := Change{Cluster: r.After.firstCluster(), Group: o.Group, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Change: change}
		for _, f := range o.Fields {
			c.Fields = append(c.Fields, f.Path)
		}
		r.Changes = append(r.Changes, c)
	}
	for _, o := range d.InGitOnly {
		add(o, "removed")
	}
	for _, o := range d.InClusterOnly {
		add(o, "added")
	}
	for _, o := range d.Differs {
		add(o, "changed")
	}
	for _, c := range r.Changes {
		r.Summary[c.Change]++
	}
	r.Summary["inGitNotInCluster"], r.Summary["inClusterNotInGit"], r.Summary["differs"] = len(d.InGitOnly), len(d.InClusterOnly), len(d.Differs)
	return r
}

func (s Side) firstCluster() string {
	if len(s.Clusters) > 0 {
		return s.Clusters[0]
	}
	return ""
}

// IsDeclared reports whether ws is a repo scan.
func IsDeclared(ws *store.Workspace) bool { return ws != nil && ws.Header().Declared() }

type dkey struct{ group, kind, ns, name string }

func source(o *store.Object) string {
	md, _ := o.Obj["metadata"].(map[string]any)
	ann, _ := md["annotations"].(map[string]any)
	s, _ := ann["flightbay.dev/source"].(string)
	if l, _ := ann["flightbay.dev/source-line"].(string); l != "" && s != "" {
		s += ":" + l
	}
	return s
}

// generated reports live objects a controller or the API server made, which
// a repository never declares.
func generated(o *store.Object) bool {
	md, _ := o.Obj["metadata"].(map[string]any)
	if refs, _ := md["ownerReferences"].([]any); len(refs) > 0 {
		return true
	}
	switch {
	case o.Kind == "ServiceAccount" && o.Name == "default":
		return true
	case o.Kind == "ConfigMap" && (o.Name == "kube-root-ca.crt" || o.Name == "istio-ca-root-cert"):
		return true
	case o.Kind == "Secret":
		t, _ := o.Obj["type"].(string)
		if t == "kubernetes.io/service-account-token" || t == "helm.sh/release.v1" {
			return true
		}
	}
	return false
}

// Drift compares a repo scan with a capture of the cluster, ignoring cluster
// names: what Git declares that the cluster lacks, what the cluster runs in
// the repository's namespaces and kinds that Git does not declare (applied by
// hand), and fields that differ once status, metadata noise and defaulted
// fields are removed.
func Drift(declared, live *store.Workspace) DriftReport {
	r := DriftReport{Declared: labelled(side(declared), DeclaredLabel), Live: labelled(side(live), LiveLabel), Labels: [2]string{DeclaredLabel, LiveLabel}, Summary: map[string]int{},
		InGitOnly: []DriftObject{}, InClusterOnly: []DriftObject{}, Differs: []DriftObject{}, NotComparable: []string{}}
	want := map[dkey]*store.Object{}
	nss := map[string]bool{}
	kinds := map[string]bool{}
	for _, c := range declared.Clusters() {
		for _, o := range declared.AllObjects(c) {
			if synthesised(o) {
				continue
			}
			want[dkey{o.Group, o.Kind, o.Namespace, o.Name}] = o
			kinds[o.Group+"/"+o.Kind] = true
			if o.Namespace != "" {
				nss[o.Namespace] = true
			}
			if o.Kind == "Namespace" && o.Group == "" {
				nss[o.Name] = true
			}
		}
	}
	have := map[dkey]*store.Object{}
	liveCluster := ""
	for _, c := range live.Clusters() {
		if liveCluster == "" {
			liveCluster = c
		}
		for _, o := range live.AllObjects(c) {
			if synthesised(o) {
				continue
			}
			k := dkey{o.Group, o.Kind, o.Namespace, o.Name}
			if _, dup := have[k]; !dup {
				have[k] = o
			}
		}
	}
	notComparable := map[string]int{}
	for k, d := range want {
		l, ok := have[k]
		if !ok {
			if !covered(live, liveCluster, d.Group, d.Resource, d.Kind, d.Namespace, d.Namespaced) {
				notComparable[fmt.Sprintf("%s in %s: not captured completely in the cluster bundle", d.Kind, orCluster(d.Namespace))]++
				continue
			}
			r.InGitOnly = append(r.InGitOnly, DriftObject{Group: k.group, Kind: k.kind, Namespace: k.ns, Name: k.name, Source: source(d)})
			continue
		}
		fields := compareDeclared(d.Obj, l.Obj)
		if len(fields) == 0 {
			r.Same++
			continue
		}
		r.Differs = append(r.Differs, DriftObject{Group: k.group, Kind: k.kind, Namespace: k.ns, Name: k.name, Source: source(d), Fields: fields})
	}
	for k, l := range have {
		if _, ok := want[k]; ok || !kinds[k.group+"/"+k.kind] || generated(l) {
			continue
		}
		if k.ns != "" && !nss[k.ns] {
			continue
		}
		if k.ns == "" && l.Kind != "Namespace" {
			// Cluster-scoped kinds the repo declares (ClusterRoles, CRDs,
			// webhooks): every add-on in the cluster has some, so only
			// report them when their name matches nothing declared at all.
			continue
		}
		if l.Kind == "Namespace" && !nss[l.Name] {
			continue
		}
		r.InClusterOnly = append(r.InClusterOnly, DriftObject{Group: k.group, Kind: k.kind, Namespace: k.ns, Name: k.name})
	}
	for _, list := range [][]DriftObject{r.InGitOnly, r.InClusterOnly, r.Differs} {
		sort.Slice(list, func(i, j int) bool { return list[i].ID() < list[j].ID() })
	}
	for msg, n := range notComparable {
		r.NotComparable = append(r.NotComparable, fmt.Sprintf("%s (%d objects)", msg, n))
	}
	sort.Strings(r.NotComparable)
	r.Scope.Namespaces = keys(nss)
	r.Scope.Kinds = keys(kinds)
	r.Summary["inGitNotInCluster"], r.Summary["inClusterNotInGit"], r.Summary["differs"], r.Summary["same"] = len(r.InGitOnly), len(r.InClusterOnly), len(r.Differs), r.Same
	return r
}

func labelled(s Side, l string) Side { s.Label = l; return s }

func orCluster(ns string) string {
	if ns == "" {
		return "the cluster scope"
	}
	return ns
}

func synthesised(o *store.Object) bool {
	md, _ := o.Obj["metadata"].(map[string]any)
	ann, _ := md["annotations"].(map[string]any)
	v, _ := ann[store.SynthesisedAnnotation].(string)
	return v == "true"
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalise drops what never counts as drift: status, metadata noise, the
// scan's own flightbay.dev annotations and redaction bookkeeping.
func normalise(obj map[string]any) map[string]any {
	b, _ := json.Marshal(obj)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	delete(out, "status")
	if md, ok := out["metadata"].(map[string]any); ok {
		for _, k := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields", "selfLink", "ownerReferences"} {
			delete(md, k)
		}
		if ann, ok := md["annotations"].(map[string]any); ok {
			for k := range ann {
				if strings.HasPrefix(k, "flightbay.dev/") || strings.HasPrefix(k, "kubectl.kubernetes.io/last-applied") {
					delete(ann, k)
				}
			}
			if len(ann) == 0 {
				delete(md, "annotations")
			}
		}
		if lb, ok := md["labels"].(map[string]any); ok && len(lb) == 0 {
			delete(md, "labels")
		}
	}
	if x, ok := out["x-flightbay"].(map[string]any); ok {
		// Secrets: only key names and sizes are ever held. Compare those.
		out["x-flightbay"] = map[string]any{"keys": x["keys"]}
	}
	return out
}

// compareDeclared lists the fields that differ between a declared and a live
// object. Defaulted fields that the declared object does not set are ignored.
func compareDeclared(declared, live map[string]any) []FieldDiff {
	d, l := normalise(declared), normalise(live)
	var out []FieldDiff
	var walk func(p, pattern string, x, y any)
	walk = func(p, pattern string, x, y any) {
		if alwaysIgnored[pattern] {
			return
		}
		if x == nil && y != nil && isDefaulted(pattern) {
			return
		}
		if reflect.DeepEqual(x, y) {
			return
		}
		if strings.Contains(pattern, "resources.") && sameQuantity(x, y) {
			return
		}
		if pattern == "x-flightbay.keys" && (x == nil || y == nil) {
			return // one side was captured without key names
		}
		xm, xok := x.(map[string]any)
		ym, yok := y.(map[string]any)
		// A map set on one side only is walked key by key, so defaulted keys
		// inside it (annotations the server adds) are still ignored.
		if x == nil && yok {
			xm, xok = map[string]any{}, true
		}
		if y == nil && xok {
			ym, yok = map[string]any{}, true
		}
		if xok && yok {
			ks := map[string]bool{}
			for k := range xm {
				ks[k] = true
			}
			for k := range ym {
				ks[k] = true
			}
			for _, k := range keys(ks) {
				walk(join(p, k), join(pattern, k), xm[k], ym[k])
			}
			return
		}
		xl, xlok := x.([]any)
		yl, ylok := y.([]any)
		if xlok && ylok && len(xl) == len(yl) {
			for i := range xl {
				walk(p+"["+strconv.Itoa(i)+"]", pattern+"[]", xl[i], yl[i])
			}
			return
		}
		out = append(out, FieldDiff{Path: p, Declared: short(x), Live: short(y)})
	}
	walk("", "", d, l)
	return out
}

// sameQuantity reports whether two resource quantities are equal once
// parsed ("0.5" and "500m", "1Gi" and "1024Mi").
func sameQuantity(x, y any) bool {
	qx, err1 := resource.ParseQuantity(fmt.Sprint(x))
	qy, err2 := resource.ParseQuantity(fmt.Sprint(y))
	return err1 == nil && err2 == nil && qx.Cmp(qy) == 0
}

func join(p, k string) string {
	if p == "" {
		return k
	}
	return p + "." + k
}

var defaulted = func() map[string]bool {
	m := map[string]bool{}
	for _, f := range DefaultedFields {
		m[f] = true
	}
	return m
}()

func isDefaulted(pattern string) bool { return defaulted[pattern] }

func short(v any) string {
	if v == nil {
		return "(unset)"
	}
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// Text renders the drift report for the terminal.
func (r DriftReport) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", DeclaredLabel, strings.Join(r.Declared.Clusters, ", "))
	if r.Declared.Repo != "" {
		fmt.Fprintf(&b, " (%s)", r.Declared.Repo)
	}
	fmt.Fprintf(&b, "\n%s:  %s captured %s\n", LiveLabel, strings.Join(r.Live.Clusters, ", "), r.Live.Captured.Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "\n%d in Git, not in the cluster; %d in the cluster, not in Git; %d differ; %d match\n", len(r.InGitOnly), len(r.InClusterOnly), len(r.Differs), r.Same)
	if len(r.InGitOnly) > 0 {
		b.WriteString("\nIn Git, not in the cluster:\n")
		for _, o := range r.InGitOnly {
			fmt.Fprintf(&b, "  %s", o.ID())
			if o.Source != "" {
				fmt.Fprintf(&b, "  (%s)", o.Source)
			}
			b.WriteString("\n")
		}
	}
	if len(r.InClusterOnly) > 0 {
		b.WriteString("\nIn the cluster, not in Git:\n")
		for _, o := range r.InClusterOnly {
			fmt.Fprintf(&b, "  %s\n", o.ID())
		}
	}
	if len(r.Differs) > 0 {
		b.WriteString("\nDiffers:\n")
		for _, o := range r.Differs {
			fmt.Fprintf(&b, "  %s", o.ID())
			if o.Source != "" {
				fmt.Fprintf(&b, "  (%s)", o.Source)
			}
			b.WriteString("\n")
			for _, f := range o.Fields {
				fmt.Fprintf(&b, "      %s: %s -> %s\n", f.Path, f.Declared, f.Live)
			}
		}
	}
	fmt.Fprintf(&b, "\nScope of \"in the cluster, not in Git\": namespaces %s; kinds %s\n", strings.Join(r.Scope.Namespaces, ", "), strings.Join(r.Scope.Kinds, ", "))
	if len(r.NotComparable) > 0 {
		b.WriteString("\nNot comparable (absence proves nothing here):\n")
		for _, n := range r.NotComparable {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	return b.String()
}

// JSON renders the drift report.
func (r DriftReport) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return b
}
