package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/sync/errgroup"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
)

// PortForwarder opens port-forward sessions to pods, which is the only way to
// reach the localhost admin ports of Envoy, ztunnel and agentgateway.
type PortForwarder interface {
	Open(ctx context.Context, ns, pod string, ports []int) (ForwardSession, error)
}

// ForwardSession reads HTTP paths over an open port-forward.
type ForwardSession interface {
	// Get returns at most max bytes of the response body; truncated is set
	// when the body was longer.
	Get(ctx context.Context, port int, path string, max int64) (body []byte, truncated bool, err error)
	Close()
}

// resRef names a resource type whose policy decides whether an endpoint, or a
// part of its output, may be collected.
type resRef struct{ group, resource, kind string }

var (
	refPods      = resRef{"", "pods", "Pod"}
	refServices  = resRef{"", "services", "Service"}
	refSecrets   = resRef{"", "secrets", "Secret"}
	refAuthz     = resRef{"security.istio.io", "authorizationpolicies", "AuthorizationPolicy"}
	refEndpoints = resRef{"discovery.k8s.io", "endpointslices", "EndpointSlice"}
)

type endpoint struct {
	cat      policy.Category
	port     int
	path     string
	file     string
	json     bool
	envoy    bool              // drop SDS secret sections
	lines    bool              // line-oriented text (Prometheus, Envoy stats)
	needs    []resRef          // skip the endpoint if any is excluded
	strip    map[string]resRef // drop a top-level key when its resource is excluded
	perEntry bool              // istiod configz: each entry is a Kubernetes object checked against the policy
	nsKeyed  bool              // istiod endpointz: {hostname: {namespace: ...}}, second-level keys are namespaces
}

type component struct {
	name      string // ztunnel, envoy, agentgateway, istiod, controller
	kind      string // proxy or control-plane (bundle directory)
	container string // the container this matched
	endpoints []endpoint
	notes     []string // recorded in coverage: what is deliberately not collected
}

// imageName returns the repository basename: us-docker.pkg.dev/x/istio/pilot:1.30 -> pilot.
func imageName(image string) string {
	if i := strings.IndexAny(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	if i := strings.Index(image, ":"); i >= 0 {
		image = image[:i]
	}
	return image
}

// imageTag returns the tag, or "" for digests and untagged images.
func imageTag(image string) string {
	if strings.Contains(image, "@") {
		return ""
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return ""
}

var envoyEndpoints = []endpoint{
	{cat: policy.CatProxies, port: 15000, path: "/config_dump?include_eds", file: "config_dump.json", json: true, envoy: true, needs: []resRef{refServices}},
	{cat: policy.CatProxies, port: 15000, path: "/clusters?format=json", file: "clusters.json", json: true, needs: []resRef{refServices}},
	{cat: policy.CatProxies, port: 15000, path: "/listeners?format=json", file: "listeners.json", json: true},
	{cat: policy.CatProxies, port: 15000, path: "/server_info", file: "server_info.json", json: true},
	{cat: policy.CatProxies, port: 15000, path: "/certs", file: "certs.json", json: true},
	{cat: policy.CatProxies, port: 15000, path: "/stats", file: "stats.txt", lines: true},
	{cat: policy.CatMetrics, port: 15020, path: "/stats/prometheus", file: "metrics.prom", lines: true},
}

// detect classifies a pod's containers into collectable Solo components.
func detect(p podInfo) []component {
	var out []component
	for _, ct := range p.containers {
		if ct.init || strings.HasPrefix(ct.name, "ephemeral:") {
			continue
		}
		img := imageName(ct.image)
		switch {
		case img == "ztunnel":
			out = append(out, component{name: "ztunnel", kind: "proxies", container: ct.name, endpoints: []endpoint{
				{cat: policy.CatProxies, port: 15000, path: "/config_dump", file: "config_dump.json", json: true,
					strip: map[string]resRef{"workloads": refPods, "services": refServices, "policies": refAuthz}},
				{cat: policy.CatMetrics, port: 15020, path: "/metrics", file: "metrics.prom", lines: true},
			}})
		case img == "proxyv2" || (ct.name == "istio-proxy" && strings.Contains(img, "proxy")):
			out = append(out, component{name: "envoy", kind: "proxies", container: ct.name, endpoints: envoyEndpoints})
		case img == "agentgateway" || img == "agentgateway-enterprise":
			out = append(out, component{name: "agentgateway", kind: "proxies", container: ct.name, endpoints: []endpoint{
				{cat: policy.CatProxies, port: 15000, path: "/config_dump", file: "config_dump.json", json: true,
					strip: map[string]resRef{"workloads": refPods, "services": refServices}},
				{cat: policy.CatMetrics, port: 15020, path: "/metrics", file: "metrics.prom", lines: true},
			}})
		case img == "pilot":
			out = append(out, component{name: "istiod", kind: "control-plane", container: ct.name, endpoints: []endpoint{
				{cat: policy.CatControlPlane, port: 8080, path: "/debug/syncz", file: "syncz.json", json: true},
				{cat: policy.CatControlPlane, port: 8080, path: "/debug/configz", file: "configz.json", json: true, perEntry: true},
				{cat: policy.CatControlPlane, port: 8080, path: "/debug/registryz", file: "registryz.json", json: true, needs: []resRef{refServices}},
				{cat: policy.CatControlPlane, port: 8080, path: "/debug/endpointz", file: "endpointz.json", json: true, nsKeyed: true, needs: []resRef{refServices, refEndpoints}},
				{cat: policy.CatControlPlane, port: 8080, path: "/debug/mesh", file: "mesh.json", json: true},
				{cat: policy.CatControlPlane, port: 8080, path: "/debug/connections", file: "connections.json", json: true},
				{cat: policy.CatMetrics, port: 15014, path: "/metrics", file: "metrics.prom", lines: true},
			}})
		case strings.HasSuffix(img, "agentgateway-controller") || img == "kgateway" || strings.HasSuffix(img, "kgateway-controller"):
			out = append(out, component{name: "controller", kind: "control-plane", container: ct.name, endpoints: []endpoint{
				{cat: policy.CatMetrics, port: 9092, path: "/metrics", file: "metrics.prom", lines: true},
			}, notes: []string{"the admin krt snapshot is not collected: it spans every namespace and can include Secret contents, so it cannot honour the collection policy"}})
		}
	}
	return out
}

// DiagMeta is written next to each component's files as meta.json.
type DiagMeta struct {
	Component string `json:"component"`
	Container string `json:"container"`
	Image     string `json:"image"`
	Version   string `json:"version,omitempty"`
	Node      string `json:"node,omitempty"`
}

// SoloSummary is solo/summary.json: which Solo components the collector saw.
type SoloSummary struct {
	Components []SummaryComponent `json:"components"`
}

// SummaryComponent is one detected component.
type SummaryComponent struct {
	Component string   `json:"component"`
	Image     string   `json:"image"`
	Version   string   `json:"version"`
	Pods      []string `json:"pods"`
}

func (c *Collector) diagSelected() bool {
	pl := c.Policy.Plan()
	return pl.Selected(policy.CatProxies) || pl.Selected(policy.CatControlPlane) || pl.Selected(policy.CatMetrics)
}

func (c *Collector) refAllowed(r resRef) bool {
	if r.group == "" && r.resource == "secrets" {
		return c.Policy.Secrets != "omit" && c.Policy.ResourceAllowed(r.resource, r.group, r.kind, true)
	}
	// Diagnostic output is judged on what the resource contains, so the
	// cluster-scoped opt-in does not apply here: everything named is namespaced.
	return c.Policy.ResourceAllowed(r.resource, r.group, r.kind, true)
}

// collectDiagnostics reads proxy dumps, control-plane debug output and
// metrics from the Solo components among the selected pods.
func (c *Collector) collectDiagnostics(ctx context.Context, cluster string, cl Clients, pods []podInfo, kinds map[string]string) error {
	summary := map[string]*SummaryComponent{}
	var work []diagJob
	for _, p := range pods {
		comps := detect(p)
		if len(comps) == 0 {
			continue
		}
		for _, comp := range comps {
			img := p.images[comp.container]
			key := comp.name + " " + img
			if summary[key] == nil {
				summary[key] = &SummaryComponent{Component: comp.name, Image: img, Version: imageTag(img)}
			}
			summary[key].Pods = append(summary[key].Pods, p.ns+"/"+p.name)
		}
		work = append(work, diagJob{p, comps})
	}
	// Pods are read diagWorkers at a time: each is a port-forward through the
	// API server to its kubelet, and a proxy dump can be tens of MB, so a
	// cluster with a ztunnel on every node would otherwise take one node at a time.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.diagWorkers())
	for _, j := range work {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error { return c.diagPod(gctx, cluster, cl, j.p, j.comps, kinds) })
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(summary) > 0 {
		var s SoloSummary
		for _, v := range summary {
			sort.Strings(v.Pods)
			s.Components = append(s.Components, *v)
		}
		sort.Slice(s.Components, func(i, j int) bool {
			if s.Components[i].Component != s.Components[j].Component {
				return s.Components[i].Component < s.Components[j].Component
			}
			return s.Components[i].Image < s.Components[j].Image
		})
		if err := c.Writer.AddJSON(bundle.SoloPath(cluster, "", "", "", "summary.json"), s); err != nil {
			return err
		}
	}
	return nil
}

type diagJob struct {
	p     podInfo
	comps []component
}

// diagWorkers is how many pods' diagnostics are read at once.
func (c *Collector) diagWorkers() int { return min(4, c.listWorkers()) }

// diagPod reads one pod's diagnostic endpoints through one port-forward.
func (c *Collector) diagPod(ctx context.Context, cluster string, cl Clients, p podInfo, comps []component, kinds map[string]string) error {
	pl := c.Policy.Plan()
	if cl.Forward == nil || !p.running {
		for _, comp := range comps {
			reason := "pod is not running"
			if cl.Forward == nil {
				reason = "port-forward is not available"
			}
			for _, ep := range comp.endpoints {
				if pl.Selected(ep.cat) {
					_ = c.diagCoverage(cluster, p, comp, ep, bundle.StateFailed, reason, "", 0)
				}
			}
		}
		return nil
	}
	var ports []int
	seen := map[int]bool{}
	for _, comp := range comps {
		for _, ep := range comp.endpoints {
			if pl.Selected(ep.cat) && !seen[ep.port] {
				seen[ep.port] = true
				ports = append(ports, ep.port)
			}
		}
	}
	if len(ports) == 0 {
		return nil
	}
	sort.Ints(ports)
	c.progress("%s: diagnostics from %s/%s", cluster, p.ns, p.name)
	sess, err := cl.Forward.Open(ctx, p.ns, p.name, ports)
	if err != nil {
		c.record(LogEntry{Cluster: cluster, Operation: "port-forward", Target: p.name, Namespace: p.ns, Result: "failed", Error: err.Error()})
		for _, comp := range comps {
			for _, ep := range comp.endpoints {
				if pl.Selected(ep.cat) {
					_ = c.diagCoverage(cluster, p, comp, ep, bundle.StateFailed, "port-forward failed: "+err.Error(), "", 0)
				}
			}
		}
		return nil
	}
	for _, comp := range comps {
		dir := bundle.SoloPath(cluster, comp.kind, p.ns, p.name, "")
		_ = c.Writer.AddJSON(dir+"meta.json", DiagMeta{Component: comp.name, Container: comp.container, Image: p.images[comp.container], Version: imageTag(p.images[comp.container]), Node: p.node})
		for _, ep := range comp.endpoints {
			if err := ctx.Err(); err != nil {
				sess.Close()
				return err
			}
			if !pl.Selected(ep.cat) {
				continue
			}
			if err := c.collectEndpoint(ctx, cluster, sess, p, comp, ep, kinds); err != nil {
				sess.Close()
				return err
			}
		}
	}
	sess.Close()
	return nil
}

func (c *Collector) diagCoverage(cluster string, p podInfo, comp component, ep endpoint, state, reason, member string, items int) error {
	cov := bundle.Coverage{ID: fmt.Sprintf("%s.%s.%s.%s.%s", cluster, comp.kind, p.ns, p.name, strings.TrimSuffix(ep.file, ".json")),
		Cluster: cluster, Type: bundle.CoverageDiagnostic, Namespace: p.ns, Pod: p.name, Container: comp.name, Resource: ep.file,
		Kind: string(ep.cat), State: state, Reason: reason, Items: items}
	if member != "" {
		cov.Chunks = []string{member}
	}
	if len(comp.notes) > 0 && cov.Reason == "" {
		cov.Reason = strings.Join(comp.notes, "; ")
	}
	if state == bundle.StateFailed {
		c.record(LogEntry{Cluster: cluster, Operation: "diagnostic", Target: p.name + " " + ep.path, Namespace: p.ns, Result: "failed", Error: reason})
	}
	return c.Writer.Coverage(cov)
}

func (c *Collector) collectEndpoint(ctx context.Context, cluster string, sess ForwardSession, p podInfo, comp component, ep endpoint, kinds map[string]string) error {
	for _, r := range ep.needs {
		if !c.refAllowed(r) {
			return c.diagCoverage(cluster, p, comp, ep, bundle.StateExcluded, fmt.Sprintf("not collected: it exposes %s, which the policy excludes", r.resource), "", 0)
		}
	}
	if c.overBudget() {
		return c.diagCoverage(cluster, p, comp, ep, bundle.StateTruncated, "bundle size budget reached before this collection", "", 0)
	}
	body, truncated, err := sess.Get(ctx, ep.port, ep.path, c.MaxDiagBytes)
	if err != nil {
		return c.diagCoverage(cluster, p, comp, ep, bundle.StateFailed, err.Error(), "", 0)
	}
	loc := bundle.SoloPath(cluster, comp.kind, p.ns, p.name, ep.file)
	filter := &nsFilter{allowed: c.Policy.NamespaceAllowed}
	scoped := len(c.Policy.Namespaces) > 0 || len(c.Policy.ExcludeNamespaces) > 0
	var notes []string
	var out []byte
	switch {
	case ep.json:
		if truncated {
			// A cut JSON document cannot be filtered or redacted safely.
			return c.diagCoverage(cluster, p, comp, ep, bundle.StateTruncated,
				fmt.Sprintf("not kept: larger than the %d byte limit for one diagnostic", c.MaxDiagBytes), "", 0)
		}
		var doc any
		if err := json.Unmarshal(body, &doc); err != nil {
			return c.diagCoverage(cluster, p, comp, ep, bundle.StateFailed, "response was not JSON: "+firstLine(body), "", 0)
		}
		if m, ok := doc.(map[string]any); ok {
			for key, r := range ep.strip {
				if _, present := m[key]; present && !c.refAllowed(r) {
					delete(m, key)
					notes = append(notes, fmt.Sprintf("%s removed (%s excluded)", key, r.resource))
				}
			}
		}
		if ep.envoy {
			if n := stripEnvoySecrets(doc); n > 0 {
				notes = append(notes, "SDS secret sections removed")
			}
		}
		if ep.perEntry {
			var n int
			doc, n = c.filterObjects(doc, kinds)
			if n > 0 {
				notes = append(notes, fmt.Sprintf("%d objects outside the resource selection removed", n))
			}
		}
		if scoped && ep.nsKeyed {
			doc = filter.nsKeyed(doc)
		}
		if scoped {
			doc = filter.Root(doc)
		}
		doc = c.Redactor.Object(loc, map[string]any{"v": doc})["v"]
		if out, err = json.MarshalIndent(doc, "", " "); err != nil {
			return err
		}
	default:
		text := string(body)
		if scoped {
			text = filter.Lines(text)
		}
		lr := c.Redactor.Lines(loc)
		var b strings.Builder
		for _, line := range strings.SplitAfter(text, "\n") {
			if line == "" {
				continue
			}
			nl := strings.HasSuffix(line, "\n")
			b.WriteString(lr.Line(strings.TrimSuffix(line, "\n")))
			if nl {
				b.WriteString("\n")
			}
		}
		out = []byte(b.String())
	}
	if filter.removed > 0 {
		notes = append(notes, fmt.Sprintf("%d entries naming namespaces outside the selection removed", filter.removed))
	}
	if err := c.Writer.Add(loc, out); err != nil {
		return err
	}
	state := bundle.StateComplete
	if truncated {
		state = bundle.StateTruncated
		notes = append(notes, fmt.Sprintf("kept the first %d bytes", c.MaxDiagBytes))
	}
	return c.diagCoverage(cluster, p, comp, ep, state, strings.Join(notes, "; "), loc, 1)
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// stripEnvoySecrets removes the SecretsConfigDump from an Envoy config_dump.
// Certificate details for expiry checks come from /certs, which has no keys.
func stripEnvoySecrets(doc any) int {
	m, ok := doc.(map[string]any)
	if !ok {
		return 0
	}
	cfgs, ok := m["configs"].([]any)
	if !ok {
		return 0
	}
	kept := cfgs[:0]
	n := 0
	for _, c := range cfgs {
		cm, _ := c.(map[string]any)
		if t, _ := cm["@type"].(string); strings.HasSuffix(t, "SecretsConfigDump") {
			n++
			continue
		}
		kept = append(kept, c)
	}
	m["configs"] = kept
	return n
}

// filterObjects applies the resource selection to istiod configz, which is a
// list of Kubernetes objects. kinds maps "group/Kind" to the resource name.
func (c *Collector) filterObjects(doc any, kinds map[string]string) (any, int) {
	list, ok := doc.([]any)
	if !ok {
		return doc, 0
	}
	out := make([]any, 0, len(list))
	removed := 0
	for _, el := range list {
		o, _ := el.(map[string]any)
		kind, _ := o["kind"].(string)
		apiVersion, _ := o["apiVersion"].(string)
		group := ""
		if i := strings.Index(apiVersion, "/"); i >= 0 {
			group = apiVersion[:i]
		}
		resource := kinds[group+"/"+kind]
		if resource == "" {
			resource = strings.ToLower(kind) + "s"
		}
		md, _ := o["metadata"].(map[string]any)
		ns, _ := md["namespace"].(string)
		// Cluster-scoped objects (GatewayClass, MeshConfig-like kinds) need the
		// cluster-scoped opt-in, as they would when listed directly.
		if !c.Policy.ResourceAllowed(resource, group, kind, true) || (ns != "" && !c.Policy.NamespaceAllowed(ns)) || (ns == "" && !c.Policy.ClusterScoped) {
			removed++
			continue
		}
		out = append(out, el)
	}
	return out, removed
}
