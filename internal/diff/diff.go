// Package diff compares objects across clusters or across two bundles from the
// same customer (before and after), with metadata noise removed and absence
// judged against coverage rather than assumed.
package diff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/analyze"
	"github.com/tjorourke/flightbay/internal/store"
)

// Clean returns a copy of obj without fields that differ between captures of
// an unchanged object: uid, resourceVersion, timestamps, managedFields,
// last-applied annotations and condition transition times.
func Clean(obj map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range obj {
		out[k] = v
	}
	if md, ok := obj["metadata"].(map[string]any); ok {
		m2 := map[string]any{}
		for k, v := range md {
			switch k {
			case "uid", "resourceVersion", "creationTimestamp", "generation", "managedFields", "selfLink":
				continue
			}
			m2[k] = v
		}
		if ann, ok := m2["annotations"].(map[string]any); ok {
			a2 := map[string]any{}
			for k, v := range ann {
				if !strings.HasPrefix(k, "kubectl.kubernetes.io/last-applied") && !strings.HasPrefix(k, "deployment.kubernetes.io/revision") &&
					k != "control-plane.alpha.kubernetes.io/leader" { // leader election heartbeats
					a2[k] = v
				}
			}
			m2["annotations"] = a2
		}
		out["metadata"] = m2
	}
	if st, ok := obj["status"].(map[string]any); ok {
		out["status"] = stripTimes(st)
	}
	return out
}

func stripTimes(v any) any {
	switch t := v.(type) {
	case map[string]any:
		o := map[string]any{}
		for k, x := range t {
			switch k {
			case "lastTransitionTime", "lastHeartbeatTime", "lastProbeTime", "lastUpdateTime", "observedGeneration", "startedAt", "finishedAt", "startTime":
				continue
			}
			o[k] = stripTimes(x)
		}
		return o
	case []any:
		o := make([]any, len(t))
		for i, x := range t {
			o[i] = stripTimes(x)
		}
		return o
	default:
		return v
	}
}

// YAML renders a cleaned object.
func YAML(obj map[string]any) string {
	b, _ := yaml.Marshal(Clean(obj))
	return string(b)
}

// Change is one object that differs between two bundles.
type Change struct {
	Cluster   string   `json:"cluster"`
	Group     string   `json:"group"`
	Resource  string   `json:"resource"`
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Change    string   `json:"change"` // added, removed, changed
	Fields    []string `json:"fields,omitempty"`
	Before    string   `json:"before,omitempty"`
	After     string   `json:"after,omitempty"`
}

// VersionChange is a component image that moved between bundles.
type VersionChange struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Workload  string `json:"workload"`
	Container string `json:"container"`
	Before    string `json:"before"`
	After     string `json:"after"`
}

// Side describes one bundle.
type Side struct {
	BundleID string    `json:"bundleId"`
	Case     string    `json:"case"`
	Captured time.Time `json:"captured"`
	Clusters []string  `json:"clusters"`
	Verified bool      `json:"verified"`
}

// Report is the bundle-to-bundle comparison.
type Report struct {
	Before        Side              `json:"before"`
	After         Side              `json:"after"`
	Changes       []Change          `json:"changes"`
	Versions      []VersionChange   `json:"versions"`
	NewFindings   []analyze.Finding `json:"newFindings"`
	Resolved      []analyze.Finding `json:"resolvedFindings"`
	NotComparable []string          `json:"notComparable"`
	Summary       map[string]int    `json:"summary"`
}

func side(ws *store.Workspace) Side {
	s := Side{Clusters: ws.Clusters(), Verified: ws.State().Verified}
	if h := ws.Header(); h != nil {
		s.BundleID, s.Case, s.Captured = h.BundleID, h.Case, h.CaptureStart
	}
	if m := ws.Manifest(); m != nil && !m.CaptureEnd.IsZero() {
		s.Captured = m.CaptureEnd
	}
	return s
}

// covered reports whether ws holds a complete collection of this type in this
// namespace, so that the object's absence there means something.
func covered(ws *store.Workspace, cluster, group, resource, kind, ns string, namespaced bool) bool {
	st := ws.CollectionState(cluster, group, resource, kind, namespaced).State
	if st != store.StateComplete {
		return false
	}
	if p := ws.Policy(); p != nil && ns != "" && !p.NamespaceAllowed(ns) {
		return false
	}
	return true
}

// changedFields lists the paths (to depth 3) whose values differ.
func changedFields(a, b map[string]any) []string {
	var out []string
	var walk func(prefix string, x, y any, depth int)
	walk = func(prefix string, x, y any, depth int) {
		if reflect.DeepEqual(x, y) {
			return
		}
		xm, xok := x.(map[string]any)
		ym, yok := y.(map[string]any)
		if !xok || !yok || depth == 3 {
			out = append(out, prefix)
			return
		}
		keys := map[string]bool{}
		for k := range xm {
			keys[k] = true
		}
		for k := range ym {
			keys[k] = true
		}
		for k := range keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			walk(p, xm[k], ym[k], depth+1)
		}
	}
	walk("", a, b, 0)
	sort.Strings(out)
	return out
}

// skipKinds are captured per replica or per event and churn on every capture.
var skipKinds = map[string]bool{"events": true, "leases": true, "endpoints": true, "endpointslices": true, "controllerrevisions": true}

// Compare reports what changed from before to after.
func Compare(before, after *store.Workspace) Report {
	r := Report{Before: side(before), After: side(after), Summary: map[string]int{}}
	type key struct{ cluster, group, resource, ns, name string }
	index := func(ws *store.Workspace) map[key]*store.Object {
		m := map[key]*store.Object{}
		for _, c := range ws.Clusters() {
			for _, o := range ws.AllObjects(c) {
				if skipKinds[o.Resource] {
					continue
				}
				m[key{o.Cluster, o.Group, o.Resource, o.Namespace, o.Name}] = o
			}
		}
		return m
	}
	a, b := index(before), index(after)
	notComparable := map[string]int{}
	for k, bo := range b {
		ao, ok := a[k]
		switch {
		case !ok:
			if !covered(before, k.cluster, k.group, k.resource, bo.Kind, k.ns, bo.Namespaced) {
				notComparable[fmt.Sprintf("%s %s/%s: not captured completely in the earlier bundle", k.cluster, bo.Kind, k.ns)]++
				continue
			}
			r.Changes = append(r.Changes, Change{Cluster: k.cluster, Group: k.group, Resource: k.resource, Kind: bo.Kind, Namespace: k.ns, Name: k.name, Change: "added", After: YAML(bo.Obj)})
		default:
			ca, cb := Clean(ao.Obj), Clean(bo.Obj)
			if reflect.DeepEqual(ca, cb) {
				continue
			}
			r.Changes = append(r.Changes, Change{Cluster: k.cluster, Group: k.group, Resource: k.resource, Kind: bo.Kind, Namespace: k.ns, Name: k.name, Change: "changed",
				Fields: changedFields(ca, cb), Before: YAML(ao.Obj), After: YAML(bo.Obj)})
		}
	}
	for k, ao := range a {
		if _, ok := b[k]; ok {
			continue
		}
		if !covered(after, k.cluster, k.group, k.resource, ao.Kind, k.ns, ao.Namespaced) {
			notComparable[fmt.Sprintf("%s %s/%s: not captured completely in the later bundle", k.cluster, ao.Kind, k.ns)]++
			continue
		}
		r.Changes = append(r.Changes, Change{Cluster: k.cluster, Group: k.group, Resource: k.resource, Kind: ao.Kind, Namespace: k.ns, Name: k.name, Change: "removed", Before: YAML(ao.Obj)})
	}
	sort.Slice(r.Changes, func(i, j int) bool {
		x, y := r.Changes[i], r.Changes[j]
		return x.Cluster+x.Kind+x.Namespace+x.Name < y.Cluster+y.Kind+y.Namespace+y.Name
	})
	for _, c := range r.Changes {
		r.Summary[c.Change]++
	}
	for msg, n := range notComparable {
		r.NotComparable = append(r.NotComparable, fmt.Sprintf("%s (%d objects)", msg, n))
	}
	sort.Strings(r.NotComparable)
	r.Versions = versions(a, b)
	r.NewFindings, r.Resolved = findings(before, after)
	r.Summary["versions"], r.Summary["newFindings"], r.Summary["resolvedFindings"] = len(r.Versions), len(r.NewFindings), len(r.Resolved)
	return r
}

// versions compares container images of Deployments, DaemonSets and
// StatefulSets, which carry the versions that matter across an upgrade.
func versions[K comparable](a, b map[K]*store.Object) []VersionChange {
	images := func(m map[K]*store.Object) map[string]string {
		out := map[string]string{}
		for _, o := range m {
			if o.Group != "apps" || (o.Resource != "deployments" && o.Resource != "daemonsets" && o.Resource != "statefulsets") {
				continue
			}
			spec, _ := o.Obj["spec"].(map[string]any)
			tpl, _ := spec["template"].(map[string]any)
			ps, _ := tpl["spec"].(map[string]any)
			cs, _ := ps["containers"].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				n, _ := cm["name"].(string)
				img, _ := cm["image"].(string)
				out[strings.Join([]string{o.Cluster, o.Namespace, o.Kind + "/" + o.Name, n}, "|")] = img
			}
		}
		return out
	}
	ia, ib := images(a), images(b)
	var out []VersionChange
	for k, after := range ib {
		if before, ok := ia[k]; ok && before != after {
			p := strings.Split(k, "|")
			out = append(out, VersionChange{Cluster: p[0], Namespace: p[1], Workload: p[2], Container: p[3], Before: before, After: after})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace+out[i].Workload < out[j].Namespace+out[j].Workload })
	return out
}

func findingKey(f analyze.Finding) string {
	obj := ""
	if f.Object != nil {
		obj = f.Object.Kind + "/" + f.Object.Namespace + "/" + f.Object.Name
		// Pod names change with every rollout; key pod findings by title shape instead.
		if f.Object.Kind == "Pod" {
			obj = "Pod/" + f.Object.Namespace
		}
	}
	return f.Cluster + "|" + f.Rule + "|" + obj + "|" + strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return -1
		}
		return r
	}, f.Title)
}

// canJudge reports whether ws captured what a finding is about, so that the
// finding's absence from ws means the problem went away rather than that
// nobody looked.
func canJudge(ws *store.Workspace, f analyze.Finding) bool {
	p := ws.Policy()
	if p == nil {
		return true
	}
	if f.Object != nil && f.Object.Namespace != "" && !p.NamespaceAllowed(f.Object.Namespace) {
		return false
	}
	if strings.HasPrefix(f.Rule, "logs.") && !p.Has("logs") {
		return false
	}
	return true
}

func findings(before, after *store.Workspace) (added, resolved []analyze.Finding) {
	fa, fb := analyze.Run(before), analyze.Run(after)
	ka, kb := map[string]bool{}, map[string]bool{}
	for _, f := range fa {
		ka[findingKey(f)] = true
	}
	for _, f := range fb {
		kb[findingKey(f)] = true
		if !ka[findingKey(f)] && f.Severity != analyze.Info && canJudge(before, f) {
			added = append(added, f)
		}
	}
	for _, f := range fa {
		if !kb[findingKey(f)] && f.Severity != analyze.Info && canJudge(after, f) {
			resolved = append(resolved, f)
		}
	}
	return added, resolved
}

// Text renders the report for the terminal.
func (r Report) Text(showYAML bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Before: %s case %s captured %s\nAfter:  %s case %s captured %s\n", r.Before.BundleID, r.Before.Case, r.Before.Captured.Format(time.RFC3339), r.After.BundleID, r.After.Case, r.After.Captured.Format(time.RFC3339))
	fmt.Fprintf(&b, "\n%d added, %d removed, %d changed objects; %d image changes; %d new and %d resolved findings\n",
		r.Summary["added"], r.Summary["removed"], r.Summary["changed"], r.Summary["versions"], r.Summary["newFindings"], r.Summary["resolvedFindings"])
	if len(r.Versions) > 0 {
		b.WriteString("\nImage changes:\n")
		for _, v := range r.Versions {
			fmt.Fprintf(&b, "  %s %s/%s %s: %s -> %s\n", v.Cluster, v.Namespace, v.Workload, v.Container, v.Before, v.After)
		}
	}
	if len(r.NewFindings) > 0 {
		b.WriteString("\nNew findings:\n")
		for _, f := range r.NewFindings {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", strings.ToUpper(f.Severity), f.Cluster, f.Title)
		}
	}
	if len(r.Resolved) > 0 {
		b.WriteString("\nResolved findings:\n")
		for _, f := range r.Resolved {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", strings.ToUpper(f.Severity), f.Cluster, f.Title)
		}
	}
	if len(r.Changes) > 0 {
		b.WriteString("\nObjects:\n")
		for _, c := range r.Changes {
			ns := c.Namespace
			if ns != "" {
				ns += "/"
			}
			fmt.Fprintf(&b, "  %-8s %s %s %s%s", c.Change, c.Cluster, c.Kind, ns, c.Name)
			if len(c.Fields) > 0 {
				fields := c.Fields
				if len(fields) > 6 {
					fields = append(fields[:6:6], fmt.Sprintf("+%d more", len(c.Fields)-6))
				}
				fmt.Fprintf(&b, "  (%s)", strings.Join(fields, ", "))
			}
			b.WriteString("\n")
			if showYAML && c.Change == "changed" {
				b.WriteString(indent(c.Before, "      - ") + indent(c.After, "      + "))
			}
		}
	}
	if len(r.NotComparable) > 0 {
		b.WriteString("\nNot comparable (absence proves nothing here):\n")
		for _, n := range r.NotComparable {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	return b.String()
}

func indent(s, p string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString(p + l + "\n")
	}
	return b.String()
}

// JSON renders the report.
func (r Report) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return b
}
