package ambient

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func TestNamespaceSummaryIncludesAllServicesAndRealEndpointState(t *testing.T) {
	ws := fixture(t)
	v, err := NewView(ws, "c", "Namespace", "", "app", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Services) != 3 {
		t.Fatalf("lost namespace services: %d", len(v.Services))
	}
	for _, s := range v.Services {
		if s.Name == "front" && (s.State != "pass" || s.Ready != 1) {
			t.Fatalf("ready service labelled unknown: %+v", s)
		}
	}
	ws.SetCoverage(bundle.Coverage{ID: "endpointslices", Cluster: "c", Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices", Type: bundle.CoverageResource, State: bundle.StateRemoved})
	v, _ = NewView(ws, "c", "Namespace", "", "app", "")
	for _, s := range v.Services {
		if s.Name == "canary" && (s.State != "unknown" || !strings.Contains(s.Detail, "incomplete")) {
			t.Fatalf("absence treated as unhealthy with missing evidence: %+v", s)
		}
	}
}

func TestControllerDeterminesYAMLWhenBothPolicyAPIsExist(t *testing.T) {
	ws := fixture(t)
	ws.AddObjects("c", bundle.APIResource{Group: "agentgateway.dev", Version: "v1alpha1", Resource: "agentgatewaypolicies", Kind: "AgentgatewayPolicy", Namespaced: true}, nil, "oss-policy")
	wp := Target(ws, "c", "Gateway", "app", "wp")
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"rate-limit"}, Requests: 5, Unit: "Seconds"}
	for _, tc := range []struct{ class, kind, group, host string }{{"agentgateway-waypoint", "AgentgatewayPolicy", "agentgateway.dev", "agentgateway.dev"}, {"enterprise-agentgateway-waypoint", "EnterpriseAgentgatewayPolicy", "enterpriseagentgateway.solo.io", "docs.solo.io"}} {
		t.Run(tc.class, func(t *testing.T) {
			M(wp.Obj["spec"])["gatewayClassName"] = tc.class
			e := DetectEnvironment(ws, "c", Target(ws, "c", "Service", "app", "front"))
			if e.PolicyKind != tc.kind || !strings.HasPrefix(e.PolicyAPI, tc.group+"/") {
				t.Fatalf("wrong policy: %+v", e)
			}
			p, err := Generate(ws, req)
			if err != nil {
				t.Fatal(err)
			}
			if S(p.Objects[0]["kind"]) != tc.kind {
				t.Fatal(p.YAML)
			}
			if !strings.Contains(e.TaskDocs["rate-limit"][0].URL, tc.host) {
				t.Fatal(e.TaskDocs["rate-limit"])
			}
		})
	}
}

func TestMixedInstallationsAndMissingEvidence(t *testing.T) {
	ws := fixture(t)
	p := object("Pod", "ztunnel", map[string]any{"containers": []any{map[string]any{"image": "docker.io/istio/ztunnel:1.30.4"}}})
	put(ws, "", "pods", "Pod", p)
	gw := object("Gateway", "enterprise", map[string]any{"gatewayClassName": "enterprise-agentgateway-waypoint"})
	put(ws, GatewayGroup, "gateways", "Gateway", gw)
	e := DetectEnvironment(ws, "c", nil)
	if e.Edition != "mixed" {
		t.Fatalf("mixed cluster was flattened: %s", e.Edition)
	}
	empty := store.New()
	defer empty.Close()
	empty.SetHeader(&bundle.Header{Contexts: []string{"empty"}})
	e = DetectEnvironment(empty, "empty", nil)
	if e.Edition != "unknown" || len(e.Classes) != 0 {
		t.Fatal("invented an installation without evidence")
	}
}

func TestAllStepsAndTasksHaveOfficialDocs(t *testing.T) {
	ws := fixture(t)
	v, err := NewView(ws, "c", "Service", "app", "front", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range v.Steps {
		if len(s.Docs) == 0 {
			t.Fatalf("step %s has no docs", s.ID)
		}
	}
	for _, task := range Capabilities(ws, "c", "app", "front") {
		if len(v.Environment.TaskDocs[task.ID]) == 0 {
			t.Fatalf("task %s has no docs", task.ID)
		}
	}
	allowed := map[string]bool{"istio.io": true, "docs.solo.io": true, "agentgateway.dev": true, "gateway-api.sigs.k8s.io": true, "kubernetes.io": true, "www.envoyproxy.io": true}
	for _, ls := range v.Environment.TaskDocs {
		for _, d := range ls {
			u, err := url.Parse(d.URL)
			if err != nil || u.Scheme != "https" || !allowed[u.Hostname()] || d.Source == "" || d.Title == "" {
				t.Fatalf("not an official labelled link: %+v", d)
			}
		}
	}
}

func TestUnknownEditionDoesNotForceEnterpriseDocs(t *testing.T) {
	e := Environment{Edition: "unknown"}
	links := TaskDocs(e, "strict-mtls")
	if !strings.HasPrefix(links[0].URL, istioDocs) {
		t.Fatal(links)
	}
	e.Components = []Component{{Product: "Istio", Edition: "enterprise"}}
	links = TaskDocs(e, "strict-mtls")
	if !strings.HasPrefix(links[0].URL, enterpriseIstioDocs) {
		t.Fatal(links)
	}
}

func TestNoUnobservedWaypointClassesOfferedOrGenerated(t *testing.T) {
	ws := fixture(t)
	e := DetectEnvironment(ws, "c", nil)
	if len(e.Classes) != 1 || e.Classes[0].Name != "istio-waypoint" {
		t.Fatalf("offered classes not in the capture: %+v", e.Classes)
	}
	req := Request{Cluster: "c", Namespace: "app", TargetKind: "Namespace", TargetName: "app", Tasks: []string{"waypoint"}, Waypoint: &WaypointRequest{Mode: "new", Scope: "namespace", Namespace: "app", Name: "new-waypoint", Class: "enterprise-agentgateway-waypoint"}}
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("generated an unobserved Enterprise waypoint")
	}
}
func TestUnknownControllerDoesNotTrustClassName(t *testing.T) {
	ws := fixture(t)
	gc := object("GatewayClass", "istio-waypoint", map[string]any{"controllerName": "example.com/custom-controller"})
	M(gc["metadata"])["namespace"] = ""
	put(ws, GatewayGroup, "gatewayclasses", "GatewayClass", gc)
	e := DetectEnvironment(ws, "c", Target(ws, "c", "Service", "app", "front"))
	if e.Selected == nil || e.Selected.Supported || e.Selected.Product != "unknown" {
		t.Fatalf("class name overrode explicit unknown controller: %+v", e.Selected)
	}
}
func TestPolicyCRDsAloneDoNotEstablishActiveEdition(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}})
	put(ws, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy")
	e := DetectEnvironment(ws, "c", nil)
	if e.Edition != "unknown" || len(e.Classes) > 0 {
		t.Fatal("a CRD alone was treated as an active Enterprise controller")
	}
}

func TestCapabilityGuidanceIdentifiesControllerAndRegionBlockers(t *testing.T) {
	ws := fixture(t)
	wp := Target(ws, "c", "Gateway", "app", "wp")
	M(wp.Obj["spec"])["gatewayClassName"] = "enterprise-agentgateway-waypoint"
	for _, task := range Capabilities(ws, "c", "app", "front") {
		if task.ID == "methods" && (task.Supported || !strings.Contains(task.Reason, "Istio/Envoy waypoint") || !strings.Contains(task.Reason, "generator in Flightbay")) {
			t.Fatalf("controller mismatch did not explain the template limitation: %+v", task)
		}
		if task.ID == "l4-allow-namespace" && (task.Supported || !strings.Contains(task.Reason, "original caller")) {
			t.Fatalf("waypoint topology did not explain the caller-identity limitation: %+v", task)
		}
	}
	M(wp.Obj["spec"])["gatewayClassName"] = "istio-waypoint"
	for _, task := range Capabilities(ws, "c", "app", "front") {
		if task.ID == "methods" && !task.Supported {
			t.Fatalf("matching waypoint did not enable the existing method template: %+v", task)
		}
		if task.ID == "locality-failover" && (task.Supported || !strings.Contains(task.Reason, "topology.kubernetes.io/region")) {
			t.Fatalf("missing region evidence was mistaken for a controller problem: %+v", task)
		}
	}
}

// Opt-in network check: ordinary unit tests remain offline and deterministic.
func TestAmbientOfficialLinksLive(t *testing.T) {
	if os.Getenv("FLIGHTBAY_CHECK_AMBIENT_DOCS") != "1" {
		t.Skip("opt-in live link check")
	}
	envs := []Environment{{}, {Components: []Component{{Product: "Istio", Edition: "enterprise"}}, Selected: &ClassChoice{Product: "agentgateway", Edition: "enterprise"}}, {Selected: &ClassChoice{Product: "agentgateway", Edition: "oss"}}}
	ids := []string{"l4-default-deny", "strict-mtls", "methods", "weighted-routing", "timeouts", "load-balancing", "endpoint-failover", "locality-failover", "rate-limit", "global-rate-limit", "service-failover", "waypoint", "enrol-namespace", "endpoints", "scale-workload", "route-parent", ""}
	seen := map[string]bool{}
	client := &http.Client{Timeout: 25 * time.Second}
	check := func(d DocLink) {
		u := strings.Split(d.URL, "#")[0]
		if seen[u] {
			return
		}
		seen[u] = true
		t.Run(u, func(t *testing.T) {
			resp, err := client.Get(u)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("%s: %d", u, resp.StatusCode)
			}
		})
	}
	for _, e := range envs {
		for _, id := range ids {
			for _, d := range TaskDocs(e, id) {
				check(d)
			}
		}
		for _, id := range []string{"route-x-status", "backend-x-grant", "agent-policy-x", "pod-x", "external-hosts"} {
			for _, d := range StepDocs(e, Step{ID: id}) {
				check(d)
			}
		}
	}
}
