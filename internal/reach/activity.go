package reach

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
)

// Activity is what agents actually did through agentgateway: who called
// which MCP tool, model or agent, as whom, how often, what was refused and
// what it cost in tokens. It is read from agentgateway request logs (gateways
// and agentgateway waypoints), with callers resolved to workloads from the
// captured pod addresses and SPIFFE identities, and set against the tools the
// MCP servers declare. With a baseline it also says which tools appeared,
// went away or changed their description between the two captures.
type Activity struct {
	Callers  []ActCaller   `json:"callers"`
	Targets  []ActTarget   `json:"targets"`
	Edges    []ActEdge     `json:"edges"`
	Denials  []ActDenial   `json:"denials"`
	Tools    []ToolInfo    `json:"tools"`
	Servers  []ServerInfo  `json:"servers"`
	Findings []Finding     `json:"findings"`
	Changes  *ToolChanges  `json:"changes,omitempty"`
	Totals   ActTotals     `json:"totals"`
	Gateways []string      `json:"gateways"`
	Notes    []string      `json:"notes"`
	First    time.Time     `json:"first"`
	Last     time.Time     `json:"last"`
	Clusters []string      `json:"clusters"`
	Coverage []ActCoverage `json:"coverage"`
	// Reach says, for each agent, whether the network lets it reach the
	// Services its own configuration points its MCP servers at: agent ns/name
	// -> svc:<id> -> the verdict of NetworkPolicy and mesh authorization.
	Reach map[string]map[string]ActReach `json:"reach,omitempty"`
}

// ActReach is whether an agent can reach one Service, and why.
type ActReach struct {
	Verdict string `json:"verdict"` // allowed, denied, unknown
	Why     string `json:"why,omitempty"`
}

// ActTotals sums the whole report.
type ActTotals struct {
	Requests     int     `json:"requests"`
	ToolCalls    int     `json:"toolCalls"`
	LLMCalls     int     `json:"llmCalls"`
	Denied       int     `json:"denied"`
	Errors       int     `json:"errors"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Cost         float64 `json:"cost"`
	Anonymous    int     `json:"anonymous"` // tool, model and agent calls with no identity at all
}

// ActCoverage says which gateways logged requests, so an empty map can be
// told apart from a gateway whose logs were not captured.
type ActCoverage struct {
	Gateway  string `json:"gateway"`
	Pods     int    `json:"pods"`
	Requests int    `json:"requests"`
	Waypoint bool   `json:"waypoint"`
}

// ActCaller is who made calls: a workload, a mesh identity, a JWT subject or
// an address.
type ActCaller struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"` // workload, identity, user, outside, address
	Label     string   `json:"label"`
	Namespace string   `json:"namespace,omitempty"`
	Identity  string   `json:"identity,omitempty"` // SPIFFE ID
	Users     []string `json:"users,omitempty"`    // JWT subjects seen on its calls
	Addresses []string `json:"addresses,omitempty"`
	AgentType string   `json:"agentType,omitempty"` // set when the workload is an agent
	Ref       *Ref     `json:"ref,omitempty"`
	Requests  int      `json:"requests"`
	Denied    int      `json:"denied"`
	Errors    int      `json:"errors"`
	Tokens    int64    `json:"tokens"`
	Cost      float64  `json:"cost,omitempty"`
	// Anonymous: no mesh identity and no JWT subject on any of its calls.
	Anonymous bool `json:"anonymous"`
	// ServerServices is, for an agent, the Service its own configuration
	// reaches each MCP server at (MCP_SERVERS_CONFIG, or a declarative
	// Agent's RemoteMCPServer URL).
	ServerServices map[string][]string `json:"serverServices,omitempty"`
	// Gone: a service account that made calls but has no workload in the
	// capture any more (the agent was deleted).
	Gone bool `json:"gone,omitempty"`
	// Agentic: a workload or service account that called MCP tools, models
	// or agents. It marks agents whose workload is gone or was not captured,
	// which have no agent type of their own.
	Agentic bool `json:"agentic,omitempty"`
}

// ActTarget is what was called.
type ActTarget struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`           // tool, meta (run_code, get_tool), model, agent, mcp, http
	Mode         string   `json:"mode,omitempty"` // for meta: Code (run_code) or Search (get_tool)
	Label        string   `json:"label"`
	Server       string   `json:"server,omitempty"`   // MCP target, or the route that serves it
	Provider     string   `json:"provider,omitempty"` // LLM provider
	Routes       []string `json:"routes,omitempty"`
	Gateways     []string `json:"gateways,omitempty"`
	Methods      []string `json:"methods,omitempty"` // MCP or A2A methods
	Requests     int      `json:"requests"`
	Denied       int      `json:"denied"`
	Errors       int      `json:"errors"`
	InputTokens  int64    `json:"inputTokens,omitempty"`
	OutputTokens int64    `json:"outputTokens,omitempty"`
	Cost         float64  `json:"cost,omitempty"`
	// Declared is set for tools an MCP server in the capture lists; Undeclared
	// for tools called that no captured server lists (only said when some
	// server's tool list was captured).
	Declared    bool   `json:"declared"`
	Undeclared  bool   `json:"undeclared,omitempty"`
	Description string `json:"description,omitempty"`
}

// ActEdge is one caller calling one target.
type ActEdge struct {
	From         string         `json:"from"`
	To           string         `json:"to"`
	Service      string         `json:"service,omitempty"` // svc:<id> the calls went through: the waypoint's Service, or the gateway's
	Requests     int            `json:"requests"`
	Denied       int            `json:"denied"`
	Errors       int            `json:"errors"`
	Reasons      map[string]int `json:"reasons,omitempty"`  // agentgateway reason= values
	Statuses     map[string]int `json:"statuses,omitempty"` // HTTP status -> count
	InputTokens  int64          `json:"inputTokens,omitempty"`
	OutputTokens int64          `json:"outputTokens,omitempty"`
	Cost         float64        `json:"cost,omitempty"`
	Gateways     []string       `json:"gateways"`
	Routes       []string       `json:"routes,omitempty"`
	Users        []string       `json:"users,omitempty"`
	First        time.Time      `json:"first"`
	Last         time.Time      `json:"last"`
	Error        string         `json:"error,omitempty"` // one example
	// OutsideAllowlist: the caller is a kagent agent whose spec lists the
	// tools it may use, and this one is not among them.
	OutsideAllowlist bool `json:"outsideAllowlist,omitempty"`
}

// ActDenial is a group of refused calls, with the policies that could have
// refused them.
type ActDenial struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Caller   string    `json:"caller"`
	Target   string    `json:"target"`
	Gateway  string    `json:"gateway"`
	Route    string    `json:"route,omitempty"`
	Reason   string    `json:"reason"` // agentgateway reason, or the HTTP status when it gave none
	Status   string    `json:"status,omitempty"`
	Count    int       `json:"count"`
	Error    string    `json:"error,omitempty"`
	Last     time.Time `json:"last"`
	Meaning  string    `json:"meaning"`            // what the reason means, one sentence
	Policies []PolRef  `json:"policies,omitempty"` // agentgateway policies on the route, gateway or Service
	Look     string    `json:"look,omitempty"`     // what to check next
}

// PolRef is a policy that applies where a call was refused, and the sections
// it sets.
type PolRef struct {
	Ref      Ref      `json:"ref"`
	Target   string   `json:"target"`   // what it is attached to: Kind/ns/name
	Sections []string `json:"sections"` // traffic.jwtAuthentication, backend.mcp.authorization...
	Matches  bool     `json:"matches"`  // it has a section that produces this reason
}

// ToolInfo is one tool an MCP server declares, or one that was called.
type ToolInfo struct {
	Server      string   `json:"server"` // ns/name of the kagent server, or the MCP target seen in logs
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Declared    bool     `json:"declared"`
	Calls       int      `json:"calls"`
	Callers     []string `json:"callers,omitempty"`
	Ref         *Ref     `json:"ref,omitempty"`
	// AllowedFor lists who an agentgateway MCP authorization rule allows to
	// call it ("kagent/demo", "jwt alice", "any caller"), with the policy.
	AllowedFor []string `json:"allowedFor,omitempty"`
	// DeniedFor lists who an MCP authorization rule with action Deny refuses.
	DeniedFor []string `json:"deniedFor,omitempty"`
	Policies  []Ref    `json:"policies,omitempty"`
	// ConfiguredOn lists the agents whose configuration names it (a kagent
	// Agent's toolNames, or an ALLOWED_TOOLS variable).
	ConfiguredOn []string `json:"configuredOn,omitempty"`
}

// ServerInfo says what the capture knows about one MCP server's tools.
type ServerInfo struct {
	Name       string `json:"name"`
	Listed     int    `json:"listed"`     // tools in its discovered list (kagent)
	Allowed    int    `json:"allowed"`    // tools an allowlist names
	Configured int    `json:"configured"` // tools an agent is configured for
	Called     int    `json:"called"`
	HasList    bool   `json:"hasList"` // its own tool list was captured
	Known      int    `json:"known"`   // tools the capture names for it, from any source
	// ToolMode is how the agentgateway backend exposes the server now:
	// Standard, Search (get_tool and invoke_tool), Code (run_code) or
	// CodeSearch (get_tool and run_code). Empty when no backend names it.
	ToolMode string `json:"toolMode,omitempty"`
	// Meta counts calls to the meta-tools agentgateway gave agents instead of
	// the server's tools: run_code (Code Mode) and get_tool (Search).
	// invoke_tool is logged as the tool it invoked, so it is not here.
	Meta map[string]int `json:"meta,omitempty"`
	// Services are the Services (svc:<id>) the server is reached through:
	// its kagent URL, an agent's MCP_SERVERS_CONFIG URL, or the Service its
	// calls went through.
	Services []string `json:"services,omitempty"`
	// Guarded: an accepted agentgateway MCP authorization rule with action
	// Allow covers the server, so a caller may call only what it names. A
	// rule that names no tool for anyone (false) guards it too: it allows
	// nothing.
	Guarded bool `json:"guarded,omitempty"`
	// AllowAll and DenyAll list callers a rule allows or refuses every tool
	// of the server, with no tool named.
	AllowAll []string `json:"allowAll,omitempty"`
	DenyAll  []string `json:"denyAll,omitempty"`
	Policies []Ref    `json:"policies,omitempty"` // the MCP authorization rules on it
	// Unread are rule expressions Flightbay could not read; what they allow
	// is not shown.
	Unread []string `json:"unread,omitempty"`
}

// ToolChanges compares tools and callers with a baseline capture.
type ToolChanges struct {
	Baseline     string       `json:"baseline"`
	Added        []ToolInfo   `json:"added"`
	Removed      []ToolInfo   `json:"removed"`
	Described    []ToolChange `json:"described"` // same tool, new description
	NewlyCalled  []string     `json:"newlyCalled"`
	NewCallers   []string     `json:"newCallers"` // caller -> target pairs not seen before
	Compared     bool         `json:"compared"`   // both captures had some tool list or tool calls
	BaselineNote string       `json:"baselineNote,omitempty"`
}

// ToolChange is a tool whose description changed.
type ToolChange struct {
	Server string `json:"server"`
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Denied reasons: agentgateway refused the call on policy.
var denyReasons = map[string]string{
	"JwtAuth":       "The request had no JWT, or its JWT did not validate: wrong issuer or audience, expired, or signed with a key the JWKS does not hold.",
	"Oidc":          "The OIDC flow failed: no session, or the identity provider refused the login.",
	"BasicAuth":     "The Basic authentication credentials were missing or did not match a user in the policy.",
	"APIKeyAuth":    "The API key was missing or not one the policy accepts.",
	"ExtAuth":       "The external authorisation service refused the request.",
	"Authorization": "An authorization rule refused the caller: no allow rule matched, or a deny rule did. For MCP, this includes a tool the caller may not call.",
	"RateLimit":     "The caller went over a rate limit (local, global or token budget).",
	"Guardrail":     "A prompt guard or guardrail rejected the request or the response.",
}

// Error reasons: the call failed for a reason other than policy.
var errorReasons = map[string]string{
	"MCP":              "The MCP backend returned an error, or the MCP request was invalid.",
	"UpstreamFailure":  "The call to the backend failed.",
	"Timeout":          "The request or the backend call timed out.",
	"NoHealthyBackend": "There was no healthy backend to send it to.",
	"NotFound":         "No route, listener or Service matched the request.",
	"Internal":         "agentgateway failed while processing the request.",
	"ExtProc":          "The external processor failed.",
	"DirectResponse":   "A direct response configured on the route answered it.",
}

// The policy sections that produce each denial reason.
var reasonSections = map[string][]string{
	"JwtAuth":       {"traffic.jwtAuthentication", "backend.mcp.authentication"},
	"Oidc":          {"traffic.oidc"},
	"BasicAuth":     {"traffic.basicAuthentication"},
	"APIKeyAuth":    {"traffic.apiKeyAuthentication"},
	"ExtAuth":       {"traffic.extAuth", "traffic.entExtAuth", "backend.extAuth"},
	"Authorization": {"traffic.authorization", "backend.mcp.authorization", "traffic.csrf"},
	"RateLimit":     {"traffic.rateLimit", "traffic.entRateLimit", "traffic.entBudgetEnforcement"},
	"Guardrail":     {"backend.ai.promptGuard", "backend.ai.entPromptGuard"},
}

var reasonLook = map[string]string{
	"JwtAuth":       "Compare the token's iss and aud with the policy's issuer and audiences, and check the JWKS it points at is reachable.",
	"Authorization": "Read the policy's CEL rules against the caller's identity or claims, and the tool name for MCP.",
	"RateLimit":     "Check the limit and what it is keyed on; a shared key makes every caller share one budget.",
	"ExtAuth":       "Read the external authorisation service's logs for the same time.",
	"Guardrail":     "Read the guard's rules and the gateway logs around the request.",
	"APIKeyAuth":    "Check the key is in the Secret the policy reads.",
	"BasicAuth":     "Check the user is in the Secret the policy reads.",
}

func isDenied(reason, status string) bool {
	if _, ok := denyReasons[reason]; ok {
		return true
	}
	return reason == "" && (status == "401" || status == "403" || status == "429")
}

func isError(reason, status string) bool {
	if isDenied(reason, status) {
		return false
	}
	if _, ok := errorReasons[reason]; ok && reason != "DirectResponse" {
		return true
	}
	n, _ := strconv.Atoi(status)
	return n >= 500
}

// EvaluateActivity builds the activity report. baseline may be nil; name
// labels it in the report.
func EvaluateActivity(ws, baseline *store.Workspace, baselineName string) Activity {
	a := buildActivity(ws)
	if baseline != nil {
		b := buildActivity(baseline)
		a.Changes = compareTools(b, a, baselineName)
		if a.Changes != nil {
			a.Findings = append(a.Findings, changeFindings(a.Changes)...)
		}
	}
	sortFindings(a.Findings)
	return a
}

func sortFindings(fs []Finding) {
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.SliceStable(fs, func(i, j int) bool { return rank[fs[i].Severity] < rank[fs[j].Severity] })
}

type actBuilder struct {
	a        *Activity
	callers  map[string]*ActCaller
	targets  map[string]*ActTarget
	edges    map[[2]string]*ActEdge
	denials  map[string]*ActDenial
	tools    map[string]*ToolInfo // server/name
	cov      map[string]*ActCoverage
	pods     map[string]map[string]bool
	allow    map[string]map[string]bool // caller id -> tools its kagent Agent lists
	servers  map[string]string          // names a captured MCP server goes by -> its ns/name
	clusters []*cluster
	pending  []pendingTool                  // configured tools whose server is decided once everything is read
	svcOf    map[string][]string            // server -> the Services it is reached through
	agentSvc map[string]map[string][]string // agent ns/name -> server -> Services its config points at
	// From agentgateway MCP authorization rules, per server.
	guard    map[string][]Ref    // accepted Allow rules covering it
	allowAll map[string][]string // callers allowed every tool
	denyAll  map[string][]string // callers refused every tool
	unread   map[string][]string // expressions not read
	noNS     []FindingItem       // service accounts named without a namespace
}

// pendingTool is a tool an agent is configured for, with the servers it
// could be on (the agent's MCP_SERVERS_CONFIG).
type pendingTool struct {
	who, name  string
	candidates []string
}

func buildActivity(ws *store.Workspace) Activity {
	a := Activity{Callers: []ActCaller{}, Targets: []ActTarget{}, Edges: []ActEdge{}, Denials: []ActDenial{}, Tools: []ToolInfo{},
		Findings: []Finding{}, Gateways: []string{}, Notes: []string{}, Clusters: ws.Clusters(), Coverage: []ActCoverage{}, Servers: []ServerInfo{}}
	b := &actBuilder{a: &a, callers: map[string]*ActCaller{}, targets: map[string]*ActTarget{}, edges: map[[2]string]*ActEdge{},
		denials: map[string]*ActDenial{}, tools: map[string]*ToolInfo{}, cov: map[string]*ActCoverage{}, pods: map[string]map[string]bool{}, allow: map[string]map[string]bool{}, servers: map[string]string{}, svcOf: map[string][]string{}, agentSvc: map[string]map[string][]string{},
		guard: map[string][]Ref{}, allowAll: map[string][]string{}, denyAll: map[string][]string{}, unread: map[string][]string{}}
	inventory := false
	for _, cl := range ws.Clusters() {
		c := newCluster(ws, cl)
		agents := map[*workload]string{}
		for _, w := range c.findAgents() {
			agents[w] = w.agentType
		}
		if b.inventory(c) {
			inventory = true
		}
		b.allowlists(c)
		b.policyTools(c)
		b.configuredTools(c)
		b.clusters = append(b.clusters, c)
		for _, r := range ws.AgentgatewayRequests(cl) {
			b.add(c, agents, r)
		}
	}
	b.finish(inventory)
	return a
}

// inventory reads the tools kagent MCP servers declare (status.discoveredTools).
func (b *actBuilder) inventory(c *cluster) bool {
	found := false
	for _, g := range []struct{ group, res string }{{"kagent.dev", "remotemcpservers"}, {"kagent.dev", "mcpservers"}, {"kagent.dev", "toolservers"}} {
		for _, o := range c.list(g.group, g.res) {
			ts := l(path(o.Obj, "status", "discoveredTools"))
			if len(ts) > 0 {
				found = true
				srv := key(o.Namespace, o.Name)
				b.servers[o.Name], b.servers[srv] = srv, srv
				if u := s(path(o.Obj, "spec", "url")); u != "" {
					host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
					host, _, _ = strings.Cut(host, "/")
					host = stripPort(host)
					b.addSvc(c, srv, host)
					b.servers[host] = srv
					if p := strings.Split(host, "."); len(p) >= 2 {
						b.servers[key(p[1], p[0])] = srv
					}
				}
			}
			for _, t := range ts {
				tm := m(t)
				name := s(tm["name"])
				if name == "" {
					continue
				}
				srv := key(o.Namespace, o.Name)
				ti := b.tool(srv, name)
				ti.Declared, ti.Description, ti.Ref = true, s(tm["description"]), refOf(o)
			}
		}
	}
	return found
}

// allowlists reads the tools each declarative kagent Agent lists.
func (b *actBuilder) allowlists(c *cluster) {
	for _, ag := range c.list("kagent.dev", "agents") {
		tools := l(path(ag.Obj, "spec", "declarative", "tools"))
		if len(tools) == 0 {
			continue
		}
		names := map[string]bool{}
		open := false
		for _, t := range tools {
			ms := m(m(t)["mcpServer"])
			if ms == nil {
				continue
			}
			tn := strList(l(ms["toolNames"]))
			if len(tn) == 0 {
				open = true // every tool of the server
			}
			for _, n := range tn {
				names[n] = true
			}
		}
		if open || len(names) == 0 {
			continue
		}
		for _, w := range c.works {
			if w.ns == ag.Namespace && w.name == ag.Name {
				b.allow[w.id] = names
			}
		}
	}
}

func (b *actBuilder) tool(server, name string) *ToolInfo {
	k := server + "/" + name
	if t := b.tools[k]; t != nil {
		return t
	}
	t := &ToolInfo{Server: server, Name: name}
	b.tools[k] = t
	return t
}

// caller resolves who made a request: the mesh identity agentgateway saw
// (waypoints log src.identity), else the source address, with the JWT
// subject kept as the user.
func (b *actBuilder) caller(c *cluster, agents map[*workload]string, f map[string]string, at time.Time) *ActCaller {
	ident, user, addr := f["src.identity"], f["jwt.sub"], stripPort(f["src.addr"])
	var w *workload
	ns, sa, hasID := spiffeSA(ident)
	if hasID {
		// The mesh identity is verified by mTLS; the address is not. With an
		// identity, the address only picks between workloads that share it.
		var hits []*workload
		for _, x := range c.works {
			if x.ns == ns && x.sa == sa {
				hits = append(hits, x)
			}
		}
		switch {
		case len(hits) == 1:
			w = hits[0]
		case len(hits) > 1 && addr != "":
			if byIP := c.podAt(addr, at); byIP != nil && byIP.ns == ns && byIP.sa == sa {
				w = byIP
			}
		}
	} else if addr != "" {
		w = c.podAt(addr, at)
	}
	var id string
	var ca ActCaller
	switch {
	case w != nil:
		id = "w:" + w.id
		ca = ActCaller{Kind: "workload", Label: w.ns + "/" + w.name, Namespace: w.ns, AgentType: agents[w], Ref: refOf(w.obj)}
		if ident != "" {
			ca.Identity = ident
		}
	case ident != "":
		id = "id:" + ident
		ca = ActCaller{Kind: "identity", Label: ns + "/" + sa, Namespace: ns, Identity: ident}
	case user != "":
		id = "user:" + user
		ca = ActCaller{Kind: "user", Label: "JWT subject " + user}
	case addr != "" && c.ips().node[addr]:
		id = "node:" + c.name + "/" + addr
		ca = ActCaller{Kind: "outside", Label: "outside the cluster, through node " + addr}
	case addr != "" && isNodeGatewayIP(addr):
		id = "node:" + c.name + "/" + addr
		ca = ActCaller{Kind: "outside", Label: "outside the cluster, through " + addr}
	case addr != "":
		id = "ip:" + c.name + "/" + addr
		ca = ActCaller{Kind: "address", Label: addr}
	default:
		id = "unknown"
		ca = ActCaller{Kind: "address", Label: "unknown caller"}
	}
	if x := b.callers[id]; x != nil {
		return x
	}
	ca.ID, ca.Anonymous = id, true
	b.callers[id] = &ca
	return &ca
}

// podAt returns the workload whose pod had this address at time t. Pod IPs
// are reused, and logs can be older than the pod that holds the address now:
// a pod that started after t did not make the request.
func (c *cluster) podAt(addr string, t time.Time) *workload {
	w := c.ips().pod[addr]
	if w == nil || t.IsZero() {
		return w
	}
	for _, p := range w.pods {
		st := m(p.Obj["status"])
		if s(st["podIP"]) != addr {
			continue
		}
		start, err := time.Parse(time.RFC3339, first(s(st["startTime"]), s(path(p.Obj, "metadata", "creationTimestamp"))))
		if err == nil && start.After(t.Add(time.Minute)) {
			return nil
		}
	}
	return w
}

// isNodeGatewayIP: the .1 address of a pod range is the node's bridge on kind
// and most CNIs; traffic from outside the cluster arrives from it once SNATed.
func isNodeGatewayIP(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.To4() != nil && p.To4()[3] == 1
}

// target works out what a request called.
func target(f map[string]string) (id string, t ActTarget) {
	route := f["route"]
	mcpMethod := first(f["mcp.method.name"], f["mcp.method"])
	tool := first(f["gen_ai.tool.name"], f["mcp.tool.name"], f["mcp.tool"])
	server := first(f["mcp.target"], route, f["http.host"])
	model := first(f["gen_ai.response.model"], f["gen_ai.request.model"])
	provider := first(f["gen_ai.provider.name"], f["llm.provider"])
	switch {
	case mcpMethod == "tools/call" && tool != "":
		name := tool
		if tg := f["mcp.target"]; tg != "" && strings.HasPrefix(name, tg+"_") {
			name = strings.TrimPrefix(name, tg+"_") // multiplexed MCP prefixes the target
		}
		return "tool:" + server + "/" + name, ActTarget{Kind: "tool", Label: name, Server: server}
	case mcpMethod != "" || strings.EqualFold(f["protocol"], "mcp"):
		return "mcp:" + server, ActTarget{Kind: "mcp", Label: server, Server: server}
	case model != "" || provider != "" || strings.EqualFold(f["protocol"], "llm"):
		label := model
		if label == "" {
			label = "(model not logged)"
		}
		return "model:" + provider + "/" + label, ActTarget{Kind: "model", Label: label, Provider: provider}
	case f["a2a.method"] != "" || strings.EqualFold(f["protocol"], "a2a"):
		return "agent:" + first(route, f["http.host"]), ActTarget{Kind: "agent", Label: first(route, f["http.host"])}
	}
	return "http:" + first(route, f["http.host"], "unrouted"), ActTarget{Kind: "http", Label: first(route, f["http.host"], "unrouted")}
}

func (b *actBuilder) add(c *cluster, agents map[*workload]string, r store.AgwRequest) {
	f := r.Fields
	gw := first(f["gateway"], r.Namespace+"/"+r.Pod)
	cv := b.cov[gw]
	if cv == nil {
		cv = &ActCoverage{Gateway: gw, Waypoint: f["listener"] == "waypoint" || strings.Contains(gw, "waypoint")}
		b.cov[gw] = cv
		b.pods[gw] = map[string]bool{}
	}
	cv.Requests += r.Count
	b.pods[gw][r.Namespace+"/"+r.Pod] = true

	ca := b.caller(c, agents, f, r.First)
	tid, tt := target(f)
	// Code and Search modes give agents meta-tools instead of the server's
	// tools. run_code is logged under the target "code_mode"; its server is
	// the one the route's backend names. get_tool is a lookup, unless the
	// server really has a tool of that name.
	if tt.Kind == "tool" && (tt.Label == "run_code" || tt.Label == "get_tool") {
		srv := tt.Server
		if srvs := c.routeServers(f["route"]); len(srvs) > 0 && (srv == "code_mode" || !has(srvs, srv)) {
			srv = strings.Join(srvs, ",")
		}
		real := b.tools[srv+"/"+tt.Label]
		if tt.Label == "run_code" || real == nil || (!real.Declared && len(real.AllowedFor) == 0) {
			mode := map[string]string{"run_code": "Code", "get_tool": "Search"}[tt.Label]
			tt = ActTarget{Kind: "meta", Mode: mode, Label: tt.Label, Server: srv}
			tid = "meta:" + srv + "/" + tt.Label
		}
	}
	t := b.targets[tid]
	if t == nil {
		tt.ID = tid
		t = &tt
		b.targets[tid] = t
	}
	status, reason := f["http.status"], f["reason"]
	denied, failed := isDenied(reason, status), isError(reason, status)
	if f["src.identity"] != "" || f["jwt.sub"] != "" {
		ca.Anonymous = false
	}
	if u := f["jwt.sub"]; u != "" {
		ca.Users = union(ca.Users, []string{u})
	}
	if a := stripPort(f["src.addr"]); a != "" && ca.Kind != "workload" {
		ca.Addresses = union(ca.Addresses, []string{a})
	}
	tokens := r.InputTokens + r.OutputTokens
	ca.Requests += r.Count
	ca.Tokens += tokens
	ca.Cost += r.Cost
	t.Requests += r.Count
	t.InputTokens += r.InputTokens
	t.OutputTokens += r.OutputTokens
	t.Cost += r.Cost
	t.Gateways = union(t.Gateways, []string{gw})
	if rt := f["route"]; rt != "" {
		t.Routes = union(t.Routes, []string{rt})
	}
	if mth := first(f["mcp.method.name"], f["mcp.method"], f["a2a.method"]); mth != "" {
		t.Methods = union(t.Methods, []string{mth})
	}
	if t.Provider == "" {
		t.Provider = first(f["gen_ai.provider.name"], f["llm.provider"])
	}
	e := b.edges[[2]string{ca.ID, tid}]
	if e == nil {
		e = &ActEdge{From: ca.ID, To: tid, Reasons: map[string]int{}, Statuses: map[string]int{}, Gateways: []string{}}
		b.edges[[2]string{ca.ID, tid}] = e
	}
	if e.Service == "" {
		if sv := c.serviceOfCall(ActTarget{Routes: nonEmpty(f["route"]), Gateways: nonEmpty(f["gateway"])}); sv != nil {
			e.Service = "svc:" + sv.id
		}
	}
	if e.Service != "" && (t.Kind == "tool" || t.Kind == "meta") {
		for _, srv := range strings.Split(t.Server, ",") {
			b.svcOf[srv] = union(b.svcOf[srv], []string{e.Service})
		}
	}
	e.Requests += r.Count
	e.InputTokens += r.InputTokens
	e.OutputTokens += r.OutputTokens
	e.Cost += r.Cost
	e.Gateways = union(e.Gateways, []string{gw})
	if rt := f["route"]; rt != "" {
		e.Routes = union(e.Routes, []string{rt})
	}
	if u := f["jwt.sub"]; u != "" {
		e.Users = union(e.Users, []string{u})
	}
	if status != "" {
		e.Statuses[status] += r.Count
	}
	if reason != "" {
		e.Reasons[reason] += r.Count
	}
	if e.First.IsZero() || (!r.First.IsZero() && r.First.Before(e.First)) {
		e.First = r.First
	}
	if r.Last.After(e.Last) {
		e.Last = r.Last
	}
	if r.Error != "" && (denied || failed) {
		e.Error = r.Error
	}
	b.a.Totals.Requests += r.Count
	b.a.Totals.InputTokens += r.InputTokens
	b.a.Totals.OutputTokens += r.OutputTokens
	b.a.Totals.Cost += r.Cost
	switch t.Kind {
	case "tool":
		b.a.Totals.ToolCalls += r.Count
		// A call to a kagent server is logged under its MCP target name; file
		// it with the server's own tool list when the name, route or host
		// matches one.
		srv := t.Server
		for _, k := range []string{t.Server, f["route"], lastPart(f["route"])} {
			if x := b.servers[k]; k != "" && x != "" {
				srv = x
				break
			}
		}
		ti := b.tool(srv, t.Label)
		ti.Calls += r.Count
		ti.Callers = union(ti.Callers, []string{ca.Label})
		if allowed, ok := b.allow[strings.TrimPrefix(ca.ID, "w:")]; ok && !allowed[t.Label] {
			e.OutsideAllowlist = true
		}
	case "meta":
		b.a.Totals.ToolCalls += r.Count
	case "model":
		b.a.Totals.LLMCalls += r.Count
	}
	if denied {
		ca.Denied += r.Count
		t.Denied += r.Count
		e.Denied += r.Count
		b.a.Totals.Denied += r.Count
		b.denial(c, ca, t, gw, f, r)
	} else if failed {
		ca.Errors += r.Count
		t.Errors += r.Count
		e.Errors += r.Count
		b.a.Totals.Errors += r.Count
	}
	if !r.First.IsZero() && (b.a.First.IsZero() || r.First.Before(b.a.First)) {
		b.a.First = r.First
	}
	if r.Last.After(b.a.Last) {
		b.a.Last = r.Last
	}
}

func (b *actBuilder) denial(c *cluster, ca *ActCaller, t *ActTarget, gw string, f map[string]string, r store.AgwRequest) {
	reason := f["reason"]
	if reason == "" {
		reason = "HTTP " + f["http.status"]
	}
	k := strings.Join([]string{ca.ID, t.ID, gw, f["route"], reason}, "|")
	d := b.denials[k]
	if d == nil {
		d = &ActDenial{From: ca.ID, To: t.ID, Caller: ca.Label, Target: t.Label, Gateway: gw, Route: f["route"], Reason: reason, Status: f["http.status"]}
		switch {
		case denyReasons[reason] != "":
			d.Meaning = denyReasons[reason]
		case f["http.status"] == "401":
			d.Meaning = "Refused as unauthenticated (401). The log gave no reason; an authentication policy on the route or gateway is the likely cause."
		case f["http.status"] == "403":
			d.Meaning = "Refused as forbidden (403). The log gave no reason; an authorization policy on the route or gateway is the likely cause."
		case f["http.status"] == "429":
			d.Meaning = "Refused as over a limit (429)."
		}
		d.Look = reasonLook[reason]
		d.Policies = c.policiesAt(gw, f["route"], f["http.host"], reason)
		b.denials[k] = d
	}
	d.Count += r.Count
	if r.Error != "" {
		d.Error = r.Error
	}
	if r.Last.After(d.Last) {
		d.Last = r.Last
	}
}

// policiesAt lists the agentgateway policies attached to a route, its
// gateway, or (for a waypoint) the Service it serves, with the sections each
// sets and whether one of them produces this denial reason.
func (c *cluster) policiesAt(gateway, route, host, reason string) []PolRef {
	targets := map[string]bool{}
	add := func(kind, nsName string) {
		if nsName != "" && strings.Contains(nsName, "/") {
			targets[kind+"/"+nsName] = true
		}
	}
	add("Gateway", gateway)
	add("HTTPRoute", route)
	add("GRPCRoute", route)
	add("Service", route) // waypoint logs name the Service as the route
	if h := strings.Split(host, "."); len(h) >= 2 && !strings.Contains(host, ":") && net.ParseIP(host) == nil {
		add("Service", h[1]+"/"+h[0])
	}
	var out []PolRef
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, p := range c.list(g.group, g.res) {
			sp := m(p.Obj["spec"])
			for _, tr := range l(sp["targetRefs"]) {
				k := s(m(tr)["kind"]) + "/" + p.Namespace + "/" + s(m(tr)["name"])
				if !targets[k] {
					continue
				}
				pr := PolRef{Ref: *refOf(p), Target: k, Sections: policySections(sp)}
				for _, want := range reasonSections[reason] {
					if has(pr.Sections, want) {
						pr.Matches = true
					}
				}
				out = append(out, pr)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Matches && !out[j].Matches })
	return out
}

func policySections(sp map[string]any) []string {
	var out []string
	for _, top := range []string{"traffic", "backend", "frontend"} {
		sec := m(sp[top])
		keys := make([]string, 0, len(sec))
		for k := range sec {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, top+"."+k)
			if k == "mcp" || k == "ai" {
				sub := m(sec[k])
				var sk []string
				for x := range sub {
					sk = append(sk, x)
				}
				sort.Strings(sk)
				for _, x := range sk {
					out = append(out, top+"."+k+"."+x)
				}
			}
		}
	}
	return out
}

func (b *actBuilder) finish(inventory bool) {
	a := b.a
	for _, cv := range b.cov {
		cv.Pods = len(b.pods[cv.Gateway])
		a.Coverage = append(a.Coverage, *cv)
		a.Gateways = append(a.Gateways, cv.Gateway)
	}
	sort.Slice(a.Coverage, func(i, j int) bool { return a.Coverage[i].Requests > a.Coverage[j].Requests })
	sort.Strings(a.Gateways)
	for _, t := range b.targets {
		if t.Kind == "tool" {
			// A call is checked against the one server it can be matched to by
			// name, URL host or route; with no match there is nothing to check
			// it against.
			if srv := b.serverOf(t); srv != "" {
				if ti := b.tools[srv+"/"+t.Label]; ti != nil && ti.Declared {
					t.Declared, t.Description = true, ti.Description
				} else {
					t.Undeclared = true
				}
			} else if ti := b.declaredTool(t.Label); ti != nil {
				t.Declared, t.Description = true, ti.Description
			}
		}
		a.Targets = append(a.Targets, *t)
	}
	sort.Slice(a.Targets, func(i, j int) bool {
		if a.Targets[i].Requests != a.Targets[j].Requests {
			return a.Targets[i].Requests > a.Targets[j].Requests
		}
		return a.Targets[i].ID < a.Targets[j].ID
	})
	for _, e := range b.edges {
		if t := b.targets[e.To]; t != nil && (t.Kind == "tool" || t.Kind == "meta" || t.Kind == "model" || t.Kind == "agent") {
			if ca := b.callers[e.From]; ca != nil && (ca.Kind == "workload" || ca.Kind == "identity") && e.Requests > e.Denied {
				ca.Agentic = true
			}
		}
	}
	b.placeConfigured()
	b.applyServerRules()
	b.configuredReach()
	b.serverSummary()
	for _, ca := range b.callers {
		if m := b.agentSvc[ca.Label]; m != nil && (ca.Kind == "workload" || ca.Kind == "identity") {
			ca.ServerServices = m
		}
		switch ca.Kind {
		case "identity":
			ca.Gone = !b.hasServiceAccount(ca.Identity)
		case "address":
			// A pod address no pod in the capture holds: the pod is gone.
			ca.Gone = b.podAddressGone(ca.Label)
		}
	}
	for _, ca := range b.callers {
		if ca.Anonymous {
			for _, e := range b.edges {
				if e.From == ca.ID && !strings.HasPrefix(e.To, "http:") {
					a.Totals.Anonymous += e.Requests
				}
			}
		}
		a.Callers = append(a.Callers, *ca)
	}
	sort.Slice(a.Callers, func(i, j int) bool {
		if a.Callers[i].Requests != a.Callers[j].Requests {
			return a.Callers[i].Requests > a.Callers[j].Requests
		}
		return a.Callers[i].ID < a.Callers[j].ID
	})
	for _, e := range b.edges {
		if len(e.Reasons) == 0 {
			e.Reasons = nil
		}
		if len(e.Statuses) == 0 {
			e.Statuses = nil
		}
		a.Edges = append(a.Edges, *e)
	}
	sort.Slice(a.Edges, func(i, j int) bool {
		if a.Edges[i].Requests != a.Edges[j].Requests {
			return a.Edges[i].Requests > a.Edges[j].Requests
		}
		return a.Edges[i].From+a.Edges[i].To < a.Edges[j].From+a.Edges[j].To
	})
	for _, d := range b.denials {
		a.Denials = append(a.Denials, *d)
	}
	sort.Slice(a.Denials, func(i, j int) bool {
		if a.Denials[i].Count != a.Denials[j].Count {
			return a.Denials[i].Count > a.Denials[j].Count
		}
		return a.Denials[i].From+a.Denials[i].To < a.Denials[j].From+a.Denials[j].To
	})
	for _, t := range b.tools {
		a.Tools = append(a.Tools, *t)
	}
	sort.Slice(a.Tools, func(i, j int) bool {
		if a.Tools[i].Calls != a.Tools[j].Calls {
			return a.Tools[i].Calls > a.Tools[j].Calls
		}
		return a.Tools[i].Server+a.Tools[i].Name < a.Tools[j].Server+a.Tools[j].Name
	})
	if len(b.cov) == 0 {
		a.Notes = append(a.Notes, "no agentgateway request logs in this capture: either no agentgateway ran, or its pods' logs were not collected")
	}
	if !inventory {
		a.Notes = append(a.Notes, "no MCP server tool list was captured (kagent RemoteMCPServer or MCPServer status), so called tools cannot be checked against what the servers declare")
	}
	if a.Totals.LLMCalls > 0 && a.Totals.InputTokens+a.Totals.OutputTokens == 0 {
		a.Notes = append(a.Notes, "LLM calls were logged without token counts")
	}
	a.Findings = append(a.Findings, b.findings()...)
	a.Findings = append(a.Findings, b.unenforcedLists()...)
	a.Findings = append(a.Findings, b.noNamespaceFinding()...)
}

// serverOf matches a called tool to a captured MCP server.
func (b *actBuilder) serverOf(t *ActTarget) string {
	keys := []string{t.Server}
	keys = append(keys, t.Routes...)
	for _, r := range t.Routes {
		keys = append(keys, lastPart(r))
	}
	for _, k := range keys {
		if srv := b.servers[k]; srv != "" {
			return srv
		}
	}
	return ""
}

func (b *actBuilder) declaredTool(name string) *ToolInfo {
	for _, t := range b.tools {
		if t.Declared && t.Name == name {
			return t
		}
	}
	return nil
}

const activityGuide = "https://mastertheagent.com/solo/agentgateway-mcp-authz-kind/"

func (b *actBuilder) findings() []Finding {
	a := b.a
	var out []Finding
	label := func(id string) string {
		if c := b.callers[id]; c != nil {
			return c.Label
		}
		if t := b.targets[id]; t != nil {
			if t.Server != "" && t.Kind == "tool" {
				return t.Server + "/" + t.Label
			}
			return t.Label
		}
		return id
	}
	// Anonymous calls to tools, models and agents.
	var anon []FindingItem
	for _, e := range a.Edges {
		ca := b.callers[e.From]
		if ca == nil || !ca.Anonymous || strings.HasPrefix(e.To, "http:") || e.Requests == e.Denied {
			continue
		}
		anon = append(anon, FindingItem{Target: label(e.To), Access: fmt.Sprintf("%d calls", e.Requests-e.Denied), Note: "from " + ca.Label + " through " + strings.Join(e.Gateways, ", ")})
	}
	if len(anon) > 0 {
		out = append(out, Finding{Severity: "high", Title: "Tools, models or agents answered calls that carried no identity",
			Detail: "These calls reached their target with neither a mesh identity nor a JWT subject in the log, so nobody can say who made them, and per-caller limits and authorization cannot apply.",
			Items:  anon, Advice: []string{"Require JWT authentication on the route (traffic.jwtAuthentication, mode Strict), or send agents through an agentgateway waypoint so their mesh identity is checked."},
			Source: activityGuide, Docs: "https://docs.solo.io/agentgateway/kubernetes/latest/security/jwt/"})
	}
	// Tools outside an agent's allowlist.
	var outside []FindingItem
	for _, e := range a.Edges {
		if e.OutsideAllowlist {
			outside = append(outside, FindingItem{Target: label(e.To), Access: fmt.Sprintf("%d calls", e.Requests), Note: "by " + label(e.From)})
		}
	}
	if len(outside) > 0 {
		out = append(out, Finding{Severity: "high", Title: "Agents called tools their Agent spec does not list",
			Detail: "The kagent Agent lists the tools it may use (toolNames), but these calls went to other tools. The list is only a prompt-time filter; enforce it at the gateway.",
			Items:  outside, Advice: []string{"Add an MCP authorization rule on the gateway (backend.mcp.authorization) that allows only the listed tools for this agent's identity."},
			Source: activityGuide, Docs: "https://docs.solo.io/agentgateway/kubernetes/latest/mcp/mcp-access/"})
	}
	// Tools called that no server declares.
	var undecl []FindingItem
	for _, t := range a.Targets {
		if t.Undeclared {
			undecl = append(undecl, FindingItem{Target: t.Server + "/" + t.Label, Access: fmt.Sprintf("%d calls", t.Requests)})
		}
	}
	if len(undecl) > 0 {
		out = append(out, Finding{Severity: "medium", Title: "Tools were called that their MCP server does not list",
			Detail: "The call was matched to a captured MCP server, and the server's discovered tool list does not have this tool. Its tool list may have changed after discovery, or the call went to a different server of the same name.",
			Items:  undecl, Source: activityGuide})
	}
	// Refusals.
	if len(a.Denials) > 0 {
		var items []FindingItem
		for _, d := range a.Denials {
			items = append(items, FindingItem{Target: d.Target, Class: d.Reason, Access: fmt.Sprintf("%d refused", d.Count), Note: d.Caller + " through " + d.Gateway})
		}
		out = append(out, Finding{Severity: "low", Title: fmt.Sprintf("%d calls were refused by gateway policy", a.Totals.Denied),
			Detail: "Refusals are the policy working, unless the caller should have been allowed. The Denials tab lists the policies at each gateway and what to check.",
			Items:  items, Source: activityGuide})
	}
	// Unattributed spend.
	var spend []FindingItem
	for _, e := range a.Edges {
		ca := b.callers[e.From]
		if ca != nil && ca.Anonymous && strings.HasPrefix(e.To, "model:") && e.InputTokens+e.OutputTokens > 0 {
			spend = append(spend, FindingItem{Target: label(e.To), Access: fmt.Sprintf("%d tokens", e.InputTokens+e.OutputTokens), Note: "from " + ca.Label})
		}
	}
	if len(spend) > 0 {
		out = append(out, Finding{Severity: "medium", Title: "LLM spend that cannot be put down to a caller",
			Detail: "These tokens were used by calls without an identity, so a per-caller budget or rate limit cannot apply to them.",
			Items:  spend, Source: "https://mastertheagent.com/solo/agentgateway-virtual-keys-kind/"})
	}
	return out
}

func actKeyTools(a Activity) map[string]ToolInfo {
	out := map[string]ToolInfo{}
	for _, t := range a.Tools {
		out[t.Server+"/"+t.Name] = t
	}
	return out
}

// compareTools diffs the tool lists and the calls of two captures.
func compareTools(before, after Activity, name string) *ToolChanges {
	ch := &ToolChanges{Baseline: name, Added: []ToolInfo{}, Removed: []ToolInfo{}, Described: []ToolChange{}, NewlyCalled: []string{}, NewCallers: []string{}}
	bt, at := actKeyTools(before), actKeyTools(after)
	declBefore, declAfter := map[string]bool{}, map[string]bool{}
	for _, t := range before.Tools {
		if t.Declared {
			declBefore[t.Server] = true
		}
	}
	for _, t := range after.Tools {
		if t.Declared {
			declAfter[t.Server] = true
		}
	}
	for k, t := range at {
		o, ok := bt[k]
		switch {
		case t.Declared && (!ok || !o.Declared) && declBefore[t.Server]:
			ch.Added = append(ch.Added, t)
		case t.Declared && ok && o.Declared && o.Description != t.Description:
			ch.Described = append(ch.Described, ToolChange{Server: t.Server, Name: t.Name, Before: o.Description, After: t.Description})
		}
		if t.Calls > 0 && (!ok || o.Calls == 0) && before.Totals.ToolCalls > 0 {
			ch.NewlyCalled = append(ch.NewlyCalled, t.Server+"/"+t.Name)
		}
	}
	for k, o := range bt {
		t, ok := at[k]
		if o.Declared && (!ok || !t.Declared) && declAfter[o.Server] {
			ch.Removed = append(ch.Removed, o)
		}
	}
	labels := func(a Activity) (map[string]string, map[string]string) {
		c, t := map[string]string{}, map[string]string{}
		for _, x := range a.Callers {
			c[x.ID] = x.Label
		}
		for _, x := range a.Targets {
			t[x.ID] = x.Label
			if x.Kind == "tool" {
				t[x.ID] = x.Server + "/" + x.Label
			}
		}
		return c, t
	}
	bc, btg := labels(before)
	ac, atg := labels(after)
	seen := map[string]bool{}
	for _, e := range before.Edges {
		seen[bc[e.From]+" → "+btg[e.To]] = true
	}
	for _, e := range after.Edges {
		if strings.HasPrefix(e.To, "http:") {
			continue
		}
		k := ac[e.From] + " → " + atg[e.To]
		if !seen[k] && before.Totals.ToolCalls+before.Totals.LLMCalls > 0 {
			ch.NewCallers = append(ch.NewCallers, k)
		}
	}
	ch.Compared = len(before.Tools) > 0 && len(after.Tools) > 0
	switch {
	case !ch.Compared:
		ch.BaselineNote = "one of the captures has no tool list and no tool calls, so tools cannot be compared"
	case before.Totals.ToolCalls+before.Totals.LLMCalls == 0 && after.Totals.ToolCalls+after.Totals.LLMCalls > 0:
		ch.BaselineNote = "the baseline's logs have no tool or model calls (its log window may not overlap any), so calls are not compared; tool lists are"
	}
	for _, xs := range [][]string{ch.NewlyCalled, ch.NewCallers} {
		sort.Strings(xs)
	}
	sort.Slice(ch.Added, func(i, j int) bool { return ch.Added[i].Server+ch.Added[i].Name < ch.Added[j].Server+ch.Added[j].Name })
	sort.Slice(ch.Removed, func(i, j int) bool {
		return ch.Removed[i].Server+ch.Removed[i].Name < ch.Removed[j].Server+ch.Removed[j].Name
	})
	sort.Slice(ch.Described, func(i, j int) bool {
		return ch.Described[i].Server+ch.Described[i].Name < ch.Described[j].Server+ch.Described[j].Name
	})
	return ch
}

func changeFindings(ch *ToolChanges) []Finding {
	var out []Finding
	if len(ch.Described) > 0 {
		var items []FindingItem
		for _, d := range ch.Described {
			items = append(items, FindingItem{Target: d.Server + "/" + d.Name, Note: "was: " + clip(d.Before, 120) + " | now: " + clip(d.After, 120)})
		}
		out = append(out, Finding{Severity: "high", Title: "MCP tools changed their description since the baseline",
			Detail: "A tool's description is part of the prompt the model reads. A description that changes without a new release is how a tool gets turned against its callers (a rug pull).",
			Items:  items, Advice: []string{"Confirm each change with the server's owner.", "Pin the server image by digest, and register servers through agentregistry so a change needs approval."},
			Source: "https://mastertheagent.com/solo/agentregistry-push-best-practices/"})
	}
	if len(ch.Added) > 0 {
		var items []FindingItem
		for _, t := range ch.Added {
			items = append(items, FindingItem{Target: t.Server + "/" + t.Name, Note: clip(t.Description, 140)})
		}
		out = append(out, Finding{Severity: "medium", Title: "MCP servers offer tools that were not there in the baseline",
			Detail: "New tools are new things an agent can be talked into doing. Check each is expected, and that tool-level authorization covers it.",
			Items:  items, Source: activityGuide})
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// first returns the first value that is set ("-" counts as unset, as in
// agentgateway's text logs).
func first(v ...string) string {
	for _, x := range v {
		if x != "" && x != "-" {
			return x
		}
	}
	return ""
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// policyTools reads the tools agentgateway MCP authorization rules allow or
// refuse, and for whom. Each rule is read into its ways through (see
// readMCPRule): the callers each admits and the tools it covers. A policy
// its controller did not accept is left out, as the gateway leaves it out.
func (b *actBuilder) policyTools(c *cluster) {
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, p := range c.list(g.group, g.res) {
			az := m(path(p.Obj, "spec", "backend", "mcp", "authorization"))
			if az == nil {
				continue
			}
			if why := notAccepted(p); why != "" {
				b.a.Notes = append(b.a.Notes, fmt.Sprintf("%s %s/%s has an MCP authorization rule, but %s, so it is not applied and its allowlist is left out", p.Kind, p.Namespace, p.Name, why))
				continue
			}
			deny := strings.EqualFold(s(az["action"]), "Deny")
			raw := c.policyServers(p)
			servers := b.canonicalServers(c, p.Namespace, raw)
			if !deny {
				for _, srv := range servers {
					if !hasRef(b.guard[srv], p) {
						b.guard[srv] = append(b.guard[srv], *refOf(p))
					}
				}
			}
			for _, expr := range strList(l(path(az, "policy", "matchExpressions"))) {
				clauses, ok := readMCPRule(expr)
				if !ok {
					for _, srv := range servers {
						b.unread[srv] = union(b.unread[srv], []string{strings.Join(strings.Fields(expr), " ")})
					}
					continue
				}
				for _, cl := range clauses {
					for _, sa := range cl.saNoNS {
						b.noNS = append(b.noNS, FindingItem{Target: p.Namespace + "/" + p.Name, Class: "service account " + sa, Access: strings.Join(servers, ", "),
							Note: fmt.Sprintf("the rule matches service account %s in any namespace; add source.identity.namespace == \"<namespace>\" to the same clause", sa)})
					}
					for _, srv := range servers {
						if cl.tools == nil {
							if deny {
								b.denyAll[srv] = union(b.denyAll[srv], cl.who)
							} else {
								b.allowAll[srv] = union(b.allowAll[srv], cl.who)
							}
							continue
						}
						for _, t := range cl.tools {
							// Multiplexed MCP prefixes tools with the target; a rule
							// often names both spellings.
							for _, pre := range append([]string{lastPart(srv)}, raw...) {
								t = strings.TrimPrefix(t, pre+"_")
							}
							ti := b.tool(srv, t)
							if deny {
								ti.DeniedFor = union(ti.DeniedFor, cl.who)
							} else {
								ti.AllowedFor = union(ti.AllowedFor, cl.who)
							}
							if !hasRef(ti.Policies, p) {
								ti.Policies = append(ti.Policies, *refOf(p))
							}
						}
					}
				}
			}
		}
	}
}

// canonicalServers files the servers a policy names under the names calls
// and tool lists use: a kagent server's ns/name when the target name, or the
// Service the policy is attached to, is how that server is reached.
func (b *actBuilder) canonicalServers(c *cluster, ns string, names []string) []string {
	var out []string
	for _, n := range names {
		if x := b.servers[n]; x != "" {
			out = union(out, []string{x})
			continue
		}
		found := false
		if sv := c.svcByKey[key(ns, n)]; sv != nil {
			for srv, svcs := range b.svcOf {
				for _, id := range svcs {
					if id == "svc:"+sv.id && strings.Contains(srv, "/") {
						out, found = union(out, []string{srv}), true
					}
				}
			}
		}
		if !found {
			out = union(out, []string{n})
		}
	}
	return out
}

// notAccepted says why a policy is not in force, from its Accepted and
// Attached conditions; empty when it is, or when the capture has no status.
func notAccepted(p *store.Object) string {
	for _, a := range l(path(p.Obj, "status", "ancestors")) {
		for _, cd := range l(m(a)["conditions"]) {
			cm := m(cd)
			if t := s(cm["type"]); (t == "Accepted" || t == "Attached") && s(cm["status"]) == "False" {
				msg := first(s(cm["message"]), s(cm["reason"]))
				return fmt.Sprintf("its %s condition is False (%s)", t, msg)
			}
		}
	}
	return ""
}

// applyServerRules gives the callers a rule allows (or refuses) every tool
// of a server each tool known for it, once every tool is known.
func (b *actBuilder) applyServerRules() {
	for _, t := range b.tools {
		if who := b.allowAll[t.Server]; len(who) > 0 {
			t.AllowedFor = union(t.AllowedFor, who)
		}
		if who := b.denyAll[t.Server]; len(who) > 0 {
			t.DeniedFor = union(t.DeniedFor, who)
		}
	}
}

// configuredReach evaluates, for each agent, the network path to the
// Services its configuration reaches its MCP servers at, so an open server
// it cannot reach is not reported as one it can call.
func (b *actBuilder) configuredReach() {
	for who, bySrv := range b.agentSvc {
		for _, svcs := range bySrv {
			for _, id := range svcs {
				for _, c := range b.clusters {
					w, sv := c.findWorkload(who), c.findService(id)
					if w == nil || sv == nil {
						continue
					}
					h := c.evalHop(w, sv)
					r := ActReach{Verdict: h.verdict}
					if len(h.reasons) > 0 {
						r.Why = h.reasons[0]
					}
					if b.a.Reach == nil {
						b.a.Reach = map[string]map[string]ActReach{}
					}
					if b.a.Reach[who] == nil {
						b.a.Reach[who] = map[string]ActReach{}
					}
					b.a.Reach[who][id] = r
					break
				}
			}
		}
	}
}

// hasServiceAccount says whether any workload in the capture runs as the
// service account of this SPIFFE ID.
func (b *actBuilder) hasServiceAccount(ident string) bool {
	ns, sa, ok := spiffeSA(ident)
	if !ok {
		return true
	}
	for _, c := range b.clusters {
		for _, w := range c.works {
			if w.ns == ns && w.sa == sa {
				return true
			}
		}
	}
	return false
}

// podAddressGone says whether addr is in a node's pod range while no pod in
// the capture has it.
func (b *actBuilder) podAddressGone(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, c := range b.clusters {
		if c.ips().pod[addr] != nil || c.ips().node[addr] {
			return false
		}
		nodes := c.list("", "nodes")
		if len(nodes) == 0 {
			// Nodes not collected (a namespaced mirror): take the /24 of the
			// pod addresses in the capture as the pod range.
			if v4 := ip.To4(); v4 != nil {
				for a := range c.ips().pod {
					if p := net.ParseIP(a).To4(); p != nil && p[0] == v4[0] && p[1] == v4[1] && p[2] == v4[2] {
						return true
					}
				}
			}
		}
		for _, n := range nodes {
			cidrs := strList(l(path(n.Obj, "spec", "podCIDRs")))
			if cidr := s(path(n.Obj, "spec", "podCIDR")); cidr != "" {
				cidrs = append(cidrs, cidr)
			}
			for _, cd := range cidrs {
				if _, nw, err := net.ParseCIDR(cd); err == nil && nw.Contains(ip) {
					return true
				}
			}
		}
	}
	return false
}

// guarded says whether an allowlist covers the server.
func (b *actBuilder) guarded(srv string) bool {
	return len(b.guard[srv]) > 0
}

// noNamespaceFinding: a rule that names a service account without a
// namespace lets a service account of that name in any namespace in.
func (b *actBuilder) noNamespaceFinding() []Finding {
	if len(b.noNS) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var items []FindingItem
	for _, it := range b.noNS {
		k := it.Target + "|" + it.Class
		if seen[k] {
			continue
		}
		seen[k] = true
		items = append(items, it)
	}
	return []Finding{{Severity: "medium", Title: "An MCP allowlist names a service account without its namespace",
		Detail: "The rule matches on source.identity.serviceAccount alone. A workload in any namespace in the mesh whose service account has the same name passes it, so whoever can create a service account in another namespace gets the same tools.",
		Items:  items,
		Advice: []string{"Test the namespace in the same clause as the service account: source.identity.namespace == \"kagent\" && source.identity.serviceAccount == \"<agent>\"."},
		Source: activityGuide, Docs: "https://docs.solo.io/agentgateway/kubernetes/latest/mcp/mcp-access/"}}
}

func hasRef(rs []Ref, o *store.Object) bool {
	for _, r := range rs {
		if r.Namespace == o.Namespace && r.Name == o.Name && r.Kind == o.Kind {
			return true
		}
	}
	return false
}

// policyServers names the MCP servers a policy applies to, the way calls
// name them (the backend's MCP target names), falling back to the object
// it targets.
func (c *cluster) policyServers(p *store.Object) []string {
	var out []string
	for _, tr := range l(path(p.Obj, "spec", "targetRefs")) {
		kind, name := s(m(tr)["kind"]), s(m(tr)["name"])
		found := false
		if strings.HasSuffix(kind, "Backend") {
			for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}} {
				for _, be := range c.list(g.group, g.res) {
					if be.Namespace != p.Namespace || be.Name != name {
						continue
					}
					for _, sec := range []string{"mcp", "entMcp"} {
						for _, t := range l(path(be.Obj, "spec", sec, "targets")) {
							if n := s(m(t)["name"]); n != "" {
								out = union(out, []string{n})
								found = true
							}
						}
					}
				}
			}
		}
		if !found && name != "" {
			out = union(out, []string{name})
		}
	}
	return out
}

// configuredTools reads the tools agents are configured for: a declarative
// kagent Agent's toolNames, and an ALLOWED_TOOLS variable (a JSON list) on a
// BYO agent, against the servers in its MCP_SERVERS_CONFIG.
func (b *actBuilder) configuredTools(c *cluster) {
	for _, ag := range c.list("kagent.dev", "agents") {
		who := ag.Namespace + "/" + ag.Name
		for _, t := range l(path(ag.Obj, "spec", "declarative", "tools")) {
			ms := m(m(t)["mcpServer"])
			if ms == nil {
				continue
			}
			srv := s(ms["name"])
			if b.servers[key(ag.Namespace, srv)] != "" {
				srv = b.servers[key(ag.Namespace, srv)]
			}
			if svcs := b.svcOf[srv]; len(svcs) == 1 {
				if b.agentSvc[who] == nil {
					b.agentSvc[who] = map[string][]string{}
				}
				b.agentSvc[who][srv] = svcs
			}
			for _, n := range strList(l(ms["toolNames"])) {
				ti := b.tool(srv, n)
				ti.ConfiguredOn = union(ti.ConfiguredOn, []string{who})
			}
		}
		var tools, servers []string
		resolve := func(name, url string) string {
			host := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
			host, _, _ = strings.Cut(host, "/")
			host = stripPort(host)
			keys := []string{host}
			if p := strings.Split(host, "."); len(p) >= 2 {
				keys = append(keys, key(p[1], p[0]))
			}
			for _, k := range keys {
				if srv := b.servers[k]; srv != "" {
					return srv // a kagent server with a tool list
				}
			}
			return name // the name calls through agentgateway use
		}
		for _, e := range l(path(ag.Obj, "spec", "byo", "deployment", "env")) {
			em := m(e)
			var v []any
			switch s(em["name"]) {
			case "ALLOWED_TOOLS":
				if json.Unmarshal([]byte(s(em["value"])), &v) == nil {
					tools = strList(v)
				}
			case "MCP_SERVERS_CONFIG":
				if json.Unmarshal([]byte(s(em["value"])), &v) == nil {
					for _, x := range v {
						if n := s(m(x)["name"]); n != "" {
							srv := resolve(n, s(m(x)["url"]))
							servers = append(servers, srv)
							h := strings.TrimPrefix(strings.TrimPrefix(s(m(x)["url"]), "https://"), "http://")
							h, _, _ = strings.Cut(h, "/")
							if svc := b.addSvc(c, srv, stripPort(h)); svc != "" {
								if b.agentSvc[who] == nil {
									b.agentSvc[who] = map[string][]string{}
								}
								b.agentSvc[who][srv] = union(b.agentSvc[who][srv], []string{svc})
							}
						}
					}
				}
			}
		}
		for _, n := range tools {
			b.pending = append(b.pending, pendingTool{who: who, name: n, candidates: servers})
		}
	}
}

// serverSummary counts, per MCP server, what the capture knows of its tools.
func (b *actBuilder) serverSummary() {
	by := map[string]*ServerInfo{}
	get := func(n string) *ServerInfo {
		if by[n] == nil {
			by[n] = &ServerInfo{Name: n}
		}
		return by[n]
	}
	for _, t := range b.tools {
		si := get(t.Server)
		if t.Declared {
			si.Listed++
			si.HasList = true
		}
		if len(t.AllowedFor) > 0 {
			si.Allowed++
		}
		if len(t.ConfiguredOn) > 0 {
			si.Configured++
		}
		if t.Calls > 0 {
			si.Called++
		}
	}
	modes := map[string]string{}
	for _, c := range b.clusters {
		for k, v := range c.serverModes() {
			modes[k] = v
		}
	}
	for _, t := range b.targets {
		if t.Kind != "meta" {
			continue
		}
		si := get(t.Server)
		if si.Meta == nil {
			si.Meta = map[string]int{}
		}
		si.Meta[t.Label] += t.Requests
	}
	for n := range b.guard {
		get(n)
	}
	for n, si := range by {
		si.ToolMode = modes[n]
		si.Services = b.svcOf[n]
		si.Guarded, si.Policies = b.guarded(n), b.guard[n]
		si.AllowAll, si.DenyAll, si.Unread = b.allowAll[n], b.denyAll[n], b.unread[n]
		b.a.Servers = append(b.a.Servers, *si)
	}
	sort.Slice(b.a.Servers, func(i, j int) bool {
		x, y := b.a.Servers[i], b.a.Servers[j]
		if x.Called != y.Called {
			return x.Called > y.Called
		}
		return x.Name < y.Name
	})
	known := map[string]int{}
	for _, t := range b.tools {
		known[t.Server]++
	}
	for i := range b.a.Servers {
		b.a.Servers[i].Known = known[b.a.Servers[i].Name]
	}
	for _, si := range b.a.Servers {
		if si.Meta["run_code"] > 0 {
			b.a.Notes = append(b.a.Notes, fmt.Sprintf("Agents reached MCP server %s through Code Mode %d times: agentgateway gave them one tool, run_code, and the code they sent called the server's tools. The logs do not say which tools the code called, so any tool the caller is allowed may have been used", si.Name, si.Meta["run_code"]))
		}
		if si.Called > 0 && !si.HasList {
			b.a.Notes = append(b.a.Notes, serverNote(si))
		}
	}
}

// placeConfigured puts each configured tool on a server: the only candidate
// server, or the one candidate known to offer it (listed, allowed by a
// policy, or called). Otherwise it stays under "(server not known)".
func (b *actBuilder) placeConfigured() {
	known := func(srv, name string) bool {
		t := b.tools[srv+"/"+name]
		return t != nil && (t.Declared || len(t.AllowedFor) > 0 || t.Calls > 0)
	}
	for _, p := range b.pending {
		srv := "(server not known)"
		if len(p.candidates) == 1 {
			srv = p.candidates[0]
		} else {
			var hits []string
			for _, c := range p.candidates {
				if known(c, p.name) {
					hits = append(hits, c)
				}
			}
			if len(hits) == 1 {
				srv = hits[0]
			}
		}
		ti := b.tool(srv, p.name)
		ti.ConfiguredOn = union(ti.ConfiguredOn, []string{p.who})
	}
}

// serverNote says, in plain words, why a server's tool list is incomplete.
func serverNote(si ServerInfo) string {
	return fmt.Sprintf("Flightbay cannot see the full list of tools MCP server %s offers: it is not a kagent MCP server (only those report one), and agentgateway does not log the list a server offers. The %d tools shown are the ones seen in the capture (%d called, %d in an allowlist, %d in an agent's configuration); it may offer more",
		si.Name, si.Known, si.Called, si.Allowed, si.Configured)
}

// routeServers names the MCP servers behind a route: the targets of the
// agentgateway backends an HTTPRoute sends to.
func (c *cluster) routeServers(route string) []string {
	ns, name, ok := strings.Cut(route, "/")
	if !ok {
		return nil
	}
	var out []string
	for _, r := range c.list("gateway.networking.k8s.io", "httproutes") {
		if r.Namespace != ns || r.Name != name {
			continue
		}
		for _, rule := range l(path(r.Obj, "spec", "rules")) {
			for _, br := range l(m(rule)["backendRefs"]) {
				bm := m(br)
				if !strings.HasSuffix(s(bm["kind"]), "Backend") {
					continue
				}
				bns := first(s(bm["namespace"]), ns)
				for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}} {
					for _, be := range c.list(g.group, g.res) {
						if be.Namespace == bns && be.Name == s(bm["name"]) {
							for _, sec := range []string{"mcp", "entMcp"} {
								for _, t := range l(path(be.Obj, "spec", sec, "targets")) {
									if n := s(m(t)["name"]); n != "" {
										out = union(out, []string{n})
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}

// serverModes maps each MCP target name to its backend's tool mode.
func (c *cluster) serverModes() map[string]string {
	out := map[string]string{}
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}} {
		for _, be := range c.list(g.group, g.res) {
			for _, sec := range []string{"mcp", "entMcp"} {
				mcp := m(path(be.Obj, "spec", sec))
				if mcp == nil {
					continue
				}
				mode := first(s(mcp["toolMode"]), "Standard")
				for _, t := range l(mcp["targets"]) {
					if n := s(m(t)["name"]); n != "" {
						if prev, ok := out[n]; !ok || prev == "Standard" {
							out[n] = mode
						}
					}
				}
			}
		}
	}
	return out
}

// addSvc records that server is reached at host, when host names a Service
// in the capture (name.namespace[.svc...]).
func (b *actBuilder) addSvc(c *cluster, server, host string) string {
	p := strings.Split(host, ".")
	if len(p) < 2 {
		return ""
	}
	if sv := c.svcByKey[key(p[1], p[0])]; sv != nil {
		b.svcOf[server] = union(b.svcOf[server], []string{"svc:" + sv.id})
		return "svc:" + sv.id
	}
	return ""
}

// unenforcedLists finds agents whose configuration limits the tools of an MCP
// server that no agentgateway allowlist covers: the list only filters what
// the model is offered, and nothing on the wire stops a call to the rest.
func (b *actBuilder) unenforcedLists() []Finding {
	guarded := map[string]bool{}
	total := map[string]int{}
	cfg := map[string]map[string]int{} // server -> agent -> tools configured
	for _, t := range b.tools {
		total[t.Server]++
		if len(t.AllowedFor) > 0 || b.guarded(t.Server) {
			guarded[t.Server] = true
		}
		for _, who := range t.ConfiguredOn {
			if cfg[t.Server] == nil {
				cfg[t.Server] = map[string]int{}
			}
			cfg[t.Server][who]++
		}
	}
	var items []FindingItem
	for _, srv := range sortedKeys(cfg) {
		if guarded[srv] || srv == "(server not known)" {
			continue
		}
		for _, who := range sortedKeys(cfg[srv]) {
			n := cfg[srv][who]
			rest := total[srv] - n
			note := fmt.Sprintf("its configuration lists %d tool(s) on %s; no agentgateway allowlist covers the server, so nothing stops it calling the rest", n, srv)
			if rest > 0 {
				note = fmt.Sprintf("its configuration lists %d of the %d tools %s is known to offer; no agentgateway allowlist covers the server, so nothing stops it calling the other %d", n, total[srv], srv, rest)
			}
			items = append(items, FindingItem{Target: who, Class: srv, Access: fmt.Sprintf("%d configured", n), Note: note})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return []Finding{{Severity: "high", Title: "An agent's tool list is not enforced",
		Detail: "These agents are configured to use only some of an MCP server's tools (kagent toolNames or ALLOWED_TOOLS), but no agentgateway allowlist covers that server. The configuration only filters what the model is offered: a prompt injection or a changed agent can call any tool the server has.",
		Items:  items,
		Advice: []string{"Put the server behind agentgateway (an agentgateway waypoint on its Service, or a gateway route) and add a backend.mcp.authorization rule that allows each agent's service account only its configured tools. The Lock down tab of the agent on the Agents tab writes one from what it used."},
		Source: activityGuide, Docs: "https://docs.solo.io/agentgateway/kubernetes/latest/mcp/mcp-access/"}}
}
