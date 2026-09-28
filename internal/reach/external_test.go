package reach

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

var extraRes = map[string]bundle.APIResource{
	"Secret":              res("", "v1", "secrets", "Secret"),
	"ConfigMap":           res("", "v1", "configmaps", "ConfigMap"),
	"Pod":                 rPod,
	"Namespace":           rNS,
	"Service":             rSvc,
	"NetworkPolicy":       rNP,
	"Gateway":             res("gateway.networking.k8s.io", "v1", "gateways", "Gateway"),
	"HTTPRoute":           res("gateway.networking.k8s.io", "v1", "httproutes", "HTTPRoute"),
	"AgentgatewayBackend": res("agentgateway.dev", "v1alpha1", "agentgatewaybackends", "AgentgatewayBackend"),
	"AgentgatewayPolicy":  res("agentgateway.dev", "v1alpha1", "agentgatewaypolicies", "AgentgatewayPolicy"),
	"ServiceEntry":        res("networking.istio.io", "v1", "serviceentries", "ServiceEntry"),
}

// add puts more objects into a fixture, whatever their kind.
func add(t *testing.T, ws *store.Workspace, objs ...map[string]any) *store.Workspace {
	t.Helper()
	for i, x := range objs {
		r, ok := extraRes[x["kind"].(string)]
		if !ok {
			t.Fatalf("add: no resource for kind %v", x["kind"])
		}
		ws.AddObjects("c", r, []map[string]any{x}, "extra"+string(rune('a'+i)))
	}
	return ws
}

func externalNamed(t *testing.T, a Agent, host string) External {
	t.Helper()
	for _, x := range a.External {
		if x.Host == host {
			return x
		}
	}
	var hs []string
	for _, x := range a.External {
		hs = append(hs, x.Host+" ("+x.ReachedVia+")")
	}
	t.Fatalf("no external %s; have %v", host, hs)
	return External{}
}

func finding(a Agent, prefix string) *Finding {
	for i, f := range a.Findings {
		if strings.HasPrefix(f.Title, prefix) {
			return &a.Findings[i]
		}
	}
	return nil
}

func artifactorySecret() map[string]any {
	return map[string]any{"kind": "Secret", "type": "Opaque", "metadata": map[string]any{"name": "artifactory-creds", "namespace": "agents"},
		"x-flightbay": map[string]any{"keys": []any{map[string]any{"name": "token", "bytes": float64(64)}}}}
}

// The incident as it happened: the registry is not in the cluster at all.
// The agent is configured to install from it and holds a token for it.
func TestExternalRegistryIsTheIncident(t *testing.T) {
	a := agentNamed(t, Evaluate(add(t, incident(t, "ext-registry"), artifactorySecret())), "eval-agent")
	x := externalNamed(t, a, "acme.jfrog.io")
	if x.Class != "registry" || !x.Fetcher || x.ReachedVia != ViaDirect || x.Verdict != Allowed || x.Risk != "red" {
		t.Fatalf("external: %+v", x)
	}
	if len(x.Credentials) == 0 || !strings.HasPrefix(x.Credentials[0], "ARTIFACTORY_TOKEN") {
		t.Fatalf("credential ARTIFACTORY_TOKEN expected: %v", x.Credentials)
	}
	if !has(x.DeclaredIn, "env PIP_INDEX_URL") {
		t.Fatalf("declared in: %v", x.DeclaredIn)
	}
	f := finding(a, "Can reach an external registry that fetches from the internet: acme.jfrog.io")
	if f == nil || f.Severity != "critical" {
		t.Fatalf("critical external registry finding expected:\n%s", titles(a))
	}
	// The fix routes it through agentgateway with a read-only CEL rule and
	// cuts the agent's direct egress, and every document parses.
	for _, want := range []string{"kind: AgentgatewayBackend", "host: acme.jfrog.io", `request.method in ["GET", "HEAD"]`, `request.path.startsWith("/artifactory/api/pypi/")`, "kind: NetworkPolicy"} {
		if !strings.Contains(f.Fix, want) {
			t.Fatalf("fix lacks %q:\n%s", want, f.Fix)
		}
	}
	docs := parseFix(t, f.Fix)
	if len(docs) != 4 {
		t.Fatalf("want backend, route, policy and NetworkPolicy, got %d documents", len(docs))
	}
	if len(f.Advice) == 0 || !strings.Contains(strings.Join(f.Advice, " "), "read-only token") {
		t.Fatalf("advice: %v", f.Advice)
	}
	if !strings.Contains(findingFix(t, a, "Reaches the internet directly"), "#   acme.jfrog.io") {
		t.Fatalf("the egress fix should list the hosts the agent needs:\n%s", findingFix(t, a, "Reaches the internet directly"))
	}
	if finding(a, "Holds a credential for acme.jfrog.io") == nil {
		t.Fatalf("a write-capable token finding expected:\n%s", titles(a))
	}
	if a.Counts.External == 0 {
		t.Fatal("Counts.External")
	}
	// The graph draws it in the last column, out through the internet node.
	found := false
	for _, e := range a.Edges {
		if e.From == "internet" && e.To == "ext:acme.jfrog.io" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no internet -> acme.jfrog.io edge")
	}
	hasPath := false
	for _, p := range a.Paths {
		if p.Kind == "registry" && strings.Join(p.Hops, ">") == "agents/eval-agent>acme.jfrog.io>internet" {
			hasPath = true
		}
	}
	if !hasPath {
		t.Fatalf("registry path expected: %+v", a.Paths)
	}

	// Block the internet: the host is unreachable and the finding clears.
	b := agentNamed(t, Evaluate(add(t, incident(t, "ext-registry agent-egress"), artifactorySecret())), "eval-agent")
	x = externalNamed(t, b, "acme.jfrog.io")
	if x.Verdict != Denied || x.ReachedVia != ViaBlocked || x.Risk != "grey" {
		t.Fatalf("blocked external: %+v", x)
	}
	if finding(b, "Can reach an external registry") != nil {
		t.Fatalf("the finding should clear once egress is blocked:\n%s", titles(b))
	}
}

// agentgateway in front of the registry: the agent reaches it through the
// gateway, which authenticates the route.
func gatewayObjects(readOnly bool) []map[string]any {
	gwLabels := map[string]any{"gateway.networking.k8s.io/gateway-name": "agw"}
	objs := []map[string]any{
		o("Namespace", "", "agentgateway-system", map[string]any{"kubernetes.io/metadata.name": "agentgateway-system"}, nil),
		{"kind": "Gateway", "metadata": map[string]any{"name": "agw", "namespace": "agentgateway-system"}, "spec": map[string]any{"gatewayClassName": "agentgateway"}},
		pod("agentgateway-system", "agw", "agw", gwLabels, false, nil),
		svc("agentgateway-system", "agw", gwLabels, 8080, gwLabels),
		{"kind": "AgentgatewayBackend", "metadata": map[string]any{"name": "jfrog", "namespace": "agentgateway-system"},
			"spec": map[string]any{"static": map[string]any{"host": "acme.jfrog.io", "port": float64(443)}}},
		{"kind": "HTTPRoute", "metadata": map[string]any{"name": "jfrog", "namespace": "agentgateway-system"}, "spec": map[string]any{
			"parentRefs": []any{map[string]any{"name": "agw"}},
			"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"group": "agentgateway.dev", "kind": "AgentgatewayBackend", "name": "jfrog"}}}}}},
	}
	traffic := map[string]any{"jwtAuthentication": map[string]any{"mode": "Strict"}}
	if readOnly {
		traffic["authorization"] = map[string]any{"action": "Allow", "policy": map[string]any{"matchExpressions": []any{`request.method in ["GET", "HEAD"]`}}}
	}
	objs = append(objs, map[string]any{"kind": "AgentgatewayPolicy", "metadata": map[string]any{"name": "jfrog-authn", "namespace": "agentgateway-system"},
		"spec": map[string]any{"targetRefs": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "name": "jfrog"}}, "traffic": traffic}})
	return objs
}

func TestExternalRegistryThroughGateway(t *testing.T) {
	ws := add(t, incident(t, "no-waypoint"), gatewayObjects(false)...)
	a := agentNamed(t, Evaluate(ws), "eval-agent")
	x := externalNamed(t, a, "acme.jfrog.io")
	if x.ReachedVia != ViaGateway || x.Gateway != "agentgateway-system/agw" || x.Route != "agentgateway-system/jfrog" || !x.Authn || x.ReadOnly {
		t.Fatalf("through the gateway: %+v", x)
	}
	// The objects behind the words, so the UI can open them.
	var kinds []string
	for _, r := range x.ConfigRefs {
		kinds = append(kinds, r.Kind+" "+r.Namespace+"/"+r.Name)
	}
	if !has(kinds, "HTTPRoute agentgateway-system/jfrog") || !has(kinds, "Gateway agentgateway-system/agw") {
		t.Fatalf("config refs: %v", kinds)
	}
	// Writes are still possible (no method rule), so the finding stands,
	// and its fix makes the existing route read-only.
	f := finding(a, "Can reach an external registry")
	if f == nil {
		t.Fatalf("finding expected:\n%s", titles(a))
	}
	docs := parseFix(t, f.Fix)
	if len(docs) != 2 || docs[0]["kind"] != "AgentgatewayPolicy" || !strings.Contains(f.Fix, "name: jfrog}") || strings.Contains(f.Fix, "kind: AgentgatewayBackend\n") {
		t.Fatalf("fix should add a read-only policy to route jfrog, not a second backend:\n%s", f.Fix)
	}
	edgeFromGW := false
	for _, e := range a.Edges {
		if e.To == "ext:acme.jfrog.io" && strings.HasSuffix(e.From, "/svc/agw") {
			edgeFromGW = true
		}
	}
	if !edgeFromGW {
		t.Fatal("the graph should draw agent -> agentgateway -> acme.jfrog.io")
	}

	// Read-only at the gateway, and direct egress closed except to the
	// gateway: nothing left to fix.
	np := map[string]any{"kind": "NetworkPolicy", "metadata": map[string]any{"name": "eval-agent-egress", "namespace": "agents"}, "spec": map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"app": "eval-agent"}}, "policyTypes": []any{"Egress"},
		"egress": []any{map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "agentgateway-system"}}}}}}}}
	ws = add(t, incident(t, "no-waypoint"), append(gatewayObjects(true), np)...)
	b := agentNamed(t, Evaluate(ws), "eval-agent")
	x = externalNamed(t, b, "acme.jfrog.io")
	if x.ReachedVia != ViaGateway || !x.ReadOnly || x.DirectEgress != Denied || x.Risk != "green" || x.Write {
		t.Fatalf("read-only through the gateway: %+v", x)
	}
	if f := finding(b, "Can reach an external registry"); f != nil {
		t.Fatalf("no registry finding expected once it is read-only through the gateway:\n%s", titles(b))
	}
}

// Flows: a ztunnel line to an external address, named by a CoreDNS lookup
// from the same pod; and an agentgateway flow through the gateway.
func TestExternalFromFlows(t *testing.T) {
	ws := add(t, incident(t, "no-waypoint"), gatewayObjects(false)...)
	ws.AddLogLines("c", "istio-system", "ztunnel-x", "istio-proxy", "current", []string{
		"2026-09-28T11:06:10.074538Z\tinfo\taccess\tconnection complete\tsrc.addr=10.244.0.11:34734 src.workload=\"eval-agent\" src.namespace=\"agents\" src.identity=\"spiffe://cluster.local/ns/agents/sa/eval-agent\" dst.addr=104.18.1.2:443 direction=\"outbound\" bytes_sent=360",
		"2026-09-28T11:06:11.074538Z\tinfo\taccess\tconnection complete\tsrc.addr=10.244.0.11:34736 src.workload=\"eval-agent\" src.namespace=\"agents\" src.identity=\"spiffe://cluster.local/ns/agents/sa/eval-agent\" dst.addr=104.18.1.2:443 direction=\"outbound\" bytes_sent=360",
	}, "zt")
	ws.WaitLogs()
	extra := []store.Flow{
		{Cluster: "c", Source: "coredns", SrcIP: "10.244.0.11", DstHost: "acme.jfrog.io.", Protocol: "dns", Count: 3},
		{Cluster: "c", Source: "coredns", SrcIP: "10.244.0.11", DstHost: "acme.jfrog.io.agents.svc.cluster.local.", Protocol: "dns", Verdict: "NXDOMAIN", Count: 3},
		{Cluster: "c", Source: "agentgateway", SrcNamespace: "agents", SrcPod: "eval-agent", Via: "agentgateway-system/agw", Route: "agentgateway-system/github",
			DstHost: "api.githubcopilot.com", Protocol: "mcp", Method: "POST", Tool: "get_pull_request", Count: 12},
		// hubble saw the same two connections: counts are not added across sources
		{Cluster: "c", Source: "hubble", SrcIP: "10.244.0.11", DstIP: "104.18.1.2", DstPort: 443, Protocol: "tcp", Count: 2},
	}
	r := evaluate(ws, func(cl string) []store.Flow { return append(ws.Flows(cl), extra...) })
	a := agentNamed(t, r, "eval-agent")
	x := externalNamed(t, a, "acme.jfrog.io")
	if x.Observed != 2 || x.IP != "104.18.1.2" || !has(x.Sources, "ztunnel") || !has(x.Sources, "coredns") || !has(x.Sources, "hubble") || x.Class != "registry" {
		t.Fatalf("observed external: %+v", x)
	}
	// It called the registry directly although a gateway route exists for it.
	if x.ReachedVia != ViaDirect || x.GatewayRoute != "agentgateway-system/agw route agentgateway-system/jfrog" {
		t.Fatalf("direct call with a gateway route available: %+v", x)
	}
	if f := finding(a, "Calls external services directly, not through the gateway"); f == nil || !strings.Contains(f.Detail, "acme.jfrog.io") {
		t.Fatalf("direct-call finding expected:\n%s", titles(a))
	}
	// No route in the capture names api.githubcopilot.com.
	if f := finding(a, "Reaches external hosts it is not configured for"); f == nil || !strings.Contains(f.Detail, "api.githubcopilot.com") {
		t.Fatalf("unexpected-host finding expected:\n%s", titles(a))
	}
	gh := externalNamed(t, a, "api.githubcopilot.com")
	if gh.ReachedVia != ViaGateway || gh.Observed != 12 || !has(gh.Tools, "get_pull_request") || gh.Class != "git" {
		t.Fatalf("through the gateway: %+v", gh)
	}
	var toGW Edge
	for _, e := range a.Direct {
		if strings.HasSuffix(e.To, "/svc/agw") {
			toGW = e
		}
	}
	if toGW.Observed != 12 || !has(toGW.Tools, "get_pull_request") || !has(toGW.ObservedBy, "agentgateway") {
		t.Fatalf("agent -> gateway edge: %+v", toGW)
	}
	if !a.Observed || !has(a.ObservedBy, "ztunnel") || !has(a.ObservedBy, "agentgateway") || !has(a.ObservedBy, "hubble") || has(a.ObservedBy, "coredns") {
		t.Fatalf("observed by: %v", a.ObservedBy)
	}
	// The evidence: every source's own record, so a reader can see how each
	// destination was found. The registry was named by the DNS lookup and
	// seen by ztunnel and Hubble; the gateway edge by agentgateway.
	evSources := func(fs []store.Flow) []string {
		var out []string
		for _, f := range fs {
			out = union(out, []string{f.Source})
		}
		return out
	}
	if got := evSources(x.Evidence); len(got) != 3 || !has(got, "ztunnel") || !has(got, "coredns") || !has(got, "hubble") {
		t.Fatalf("registry evidence sources: %v (%+v)", got, x.Evidence)
	}
	if len(toGW.Evidence) != 1 || toGW.Evidence[0].Tool != "get_pull_request" || toGW.Evidence[0].Count != 12 {
		t.Fatalf("gateway edge evidence: %+v", toGW.Evidence)
	}
}

// The resolution rules on their own: source by IP, by SPIFFE ID, and
// destinations by cluster IP and pod IP.
func TestResolveFlows(t *testing.T) {
	ws := incident(t, "")
	c := newCluster(ws, "c")
	if w := c.resolveSrc(store.Flow{SrcIP: "10.244.0.11"}); w == nil || w.name != "eval-agent" {
		t.Fatalf("by IP: %v", w)
	}
	if w := c.resolveSrc(store.Flow{SrcIdentity: "spiffe://cluster.local/ns/agents/sa/eval-agent-2"}); w == nil || w.name != "eval-agent-2" {
		t.Fatalf("by SPIFFE ID: %v", w)
	}
	if d := c.resolveDst(store.Flow{DstHost: "artifactory.tools.svc.cluster.local"}); d.key != "tools/artifactory" {
		t.Fatalf("by host: %+v", d)
	}
	if d := c.resolveDst(store.Flow{DstHost: "93.184.216.34"}); d.key != "ext:93.184.216.34" {
		t.Fatalf("external IP: %+v", d)
	}
	if d := c.resolveDst(store.Flow{DstIP: "10.244.0.11", DstPort: 8080}); d.key != "" {
		t.Fatalf("a pod with no Service is not a destination: %+v", d)
	}
}

func TestExfiltrationHost(t *testing.T) {
	a := agentNamed(t, Evaluate(incident(t, "exfil")), "eval-agent")
	x := externalNamed(t, a, "webhook.site")
	if x.Class != "exfil" || x.Risk != "red" {
		t.Fatalf("webhook.site: %+v", x)
	}
	if f := finding(a, "Can reach paste, webhook or tunnel services"); f == nil || f.Severity != "critical" {
		t.Fatalf("exfil finding expected:\n%s", titles(a))
	}
}

func TestClassifyHosts(t *testing.T) {
	for h, want := range map[string]string{
		"acme.jfrog.io": "registry", "files.pythonhosted.org": "registry", "huggingface.co": "registry", "europe-docker.pkg.dev": "registry",
		"nexus.corp.example": "registry", "pypi-mirror.corp.example": "registry",
		"api.anthropic.com": "llm", "bedrock-runtime.eu-west-1.amazonaws.com": "llm", "acme.openai.azure.com": "llm",
		"api.githubcopilot.com": "git", "gitlab.com": "git",
		"my-bucket.s3.eu-west-2.amazonaws.com": "storage", "acct.blob.core.windows.net": "storage",
		"webhook.site": "exfil", "abc.ngrok-free.app": "exfil", "hooks.slack.com": "exfil", "x.trycloudflare.com": "exfil",
		"169.254.169.254": "metadata", "example.org": "unknown",
	} {
		if got, _ := classifyHost(h); got != want {
			t.Errorf("%s: got %s, want %s", h, got, want)
		}
	}
}

// Config files and env names that point at registries.
func TestDeclarationsFromConfig(t *testing.T) {
	ws := incident(t, "")
	ws = add(t, ws, map[string]any{"kind": "ConfigMap", "metadata": map[string]any{"name": "pkg", "namespace": "agents"}, "data": map[string]any{
		"pip.conf": "[global]\nindex-url = https://nexus.corp.example/repository/pypi/simple\n",
		".npmrc":   "registry=https://registry.npmjs.org/\n//registry.npmjs.org/:_authToken=[REDACTED]\n",
		".netrc":   "machine huggingface.co login x password [REDACTED]\n",
	}})
	c := newCluster(ws, "c")
	var w *workload
	for _, x := range c.works {
		if x.name == "eval-agent" {
			w = x
		}
	}
	w.spec["volumes"] = []any{map[string]any{"name": "pkg", "configMap": map[string]any{"name": "pkg"}}}
	w.spec["containers"].([]any)[0].(map[string]any)["env"] = []any{
		map[string]any{"name": "GOPROXY", "value": "https://proxy.golang.org,direct"},
		map[string]any{"name": "HF_ENDPOINT", "value": "hf-mirror.corp.example"},
		map[string]any{"name": "HTTPS_PROXY", "value": "http://squid.corp.example:3128"},
		map[string]any{"name": "NO_PROXY", "value": ".svc,.cluster.local,nexus.corp.example"},
	}
	w.decl = nil
	d := c.declarations(w)
	for _, h := range []string{"nexus.corp.example", "registry.npmjs.org", "huggingface.co", "proxy.golang.org", "hf-mirror.corp.example", "squid.corp.example"} {
		if d.hosts[h] == nil {
			t.Errorf("missing %s in %v", h, sortedKeys(d.hosts))
		}
	}
	if d.proxy != "squid.corp.example" || !noProxy("nexus.corp.example", d.noProxy) || noProxy("pypi.org", d.noProxy) {
		t.Fatalf("proxy %q no_proxy %v", d.proxy, d.noProxy)
	}
	if got := credsFor("registry.npmjs.org", d, d.hosts["registry.npmjs.org"]); len(got) == 0 {
		t.Fatalf("npm _authToken should count as a credential for the registry")
	}
}

// Envoy logs a request on both sidecars: it must count once.
func TestEnvoyBothSidesCountOnce(t *testing.T) {
	ws := incident(t, "")
	c := newCluster(ws, "c")
	agent := c.byPod[key("agents", "eval-agent")]
	fs := []store.Flow{
		{Cluster: "c", Source: "envoy", Direction: "outbound", SrcNamespace: "agents", SrcPod: "eval-agent", DstHost: "artifactory.tools.svc.cluster.local", Method: "GET", Count: 3},
		{Cluster: "c", Source: "envoy", Direction: "inbound", SrcIP: "", SrcNamespace: "agents", SrcPod: "eval-agent", DstHost: "artifactory.tools.svc.cluster.local", Method: "GET", Count: 3},
	}
	sum := c.observe(fs)
	o := sum.byW[agent][key("tools", "artifactory")]
	if o == nil || o.requests != 3 {
		t.Fatalf("want 3 requests, got %+v", o)
	}
}

// A gateway that routes back to the agent's own Service: the graph shows the
// gateway's other backends but not the agent reaching itself.
func TestGraphSkipsTheAgentsOwnService(t *testing.T) {
	objs := append(gatewayObjects(false),
		svc("agents", "eval-agent", map[string]any{"app": "eval-agent"}, 8080, nil),
		map[string]any{"kind": "HTTPRoute", "metadata": map[string]any{"name": "back", "namespace": "agentgateway-system"}, "spec": map[string]any{
			"parentRefs": []any{map[string]any{"name": "agw"}},
			"rules": []any{map[string]any{"backendRefs": []any{
				map[string]any{"kind": "Service", "name": "eval-agent", "namespace": "agents", "port": float64(8080)},
				map[string]any{"kind": "Service", "name": "postgres", "namespace": "tools", "port": float64(5432)},
			}}}}})
	a := agentNamed(t, Evaluate(add(t, incident(t, "no-waypoint"), objs...)), "eval-agent")
	toPostgres := false
	for _, e := range a.Edges {
		if strings.HasSuffix(e.From, "/svc/agw") && strings.HasSuffix(e.To, "/svc/postgres") {
			toPostgres = true
		}
		if strings.HasSuffix(e.To, "/agents/svc/eval-agent") {
			t.Fatalf("the agent should not reach its own Service in the graph: %+v", e)
		}
	}
	if !toPostgres {
		t.Fatal("the graph should still draw agw -> postgres")
	}
}
