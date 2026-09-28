package reach

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// ---------- output ----------

// Ref points at a captured object.
type Ref struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func refOf(o *store.Object) *Ref {
	if o == nil {
		return nil
	}
	return &Ref{Cluster: o.Cluster, Group: o.Group, Resource: o.Resource, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
}

// Identity is what an agent carries without anyone deciding to give it.
type Identity struct {
	ServiceAccount string   `json:"serviceAccount"`
	Principal      string   `json:"principal"`
	InMesh         bool     `json:"inMesh"`
	TokenMounted   bool     `json:"tokenMounted"`
	RBAC           []string `json:"rbac"`      // rules granted to the service account
	Dangerous      []string `json:"dangerous"` // escalation paths among them
	RBACComplete   bool     `json:"rbacComplete"`
	CloudRole      string   `json:"cloudRole,omitempty"`
	Secrets        []string `json:"secrets"`
	HostAccess     []string `json:"hostAccess"`
	Sandbox        string   `json:"sandbox,omitempty"` // runtimeClassName when it is a sandbox
	SharedVolumes  []string `json:"sharedVolumes"`
}

// Edge is one reachable destination.
type Edge struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Verdict    string   `json:"verdict"`
	Layer      string   `json:"layer"`
	Methods    []string `json:"methods"` // null: every method
	Paths      []string `json:"paths,omitempty"`
	Write      bool     `json:"write"` // the caller can change data there
	Via        string   `json:"via,omitempty"`
	Reasons    []string `json:"reasons"`
	Conditions []string `json:"conditions,omitempty"`
	Observed   int      `json:"observed"` // requests seen in the flows (the most any one source saw)
	ObsMethods []string `json:"observedMethods,omitempty"`
	ObsPaths   []string `json:"observedPaths,omitempty"`
	ObsDenied  int      `json:"observedDenied,omitempty"` // of those, denied
	Tools      []string `json:"tools,omitempty"`          // MCP tools called
	ObservedBy []string `json:"observedBy,omitempty"`     // flow sources that saw it: ztunnel, agentgateway, hubble...
	Declared   bool     `json:"declared"`                 // configured as a dependency (env URL, ModelConfig, tool server)
	DeclaredIn []string `json:"declaredIn,omitempty"`     // where it is configured
	Risk       string   `json:"risk"`                     // red, amber, green, grey
	Why        string   `json:"why"`
	// Evidence is the flow records that show the traffic (at most
	// maxEvidence, largest first), so a reader can see how it was found.
	Evidence []store.Flow `json:"evidence,omitempty"`
}

// Node is a destination in the graph.
type Node struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"` // agent, service, group, internet, metadata, external
	Label     string   `json:"label"`
	Namespace string   `json:"namespace,omitempty"`
	Classes   []string `json:"classes,omitempty"`
	Depth     int      `json:"depth"`
	Risk      string   `json:"risk"`
	Count     int      `json:"count,omitempty"` // for group nodes
	Ref       *Ref     `json:"ref,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Via       string   `json:"via,omitempty"` // external hosts: how it is reached
}

// Path is a chain from the agent to something that matters.
type Path struct {
	Hops   []string `json:"hops"` // labels, agent first
	Target string   `json:"target"`
	Kind   string   `json:"kind"` // internet, metadata, fetcher, store, credentials
	Why    string   `json:"why"`
}

// Finding is one thing to lock down.
type Finding struct {
	Severity string        `json:"severity"` // critical, high, medium, low
	Title    string        `json:"title"`
	Detail   string        `json:"detail"` // one or two sentences
	Items    []FindingItem `json:"items,omitempty"`
	Fix      string        `json:"fix,omitempty"`
	Advice   []string      `json:"advice,omitempty"` // steps that are not YAML
	Source   string        `json:"source"`
}

// FindingItem is one affected destination or object, for a table rather than prose.
type FindingItem struct {
	Target string   `json:"target"`           // ns/name, or a host
	Class  string   `json:"class,omitempty"`  // data store, credential service, fetches from elsewhere
	Access string   `json:"access,omitempty"` // methods allowed
	Others []string `json:"others,omitempty"` // other agents with the same access
	Note   string   `json:"note,omitempty"`
}

// Agent is the blast radius of one agent-like workload.
type Agent struct {
	ID         string     `json:"id"`
	Cluster    string     `json:"cluster"`
	Namespace  string     `json:"namespace"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Type       string     `json:"type"` // kagent-agent, mcp-server, agentgateway, possible-agent
	Why        string     `json:"why"`
	Ref        *Ref       `json:"ref"`
	Identity   Identity   `json:"identity"`
	Summary    string     `json:"summary"`
	Radius     string     `json:"radius"` // contained, narrow, wide, open
	Internet   string     `json:"internet"`
	Metadata   string     `json:"metadata"`
	Direct     []Edge     `json:"direct"`
	Paths      []Path     `json:"paths"`
	Findings   []Finding  `json:"findings"`
	Nodes      []Node     `json:"nodes"`
	Edges      []Edge     `json:"edges"`
	Counts     Counts     `json:"counts"`
	NeverUsed  []string   `json:"neverUsed"`
	Observed   bool       `json:"observed"`   // some flow source covers this agent, so "never used" means something
	ObservedBy []string   `json:"observedBy"` // the flow sources that cover it
	External   []External `json:"external"`
}

// Counts summarise an agent's reach.
type Counts struct {
	Services  int `json:"services"`  // Services in the capture
	Direct    int `json:"direct"`    // reachable in one hop
	Closure   int `json:"closure"`   // reachable in any number of hops
	Write     int `json:"write"`     // direct destinations it can change data on
	Observed  int `json:"observed"`  // destinations it actually called
	NeverUsed int `json:"neverUsed"` // allowed, not declared, never called
	External  int `json:"external"`  // destinations outside the cluster (not counting image pulls)
	Critical  int `json:"critical"`
	High      int `json:"high"`
}

// Report is the result for a whole snapshot.
type Report struct {
	Agents   []Agent  `json:"agents"`
	Notes    []string `json:"notes"`
	Flat     bool     `json:"flat"` // no NetworkPolicy and no AuthorizationPolicy anywhere
	Clusters []string `json:"clusters"`
}

const guide = "https://mastertheagent.com/solo/zero-trust-agents-kubernetes/"

// ---------- agents ----------

var notAgentRe = regexp.MustCompile(`(?i)(controller|operator|manager|waypoint|registry|postgres|redis|-ui$|^ui-|webhook|metrics|telemetry|collector|keycloak)`)

func (c *cluster) findAgents() []*workload {
	agentDeploy := map[string]string{} // ns/name -> Agent name
	for _, a := range c.list("kagent.dev", "agents") {
		agentDeploy[key(a.Namespace, a.Name)] = a.Name
	}
	mcpNames := map[string]bool{}
	for _, o := range c.list("kagent.dev", "mcpservers") {
		mcpNames[key(o.Namespace, o.Name)] = true
	}
	mcpHosts := map[string]bool{}
	for _, o := range c.list("kagent.dev", "remotemcpservers") {
		if u, err := url.Parse(s(path(o.Obj, "spec", "url"))); err == nil {
			mcpHosts[svcKeyFromHost(u.Hostname(), o.Namespace)] = true
		}
	}
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}} {
		for _, b := range c.list(g.group, g.res) {
			sp := m(b.Obj["spec"])
			mc := m(sp["mcp"])
			if mc == nil {
				mc = m(sp["entMcp"])
			}
			for _, t := range l(mc["targets"]) {
				st := m(m(t)["static"])
				if h := s(st["host"]); h != "" {
					mcpHosts[svcKeyFromHost(h, b.Namespace)] = true
				}
				if br := m(st["backendRef"]); br != nil {
					ns := s(br["namespace"])
					if ns == "" {
						ns = b.Namespace
					}
					mcpHosts[key(ns, s(br["name"]))] = true
				}
			}
		}
	}
	for _, sv := range c.services {
		for _, p := range sv.ports {
			if strings.Contains(p.appProtocol, "/mcp") || strings.Contains(p.appProtocol, "/a2a") {
				mcpHosts[key(sv.ns, sv.name)] = true
			}
		}
	}
	mcpWork := map[*workload]bool{}
	agwWork := map[*workload]bool{}
	for _, sv := range c.services {
		if mcpHosts[key(sv.ns, sv.name)] {
			for _, b := range sv.backends {
				mcpWork[b] = true
			}
		}
		if sv.agwClass != "" {
			for _, b := range sv.backends {
				agwWork[b] = true
			}
		}
	}
	var out []*workload
	for _, w := range c.works {
		ok, _ := ownerKind(w.obj, "Agent")
		switch {
		case ok || agentDeploy[key(w.ns, w.name)] != "" && !w.gateway:
			w.agentType, w.agentWhy = "kagent-agent", "kagent Agent "+w.ns+"/"+w.name
		case agwWork[w]:
			w.agentType, w.agentWhy = "agentgateway", "agentgateway data plane for Gateway "+w.name
		case mcpNames[key(w.ns, w.name)] || mcpWork[w]:
			w.agentType, w.agentWhy = "mcp-server", "MCP server (a gateway backend, tool server or MCPServer)"
		case w.gateway || strings.HasPrefix(w.ns, "kube-") || w.ns == "istio-system":
			continue
		case has(w.classes, "mcp") && !notAgentRe.MatchString(w.name) && !infraAgentRe.MatchString(w.name):
			w.agentType, w.agentWhy = "mcp-server", "name or image looks like an MCP server"
		case agentRe.MatchString(w.name+" "+strings.Join(w.images, " ")) && !notAgentRe.MatchString(w.name) && !infraAgentRe.MatchString(w.name+" "+strings.Join(w.images, " ")):
			w.agentType, w.agentWhy = "possible-agent", "name or image looks like an agent; confirm"
		default:
			continue
		}
		out = append(out, w)
	}
	return out
}

func ownerKind(o *store.Object, kind string) (bool, string) {
	for _, r := range l(path(o.Obj, "metadata", "ownerReferences")) {
		if s(m(r)["kind"]) == kind {
			return true, s(m(r)["name"])
		}
	}
	return false, ""
}

// svcKeyFromHost turns name, name.ns, name.ns.svc[.cluster.local] into ns/name.
func svcKeyFromHost(host, defNS string) string {
	host = strings.TrimSuffix(strings.TrimSuffix(host, "."), ".cluster.local")
	host = strings.TrimSuffix(host, ".svc")
	parts := strings.Split(host, ".")
	switch len(parts) {
	case 1:
		return key(defNS, parts[0])
	case 2:
		return key(parts[1], parts[0])
	}
	return "" // external
}

// ---------- identity ----------

func (c *cluster) identity(w *workload) Identity {
	id := Identity{ServiceAccount: w.ns + "/" + w.sa, Principal: w.principal(), InMesh: w.inMesh, RBAC: []string{}, Dangerous: []string{}, Secrets: []string{}, HostAccess: []string{}, SharedVolumes: []string{}}
	sa := c.ws.Get(c.name, "", "serviceaccounts", w.ns, w.sa)
	auto := true
	if b, ok := w.spec["automountServiceAccountToken"].(bool); ok {
		auto = b
	} else if sa != nil {
		if b, ok := sa.Obj["automountServiceAccountToken"].(bool); ok {
			auto = b
		}
	}
	id.TokenMounted = auto
	if sa != nil {
		ann := strMap(path(sa.Obj, "metadata", "annotations"))
		for _, k := range []string{"eks.amazonaws.com/role-arn", "iam.gke.io/gcp-service-account", "azure.workload.identity/client-id"} {
			if ann[k] != "" {
				id.CloudRole = k + "=" + ann[k]
			}
		}
	}
	// Secrets it can read: env, envFrom and volumes.
	seen := map[string]bool{}
	addSecret := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			id.Secrets = append(id.Secrets, n)
		}
	}
	for _, ct := range append(l(w.spec["containers"]), l(w.spec["initContainers"])...) {
		for _, e := range l(m(ct)["env"]) {
			addSecret(s(path(e, "valueFrom", "secretKeyRef", "name")))
		}
		for _, e := range l(m(ct)["envFrom"]) {
			addSecret(s(path(e, "secretRef", "name")))
		}
		sc := m(m(ct)["securityContext"])
		if b, _ := sc["privileged"].(bool); b {
			id.HostAccess = append(id.HostAccess, "privileged container "+s(m(ct)["name"]))
		}
	}
	for _, v := range l(w.spec["volumes"]) {
		vm := m(v)
		addSecret(s(path(vm, "secret", "secretName")))
		for _, src := range l(path(vm, "projected", "sources")) {
			addSecret(s(path(src, "secret", "name")))
		}
		if hp := s(path(vm, "hostPath", "path")); hp != "" {
			id.HostAccess = append(id.HostAccess, "hostPath "+hp)
		}
	}
	sort.Strings(id.Secrets)
	for _, k := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if b, _ := w.spec[k].(bool); b {
			id.HostAccess = append(id.HostAccess, k)
		}
	}
	if rc := s(w.spec["runtimeClassName"]); regexp.MustCompile(`(?i)gvisor|runsc|kata|firecracker|microvm|sandbox`).MatchString(rc) {
		id.Sandbox = rc
	}
	id.RBAC, id.Dangerous, id.RBACComplete = c.rbacFor(w)
	return id
}

func (c *cluster) rbacFor(w *workload) (rules, dangerous []string, complete bool) {
	rules, dangerous = []string{}, []string{}
	complete = c.known("rbac.authorization.k8s.io", "clusterrolebindings", "ClusterRoleBinding")
	subjectOK := func(b *store.Object) bool {
		for _, sj := range l(b.Obj["subjects"]) {
			sm := m(sj)
			switch s(sm["kind"]) {
			case "ServiceAccount":
				ns := s(sm["namespace"])
				if ns == "" {
					ns = b.Namespace
				}
				if s(sm["name"]) == w.sa && ns == w.ns {
					return true
				}
			case "Group":
				if g := s(sm["name"]); g == "system:serviceaccounts" || g == "system:serviceaccounts:"+w.ns {
					return true
				}
			}
		}
		return false
	}
	role := func(kind, ns, name string) *store.Object {
		if kind == "ClusterRole" {
			return c.ws.Get(c.name, "rbac.authorization.k8s.io", "clusterroles", "", name)
		}
		return c.ws.Get(c.name, "rbac.authorization.k8s.io", "roles", ns, name)
	}
	add := func(scope string, r *store.Object, roleName string) {
		if r == nil {
			rules = append(rules, scope+": "+roleName+" (rules not captured)")
			return
		}
		verbsFor := map[string]map[string]bool{}
		for _, rule := range l(r.Obj["rules"]) {
			rm := m(rule)
			verbs := strList(l(rm["verbs"]))
			for _, res := range strList(l(rm["resources"])) {
				rules = append(rules, fmt.Sprintf("%s: %s %s", scope, strings.Join(verbs, ","), res))
				if verbsFor[res] == nil {
					verbsFor[res] = map[string]bool{}
				}
				for _, v := range verbs {
					verbsFor[res][v] = true
				}
			}
		}
		can := func(res string, verbs ...string) bool {
			for _, r := range []string{res, "*"} {
				for _, v := range append(verbs, "*") {
					if verbsFor[r][v] {
						return true
					}
				}
			}
			return false
		}
		if can("secrets", "get", "list", "watch") {
			dangerous = append(dangerous, scope+": read Secrets")
		}
		if can("pods", "create") {
			dangerous = append(dangerous, scope+": create pods (can mount any Secret or service account in scope)")
		}
		if can("pods/exec", "create") {
			dangerous = append(dangerous, scope+": exec into pods")
		}
		if can("serviceaccounts/token", "create") {
			dangerous = append(dangerous, scope+": mint service account tokens")
		}
		for _, v := range []string{"escalate", "bind", "impersonate"} {
			for res := range verbsFor {
				if verbsFor[res][v] {
					dangerous = append(dangerous, scope+": "+v+" on "+res)
				}
			}
		}
	}
	for _, b := range c.list("rbac.authorization.k8s.io", "rolebindings") {
		if b.Namespace == w.ns && subjectOK(b) {
			rr := m(b.Obj["roleRef"])
			add("in "+w.ns, role(s(rr["kind"]), b.Namespace, s(rr["name"])), s(rr["kind"])+" "+s(rr["name"]))
		}
	}
	for _, b := range c.list("rbac.authorization.k8s.io", "clusterrolebindings") {
		if subjectOK(b) {
			rr := m(b.Obj["roleRef"])
			add("cluster-wide", role("ClusterRole", "", s(rr["name"])), "ClusterRole "+s(rr["name"]))
		}
	}
	sort.Strings(rules)
	dangerous = union(dangerous, nil)
	return
}

// ---------- declared dependencies ----------

// declared returns the Service keys and external hosts a workload is
// configured to call (see declarations in external.go).
func (c *cluster) declared(w *workload) (svcs map[string]bool, hosts map[string]bool) {
	svcs, hosts = map[string]bool{}, map[string]bool{}
	d := c.declarations(w)
	for k := range d.svcs {
		svcs[k] = true
	}
	for h := range d.hosts {
		hosts[h] = true
	}
	return
}

// Workloads with ztunnel access logs in the capture: ztunnel only logs pods
// it captured, so an in-mesh agent with no lines simply made no calls.
func (c *cluster) accessLogsCaptured() bool {
	for _, w := range c.works {
		if w.name == "ztunnel" || strings.HasPrefix(w.name, "ztunnel") {
			for _, p := range w.pods {
				if c.ws.HasStream(c.name, p.Namespace, p.Name, "istio-proxy", "current") {
					return true
				}
			}
		}
	}
	return false
}

// ---------- evaluation ----------

type engine struct {
	c        *cluster
	cache    map[string]hop
	internet map[*workload]string
	td       string
}

func (e *engine) hop(src *workload, sv *service) hop {
	k := src.id + "->" + sv.id
	if h, ok := e.cache[k]; ok {
		return h
	}
	h := e.c.evalHop(src, sv)
	e.cache[k] = h
	return h
}

func (e *engine) internetOf(w *workload) string {
	if v, ok := e.internet[w]; ok {
		return v
	}
	v, _ := e.c.egressTo(w, publicIP)
	e.internet[w] = v
	return v
}

var writeMethods = map[string]bool{"PUT": true, "POST": true, "PATCH": true, "DELETE": true, "MKCOL": true, "COPY": true, "MOVE": true, "PROPPATCH": true, "LOCK": true}

func canWrite(sv *service, h hop) bool {
	if h.verdict != Allowed {
		return false
	}
	if h.methods == nil {
		return true
	}
	for _, mth := range h.methods {
		if writeMethods[strings.ToUpper(mth)] || strings.HasPrefix(mth, "all except") {
			return true
		}
	}
	return false
}

func svcLabel(sv *service) string { return sv.ns + "/" + sv.name }

// Evaluate computes the reach of every agent in the workspace.
func Evaluate(ws *store.Workspace) Report {
	return evaluate(ws, func(cl string) []store.Flow { return ws.Flows(cl) })
}

// evaluate takes the flows as a function, so tests can add flows from
// sources the store does not parse yet.
func evaluate(ws *store.Workspace, flows func(cluster string) []store.Flow) Report {
	r := Report{Agents: []Agent{}, Notes: []string{}, Clusters: ws.Clusters()}
	flat := true
	for _, cl := range ws.Clusters() {
		c := newCluster(ws, cl)
		if len(c.netpols) > 0 || len(c.authz) > 0 {
			flat = false
		}
		r.Notes = append(r.Notes, c.notes...)
		fs := c.observe(flows(cl))
		td := fs.td
		if td == "" {
			td = "cluster.local"
		}
		logs := c.accessLogsCaptured()
		e := &engine{c: c, cache: map[string]hop{}, internet: map[*workload]string{}, td: td}
		agents := c.findAgents()
		for _, w := range agents {
			r.Agents = append(r.Agents, e.agent(w, agents, fs.byW[w], c.coverage(fs, w, logs)))
		}
	}
	r.Flat = flat && len(r.Agents) > 0
	if r.Flat {
		r.Notes = append(r.Notes, "no NetworkPolicy or AuthorizationPolicy exists in this capture: the network is flat, so every agent can reach every Service and the internet")
	}
	sort.SliceStable(r.Agents, func(i, j int) bool {
		a, b := r.Agents[i], r.Agents[j]
		if a.Counts.Critical != b.Counts.Critical {
			return a.Counts.Critical > b.Counts.Critical
		}
		if a.Counts.High != b.Counts.High {
			return a.Counts.High > b.Counts.High
		}
		return a.ID < b.ID
	})
	return r
}

func (e *engine) agent(w *workload, all []*workload, obs map[string]*observed, coveredBy []string) Agent {
	c := e.c
	observedCovered := len(coveredBy) > 0
	a := Agent{ID: w.id, Cluster: c.name, Namespace: w.ns, Name: w.name, Kind: w.kind, Type: w.agentType, Why: w.agentWhy, Ref: refOf(w.obj),
		Direct: []Edge{}, Paths: []Path{}, Findings: []Finding{}, NeverUsed: []string{}, Observed: observedCovered, ObservedBy: append([]string{}, coveredBy...), External: []External{}}
	a.Identity = c.identity(w)
	a.Identity.SharedVolumes = c.sharedVolumes(w, all)
	declSvcs, declHosts := c.declared(w)
	a.Counts.Services = len(c.services)

	// Direct reach.
	agentNode := "agent:" + w.id
	var reach []pair
	var deniedSeen []pair // the policies deny it, but traffic was seen getting through
	for _, sv := range c.services {
		if has(sv.classes, "apiserver") || has(sv.classes, "waypoint") {
			continue // the API server: through the token and RBAC; a waypoint: through the services it serves
		}
		self := false
		for _, b := range sv.backends {
			if b == w {
				self = true
			}
		}
		if self {
			continue
		}
		h := e.hop(w, sv)
		if h.verdict == Denied {
			if o := obs[key(sv.ns, sv.name)]; o != nil && o.requests > o.denied {
				deniedSeen = append(deniedSeen, pair{sv, h})
			}
			continue
		}
		reach = append(reach, pair{sv, h})
	}
	for _, d := range append(append([]pair{}, reach...), deniedSeen...) {
		k := key(d.sv.ns, d.sv.name)
		ed := Edge{From: agentNode, To: "svc:" + d.sv.id, Verdict: d.h.verdict, Layer: d.h.layer, Methods: d.h.methods, Paths: d.h.paths,
			Reasons: d.h.reasons, Conditions: d.h.conds, Declared: declSvcs[k], DeclaredIn: c.declarations(w).svcs[k], Write: canWrite(d.sv, d.h)}
		if o := obs[k]; o != nil {
			ed.Observed, ed.ObsMethods, ed.ObsPaths, ed.ObsDenied, ed.Tools, ed.ObservedBy = o.requests, o.methods, o.paths, o.denied, o.tools, o.sources
			ed.Evidence = o.records
		}
		ed.Risk, ed.Why = edgeRisk(w, d.sv, ed)
		if ed.Verdict == Denied {
			ed.Risk, ed.Why = "amber", fmt.Sprintf("the policies deny it, yet %d requests got through in the capture: the policy changed while it was recorded, or something here is not evaluated", ed.Observed-ed.ObsDenied)
			a.Direct = append(a.Direct, ed)
			continue
		}
		if ed.ObsDenied > 0 && ed.Verdict == Allowed {
			ed.Why += fmt.Sprintf("; %d of the observed requests were denied, although the policies allow it", ed.ObsDenied)
		}
		a.Direct = append(a.Direct, ed)
		if ed.Verdict == Allowed {
			a.Counts.Direct++
			if ed.Write && !has(d.sv.classes, "dns") {
				a.Counts.Write++
			}
		}
		if ed.Observed > 0 {
			a.Counts.Observed++
		}
		if observedCovered && ed.Verdict == Allowed && ed.Observed == 0 && !ed.Declared && !has(d.sv.classes, "dns") {
			a.NeverUsed = append(a.NeverUsed, svcLabel(d.sv))
		}
	}
	a.Counts.NeverUsed = len(a.NeverUsed)
	sort.SliceStable(a.Direct, func(i, j int) bool { return riskRank[a.Direct[i].Risk] < riskRank[a.Direct[j].Risk] })

	// Internet and cloud metadata.
	a.Internet = e.internetOf(w)
	a.Metadata, _ = c.egressTo(w, metadataIP)

	// Transitive closure: what the destinations can reach on the agent's behalf.
	type item struct {
		w     *workload
		sv    *service
		chain []string
	}
	visitedSvc := map[*service]bool{}
	visitedW := map[*workload]bool{w: true}
	queue := []item{}
	for _, d := range reach {
		if d.h.verdict != Allowed {
			continue
		}
		visitedSvc[d.sv] = true
		for _, b := range d.sv.backends {
			if !visitedW[b] && !has(b.classes, "dns") {
				visitedW[b] = true
				queue = append(queue, item{b, d.sv, []string{w.ns + "/" + w.name, svcLabel(d.sv)}})
			}
		}
	}
	var internetVia []Path
	const maxDepth = 3
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		sv := it.sv
		if e.internetOf(it.w) == Allowed {
			kind := "service"
			why := "if it can be made to fetch or forward for the agent, or is compromised through it"
			if sv != nil && has(sv.classes, "fetcher") {
				kind, why = "fetcher", "it fetches or proxies URLs, so an SSRF or a proxy feature lends its internet access to the agent (the Hugging Face path)"
			}
			internetVia = append(internetVia, Path{Hops: append(append([]string{}, it.chain...), "internet"), Target: "internet", Kind: kind, Why: why})
		}
		if len(it.chain)-1 >= maxDepth {
			continue
		}
		for _, sv2 := range c.services {
			if visitedSvc[sv2] || has(sv2.classes, "apiserver") || has(sv2.classes, "waypoint") {
				continue
			}
			if e.hop(it.w, sv2).verdict != Allowed {
				continue
			}
			visitedSvc[sv2] = true
			for _, b := range sv2.backends {
				if !visitedW[b] && !has(b.classes, "dns") {
					visitedW[b] = true
					queue = append(queue, item{b, sv2, append(append([]string{}, it.chain...), svcLabel(sv2))})
				}
			}
		}
	}
	a.Counts.Closure = len(visitedSvc)

	// Paths that matter, worst first.
	if a.Internet == Allowed {
		a.Paths = append(a.Paths, Path{Hops: []string{w.ns + "/" + w.name, "internet"}, Target: "internet", Kind: "internet", Why: "no egress control on the agent itself"})
	}
	sort.SliceStable(internetVia, func(i, j int) bool {
		if (internetVia[i].Kind == "fetcher") != (internetVia[j].Kind == "fetcher") {
			return internetVia[i].Kind == "fetcher"
		}
		return len(internetVia[i].Hops) < len(internetVia[j].Hops)
	})
	for i, p := range internetVia {
		if p.Kind == "fetcher" || i < 3 {
			a.Paths = append(a.Paths, p)
		}
	}
	if a.Metadata == Allowed && c.platform != "kind" {
		a.Paths = append(a.Paths, Path{Hops: []string{w.ns + "/" + w.name, "169.254.169.254"}, Target: "metadata", Kind: "metadata", Why: "the cloud instance metadata endpoint hands out node credentials"})
	}

	// Destinations outside the cluster.
	a.External = e.externals(w, &a, reach, obs)
	for _, x := range a.External {
		if x.ReachedVia != ViaNode {
			a.Counts.External++
		}
	}
	a.Paths = append(a.Paths, externalPaths(w, a)...)

	a.Findings = e.findings(w, a, reach, all, declSvcs, declHosts)
	for _, f := range a.Findings {
		switch f.Severity {
		case "critical":
			a.Counts.Critical++
		case "high":
			a.Counts.High++
		}
	}
	a.Radius = radius(a)
	a.Summary = summary(w, a, internetVia)
	a.Nodes, a.Edges = e.graph(w, a, reach, internetVia)
	return a
}

type pair struct {
	sv *service
	h  hop
}

var riskRank = map[string]int{"red": 0, "amber": 1, "green": 2, "grey": 3}

func edgeRisk(w *workload, sv *service, ed Edge) (string, string) {
	if ed.Verdict == Unknown {
		return "amber", "could not be evaluated from the capture"
	}
	sensitive := has(sv.classes, "fetcher") || has(sv.classes, "store") || has(sv.classes, "credentials")
	switch {
	case has(sv.classes, "dns"):
		return "grey", "cluster DNS"
	case ed.Write && has(sv.classes, "fetcher"):
		return "red", "can write to a service that fetches from elsewhere"
	case ed.Write && has(sv.classes, "store"):
		return "red", "can read and write a data store other workloads use"
	case ed.Write && has(sv.classes, "credentials"):
		return "red", "can call a credential service with any method"
	case sensitive && ed.Methods == nil:
		return "red", "no request-level rule"
	case !w.inMesh:
		return "amber", "the agent has no mesh identity, so nothing can authorise it by name"
	case ed.Layer == "L7" && !ed.Write:
		return "green", "read-only methods allowed"
	case ed.Methods == nil && ed.Observed == 0 && !ed.Declared:
		return "amber", "allowed, but never called and not configured"
	case ed.Methods == nil:
		return "amber", "every method allowed (no request-level rule)"
	}
	return "green", "restricted"
}

func radius(a Agent) string {
	switch {
	case a.Internet == Allowed || a.Counts.Critical > 0:
		return "open"
	case a.Counts.Services > 0 && a.Counts.Closure*2 >= a.Counts.Services:
		return "wide"
	case a.Counts.Closure > 3:
		return "narrow"
	}
	return "contained"
}

func summary(w *workload, a Agent, via []Path) string {
	name := w.name
	switch {
	case len(via) > 0 && via[0].Kind == "fetcher":
		return fmt.Sprintf("%s reaches the internet in %d hops through %s, which fetches on request (%s).", name, len(via[0].Hops)-1, via[0].Hops[1], strings.Join(via[0].Hops, " → "))
	case a.Internet != Allowed && externalFetcher(a) != nil:
		x := externalFetcher(a)
		return fmt.Sprintf("%s has no open internet egress, but reaches %s, %s outside the cluster that fetches from the internet on request.", name, x.Host, article(x.Product))
	case a.Internet == Allowed:
		return fmt.Sprintf("%s reaches the internet directly and %d of %d Services in the cluster (%d in any number of hops).", name, a.Counts.Direct, a.Counts.Services, a.Counts.Closure)
	case len(via) > 0:
		return fmt.Sprintf("%s has no internet access itself, but reaches it in %d hops: %s.", name, len(via[0].Hops)-1, strings.Join(via[0].Hops, " → "))
	case a.Counts.Direct == 0:
		return fmt.Sprintf("%s is contained: no Service or internet destination is allowed.", name)
	}
	return fmt.Sprintf("%s reaches %d Services directly and %d in total; no path to the internet was found.", name, a.Counts.Direct, a.Counts.Closure)
}

func externalFetcher(a Agent) *External {
	for i, x := range a.External {
		if x.Fetcher && x.Verdict == Allowed && x.ReachedVia != ViaNode && !(x.ReachedVia == ViaGateway && x.Authn && x.ReadOnly && x.DirectEgress != Allowed) {
			return &a.External[i]
		}
	}
	return nil
}

// sharedVolumes lists PersistentVolumeClaims this workload mounts that other
// agents mount too.
func (c *cluster) sharedVolumes(w *workload, all []*workload) []string {
	claims := func(x *workload) map[string]bool {
		out := map[string]bool{}
		for _, v := range l(x.spec["volumes"]) {
			if cn := s(path(v, "persistentVolumeClaim", "claimName")); cn != "" {
				out[cn] = true
			}
		}
		return out
	}
	mine := claims(w)
	out := []string{}
	for _, cn := range sortedKeys(mine) {
		var with []string
		for _, o := range all {
			if o != w && o.ns == w.ns && claims(o)[cn] {
				with = append(with, o.name)
			}
		}
		if len(with) > 0 {
			out = append(out, fmt.Sprintf("PersistentVolumeClaim %s/%s, also mounted by %s", w.ns, cn, strings.Join(with, ", ")))
		}
	}
	return out
}
