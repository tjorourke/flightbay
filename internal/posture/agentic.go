package posture

import (
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/solodocs"
	"github.com/tjorourke/flightbay/internal/store"
)

// spec is a check's definition. source is the Solo field guide (a lab on
// mastertheagent.com or masterthemesh.com); docs is the product documentation
// page for the feature the check is about, on docs.solo.io where one exists
// (see docs.go). scripts/scorecard-checks reads these literals.
type spec struct {
	id, category, severity, title, why, fix, source, docs string
}

func (sp spec) check(area string) Check {
	return Check{ID: sp.id, Area: area, Category: sp.category, Severity: sp.severity, Title: sp.title, Why: sp.why, Fix: sp.fix, Source: sp.source, Docs: sp.docs}
}

// Guide links: agentic pages live on mastertheagent.com, mesh pages on
// masterthemesh.com (the same catalogue, served under two brands).
const (
	kbBase   = "https://mastertheagent.com/solo/"
	meshBase = "https://masterthemesh.com/solo/"
)

func agenticChecks(e env) []Check {
	md := newModel(e)
	var out []Check
	add := func(sp spec, eval func(*Check)) {
		c := sp.check("agentic")
		eval(&c)
		out = append(out, c)
	}
	polOK, polWhy := e.evaluable("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true)
	epolOK, epolWhy := e.evaluable("enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy", true)
	if !epolOK {
		polWhy = strings.TrimSpace(polWhy + " " + epolWhy)
	}
	routesOK, routesWhy := e.evaluable("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true)
	// Both policy kinds must be readable (a kind whose CRD is not installed
	// counts as readable): a policy we cannot see might be the one that applies.
	policiesKnown := polOK && epolOK
	na := func(c *Check, why string) { c.Status, c.Reason = NA, why }
	var mcp, ai, exposedMCP, exposedAI []*backend
	for _, b := range md.backends {
		if b.mcp != nil {
			mcp = append(mcp, b)
			if len(b.routes) > 0 {
				exposedMCP = append(exposedMCP, b)
			}
		}
		if b.ai != nil {
			ai = append(ai, b)
			if len(b.routes) > 0 {
				exposedAI = append(exposedAI, b)
			}
		}
	}

	authCheck := func(c *Check, bs []*backend, what string) {
		if !policiesKnown || !routesOK {
			na(c, strings.TrimSpace(polWhy+" "+routesWhy))
			return
		}
		if len(bs) == 0 {
			na(c, "no "+what+" backend is exposed through an HTTPRoute")
			return
		}
		for _, b := range bs {
			ps := md.applicable(b)
			authn, meshOnly := hasAuthn(ps)
			denyAll := false
			for _, p := range ps {
				a := m(path(p.spec, "backend", "mcp", "authorization"))
				if a != nil && s(a["action"]) != "Deny" && strings.TrimSpace(strings.Trim(fmt.Sprint(path(a, "policy", "matchExpressions")), "[]")) == "false" {
					denyAll = true
				}
			}
			switch {
			case authn:
				c.Items = append(c.Items, e.item(b.o, Pass, "authenticated by "+policyNames(ps)))
			case meshOnly && md.eastWest(b):
				c.Items = append(c.Items, e.item(b.o, Pass, "in-mesh (waypoint) route; callers authorised by SPIFFE identity in "+policyNames(ps)))
			case meshOnly:
				c.Items = append(c.Items, e.item(b.o, Warn, "only mesh identity (source.identity) is checked, not the calling user; exposed via "+md.exposure(b)))
			case denyAll:
				c.Items = append(c.Items, e.item(b.o, Warn, "no authentication, but every tool is denied (matchExpressions: [\"false\"]); exposed via "+md.exposure(b)))
			default:
				c.Items = append(c.Items, e.item(b.o, Fail, "OPEN: exposed via "+md.exposure(b)+" with no JWT or external auth ("+policyNames(ps)+")"))
			}
		}
	}

	add(spec{"agentic.mcp-auth-required", "Authentication", "critical", "MCP endpoints require authentication",
		"A stock MCP server's only control is the token it holds upstream. Without JWT validation at the gateway, anyone who reaches the listener gets every tool under one shared identity.",
		"apiVersion: agentgateway.dev/v1alpha1   # or enterpriseagentgateway.solo.io/v1alpha1 EnterpriseAgentgatewayPolicy\nkind: AgentgatewayPolicy\nmetadata: {name: mcp-jwt, namespace: <route namespace>}\nspec:\n  targetRefs:\n  - {group: gateway.networking.k8s.io, kind: HTTPRoute, name: <mcp route>}\n  traffic:\n    jwtAuthentication:\n      mode: Strict\n      providers:\n      - issuer: https://idp.example.com/realms/solo\n        audiences: [mcp]\n        jwks: {remote: {jwksPath: /protocol/openid-connect/certs, backendRef: {name: keycloak, namespace: auth, kind: Service, port: 8080}}}",
		kbBase + "agentgateway-mcp-controls/", solodocs.Agentgateway + "mcp/auth/setup/"}, func(c *Check) { authCheck(c, exposedMCP, "MCP") })

	add(spec{"agentic.llm-auth-required", "Authentication", "critical", "LLM routes require caller authentication",
		"The gateway holds the provider key, so an open LLM route lets anyone spend it and skip every per-user policy.",
		"spec:\n  targetRefs:\n  - {group: gateway.networking.k8s.io, kind: HTTPRoute, name: <llm route>}\n  traffic:\n    jwtAuthentication:\n      mode: Strict\n      providers:\n      - {issuer: <issuer>, audiences: [<audience>], jwks: {remote: {...}}}",
		kbBase + "agentgateway-mcp-controls/", solodocs.Agentgateway + "security/jwt/setup/"}, func(c *Check) { authCheck(c, exposedAI, "LLM") })

	add(spec{"agentic.jwt-strict-mode", "Authentication", "high", "JWT validation runs in Strict mode",
		"Anything but Strict lets requests with no token through, and Strict fails closed when the JWKS cannot be fetched.",
		"traffic:\n  jwtAuthentication:\n    mode: Strict", kbBase + "securing-mcp-agentic-systems/", solodocs.Agentgateway + "security/jwt/about/"}, func(c *Check) {
		if !policiesKnown {
			na(c, polWhy)
			return
		}
		for _, p := range md.policies {
			j := m(path(p.spec, "traffic", "jwtAuthentication"))
			if j == nil {
				continue
			}
			if s(j["mode"]) == "Strict" {
				c.Items = append(c.Items, e.item(p.o, Pass, "mode: Strict"))
			} else {
				c.Items = append(c.Items, e.item(p.o, Fail, fmt.Sprintf("mode: %q", s(j["mode"]))))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no JWT authentication is configured")
		}
	})

	add(spec{"agentic.jwt-issuer-audience-pinned", "Authentication", "high", "JWT providers pin the issuer and audience",
		"Without an audience, a token minted for another service by the same issuer can be replayed here (confused deputy). Inline JWKS never rotate with the IdP.",
		"providers:\n- issuer: https://idp.example.com/realms/solo\n  audiences: [github-mcp]\n  jwks: {remote: {jwksPath: /protocol/openid-connect/certs, backendRef: {...}}}", kbBase + "securing-mcp-agentic-systems/", solodocs.Agentgateway + "security/jwt/setup/"}, func(c *Check) {
		if !policiesKnown {
			na(c, polWhy)
			return
		}
		for _, p := range md.policies {
			for _, pr := range l(path(p.spec, "traffic", "jwtAuthentication", "providers")) {
				pm := m(pr)
				switch {
				case s(pm["issuer"]) == "" || len(l(pm["audiences"])) == 0:
					c.Items = append(c.Items, e.item(p.o, Fail, fmt.Sprintf("issuer %q, audiences %v", s(pm["issuer"]), l(pm["audiences"]))))
				case path(pm, "jwks", "inline") != nil:
					c.Items = append(c.Items, e.item(p.o, Warn, "inline JWKS: keys will not rotate with the IdP"))
				default:
					c.Items = append(c.Items, e.item(p.o, Pass, "issuer "+s(pm["issuer"])))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no JWT providers are configured")
		}
	})

	add(spec{"agentic.mcp-tool-authorization", "Authorization", "high", "MCP backends have a tool allowlist (rug-pull defence)",
		"Being allowed to reach an MCP server should not mean every tool on it. An allowlist hides unlisted tools from tools/list and rejects them on tools/call, so a compromised upstream cannot add tools callers will use.",
		"spec:\n  targetRefs:\n  - {group: agentgateway.dev, kind: AgentgatewayBackend, name: <mcp backend>}\n  backend:\n    mcp:\n      authorization:\n        action: Allow\n        policy:\n          matchExpressions:\n          - 'jwt.groups.exists(g, g == \"developers\") && mcp.tool.name in [\"get_pull_request\", \"search_repositories\"]'",
		kbBase + "agentgw-agentic-mcp/", solodocs.Agentgateway + "mcp/tool-access/"}, func(c *Check) {
		if !policiesKnown {
			na(c, polWhy)
			return
		}
		if len(mcp) == 0 {
			na(c, "no MCP backends")
			return
		}
		for _, b := range mcp {
			st, detail := Fail, "no backend.mcp.authorization applies"
			for _, p := range md.applicable(b) {
				a := m(path(p.spec, "backend", "mcp", "authorization"))
				if a == nil {
					continue
				}
				exprs := fmt.Sprint(path(a, "policy", "matchExpressions"))
				switch {
				case s(a["action"]) != "Deny" && (strings.Contains(exprs, "mcp.tool.name") || strings.TrimSpace(strings.Trim(exprs, "[]")) == "false"):
					st, detail = Pass, "allowlist in "+p.o.Namespace+"/"+p.o.Name
				case s(a["action"]) == "Deny" && st != Pass:
					st, detail = Warn, "only a denylist ("+p.o.Name+"): new tools are allowed by default"
				case st == Fail:
					st, detail = Warn, "authorization in "+p.o.Name+" does not name tools (mcp.tool.name)"
				}
			}
			c.Items = append(c.Items, e.item(b.o, st, detail))
		}
	})

	add(spec{"agentic.authz-no-broad-rule", "Authorization", "high", "No authorization rule allows everything",
		"agentgateway combines matchExpressions with OR. A permissive expression can allow requests that narrower expressions would reject.",
		"# write each rule to stand alone: subject AND explicit tools\n- 'jwt.sub == \"alice\" && mcp.tool.name == \"echo\"'", kbBase + "zero-trust-agents-kubernetes/", solodocs.Agentgateway + "mcp/tool-access/"}, func(c *Check) {
		if !policiesKnown {
			na(c, polWhy)
			return
		}
		for _, p := range md.policies {
			for _, where := range [][]string{{"backend", "mcp", "authorization"}, {"traffic", "authorization"}} {
				a := m(path(p.spec, where...))
				if a == nil || s(a["action"]) == "Deny" {
					continue
				}
				bad := ""
				for _, x := range l(path(a, "policy", "matchExpressions")) {
					ex := strings.Join(strings.Fields(s(x)), " ")
					if ex == "true" || ex == "has(jwt.sub)" || ex == `jwt.sub != ""` {
						bad = ex
					}
				}
				if bad != "" {
					c.Items = append(c.Items, e.item(p.o, Fail, fmt.Sprintf("%s allows everything: %q", strings.Join(where, "."), bad)))
				} else {
					c.Items = append(c.Items, e.item(p.o, Pass, strings.Join(where, ".")))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no authorization rules")
		}
	})

	add(spec{"agentic.rate-limit", "Resilience", "high", "LLM and MCP routes are rate limited per caller",
		"Rate and concurrency ceilings contain a runaway agent loop or swarm, and give you the volume signal to alert on.",
		"traffic:\n  entRateLimit:\n    global:\n      rateLimitConfigRefs: [{name: per-user, namespace: agentgateway-system}]\n      backendRef: {name: rate-limiter-enterprise-agentgateway, namespace: agentgateway-system, port: 8081}",
		kbBase + "agentgateway-mcp-controls/", solodocs.Agentgateway + "llm/rate-limit/"}, func(c *Check) {
		if !policiesKnown || !routesOK {
			na(c, strings.TrimSpace(polWhy+" "+routesWhy))
			return
		}
		bs := append(append([]*backend{}, exposedAI...), exposedMCP...)
		if len(bs) == 0 {
			na(c, "no LLM or MCP backend is exposed through an HTTPRoute")
			return
		}
		for _, b := range bs {
			st, detail := Fail, "no rate limit applies ("+policyNames(md.applicable(b))+")"
			for _, p := range md.applicable(b) {
				t := m(p.spec["traffic"])
				if t["entRateLimit"] != nil || len(l(path(t, "rateLimit", "local"))) > 0 || path(t, "rateLimit", "global") != nil || len(l(path(t, "rateLimit", "conditional"))) > 0 {
					st, detail = Pass, "rate limited by "+p.o.Name
				}
			}
			c.Items = append(c.Items, e.item(b.o, st, detail))
		}
	})

	guard := func(c *Check, dir string) {
		if len(ai) == 0 {
			na(c, "no LLM (AI) backends")
			return
		}
		for _, b := range ai {
			st, detail := Fail, "no promptGuard."+dir
			if len(l(path(b.spec, "policies", "ai", "promptGuard", dir))) > 0 {
				st, detail = Pass, "inline on the backend"
			}
			for _, g := range l(path(b.ai, "groups")) {
				for _, pr := range l(m(g)["providers"]) {
					if len(l(path(pr, "policies", "ai", "promptGuard", dir))) > 0 {
						st, detail = Pass, "on provider "+s(m(pr)["name"])
					}
				}
			}
			if dir == "request" && path(b.ai, "provider", "bedrock", "guardrail") != nil {
				st, detail = Pass, "Bedrock guardrail"
			}
			for _, p := range md.applicable(b) {
				for _, key := range []string{"promptGuard", "entPromptGuard"} {
					if len(l(path(p.spec, "backend", "ai", key, dir))) > 0 {
						st, detail = Pass, key+"."+dir+" in "+p.o.Name
					}
				}
			}
			c.Items = append(c.Items, e.item(b.o, st, detail))
		}
	}
	add(spec{"agentic.prompt-guard-request", "Guardrails", "high", "LLM requests pass through a prompt guard",
		"Prompt-injection and data-leak defences belong in the data plane, not the system prompt.",
		"backend:\n  ai:\n    promptGuard:\n      request:\n      - regex: {action: Reject, builtins: [Ssn, CreditCard, Email]}\n        response: {statusCode: 403}",
		kbBase + "agentgateway-mcp-controls/", solodocs.Agentgateway + "llm/guardrails/overview/"}, func(c *Check) { guard(c, "request") })
	add(spec{"agentic.prompt-guard-response", "Guardrails", "medium", "LLM responses are guarded (output gating)",
		"Model output flows back into the agent's context; mask PII and strip injection strings on the way back.",
		"backend:\n  ai:\n    promptGuard:\n      response:\n      - regex: {action: Mask, builtins: [Ssn, CreditCard]}", kbBase + "agentgateway-mcp-controls/", solodocs.Agentgateway + "llm/guardrails/regex/"}, func(c *Check) { guard(c, "response") })

	add(spec{"agentic.backend-tls-upstream", "Encryption", "high", "External MCP upstreams use TLS",
		"The gateway-to-upstream hop carries the upstream credential; HTTPS on 443 needs TLS and SNI.",
		"targets:\n- name: github\n  static:\n    host: api.githubcopilot.com\n    port: 443\n    policies:\n      tls: {sni: api.githubcopilot.com}", kbBase + "agentgateway-mcp-controls/", solodocs.Agentgateway + "security/backendtls/"}, func(c *Check) {
		n := 0
		for _, b := range mcp {
			for _, t := range mcpTargets(b) {
				host := s(t["host"])
				port := fmt.Sprint(t["port"])
				if clusterLocal(host) && port != "443" {
					continue
				}
				n++
				if path(t, "policies", "tls") != nil || path(b.spec, "policies", "tls") != nil {
					c.Items = append(c.Items, e.item(b.o, Pass, host+":"+port+" with TLS"))
				} else {
					c.Items = append(c.Items, e.item(b.o, Fail, host+":"+port+" without a tls policy"))
				}
			}
		}
		if n == 0 {
			na(c, "no external MCP upstreams")
		}
	})

	add(spec{"agentic.provider-secret-ref", "Identity", "high", "Upstream credentials come from existing Secrets",
		"The gateway, not the client, should hold provider keys, as Secrets that rotate without touching manifests. A dangling reference fails at request time.",
		"spec:\n  policies:\n    auth:\n      secretRef: {name: openai-secret}", kbBase + "securing-mcp-agentic-systems/", solodocs.Agentgateway + "llm/api-keys/"}, func(c *Check) {
		secOK, secWhy := e.evaluable("", "secrets", "Secret", true)
		check := func(b *backend, ref map[string]any, what string) {
			name := s(m(ref["secretRef"])["name"])
			refKind := s(m(ref["secretRef"])["kind"])
			var federated string
			for _, k := range []string{"aws", "azure", "gcp", "oauthTokenExchange", "workloadIdentity", "entTokenExchange"} {
				if ref[k] != nil {
					federated = k
				}
			}
			switch {
			case federated != "":
				c.Items = append(c.Items, e.item(b.o, Pass, what+": "+federated+" (no static key to leak)"))
			case name != "" && refKind != "" && refKind != "Secret":
				c.Items = append(c.Items, e.item(b.o, Pass, what+": "+refKind+" "+name+" (existence not checked)"))
			case name == "" && ref["passthrough"] != nil:
				c.Items = append(c.Items, e.item(b.o, Warn, what+": the client's token is passed through (see agentic.no-token-passthrough)"))
			case name == "":
				c.Items = append(c.Items, e.item(b.o, Fail, what+": no auth.secretRef"))
			case !secOK:
				c.Items = append(c.Items, e.item(b.o, Pass, what+": secretRef "+name+" (existence not checked: "+secWhy+")"))
			case e.ws.Get(e.cluster, "", "secrets", b.o.Namespace, name) == nil:
				c.Items = append(c.Items, e.item(b.o, Fail, what+": Secret "+b.o.Namespace+"/"+name+" does not exist"))
			default:
				c.Items = append(c.Items, e.item(b.o, Pass, what+": Secret "+name))
			}
		}
		for _, b := range ai {
			check(b, m(path(b.spec, "policies", "auth")), "provider key")
		}
		for _, b := range mcp {
			for _, t := range mcpTargets(b) {
				if a := m(path(t, "policies", "auth")); a != nil {
					check(b, a, "target "+s(t["host"]))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no backends with upstream credentials")
		}
	})

	add(spec{"agentic.no-literal-credentials", "Identity", "critical", "No literal credentials in routes or agent env",
		"A literal token in an HTTPRoute or environment value is exposed to readers of the object. Use a Secret reference and configure credential rotation.",
		"env:\n- name: MODEL_API_KEY\n  valueFrom: {secretKeyRef: {name: model-key, key: api-key}}", kbBase + "zero-trust-agents-kubernetes/", solodocs.Agentgateway + "security/backend-authn/key/"}, func(c *Check) {
		for _, r := range md.routes {
			for _, rule := range l(path(r.Obj, "spec", "rules")) {
				for _, f := range l(m(rule)["filters"]) {
					for _, op := range []string{"set", "add"} {
						for _, h := range l(path(f, "requestHeaderModifier", op)) {
							hm := m(h)
							name := strings.ToLower(s(hm["name"]))
							if name == "authorization" || name == "x-api-key" {
								c.Items = append(c.Items, e.item(r, Fail, "literal "+s(hm["name"])+" header in a route filter"))
							}
						}
					}
				}
			}
		}
		envCheck := func(o *store.Object, env []any, where string) {
			for _, ev := range env {
				em := m(ev)
				if sensitiveEnv.MatchString(s(em["name"])) && !settingEnv.MatchString(s(em["name"])) && em["value"] != nil && s(em["value"]) != "" {
					c.Items = append(c.Items, e.item(o, Fail, fmt.Sprintf("%s env %s has a literal value (redacted in the capture); use valueFrom.secretKeyRef", where, s(em["name"]))))
				}
			}
		}
		for _, a := range e.list("kagent.dev", "agents") {
			envCheck(a, l(path(a.Obj, "spec", "byo", "deployment", "env")), "agent")
			envCheck(a, l(path(a.Obj, "spec", "declarative", "deployment", "env")), "agent")
		}
		agentNS := map[string]bool{}
		for _, a := range e.list("kagent.dev", "agents") {
			agentNS[a.Namespace] = true
		}
		agentNames := map[string]bool{}
		for _, a := range e.list("kagent.dev", "agents") {
			agentNames[a.Namespace+"/"+a.Name] = true
		}
		for _, d := range e.list("apps", "deployments") {
			if !agentNS[d.Namespace] || agentNames[d.Namespace+"/"+d.Name] {
				continue // generated from an Agent, already reported there
			}
			for _, ct := range l(path(d.Obj, "spec", "template", "spec", "containers")) {
				envCheck(d, l(m(ct)["env"]), "container "+s(m(ct)["name"]))
			}
		}
		if len(c.Items) == 0 && (len(md.routes) > 0 || len(agentNS) > 0) {
			c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Pass, Detail: "no literal credentials found in routes or agent workloads"})
		}
		if len(c.Items) == 0 {
			na(c, "no routes or agents")
		}
	})

	agents := e.list("kagent.dev", "agents")
	agentDeploy := func(a *store.Object) *store.Object {
		for _, d := range e.ws.List(e.cluster, "apps", "deployments", a.Namespace, nil) {
			lb := m(path(d.Obj, "spec", "template", "metadata", "labels"))
			if d.Name == a.Name || s(lb["app.kubernetes.io/name"]) == a.Name || s(lb["kagent"]) == a.Name {
				return d
			}
		}
		return nil
	}
	add(spec{"agentic.kagent-dedicated-serviceaccount", "Identity", "high", "Each agent runs under its own service account",
		"Mesh identity comes from the service account. A shared or default one means agents cannot be told apart in authorization policy: the shared-identity failure mode behind recent agent incidents.",
		"# kagent's default is a service account named after the agent; policy can then say:\ntraffic:\n  authorization:\n    action: Allow\n    policy:\n      matchExpressions: ['source.identity.serviceAccount == \"sre-agent\"']",
		kbBase + "zero-trust-agents-kubernetes/", solodocs.Kagent + "security/access-policies/l4/"}, func(c *Check) {
		if len(agents) == 0 {
			na(c, "no kagent agents")
			return
		}
		use := map[string][]string{}
		sa := map[*store.Object]string{}
		for _, a := range agents {
			// The Agent's own serviceAccountName wins; otherwise read the
			// generated Deployment.
			n := s(path(a.Obj, "spec", "declarative", "deployment", "serviceAccountName"))
			if n == "" {
				n = s(path(a.Obj, "spec", "byo", "deployment", "serviceAccountName"))
			}
			if n == "" {
				d := agentDeploy(a)
				if d == nil {
					why := "agent deployment not captured"
					if e.declaredOnly() {
						why = DeclaredWhy + ": the kagent controller creates the agent Deployment and its service account"
					}
					c.Items = append(c.Items, e.item(a, NA, why))
					continue
				}
				n = s(path(d.Obj, "spec", "template", "spec", "serviceAccountName"))
			}
			sa[a] = n
			use[a.Namespace+"/"+n] = append(use[a.Namespace+"/"+n], a.Name)
		}
		for a, n := range sa {
			switch {
			case n == "" || n == "default":
				c.Items = append(c.Items, e.item(a, Fail, "runs as the default service account"))
			case len(use[a.Namespace+"/"+n]) > 1:
				c.Items = append(c.Items, e.item(a, Fail, "shares service account "+n+" with "+strings.Join(use[a.Namespace+"/"+n], ", ")))
			default:
				c.Items = append(c.Items, e.item(a, Pass, "service account "+n))
			}
		}
	})

	add(spec{"agentic.kagent-tool-scope", "Guardrails", "medium", "Agents name their tools and gate high-impact ones",
		"An empty tool list hands the agent every tool the server advertises; irreversible tools need a human approval step.",
		"tools:\n- type: McpServer\n  mcpServer:\n    name: k8s-tool-server\n    kind: RemoteMCPServer\n    toolNames: [k8s_get_resources, k8s_delete_resource]\n    requireApproval: [k8s_delete_resource]",
		kbBase + "securing-mcp-agentic-systems/", solodocs.Kagent + "agents/human-in-the-loop/"}, func(c *Check) {
		if len(agents) == 0 {
			na(c, "no kagent agents")
			return
		}
		risky := strings.NewReplacer().Replace("delete|apply|create|merge|write|update|patch|exec|scale|remove|terminate")
		for _, a := range agents {
			if byo := m(path(a.Obj, "spec", "byo")); byo != nil {
				if strings.Contains(fmt.Sprint(path(byo, "deployment", "env")), "ALLOWED_TOOLS") {
					c.Items = append(c.Items, e.item(a, Pass, "BYO agent with an ALLOWED_TOOLS list"))
				} else {
					c.Items = append(c.Items, e.item(a, Warn, "BYO agent: tool scope is not visible in the spec"))
				}
				continue
			}
			tools := l(path(a.Obj, "spec", "declarative", "tools"))
			st, detail := Pass, "tools scoped"
			for _, t := range tools {
				ms := m(m(t)["mcpServer"])
				if ms == nil {
					continue
				}
				names := l(ms["toolNames"])
				if len(names) == 0 {
					st, detail = Fail, "MCP server "+s(ms["name"])+" with no toolNames (every tool)"
					break
				}
				approve := fmt.Sprint(ms["requireApproval"])
				for _, n := range names {
					if matchesAny(s(n), risky) && !strings.Contains(approve, s(n)) {
						st, detail = Warn, "high-impact tool "+s(n)+" without requireApproval"
					}
				}
			}
			c.Items = append(c.Items, e.item(a, st, detail))
		}
	})

	nsl, nsKnown := e.nsLabels()
	add(spec{"agentic.agents-in-ambient", "Encryption", "high", "Agents, MCP servers and gateways are in the ambient mesh",
		"Ambient gives every pod a SPIFFE identity and mTLS: the base for identity-based policy between agent, gateway and MCP server.",
		"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: <namespace>\n  labels: {istio.io/dataplane-mode: ambient}", kbBase + "agentgw-agentic-mcp/", solodocs.Istio + "agentic-mesh/about/"}, func(c *Check) {
		if !nsKnown {
			na(c, e.noNSWhy())
			return
		}
		seen := map[string]string{}
		for _, a := range agents {
			seen[a.Namespace] = "agents"
		}
		for _, gw := range md.agentgatewayGateways() {
			seen[gw.Namespace] = "agentgateway"
		}
		for _, b := range mcp {
			for _, t := range mcpTargets(b) {
				if h := s(t["host"]); clusterLocal(h) {
					if parts := strings.Split(h, "."); len(parts) > 1 {
						seen[parts[1]] = "MCP servers"
					}
				}
			}
		}
		for ns, what := range seen {
			o := &store.Object{Resource: "namespaces", Kind: "Namespace", Name: ns}
			if nsl[ns]["istio.io/dataplane-mode"] == "ambient" {
				c.Items = append(c.Items, e.item(o, Pass, what+" namespace enrolled"))
			} else if _, ok := nsl[ns]; ok {
				c.Items = append(c.Items, e.item(o, Fail, what+" namespace not enrolled in ambient"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no agent, gateway or MCP namespaces")
		}
	})

	add(spec{"agentic.mcp-server-not-directly-reachable", "Isolation", "high", "MCP servers are only reachable through the gateway",
		"Tool policy only holds if the gateway is the only path; a direct path skips authorization, audit and rate limits.",
		"apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: mcp-only-from-gateway, namespace: <mcp namespace>}\nspec:\n  selector: {matchLabels: {app: <mcp server>}}\n  action: ALLOW\n  rules:\n  - from:\n    - source: {principals: [\"cluster.local/ns/agentgateway-system/sa/<gateway sa>\"]}",
		kbBase + "securing-mcp-agentic-systems/", solodocs.Istio + "agentic-mesh/policies/"}, func(c *Check) {
		apOK, _ := e.evaluable("security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true)
		n := 0
		for _, b := range mcp {
			for _, t := range mcpTargets(b) {
				h := s(t["host"])
				if !clusterLocal(h) {
					continue
				}
				parts := strings.Split(h, ".")
				ns := b.o.Namespace
				if len(parts) > 1 {
					ns = parts[1]
				}
				svc := e.ws.Get(e.cluster, "", "services", ns, parts[0])
				if svc == nil {
					continue
				}
				n++
				typ := s(path(svc.Obj, "spec", "type"))
				restricted := false
				for _, ap := range e.ws.List(e.cluster, "security.istio.io", "authorizationpolicies", ns, nil) {
					if strings.Contains(fmt.Sprint(path(ap.Obj, "spec", "rules")), "principals") {
						restricted = true
					}
				}
				switch {
				case typ == "LoadBalancer" || typ == "NodePort":
					c.Items = append(c.Items, e.item(svc, Fail, "MCP server Service is type "+typ+": reachable without the gateway"))
				case apOK && !restricted:
					c.Items = append(c.Items, e.item(svc, Warn, "no AuthorizationPolicy limits callers to the gateway's identity"))
				default:
					c.Items = append(c.Items, e.item(svc, Pass, "internal Service"))
				}
			}
		}
		if n == 0 {
			na(c, "no in-cluster MCP servers found")
		}
	})

	gws := md.agentgatewayGateways()
	add(spec{"agentic.audit-access-log", "Observability", "medium", "Gateways write an access log with caller identity",
		"An audit trail of who called which tool or model, from which JWT, is the governance evidence you need after an incident.",
		"frontend:\n  accessLog:\n    attributes:\n      add:\n      - {name: jwt.sub, expression: 'has(jwt.sub) ? jwt.sub : \"anonymous\"'}", kbBase + "agentgateway-mcp-controls/", solodocs.Agentgateway + "observability/access-logs/"}, func(c *Check) {
		if len(gws) == 0 {
			na(c, "no agentgateway Gateways")
			return
		}
		for _, gw := range gws {
			st, detail := Fail, "no frontend.accessLog"
			for _, p := range md.gatewayPolicies(gw) {
				al := path(p.spec, "frontend", "accessLog")
				if al == nil {
					continue
				}
				if str := fmt.Sprint(al); strings.Contains(str, "jwt.") || strings.Contains(str, "apiKey.") || strings.Contains(str, "source.identity") {
					st, detail = Pass, "identity in the access log ("+p.o.Name+")"
				} else if st == Fail {
					st, detail = Warn, "access log without identity attributes ("+p.o.Name+")"
				}
			}
			c.Items = append(c.Items, e.item(gw, st, detail))
		}
	})
	add(spec{"agentic.tracing", "Observability", "low", "Gateways export traces",
		"Trace IDs across every agent step and tool call are how an incident gets reconstructed.",
		"frontend:\n  tracing:\n    backendRef: {name: otel-collector, namespace: telemetry, port: 4317}\n    protocol: GRPC", kbBase + "agentgw-agentic-mcp/", solodocs.Agentgateway + "observability/traces/setup/"}, func(c *Check) {
		if len(gws) == 0 {
			na(c, "no agentgateway Gateways")
			return
		}
		for _, gw := range gws {
			st, detail := Fail, "no frontend.tracing"
			for _, p := range md.gatewayPolicies(gw) {
				if path(p.spec, "frontend", "tracing") != nil {
					st, detail = Pass, "tracing via "+p.o.Name
				}
			}
			c.Items = append(c.Items, e.item(gw, st, detail))
		}
	})

	add(spec{"agentic.metadata-endpoint-blocked", "Isolation", "high", "Agent namespaces cannot reach the cloud metadata endpoint",
		"The instance metadata service at 169.254.169.254 is the highest-value credential target on a node, and agents go looking for it.",
		"apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: deny-metadata, namespace: <agent namespace>}\nspec:\n  podSelector: {}\n  policyTypes: [Egress]\n  egress:\n  - to:\n    - ipBlock: {cidr: 0.0.0.0/0, except: [169.254.169.254/32]}",
		kbBase + "zero-trust-agents-kubernetes/", solodocs.Kubernetes + "concepts/services-networking/network-policies/"}, func(c *Check) {
		ok, why := e.evaluable("networking.k8s.io", "networkpolicies", "NetworkPolicy", true)
		if !ok {
			na(c, why)
			return
		}
		nss := map[string]bool{}
		for _, a := range agents {
			nss[a.Namespace] = true
		}
		if len(nss) == 0 {
			na(c, "no kagent agents")
			return
		}
		for ns := range nss {
			o := &store.Object{Resource: "namespaces", Kind: "Namespace", Name: ns}
			blocked := false
			for _, np := range e.ws.List(e.cluster, "networking.k8s.io", "networkpolicies", ns, nil) {
				if strings.Contains(fmt.Sprint(np.Obj["spec"]), "169.254.169.254") {
					blocked = true
				}
			}
			if blocked {
				c.Items = append(c.Items, e.item(o, Pass, "egress to 169.254.169.254 blocked"))
			} else {
				c.Items = append(c.Items, e.item(o, Fail, "no NetworkPolicy blocks the metadata endpoint"))
			}
		}
	})
	out = append(out, agenticExtra(e, md)...)
	return out
}

func matchesAny(name, alternatives string) bool {
	n := strings.ToLower(name)
	for _, a := range strings.Split(alternatives, "|") {
		if strings.Contains(n, a) {
			return true
		}
	}
	return false
}
