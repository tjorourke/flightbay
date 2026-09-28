// Package policy resolves what a customer allows Flightbay to collect.
// It is evaluated before any customer data is read: exclusions always win,
// and nothing is silently added to satisfy a diagnostic dependency.
package policy

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Category is a class of collected data.
type Category string

const (
	CatObjects      Category = "objects"
	CatLogs         Category = "logs"
	CatEvents       Category = "events"
	CatNodes        Category = "nodes"
	CatHelm         Category = "helm"
	CatProxies      Category = "proxies"
	CatControlPlane Category = "control-plane"
	CatMetrics      Category = "metrics"
)

// AllCategories lists every category in display order.
var AllCategories = []Category{CatObjects, CatLogs, CatEvents, CatNodes, CatHelm, CatProxies, CatControlPlane, CatMetrics}

// NotYetImplemented lists categories this build cannot collect yet.
var NotYetImplemented = map[Category]bool{}

// Object detail levels.
const (
	DetailMetadata = "metadata"
	DetailFull     = "full"
)

// LogPolicy scopes container log collection.
type LogPolicy struct {
	Streams    []string `json:"streams,omitempty"`    // current, previous, init, ephemeral
	Since      string   `json:"since,omitempty"`      // Go duration, e.g. 24h
	MaxBytes   int64    `json:"maxBytes,omitempty"`   // per container stream, tail-biased
	Pods       []string `json:"pods,omitempty"`       // pod name globs; empty = all
	Containers []string `json:"containers,omitempty"` // container name globs; empty = all
}

// FlowPolicy declares observed-traffic sources beyond the captured logs.
type FlowPolicy struct {
	// Hubble is how many recent flows to read from Cilium's hubble-relay
	// (svc/hubble-relay in kube-system) through port-forward and the local
	// hubble CLI; 0 = off. Flows naming namespaces outside the selection are
	// dropped and HTTP paths are redacted before writing.
	Hubble int `json:"hubble,omitempty"`
	// Attach lists flow log files supplied by the customer and embedded in the
	// bundle (base names; the content is covered by the manifest).
	Attach []string `json:"attach,omitempty"`
}

// HubbleNamespace and HubbleRelay locate Cilium's flow relay.
const (
	HubbleNamespace = "kube-system"
	HubbleRelay     = "hubble-relay"
)

// Policy is the resolved collection policy. It is written into every bundle.
type Policy struct {
	Profile           string     `json:"profile,omitempty"`
	Case              string     `json:"case,omitempty"`
	Contexts          []string   `json:"contexts,omitempty"`
	Namespaces        []string   `json:"namespaces,omitempty"`
	ExcludeNamespaces []string   `json:"excludeNamespaces,omitempty"`
	Categories        []Category `json:"categories,omitempty"`
	Resources         []string   `json:"resources,omitempty"`
	ExcludeResources  []string   `json:"excludeResources,omitempty"`
	LabelSelector     string     `json:"labelSelector,omitempty"`
	ClusterScoped     bool       `json:"clusterScoped,omitempty"`
	ObjectDetail      string     `json:"objectDetail,omitempty"`
	Logs              LogPolicy  `json:"logs,omitempty"`
	ConfigMaps        string     `json:"configMaps,omitempty"` // omit, metadata, scrubbed
	Secrets           string     `json:"secrets,omitempty"`    // omit, metadata, keys
	PortForward       bool       `json:"portForward,omitempty"`
	Compression       string     `json:"compression,omitempty"`
	EncryptTo         []string   `json:"encryptTo,omitempty"`
	MaxBundleBytes    int64      `json:"maxBundleBytes,omitempty"`
	RedactExtra       []string   `json:"redactExtra,omitempty"`
	QPS               float32    `json:"qps,omitempty"`
	Burst             int        `json:"burst,omitempty"`
	Flows             FlowPolicy `json:"flows,omitempty"`
}

// Profile returns a named starting policy.
func Profile(name string) (*Policy, error) {
	base := Policy{Profile: name, ObjectDetail: DetailFull, ConfigMaps: "scrubbed", Secrets: "keys", Compression: "best",
		Logs: LogPolicy{Streams: []string{"current", "previous"}, Since: "24h", MaxBytes: 20 << 20}, QPS: 20, Burst: 40}
	switch name {
	case "minimal":
		base.Categories = []Category{CatObjects, CatEvents}
		base.ObjectDetail = DetailMetadata
		base.ConfigMaps = "metadata"
		base.Secrets = "metadata"
	case "standard", "":
		base.Profile = "standard"
		base.Categories = []Category{CatObjects, CatEvents, CatLogs, CatHelm}
	case "custom":
		base.Categories = nil
	default:
		return nil, fmt.Errorf("unknown profile %q (use minimal, standard or custom)", name)
	}
	return &base, nil
}

// Load reads a collection config file on top of the profile it names.
func Load(file string) (*Policy, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var head struct {
		Profile string `json:"profile"`
	}
	if err := yaml.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if head.Profile == "" {
		head.Profile = "custom"
	}
	p, err := Profile(head.Profile)
	if err != nil {
		return nil, err
	}
	if err := yaml.UnmarshalStrict(raw, p); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return p, p.Validate()
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// Validate rejects settings that cannot be honoured.
func (p *Policy) Validate() error {
	if !oneOf(p.Secrets, "omit", "metadata", "keys") {
		return fmt.Errorf("secrets must be omit, metadata or keys (values are never collected), got %q", p.Secrets)
	}
	if !oneOf(p.ConfigMaps, "omit", "metadata", "scrubbed") {
		return fmt.Errorf("configMaps must be omit, metadata or scrubbed, got %q", p.ConfigMaps)
	}
	if !oneOf(p.Compression, "best", "better", "default", "fastest") {
		return fmt.Errorf("compression must be best, better, default or fastest, got %q", p.Compression)
	}
	if !oneOf(p.ObjectDetail, DetailMetadata, DetailFull) {
		return fmt.Errorf("objectDetail must be metadata or full, got %q", p.ObjectDetail)
	}
	for _, c := range p.Categories {
		found := false
		for _, k := range AllCategories {
			found = found || k == c
		}
		if !found {
			return fmt.Errorf("unknown category %q", c)
		}
	}
	for _, s := range p.Logs.Streams {
		if !oneOf(s, "current", "previous", "init", "ephemeral") {
			return fmt.Errorf("unknown log stream %q (use current, previous, init, ephemeral)", s)
		}
	}
	if p.Logs.Since != "" {
		if _, err := time.ParseDuration(p.Logs.Since); err != nil {
			return fmt.Errorf("logs.since: %w", err)
		}
	}
	if p.Flows.Hubble < 0 {
		return fmt.Errorf("flows.hubble must be 0 (off) or a number of flows, got %d", p.Flows.Hubble)
	}
	for _, g := range append(append([]string{}, p.Namespaces...), p.ExcludeNamespaces...) {
		if _, err := path.Match(g, ""); err != nil {
			return fmt.Errorf("bad namespace pattern %q: %w", g, err)
		}
	}
	return nil
}

// Has reports whether a category was requested.
func (p *Policy) Has(c Category) bool {
	for _, x := range p.Categories {
		if x == c {
			return true
		}
	}
	return false
}

// Stream reports whether a log stream is selected.
func (p *Policy) Stream(s string) bool {
	for _, x := range p.Logs.Streams {
		if x == s {
			return true
		}
	}
	return false
}

// SinceDuration returns the log window, zero meaning unlimited.
func (p *Policy) SinceDuration() time.Duration {
	d, _ := time.ParseDuration(p.Logs.Since)
	return d
}

func globAny(patterns []string, s string) bool {
	for _, g := range patterns {
		if ok, _ := path.Match(g, s); ok {
			return true
		}
	}
	return false
}

// NamespaceAllowed applies namespace includes and excludes. Excludes win.
func (p *Policy) NamespaceAllowed(ns string) bool {
	if globAny(p.ExcludeNamespaces, ns) {
		return false
	}
	return len(p.Namespaces) == 0 || globAny(p.Namespaces, ns)
}

// ExplicitNamespaces returns the namespace list when it contains no globs.
func (p *Policy) ExplicitNamespaces() []string {
	for _, n := range p.Namespaces {
		if strings.ContainsAny(n, "*?[") {
			return nil
		}
	}
	return p.Namespaces
}

func resourceMatch(specs []string, resource, group, kind string) bool {
	full := resource
	if group != "" {
		full = resource + "." + group
	}
	for _, s := range specs {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == resource || s == full || s == strings.ToLower(kind) {
			return true
		}
		if ok, _ := path.Match(s, full); ok {
			return true
		}
	}
	return false
}

// ResourceAllowed decides whether a resource type may be read at all.
func (p *Policy) ResourceAllowed(resource, group, kind string, namespaced bool) bool {
	if resourceMatch(p.ExcludeResources, resource, group, kind) {
		return false
	}
	if group == "" && resource == "secrets" && p.Secrets == "omit" {
		return false
	}
	if group == "" && resource == "configmaps" && p.ConfigMaps == "omit" {
		return false
	}
	if !namespaced && !p.ClusterScoped {
		return false
	}
	return len(p.Resources) == 0 || resourceMatch(p.Resources, resource, group, kind)
}

// Plan is the resolved selection shown by --dry-run.
type Plan struct {
	Categories  []Category `json:"categories"`
	Unavailable []string   `json:"unavailable,omitempty"`
}

// Selected reports whether a category will actually be collected.
func (pl Plan) Selected(c Category) bool {
	for _, x := range pl.Categories {
		if x == c {
			return true
		}
	}
	return false
}

// Plan resolves dependencies between categories without expanding the selection.
func (p *Policy) Plan() Plan {
	var pl Plan
	for _, c := range AllCategories {
		if !p.Has(c) {
			continue
		}
		switch {
		case (c == CatProxies || c == CatControlPlane || c == CatMetrics) && !p.PortForward:
			pl.Unavailable = append(pl.Unavailable, fmt.Sprintf("%s: needs port-forward, which this policy does not allow", c))
		case NotYetImplemented[c]:
			pl.Unavailable = append(pl.Unavailable, fmt.Sprintf("%s: not implemented in this build of flightbay", c))
		case c == CatHelm && (!p.ResourceAllowed("secrets", "", "Secret", true) || p.Secrets != "keys"):
			pl.Unavailable = append(pl.Unavailable, "helm: release summaries are read from Secret data, which needs secrets=keys and Secrets not excluded")
		case c == CatNodes && !p.ClusterScoped:
			pl.Unavailable = append(pl.Unavailable, "nodes: cluster-scoped data needs --cluster-scoped")
		case c == CatLogs && len(p.Logs.Streams) == 0:
			pl.Unavailable = append(pl.Unavailable, "logs: no log streams selected")
		default:
			pl.Categories = append(pl.Categories, c)
		}
	}
	if p.Flows.Hubble > 0 && !p.PortForward {
		pl.Unavailable = append(pl.Unavailable, "hubble flows: needs port-forward to hubble-relay, which this policy does not allow")
	}
	return pl
}

// HubbleSelected reports whether flows will be read from hubble-relay.
func (p *Policy) HubbleSelected() bool { return p.Flows.Hubble > 0 && p.PortForward }

// APIResource is a discovered resource type.
type APIResource struct {
	Group, Version, Resource, Kind string
	Namespaced                     bool
}

type ruleSet map[string]map[string]bool // group -> resource -> true

func (r ruleSet) add(group, res string) {
	if r[group] == nil {
		r[group] = map[string]bool{}
	}
	r[group][res] = true
}

func renderRules(b *strings.Builder, rs ruleSet, verbs string) {
	groups := make([]string, 0, len(rs))
	for g := range rs {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		res := make([]string, 0, len(rs[g]))
		for r := range rs[g] {
			res = append(res, r)
		}
		sort.Strings(res)
		gq := g
		if g == "" {
			gq = `""`
		} else if g == "*" {
			gq = "'*'"
		}
		fmt.Fprintf(b, "  - apiGroups:\n    - %s\n    resources:\n", gq)
		for _, r := range res {
			if r == "*" {
				r = "'*'"
			}
			fmt.Fprintf(b, "    - %s\n", r)
		}
		fmt.Fprintf(b, "    verbs: [%s]\n", verbs)
	}
}

// RBAC renders least-privilege permissions for this policy. With discovered
// resources it lists them explicitly; otherwise it falls back to a wildcard.
func (p *Policy) RBAC(discovered []APIResource) string {
	pl := p.Plan()
	nsRules, clusterRules, pfRules := ruleSet{}, ruleSet{}, ruleSet{}
	var crdNames []string // CRD definitions read for printer columns; "*" = any
	var b strings.Builder
	b.WriteString("# Generated by `flightbay rbac` from the resolved collection policy.\n")
	b.WriteString("# Change the subject to the user, group or service account that runs flightbay.\n")
	if pl.Selected(CatObjects) || pl.Selected(CatHelm) {
		switch {
		case discovered != nil:
			for _, r := range discovered {
				if !p.ResourceAllowed(r.Resource, r.Group, r.Kind, r.Namespaced) {
					continue
				}
				if r.Namespaced {
					nsRules.add(r.Group, r.Resource)
				} else {
					clusterRules.add(r.Group, r.Resource)
				}
				if strings.Contains(r.Group, ".") {
					crdNames = append(crdNames, r.Resource+"."+r.Group)
				}
			}
		case len(p.Resources) > 0:
			for _, spec := range p.Resources {
				res, group, _ := strings.Cut(strings.ToLower(spec), ".")
				if !p.ResourceAllowed(res, group, "", true) {
					continue
				}
				nsRules.add(group, res)
				if strings.Contains(group, ".") {
					crdNames = append(crdNames, res+"."+group)
				}
			}
		default:
			crdNames = []string{"*"}
			if len(p.ExcludeResources) > 0 || p.Secrets == "omit" || p.ConfigMaps == "omit" {
				b.WriteString("# WARNING: Kubernetes RBAC cannot express exclusions. This wildcard grants more than\n")
				b.WriteString("# the policy collects; the collector still enforces every exclusion. Run\n")
				b.WriteString("# `flightbay rbac --discover` against the cluster for an explicit list.\n")
			}
			nsRules.add("*", "*")
			if p.ClusterScoped {
				clusterRules.add("*", "*")
			}
		}
	}
	if pl.Selected(CatLogs) {
		nsRules.add("", "pods")
		nsRules.add("", "pods/log")
	}
	if pl.Selected(CatEvents) {
		nsRules.add("", "events")
		nsRules.add("events.k8s.io", "events")
	}
	if pl.Selected(CatNodes) {
		clusterRules.add("", "nodes")
	}
	if p.PortForward && (p.Has(CatProxies) || p.Has(CatControlPlane) || p.Has(CatMetrics)) {
		nsRules.add("", "pods") // to find the Solo components among the selected pods
		pfRules.add("", "pods/portforward")
	}
	namespaces := p.ExplicitNamespaces()
	// The Namespace objects in scope are always read (their labels decide
	// ambient enrolment); a namespace-limited run only needs those names.
	nsRead := pl.Selected(CatObjects) && !p.ClusterScoped
	subject := "  - apiGroup: rbac.authorization.k8s.io\n    kind: Group\n    name: flightbay:readers\n"
	crdRole := func() {
		if len(crdNames) == 0 {
			return
		}
		b.WriteString("---\n# CRD definitions of the selected custom resources: names, scope and printer\n# columns only (the schema is not kept). Cluster-scoped by nature.\n")
		b.WriteString("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: flightbay-crd-reader\nrules:\n  - apiGroups:\n    - apiextensions.k8s.io\n    resources:\n    - customresourcedefinitions\n")
		if crdNames[0] != "*" {
			sort.Strings(crdNames)
			b.WriteString("    resourceNames:\n")
			for _, n := range crdNames {
				fmt.Fprintf(&b, "    - %s\n", n)
			}
		}
		b.WriteString("    verbs: [get]\n")
		fmt.Fprintf(&b, "---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: flightbay-crd-reader\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: flightbay-crd-reader\nsubjects:\n%s", subject)
	}
	hubbleRole := func() {
		if !p.HubbleSelected() {
			return
		}
		fmt.Fprintf(&b, "---\n# Recent flows from Cilium's hubble-relay (--hubble-flows): find the relay pod and port-forward to it.\napiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: flightbay-hubble-flows\n  namespace: %s\nrules:\n  - apiGroups: [\"\"]\n    resources: [pods]\n    verbs: [get, list]\n  - apiGroups: [\"\"]\n    resources: [pods/portforward]\n    verbs: [create]\n", HubbleNamespace)
		fmt.Fprintf(&b, "---\napiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: flightbay-hubble-flows\n  namespace: %s\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: flightbay-hubble-flows\nsubjects:\n%s", HubbleNamespace, subject)
	}
	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			fmt.Fprintf(&b, "---\napiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: flightbay-reader\n  namespace: %s\nrules:\n", ns)
			renderRules(&b, nsRules, "get, list")
			renderRules(&b, pfRules, "create")
			fmt.Fprintf(&b, "---\napiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: flightbay-reader\n  namespace: %s\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: flightbay-reader\nsubjects:\n%s", ns, subject)
		}
		if len(clusterRules) > 0 {
			b.WriteString("---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: flightbay-cluster-reader\nrules:\n")
			renderRules(&b, clusterRules, "get, list")
			fmt.Fprintf(&b, "---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: flightbay-cluster-reader\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: flightbay-cluster-reader\nsubjects:\n%s", subject)
		}
		if nsRead {
			b.WriteString("---\n# Namespace objects of the namespaces in scope (labels such as istio.io/dataplane-mode).\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: flightbay-namespace-reader\nrules:\n  - apiGroups: [\"\"]\n    resources: [namespaces]\n    resourceNames:\n")
			for _, ns := range namespaces {
				fmt.Fprintf(&b, "    - %s\n", ns)
			}
			b.WriteString("    verbs: [get]\n")
			fmt.Fprintf(&b, "---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: flightbay-namespace-reader\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: flightbay-namespace-reader\nsubjects:\n%s", subject)
		}
		crdRole()
		hubbleRole()
		return b.String()
	}
	for g, rs := range clusterRules {
		for r := range rs {
			nsRules.add(g, r)
		}
	}
	if nsRead {
		nsRules.add("", "namespaces")
	}
	b.WriteString("---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: flightbay-reader\nrules:\n")
	renderRules(&b, nsRules, "get, list")
	renderRules(&b, pfRules, "create")
	fmt.Fprintf(&b, "---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: flightbay-reader\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: flightbay-reader\nsubjects:\n%s", subject)
	if len(crdNames) > 0 && !(nsRules["*"] != nil && nsRules["*"]["*"]) {
		crdRole()
	}
	hubbleRole()
	return b.String()
}
