package posture

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
)

// agenticExtra holds the agentic checks added after the first release. They
// cover the agentgateway policy and backend fields, kagent Agents, ModelConfigs
// and tool servers, kmcp MCPServers, the kagent-enterprise AccessPolicy and
// agentregistry, read from the CRD types rather than guessed.
func agenticExtra(e env, md *model) []Check {
	x := newXctx(e, md)
	var out []Check
	for _, d := range x.defs() {
		c := d.sp.check("agentic")
		d.eval(&c)
		out = append(out, c)
	}
	return out
}

type xdef struct {
	sp   spec
	eval func(*Check)
}

// xctx is what every extra check needs: the model, whether policies and
// routes were captured, and the kagent objects.
type xctx struct {
	e                             env
	md                            *model
	polOK, routesOK               bool
	polWhy, routesWhy             string
	ai, mcp, exposedAI, exposedMC []*backend
	agents                        []*store.Object // Agents and SandboxAgents (same spec)
	mcs, rms, mcpservers          []*store.Object
}

func newXctx(e env, md *model) *xctx {
	x := &xctx{e: e, md: md}
	p1, w1 := e.evaluable("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true)
	p2, w2 := e.evaluable("enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy", true)
	x.polOK = p1 && p2
	if !p1 {
		x.polWhy = w1
	}
	if !p2 {
		x.polWhy = strings.TrimSpace(x.polWhy + " " + w2)
	}
	x.routesOK, x.routesWhy = e.evaluable("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true)
	for _, b := range md.backends {
		if b.mcp != nil {
			x.mcp = append(x.mcp, b)
			if len(b.routes) > 0 {
				x.exposedMC = append(x.exposedMC, b)
			}
		}
		if b.ai != nil {
			x.ai = append(x.ai, b)
			if len(b.routes) > 0 {
				x.exposedAI = append(x.exposedAI, b)
			}
		}
	}
	x.agents = append(e.list("kagent.dev", "agents"), e.list("kagent.dev", "sandboxagents")...)
	x.mcs = e.list("kagent.dev", "modelconfigs")
	x.rms = e.list("kagent.dev", "remotemcpservers")
	x.mcpservers = e.list("kagent.dev", "mcpservers")
	return x
}

func xNA(c *Check, why string) { c.Status, c.Reason = NA, why }

// ---------- helpers ----------

// policyActive reports whether a policy's status lets it enforce anything:
// a policy the controller did not accept, or could not attach, does nothing.
func policyActive(p policy) bool {
	for _, t := range []string{"Accepted", "Attached"} {
		if st, _, _, found := condition(p.o, t); found && st == "False" {
			return false
		}
	}
	return true
}

// finding collects the problems found on one object into one item.
type finding struct {
	st        Status
	bad, good []string
}

func (f *finding) bump(st Status) {
	if f.st == "" || statusRank[st] < statusRank[f.st] {
		f.st = st
	}
}
func (f *finding) fail(s string) { f.bump(Fail); f.bad = append(f.bad, s) }
func (f *finding) warn(s string) { f.bump(Warn); f.bad = append(f.bad, s) }
func (f *finding) ok(s string)   { f.bump(Pass); f.good = append(f.good, s) }
func (f *finding) any() bool     { return f.st != "" }
func (f *finding) item(e env, o *store.Object) Item {
	st := f.st
	if st == "" {
		st = Pass
	}
	d := strings.Join(f.bad, "; ")
	if d == "" {
		d = strings.Join(f.good, "; ")
	}
	return e.item(o, st, d)
}

func exprText(a map[string]any) string { return fmt.Sprint(path(a, "policy", "matchExpressions")) }

// authzBlocks returns every CEL authorization block of a policy.
func authzBlocks(p policy) map[string]map[string]any {
	out := map[string]map[string]any{}
	for where, keys := range map[string][]string{
		"traffic.authorization":         {"traffic", "authorization"},
		"backend.mcp.authorization":     {"backend", "mcp", "authorization"},
		"frontend.networkAuthorization": {"frontend", "networkAuthorization"},
	} {
		if a := m(path(p.spec, keys...)); a != nil {
			out[where] = a
		}
	}
	return out
}

// findKey returns every value stored under key anywhere below v.
func findKey(v any, key string) []any {
	var out []any
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				if k == key {
					out = append(out, val)
				}
				walk(val)
			}
		case []any:
			for _, val := range t {
				walk(val)
			}
		}
	}
	walk(v)
	return out
}

func hasKey(mp map[string]any, key string) bool { _, ok := mp[key]; return ok }

func toNum(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return (ok && b) || s(v) == "true"
}

func isRedacted(v string) bool { return strings.HasPrefix(v, "[REDACTED") }

// parseURL splits a URL into scheme and lower-case host (no port).
func parseURL(raw string) (scheme, host string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", ""
	}
	return strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
}

// hostNS returns the namespace named by a cluster-local host
// (svc.ns, svc.ns.svc, svc.ns.svc.cluster.local), or def.
func hostNS(host, def string) string {
	if !clusterLocal(host) {
		return ""
	}
	if parts := strings.Split(host, "."); len(parts) > 1 {
		return parts[1]
	}
	return def
}

// normHost reduces a cluster-local host to name.namespace so the short and
// long forms of a Service name compare equal.
func normHost(host, def string) string {
	host = strings.ToLower(host)
	if !clusterLocal(host) {
		return host
	}
	parts := strings.Split(host, ".")
	if len(parts) == 1 {
		return parts[0] + "." + def
	}
	return parts[0] + "." + parts[1]
}

func refNS(r map[string]any, def string) string {
	if ns := s(r["namespace"]); ns != "" {
		return ns
	}
	return def
}

// routesTo returns the HTTPRoutes with a backendRef of one of kinds (an empty
// kind means Service) naming ns/name.
func (md *model) routesTo(kinds map[string]bool, ns, name string) []*store.Object {
	var out []*store.Object
	for _, r := range md.routes {
		hit := false
		for _, rule := range l(path(r.Obj, "spec", "rules")) {
			for _, br := range l(m(rule)["backendRefs"]) {
				bm := m(br)
				k := s(bm["kind"])
				if k == "" {
					k = "Service"
				}
				if kinds[k] && s(bm["name"]) == name && refNS(bm, r.Namespace) == ns {
					hit = true
				}
			}
		}
		if hit {
			out = append(out, r)
		}
	}
	return out
}

// northSouth reports whether a route hangs off a Gateway that is not a waypoint.
func (md *model) northSouth(r *store.Object) bool {
	for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
		pm := m(pr)
		kind := s(pm["kind"])
		if kind == "" {
			kind = "Gateway"
		}
		if kind != "Gateway" {
			continue
		}
		gw := md.gateways[refNS(pm, r.Namespace)+"/"+s(pm["name"])]
		if gw == nil || !strings.Contains(gatewayClass(gw), "waypoint") {
			return true
		}
	}
	return false
}

// rulesTo returns the rules of a route that send to a backend.
func rulesTo(r *store.Object, b *backend) []map[string]any {
	var out []map[string]any
	for _, rule := range l(path(r.Obj, "spec", "rules")) {
		for _, br := range l(m(rule)["backendRefs"]) {
			bm := m(br)
			if s(bm["kind"]) == b.o.Kind && s(bm["name"]) == b.o.Name && refNS(bm, r.Namespace) == b.o.Namespace {
				out = append(out, m(rule))
				break
			}
		}
	}
	return out
}

// siblings are the active policies sharing a target with p, p included.
func (md *model) siblings(p policy) []policy {
	var out []policy
	for _, q := range md.policies {
		if q.o.Namespace != p.o.Namespace || !policyActive(q) {
			continue
		}
		if q.o == p.o {
			out = append(out, q)
			continue
		}
	shared:
		for _, t := range q.targets {
			for _, u := range p.targets {
				if t.kind == u.kind && t.name == u.name {
					out = append(out, q)
					break shared
				}
			}
		}
	}
	return out
}

// chains returns, for each backend a policy reaches, the policies that apply
// to that backend. A policy that reaches no agentgateway backend gets one
// chain: its siblings plus the policies on its routes' Gateways.
func (md *model) chains(p policy) [][]policy {
	var out [][]policy
	for _, b := range md.backends {
		for _, q := range md.applicable(b) {
			if q.o == p.o {
				out = append(out, md.applicable(b))
				break
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	chain := md.siblings(p)
	for _, t := range p.targets {
		if t.kind != "HTTPRoute" {
			continue
		}
		if r := md.e.ws.Get(md.e.cluster, "gateway.networking.k8s.io", "httproutes", p.o.Namespace, t.name); r != nil {
			for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
				pm := m(pr)
				if gw := md.gateways[refNS(pm, r.Namespace)+"/"+s(pm["name"])]; gw != nil {
					chain = append(chain, md.gatewayPolicies(gw)...)
				}
			}
		}
	}
	return [][]policy{chain}
}

// kagent: a ModelConfig's base URL (only OpenAI and Anthropic ModelConfigs can
// be pointed at a gateway this way).
func modelBaseURL(mc *store.Object) (provider, base string, ok bool) {
	sp := m(mc.Obj["spec"])
	switch strings.ToLower(s(sp["provider"])) {
	case "openai":
		return "OpenAI", s(path(sp, "openAI", "baseUrl")), true
	case "anthropic":
		return "Anthropic", s(path(sp, "anthropic", "baseUrl")), true
	}
	return s(sp["provider"]), "", false
}

// agentTools returns the declarative tools of an Agent.
func agentTools(a *store.Object) []map[string]any {
	var out []map[string]any
	for _, t := range l(path(a.Obj, "spec", "declarative", "tools")) {
		out = append(out, m(t))
	}
	return out
}

// mcpServerRef resolves a tools[].mcpServer reference to group, resource,
// kind, namespace and name. kagent treats an empty kind as MCPServer.
func mcpServerRef(ms map[string]any, agentNS string) (group, res, kind, ns, name string) {
	kind = s(ms["kind"])
	switch kind {
	case "", "MCPServer":
		group, res, kind = "kagent.dev", "mcpservers", "MCPServer"
	case "RemoteMCPServer":
		group, res = "kagent.dev", "remotemcpservers"
	case "Service":
		group, res = "", "services"
	}
	return group, res, kind, refNS(ms, agentNS), s(ms["name"])
}

const riskyVerbs = "delete|apply|create|merge|write|update|patch|exec|scale|remove|terminate"

var (
	authHeader   = regexp.MustCompile(`(?i)(authorization|api[-_]?key|token|secret|cookie|password)`)
	authDefault  = regexp.MustCompile(`(?i)^(authorization|api-key|x-api-key)$`)
	headerRead   = regexp.MustCompile(`request\.headers\[\s*["']([^"']+)["']\s*\]`)
	sha40        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	versionedArg = regexp.MustCompile(`^.+@[0-9A-Za-z.\-^~]+$|==`)
)

func toDur(v any) time.Duration {
	d, _ := time.ParseDuration(s(v))
	return d
}

func xSortedKeys(mp map[string]bool) []string {
	var out []string
	for k := range mp {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (x *xctx) defs() []xdef {
	pol := "apiVersion: agentgateway.dev/v1alpha1   # or enterpriseagentgateway.solo.io/v1alpha1 EnterpriseAgentgatewayPolicy\nkind: AgentgatewayPolicy\n"
	return []xdef{
		{spec{"agentic.a2a-auth-required", "Authentication", "critical", "A2A agent endpoints require authentication",
			"An A2A endpoint runs the agent with every tool and credential it holds. Exposed on a Gateway without JWT or external auth, anyone who reaches the listener can drive the agent.",
			pol + "metadata: {name: a2a-jwt, namespace: <route namespace>}\nspec:\n  targetRefs:\n  - {group: gateway.networking.k8s.io, kind: HTTPRoute, name: <a2a route>}\n  traffic:\n    jwtAuthentication:\n      mode: Strict\n      providers:\n      - {issuer: <issuer>, audiences: [<agent>], jwks: {remote: {...}}}",
			kbBase + "agentic-a2a-kind/"}, x.a2aAuth},
		{spec{"agentic.auth-mode-enforcing", "Authentication", "high", "API key and basic authentication reject missing credentials",
			"Optional and Permissive modes let a request with no credential through, so the policy authenticates nobody who does not want to be authenticated.",
			"traffic:\n  apiKeyAuthentication:\n    mode: Strict        # the default; do not set Optional or Permissive\n    secretRef: {name: api-keys}",
			kbBase + "agentgateway-virtual-keys/"}, x.authMode},
		{spec{"agentic.basic-auth-users-in-secret", "Authentication", "medium", "Basic auth users live in a Secret, hashed with bcrypt",
			"Inline htpasswd lines are readable by anyone who can read the policy, and MD5 ($apr1$) and SHA-1 ({SHA}) hashes are cheap to crack.",
			"traffic:\n  basicAuthentication:\n    secretRef: {name: htpasswd}   # key .htaccess, lines made with htpasswd -B (bcrypt)",
			kbBase + "agentgateway-virtual-keys/"}, x.basicUsers},
		{spec{"agentic.mcp-authn-traffic-phase", "Authentication", "low", "MCP authentication runs in the traffic phase",
			"backend.mcp.authentication is deprecated: it runs after transformation and rate limiting. traffic.jwtAuthentication.mcp authenticates first. Without audiences any token from the issuer is accepted.",
			"traffic:\n  jwtAuthentication:\n    mode: Strict\n    providers:\n    - {issuer: <issuer>, audiences: [<mcp resource>], jwks: {remote: {...}}}\n    mcp:\n      provider: Keycloak\n      resourceMetadata: {...}",
			kbBase + "rfcs-jwt-oauth-mcp/"}, x.mcpAuthnPhase},
		{spec{"agentic.authz-identity-source-present", "Authorization", "high", "Authorization rules that read jwt or apiKey have an authentication policy behind them",
			"A rule on jwt.* or apiKey.* only means something when a jwtAuthentication or apiKeyAuthentication on the same route validated the credential first. Without one the claims are empty or never checked.",
			"traffic:\n  jwtAuthentication:\n    mode: Strict\n    providers: [...]\n  authorization:\n    action: Allow\n    policy:\n      matchExpressions: ['jwt.groups.exists(g, g == \"sre\")']",
			kbBase + "agentic-mcp-rbac-kind/"}, x.authzIdentity},
		{spec{"agentic.gateway-routes-namespace-restricted", "Isolation", "high", "Agentgateway listeners restrict which namespaces can attach routes",
			"allowedRoutes from: All lets any namespace publish a route on the gateway, including one that shadows an MCP or LLM path and skips its policy.",
			"listeners:\n- name: http\n  port: 80\n  protocol: HTTP\n  allowedRoutes:\n    namespaces:\n      from: Selector\n      selector: {matchLabels: {agentgateway.dev/routes: allowed}}",
			kbBase + "agentgateway-mcp-controls/"}, x.gatewayNamespaces},
		{spec{"agentic.dynamic-forward-proxy-restricted", "Isolation", "critical", "Forward proxies authenticate callers and allow only known hosts",
			"A dynamicForwardProxy backend or a CONNECT tunnel sends requests anywhere the Host header says: an open one is an egress proxy for anyone who reaches it.",
			"traffic:\n  jwtAuthentication: {mode: Strict, providers: [...]}\n  authorization:\n    action: Allow\n    policy:\n      matchExpressions: ['request.host in [\"api.openai.com\", \"api.githubcopilot.com\"]']",
			kbBase + "agentgateway-egress-corp-proxy/"}, x.forwardProxy},
		{spec{"agentic.kagent-cross-namespace-refs", "Authorization", "medium", "Agents and tool servers limit which namespaces can reference them",
			"allowedNamespaces from: All lets an Agent in any namespace use this agent as a tool, or this tool server, with its credentials.",
			"spec:\n  allowedNamespaces:\n    from: Selector\n    selector: {matchLabels: {team: sre}}",
			kbBase + "zero-trust-agents-kubernetes/"}, x.crossNamespace},
		{spec{"agentic.kagent-accesspolicy-baseline", "Authorization", "high", "Agent namespaces have a deny-all AccessPolicy baseline",
			"Without a namespace baseline every user and agent can call every agent; ALLOW policies only narrow access once the default is deny.",
			"apiVersion: policy.kagent-enterprise.solo.io/v1alpha1\nkind: AccessPolicy\nmetadata: {name: baseline, namespace: <agent namespace>}\nspec:\n  action: DENY\n  from: {subjects: []}\n  targetRef: {kind: Agent, name: \"*\"}",
			kbBase + "agentic-a2a-kind/"}, x.accessBaseline},
		{spec{"agentic.kagent-privilege-separated-toolservers", "Identity", "high", "Tool servers with write tools are not shared between agents",
			"A tool server holds one upstream credential. Shared by several agents, a read-only agent sits one prompt away from the write tools the others need.",
			"# one RemoteMCPServer (and credential) per privilege level\nkind: RemoteMCPServer\nmetadata: {name: k8s-readonly}\nspec:\n  url: http://k8s-tools-ro.kagent:8084/mcp\n  headersFrom:\n  - {name: Authorization, valueFrom: {type: Secret, name: k8s-ro-token, key: token}}",
			kbBase + "agentic-tool-privilege-kind/"}, x.toolServerSharing},
		{spec{"agentic.mcp-prefix-no-shadowing", "Guardrails", "medium", "Multi-target MCP backends prefix tool names",
			"With prefixMode Never two targets can publish a tool with the same name, and a later target can shadow a trusted tool.",
			"mcp:\n  prefixMode: Always\n  targets:\n  - {name: github, static: {...}}\n  - {name: jira, static: {...}}",
			kbBase + "agentic-tool-curation-kind/"}, x.prefixMode},
		{spec{"agentic.mcp-selector-targets-scoped", "Supply chain", "medium", "MCP selector targets name the Services they federate",
			"A selector across namespaces federates any matching Service as a tool server. With no Service labels, anyone who can create a Service there adds tools.",
			"targets:\n- name: approved\n  selector:\n    services: {matchLabels: {mcp.solo.io/approved: \"true\"}}",
			kbBase + "agentic-tool-curation-kind/"}, x.selectorTargets},
		{spec{"agentic.code-mode-scoped", "Guardrails", "high", "Code mode backends have a tool allowlist and tight time limits",
			"Code mode runs model-written code against every tool on the backend. An allowlist on mcp.tool.name bounds what the code can call, and short timeouts bound runaway loops.",
			"entMcp:\n  toolMode: Code\n  codeMode: {timeout: 10s, cpuTimeout: 500ms}\n---\nbackend:\n  mcp:\n    authorization:\n      action: Allow\n      policy:\n        matchExpressions: ['mcp.tool.name in [\"getPetById\", \"findPetsByStatus\"]']",
			kbBase + "agentgw-code-mode-kind/"}, x.codeMode},
		{spec{"agentic.prompt-guard-streaming", "Guardrails", "medium", "Response guards also run on streamed responses",
			"Prompt guards skip streaming responses unless streaming is Enabled, so a streaming client gets the unmasked output.",
			"backend:\n  ai:\n    promptGuard:\n      streaming: Enabled\n      response:\n      - regex: {action: Mask, builtins: [Ssn, CreditCard]}",
			kbBase + "agentic-pii-guardrail-kind/"}, x.guardStreaming},
		{spec{"agentic.enforcement-fails-closed", "Guardrails", "high", "Guardrails and external auth fail closed",
			"FailOpen turns the control off whenever its service is down or overloaded.",
			"traffic:\n  extAuth:\n    failureMode: FailClosed\nbackend:\n  mcp:\n    guardrails:\n      processors:\n      - remote: {backendRef: {...}, failureMode: FailClosed}",
			kbBase + "agentic-external-guardrail-kind/"}, x.failsClosed},
		{spec{"agentic.mcp-guardrails-cover-tool-calls", "Guardrails", "medium", "MCP guardrails inspect tools/call",
			"Methods that match no key bypass the processor. A guardrail that does not cover tools/call never sees the tool arguments or results.",
			"guardrails:\n  processors:\n  - remote: {backendRef: {name: guard, port: 9000}}\n    methods:\n      tools/call: Full\n      tools/list: Response",
			kbBase + "agentic-external-guardrail-kind/"}, x.guardrailMethods},
		{spec{"agentic.kagent-sandbox-egress-scoped", "Isolation", "medium", "Agent sandboxes allow only named domains",
			"A wildcard allowedDomains entry lets sandboxed code send data anywhere.",
			"spec:\n  sandbox:\n    network:\n      allowedDomains: [api.github.com, pypi.org]",
			kbBase + "kagent-substrate-actor-egress/"}, x.sandboxEgress},
		{spec{"agentic.upstream-tls-verified", "Encryption", "high", "Upstream TLS certificates are verified",
			"Skipping verification lets anyone on the path present a certificate and read the credential the gateway or agent sends.",
			"policies:\n  tls:\n    sni: api.example.com\n    caCertificateRefs: [{name: upstream-ca}]   # instead of insecureSkipVerify",
			kbBase + "agentgateway-mcp-controls/"}, x.tlsVerified},
		{spec{"agentic.credentials-not-plaintext", "Encryption", "medium", "Credentials are not sent over plain HTTP",
			"A tool header or model API key sent to an http:// URL crosses the network in clear text unless both ends are in the ambient mesh.",
			"spec:\n  url: https://tools.example.com/mcp   # or keep http:// only between ambient namespaces",
			kbBase + "zero-trust-agents-kubernetes/"}, x.plaintextCreds},
		{spec{"agentic.gateway-listener-tls", "Encryption", "medium", "Exposed agentgateway listeners terminate TLS",
			"Bearer tokens and API keys cross the network on every request. An exposed plain-HTTP listener sends them in clear text.",
			"listeners:\n- name: https\n  port: 443\n  protocol: HTTPS\n  tls:\n    mode: Terminate\n    certificateRefs: [{name: gateway-tls}]",
			kbBase + "sovereign-ai-uk-eks/"}, x.listenerTLS},
		{spec{"agentic.no-token-passthrough", "Identity", "critical", "The caller's token is not passed through to upstreams",
			"The MCP authorization spec forbids token passthrough: the upstream cannot tell the token was meant for the gateway, and a token minted for one service is replayed to another.",
			"targets:\n- name: github\n  static:\n    policies:\n      auth:\n        secretRef: {name: github-pat}   # or entTokenExchange, not passthrough",
			kbBase + "rfcs-jwt-oauth-mcp/"}, x.passthrough},
		{spec{"agentic.no-inline-crd-credentials", "Identity", "critical", "No credentials written inline in agent and gateway resources",
			"A key in a CRD field is readable by anyone who can read the resource, lands in Git and never rotates.",
			"policies:\n  auth:\n    secretRef: {name: provider-key}\n---\nheadersFrom:\n- name: Authorization\n  valueFrom: {type: Secret, name: tool-token, key: token}",
			kbBase + "agentgateway-vault-secrets-kind/"}, x.inlineCreds},
		{spec{"agentic.credential-refs-resolve", "Identity", "high", "Credential references name Secrets and keys that exist",
			"A reference to a missing Secret or key fails at request time, often as a confusing 401 from the upstream.",
			"kubectl -n <namespace> get secret <name> -o jsonpath='{.data}' | jq 'keys'",
			kbBase + "agentgateway-vault-secrets-kind/"}, x.credRefs},
		{spec{"agentic.agents-use-llm-gateway", "Guardrails", "high", "Agents reach models and tools through the gateway",
			"Gateway policy only applies to traffic that goes through it. An agent calling the provider or the MCP server directly skips authentication, guards, budgets and audit.",
			"kind: ModelConfig\nspec:\n  provider: OpenAI\n  openAI:\n    baseUrl: http://ai-gateway.agentgateway-system.svc.cluster.local/v1",
			kbBase + "agentgateway-inference-model-routing-eks/"}, x.useGateway},
		{spec{"agentic.policy-accepted-and-attached", "Resilience", "high", "Policies are accepted, attached and reference objects that exist",
			"A policy whose target is missing, or that the controller rejected, enforces nothing while looking like it does.",
			"kubectl get agentgatewaypolicies,enterpriseagentgatewaypolicies -A -o custom-columns=NAME:.metadata.name,ACCEPTED:.status.ancestors[*].conditions[?(@.type==\"Accepted\")].status",
			kbBase + "agentgateway-mcp-controls/"}, x.policyStatus},
		{spec{"agentic.agent-references-resolve", "Resilience", "high", "Agent references resolve and agents are ready",
			"An Agent naming a missing ModelConfig, tool server, agent or tool does not start, or silently runs without the tool.",
			"kubectl -n <namespace> get agent <name> -o jsonpath='{.status.conditions}'",
			kbBase + "kagent-quickstart-kind/"}, x.agentRefs},
		{spec{"agentic.cors-safe", "Authorization", "high", "CORS does not allow credentials from any origin",
			"allowCredentials with a wildcard origin lets any website call the gateway with the user's cookies or tokens.",
			"traffic:\n  cors:\n    allowOrigins: [https://portal.example.com]\n    allowCredentials: true",
			kbBase + "agentgateway-standalone-aws-enterprise/"}, x.corsSafe},
		{spec{"agentic.llm-mcp-timeouts", "Resilience", "medium", "LLM and MCP routes set a request timeout",
			"Without a timeout a hung model or tool call holds a connection and an agent step indefinitely.",
			"traffic:\n  timeouts:\n    request: 120s",
			kbBase + "agentgateway-mcp-controls/"}, x.timeouts},
		{spec{"agentic.retries-idempotent-only", "Cost", "medium", "Retries on LLM and MCP routes are limited to safe requests",
			"Retrying a completion or a tools/call repeats token spend and side effects. Retry only with a precondition that selects safe requests.",
			"traffic:\n  retry:\n    attempts: 2\n    codes: [503]\n    precondition: 'mcp.method == \"tools/list\"'",
			kbBase + "agentgateway-standalone-aws-ha/"}, x.retries},
		{spec{"agentic.llm-spend-controlled", "Cost", "medium", "LLM spend is capped by a budget or a token rate limit",
			"Without a token budget one runaway agent loop spends the provider key without limit.",
			"traffic:\n  rateLimit:\n    local:\n    - {tokens: 100000, unit: Hours}\n# Enterprise: traffic.entBudgetEnforcement: {} plus an EnterpriseAgentgatewayBudget with onBudgetExceeded: Block",
			kbBase + "agentic-budgets-kind/"}, x.spend},
		{spec{"agentic.rate-limit-key-trusted", "Cost", "high", "Rate limit keys come from verified identity, not raw headers",
			"A descriptor keyed on a request header the client sets can be dodged by sending a new value on every request.",
			"descriptors:\n- entries:\n  - {name: user, expression: 'jwt.sub'}",
			kbBase + "agentgateway-team-key-injection-kind/"}, x.rateLimitKey},
		{spec{"agentic.agent-artifacts-pinned", "Supply chain", "medium", "Agent skills, images and tool packages are pinned",
			"A floating tag, branch or unversioned npx package changes under the agent without review.",
			"skills:\n  refs: [ghcr.io/org/skills@sha256:<digest>]\n  gitRefs: [{url: https://github.com/org/skills, ref: <40-hex commit>}]\n---\ndeployment:\n  cmd: npx\n  args: [\"@modelcontextprotocol/server-github@2025.4.8\"]",
			kbBase + "agentregistry-push-best-practices/"}, x.pinned},
		{spec{"agentic.registry-hardened", "Supply chain", "medium", "Agentregistry runs with audit, approval and an external database",
			"The registry decides which agents and tools reach production. Audit, approval of new artifacts and a managed TLS database keep that decision reviewable.",
			"# agentregistry-enterprise values\nconfig:\n  AUDIT_ENABLED: \"true\"\n  REQUIRE_CREATE_APPROVAL: \"true\"\n  MCP_BRIDGE_AUDIENCE: <bridge audience>\ndatabase:\n  url: postgres://...?sslmode=verify-full",
			kbBase + "agentregistry-governance-kind/"}, x.registry},
		{spec{"agentic.mcp-transport-current", "Resilience", "low", "MCP targets use Streamable HTTP rather than SSE",
			"The SSE transport is deprecated in the MCP specification and holds a long-lived connection per session.",
			"static:\n  host: tools.example.com\n  port: 443\n  protocol: StreamableHTTP",
			kbBase + "agentgw-agentic-mcp/"}, x.transport},
	}
}

func (x *xctx) needPolicies(c *Check) bool {
	if !x.polOK {
		xNA(c, x.polWhy)
		return false
	}
	return true
}

func (x *xctx) needPoliciesRoutes(c *Check) bool {
	if !x.polOK || !x.routesOK {
		xNA(c, strings.TrimSpace(x.polWhy+" "+x.routesWhy))
		return false
	}
	return true
}

// 1
func (x *xctx) a2aAuth(c *Check) {
	if !x.needPoliciesRoutes(c) {
		return
	}
	e, md := x.e, x.md
	var cands []*backend
	for _, b := range md.backends {
		if b.spec["a2a"] != nil {
			cands = append(cands, b)
		}
	}
	agentSvc := map[string]bool{}
	for _, a := range x.agents {
		agentSvc[a.Namespace+"/"+a.Name] = true
	}
	for _, sv := range e.list("", "services") {
		why := ""
		for _, p := range l(path(sv.Obj, "spec", "ports")) {
			if ap := s(m(p)["appProtocol"]); ap == "agentgateway.dev/a2a" || ap == "kgateway.dev/a2a" {
				why = "appProtocol " + ap
			}
		}
		if sv.Name == "kagent-controller" {
			why = "kagent controller"
		}
		if agentSvc[sv.Namespace+"/"+sv.Name] {
			why = "kagent agent"
		}
		if why == "" {
			continue
		}
		b := &backend{o: sv, routes: md.routesTo(map[string]bool{"Service": true}, sv.Namespace, sv.Name)}
		cands = append(cands, b)
	}
	for _, b := range cands {
		var ns []*store.Object
		for _, r := range b.routes {
			if md.northSouth(r) {
				ns = append(ns, r)
			}
		}
		if len(ns) == 0 {
			continue
		}
		eb := &backend{o: b.o, spec: b.spec, routes: ns}
		ps := md.applicable(eb)
		authn, meshOnly := hasAuthn(ps)
		switch {
		case authn:
			c.Items = append(c.Items, e.item(b.o, Pass, "authenticated by "+policyNames(ps)))
		case meshOnly:
			c.Items = append(c.Items, e.item(b.o, Warn, "only mesh identity (source.identity) is checked, not the caller; exposed via "+md.exposure(eb)))
		default:
			c.Items = append(c.Items, e.item(b.o, Fail, "OPEN: A2A endpoint exposed via "+md.exposure(eb)+" with no JWT or external auth ("+policyNames(ps)+")"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no A2A endpoint is exposed on a non-waypoint Gateway")
	}
}

// 2
func (x *xctx) authMode(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	for _, p := range x.md.policies {
		var f finding
		t := m(p.spec["traffic"])
		if a := m(t["apiKeyAuthentication"]); a != nil {
			switch mode := s(a["mode"]); mode {
			case "Optional", "Permissive":
				f.fail("apiKeyAuthentication mode " + mode + " lets requests without a key through")
			default:
				f.ok("apiKeyAuthentication mode " + strings.TrimSpace(mode+" (Strict by default)"))
			}
		}
		if a := m(t["basicAuthentication"]); a != nil {
			if mode := s(a["mode"]); mode == "Optional" {
				f.fail("basicAuthentication mode Optional lets requests without credentials through")
			} else {
				f.ok("basicAuthentication enforcing")
			}
		}
		if a := m(path(p.spec, "backend", "mcp", "authentication")); a != nil {
			if mode := s(a["mode"]); mode != "" && mode != "Strict" {
				f.fail("backend.mcp.authentication mode " + mode)
			} else {
				f.ok("backend.mcp.authentication Strict")
			}
		}
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, p.o))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no API key, basic or MCP authentication is configured")
	}
}

// 3
func (x *xctx) basicUsers(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	for _, p := range x.md.policies {
		a := m(path(p.spec, "traffic", "basicAuthentication"))
		if a == nil {
			continue
		}
		users := l(a["users"])
		if len(users) == 0 {
			c.Items = append(c.Items, x.e.item(p.o, Pass, "users read from a Secret"))
			continue
		}
		st, detail := Warn, fmt.Sprintf("%d users inline in the policy", len(users))
		for _, u := range users {
			us := s(u)
			if strings.Contains(us, ":$apr1$") || strings.Contains(us, ":{SHA}") {
				st, detail = Fail, fmt.Sprintf("%d users inline, at least one hashed with MD5 ($apr1$) or SHA-1 ({SHA})", len(users))
				break
			}
			if strings.Contains(us, "[REDACTED") {
				detail = fmt.Sprintf("%d users inline (hashes redacted in the capture, so the algorithm is unknown)", len(users))
			}
		}
		c.Items = append(c.Items, x.e.item(p.o, st, detail))
	}
	if len(c.Items) == 0 {
		xNA(c, "no basic authentication is configured")
	}
}

// 4
func (x *xctx) mcpAuthnPhase(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	for _, p := range x.md.policies {
		if a := m(path(p.spec, "backend", "mcp", "authentication")); a != nil {
			if len(l(a["audiences"])) == 0 {
				c.Items = append(c.Items, x.e.item(p.o, Fail, "deprecated backend.mcp.authentication with no audiences: any token from "+strings.TrimSpace(s(a["issuer"])+" is accepted")))
			} else {
				c.Items = append(c.Items, x.e.item(p.o, Warn, "deprecated backend.mcp.authentication; move it to traffic.jwtAuthentication.mcp"))
			}
		} else if path(p.spec, "traffic", "jwtAuthentication", "mcp") != nil {
			c.Items = append(c.Items, x.e.item(p.o, Pass, "traffic.jwtAuthentication.mcp"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no MCP authentication is configured")
	}
}

// 5
func (x *xctx) authzIdentity(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	for _, p := range x.md.policies {
		if !policyActive(p) {
			continue
		}
		var needJWT, needKey []string
		for where, a := range authzBlocks(p) {
			ex := exprText(a)
			if strings.Contains(ex, "jwt.") {
				needJWT = append(needJWT, where)
			}
			if strings.Contains(ex, "apiKey.") {
				needKey = append(needKey, where)
			}
		}
		if len(needJWT)+len(needKey) == 0 {
			continue
		}
		sort.Strings(needJWT)
		sort.Strings(needKey)
		var f finding
		missJWT, missKey := false, false
		for _, chain := range x.md.chains(p) {
			hasJWT, hasKey := false, false
			for _, q := range chain {
				t := m(q.spec["traffic"])
				if t["jwtAuthentication"] != nil || path(q.spec, "backend", "mcp", "authentication") != nil {
					hasJWT = true
				}
				if t["apiKeyAuthentication"] != nil {
					hasKey = true
				}
			}
			missJWT = missJWT || (len(needJWT) > 0 && !hasJWT)
			missKey = missKey || (len(needKey) > 0 && !hasKey)
		}
		if missJWT {
			f.fail(strings.Join(needJWT, ", ") + " reads jwt.* but no jwtAuthentication applies to the same route")
		} else if len(needJWT) > 0 {
			f.ok("jwt.* rules backed by jwtAuthentication")
		}
		if missKey {
			f.fail(strings.Join(needKey, ", ") + " reads apiKey.* but no apiKeyAuthentication applies to the same route")
		} else if len(needKey) > 0 {
			f.ok("apiKey.* rules backed by apiKeyAuthentication")
		}
		c.Items = append(c.Items, f.item(x.e, p.o))
	}
	if len(c.Items) == 0 {
		xNA(c, "no authorization rule reads jwt or apiKey claims")
	}
}

// 6
func (x *xctx) gatewayNamespaces(c *Check) {
	gws := x.md.agentgatewayGateways()
	if len(gws) == 0 {
		xNA(c, "no agentgateway Gateways")
		return
	}
	for _, gw := range gws {
		var all, other []string
		for _, ls := range l(path(gw.Obj, "spec", "listeners")) {
			lm := m(ls)
			from := s(path(lm, "allowedRoutes", "namespaces", "from"))
			if from == "" {
				from = "Same"
			}
			if from == "All" {
				all = append(all, s(lm["name"]))
			} else {
				other = append(other, s(lm["name"])+": "+from)
			}
		}
		if len(all) > 0 {
			attached := map[string]bool{}
			for _, r := range x.md.routes {
				for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
					pm := m(pr)
					if s(pm["name"]) == gw.Name && refNS(pm, r.Namespace) == gw.Namespace && r.Namespace != gw.Namespace {
						attached[r.Namespace] = true
					}
				}
			}
			d := "listener " + strings.Join(all, ", ") + " accepts routes from every namespace"
			if len(attached) > 0 {
				d += "; routes attach from " + strings.Join(xSortedKeys(attached), ", ")
			}
			c.Items = append(c.Items, x.e.item(gw, Warn, d))
		} else {
			c.Items = append(c.Items, x.e.item(gw, Pass, strings.Join(other, ", ")))
		}
	}
}

// 7
func (x *xctx) forwardProxy(c *Check) {
	if !x.needPoliciesRoutes(c) {
		return
	}
	for _, b := range x.md.backends {
		if b.spec["dynamicForwardProxy"] == nil || len(b.routes) == 0 {
			continue
		}
		ps := x.md.applicable(b)
		authn, _ := hasAuthn(ps)
		hosts := false
		for _, p := range ps {
			if a := m(path(p.spec, "traffic", "authorization")); a != nil && strings.Contains(exprText(a), "request.host") {
				hosts = true
			}
		}
		switch {
		case !authn && !hosts:
			c.Items = append(c.Items, x.e.item(b.o, Fail, "OPEN forward proxy: exposed via "+x.md.exposure(b)+" with no authentication and no request.host allowlist"))
		case !hosts:
			c.Items = append(c.Items, x.e.item(b.o, Warn, "callers authenticated, but any destination host is allowed"))
		case !authn:
			c.Items = append(c.Items, x.e.item(b.o, Warn, "destination hosts allowlisted, but callers are not authenticated"))
		default:
			c.Items = append(c.Items, x.e.item(b.o, Pass, "authenticated, with a request.host allowlist"))
		}
	}
	for _, p := range x.md.policies {
		if !policyActive(p) || s(path(p.spec, "frontend", "connect", "mode")) != "Tunnel" {
			continue
		}
		authn := false
		for _, t := range p.targets {
			if t.kind != "Gateway" {
				continue
			}
			if gw := x.md.gateways[p.o.Namespace+"/"+t.name]; gw != nil {
				if a, _ := hasAuthn(x.md.gatewayPolicies(gw)); a {
					authn = true
				}
			}
		}
		if authn {
			c.Items = append(c.Items, x.e.item(p.o, Pass, "CONNECT tunnel with gateway-level authentication"))
		} else {
			c.Items = append(c.Items, x.e.item(p.o, Fail, "frontend.connect mode Tunnel with no authentication on the Gateway"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no exposed dynamic forward proxy or CONNECT tunnel")
	}
}

// 8
func (x *xctx) crossNamespace(c *Check) {
	for _, o := range append(append([]*store.Object{}, x.agents...), x.rms...) {
		from := s(path(o.Obj, "spec", "allowedNamespaces", "from"))
		switch from {
		case "All":
			c.Items = append(c.Items, x.e.item(o, Warn, "allowedNamespaces from: All: any namespace can reference it"))
		case "":
			c.Items = append(c.Items, x.e.item(o, Pass, "same namespace only (default)"))
		default:
			c.Items = append(c.Items, x.e.item(o, Pass, "allowedNamespaces from: "+from))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no kagent Agents or RemoteMCPServers")
	}
}

// 9
func (x *xctx) accessBaseline(c *Check) {
	const grp, res, kind = "policy.kagent-enterprise.solo.io", "accesspolicies", "AccessPolicy"
	if _, known := x.e.ws.FindResource(x.e.cluster, res+"."+grp); !known {
		xNA(c, "kagent-enterprise AccessPolicy is not installed")
		return
	}
	ok, why := x.e.absenceOK(grp, res, kind, true)
	if !ok {
		xNA(c, why)
		return
	}
	if len(x.agents) == 0 {
		xNA(c, "no kagent agents")
		return
	}
	aps := x.e.list(grp, res)
	nss := map[string]bool{}
	for _, a := range x.agents {
		nss[a.Namespace] = true
	}
	for _, ns := range xSortedKeys(nss) {
		base := false
		for _, ap := range aps {
			if ap.Namespace == ns && s(path(ap.Obj, "spec", "targetRef", "name")) == "*" && len(l(path(ap.Obj, "spec", "from", "subjects"))) == 0 {
				base = true
			}
		}
		if base {
			c.Items = append(c.Items, x.e.item(nsObj(ns), Pass, "deny-all baseline AccessPolicy"))
		} else {
			c.Items = append(c.Items, x.e.item(nsObj(ns), Fail, "no deny-all baseline (targetRef name \"*\" with no subjects)"))
		}
	}
	for _, a := range x.agents {
		named := false
		for _, ap := range aps {
			sp := m(ap.Obj["spec"])
			if ap.Namespace == a.Namespace && s(sp["action"]) == "ALLOW" && s(path(sp, "targetRef", "kind")) == "Agent" && s(path(sp, "targetRef", "name")) == a.Name {
				named = true
			}
		}
		if named {
			c.Items = append(c.Items, x.e.item(a, Pass, "an ALLOW AccessPolicy names this agent"))
		} else {
			c.Items = append(c.Items, x.e.item(a, Warn, "no ALLOW AccessPolicy names this agent"))
		}
	}
	for _, ap := range aps {
		switch st := s(path(ap.Obj, "status", "state")); st {
		case "Failed", "Pending":
			c.Items = append(c.Items, x.e.item(ap, Fail, "status.state "+st+": "+s(path(ap.Obj, "status", "message"))))
		case "PartiallyApplied":
			c.Items = append(c.Items, x.e.item(ap, Warn, "status.state PartiallyApplied: "+s(path(ap.Obj, "status", "message"))))
		}
	}
}

// 10
func (x *xctx) toolServerSharing(c *Check) {
	if len(x.rms) == 0 {
		xNA(c, "no RemoteMCPServers")
		return
	}
	users := map[string][]string{}
	tools := map[string]map[string]bool{}
	for _, a := range x.agents {
		for _, t := range agentTools(a) {
			ms := m(t["mcpServer"])
			if ms == nil || s(ms["kind"]) != "RemoteMCPServer" {
				continue
			}
			key := refNS(ms, a.Namespace) + "/" + s(ms["name"])
			users[key] = append(users[key], a.Namespace+"/"+a.Name)
			if tools[key] == nil {
				tools[key] = map[string]bool{}
			}
			for _, n := range l(ms["toolNames"]) {
				tools[key][s(n)] = true
			}
		}
	}
	secretUse := map[string][]string{}
	for _, r := range x.rms {
		for _, h := range l(path(r.Obj, "spec", "headersFrom")) {
			vf := m(m(h)["valueFrom"])
			if s(vf["type"]) == "Secret" && s(vf["name"]) != "" {
				k := r.Namespace + "/" + s(vf["name"]) + ":" + s(vf["key"])
				secretUse[k] = append(secretUse[k], r.Name)
			}
		}
	}
	for _, r := range x.rms {
		var f finding
		key := r.Namespace + "/" + r.Name
		if u := users[key]; len(u) > 1 {
			var risky []string
			for _, n := range xSortedKeys(tools[key]) {
				if matchesAny(n, riskyVerbs) {
					risky = append(risky, n)
				}
			}
			if len(risky) > 0 {
				f.warn(fmt.Sprintf("shared by %s and exposes write tools %s", strings.Join(u, ", "), strings.Join(risky, ", ")))
			} else {
				f.ok("shared by " + strings.Join(u, ", ") + ", read-only tools")
			}
		} else {
			f.ok(fmt.Sprintf("used by %d agent(s)", len(u)))
		}
		for k, names := range secretUse {
			if strings.HasPrefix(k, r.Namespace+"/") && len(names) > 1 {
				for _, n := range names {
					if n == r.Name {
						f.warn("shares header Secret " + strings.TrimPrefix(k, r.Namespace+"/") + " with " + strings.Join(names, ", "))
						break
					}
				}
			}
		}
		c.Items = append(c.Items, f.item(x.e, r))
	}
}

// 11
func (x *xctx) prefixMode(c *Check) {
	for _, b := range x.mcp {
		n := len(l(b.mcp["targets"]))
		if n < 2 {
			continue
		}
		if s(b.mcp["prefixMode"]) == "Never" {
			c.Items = append(c.Items, x.e.item(b.o, Warn, fmt.Sprintf("%d targets with prefixMode Never: tool names can collide", n)))
		} else {
			c.Items = append(c.Items, x.e.item(b.o, Pass, fmt.Sprintf("%d targets, tool names prefixed", n)))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no MCP backend has more than one target")
	}
}

// 12
func (x *xctx) selectorTargets(c *Check) {
	for _, b := range x.mcp {
		for _, t := range l(b.mcp["targets"]) {
			sel := m(m(t)["selector"])
			if sel == nil {
				continue
			}
			name := s(m(t)["name"])
			svc := m(sel["services"])
			svcScoped := len(m(svc["matchLabels"])) > 0 || len(l(svc["matchExpressions"])) > 0
			switch {
			case sel["namespaces"] != nil && !svcScoped:
				c.Items = append(c.Items, x.e.item(b.o, Fail, "target "+name+" selects every Service in the selected namespaces"))
			case sel["namespaces"] != nil:
				c.Items = append(c.Items, x.e.item(b.o, Warn, "target "+name+" federates labelled Services across namespaces"))
			default:
				c.Items = append(c.Items, x.e.item(b.o, Pass, "target "+name+" selects Services in its own namespace"))
			}
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no MCP selector targets")
	}
}

// 13
func (x *xctx) codeMode(c *Check) {
	for _, b := range x.md.backends {
		mode := s(path(b.spec, "entMcp", "toolMode"))
		if mode != "Code" && mode != "CodeSearch" {
			continue
		}
		if !x.polOK {
			xNA(c, x.polWhy)
			return
		}
		var f finding
		allow := false
		for _, p := range x.md.applicable(b) {
			if a := m(path(p.spec, "backend", "mcp", "authorization")); a != nil && strings.Contains(exprText(a), "mcp.tool.name") {
				allow = true
			}
		}
		if !allow {
			f.fail("toolMode " + mode + " with no backend.mcp.authorization naming tools (mcp.tool.name)")
		} else {
			f.ok("toolMode " + mode + " with a tool allowlist")
		}
		cm := m(path(b.spec, "entMcp", "codeMode"))
		if d := toDur(cm["timeout"]); d > 60*time.Second {
			f.warn("codeMode.timeout " + s(cm["timeout"]) + " is over 60s")
		}
		if d := toDur(cm["cpuTimeout"]); d > time.Second {
			f.warn("codeMode.cpuTimeout " + s(cm["cpuTimeout"]) + " is over 1s")
		}
		c.Items = append(c.Items, f.item(x.e, b.o))
	}
	if len(c.Items) == 0 && c.Status != NA {
		xNA(c, "no Code or CodeSearch MCP backends")
	}
}

// 14
func (x *xctx) guardStreaming(c *Check) {
	if len(x.ai) == 0 {
		xNA(c, "no LLM (AI) backends")
		return
	}
	// Agents that stream, by the gateway namespace their ModelConfig points at.
	streamNS := map[string][]string{}
	for _, a := range x.agents {
		if !truthy(path(a.Obj, "spec", "declarative", "stream")) {
			continue
		}
		mc := x.e.ws.Get(x.e.cluster, "kagent.dev", "modelconfigs", a.Namespace, s(path(a.Obj, "spec", "declarative", "modelConfig")))
		if mc == nil {
			continue
		}
		if _, base, ok := modelBaseURL(mc); ok && base != "" {
			_, h := parseURL(base)
			if ns := hostNS(h, mc.Namespace); ns != "" {
				streamNS[ns] = append(streamNS[ns], a.Name)
			}
		}
	}
	for _, b := range x.ai {
		type guard struct {
			where string
			g     map[string]any
		}
		var gs []guard
		if g := m(path(b.spec, "policies", "ai", "promptGuard")); len(l(g["response"])) > 0 {
			gs = append(gs, guard{"backend", g})
		}
		for _, p := range x.md.applicable(b) {
			for _, k := range []string{"promptGuard", "entPromptGuard"} {
				if g := m(path(p.spec, "backend", "ai", k)); len(l(g["response"])) > 0 {
					gs = append(gs, guard{p.o.Name + " " + k, g})
				}
			}
		}
		if len(gs) == 0 {
			continue
		}
		var f finding
		var streamers []string
		for _, r := range b.routes {
			for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
				streamers = append(streamers, streamNS[refNS(m(pr), r.Namespace)]...)
			}
		}
		for _, g := range gs {
			switch {
			case s(g.g["streaming"]) == "Enabled":
				f.ok(g.where + ": streaming Enabled")
			case len(streamers) > 0:
				f.fail(g.where + ": response guard skips streamed responses, and agents " + strings.Join(streamers, ", ") + " stream")
			default:
				f.warn(g.where + ": response guard skips streamed responses (streaming not Enabled)")
			}
		}
		c.Items = append(c.Items, f.item(x.e, b.o))
	}
	if len(c.Items) == 0 {
		xNA(c, "no response guards")
	}
}

// 15
func (x *xctx) failsClosed(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	check := func(o *store.Object, spec map[string]any) {
		var f finding
		failOpen := func(where string, v any) (found bool) {
			for _, fm := range findKey(v, "failureMode") {
				found = true
				if s(fm) == "FailOpen" {
					f.fail(where + " is FailOpen")
					return
				}
			}
			return
		}
		seen := false
		for _, g := range []string{"promptGuard", "entPromptGuard"} {
			for _, base := range [][]string{{"backend", "ai", g}, {"policies", "ai", g}} {
				if v := path(spec, base...); v != nil {
					for _, dir := range []string{"request", "response"} {
						for _, it := range l(m(v)[dir]) {
							if wh := m(m(it)["webhook"]); wh != nil {
								seen = true
								failOpen(g+"."+dir+" webhook", wh)
							}
						}
					}
				}
			}
		}
		for _, pr := range l(path(spec, "backend", "mcp", "guardrails", "processors")) {
			seen = true
			failOpen("mcp guardrails processor", m(pr)["remote"])
		}
		for where, keys := range map[string][]string{"traffic.extAuth": {"traffic", "extAuth"}, "backend.extAuth": {"backend", "extAuth"}, "traffic.extProc": {"traffic", "extProc"}} {
			if v := path(spec, keys...); v != nil {
				seen = true
				failOpen(where, v)
			}
		}
		if v := path(spec, "traffic", "rateLimit"); v != nil {
			for _, g := range findKey(v, "global") {
				seen = true
				if s(m(g)["failureMode"]) == "FailOpen" {
					f.warn("rateLimit.global is FailOpen")
				}
			}
		}
		if seen {
			if !f.any() {
				f.ok("enforcement fails closed")
			}
			c.Items = append(c.Items, f.item(x.e, o))
		}
	}
	for _, p := range x.md.policies {
		check(p.o, p.spec)
	}
	for _, b := range x.md.backends {
		check(b.o, b.spec)
	}
	if len(c.Items) == 0 {
		xNA(c, "no guardrail webhooks, external auth, external processing or global rate limits")
	}
}

// 16
func (x *xctx) guardrailMethods(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	for _, p := range x.md.policies {
		procs := l(path(p.spec, "backend", "mcp", "guardrails", "processors"))
		if len(procs) == 0 {
			continue
		}
		covered := false
		for _, pr := range procs {
			methods := m(m(pr)["methods"])
			phase := ""
			for _, k := range []string{"tools/call", "tools/*", "*/call", "*"} {
				if v, ok := methods[k]; ok {
					phase = s(v)
					break
				}
			}
			if phase != "" && phase != "Off" {
				covered = true
			}
		}
		if covered {
			c.Items = append(c.Items, x.e.item(p.o, Pass, "tools/call is inspected"))
		} else {
			c.Items = append(c.Items, x.e.item(p.o, Fail, "no guardrail processor covers tools/call"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no MCP guardrails")
	}
}

// 17
var broadDomain = regexp.MustCompile(`^\*\.[A-Za-z0-9-]+$`)

func (x *xctx) sandboxEgress(c *Check) {
	for _, a := range x.agents {
		doms := l(path(a.Obj, "spec", "sandbox", "network", "allowedDomains"))
		if path(a.Obj, "spec", "sandbox") == nil {
			continue
		}
		var f finding
		for _, d := range doms {
			ds := s(d)
			switch {
			case ds == "*":
				f.fail("allowedDomains contains \"*\": any destination")
			case broadDomain.MatchString(ds):
				f.warn("allowedDomains contains " + ds + ": a whole top-level domain")
			}
		}
		if !f.any() {
			if len(doms) == 0 {
				f.ok("no outbound access (default deny)")
			} else {
				f.ok(fmt.Sprintf("%d named domains", len(doms)))
			}
		}
		c.Items = append(c.Items, f.item(x.e, a))
	}
	if len(c.Items) == 0 {
		xNA(c, "no agents with a sandbox")
	}
}

// 18
func (x *xctx) tlsVerified(c *Check) {
	insecure := func(f *finding, where string, tls any) {
		if v := s(m(tls)["insecureSkipVerify"]); v != "" {
			f.fail(where + " insecureSkipVerify " + v)
		}
	}
	for _, b := range x.md.backends {
		var f finding
		insecure(&f, "policies.tls", path(b.spec, "policies", "tls"))
		for _, t := range l(b.mcp["targets"]) {
			insecure(&f, "target "+s(m(t)["name"]), path(t, "static", "policies", "tls"))
		}
		for _, g := range l(path(b.ai, "groups")) {
			for _, pr := range l(m(g)["providers"]) {
				insecure(&f, "provider "+s(m(pr)["name"]), path(pr, "policies", "tls"))
			}
		}
		if !f.any() {
			f.ok("no insecureSkipVerify")
		}
		c.Items = append(c.Items, f.item(x.e, b.o))
	}
	for _, p := range x.md.policies {
		if t := path(p.spec, "backend", "tls"); t != nil {
			var f finding
			insecure(&f, "backend.tls", t)
			if !f.any() {
				f.ok("backend.tls without insecureSkipVerify")
			}
			c.Items = append(c.Items, f.item(x.e, p.o))
		}
	}
	boolCheck := func(o *store.Object, where string, keys ...string) {
		if truthy(path(o.Obj, keys...)) {
			c.Items = append(c.Items, x.e.item(o, Fail, where+" is true"))
		} else {
			c.Items = append(c.Items, x.e.item(o, Pass, "verification not disabled"))
		}
	}
	for _, o := range x.mcs {
		boolCheck(o, "tls.disableVerify", "spec", "tls", "disableVerify")
	}
	for _, o := range x.rms {
		boolCheck(o, "tls.disableVerify", "spec", "tls", "disableVerify")
	}
	for _, o := range x.mcpservers {
		boolCheck(o, "httpTransport.tls.insecureSkipVerify", "spec", "httpTransport", "tls", "insecureSkipVerify")
	}
	for _, a := range x.agents {
		if path(a.Obj, "spec", "skills") != nil {
			boolCheck(a, "skills.insecureSkipVerify", "spec", "skills", "insecureSkipVerify")
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no backends, tool servers or ModelConfigs")
	}
}

// 19
func (x *xctx) plaintextCreds(c *Check) {
	nsl, nsKnown := x.e.nsLabels()
	ambient := func(ns string) bool { return nsKnown && nsl[ns]["istio.io/dataplane-mode"] == "ambient" }
	judge := func(o *store.Object, what, raw string) {
		scheme, host := parseURL(raw)
		if scheme != "http" {
			c.Items = append(c.Items, x.e.item(o, Pass, what+" sent over "+strings.ToUpper(scheme)))
			return
		}
		switch {
		case !clusterLocal(host):
			c.Items = append(c.Items, x.e.item(o, Fail, what+" sent in clear text to "+host+" over plain HTTP"))
		case ambient(o.Namespace) && ambient(hostNS(host, o.Namespace)):
			c.Items = append(c.Items, x.e.item(o, Pass, what+" over http:// to "+host+", both namespaces in ambient (mTLS)"))
		default:
			c.Items = append(c.Items, x.e.item(o, Warn, what+" over http:// to "+host+" outside the ambient mesh"))
		}
	}
	for _, r := range x.rms {
		if len(l(path(r.Obj, "spec", "headersFrom"))) > 0 {
			judge(r, "tool headers", s(path(r.Obj, "spec", "url")))
		}
	}
	for _, mc := range x.mcs {
		if s(path(mc.Obj, "spec", "apiKeySecret")) == "" {
			continue
		}
		prov, base, ok := modelBaseURL(mc)
		if !ok {
			continue
		}
		if base == "" {
			c.Items = append(c.Items, x.e.item(mc, Pass, "API key sent to the "+prov+" default endpoint over HTTPS"))
			continue
		}
		judge(mc, "API key", base)
	}
	if len(c.Items) == 0 {
		xNA(c, "no tool servers or ModelConfigs send credentials")
	}
}

// 20
func (x *xctx) listenerTLS(c *Check) {
	for _, gw := range x.md.agentgatewayGateways() {
		sv := x.e.gatewayService(gw)
		if !exposedService(sv) {
			continue
		}
		var f finding
		https, plain := false, false
		for _, ls := range l(path(gw.Obj, "spec", "listeners")) {
			lm := m(ls)
			proto, port := s(lm["protocol"]), toNum(lm["port"])
			switch {
			case proto == "HTTP" && port == 443:
				f.fail("listener " + s(lm["name"]) + " is plain HTTP on port 443")
			case proto == "HTTP":
				plain = true
			case proto == "HTTPS" && len(l(path(lm, "tls", "certificateRefs"))) > 0:
				https = true
			}
		}
		switch {
		case https:
			f.ok("HTTPS listener with a certificate")
		case plain:
			f.warn("Service " + sv.Name + " is " + s(path(sv.Obj, "spec", "type")) + " and every listener is plain HTTP")
		}
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, gw))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no agentgateway Gateway is exposed outside the cluster (or its Service was not captured)")
	}
}

// 21
func (x *xctx) passthrough(c *Check) {
	pt := func(auth any) bool { return hasKey(m(auth), "passthrough") }
	for _, b := range x.md.backends {
		if b.mcp == nil && b.ai == nil {
			continue
		}
		var ps []policy
		if x.polOK {
			ps = x.md.applicable(b)
		}
		var f finding
		via := ""
		if pt(path(b.spec, "policies", "auth")) {
			via = "spec.policies.auth"
		}
		for _, t := range l(b.mcp["targets"]) {
			if pt(path(t, "static", "policies", "auth")) {
				via = "target " + s(m(t)["name"])
			}
		}
		for _, p := range ps {
			if pt(path(p.spec, "backend", "auth")) {
				via = "backend.auth in " + p.o.Name
			}
		}
		switch {
		case via == "":
			f.ok("no token passthrough")
		case b.mcp != nil:
			f.fail("the caller's token is passed through to the MCP upstream (" + via + ")")
		default:
			if authn, _ := hasAuthn(ps); authn {
				f.ok("caller's provider key passed through (" + via + "), callers authenticated")
			} else {
				f.warn("caller's token passed through to the LLM provider (" + via + ") with no gateway authentication")
			}
		}
		c.Items = append(c.Items, f.item(x.e, b.o))
	}
	for _, mc := range x.mcs {
		if truthy(path(mc.Obj, "spec", "apiKeyPassthrough")) {
			c.Items = append(c.Items, x.e.item(mc, Warn, "apiKeyPassthrough: the caller's bearer token is forwarded to the model provider"))
		} else {
			c.Items = append(c.Items, x.e.item(mc, Pass, "no apiKeyPassthrough"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no LLM or MCP backends or ModelConfigs")
	}
}

// 22
func (x *xctx) inlineCreds(c *Check) {
	envCheck := func(f *finding, where string, env []any) {
		for _, ev := range env {
			em := m(ev)
			n := s(em["name"])
			if sensitiveEnv.MatchString(n) && !settingEnv.MatchString(n) && s(em["value"]) != "" {
				f.fail(where + " env " + n + " has a literal value")
			}
		}
	}
	headers := func(f *finding, where string, hs []any) {
		for _, h := range hs {
			hm := m(h)
			if authHeader.MatchString(s(hm["name"])) && s(hm["value"]) != "" {
				f.fail(where + " header " + s(hm["name"]) + " has a literal value")
			}
		}
	}
	for _, b := range x.md.backends {
		var f finding
		if s(path(b.spec, "policies", "auth", "key")) != "" {
			f.fail("spec.policies.auth.key holds a literal key")
		}
		for _, t := range l(b.mcp["targets"]) {
			if s(path(t, "static", "policies", "auth", "key")) != "" {
				f.fail("target " + s(m(t)["name"]) + " auth.key holds a literal key")
			}
		}
		if !f.any() {
			f.ok("no inline keys")
		}
		c.Items = append(c.Items, f.item(x.e, b.o))
	}
	for _, p := range x.md.policies {
		if s(path(p.spec, "backend", "auth", "key")) != "" {
			c.Items = append(c.Items, x.e.item(p.o, Fail, "backend.auth.key holds a literal key"))
		}
	}
	for _, mc := range x.mcs {
		var f finding
		if s(path(mc.Obj, "spec", "azureOpenAI", "azureAdToken")) != "" {
			f.fail("azureOpenAI.azureAdToken holds a literal token")
		}
		for k, v := range m(path(mc.Obj, "spec", "defaultHeaders")) {
			if authDefault.MatchString(k) && s(v) != "" {
				f.fail("defaultHeaders " + k + " holds a literal credential")
			}
		}
		if !f.any() {
			f.ok("no inline credentials")
		}
		c.Items = append(c.Items, f.item(x.e, mc))
	}
	for _, r := range x.rms {
		var f finding
		headers(&f, "headersFrom", l(path(r.Obj, "spec", "headersFrom")))
		if !f.any() {
			f.ok("no inline header credentials")
		}
		c.Items = append(c.Items, f.item(x.e, r))
	}
	for _, o := range x.mcpservers {
		var f finding
		for k, v := range m(path(o.Obj, "spec", "deployment", "env")) {
			if sensitiveEnv.MatchString(k) && !settingEnv.MatchString(k) && s(v) != "" {
				f.fail("deployment env " + k + " has a literal value; use secretRefs")
			}
		}
		if !f.any() {
			f.ok("no inline credentials")
		}
		c.Items = append(c.Items, f.item(x.e, o))
	}
	for _, a := range x.agents {
		var f finding
		for _, t := range agentTools(a) {
			headers(&f, "tool "+s(m(t["mcpServer"])["name"]), l(t["headersFrom"]))
		}
		envCheck(&f, "skills.initContainer", l(path(a.Obj, "spec", "skills", "initContainer", "env")))
		if !f.any() {
			f.ok("no inline credentials in tools or skills")
		}
		c.Items = append(c.Items, f.item(x.e, a))
	}
	if len(c.Items) == 0 {
		xNA(c, "no agentgateway backends or kagent resources")
	}
}

// 23
func (x *xctx) credRefs(c *Check) {
	ok, why := x.e.evaluable("", "secrets", "Secret", true)
	if !ok {
		xNA(c, why)
		return
	}
	resolve := func(f *finding, what, ns, name, key string) {
		keys, exists, known := x.e.secretKeys(ns, name)
		switch {
		case !exists:
			f.fail(what + ": Secret " + ns + "/" + name + " does not exist")
		case !known:
			f.ok(what + ": Secret " + name + " exists (keys not captured)")
		case key == "":
			f.ok(what + ": Secret " + name)
		default:
			if _, has := keys[key]; has {
				f.ok(what + ": Secret " + name + " has key " + key)
			} else {
				f.fail(what + ": Secret " + ns + "/" + name + " has no key " + key)
			}
		}
	}
	authRef := func(f *finding, what, ns string, auth any) {
		sr := m(m(auth)["secretRef"])
		if sr == nil || (s(sr["kind"]) != "" && s(sr["kind"]) != "Secret") {
			return
		}
		key := s(sr["key"])
		if key == "" {
			key = "Authorization"
		}
		resolve(f, what, ns, s(sr["name"]), key)
	}
	for _, b := range x.md.backends {
		var f finding
		authRef(&f, "policies.auth", b.o.Namespace, path(b.spec, "policies", "auth"))
		for _, t := range l(b.mcp["targets"]) {
			authRef(&f, "target "+s(m(t)["name"]), b.o.Namespace, path(t, "static", "policies", "auth"))
		}
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, b.o))
		}
	}
	for _, p := range x.md.policies {
		var f finding
		authRef(&f, "backend.auth", p.o.Namespace, path(p.spec, "backend", "auth"))
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, p.o))
		}
	}
	headerRefs := func(f *finding, where, ns string, hs []any) {
		for _, h := range hs {
			vf := m(m(h)["valueFrom"])
			if s(vf["type"]) == "Secret" {
				resolve(f, where+" header "+s(m(h)["name"]), ns, s(vf["name"]), s(vf["key"]))
			}
		}
	}
	for _, mc := range x.mcs {
		var f finding
		sp := m(mc.Obj["spec"])
		if name, key := s(sp["apiKeySecret"]), s(sp["apiKeySecretKey"]); name != "" && !truthy(sp["apiKeyPassthrough"]) {
			if isRedacted(name) {
				// The capture scrubs the name (its field name contains
				// "secret"); the key is kept, so look for a Secret holding it.
				var holders []string
				for _, sec := range x.e.ws.List(x.e.cluster, "", "secrets", mc.Namespace, nil) {
					if keys, _, known := x.e.secretKeys(sec.Namespace, sec.Name); known {
						if _, has := keys[key]; has || key == "" {
							holders = append(holders, sec.Name)
						}
					}
				}
				if len(holders) > 0 {
					f.ok("apiKeySecret (name redacted in the capture): " + strings.Join(holders, ", ") + " holds key " + key)
				} else {
					f.fail("apiKeySecret (name redacted in the capture): no Secret in " + mc.Namespace + " holds key " + key)
				}
			} else {
				resolve(&f, "apiKeySecret", mc.Namespace, name, key)
			}
		}
		if ca := s(path(sp, "tls", "caCertSecretRef")); ca != "" {
			resolve(&f, "tls.caCertSecretRef", mc.Namespace, ca, s(path(sp, "tls", "caCertSecretKey")))
		}
		if st, reason, msg, found := condition(mc, "Accepted"); found && st == "False" {
			f.fail("Accepted False: " + strings.TrimSpace(reason+" "+msg))
		}
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, mc))
		}
	}
	for _, r := range x.rms {
		var f finding
		headerRefs(&f, "headersFrom", r.Namespace, l(path(r.Obj, "spec", "headersFrom")))
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, r))
		}
	}
	for _, a := range x.agents {
		var f finding
		for _, t := range agentTools(a) {
			headerRefs(&f, "tool "+s(m(t["mcpServer"])["name"]), a.Namespace, l(t["headersFrom"]))
		}
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, a))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no credential references")
	}
}

// 24
func (x *xctx) useGateway(c *Check) {
	gwNS := map[string]bool{}
	hostnames := map[string]bool{}
	for _, gw := range x.md.agentgatewayGateways() {
		gwNS[gw.Namespace] = true
		for _, r := range x.md.routes {
			for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
				pm := m(pr)
				if s(pm["name"]) == gw.Name && refNS(pm, r.Namespace) == gw.Namespace {
					for _, h := range l(path(r.Obj, "spec", "hostnames")) {
						hostnames[strings.ToLower(s(h))] = true
					}
				}
			}
		}
	}
	if len(x.ai) > 0 {
		usedMC := x.usedModelConfigs()
		for _, mc := range x.mcs {
			prov, base, ok := modelBaseURL(mc)
			if !ok || !usedMC[mc.Namespace+"/"+mc.Name] {
				continue // only ModelConfigs an agent actually runs on
			}
			_, host := parseURL(base)
			switch {
			case base == "":
				c.Items = append(c.Items, x.e.item(mc, Warn, "no baseUrl: calls "+prov+" directly, not through the gateway"))
			case gwNS[hostNS(host, mc.Namespace)] || hostnames[host]:
				c.Items = append(c.Items, x.e.item(mc, Pass, "baseUrl "+base+" is an agentgateway"))
			default:
				c.Items = append(c.Items, x.e.item(mc, Warn, "baseUrl "+base+" is not an agentgateway in this cluster"))
			}
		}
	}
	targets := map[string]string{}
	for _, b := range x.mcp {
		for _, t := range mcpTargets(b) {
			targets[normHost(s(t["host"]), b.o.Namespace)] = b.o.Namespace + "/" + b.o.Name
		}
	}
	used := map[string]bool{}
	for _, a := range x.agents {
		for _, t := range agentTools(a) {
			if ms := m(t["mcpServer"]); ms != nil && s(ms["kind"]) == "RemoteMCPServer" {
				used[refNS(ms, a.Namespace)+"/"+s(ms["name"])] = true
			}
		}
	}
	for _, r := range x.rms {
		_, host := parseURL(s(path(r.Obj, "spec", "url")))
		be, bypass := targets[normHost(host, r.Namespace)]
		switch {
		case !used[r.Namespace+"/"+r.Name]:
			continue // no agent uses it
		case bypass:
			c.Items = append(c.Items, x.e.item(r, Fail, "points straight at "+host+", the upstream of MCP backend "+be+": agents skip the gateway"))
		case gwNS[hostNS(host, r.Namespace)] || hostnames[host]:
			c.Items = append(c.Items, x.e.item(r, Pass, "reaches its tools through the gateway ("+host+")"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no agent runs on a ModelConfig alongside the LLM backends, and no agent tool server could bypass an MCP backend")
	}
}

// usedModelConfigs returns the ModelConfigs (ns/name) declarative agents
// run on. An agent that names none uses the controller's default, set by
// the kagent-controller flag -default-model-config-name.
func (x *xctx) usedModelConfigs() map[string]bool {
	def, defNS := "default-model-config", ""
	for _, d := range x.e.list("apps", "deployments") {
		for _, ct := range containers(podSpec(d)) {
			args := l(ct.c["args"])
			for i, a := range args {
				if s(a) == "-default-model-config-name" || s(a) == "--default-model-config-name" {
					if i+1 < len(args) {
						def, defNS = s(args[i+1]), d.Namespace
					}
				}
			}
		}
	}
	out := map[string]bool{}
	mark := func(agentNS, ref string) {
		if i := strings.Index(ref, "/"); i > 0 {
			out[ref] = true
		} else if ref != "" {
			out[agentNS+"/"+ref] = true
		}
	}
	for _, a := range x.agents {
		d := m(path(a.Obj, "spec", "declarative"))
		if d == nil {
			continue
		}
		if mc := s(d["modelConfig"]); mc != "" {
			mark(a.Namespace, mc)
		} else if defNS != "" {
			out[defNS+"/"+def] = true
		} else {
			mark(a.Namespace, def)
		}
		mark(a.Namespace, s(path(d, "memory", "modelConfig")))
		mark(a.Namespace, s(path(d, "context", "compaction", "summarizer", "modelConfig")))
	}
	return out
}

// 25
var targetTypes = map[string][3]string{
	"HTTPRoute":                     {"gateway.networking.k8s.io", "httproutes", "HTTPRoute"},
	"Gateway":                       {"gateway.networking.k8s.io", "gateways", "Gateway"},
	"AgentgatewayBackend":           {"agentgateway.dev", "agentgatewaybackends", "AgentgatewayBackend"},
	"EnterpriseAgentgatewayBackend": {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends", "EnterpriseAgentgatewayBackend"},
	"Service":                       {"", "services", "Service"},
}

func (x *xctx) policyStatus(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	for _, p := range x.md.policies {
		var f finding
		for _, t := range p.targets {
			tt, known := targetTypes[t.kind]
			if !known {
				continue
			}
			if ok, _ := x.e.absenceOK(tt[0], tt[1], tt[2], true); !ok {
				continue
			}
			if x.e.ws.Get(x.e.cluster, tt[0], tt[1], p.o.Namespace, t.name) == nil {
				f.fail("target " + t.kind + " " + p.o.Namespace + "/" + t.name + " does not exist")
			}
		}
		for _, typ := range []string{"Accepted", "Attached"} {
			st, reason, msg, found := condition(p.o, typ)
			switch {
			case found && st == "False":
				f.fail(typ + " False: " + strings.TrimSpace(reason+" "+msg))
			case found && reason == "PartiallyValid":
				f.warn(typ + " PartiallyValid: " + msg)
			}
		}
		t := m(p.spec["traffic"])
		ref := func(what, grp, res, kind string, r map[string]any) {
			if r == nil || s(r["name"]) == "" {
				return
			}
			if ok, _ := x.e.absenceOK(grp, res, kind, true); !ok {
				return
			}
			ns := refNS(r, p.o.Namespace)
			if x.e.ws.Get(x.e.cluster, grp, res, ns, s(r["name"])) == nil {
				f.fail(what + " " + kind + " " + ns + "/" + s(r["name"]) + " does not exist")
			}
		}
		for _, r := range l(path(t, "entRateLimit", "global", "rateLimitConfigRefs")) {
			ref("entRateLimit", "ratelimit.solo.io", "ratelimitconfigs", "RateLimitConfig", m(r))
		}
		ref("entExtAuth", "extauth.solo.io", "authconfigs", "AuthConfig", m(path(t, "entExtAuth", "authConfigRef")))
		ref("entWAF", "waf.solo.io", "wafpolicies", "WAFPolicy", m(path(t, "entWAF", "wafPolicyRef")))
		if !f.any() {
			f.ok("accepted and attached; targets exist")
		}
		c.Items = append(c.Items, f.item(x.e, p.o))
	}
	if len(c.Items) == 0 {
		xNA(c, "no agentgateway policies")
	}
}

// 26
func (x *xctx) agentRefs(c *Check) {
	if len(x.agents) == 0 {
		xNA(c, "no kagent agents")
		return
	}
	mcOK, _ := x.e.absenceOK("kagent.dev", "modelconfigs", "ModelConfig", true)
	for _, a := range x.agents {
		var f finding
		d := m(path(a.Obj, "spec", "declarative"))
		for where, v := range map[string]string{
			"modelConfig":        s(d["modelConfig"]),
			"memory.modelConfig": s(path(d, "memory", "modelConfig")),
			"context.compaction.summarizer.modelConfig": s(path(d, "context", "compaction", "summarizer", "modelConfig")),
		} {
			if v == "" || !mcOK {
				continue
			}
			ns, name := a.Namespace, v
			if i := strings.Index(v, "/"); i > 0 {
				ns, name = v[:i], v[i+1:]
			}
			if x.e.ws.Get(x.e.cluster, "kagent.dev", "modelconfigs", ns, name) == nil {
				f.fail(where + " " + ns + "/" + name + " does not exist")
			}
		}
		for _, t := range agentTools(a) {
			if ms := m(t["mcpServer"]); ms != nil {
				grp, res, kind, ns, name := mcpServerRef(ms, a.Namespace)
				if res == "" {
					continue
				}
				if ok, _ := x.e.absenceOK(grp, res, kind, true); !ok {
					continue
				}
				srv := x.e.ws.Get(x.e.cluster, grp, res, ns, name)
				if srv == nil {
					f.fail("tool server " + kind + " " + ns + "/" + name + " does not exist")
					continue
				}
				if kind == "RemoteMCPServer" {
					disc := map[string]bool{}
					for _, dt := range l(path(srv.Obj, "status", "discoveredTools")) {
						disc[s(m(dt)["name"])] = true
					}
					if len(disc) > 0 {
						var missing []string
						for _, n := range l(ms["toolNames"]) {
							if !disc[s(n)] {
								missing = append(missing, s(n))
							}
						}
						if len(missing) > 0 {
							f.fail(name + " does not offer " + strings.Join(missing, ", "))
						}
					}
				}
			}
			if ag := m(t["agent"]); ag != nil {
				ns := refNS(ag, a.Namespace)
				if ok, _ := x.e.absenceOK("kagent.dev", "agents", "Agent", true); ok && x.e.ws.Get(x.e.cluster, "kagent.dev", "agents", ns, s(ag["name"])) == nil {
					f.fail("agent tool " + ns + "/" + s(ag["name"]) + " does not exist")
				}
			}
		}
		for _, typ := range []string{"Accepted", "Ready"} {
			if st, reason, msg, found := condition(a, typ); found && st == "False" {
				f.fail(typ + " False: " + strings.TrimSpace(reason+" "+msg))
			}
		}
		if !f.any() {
			f.ok("references resolve; accepted and ready")
		}
		c.Items = append(c.Items, f.item(x.e, a))
	}
}

// 27
func (x *xctx) corsSafe(c *Check) {
	judge := func(o *store.Object, where string, cors map[string]any) {
		wild := false
		for _, og := range l(cors["allowOrigins"]) {
			if s(og) == "*" {
				wild = true
			}
		}
		switch {
		case wild && truthy(cors["allowCredentials"]):
			c.Items = append(c.Items, x.e.item(o, Fail, where+": allowCredentials with origin \"*\""))
		case wild:
			c.Items = append(c.Items, x.e.item(o, Warn, where+": any origin (\"*\")"))
		default:
			c.Items = append(c.Items, x.e.item(o, Pass, where+": named origins"))
		}
	}
	if x.polOK {
		for _, p := range x.md.policies {
			if cors := m(path(p.spec, "traffic", "cors")); cors != nil {
				judge(p.o, "traffic.cors", cors)
			}
		}
	}
	seen := map[*store.Object]bool{}
	for _, b := range append(append([]*backend{}, x.ai...), x.mcp...) {
		for _, r := range b.routes {
			if seen[r] {
				continue
			}
			seen[r] = true
			for _, rule := range l(path(r.Obj, "spec", "rules")) {
				for _, fl := range l(m(rule)["filters"]) {
					if s(m(fl)["type"]) == "CORS" {
						judge(r, "CORS filter", m(m(fl)["cors"]))
					}
				}
			}
		}
	}
	if len(c.Items) == 0 {
		if !x.polOK {
			xNA(c, x.polWhy)
			return
		}
		xNA(c, "no CORS configuration")
	}
}

// 28
func (x *xctx) timeouts(c *Check) {
	if !x.needPoliciesRoutes(c) {
		return
	}
	for _, b := range append(append([]*backend{}, x.exposedAI...), x.exposedMC...) {
		via := ""
		if path(b.spec, "policies", "http", "requestTimeout") != nil {
			via = "spec.policies.http.requestTimeout"
		}
		for _, p := range x.md.applicable(b) {
			if path(p.spec, "traffic", "timeouts", "request") != nil {
				via = "traffic.timeouts.request in " + p.o.Name
			}
			if path(p.spec, "backend", "http", "requestTimeout") != nil {
				via = "backend.http.requestTimeout in " + p.o.Name
			}
		}
		for _, r := range b.routes {
			for _, rule := range rulesTo(r, b) {
				if t := s(path(rule, "timeouts", "request")); t != "" {
					via = "HTTPRoute " + r.Name + " timeouts.request " + t
				}
			}
		}
		if via != "" {
			c.Items = append(c.Items, x.e.item(b.o, Pass, via))
		} else {
			c.Items = append(c.Items, x.e.item(b.o, Warn, "no request timeout on the route, policy or backend"))
		}
	}
	for _, r := range x.rms {
		if t := s(path(r.Obj, "spec", "timeout")); t != "" {
			c.Items = append(c.Items, x.e.item(r, Pass, "timeout "+t))
		} else {
			c.Items = append(c.Items, x.e.item(r, Warn, "no spec.timeout"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no exposed LLM or MCP backends and no RemoteMCPServers")
	}
}

// 29
func (x *xctx) retries(c *Check) {
	if !x.needPoliciesRoutes(c) {
		return
	}
	seenP := map[*store.Object]bool{}
	seenR := map[*store.Object]bool{}
	for _, b := range append(append([]*backend{}, x.exposedAI...), x.exposedMC...) {
		for _, p := range x.md.applicable(b) {
			rt := m(path(p.spec, "traffic", "retry"))
			if rt == nil || seenP[p.o] {
				continue
			}
			seenP[p.o] = true
			n := toNum(rt["attempts"])
			switch {
			case n > 1 && s(rt["precondition"]) == "":
				c.Items = append(c.Items, x.e.item(p.o, Warn, fmt.Sprintf("traffic.retry attempts %v with no precondition on %s", n, b.o.Name)))
			default:
				c.Items = append(c.Items, x.e.item(p.o, Pass, fmt.Sprintf("traffic.retry attempts %v, precondition %q", n, s(rt["precondition"]))))
			}
		}
		for _, r := range b.routes {
			for _, rule := range rulesTo(r, b) {
				rt := m(rule["retry"])
				if rt == nil || seenR[r] {
					continue
				}
				seenR[r] = true
				if n := toNum(rt["attempts"]); n > 1 {
					c.Items = append(c.Items, x.e.item(r, Warn, fmt.Sprintf("rule retry attempts %v to %s (no precondition possible at route level)", n, b.o.Name)))
				} else {
					c.Items = append(c.Items, x.e.item(r, Pass, fmt.Sprintf("rule retry attempts %v", n)))
				}
			}
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no retries on LLM or MCP routes")
	}
}

// 30
func (x *xctx) spend(c *Check) {
	if !x.needPoliciesRoutes(c) {
		return
	}
	if len(x.exposedAI) == 0 {
		xNA(c, "no LLM backend is exposed through an HTTPRoute")
		return
	}
	budOK, _ := x.e.evaluable("enterpriseagentgateway.solo.io", "enterpriseagentgatewaybudgets", "EnterpriseAgentgatewayBudget", true)
	budgets := x.e.list("enterpriseagentgateway.solo.io", "enterpriseagentgatewaybudgets")
	for _, b := range x.exposedAI {
		var f finding
		ps := x.md.applicable(b)
		enforced := false
		for _, p := range ps {
			t := m(p.spec["traffic"])
			if be := t["entBudgetEnforcement"]; be != nil {
				enforced = true
				allNS := s(path(be, "discovery", "namespaces", "from")) == "All"
				block, audit := false, false
				for _, bu := range budgets {
					if !allNS && bu.Namespace != p.o.Namespace {
						continue
					}
					for _, en := range l(path(bu.Obj, "spec", "budgets")) {
						switch s(m(en)["onBudgetExceeded"]) {
						case "Block":
							block = true
						case "Audit":
							audit = true
						}
					}
				}
				switch {
				case block:
					f.ok("budget enforcement (" + p.o.Name + ") with a Block budget")
				case audit:
					f.warn("budget enforcement (" + p.o.Name + ") but every budget is Audit only")
				case budOK:
					f.warn("entBudgetEnforcement in " + p.o.Name + " but no EnterpriseAgentgatewayBudget in scope")
				}
			}
			rl := t["rateLimit"]
			tokens := len(findKey(rl, "tokens")) > 0
			for _, u := range findKey(rl, "unit") {
				if s(u) == "Tokens" {
					tokens = true
				}
			}
			if tokens {
				f.ok("token rate limit in " + p.o.Name)
			}
		}
		if !enforced && len(budgets) > 0 && f.st != Pass {
			f.warn(fmt.Sprintf("%d EnterpriseAgentgatewayBudget(s) exist but no entBudgetEnforcement applies", len(budgets)))
		}
		if !f.any() {
			f.fail("no budget or token rate limit (" + policyNames(ps) + ")")
		}
		c.Items = append(c.Items, f.item(x.e, b.o))
	}
}

// 31
func (x *xctx) rateLimitKey(c *Check) {
	if !x.needPolicies(c) {
		return
	}
	for _, p := range x.md.policies {
		var descs []any
		for _, g := range findKey(path(p.spec, "traffic", "rateLimit"), "global") {
			descs = append(descs, l(m(g)["descriptors"])...)
		}
		if len(descs) == 0 {
			continue
		}
		trusted := map[string]bool{}
		for _, q := range x.md.siblings(p) {
			for _, st := range findKey(path(q.spec, "traffic", "transformation"), "set") {
				for _, h := range l(st) {
					if v := s(m(h)["value"]); strings.Contains(v, "jwt.") || strings.Contains(v, "apiKey.") {
						trusted[strings.ToLower(s(m(h)["name"]))] = true
					}
				}
			}
		}
		var f finding
		for _, d := range descs {
			for _, en := range l(m(d)["entries"]) {
				for _, hm := range headerRead.FindAllStringSubmatch(s(m(en)["expression"]), -1) {
					if trusted[strings.ToLower(hm[1])] {
						f.ok("header " + hm[1] + " is set from a verified identity")
					} else {
						f.warn("descriptor " + s(m(en)["name"]) + " is keyed on client header " + hm[1])
					}
				}
			}
		}
		if !f.any() {
			f.ok("descriptors do not read client headers")
		}
		c.Items = append(c.Items, f.item(x.e, p.o))
	}
	if len(c.Items) == 0 {
		xNA(c, "no global rate limit descriptors")
	}
}

// 32
func imageFloating(img string) bool {
	if img == "" || strings.Contains(img, "@sha256:") {
		return false
	}
	last := img[strings.LastIndex(img, "/")+1:]
	return !strings.Contains(last, ":") || strings.HasSuffix(last, ":latest")
}

func (x *xctx) pinned(c *Check) {
	for _, a := range x.agents {
		var f finding
		sk := m(path(a.Obj, "spec", "skills"))
		for _, r := range l(sk["refs"]) {
			if !strings.Contains(s(r), "@sha256:") {
				f.warn("skill " + s(r) + " is not pinned by digest")
			} else {
				f.ok("skills pinned")
			}
		}
		for _, g := range l(sk["gitRefs"]) {
			if !sha40.MatchString(s(m(g)["ref"])) {
				f.warn("git skill " + s(m(g)["url"]) + " ref " + strings.TrimSpace(s(m(g)["ref"])+" is not a commit SHA"))
			} else {
				f.ok("git skills pinned")
			}
		}
		if img := s(path(a.Obj, "spec", "byo", "deployment", "image")); img != "" {
			if imageFloating(img) {
				f.warn("image " + img + " is :latest or untagged")
			} else {
				f.ok("image " + img)
			}
		}
		if f.any() {
			c.Items = append(c.Items, f.item(x.e, a))
		}
	}
	for _, o := range x.mcpservers {
		var f finding
		dep := m(path(o.Obj, "spec", "deployment"))
		if img := s(dep["image"]); imageFloating(img) {
			f.warn("image " + img + " is :latest or untagged")
		}
		switch cmd := s(dep["cmd"]); cmd {
		case "npx", "uvx", "pipx", "bunx":
			versioned := false
			for _, a := range l(dep["args"]) {
				if as := s(a); !strings.HasPrefix(as, "-") && versionedArg.MatchString(strings.TrimPrefix(as, "@")) {
					versioned = true
				}
			}
			if !versioned {
				f.warn(cmd + " " + strings.Trim(fmt.Sprint(l(dep["args"])), "[]") + " runs whatever version is newest")
			}
		}
		if !f.any() {
			f.ok("image and package pinned")
		}
		c.Items = append(c.Items, f.item(x.e, o))
	}
	if len(c.Items) == 0 {
		xNA(c, "no agent skills, BYO images or MCPServers")
	}
}

// 33
func (x *xctx) registry(c *Check) {
	deps := x.e.list("apps", "deployments")
	nss := map[string]bool{}
	for _, d := range deps {
		lb := fmt.Sprint(podTemplateLabels(d)) + fmt.Sprint(d.Labels())
		if strings.Contains(d.Name, "agentregistry") || strings.Contains(lb, "agentregistry") {
			nss[d.Namespace] = true
		}
	}
	if len(nss) == 0 {
		xNA(c, "no agentregistry Deployments")
		return
	}
	nsl, nsKnown := x.e.nsLabels()
	for _, ns := range xSortedKeys(nss) {
		for _, w := range append(x.e.ws.List(x.e.cluster, "apps", "deployments", ns, nil), x.e.ws.List(x.e.cluster, "apps", "statefulsets", ns, nil)...) {
			if strings.Contains(w.Name, "postgres") {
				c.Items = append(c.Items, x.e.item(w, Warn, "bundled PostgreSQL in the registry namespace; use a managed database with backups and TLS"))
			}
		}
		for _, d := range x.e.ws.List(x.e.cluster, "apps", "deployments", ns, nil) {
			cfg := map[string]string{}
			server, enterprise := false, false
			for _, ct := range containers(podSpec(d)) {
				if strings.Contains(s(ct.c["image"]), "agentregistry") {
					server = true
					enterprise = enterprise || strings.Contains(s(ct.c["image"]), "enterprise")
				}
				for _, ef := range l(ct.c["envFrom"]) {
					if n := s(path(ef, "configMapRef", "name")); n != "" {
						if cm := x.e.ws.Get(x.e.cluster, "", "configmaps", ns, n); cm != nil {
							for k, v := range m(cm.Obj["data"]) {
								cfg[k] = s(v)
							}
						}
					}
				}
				for _, ev := range l(ct.c["env"]) {
					em := m(ev)
					if hasKey(em, "value") {
						cfg[s(em["name"])] = s(em["value"])
					}
				}
			}
			if !server {
				continue
			}
			var f finding
			if u := cfg["AGENT_REGISTRY_DATABASE_URL"]; strings.Contains(u, "sslmode=disable") {
				if nsKnown && nsl[ns]["istio.io/dataplane-mode"] == "ambient" {
					f.ok("database sslmode=disable, but the namespace is in ambient (mTLS)")
				} else {
					f.warn("AGENT_REGISTRY_DATABASE_URL has sslmode=disable")
				}
			}
			if cfg["MCP_BRIDGE_URL"] != "" && cfg["MCP_BRIDGE_AUDIENCE"] == "" {
				f.warn("MCP_BRIDGE_URL set without MCP_BRIDGE_AUDIENCE: bridge tokens are not audience-checked")
			}
			if enterprise {
				if v := cfg["AUDIT_ENABLED"]; v != "true" {
					f.warn("AUDIT_ENABLED is " + map[bool]string{true: "unset", false: v}[v == ""] + ": no control-plane audit log")
				}
				if cfg["REQUIRE_CREATE_APPROVAL"] != "true" {
					f.warn("REQUIRE_CREATE_APPROVAL is not true: new artifacts go straight to the catalogue")
				}
			}
			if !f.any() {
				f.ok("audit, approval and database settings hardened")
			}
			c.Items = append(c.Items, f.item(x.e, d))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no agentregistry server Deployment found")
	}
}

// 34
func (x *xctx) transport(c *Check) {
	for _, b := range x.mcp {
		var f finding
		for _, t := range l(b.mcp["targets"]) {
			if s(path(t, "static", "protocol")) == "SSE" {
				f.warn("target " + s(m(t)["name"]) + " uses SSE")
			}
		}
		if len(l(b.mcp["targets"])) == 0 {
			continue
		}
		if !f.any() {
			f.ok("Streamable HTTP")
		}
		c.Items = append(c.Items, f.item(x.e, b.o))
	}
	for _, r := range x.rms {
		if s(path(r.Obj, "spec", "protocol")) == "SSE" {
			c.Items = append(c.Items, x.e.item(r, Warn, "protocol SSE"))
		} else {
			c.Items = append(c.Items, x.e.item(r, Pass, "Streamable HTTP"))
		}
	}
	if len(c.Items) == 0 {
		xNA(c, "no MCP backends or RemoteMCPServers")
	}
}
