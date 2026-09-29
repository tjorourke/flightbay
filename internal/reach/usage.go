package reach

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/store"
)

// Usage puts an agent's reach next to what it actually did: for each
// destination, whether policy allows it and whether it was used, with the
// connections ztunnel logged, the calls agentgateway logged (tools, models,
// tokens, refusals) and the flows that saw it. It also carries the agent's
// activity over time, and a lock-down proposal: the egress policy that allows
// only what it used, and the reach it would leave.
type Usage struct {
	Agent        string         `json:"agent"`
	Cluster      string         `json:"cluster"`
	Destinations []UseDest      `json:"destinations"`
	Timeline     store.Timeline `json:"timeline"`
	Lockdown     Lockdown       `json:"lockdown"`
	Totals       UseTotals      `json:"totals"`
	Sources      []string       `json:"sources"` // flow sources, traffic (ztunnel), activity (agentgateway)
	Notes        []string       `json:"notes"`
}

// UseTotals counts destinations.
type UseTotals struct {
	Allowed    int `json:"allowed"`
	Used       int `json:"used"`
	Unused     int `json:"unused"`     // allowed, never used
	Mismatch   int `json:"mismatch"`   // used although policy denies it
	Tried      int `json:"tried"`      // attempted, always refused
	Unexpected int `json:"unexpected"` // used, not in the reach report (outside the capture)
}

// UseDest is one destination of the agent.
type UseDest struct {
	To      string `json:"to"` // svc:<id>, ext:<host>, or x:<label> for traffic reach does not know
	Label   string `json:"label"`
	Kind    string `json:"kind"` // service, external
	Verdict string `json:"verdict,omitempty"`
	Allowed bool   `json:"allowed"`
	Risk    string `json:"risk,omitempty"`
	Used    bool   `json:"used"`
	// Tried: attempted, but every attempt was refused.
	Tried bool `json:"tried,omitempty"`
	// Mismatch: used although the captured policy denies it.
	Mismatch bool `json:"mismatch,omitempty"`

	Observed   int      `json:"observed,omitempty"`   // requests in the flows (reach's count)
	ObservedBy []string `json:"observedBy,omitempty"` // flow sources

	Connections int    `json:"connections,omitempty"` // ztunnel
	Refused     int    `json:"refused,omitempty"`
	TLS         string `json:"tls,omitempty"`
	Via         string `json:"via,omitempty"`

	Calls        int        `json:"calls,omitempty"` // agentgateway
	CallsRefused int        `json:"callsRefused,omitempty"`
	CallsFailed  int        `json:"callsFailed,omitempty"`
	Tools        []UseTool  `json:"tools,omitempty"`
	Models       []UseModel `json:"models,omitempty"`
	Tokens       int64      `json:"tokens,omitempty"`

	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// UseTool is a tool the agent called at a destination.
type UseTool struct {
	Name    string `json:"name"`
	Calls   int    `json:"calls"`
	Refused int    `json:"refused,omitempty"`
}

// UseModel is a model the agent called.
type UseModel struct {
	Name   string `json:"name"`
	Calls  int    `json:"calls"`
	Tokens int64  `json:"tokens"`
}

// Lockdown is the least-privilege proposal built from what the agent used.
type Lockdown struct {
	Fix       string       `json:"fix"`     // YAML: the egress NetworkPolicy, and MCP tool allowlists
	Keeps     []string     `json:"keeps"`   // destinations it allows
	Removes   []string     `json:"removes"` // allowed today, cut by the proposal
	Breaks    []string     `json:"breaks"`  // used, but the proposal has no rule for it
	Before    ReachSummary `json:"before"`
	After     ReachSummary `json:"after"`
	StillUsed []string     `json:"stillUsed"` // used destinations still allowed afterwards (the check)
	Cut       []string     `json:"cut"`       // used destinations the proposal would deny
	Notes     []string     `json:"notes"`
	Evaluated bool         `json:"evaluated"` // the proposal was re-evaluated with the reach evaluator
}

// ReachSummary is an agent's reach in a few numbers.
type ReachSummary struct {
	Radius   string `json:"radius"`
	Internet string `json:"internet"`
	Direct   int    `json:"direct"`
	Closure  int    `json:"closure"`
	Write    int    `json:"write"`
	External int    `json:"external"`
	Critical int    `json:"critical"`
	High     int    `json:"high"`
}

func summaryOf(a Agent) ReachSummary {
	return ReachSummary{Radius: a.Radius, Internet: a.Internet, Direct: a.Counts.Direct, Closure: a.Counts.Closure, Write: a.Counts.Write,
		External: a.Counts.External, Critical: a.Counts.Critical, High: a.Counts.High}
}

// AgentUsage builds the usage view for one agent from its reach report and,
// when given, the traffic map and the activity report.
func AgentUsage(ws *store.Workspace, a Agent, tr *Traffic, act *Activity) Usage {
	u := Usage{Agent: a.Namespace + "/" + a.Name, Cluster: a.Cluster, Destinations: []UseDest{}, Sources: append([]string{}, a.ObservedBy...), Notes: []string{}}
	c := newCluster(ws, a.Cluster)
	var w *workload
	for _, x := range c.works {
		if x.id == a.ID {
			w = x
		}
	}
	if w == nil {
		u.Notes = append(u.Notes, "the agent's workload is not in the capture")
		return u
	}
	svcByID := map[string]*service{}
	for _, sv := range c.services {
		svcByID["svc:"+sv.id] = sv
	}
	dests := map[string]*UseDest{}
	order := []string{}
	get := func(to, label, kind string) *UseDest {
		if d := dests[to]; d != nil {
			return d
		}
		d := &UseDest{To: to, Label: label, Kind: kind}
		dests[to] = d
		order = append(order, to)
		return d
	}
	seenAt := func(d *UseDest, f, l time.Time) {
		if !f.IsZero() && (d.First.IsZero() || f.Before(d.First)) {
			d.First = f
		}
		if l.After(d.Last) {
			d.Last = l
		}
	}
	for _, e := range a.Direct {
		label := e.To
		if sv := svcByID[e.To]; sv != nil {
			label = sv.ns + "/" + sv.name
		}
		d := get(e.To, label, "service")
		d.Verdict, d.Allowed, d.Risk = e.Verdict, e.Verdict != Denied, e.Risk
		d.Observed, d.ObservedBy = e.Observed, e.ObservedBy
		for _, t := range e.Tools {
			d.Tools = append(d.Tools, UseTool{Name: lastPart(t)}) // flows name multiplexed tools target/tool
		}
	}
	extByAddr := map[string]string{}
	for _, x := range a.External {
		if x.ReachedVia == "node" {
			continue
		}
		d := get("ext:"+x.Host, x.Host, "external")
		d.Verdict, d.Allowed, d.Risk, d.Via = x.Verdict, x.Verdict != Denied, x.Risk, x.ReachedVia
		d.Observed, d.ObservedBy = x.Observed, x.Sources
		extByAddr[x.Host] = d.To
		if x.IP != "" {
			extByAddr[x.IP] = d.To
		}
	}
	src := "w:" + a.ID
	if tr != nil {
		u.Sources = union(u.Sources, []string{"ztunnel"})
		labels := map[string]string{}
		for _, n := range tr.Nodes {
			labels[n.ID] = n.Label
		}
		for _, e := range tr.Edges {
			if e.From != src {
				continue
			}
			var d *UseDest
			switch {
			case dests[e.To] != nil:
				d = dests[e.To]
			case strings.HasPrefix(e.To, "svc:"):
				d = get(e.To, labels[e.To], "service")
				d.Verdict, d.Allowed = e.Verdict, e.Verdict != Denied
			default:
				lb := labels[e.To]
				if to := extByAddr[stripPort(lb)]; to != "" {
					d = dests[to]
				} else {
					d = get("x:"+lb, lb, "external")
				}
			}
			d.Connections += e.Connections
			d.Refused += e.Denied
			d.TLS = first(d.TLS, e.TLS)
			if d.Via == "" {
				d.Via = e.Via
			}
			seenAt(d, e.First, e.Last)
		}
	}
	if act != nil {
		u.Sources = union(u.Sources, []string{"agentgateway"})
		targets := map[string]ActTarget{}
		for _, t := range act.Targets {
			targets[t.ID] = t
		}
		for _, e := range act.Edges {
			if e.From != src {
				continue
			}
			t := targets[e.To]
			sv := c.serviceOfCall(t)
			var d *UseDest
			if sv != nil {
				d = get("svc:"+sv.id, sv.ns+"/"+sv.name, "service")
				if d.Verdict == "" {
					h := c.evalHop(w, sv)
					d.Verdict, d.Allowed = h.verdict, h.verdict != Denied
				}
			} else {
				gw := first(strings.Join(t.Gateways, ", "), t.Label)
				d = get("gw:"+gw, gw, "service")
			}
			d.Calls += e.Requests
			d.CallsRefused += e.Denied
			d.CallsFailed += e.Errors
			d.Tokens += e.InputTokens + e.OutputTokens
			switch t.Kind {
			case "tool":
				d.Tools = addTool(d.Tools, t.Label, e.Requests, e.Denied)
			case "model":
				d.Models = append(d.Models, UseModel{Name: t.Label, Calls: e.Requests, Tokens: e.InputTokens + e.OutputTokens})
			}
			seenAt(d, e.First, e.Last)
		}
	}
	for _, k := range order {
		d := dests[k]
		logged := d.Connections + d.Calls
		d.Used = d.Connections > d.Refused || d.Calls > d.CallsRefused || (logged == 0 && d.Observed > 0)
		d.Tried = !d.Used && logged > 0
		d.Mismatch = d.Used && d.Verdict == Denied
		switch {
		case d.Allowed && d.Used:
			u.Totals.Used++
		case d.Allowed:
			u.Totals.Unused++
		case d.Used && d.Verdict == "":
			u.Totals.Unexpected++
		}
		if d.Allowed {
			u.Totals.Allowed++
		}
		if d.Mismatch {
			u.Totals.Mismatch++
		}
		if d.Tried {
			u.Totals.Tried++
		}
		// Tools from the flows carry no counts; drop them where agentgateway gave counts.
		var tools []UseTool
		for _, t := range d.Tools {
			if t.Calls > 0 || !hasCounted(d.Tools, t.Name) {
				tools = append(tools, t)
			}
		}
		d.Tools = tools
		u.Destinations = append(u.Destinations, *d)
	}
	sort.SliceStable(u.Destinations, func(i, j int) bool {
		x, y := u.Destinations[i], u.Destinations[j]
		if x.Used != y.Used {
			return x.Used
		}
		vx, vy := x.Connections+x.Calls+x.Observed, y.Connections+y.Calls+y.Observed
		if vx != vy {
			return vx > vy
		}
		return x.Label < y.Label
	})
	if !a.Observed && tr == nil && act == nil {
		u.Notes = append(u.Notes, "no flow logs cover this agent, so what it used is unknown and nothing is proposed")
	}
	pods, ips := []string{}, []string{}
	for _, p := range w.pods {
		pods = append(pods, p.Name)
		if ip := s(path(p.Obj, "status", "podIP")); ip != "" {
			ips = append(ips, ip)
		}
	}
	u.Timeline = ws.AgentTimeline(store.TimelineFilter{Cluster: a.Cluster, Pods: pods, IdentitySuffix: "/ns/" + w.ns + "/sa/" + w.sa, IPs: ips})
	u.Lockdown = c.lockdown(ws, w, a, u.Destinations, svcByID, act)
	return u
}

func hasCounted(ts []UseTool, name string) bool {
	for _, t := range ts {
		if t.Name == name && t.Calls > 0 {
			return true
		}
	}
	return false
}

func addTool(ts []UseTool, name string, calls, refused int) []UseTool {
	for i := range ts {
		if ts[i].Name == name && ts[i].Calls > 0 {
			ts[i].Calls += calls
			ts[i].Refused += refused
			return ts
		}
	}
	return append(ts, UseTool{Name: name, Calls: calls, Refused: refused})
}

// serviceOfCall finds the Service an agentgateway call went to: the route
// when a waypoint names the Service as its route, else the gateway's Service.
func (c *cluster) serviceOfCall(t ActTarget) *service {
	for _, r := range t.Routes {
		if sv := c.svcByKey[r]; sv != nil {
			return sv
		}
	}
	for _, g := range t.Gateways {
		if sv := c.svcByKey[g]; sv != nil {
			return sv
		}
		ns, name, _ := strings.Cut(g, "/")
		for _, sv := range c.services {
			if sv.gatewayNS == ns && sv.gatewayNm == name {
				return sv
			}
		}
	}
	return nil
}

// lockdown proposes an egress policy that allows only what the agent used,
// MCP tool allowlists for the tools it called, and re-evaluates the agent
// with the policy in place to show what it would leave.
func (c *cluster) lockdown(ws *store.Workspace, w *workload, a Agent, dests []UseDest, svcByID map[string]*service, act *Activity) Lockdown {
	ld := Lockdown{Keeps: []string{}, Removes: []string{}, Breaks: []string{}, StillUsed: []string{}, Cut: []string{}, Notes: []string{}, Before: summaryOf(a)}
	used := 0
	for _, d := range dests {
		if d.Used {
			used++
		}
	}
	if used == 0 {
		ld.Notes = append(ld.Notes, "nothing this agent used is in the capture, so there is nothing to build a policy from")
		return ld
	}
	var needed []*service
	hosts := map[string]bool{}
	for _, d := range dests {
		if !d.Used {
			continue
		}
		switch {
		case svcByID[d.To] != nil:
			needed = append(needed, svcByID[d.To])
			ld.Keeps = append(ld.Keeps, d.Label)
		case d.Kind == "external" && d.Via == "gateway":
			ld.Keeps = append(ld.Keeps, d.Label+" (through its gateway)")
		case d.Kind == "external":
			hosts[d.Label] = true
			ld.Breaks = append(ld.Breaks, d.Label+": called directly, outside the cluster. The policy has no rule for it; route it through agentgateway, or add an ipBlock if it must stay direct.")
		default:
			ld.Breaks = append(ld.Breaks, d.Label+": used, but not a Service in the capture, so the policy has no rule for it.")
		}
	}
	// External hosts reached through a gateway need the gateway's Service.
	for _, x := range a.External {
		if x.Observed > 0 && x.ReachedVia == "gateway" && x.Gateway != "" {
			if sv := c.svcByKey[x.Gateway]; sv != nil {
				needed = append(needed, sv)
			}
		}
	}
	e := &engine{c: c, td: "cluster.local"}
	np := e.egressPolicy(w, needed, hosts)
	var others []string
	for _, p := range c.policiesSelecting(w.ns, w.labels, true) {
		if p.Name != w.name+"-egress" {
			others = append(others, p.Namespace+"/"+p.Name)
		}
	}
	if len(others) > 0 {
		ld.Notes = append(ld.Notes, "other egress NetworkPolicies also select "+w.name+" ("+strings.Join(others, ", ")+"). NetworkPolicies add up, so remove or narrow them too, or the agent keeps what they allow. The before and after below assume they are removed.")
	}
	if !c.npKnown {
		ld.Notes = append(ld.Notes, "NetworkPolicies were not captured, so the effect of the proposal cannot be evaluated")
	}
	fix := []string{np}
	if act != nil {
		fix = append(fix, c.mcpAllowlists(w, act)...)
	}
	ld.Fix = strings.Join(fix, "\n---\n")
	// Re-evaluate with the proposal in place of the agent's egress policies.
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(np), &obj); err != nil || obj == nil {
		ld.Notes = append(ld.Notes, "the proposal could not be re-evaluated: "+fmt.Sprint(err))
		return ld
	}
	after, ok := evaluateAgentWith(ws, a.ID, obj, others)
	if !ok {
		return ld
	}
	ld.Evaluated, ld.After = true, summaryOf(after)
	afterTo := map[string]Edge{}
	for _, e := range after.Direct {
		afterTo[e.To] = e
	}
	for _, d := range dests {
		if d.Kind != "service" || !strings.HasPrefix(d.To, "svc:") {
			continue
		}
		ae, ok := afterTo[d.To]
		allowedAfter := ok && ae.Verdict != Denied
		switch {
		case d.Used && allowedAfter:
			ld.StillUsed = append(ld.StillUsed, d.Label)
		case d.Used:
			ld.Cut = append(ld.Cut, d.Label)
		case d.Allowed && !allowedAfter:
			ld.Removes = append(ld.Removes, d.Label)
		}
	}
	sort.Strings(ld.Removes)
	return ld
}

// mcpAllowlists writes, for each MCP server the agent called tools on, a
// backend.mcp.authorization rule that allows every caller seen there only the
// tools it called. Other callers are kept, so the rule does not cut them off.
func (c *cluster) mcpAllowlists(w *workload, act *Activity) []string {
	callers := map[string]ActCaller{}
	for _, x := range act.Callers {
		callers[x.ID] = x
	}
	targets := map[string]ActTarget{}
	for _, t := range act.Targets {
		targets[t.ID] = t
	}
	self := "w:" + w.id
	// One rule per route the agent called tools through, covering every
	// caller seen on that route.
	routes := map[string]bool{}
	for _, e := range act.Edges {
		if t := targets[e.To]; e.From == self && t.Kind == "tool" {
			for _, r := range e.Routes {
				routes[r] = true
			}
		}
	}
	var out []string
	for _, route := range sortedKeys(routes) {
		byCaller := map[string][]string{}
		var anon []string
		server := ""
		for _, e := range act.Edges {
			t := targets[e.To]
			if t.Kind != "tool" || !has(e.Routes, route) || e.Requests == e.Denied {
				continue
			}
			server = first(server, t.Server)
			ca := callers[e.From]
			switch {
			case ca.Identity != "":
				if ns, sa, ok := spiffeSA(ca.Identity); ok {
					k := fmt.Sprintf("source.identity.namespace == %q && source.identity.serviceAccount == %q", ns, sa)
					byCaller[k] = union(byCaller[k], []string{t.Label})
				}
			case len(ca.Users) == 1:
				k := fmt.Sprintf("jwt.sub == %q", ca.Users[0])
				byCaller[k] = union(byCaller[k], []string{t.Label})
			default:
				anon = append(anon, ca.Label)
			}
		}
		if len(byCaller) == 0 {
			continue
		}
		group, kind, name, pgroup, pkind, ns := "agentgateway.dev", "AgentgatewayBackend", "<mcp backend behind "+route+">", "agentgateway.dev/v1alpha1", "AgentgatewayPolicy", first(strings.Split(route, "/")[0], w.ns)
		for _, g := range []struct{ group, res, kind, pv, pk string }{{"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends", "EnterpriseAgentgatewayBackend", "enterpriseagentgateway.solo.io/v1alpha1", "EnterpriseAgentgatewayPolicy"}, {"agentgateway.dev", "agentgatewaybackends", "AgentgatewayBackend", "agentgateway.dev/v1alpha1", "AgentgatewayPolicy"}} {
			for _, b := range c.list(g.group, g.res) {
				if b.Namespace == ns && (b.Name == lastPart(route) || b.Name == server) {
					group, kind, name, pgroup, pkind = g.group, g.kind, b.Name, g.pv, g.pk
				}
			}
		}
		var exprs []string
		for _, k := range sortedKeys(byCaller) {
			var q []string
			for _, t := range byCaller[k] {
				q = append(q, fmt.Sprintf("%q", t))
			}
			exprs = append(exprs, fmt.Sprintf("          - '%s && mcp.tool.name in [%s]'", k, strings.Join(q, ", ")))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# MCP server %s: each caller seen in the logs may call only the tools it called.\n", server)
		if len(anon) > 0 {
			fmt.Fprintf(&b, "# Calls with no identity (%s) get no rule, so they lose these tools.\n", strings.Join(union(nil, anon), ", "))
		}
		if strings.HasPrefix(name, "<") {
			b.WriteString("# The backend could not be found in the capture; name it before applying.\n")
		}
		fmt.Fprintf(&b, "apiVersion: %s\nkind: %s\nmetadata:\n  name: %s-tool-allowlist\n  namespace: %s\nspec:\n  targetRefs:\n  - {group: %s, kind: %s, name: %s}\n  backend:\n    mcp:\n      authorization:\n        action: Allow\n        policy:\n          matchExpressions:\n%s",
			pgroup, pkind, strings.Trim(strings.ReplaceAll(lastPart(first(server, route)), "_", "-"), "<>"), ns, group, kind, yamlStr(name), strings.Join(exprs, "\n"))
		out = append(out, b.String())
	}
	return out
}

// evaluateAgentWith evaluates one agent with an extra NetworkPolicy in place
// (replacing one of the same name) and the named egress policies removed.
func evaluateAgentWith(ws *store.Workspace, agentID string, np map[string]any, drop []string) (Agent, bool) {
	md := m(np["metadata"])
	ns, name := s(md["namespace"]), s(md["name"])
	for _, cl := range ws.Clusters() {
		c := newCluster(ws, cl)
		var agent *workload
		for _, w := range c.works {
			if w.id == agentID {
				agent = w
			}
		}
		if agent == nil {
			continue
		}
		var keep []*store.Object
		for _, p := range c.netpols {
			if (p.Namespace == ns && p.Name == name) || has(drop, p.Namespace+"/"+p.Name) {
				continue
			}
			keep = append(keep, p)
		}
		c.netpols = append(keep, &store.Object{Cluster: cl, Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true, Namespace: ns, Name: name, Obj: np})
		fs := c.observe(ws.Flows(cl))
		td := fs.td
		if td == "" {
			td = "cluster.local"
		}
		e := &engine{c: c, cache: map[string]hop{}, internet: map[*workload]string{}, td: td}
		agents := c.findAgents()
		for _, w := range agents {
			if w.id == agentID {
				return e.agent(w, agents, fs.byW[w], c.coverage(fs, w, c.accessLogsCaptured())), true
			}
		}
	}
	return Agent{}, false
}
