package reach

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/store"
)

func findingFix(t *testing.T, a Agent, prefix string) string {
	t.Helper()
	for _, f := range a.Findings {
		if strings.HasPrefix(f.Title, prefix) {
			return f.Fix
		}
	}
	t.Fatalf("no finding %q in\n%s", prefix, titles(a))
	return ""
}

// parseFix parses fix YAML the way kubectl apply would, one object per document.
func parseFix(t *testing.T, fix string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, doc := range strings.Split(fix, "\n---\n") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("fix does not parse: %v\n%s", err, doc)
		}
		if obj != nil {
			out = append(out, obj)
		}
	}
	return out
}

// The fixes generated for the open incident, applied as generated, must lock
// the agent down without cutting it off from the registry it needs. This is
// the path test/e2e/reach.sh proves on a live cluster: the registry has no
// waypoint when the fixes are generated, and gets one from the read-only fix.
func TestGeneratedFixesRoundTrip(t *testing.T) {
	before := agentNamed(t, Evaluate(incident(t, "declared no-waypoint")), "eval-agent")
	egress := findingFix(t, before, "Reaches the internet directly")
	if strings.Contains(egress, "# ambient HBONE]") {
		t.Fatalf("a comment inside the flow sequence swallows the closing ]:\n%s", egress)
	}
	np := parseFix(t, egress)
	if len(np) != 1 || np[0]["kind"] != "NetworkPolicy" {
		t.Fatalf("egress fix: %v", np)
	}
	if !strings.Contains(egress, "gateway.networking.k8s.io/gateway-name: waypoint") {
		t.Fatalf("the egress fix must allow HBONE to the waypoint the read-only fix adds:\n%s", egress)
	}
	ro := findingFix(t, before, "Can write to")
	if !strings.Contains(ro, "istioctl waypoint apply -n tools") || !strings.Contains(ro, "name: artifactory}") {
		t.Fatalf("read-only fix should target the registry and say how to add a waypoint:\n%s", ro)
	}
	ap := parseFix(t, ro)
	if len(ap) != 1 || ap[0]["kind"] != "AuthorizationPolicy" {
		t.Fatalf("read-only fix: %v", ap)
	}

	// Apply them (the fixture's tools namespace now has the waypoint) plus a
	// default egress deny on the registry itself.
	r := Evaluate(incidentWith(t, "declared registry-egress", append(np, ap...)))
	a := agentNamed(t, r, "eval-agent")
	if a.Internet != Denied || a.Counts.Critical != 0 {
		t.Fatalf("internet=%s findings:\n%s", a.Internet, titles(a))
	}
	e := edgeTo(a, "artifactory")
	if e.Verdict != Allowed || e.Write || e.Risk != "green" || strings.Join(e.Methods, ",") != "GET,HEAD" {
		t.Fatalf("the registry should stay reachable, read-only: %+v", e)
	}
	for _, p := range a.Paths {
		if p.Target == "internet" {
			t.Fatalf("no internet path expected, got %v", p.Hops)
		}
	}
	if e := edgeTo(agentNamed(t, r, "eval-agent-2"), "artifactory"); e.Verdict == Allowed {
		t.Fatalf("eval-agent-2 is not in the ALLOW rule: %+v", e)
	}
	// Generated again after the fix, the egress policy is unchanged: the
	// waypoint rule now comes from the real waypoint, not the suggestion.
	if again := findingFix(t, agentNamed(t, Evaluate(incident(t, "declared")), "eval-agent"), "Reaches the internet directly"); again != egress {
		t.Fatalf("egress fix changed once the waypoint exists:\n%s\nvs\n%s", again, egress)
	}
}

// An egress policy that allows the registry's pods but not its waypoint
// blocks the agent in ambient: HBONE goes to the waypoint pod.
func TestEgressMustAllowTheWaypoint(t *testing.T) {
	ws := incident(t, "agent-egress no-waypoint-rule")
	a := agentNamed(t, Evaluate(ws), "eval-agent")
	if e := edgeTo(a, "artifactory"); e.Verdict != "" {
		t.Fatalf("artifactory should be denied without the waypoint rule: %+v", e)
	}
	c := newCluster(ws, "c")
	var agent *workload
	for _, w := range c.works {
		if w.name == "eval-agent" {
			agent = w
		}
	}
	h := c.evalHop(agent, c.svcByKey["tools/artifactory"])
	if h.verdict != Denied || !strings.Contains(strings.Join(h.reasons, "; "), "waypoint tools/waypoint") {
		t.Fatalf("want denied, naming the waypoint: %s %v", h.verdict, h.reasons)
	}
	// With the waypoint rule, allowed.
	if e := edgeTo(agentNamed(t, Evaluate(incident(t, "agent-egress")), "eval-agent"), "artifactory"); e.Verdict != Allowed {
		t.Fatalf("with the waypoint rule the registry is reachable: %+v", e)
	}
}

// A service with no waypoint and nothing sensitive gets no waypoint rule.
func TestEgressFixNoWaypointForPlainServices(t *testing.T) {
	c := &cluster{}
	e := &engine{c: c}
	w := &workload{ns: "agents", name: "a", inMesh: true, labels: map[string]string{"app": "a"}, obj: &store.Object{Obj: map[string]any{}}}
	sv := &service{ns: "web", name: "docs", id: "c/web/svc/docs", selector: map[string]string{"app": "docs"}, ports: []svcPort{{name: "http", port: 80, target: "80"}},
		backends: []*workload{{ns: "web", name: "docs", inMesh: true}}}
	got := e.egressPolicy(w, []*service{sv}, nil)
	if strings.Contains(got, "gateway-name") {
		t.Fatalf("no waypoint rule expected:\n%s", got)
	}
	if !strings.Contains(got, "ports: [{port: 80}, {port: 15008}]   # 15008 is ambient HBONE") {
		t.Fatalf("ports line:\n%s", got)
	}
}
