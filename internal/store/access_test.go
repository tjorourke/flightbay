package store

import (
	"strconv"
	"testing"
)

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

// HBONE and plaintext are told apart, and bytes come from the source's side.
func TestAccessEdgesTLSAndBytes(t *testing.T) {
	w := New()
	w.AddLogLines("c", "istio-system", "ztunnel-x", "istio-proxy", "current", []string{
		`2026-09-28T00:08:57Z {"level":"info","time":"2026-09-28T00:08:57Z","scope":"access","message":"connection complete","src.workload":"a-1","src.namespace":"a","src.identity":"spiffe://td/ns/a/sa/a","dst.addr":"10.0.0.2:15008","dst.hbone_addr":"10.0.0.2:80","dst.service":"b.b.svc.cluster.local","dst.workload":"b-1","dst.namespace":"b","dst.identity":"spiffe://td/ns/b/sa/b","direction":"outbound","bytes_sent":100,"bytes_recv":900}`,
		`2026-09-28T00:09:57Z {"level":"info","time":"2026-09-28T00:09:57Z","scope":"access","message":"connection complete","src.workload":"a-1","src.namespace":"a","dst.addr":"10.0.0.2:80","dst.service":"b.b.svc.cluster.local","dst.workload":"b-1","dst.namespace":"b","direction":"outbound","bytes_sent":5,"bytes_recv":7}`,
	}, "l")
	w.WaitLogs()
	// The plaintext line has no source identity, so it is its own group.
	e := w.AccessEdges("")
	if len(e) != 2 {
		t.Fatalf("want 2 groups, got %+v", e)
	}
	var m, p AccessEdge
	for _, x := range e {
		if x.SrcIdentity != "" {
			m = x
		} else {
			p = x
		}
	}
	if m.HBONE != 1 || m.Plaintext != 0 || m.BytesSent != 100 || m.BytesRecv != 900 || m.DstIdentity != "spiffe://td/ns/b/sa/b" {
		t.Fatalf("mTLS group %+v", m)
	}
	if p.HBONE != 0 || p.Plaintext != 1 || p.BytesSent != 5 || p.BytesRecv != 7 {
		t.Fatalf("plaintext group %+v", p)
	}
	if m.First.IsZero() || !p.Last.After(m.First) {
		t.Fatalf("first/last %v %v", m.First, p.Last)
	}
}

// agentgateway request logs are grouped with tokens and cost summed.
func TestAgentgatewayRequests(t *testing.T) {
	w := New()
	llm := func(in, out int) string {
		return "2026-09-15T10:21:00.702134Z\tinfo\trequest gateway=agentgateway-system/ai listener=http route=agentgateway-system/anthropic endpoint=api.anthropic.com:443 src.addr=10.244.1.29:48564 http.method=POST http.host=ai.agentgateway-system.svc.cluster.local http.path=/v1/chat/completions http.status=200 protocol=llm gen_ai.operation.name=chat gen_ai.provider.name=anthropic gen_ai.request.model=claude-sonnet-4-6 gen_ai.response.model=claude-sonnet-4-6 " +
			"gen_ai.usage.input_tokens=" + itoa(in) + " gen_ai.usage.output_tokens=" + itoa(out) + " agw.ai.usage.cost.total=0.5 duration=900ms"
	}
	w.AddLogLines("c", "agentgateway-system", "ai-1", "agentgateway", "current", []string{llm(100, 10), llm(200, 20),
		"2026-09-27T18:09:17.286755Z\tinfo\trequest gateway=dd-gateway/dd-waypoint listener=waypoint route=dd-gateway/tools src.addr=10.244.1.40:35974 src.identity=spiffe://mesh1/ns/dd-agents/sa/defence-agent http.method=POST http.host=tools.dd-gateway.svc http.path=/mcp http.status=403 protocol=mcp mcp.method.name=tools/call mcp.target=ops mcp.resource.type=tool gen_ai.tool.name=close_all_incidents error=\"authorization failed\" reason=Authorization duration=1ms",
	}, "l")
	w.WaitLogs()
	rs := w.AgentgatewayRequests("")
	if len(rs) != 2 {
		t.Fatalf("want 2 groups, got %+v", rs)
	}
	var llmR, mcpR *AgwRequest
	for i := range rs {
		if rs[i].Fields["protocol"] == "llm" {
			llmR = &rs[i]
		} else {
			mcpR = &rs[i]
		}
	}
	if llmR == nil || llmR.Count != 2 || llmR.InputTokens != 300 || llmR.OutputTokens != 30 || llmR.Cost != 1.0 || llmR.Fields["src.addr"] != "10.244.1.29" {
		t.Fatalf("llm group %+v", llmR)
	}
	if mcpR == nil || mcpR.Fields["reason"] != "Authorization" || mcpR.Fields["src.identity"] == "" || mcpR.Error == "" || mcpR.Fields["gen_ai.tool.name"] != "close_all_incidents" {
		t.Fatalf("mcp group %+v", mcpR)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
