// Package reach works out, for every agent in a snapshot, everything it can
// reach on the network and everything those destinations can reach on its
// behalf: the agent's real blast radius.
//
// The lesson it encodes is from the OpenAI and Hugging Face incident (see the
// zero-trust-agents-kubernetes field guide): the agents never broke out of
// their containers. They used a package registry they were allowed to call,
// which accepted writes and could fetch from the internet, so its reach became
// theirs. An agent is as strong as the weakest thing it can call.
//
// Everything here is computed from captured objects: Services and their pods,
// Kubernetes NetworkPolicies, Istio AuthorizationPolicies and waypoints,
// agentgateway routes, ServiceAccounts, RBAC, Secrets and PVCs, and the ztunnel
// access logs for what was actually called. Where a type was not captured the
// verdict is "unknown", never "allowed" or "denied" by assumption.
package reach

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

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
func strMap(v any) map[string]string {
	out := map[string]string{}
	for k, x := range m(v) {
		out[k] = s(x)
	}
	return out
}

// ---------- the cluster as reach sees it ----------

// workload is one controller (Deployment, StatefulSet, DaemonSet, Job) or a
// bare pod, with the labels and identity of its pods.
type workload struct {
	obj       *store.Object
	id        string // cluster/ns/kind/name
	ns, name  string
	kind      string
	labels    map[string]string // pod template labels
	spec      map[string]any    // pod spec
	sa        string
	pods      []*store.Object
	images    []string
	inMesh    bool
	sidecar   bool
	gateway   bool   // a waypoint or gateway proxy
	agentType string // set for agents
	agentWhy  string
	classes   []string // fetcher, store, credentials, dns, gateway, llm, mcp
	decl      *decls   // what it is configured to call, computed once
}

func (w *workload) principal() string { return "ns/" + w.ns + "/sa/" + w.sa }

// service is a Kubernetes Service and the workloads behind it.
type service struct {
	obj        *store.Object
	id         string
	ns, name   string
	selector   map[string]string
	ports      []svcPort
	backends   []*workload
	waypoint   string // istio.io/use-waypoint (service, else namespace)
	waypointNS string // istio.io/use-waypoint-namespace, else the service's namespace
	external   string // ExternalName
	exposed    bool   // LoadBalancer or NodePort
	classes    []string
	agwClass   string // set when this Service fronts an agentgateway Gateway
	gatewayNS  string
	gatewayNm  string
}

type svcPort struct {
	name        string
	port        int
	target      string
	appProtocol string
}

type cluster struct {
	ws         *store.Workspace
	name       string
	nsLabels   map[string]map[string]string
	nsKnown    bool
	works      []*workload
	byPod      map[string]*workload // ns/pod -> workload
	services   []*service
	svcByKey   map[string]*service // ns/name
	netpols    []*store.Object
	authz      []*store.Object
	peerAuth   []*store.Object
	npKnown    bool
	apKnown    bool
	rootNS     string
	platform   string // kind, eks, gke, aks, openshift or ""
	notes      []string
	ipx        *ipIndex
	enterprise bool // Enterprise agentgateway CRDs are in use
	agwPol     map[string][]map[string]any
	agwT       map[*service][]agwTarget
	seByHost   map[string]seInfo
}

func key(ns, name string) string { return ns + "/" + name }

func (c *cluster) list(group, resource string) []*store.Object {
	return c.ws.List(c.name, group, resource, "", nil)
}

// known reports whether a type was captured completely enough that the
// absence of an object means there is none (or the CRD is not installed).
func (c *cluster) known(group, resource, kind string) bool {
	st := c.ws.CollectionState(c.name, group, resource, kind, true)
	switch st.State {
	case store.StateComplete, store.StateTruncated:
		return true
	case store.StateNotCollected:
		name := resource
		if group != "" {
			name += "." + group
		}
		if _, ok := c.ws.FindResource(c.name, name); !ok {
			if pol := c.ws.Policy(); pol == nil || pol.Plan().Selected("objects") {
				return true // the CRD is not installed, so there are none
			}
		}
	}
	return false
}

func newCluster(ws *store.Workspace, name string) *cluster {
	c := &cluster{ws: ws, name: name, byPod: map[string]*workload{}, svcByKey: map[string]*service{}, nsLabels: map[string]map[string]string{}, rootNS: "istio-system"}
	c.nsKnown = true
	for _, n := range c.list("", "namespaces") {
		ann := strMap(path(n.Obj, "metadata", "annotations"))
		if ann[store.SynthesisedAnnotation] == "true" {
			c.nsKnown = false
		}
		lb := n.Labels()
		if lb["kubernetes.io/metadata.name"] == "" {
			lb["kubernetes.io/metadata.name"] = n.Name
		}
		c.nsLabels[n.Name] = lb
	}
	c.platform = detectPlatform(ws, name)
	c.buildWorkloads()
	c.buildServices()
	c.enterprise = len(c.list("enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends")) > 0 || len(c.list("enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies")) > 0
	for _, gw := range c.list("gateway.networking.k8s.io", "gateways") {
		if strings.Contains(s(path(gw.Obj, "spec", "gatewayClassName")), "enterprise-agentgateway") {
			c.enterprise = true
		}
	}
	// A Service that is an agent's HTTP proxy, or an ExternalName pointing
	// at a registry, fetches on request like any registry in the cluster.
	for _, sv := range c.services {
		if sv.external != "" {
			if cls, _ := classifyHost(sv.external); fetcherClass(cls) {
				sv.classes = union(sv.classes, []string{"fetcher"})
			}
		}
	}
	for _, w := range c.works {
		if w.gateway || strings.HasPrefix(w.ns, "kube-") {
			continue
		}
		if d := c.declarations(w); d.proxy != "" {
			if sv := c.svcByKey[d.proxy]; sv != nil {
				sv.classes = union(sv.classes, []string{"fetcher"})
			}
		}
		w.decl = nil // agent types are not known yet; computed again for agents
	}
	c.netpols = c.list("networking.k8s.io", "networkpolicies")
	c.npKnown = c.known("networking.k8s.io", "networkpolicies", "NetworkPolicy")
	c.authz = c.list("security.istio.io", "authorizationpolicies")
	c.peerAuth = c.list("security.istio.io", "peerauthentications")
	c.apKnown = c.known("security.istio.io", "authorizationpolicies", "AuthorizationPolicy")
	if !c.npKnown {
		c.notes = append(c.notes, "NetworkPolicies were not captured, so Kubernetes network rules could not be evaluated")
	}
	if !c.apKnown {
		c.notes = append(c.notes, "Istio AuthorizationPolicies were not captured, so mesh rules could not be evaluated")
	}
	for _, crd := range []struct{ group, res, name string }{
		{"cilium.io", "ciliumnetworkpolicies", "CiliumNetworkPolicy"}, {"cilium.io", "ciliumclusterwidenetworkpolicies", "CiliumClusterwideNetworkPolicy"},
		{"crd.projectcalico.org", "networkpolicies", "Calico NetworkPolicy"}, {"crd.projectcalico.org", "globalnetworkpolicies", "Calico GlobalNetworkPolicy"},
		{"policy.networking.k8s.io", "adminnetworkpolicies", "AdminNetworkPolicy"}, {"policy.networking.k8s.io", "baselineadminnetworkpolicies", "BaselineAdminNetworkPolicy"},
	} {
		if len(c.list(crd.group, crd.res)) > 0 {
			c.notes = append(c.notes, crd.name+" objects exist but are not evaluated yet, so reach may look wider than it really is")
		}
	}
	if !c.nsKnown {
		c.notes = append(c.notes, "namespace labels were not captured, so ambient enrolment and namespace selectors are guesses")
	}
	return c
}

func detectPlatform(ws *store.Workspace, cl string) string {
	v := strings.ToLower(string(ws.Version(cl)))
	switch {
	case strings.Contains(v, "-eks-"):
		return "eks"
	case strings.Contains(v, "-gke."):
		return "gke"
	case strings.Contains(v, "aks"):
		return "aks"
	}
	if strings.Contains(strings.ToLower(cl), "kind") {
		return "kind"
	}
	return ""
}

func podSpec(o *store.Object) map[string]any {
	switch o.Kind {
	case "Pod":
		return m(o.Obj["spec"])
	case "CronJob":
		return m(path(o.Obj, "spec", "jobTemplate", "spec", "template", "spec"))
	}
	return m(path(o.Obj, "spec", "template", "spec"))
}

func templateLabels(o *store.Object) map[string]string {
	if o.Kind == "Pod" {
		return o.Labels()
	}
	return strMap(path(o.Obj, "spec", "template", "metadata", "labels"))
}

func ownerOf(o *store.Object) (string, string) {
	var kind, name string
	for _, r := range l(path(o.Obj, "metadata", "ownerReferences")) {
		rm := m(r)
		if c, _ := rm["controller"].(bool); c || kind == "" {
			kind, name = s(rm["kind"]), s(rm["name"])
		}
	}
	return kind, name
}

// controllerOf follows Pod -> ReplicaSet -> Deployment and Job -> CronJob.
func (c *cluster) controllerOf(p *store.Object) (string, string) {
	k, n := ownerOf(p)
	switch k {
	case "ReplicaSet":
		if rs := c.ws.Get(c.name, "apps", "replicasets", p.Namespace, n); rs != nil {
			if k2, n2 := ownerOf(rs); k2 == "Deployment" {
				return k2, n2
			}
		} else if i := strings.LastIndex(n, "-"); i > 0 {
			return "Deployment", n[:i]
		}
	case "Job":
		if j := c.ws.Get(c.name, "batch", "jobs", p.Namespace, n); j != nil {
			if k2, n2 := ownerOf(j); k2 == "CronJob" {
				return k2, n2
			}
		}
	}
	return k, n
}

var resourceOfKind = map[string][2]string{
	"Deployment": {"apps", "deployments"}, "StatefulSet": {"apps", "statefulsets"}, "DaemonSet": {"apps", "daemonsets"},
	"Job": {"batch", "jobs"}, "CronJob": {"batch", "cronjobs"}, "ReplicaSet": {"apps", "replicasets"},
}

func (c *cluster) buildWorkloads() {
	byID := map[string]*workload{}
	for _, p := range c.list("", "pods") {
		if ph := s(path(p.Obj, "status", "phase")); ph == "Succeeded" || ph == "Failed" {
			continue
		}
		kind, name := c.controllerOf(p)
		var owner *store.Object
		if gr, ok := resourceOfKind[kind]; ok {
			owner = c.ws.Get(c.name, gr[0], gr[1], p.Namespace, name)
		}
		if kind == "" || kind == "Node" { // bare or static pod
			kind, name, owner = "Pod", p.Name, p
		}
		if owner == nil {
			owner = p
		}
		id := c.name + "/" + p.Namespace + "/" + kind + "/" + name
		w := byID[id]
		if w == nil {
			w = &workload{obj: owner, id: id, ns: p.Namespace, name: name, kind: kind, labels: p.Labels(), spec: m(p.Obj["spec"])}
			w.sa = s(w.spec["serviceAccountName"])
			if w.sa == "" {
				w.sa = "default"
			}
			for _, ct := range l(w.spec["containers"]) {
				w.images = append(w.images, s(m(ct)["image"]))
			}
			byID[id] = w
			c.works = append(c.works, w)
		}
		w.pods = append(w.pods, p)
		c.byPod[key(p.Namespace, p.Name)] = w
		ann := strMap(path(p.Obj, "metadata", "annotations"))
		if ann["ambient.istio.io/redirection"] == "enabled" {
			w.inMesh = true
		}
		if ann["sidecar.istio.io/status"] != "" {
			w.inMesh, w.sidecar = true, true
		}
	}
	for _, w := range c.works {
		lb := w.labels
		if lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["gateway.istio.io/managed"] != "" || lb["istio"] == "ingressgateway" || lb["istio"] == "eastwestgateway" {
			w.gateway = true
		}
		// Without the pod annotation (not yet redirected), fall back to labels.
		if !w.inMesh && lb["istio.io/dataplane-mode"] != "none" && !w.gateway && !hostNetwork(w) {
			if c.nsLabels[w.ns]["istio.io/dataplane-mode"] == "ambient" || lb["istio.io/dataplane-mode"] == "ambient" {
				w.inMesh = true
			}
		}
		w.classes = classify(w.name, w.images)
	}
	sort.Slice(c.works, func(i, j int) bool { return c.works[i].id < c.works[j].id })
}

func hostNetwork(w *workload) bool { b, _ := w.spec["hostNetwork"].(bool); return b }

func (c *cluster) buildServices() {
	gwClass := map[string]string{}
	for _, gw := range c.list("gateway.networking.k8s.io", "gateways") {
		gwClass[key(gw.Namespace, gw.Name)] = s(path(gw.Obj, "spec", "gatewayClassName"))
	}
	for _, o := range c.list("", "services") {
		sp := m(o.Obj["spec"])
		sv := &service{obj: o, id: c.name + "/" + o.Namespace + "/svc/" + o.Name, ns: o.Namespace, name: o.Name, selector: strMap(sp["selector"])}
		for _, p := range l(sp["ports"]) {
			pm := m(p)
			port, _ := pm["port"].(float64)
			tp := fmt.Sprint(pm["targetPort"])
			if pm["targetPort"] == nil {
				tp = strconv.Itoa(int(port))
			}
			if f, ok := pm["targetPort"].(float64); ok {
				tp = strconv.Itoa(int(f))
			}
			sv.ports = append(sv.ports, svcPort{name: s(pm["name"]), port: int(port), target: tp, appProtocol: s(pm["appProtocol"])})
		}
		t := s(sp["type"])
		sv.exposed = t == "LoadBalancer" || t == "NodePort"
		if t == "ExternalName" {
			sv.external = s(sp["externalName"])
		}
		lb := o.Labels()
		sv.waypoint = lb["istio.io/use-waypoint"]
		if sv.waypoint == "" {
			sv.waypoint = c.nsLabels[o.Namespace]["istio.io/use-waypoint"]
		}
		if sv.waypoint == "none" {
			sv.waypoint = ""
		}
		sv.waypointNS = lb["istio.io/use-waypoint-namespace"]
		if sv.waypointNS == "" && lb["istio.io/use-waypoint"] == "" {
			sv.waypointNS = c.nsLabels[o.Namespace]["istio.io/use-waypoint-namespace"]
		}
		if sv.waypointNS == "" {
			sv.waypointNS = o.Namespace
		}
		if gn := lb["gateway.networking.k8s.io/gateway-name"]; gn != "" {
			if cls := gwClass[key(o.Namespace, gn)]; strings.Contains(cls, "agentgateway") && !strings.Contains(cls, "waypoint") {
				sv.agwClass, sv.gatewayNS, sv.gatewayNm = cls, o.Namespace, gn
			}
		}
		if len(sv.selector) > 0 {
			seen := map[*workload]bool{}
			for _, w := range c.works {
				if w.ns != o.Namespace || seen[w] {
					continue
				}
				for _, p := range w.pods {
					if labelsMatch(p.Labels(), sv.selector) {
						seen[w] = true
						sv.backends = append(sv.backends, w)
						break
					}
				}
			}
		}
		sv.classes = classify(o.Name, nil)
		for _, b := range sv.backends {
			sv.classes = union(sv.classes, b.classes)
		}
		if sv.agwClass != "" {
			sv.classes = union(sv.classes, []string{"gateway"})
		}
		// A waypoint's own Service is not a destination: the waypoint only
		// forwards to the services bound to it, and those hops are evaluated
		// as agent -> service (L7 rules included) already.
		if strings.Contains(lb["gateway.networking.k8s.io/gateway-class-name"], "waypoint") || lb["gateway.istio.io/managed"] == "istio.io-mesh-controller" {
			sv.classes = union(sv.classes, []string{"waypoint"})
		}
		if o.Namespace == "default" && o.Name == "kubernetes" {
			sv.classes = union(sv.classes, []string{"apiserver"})
		}
		c.services = append(c.services, sv)
		c.svcByKey[key(o.Namespace, o.Name)] = sv
	}
	sort.Slice(c.services, func(i, j int) bool { return c.services[i].id < c.services[j].id })
}

// waypointFor returns the captured waypoint proxy a service uses, or nil.
func (c *cluster) waypointFor(sv *service) *workload {
	if sv.waypoint == "" {
		return nil
	}
	for _, w := range c.works {
		if w.ns == sv.waypointNS && w.labels["gateway.networking.k8s.io/gateway-name"] == sv.waypoint {
			return w
		}
	}
	return nil
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// selectorMatches evaluates a Kubernetes label selector (matchLabels and
// matchExpressions). An empty selector matches everything.
func selectorMatches(sel map[string]any, lb map[string]string) bool {
	for k, v := range m(sel["matchLabels"]) {
		if lb[k] != s(v) {
			return false
		}
	}
	for _, e := range l(sel["matchExpressions"]) {
		em := m(e)
		k := s(em["key"])
		val, has := lb[k]
		var vals []string
		for _, v := range l(em["values"]) {
			vals = append(vals, s(v))
		}
		in := false
		for _, v := range vals {
			if v == val {
				in = true
			}
		}
		switch s(em["operator"]) {
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

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ---------- what a destination is ----------

// Classes of destination that change what reaching it means. Matched on
// names and images; extend the lists as new products come up.
var (
	fetcherRe = regexp.MustCompile(`(?i)(artifactory|jfrog|nexus|harbor|verdaccio|devpi|pypiserver|pypi-proxy|npm-proxy|athens|goproxy|squid|tinyproxy|forward-proxy|egress-proxy|http-proxy|webhook-sender|outbound-webhook|mcp-fetch|server-fetch|url-fetch|scraper|crawler|playwright|puppeteer|selenium|browserless|firecrawl|searxng|url-preview|unfurl|litellm|registry-proxy|pull-through)`)
	storeRe   = regexp.MustCompile(`(?i)(redis|valkey|postgres|mysql|mariadb|mongo|minio|seaweedfs|clickhouse|kafka|rabbitmq|nats|elasticsearch|opensearch|qdrant|weaviate|milvus|chroma|pgvector|memcached|cassandra|etcd|(^|[-_ /])registry([-_: ]|$)|docker-registry|zot|agentregistry-(enterprise-)?server)`)
	credRe    = regexp.MustCompile(`(?i)(vault|keycloak|dex|hydra|external-secrets|secrets-store|spire|cert-manager)`)
	dnsRe     = regexp.MustCompile(`(?i)^(kube-dns|coredns|dns-default|node-local-dns)`)
	mcpRe     = regexp.MustCompile(`(?i)(^|[-_/])mcp([-_/]|$)|mcp-server|mcpserver`)
	llmRe     = regexp.MustCompile(`(?i)(ollama|vllm|llm|model|tgi|triton|inference)`)
	agentRe   = regexp.MustCompile(`(?i)(agent|langgraph|langchain|crewai|autogen|openclaw|openhands|swe-|codex|claude-code|harness)`)
	// Workloads whose names contain "agent" but are infrastructure, not AI agents.
	infraAgentRe = regexp.MustCompile(`(?i)(agentgateway|gloo-mesh-agent|gloo-agent|datadog|dd-agent|otel|opentelemetry|fluent|newrelic|dynatrace|elastic-agent|node-agent|kube-|ext-auth|rate-limit|waf-server|ext-cache|spire-agent|cilium|calico|consul|vault-agent|argocd)`)
)

func classify(name string, images []string) []string {
	var out []string
	text := name + " " + strings.Join(images, " ")
	// Image paths contain registry hosts (e.g. registry.example.com); match
	// the image's last path element for stores to avoid tagging everything.
	short := name
	for _, im := range images {
		base := im
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		short += " " + base
	}
	if fetcherRe.MatchString(text) {
		out = append(out, "fetcher")
	}
	if storeRe.MatchString(short) {
		out = append(out, "store")
	}
	if credRe.MatchString(short) {
		out = append(out, "credentials")
	}
	if dnsRe.MatchString(name) {
		out = append(out, "dns")
	}
	if mcpRe.MatchString(short) {
		out = append(out, "mcp")
	}
	if llmRe.MatchString(short) && !agentRe.MatchString(name) {
		out = append(out, "llm")
	}
	return out
}

// publicIP and metadataIP are the addresses used to evaluate internet and
// cloud metadata egress against ipBlock rules.
var (
	publicIP   = net.ParseIP("203.0.113.10") // TEST-NET-3: stands for "anywhere on the internet"
	metadataIP = net.ParseIP("169.254.169.254")
)
