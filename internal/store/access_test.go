package store

import "testing"

// Upstream ztunnel writes one "access" line per connection, with no method or
// path. Those lines must still become edges (found by test/e2e/reach.sh: every
// L4 edge was dropped because the NULL method list failed to scan).
func TestAccessEdgesFromConnectionLines(t *testing.T) {
	w := New()
	w.AddLogLines("c", "istio-system", "ztunnel-x", "istio-proxy", "current", []string{
		"2026-09-28T11:06:10.074538Z\tinfo\taccess\tconnection complete\tsrc.addr=10.244.0.11:34734 src.workload=\"eval-agent-5898644d8f-td826\" src.namespace=\"agents\" src.identity=\"spiffe://cluster.local/ns/agents/sa/eval-agent\" dst.addr=10.244.0.9:15008 dst.hbone_addr=10.244.0.9:8081 dst.service=\"artifactory.tools.svc.cluster.local\" dst.workload=\"artifactory-84889f66f6-8fb7m\" dst.namespace=\"tools\" direction=\"inbound\" bytes_sent=360",
		"2026-09-28T11:06:11.074538Z\tinfo\taccess\tconnection complete\tsrc.addr=10.244.0.11:34736 src.workload=\"eval-agent-5898644d8f-td826\" src.namespace=\"agents\" src.identity=\"spiffe://cluster.local/ns/agents/sa/eval-agent\" dst.addr=10.244.0.9:15008 dst.hbone_addr=10.244.0.9:8081 dst.service=\"artifactory.tools.svc.cluster.local\" dst.workload=\"artifactory-84889f66f6-8fb7m\" dst.namespace=\"tools\" direction=\"inbound\" bytes_sent=360",
	}, "l")
	w.WaitLogs()
	e := w.AccessEdges("")
	if len(e) != 1 {
		t.Fatalf("want 1 edge, got %+v", e)
	}
	if e[0].SrcPod != "eval-agent-5898644d8f-td826" || e[0].DstService != "artifactory.tools.svc.cluster.local" || e[0].Requests != 2 || len(e[0].Methods) != 0 {
		t.Fatalf("edge %+v", e[0])
	}
}

// A connection logged by both ztunnels counts once.
func TestAccessEdgesCountBothEndsOnce(t *testing.T) {
	w := New()
	line := func(dir string) string {
		return "2026-09-28T11:06:10.074538Z\tinfo\taccess\tconnection complete\tsrc.workload=\"agent-1\" src.namespace=\"a\" src.identity=\"spiffe://cluster.local/ns/a/sa/agent\" dst.service=\"db.b.svc.cluster.local\" dst.workload=\"db-1\" dst.namespace=\"b\" direction=\"" + dir + "\""
	}
	w.AddLogLines("c", "istio-system", "ztunnel-src", "istio-proxy", "current", []string{line("outbound"), line("outbound")}, "l1")
	w.AddLogLines("c", "istio-system", "ztunnel-dst", "istio-proxy", "current", []string{line("inbound"), line("inbound")}, "l2")
	w.WaitLogs()
	e := w.AccessEdges("")
	if len(e) != 1 || e[0].Requests != 2 {
		t.Fatalf("two connections logged at both ends should count as 2, got %+v", e)
	}
}
