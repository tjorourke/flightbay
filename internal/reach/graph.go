package reach

import (
	"fmt"
	"sort"
	"strings"
)

// graph builds a readable blast-radius graph: the agent, the destinations
// that matter drawn one by one (risky, used, configured, sensitive, or in the
// agent's own namespace), the rest folded into one node per namespace, and
// the onward paths to the internet.
func (e *engine) graph(w *workload, a Agent, reach []pair, via []Path) ([]Node, []Edge) {
	c := e.c
	agentID := "agent:" + w.id
	nodes := []Node{{ID: agentID, Type: "agent", Label: w.ns + "/" + w.name, Namespace: w.ns, Depth: 0, Risk: riskOfAgent(a), Ref: refOf(w.obj), Detail: a.Type}}
	var edges []Edge
	have := map[string]bool{agentID: true}
	addNode := func(n Node) {
		if !have[n.ID] {
			have[n.ID] = true
			nodes = append(nodes, n)
		}
	}
	edgeByTo := map[string]Edge{}
	for _, ed := range a.Direct {
		edgeByTo[ed.To] = ed
	}
	type cand struct {
		sv *service
		ed Edge
	}
	var notable []cand
	groups := map[string][]cand{}
	for _, d := range reach {
		ed := edgeByTo["svc:"+d.sv.id]
		if ed.To == "" || has(d.sv.classes, "dns") {
			continue
		}
		sensitive := len(d.sv.classes) > 0
		if ed.Risk == "red" || ed.Observed > 0 || ed.Declared || sensitive || d.sv.ns == w.ns {
			notable = append(notable, cand{d.sv, ed})
		} else {
			groups[d.sv.ns] = append(groups[d.sv.ns], cand{d.sv, ed})
		}
	}
	sort.SliceStable(notable, func(i, j int) bool {
		a, b := notable[i], notable[j]
		if riskRank[a.ed.Risk] != riskRank[b.ed.Risk] {
			return riskRank[a.ed.Risk] < riskRank[b.ed.Risk]
		}
		return a.ed.Observed > b.ed.Observed
	})
	const maxNotable = 24
	for i, n := range notable {
		if i >= maxNotable {
			groups[n.sv.ns] = append(groups[n.sv.ns], n)
			continue
		}
		id := "svc:" + n.sv.id
		addNode(Node{ID: id, Type: "service", Label: svcLabel(n.sv), Namespace: n.sv.ns, Classes: n.sv.classes, Depth: 1, Risk: n.ed.Risk, Ref: refOf(n.sv.obj), Detail: n.ed.Why})
		edges = append(edges, n.ed)
	}
	for _, ns := range sortedKeys(groups) {
		g := groups[ns]
		id := "group:" + c.name + "/" + ns
		risk := "amber"
		allUnknown := true
		for _, x := range g {
			if x.ed.Verdict != Unknown {
				allUnknown = false
			}
		}
		var names []string
		for _, x := range g {
			names = append(names, x.sv.name)
		}
		addNode(Node{ID: id, Type: "group", Label: fmt.Sprintf("%d more in %s", len(g), ns), Namespace: ns, Depth: 1, Risk: risk, Count: len(g), Detail: strings.Join(names, ", ")})
		why := "allowed, never called and not configured"
		if allUnknown {
			why = "could not be evaluated from the capture"
		}
		edges = append(edges, Edge{From: agentID, To: id, Verdict: Allowed, Risk: risk, Why: why})
	}

	// agentgateway: show the backends the agent can reach through the gateway.
	for _, n := range notable {
		if n.sv.agwClass == "" || !have["svc:"+n.sv.id] {
			continue
		}
		for _, b := range c.agwBackends(n.sv) {
			if b.svc == nil {
				continue // external backends are drawn from a.External below
			}
			id := "svc:" + b.svc.id
			addNode(Node{ID: id, Type: "service", Label: b.label, Depth: 2, Risk: b.risk, Detail: b.why, Classes: b.classes})
			if !hasEdge(edges, "svc:"+n.sv.id, id) {
				edges = append(edges, Edge{From: "svc:" + n.sv.id, To: id, Verdict: Allowed, Via: "agentgateway route " + b.route, Risk: b.risk, Why: b.why})
			}
		}
	}

	// Internet and metadata.
	inet := "internet"
	maxDepth := 2
	for _, p := range via {
		if len(p.Hops)-1 > maxDepth {
			maxDepth = len(p.Hops) - 1
		}
	}
	if maxDepth > 4 {
		maxDepth = 4
	}
	internetNode := Node{ID: inet, Type: "internet", Label: "Internet", Depth: maxDepth, Risk: "green", Detail: "any address outside the cluster"}
	switch a.Internet {
	case Allowed:
		internetNode.Risk = "red"
		edges = append(edges, Edge{From: agentID, To: inet, Verdict: Allowed, Risk: "red", Why: "no egress control on the agent"})
	case Denied:
		edges = append(edges, Edge{From: agentID, To: inet, Verdict: Denied, Risk: "grey", Why: "blocked by NetworkPolicy"})
	case Unknown:
		internetNode.Risk = "amber"
		edges = append(edges, Edge{From: agentID, To: inet, Verdict: Unknown, Risk: "amber", Why: "NetworkPolicies not captured"})
	}
	// Onward paths (fetchers first), at most five, so the drawing stays legible.
	drawn := 0
	for _, p := range via {
		if drawn >= 5 || (p.Kind != "fetcher" && a.Internet == Allowed) {
			continue
		}
		drawn++
		prev := agentID
		for i, h := range p.Hops[1:] {
			if h == "internet" {
				edges = append(edges, Edge{From: prev, To: inet, Verdict: Allowed, Risk: "red", Why: p.Why})
				internetNode.Risk = "red"
				break
			}
			sv := c.svcByKey[h]
			id := "svc:" + h
			if sv != nil {
				id = "svc:" + sv.id
			}
			if !have[id] {
				n := Node{ID: id, Type: "service", Label: h, Depth: i + 1, Risk: "amber"}
				if sv != nil {
					n.Classes, n.Namespace, n.Ref = sv.classes, sv.ns, refOf(sv.obj)
					if has(sv.classes, "fetcher") {
						n.Risk = "red"
					}
				}
				addNode(n)
			}
			if prev != agentID || !hasEdge(edges, prev, id) {
				if !hasEdge(edges, prev, id) {
					edges = append(edges, Edge{From: prev, To: id, Verdict: Allowed, Risk: "amber", Why: "allowed"})
				}
			}
			prev = id
		}
	}
	if a.Metadata != "" && a.Metadata != Denied {
		risk, why := "red", "cloud node credentials"
		if c.platform == "kind" {
			risk, why = "grey", "not reachable on kind, but nothing blocks it on a cloud node"
		} else if a.Metadata == Unknown {
			risk, why = "amber", "NetworkPolicies not captured"
		}
		addNode(Node{ID: "metadata", Type: "metadata", Label: "169.254.169.254", Depth: 1, Risk: risk, Detail: why})
		edges = append(edges, Edge{From: agentID, To: "metadata", Verdict: a.Metadata, Risk: risk, Why: why})
	}
	// Destinations outside the cluster, in the last column: through the
	// gateway that carries them, or out through the internet node.
	drawnExt := 0
	for _, x := range a.External {
		if x.ReachedVia == ViaNode || drawnExt >= 24 {
			continue
		}
		drawnExt++
		id := "ext:" + x.Host
		tag := extTag[x.Class]
		cls := []string{}
		if tag != "" {
			cls = append(cls, tag)
		}
		via := x.ReachedVia
		if x.ReachedVia == ViaGateway && x.Authn {
			via += ", authn"
		}
		if x.Observed > 0 {
			via += fmt.Sprintf(", %d call%s", x.Observed, plural(x.Observed))
		}
		detail := x.Product + ". " + x.Why
		if len(x.Credentials) > 0 {
			detail += ". Holds " + strings.Join(x.Credentials, ", ")
		}
		addNode(Node{ID: id, Type: "external", Label: x.Host, Depth: maxDepth + 1, Risk: x.Risk, Classes: cls, Detail: detail, Via: via})
		ed := Edge{To: id, Verdict: x.Verdict, Risk: x.Risk, Why: x.Why, Observed: x.Observed, ObsMethods: x.Methods, Tools: x.Tools, ObservedBy: x.Sources, Declared: len(x.DeclaredIn) > 0}
		from := inet
		if x.ReachedVia == ViaGateway && x.Gateway != "" {
			if sv := c.svcByKey[x.Gateway]; sv != nil && have["svc:"+sv.id] {
				from = "svc:" + sv.id
				ed.Via = "agentgateway route " + x.Route
			}
		} else if x.ReachedVia == ViaProxy {
			if sv := c.svcByKey[x.Gateway]; sv != nil && have["svc:"+sv.id] {
				from = "svc:" + sv.id
				ed.Via = "HTTP proxy " + x.Gateway
			}
		}
		if from == inet && a.Internet != Allowed && x.Verdict == Allowed {
			internetNode.Risk = "amber" // an allowed host through otherwise closed egress
		}
		if x.Verdict == Denied {
			ed.Risk = "grey"
		}
		ed.From = from
		edges = append(edges, ed)
	}
	if !have[inet] {
		addNode(internetNode)
	}
	return nodes, edges
}

var extTag = map[string]string{"registry": "registry", "proxy": "proxy", "llm": "llm", "git": "git", "storage": "storage", "exfil": "exfil", "metadata": "metadata"}

func hasEdge(es []Edge, from, to string) bool {
	for _, e := range es {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}

func riskOfAgent(a Agent) string {
	switch {
	case a.Counts.Critical > 0:
		return "red"
	case a.Counts.High > 0:
		return "amber"
	}
	return "green"
}

type agwBackend struct {
	label, route, risk, why string
	svc                     *service
	classes                 []string
}

// agwBackends lists what a route on this agentgateway Gateway sends to, and
// whether the route asks for authentication.
func (c *cluster) agwBackends(gw *service) []agwBackend {
	var out []agwBackend
	backends := map[string]map[string]any{}
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}} {
		for _, b := range c.list(g.group, g.res) {
			backends[key(b.Namespace, b.Name)] = m(b.Obj["spec"])
		}
	}
	authned := map[string]bool{} // policy targets with authentication: kind/ns/name
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, p := range c.list(g.group, g.res) {
			t := m(path(p.Obj, "spec", "traffic"))
			if t["jwtAuthentication"] == nil && t["apiKeyAuthentication"] == nil && t["basicAuthentication"] == nil && t["extAuth"] == nil && t["entExtAuth"] == nil {
				continue
			}
			for _, tr := range l(path(p.Obj, "spec", "targetRefs")) {
				authned[s(m(tr)["kind"])+"/"+p.Namespace+"/"+s(m(tr)["name"])] = true
			}
		}
	}
	gwAuth := authned["Gateway/"+gw.gatewayNS+"/"+gw.gatewayNm]
	seen := map[string]bool{}
	for _, r := range c.list("gateway.networking.k8s.io", "httproutes") {
		attached := false
		for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
			pm := m(pr)
			ns := s(pm["namespace"])
			if ns == "" {
				ns = r.Namespace
			}
			if s(pm["name"]) == gw.gatewayNm && ns == gw.gatewayNS && (s(pm["kind"]) == "" || s(pm["kind"]) == "Gateway") {
				attached = true
			}
		}
		if !attached {
			continue
		}
		auth := gwAuth || authned["HTTPRoute/"+r.Namespace+"/"+r.Name]
		for _, rule := range l(path(r.Obj, "spec", "rules")) {
			for _, br := range l(m(rule)["backendRefs"]) {
				bm := m(br)
				ns := s(bm["namespace"])
				if ns == "" {
					ns = r.Namespace
				}
				var labels []string
				var svc *service
				switch s(bm["kind"]) {
				case "AgentgatewayBackend", "EnterpriseAgentgatewayBackend":
					sp := backends[key(ns, s(bm["name"]))]
					mc := m(sp["mcp"])
					if mc == nil {
						mc = m(sp["entMcp"])
					}
					for _, t := range l(mc["targets"]) {
						if h := s(path(t, "static", "host")); h != "" {
							labels = append(labels, h)
						}
					}
					if ai := m(sp["ai"]); ai != nil {
						for prov := range m(ai["provider"]) {
							labels = append(labels, prov+" (LLM provider)")
						}
						for _, g := range l(ai["groups"]) {
							for _, p := range l(m(g)["providers"]) {
								labels = append(labels, s(m(p)["name"])+" (LLM provider)")
							}
						}
					}
					if h := s(path(sp, "static", "host")); h != "" {
						labels = append(labels, h)
					}
					if len(labels) == 0 {
						labels = []string{ns + "/" + s(bm["name"])}
					}
				case "", "Service":
					svc = c.svcByKey[key(ns, s(bm["name"]))]
					labels = []string{ns + "/" + s(bm["name"])}
				default:
					continue
				}
				for _, lb := range labels {
					if k := svcKeyFromHost(lb, ns); k != "" && c.svcByKey[k] != nil && svc == nil {
						svc = c.svcByKey[k]
						lb = k
					}
					if seen[lb] {
						continue
					}
					seen[lb] = true
					b := agwBackend{label: lb, route: r.Namespace + "/" + r.Name, svc: svc, risk: "green", why: "through the gateway, which requires authentication"}
					if svc != nil {
						b.classes = svc.classes
					}
					if !auth {
						b.risk, b.why = "red", "through the gateway with no authentication on the route"
					}
					out = append(out, b)
				}
			}
		}
	}
	return out
}
