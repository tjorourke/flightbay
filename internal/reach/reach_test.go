package reach

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func res(group, version, resource, kind string) bundle.APIResource {
	return bundle.APIResource{Group: group, Version: version, Resource: resource, Kind: kind, Namespaced: resource != "namespaces"}
}

var (
	rNS   = res("", "v1", "namespaces", "Namespace")
	rPod  = res("", "v1", "pods", "Pod")
	rSvc  = res("", "v1", "services", "Service")
	rSA   = res("", "v1", "serviceaccounts", "ServiceAccount")
	rNP   = res("networking.k8s.io", "v1", "networkpolicies", "NetworkPolicy")
	rAP   = res("security.istio.io", "v1", "authorizationpolicies", "AuthorizationPolicy")
	rPA   = res("security.istio.io", "v1", "peerauthentications", "PeerAuthentication")
	rCRB  = res("rbac.authorization.k8s.io", "v1", "clusterrolebindings", "ClusterRoleBinding")
	rRB   = res("rbac.authorization.k8s.io", "v1", "rolebindings", "RoleBinding")
	rRole = res("rbac.authorization.k8s.io", "v1", "roles", "Role")
)

func o(kind, ns, name string, labels map[string]any, extra map[string]any) map[string]any {
	md := map[string]any{"name": name, "labels": labels}
	if ns != "" {
		md["namespace"] = ns
	}
	out := map[string]any{"kind": kind, "metadata": md}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func pod(ns, name, sa string, labels map[string]any, mesh bool, extra map[string]any) map[string]any {
	p := o("Pod", ns, name, labels, map[string]any{"spec": map[string]any{"serviceAccountName": sa, "containers": []any{map[string]any{"name": "c", "image": name + ":1"}}}, "status": map[string]any{"phase": "Running"}})
	if mesh {
		p["metadata"].(map[string]any)["annotations"] = map[string]any{"ambient.istio.io/redirection": "enabled"}
	}
	for k, v := range extra {
		p["spec"].(map[string]any)[k] = v
	}
	return p
}

func svc(ns, name string, sel map[string]any, port float64, labels map[string]any) map[string]any {
	return o("Service", ns, name, labels, map[string]any{"spec": map[string]any{"selector": sel, "ports": []any{map[string]any{"name": "http", "port": port, "targetPort": port}}}})
}

// incident builds the shape of the OpenAI and Hugging Face incident: an
// agent, a package registry that accepts writes and fetches from the
// internet, a second agent, and a database.
func incident(t *testing.T, fixes string) *store.Workspace { return incidentWith(t, fixes, nil) }

// incidentWith adds extra objects (for example a generated fix) to the incident.
func incidentWith(t *testing.T, fixes string, extra []map[string]any) *store.Workspace {
	ws := store.New()
	for _, r := range []bundle.APIResource{rNS, rPod, rSvc, rSA, rNP, rAP, rPA, rCRB, rRB, rRole} {
		ws.SetCoverage(bundle.Coverage{ID: "c." + r.Resource, Cluster: "c", Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, State: bundle.StateComplete})
	}
	amb := map[string]any{"istio.io/dataplane-mode": "ambient"}
	// The registry Service uses a waypoint (labelled on the Service, as the
	// read-only fix does, so postgres does not go through it).
	noWaypoint := has(strings.Fields(fixes), "no-waypoint")
	var regLabels map[string]any
	if !noWaypoint {
		regLabels = map[string]any{"istio.io/use-waypoint": "waypoint"}
	}
	ws.AddObjects("c", rNS, []map[string]any{o("Namespace", "", "agents", amb, nil), o("Namespace", "", "tools", amb, nil),
		o("Namespace", "", "kube-system", nil, nil)}, "ns")
	agent := pod("agents", "eval-agent", "eval-agent", map[string]any{"app": "eval-agent"}, true, nil)
	agent["status"].(map[string]any)["podIP"] = "10.244.0.11"
	ct := agent["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	var env []any
	if strings.Contains(fixes, "declared") { // configured to install packages through the registry
		env = append(env, map[string]any{"name": "PIP_INDEX_URL", "value": "http://artifactory.tools:8081/api/pypi/pypi/simple"})
	}
	// The incident as it happened: the registry is outside the cluster, and
	// the agent holds a token for it.
	external := strings.Contains(fixes, "ext-registry")
	if external {
		env = append(env, map[string]any{"name": "PIP_INDEX_URL", "value": "https://acme.jfrog.io/artifactory/api/pypi/pypi/simple"},
			map[string]any{"name": "ARTIFACTORY_TOKEN", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "artifactory-creds", "key": "token"}}})
	}
	if strings.Contains(fixes, "exfil") {
		env = append(env, map[string]any{"name": "RESULTS_WEBHOOK_URL", "value": "https://webhook.site/0b1c2d3e"})
	}
	if env != nil {
		ct["env"] = env
	}
	pods := []map[string]any{
		agent,
		pod("agents", "eval-agent-2", map[bool]string{true: "eval-agent", false: "eval-agent-2"}[strings.Contains(fixes, "shared-sa")], map[string]any{"app": "eval-agent-2", "agent": "true"}, true, nil),
		pod("tools", "postgres", "postgres", map[string]any{"app": "postgres"}, true, nil),
		pod("kube-system", "coredns", "coredns", map[string]any{"k8s-app": "kube-dns"}, false, nil),
	}
	if !external {
		pods = append(pods, pod("tools", "artifactory", "artifactory", map[string]any{"app": "artifactory"}, true, nil))
	}
	if !noWaypoint {
		pods = append(pods, pod("tools", "waypoint", "waypoint", map[string]any{"gateway.networking.k8s.io/gateway-name": "waypoint"}, false, nil))
	}
	ws.AddObjects("c", rPod, pods, "p")
	svcs := []map[string]any{
		svc("tools", "postgres", map[string]any{"app": "postgres"}, 5432, nil),
		svc("kube-system", "kube-dns", map[string]any{"k8s-app": "kube-dns"}, 53, nil),
	}
	if !external {
		svcs = append(svcs, svc("tools", "artifactory", map[string]any{"app": "artifactory"}, 8081, regLabels))
	}
	if !noWaypoint { // the Service Istio creates for a waypoint
		svcs = append(svcs, svc("tools", "waypoint", map[string]any{"gateway.networking.k8s.io/gateway-name": "waypoint"}, 15008,
			map[string]any{"gateway.networking.k8s.io/gateway-class-name": "istio-waypoint", "gateway.networking.k8s.io/gateway-name": "waypoint"}))
	}
	ws.AddObjects("c", rSvc, svcs, "s")
	var nps, aps []map[string]any
	if strings.Contains(fixes, "agent-egress") {
		peer := func(ns, k, v string) map[string]any {
			return map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": ns}}, "podSelector": map[string]any{"matchLabels": map[string]any{k: v}}}
		}
		egress := []any{
			map[string]any{"to": []any{peer("kube-system", "k8s-app", "kube-dns")}, "ports": []any{map[string]any{"port": float64(53), "protocol": "UDP"}}},
			map[string]any{"to": []any{peer("tools", "app", "artifactory")}, "ports": []any{map[string]any{"port": float64(8081)}, map[string]any{"port": float64(15008)}}},
		}
		// tools uses a waypoint, so HBONE goes to the waypoint pod; without
		// this rule the agent loses the registry (seen live by test/e2e/reach.sh).
		if !strings.Contains(fixes, "no-waypoint-rule") {
			egress = append(egress, map[string]any{"to": []any{peer("tools", "gateway.networking.k8s.io/gateway-name", "waypoint")}, "ports": []any{map[string]any{"port": float64(15008)}}})
		}
		nps = append(nps, o("NetworkPolicy", "agents", "eval-agent-egress", nil, map[string]any{"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "eval-agent"}}, "policyTypes": []any{"Egress"},
			"egress": egress}}))
	}
	if strings.Contains(fixes, "registry-egress") {
		nps = append(nps, o("NetworkPolicy", "tools", "artifactory-egress", nil, map[string]any{"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "artifactory"}}, "policyTypes": []any{"Egress"}, "egress": []any{}}}))
	}
	if strings.Contains(fixes, "read-only") {
		aps = append(aps, o("AuthorizationPolicy", "tools", "artifactory-read-only", nil, map[string]any{"spec": map[string]any{
			"targetRefs": []any{map[string]any{"kind": "Service", "group": "", "name": "artifactory"}}, "action": "ALLOW",
			"rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": []any{"cluster.local/ns/agents/sa/eval-agent"}}}},
				"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET", "HEAD"}}}}}}}}))
	}
	if strings.Contains(fixes, "db-deny") {
		aps = append(aps, o("AuthorizationPolicy", "tools", "postgres-callers", nil, map[string]any{"spec": map[string]any{
			"selector": map[string]any{"matchLabels": map[string]any{"app": "postgres"}}, "action": "ALLOW",
			"rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": []any{"cluster.local/ns/tools/sa/artifactory"}}}}}}}}))
	}
	for _, x := range extra {
		switch x["kind"] {
		case "NetworkPolicy":
			nps = append(nps, x)
		case "AuthorizationPolicy":
			aps = append(aps, x)
		default:
			t.Fatalf("incidentWith: unexpected kind %v", x["kind"])
		}
	}
	ws.AddObjects("c", rNP, nps, "np")
	ws.AddObjects("c", rAP, aps, "ap")
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete })
	return ws
}

func agentNamed(t *testing.T, r Report, name string) Agent {
	for _, a := range r.Agents {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("agent %s not found in %d agents", name, len(r.Agents))
	return Agent{}
}

func edgeTo(a Agent, svcName string) Edge {
	for _, e := range a.Direct {
		if strings.HasSuffix(e.To, "/svc/"+svcName) {
			return e
		}
	}
	return Edge{}
}

func titles(a Agent) string {
	var t []string
	for _, f := range a.Findings {
		t = append(t, f.Severity+": "+f.Title)
	}
	return strings.Join(t, "\n")
}

func TestOpenClusterIsTheIncident(t *testing.T) {
	a := agentNamed(t, Evaluate(incident(t, "")), "eval-agent")
	if a.Internet != Allowed || a.Radius != "open" {
		t.Fatalf("internet=%s radius=%s", a.Internet, a.Radius)
	}
	if e := edgeTo(a, "artifactory"); e.Verdict != Allowed || !e.Write || e.Risk != "red" {
		t.Fatalf("artifactory edge %+v", e)
	}
	ft := titles(a)
	for _, want := range []string{"critical: Reaches the internet directly", "Inherits internet access through tools/artifactory", "Can write to", "Shares"} {
		if !strings.Contains(ft, want) {
			t.Errorf("missing %q in\n%s", want, ft)
		}
	}
	if !strings.Contains(a.Summary, "tools/artifactory") {
		t.Errorf("summary should name the registry path: %s", a.Summary)
	}
}

// The agent cannot reach the internet itself, but the registry can and
// accepts writes: the Hugging Face path.
func TestFetcherLendsItsInternet(t *testing.T) {
	a := agentNamed(t, Evaluate(incident(t, "agent-egress")), "eval-agent")
	if a.Internet != Denied {
		t.Fatalf("agent egress policy should block the internet, got %s", a.Internet)
	}
	if e := edgeTo(a, "postgres"); e.Verdict != "" {
		t.Fatalf("postgres should not be reachable through the egress policy: %+v", e)
	}
	if !strings.Contains(a.Summary, "2 hops through tools/artifactory") {
		t.Fatalf("summary: %s", a.Summary)
	}
	ft := titles(a)
	if !strings.Contains(ft, "critical: Inherits internet access through tools/artifactory") {
		t.Fatalf("findings:\n%s", ft)
	}
	for _, f := range a.Findings {
		if strings.HasPrefix(f.Title, "Inherits internet") && !strings.Contains(f.Fix, "methods: [GET, HEAD]") {
			t.Fatalf("fix should be a read-only rule: %s", f.Fix)
		}
	}
}

func TestLockedDownIsContained(t *testing.T) {
	a := agentNamed(t, Evaluate(incident(t, "agent-egress registry-egress read-only db-deny")), "eval-agent")
	if a.Internet != Denied || a.Counts.Critical != 0 {
		t.Fatalf("internet=%s findings:\n%s", a.Internet, titles(a))
	}
	e := edgeTo(a, "artifactory")
	if e.Verdict != Allowed || e.Write || e.Risk != "green" || strings.Join(e.Methods, ",") != "GET,HEAD" {
		t.Fatalf("artifactory should be read-only: %+v", e)
	}
	for _, p := range a.Paths {
		if p.Target == "internet" {
			t.Fatalf("no internet path expected, got %v", p.Hops)
		}
	}
	// The second agent is not named in the read-only rule, so the waypoint denies it.
	b := agentNamed(t, Evaluate(incident(t, "agent-egress registry-egress read-only db-deny")), "eval-agent-2")
	if e := edgeTo(b, "artifactory"); e.Verdict != "" {
		t.Fatalf("eval-agent-2 is not in the ALLOW rule: %+v", e)
	}
	if e := edgeTo(b, "postgres"); e.Verdict != "" {
		t.Fatalf("postgres only admits artifactory: %+v", e)
	}
}

func TestEvaluators(t *testing.T) {
	if !principalMatch("cluster.local/ns/a/sa/b", "ns/a/sa/b") || !principalMatch("*/ns/a/sa/*", "ns/a/sa/x") || principalMatch("td/ns/a/sa/b", "ns/a/sa/c") {
		t.Fatal("principal matching")
	}
	if !selectorMatches(map[string]any{"matchExpressions": []any{map[string]any{"key": "tier", "operator": "In", "values": []any{"x", "y"}}}}, map[string]string{"tier": "y"}) {
		t.Fatal("matchExpressions In")
	}
	if selectorMatches(map[string]any{"matchExpressions": []any{map[string]any{"key": "tier", "operator": "DoesNotExist"}}}, map[string]string{"tier": "y"}) {
		t.Fatal("matchExpressions DoesNotExist")
	}
	if svcKeyFromHost("artifactory.tools.svc.cluster.local", "x") != "tools/artifactory" || svcKeyFromHost("api.openai.com", "x") != "" {
		t.Fatal("host parsing")
	}
	if got := classify("jfrog-artifactory", nil); !has(got, "fetcher") {
		t.Fatalf("artifactory is a fetcher: %v", got)
	}
	if got := classify("metallb-webhook-service", nil); has(got, "fetcher") {
		t.Fatalf("a webhook receiver is not a fetcher: %v", got)
	}
}

func TestSharedIdentity(t *testing.T) {
	r := Evaluate(incident(t, "shared-sa agent-egress registry-egress read-only"))
	b := agentNamed(t, r, "eval-agent-2")
	if e := edgeTo(b, "artifactory"); e.Verdict != Allowed {
		t.Fatalf("same service account, so the ALLOW rule admits it: %+v", e)
	}
	if !strings.Contains(titles(b), "Shares its identity") {
		t.Fatalf("findings:\n%s", titles(b))
	}
}
