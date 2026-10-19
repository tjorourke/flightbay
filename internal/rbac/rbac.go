package rbac

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

type Ref struct {
	Cluster, Group, Resource, Kind, Namespace, Name string
}

type Coverage struct {
	Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, ServiceAccounts string
	Complete bool
	Reason   string
}

type Grant struct {
	Scope         string   // namespace name, or "cluster"
	APIGroup      string
	Resource      string
	Subresource   string   // empty means the resource itself, not its subresources
	Verb          string
	ResourceNames []string // empty means all names
	Binding       Ref
	Role          Ref
	Sources       []string // aggregated ClusterRole names, when any
	Flags         []string
}

type BindingHit struct {
	Binding        Ref
	Role           Ref
	Why            string // "ServiceAccount", "User system:serviceaccount:ns:name", "Group system:serviceaccounts", "Group system:serviceaccounts:ns", "Group system:authenticated"
	RoleMissing    bool
	InvalidRoleRef bool
}

type Workload struct {
	Ref  Ref
	Pods int
}

type Explain struct {
	Cluster, Namespace, Name string
	Found                    bool
	Coverage                 Coverage
	Workloads                []Workload
	Grants                   []Grant
	Bindings                 []BindingHit
	Suggestion               string
	Notes                    []string
}

type WhoQuery struct {
	Cluster, Namespace, Verb, Group, Resource, Subresource, Name string
}

type WhoSubject struct {
	Kind, Name, Namespace, Via string
	Bindings                   []Ref
}

type Who struct {
	WhoQuery
	Coverage Coverage
	Subjects []WhoSubject
	Notes    []string
}

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }
func s(v any) string         { x, _ := v.(string); return x }

func strList(v any) []string {
	if xs, ok := v.([]string); ok {
		return append([]string(nil), xs...)
	}
	out := []string{}
	for _, x := range l(v) {
		if t := s(x); t != "" || (x != nil) {
			out = append(out, s(x))
		}
	}
	return out
}

func has(list []string, vals ...string) bool {
	for _, y := range list {
		for _, v := range vals {
			if y == v {
				return true
			}
		}
	}
	return false
}

func path(v any, keys ...string) any {
	cur := v
	for _, k := range keys {
		if cur == nil {
			return nil
		}
		vm := m(cur)
		cur = vm[k]
	}
	return cur
}

func copyStrs(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func (r Ref) eq(o Ref) bool {
	return r.Cluster == o.Cluster && r.Group == o.Group && r.Resource == o.Resource && r.Kind == o.Kind && r.Namespace == o.Namespace && r.Name == o.Name
}

// Collection coverage for the five RBAC+SA types.
func coverage(ws *store.Workspace, cluster string) (Coverage, bool, string, []string) {
	cov := Coverage{}
	allComplete := true
	var missing []string
	notes := []string{}

	check := func(group, res, kind string, namespaced bool, field *string) {
		st := ws.CollectionState(cluster, group, res, kind, namespaced)
		*field = st.State
		if st.State != store.StateComplete {
			allComplete = false
			if st.Reason != "" {
				missing = append(missing, fmt.Sprintf("%s (%s)", res, st.Reason))
			} else {
				missing = append(missing, res)
			}
		}
	}
	check("rbac.authorization.k8s.io", "roles", "Role", true, &cov.Roles)
	check("rbac.authorization.k8s.io", "rolebindings", "RoleBinding", true, &cov.RoleBindings)
	check("rbac.authorization.k8s.io", "clusterroles", "ClusterRole", false, &cov.ClusterRoles)
	check("rbac.authorization.k8s.io", "clusterrolebindings", "ClusterRoleBinding", false, &cov.ClusterRoleBindings)
	check("", "serviceaccounts", "ServiceAccount", true, &cov.ServiceAccounts)

	cov.Complete = allComplete
	reason := ""
	if !allComplete {
		reason = strings.Join(missing, ", ")
		notes = append(notes, "RBAC was not fully captured. Absence of a binding is not evidence that the permission is missing.")
	}
	return cov, allComplete, reason, notes
}

func subjectMatchesSA(b *store.Object, saNS, saName string) []string {
	whys := []string{}
	for _, sj := range l(b.Obj["subjects"]) {
		sm := m(sj)
		kind := s(sm["kind"])
		n := s(sm["name"])
		switch kind {
		case "ServiceAccount":
			ns := s(sm["namespace"])
			if ns == "" {
				ns = b.Namespace
			}
			if n == saName && ns == saNS {
				whys = append(whys, "ServiceAccount")
			}
		case "User":
			if n == fmt.Sprintf("system:serviceaccount:%s:%s", saNS, saName) {
				whys = append(whys, "User "+n)
			}
		case "Group":
			if n == "system:serviceaccounts" || n == "system:serviceaccounts:"+saNS || n == "system:authenticated" {
				whys = append(whys, "Group "+n)
			}
		}
	}
	return whys
}

func isValidRoleRef(rr map[string]any) bool {
	return s(rr["apiGroup"]) == "rbac.authorization.k8s.io"
}

func resolveRoleForBinding(ws *store.Workspace, cluster string, b *store.Object) (role *store.Object, invalid, missing bool, note string) {
	rr := m(b.Obj["roleRef"])
	if !isValidRoleRef(rr) {
		return nil, true, false, ""
	}
	kind := s(rr["kind"])
	rname := s(rr["name"])
	bns := b.Namespace
	if kind == "Role" {
		if b.Kind != "RoleBinding" {
			return nil, true, false, ""
		}
		r := ws.Get(cluster, "rbac.authorization.k8s.io", "roles", bns, rname)
		if r == nil {
			return nil, false, true, ""
		}
		return r, false, false, ""
	}
	if kind == "ClusterRole" {
		r := ws.Get(cluster, "rbac.authorization.k8s.io", "clusterroles", "", rname)
		if r == nil {
			return nil, false, true, ""
		}
		return r, false, false, ""
	}
	return nil, true, false, ""
}

func clusterRoleList(ws *store.Workspace, cluster string) []*store.Object {
	return ws.List(cluster, "rbac.authorization.k8s.io", "clusterroles", "", nil)
}

func labelMap(o *store.Object) map[string]string {
	if o == nil {
		return map[string]string{}
	}
	return o.Labels()
}

func selectorMatchesClusterRole(sel map[string]any, labels map[string]string, noteCollector *[]string) bool {
	ml := m(sel["matchLabels"])
	for k, v := range ml {
		if labels[k] != s(v) {
			return false
		}
	}
	for _, e := range l(sel["matchExpressions"]) {
		em := m(e)
		op := s(em["operator"])
		if op != "In" && op != "NotIn" && op != "Exists" && op != "DoesNotExist" {
			if noteCollector != nil {
				*noteCollector = append(*noteCollector, "aggregation selector not evaluated")
			}
			continue
		}
		k := s(em["key"])
		val, has := labels[k]
		var vals []string
		for _, vv := range l(em["values"]) {
			vals = append(vals, s(vv))
		}
		in := false
		for _, v := range vals {
			if v == val {
				in = true
			}
		}
		switch op {
		case "In":
			if !has || !in {
				return false
			}
		case "NotIn":
			if has && in {
				return false
			}
		case "Exists":
			if !has {
				return false
			}
		case "DoesNotExist":
			if has {
				return false
			}
		}
	}
	return true
}

func effectiveRules(role *store.Object, ws *store.Workspace, cluster string) ([]map[string]any, []string, []string) {
	if role == nil {
		return nil, nil, nil
	}
	stored := []map[string]any{}
	for _, r := range l(role.Obj["rules"]) {
		rm := m(r)
		cp := map[string]any{}
		for k, v := range rm {
			cp[k] = v
		}
		cp["_source"] = role.Name
		stored = append(stored, cp)
	}
	aggr := m(role.Obj["aggregationRule"])
	if len(aggr) == 0 || len(l(aggr["clusterRoleSelectors"])) == 0 {
		return stored, nil, nil
	}
	notes := []string{}
	selected := []map[string]any{}
	seenRoles := map[string]bool{role.Name: true}
	for _, sel := range l(aggr["clusterRoleSelectors"]) {
		sm := m(sel)
		opOk := true
		for _, e := range l(sm["matchExpressions"]) {
			op := s(m(e)["operator"])
			if op != "" && op != "In" && op != "NotIn" && op != "Exists" && op != "DoesNotExist" {
				opOk = false
			}
		}
		if !opOk {
			notes = append(notes, "aggregation selector not evaluated")
			continue
		}
		for _, oc := range clusterRoleList(ws, cluster) {
			if seenRoles[oc.Name] {
				continue
			}
			lbs := labelMap(oc)
			if selectorMatchesClusterRole(sm, lbs, &notes) {
				for _, r := range l(oc.Obj["rules"]) {
					rm := m(r)
					cp := map[string]any{}
					for k, v := range rm {
						cp[k] = v
					}
					cp["_source"] = oc.Name
					selected = append(selected, cp)
				}
			}
		}
	}
	// union deduped
	all := append(append([]map[string]any{}, stored...), selected...)
	dedup := []map[string]any{}
	seen := map[string]bool{}
	for _, r := range all {
		key := ruleKey(r)
		if !seen[key] {
			seen[key] = true
			dedup = append(dedup, r)
		}
	}
	srcs := []string{}
	for _, r := range selected {
		if src, ok := r["_source"].(string); ok && src != role.Name {
			srcs = append(srcs, src)
		}
	}
	sort.Strings(srcs)
	if len(stored) > 0 && len(selected) > 0 {
		// crude differ: if count differ
		if len(dedup) != len(all) {
			notes = append(notes, "stored rules and aggregation selectors differ; both are included")
		}
	}
	return dedup, srcs, notes
}

func ruleKey(r map[string]any) string {
	ags := strings.Join(strList(r["apiGroups"]), ",")
	res := strings.Join(strList(r["resources"]), ",")
	vs := strings.Join(strList(r["verbs"]), ",")
	rns := strings.Join(strList(r["resourceNames"]), ",")
	nru := strings.Join(strList(r["nonResourceURLs"]), ",")
	return ags + "|" + res + "|" + vs + "|" + rns + "|" + nru
}

func splitResource(r string) (res, sub string) {
	if i := strings.Index(r, "/"); i >= 0 {
		return r[:i], r[i+1:]
	}
	return r, ""
}

func allows(rule map[string]any, group, resource, subresource, verb, name string) bool {
	ags := strList(rule["apiGroups"])
	if len(ags) == 0 {
		ags = []string{""}
	}
	if !has(ags, group, "*") {
		return false
	}
	if !has(strList(rule["verbs"]), verb, "*") {
		return false
	}
	resList := strList(rule["resources"])
	if len(resList) == 0 {
		return false
	}
	matchedRes := false
	for _, rr := range resList {
		if rr == "*" {
			matchedRes = true
			break
		}
		if subresource == "" {
			if rr == resource && !strings.Contains(rr, "/") {
				matchedRes = true
				break
			}
		} else {
			q := resource + "/" + subresource
			if rr == q || rr == resource+"/*" || rr == "*/"+subresource {
				matchedRes = true
				break
			}
		}
	}
	if !matchedRes {
		return false
	}
	names := strList(rule["resourceNames"])
	if len(names) == 0 {
		return true
	}
	if name == "" {
		return false
	}
	return has(names, name)
}

func setFlags(gr *Grant) {
	apis := gr.APIGroup
	res := gr.Resource
	sub := gr.Subresource
	verb := gr.Verb
	if verb == "*" || res == "*" || apis == "*" || sub == "*" {
		gr.Flags = append(gr.Flags, "wildcard")
	}
	if (apis == "" || apis == "*") && res == "secrets" && has([]string{verb, "*"}, "get", "list", "watch") {
		gr.Flags = append(gr.Flags, "secrets-read")
	}
	if res == "pods" && sub == "exec" && has([]string{verb, "*"}, "create") {
		gr.Flags = append(gr.Flags, "pods-exec")
	}
	if res == "pods" && sub == "" && has([]string{verb, "*"}, "create") {
		gr.Flags = append(gr.Flags, "pods-create")
	}
	if verb == "impersonate" {
		gr.Flags = append(gr.Flags, "impersonate")
	}
	if verb == "escalate" {
		gr.Flags = append(gr.Flags, "escalate")
	}
	if verb == "bind" {
		gr.Flags = append(gr.Flags, "bind")
	}
	if res == "nodes" && sub == "proxy" {
		gr.Flags = append(gr.Flags, "nodes-proxy")
	}
	if res == "serviceaccounts" && sub == "token" && has([]string{verb, "*"}, "create") {
		gr.Flags = append(gr.Flags, "sa-token")
	}
}

func grantFromRule(rule map[string]any, scope string, b *store.Object, role *store.Object, extraSources []string) []Grant {
	var out []Grant
	ags := strList(rule["apiGroups"])
	if len(ags) == 0 {
		ags = []string{""}
	}
	ress := strList(rule["resources"])
	if len(ress) == 0 {
		return nil
	}
	vs := strList(rule["verbs"])
	if len(vs) == 0 {
		return nil
	}
	rns := copyStrs(strList(rule["resourceNames"]))
	src := []string{}
	if role != nil {
		src = append(src, role.Name)
	}
	src = append(src, extraSources...)
	sort.Strings(src)
	uniqSrc := []string{}
	seen := map[string]bool{}
	for _, s := range src {
		if !seen[s] {
			seen[s] = true
			uniqSrc = append(uniqSrc, s)
		}
	}
	bref := Ref{
		Cluster: b.Cluster, Group: "rbac.authorization.k8s.io",
		Resource: strings.ToLower(b.Kind) + "s", Kind: b.Kind,
		Namespace: b.Namespace, Name: b.Name,
	}
	roleRef := Ref{}
	if role != nil {
		roleRef = Ref{
			Cluster: role.Cluster, Group: role.Group, Resource: role.Resource,
			Kind: role.Kind, Namespace: role.Namespace, Name: role.Name,
		}
	}
	for _, g := range ags {
		for _, r := range ress {
			r0, sub := splitResource(r)
			for _, v := range vs {
				gr := Grant{
					Scope:         scope,
					APIGroup:      g,
					Resource:      r0,
					Subresource:   sub,
					Verb:          v,
					ResourceNames: copyStrs(rns),
					Binding:       bref,
					Role:          roleRef,
					Sources:       copyStrs(uniqSrc),
				}
				setFlags(&gr)
				out = append(out, gr)
			}
		}
	}
	return out
}

func sortGrants(gs []Grant) {
	sort.Slice(gs, func(i, j int) bool {
		a, b := gs[i], gs[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.APIGroup != b.APIGroup {
			return a.APIGroup < b.APIGroup
		}
		if a.Resource != b.Resource {
			return a.Resource < b.Resource
		}
		if a.Subresource != b.Subresource {
			return a.Subresource < b.Subresource
		}
		if a.Verb != b.Verb {
			return a.Verb < b.Verb
		}
		return a.Binding.Name < b.Binding.Name
	})
}

func workloadKinds() map[string]struct{ g, r string } {
	return map[string]struct{ g, r string }{
		"Deployment":  {"apps", "deployments"},
		"StatefulSet": {"apps", "statefulsets"},
		"DaemonSet":   {"apps", "daemonsets"},
		"Job":         {"batch", "jobs"},
		"CronJob":     {"batch", "cronjobs"},
		"ReplicaSet":  {"apps", "replicasets"},
		"Pod":         {"", "pods"},
	}
}

func isWorkloadKind(k string) bool {
	_, ok := workloadKinds()[k]
	return ok
}

func findTopController(ws *store.Workspace, cluster string, start *store.Object) *store.Object {
	cur := start
	var top *store.Object
	kindMap := workloadKinds()
	for depth := 0; depth < 5 && cur != nil; depth++ {
		refs := l(path(cur.Obj, "metadata", "ownerReferences"))
		next := (*store.Object)(nil)
		for _, ref := range refs {
			rm := m(ref)
			kind := s(rm["kind"])
			if isWorkloadKind(kind) {
				kg := kindMap[kind]
				owner := ws.Get(cluster, kg.g, kg.r, cur.Namespace, s(rm["name"]))
				if owner != nil {
					top = owner
					next = owner
					break
				}
			}
		}
		if next == nil {
			break
		}
		cur = next
	}
	if top != nil {
		return top
	}
	return start
}

func collectWorkloads(ws *store.Workspace, cluster, namespace, saName string) []Workload {
	kindMap := workloadKinds()
	// controllers that declare the SA in template
	templates := map[string]*store.Object{}
	controllers := []*store.Object{}
	for k, kg := range kindMap {
		if k == "Pod" {
			continue
		}
		for _, o := range ws.List(cluster, kg.g, kg.r, namespace, nil) {
			controllers = append(controllers, o)
			var spec any
			if k == "CronJob" {
				spec = path(o.Obj, "spec", "jobTemplate", "spec", "template", "spec")
			} else {
				spec = path(o.Obj, "spec", "template", "spec")
			}
			sa := s(path(spec, "serviceAccountName"))
			if sa == "" {
				sa = "default"
			}
			if sa == saName {
				id := o.Kind + "/" + o.Namespace + "/" + o.Name
				templates[id] = o
			}
		}
	}
	// running pods counts
	counts := map[string]int{}
	pods := ws.List(cluster, "", "pods", namespace, nil)
	for _, p := range pods {
		sa := s(path(p.Obj, "spec", "serviceAccountName"))
		if sa == "" {
			sa = "default"
		}
		if sa != saName {
			continue
		}
		top := findTopController(ws, cluster, p)
		if top == nil {
			top = p
		}
		id := top.Kind + "/" + top.Namespace + "/" + top.Name
		counts[id]++
		templates[id] = top // ensure
	}
	// build list, include 0 pod templates
	var wsOut []Workload
	for id, o := range templates {
		c := counts[id]
		kg := kindMap[o.Kind]
		wsOut = append(wsOut, Workload{
			Ref: Ref{
				Cluster: cluster, Group: kg.g, Resource: kg.r, Kind: o.Kind,
				Namespace: o.Namespace, Name: o.Name,
			},
			Pods: c,
		})
	}
	// bare pods not attributed? if no top, they are in templates as themselves? Wait, for bare, findTop returns the pod, but we didn't add pods to templates unless matched.
	// add bare pods as their own if not under template
	for _, p := range pods {
		sa := s(path(p.Obj, "spec", "serviceAccountName"))
		if sa == "" { sa = "default" }
		if sa != saName { continue }
		top := findTopController(ws, cluster, p)
		if top.Kind == "Pod" {
			id := "Pod/" + p.Namespace + "/" + p.Name
			if _, ok := templates[id]; !ok {
				wsOut = append(wsOut, Workload{
					Ref: Ref{Cluster: cluster, Group: "", Resource: "pods", Kind: "Pod", Namespace: p.Namespace, Name: p.Name},
					Pods: 1,
				})
			}
		}
	}
	// sort
	sort.Slice(wsOut, func(i, j int) bool {
		a, b := wsOut[i].Ref, wsOut[j].Ref
		if a.Kind != b.Kind { return a.Kind < b.Kind }
		return a.Name < b.Name
	})
	return wsOut
}

func suggest(ex Explain, ws *store.Workspace) string {
	var b strings.Builder
	b.WriteString("# Review only. Flightbay does not apply this.\n")
	b.WriteString(fmt.Sprintf("# Built from RoleBindings in %s for ServiceAccount %s/%s.\n", ex.Namespace, ex.Namespace, ex.Name))
	b.WriteString("# Cluster-wide grants are not copied into this Role.\n")
	if !ex.Coverage.Complete {
		b.WriteString("# Incomplete capture. This suggestion may omit grants that were not collected.\n")
	}
	fmt.Fprintf(&b, "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: %s-review\n  namespace: %s\nrules:\n", ex.Name, ex.Namespace)

	// collect ns grants no flags
	type ruleKey struct {
		g   string
		vs  string
		rns string
	}
	groups := map[ruleKey]map[string]bool{} // res -> true , and will use sub?
	// actually since sub in resource name like pods/log
	secretBroad := false
	for _, gr := range ex.Grants {
		if gr.Scope != "cluster" && gr.APIGroup == "" && gr.Resource == "secrets" && has([]string{gr.Verb, "*"}, "get", "list", "watch") && len(gr.ResourceNames) == 0 {
			secretBroad = true
		}
		if gr.Scope == "cluster" || len(gr.Flags) > 0 {
			continue
		}
		rk := ruleKey{gr.APIGroup, strings.Join([]string{gr.Verb}, ","), strings.Join(gr.ResourceNames, ",")}
		if groups[rk] == nil {
			groups[rk] = map[string]bool{}
		}
		rname := gr.Resource
		if gr.Subresource != "" {
			rname += "/" + gr.Subresource
		}
		groups[rk][rname] = true
	}

	// referenced secrets from workloads that use this SA
	refSecrets := collectReferencedSecrets(ws, ex.Cluster, ex.Namespace, ex.Name)
	if secretBroad && len(refSecrets) > 0 {
		// emit narrow get
		fmt.Fprintf(&b, "- apiGroups: [\"\"]\n  resources: [\"secrets\"]\n  verbs: [\"get\"]\n  resourceNames: [%s]\n", quoteList(refSecrets))
		b.WriteString("  # list and watch were removed because they read every Secret in the namespace.\n")
	} else if secretBroad && len(refSecrets) == 0 {
		b.WriteString("  # no named Secret to narrow to; the broad secrets rule was left out.\n")
	}

	// emit the grouped
	keys := []ruleKey{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].g < keys[j].g })
	for _, rk := range keys {
		resList := []string{}
		for r := range groups[rk] {
			resList = append(resList, r)
		}
		sort.Strings(resList)
		g := rk.g
		if g == "" {
			g = `""`
		} else {
			g = fmt.Sprintf("%q", g)
		}
		fmt.Fprintf(&b, "- apiGroups: [%s]\n  resources: [%s]\n  verbs: [%s]\n", g, quoteList(resList), quoteList(strings.Split(rk.vs, ",")))
		if rk.rns != "" {
			fmt.Fprintf(&b, "  resourceNames: [%s]\n", quoteList(strings.Split(rk.rns, ",")))
		}
	}
	if len(groups) == 0 && !secretBroad {
		b.WriteString("  # no narrow namespace rule was captured.\n")
		b.WriteString("[]\n")
	}
	// cluster comments
	for _, gr := range ex.Grants {
		if gr.Scope != "cluster" {
			continue
		}
		rname := gr.Resource
		if gr.Subresource != "" {
			rname += "/" + gr.Subresource
		}
		fmt.Fprintf(&b, "# cluster-wide: %s %s via %s\n", gr.Verb, rname, gr.Binding.Name)
	}
	return b.String()
}

func quoteList(xs []string) string {
	qs := []string{}
	for _, x := range xs {
		if x != "" {
			qs = append(qs, fmt.Sprintf("%q", x))
		}
	}
	return strings.Join(qs, ", ")
}

func collectReferencedSecrets(ws *store.Workspace, cluster, ns, sa string) []string {
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" {
			seen[n] = true
		}
	}
	// pods running as
	for _, p := range ws.List(cluster, "", "pods", ns, nil) {
		saName := s(path(p.Obj, "spec", "serviceAccountName"))
		if saName == "" {
			saName = "default"
		}
		if saName != sa {
			continue
		}
		collectSecretsFromPodSpec(path(p.Obj, "spec"), add)
	}
	// controllers templates
	for _, kg := range []struct{ g, r, k string }{
		{"apps", "deployments", "Deployment"},
		{"apps", "statefulsets", "StatefulSet"},
		{"apps", "daemonsets", "DaemonSet"},
		{"apps", "replicasets", "ReplicaSet"},
		{"batch", "jobs", "Job"},
		{"batch", "cronjobs", "CronJob"},
	} {
		for _, c := range ws.List(cluster, kg.g, kg.r, ns, nil) {
			var spec any
			if kg.k == "CronJob" {
				spec = path(c.Obj, "spec", "jobTemplate", "spec", "template", "spec")
			} else {
				spec = path(c.Obj, "spec", "template", "spec")
			}
			saName := s(path(spec, "serviceAccountName"))
			if saName == "" {
				saName = "default"
			}
			if saName == sa {
				collectSecretsFromPodSpec(spec, add)
			}
		}
	}
	out := []string{}
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func collectSecretsFromPodSpec(spec any, add func(string)) {
	sp := m(spec)
	for _, ct := range append(l(sp["containers"]), l(sp["initContainers"])...) {
		for _, e := range l(m(ct)["env"]) {
			add(s(path(e, "valueFrom", "secretKeyRef", "name")))
		}
		for _, e := range l(m(ct)["envFrom"]) {
			add(s(path(e, "secretRef", "name")))
		}
	}
	for _, v := range l(sp["volumes"]) {
		vm := m(v)
		add(s(path(vm, "secret", "secretName")))
		for _, src := range l(path(vm, "projected", "sources")) {
			add(s(path(src, "secret", "name")))
		}
	}
}

func ExplainSA(ws *store.Workspace, cluster, namespace, name string) Explain {
	ex := Explain{Cluster: cluster, Namespace: namespace, Name: name}
	if ws == nil {
		return ex
	}
	// does SA exist? not required for explain
	sa := ws.Get(cluster, "", "serviceaccounts", namespace, name)
	ex.Found = sa != nil

	cov, complete, reason, notes := coverage(ws, cluster)
	ex.Coverage = cov
	if reason != "" {
		ex.Coverage.Reason = reason
	}
	ex.Notes = append(ex.Notes, notes...)

	// bindings
	rbs := ws.List(cluster, "rbac.authorization.k8s.io", "rolebindings", "", nil)
	crbs := ws.List(cluster, "rbac.authorization.k8s.io", "clusterrolebindings", "", nil)
	bindings := append(append([]*store.Object{}, rbs...), crbs...)
	hitByBinding := map[string]BindingHit{}
	var allGrants []Grant
	whyForBinding := map[string][]string{}
	for _, b := range bindings {
		whys := subjectMatchesSA(b, namespace, name)
		if len(whys) == 0 {
			continue
		}
		role, invalid, missing, _ := resolveRoleForBinding(ws, cluster, b)
		scope := "cluster"
		if b.Kind == "RoleBinding" {
			scope = b.Namespace
		}
		bref := Ref{Cluster: cluster, Group: "rbac.authorization.k8s.io", Resource: strings.ToLower(b.Kind) + "s", Kind: b.Kind, Namespace: b.Namespace, Name: b.Name}
		rref := Ref{}
		if role != nil {
			rref = Ref{Cluster: cluster, Group: role.Group, Resource: role.Resource, Kind: role.Kind, Namespace: role.Namespace, Name: role.Name}
		}
		for _, w := range whys {
			h := BindingHit{Binding: bref, Role: rref, Why: w, RoleMissing: missing, InvalidRoleRef: invalid}
			key := b.Namespace + "/" + b.Name + "/" + w
			hitByBinding[key] = h
			whyForBinding[b.Namespace+"/"+b.Name] = append(whyForBinding[b.Namespace+"/"+b.Name], w)
		}
		if invalid || (missing && role == nil) {
			if !complete {
				ex.Notes = append(ex.Notes, "rules not captured")
			}
			continue
		}
		if role == nil {
			continue
		}
		eff, srcs, enotes := effectiveRules(role, ws, cluster)
		ex.Notes = append(ex.Notes, enotes...)
		for _, rule := range eff {
			nonRes := strList(rule["nonResourceURLs"])
			if len(nonRes) > 0 {
				ex.Notes = append(ex.Notes, "non-resource URLs are not expanded")
				// still process? but grants only for resource rules, skip adding if only nonres?
				if len(strList(rule["resources"])) == 0 {
					continue
				}
			}
			gs := grantFromRule(rule, scope, b, role, srcs)
			allGrants = append(allGrants, gs...)
		}
	}
	for _, h := range hitByBinding {
		ex.Bindings = append(ex.Bindings, h)
	}
	sort.Slice(ex.Bindings, func(i, j int) bool {
		return ex.Bindings[i].Binding.Name < ex.Bindings[j].Binding.Name
	})

	sortGrants(allGrants)
	ex.Grants = allGrants

	ex.Workloads = collectWorkloads(ws, cluster, namespace, name)

	ex.Suggestion = suggest(ex, ws)

	// dedup notes
	seenN := map[string]bool{}
	nn := []string{}
	for _, n := range ex.Notes {
		if n != "" && !seenN[n] {
			seenN[n] = true
			nn = append(nn, n)
		}
	}
	ex.Notes = nn
	return ex
}

func WhoCan(ws *store.Workspace, q WhoQuery) Who {
	w := Who{WhoQuery: q}
	if ws == nil {
		return w
	}
	cov, complete, reason, notes := coverage(ws, q.Cluster)
	w.Coverage = cov
	if reason != "" {
		w.Coverage.Reason = reason
	}
	w.Notes = append(w.Notes, notes...)

	rbs := ws.List(q.Cluster, "rbac.authorization.k8s.io", "rolebindings", q.Namespace, nil)
	crbs := ws.List(q.Cluster, "rbac.authorization.k8s.io", "clusterrolebindings", "", nil)
	allB := append(append([]*store.Object{}, rbs...), crbs...)

	subjMap := map[string]WhoSubject{}
	addSubj := func(kind, name, ns, via string, bind Ref) {
		k := kind + "/" + ns + "/" + name + "/" + via
		su, ok := subjMap[k]
		if !ok {
			su = WhoSubject{Kind: kind, Name: name, Namespace: ns, Via: via}
		}
		hasB := false
		for _, bb := range su.Bindings {
			if bb.eq(bind) {
				hasB = true
				break
			}
		}
		if !hasB {
			su.Bindings = append(su.Bindings, bind)
		}
		subjMap[k] = su
	}

	for _, b := range allB {
		// only RoleBindings in q.Namespace
		if b.Kind == "RoleBinding" && b.Namespace != q.Namespace {
			continue
		}
		role, invalid, missing, _ := resolveRoleForBinding(ws, q.Cluster, b)
		if invalid || missing || role == nil {
			continue
		}
		eff, _, _ := effectiveRules(role, ws, q.Cluster)
		grantsThis := false
		for _, rule := range eff {
			if allows(rule, q.Group, q.Resource, q.Subresource, q.Verb, q.Name) {
				grantsThis = true
				break
			}
		}
		if !grantsThis {
			continue
		}
		scope := "cluster"
		if b.Kind == "RoleBinding" {
			scope = b.Namespace
		}
		_ = scope
		bref := Ref{Cluster: q.Cluster, Group: "rbac.authorization.k8s.io", Resource: strings.ToLower(b.Kind) + "s", Kind: b.Kind, Namespace: b.Namespace, Name: b.Name}
		for _, sj := range l(b.Obj["subjects"]) {
			sm := m(sj)
			kind := s(sm["kind"])
			n := s(sm["name"])
			sns := s(sm["namespace"])
			if sns == "" {
				sns = b.Namespace
			}
			if kind == "ServiceAccount" || kind == "User" || kind == "Group" {
				addSubj(kind, n, sns, "direct", bref)
			}
		}
		// now expand groups on this binding
		for _, sj := range l(b.Obj["subjects"]) {
			sm := m(sj)
			if s(sm["kind"]) != "Group" {
				continue
			}
			g := s(sm["name"])
			if g == "system:serviceaccounts" {
				// all SAs
				for _, sa := range ws.List(q.Cluster, "", "serviceaccounts", "", nil) {
					addSubj("ServiceAccount", sa.Name, sa.Namespace, g, bref)
				}
			} else if strings.HasPrefix(g, "system:serviceaccounts:") {
				gns := strings.TrimPrefix(g, "system:serviceaccounts:")
				for _, sa := range ws.List(q.Cluster, "", "serviceaccounts", gns, nil) {
					addSubj("ServiceAccount", sa.Name, sa.Namespace, g, bref)
				}
			} else if g == "system:authenticated" {
				for _, sa := range ws.List(q.Cluster, "", "serviceaccounts", "", nil) {
					addSubj("ServiceAccount", sa.Name, sa.Namespace, g, bref)
				}
				w.Notes = append(w.Notes, "users are not in the capture, so this group is only expanded to ServiceAccounts.")
			}
		}
	}
	for _, su := range subjMap {
		sort.Slice(su.Bindings, func(i, j int) bool { return su.Bindings[i].Name < su.Bindings[j].Name })
		w.Subjects = append(w.Subjects, su)
	}
	sort.Slice(w.Subjects, func(i, j int) bool {
		a, b := w.Subjects[i], w.Subjects[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	if !complete {
		w.Notes = append(w.Notes, "cluster role collection is not complete; result is not proof that no other subject can.")
	}
	return w
}
