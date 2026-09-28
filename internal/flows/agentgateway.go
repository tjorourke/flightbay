package flows

import "strings"

// AgentgatewayFields are the request log fields FromAgentgateway reads. The
// workspace selects exactly these from its parsed log lines, grouped, so the
// same conversion runs on text (key=value) and JSON request logs.
//
// Field names come from agentgateway's request logger
// (crates/agentgateway/src/telemetry/log.rs): mcp.method.name, mcp.target and
// gen_ai.tool.name in current builds; mcp.method and mcp.tool in older ones.
var AgentgatewayFields = []string{
	"gateway", "listener", "route", "endpoint", "src.addr",
	"http.method", "http.host", "http.path", "http.status", "tls.sni", "protocol",
	"jwt.sub", "mcp.method.name", "mcp.method", "mcp.target", "gen_ai.tool.name", "mcp.tool", "mcp.tool.name",
	"a2a.method", "gen_ai.provider.name", "llm.provider", "inferencepool.selected_endpoint",
}

// FromAgentgateway converts one agentgateway request log line (its parsed
// fields) into a flow. ok is false when the fields are not a request log.
//
//	info request gateway=agentgateway-system/ar-ingress listener=http route=ar-keycloak/keycloak
//	  endpoint=10.244.1.131:8080 src.addr=10.244.1.1:29278 http.method=GET
//	  http.host=keycloak.example.com http.path=/realms/x http.status=200 protocol=http
func FromAgentgateway(f map[string]string) (Flow, bool) {
	if f["gateway"] == "" && f["src.addr"] == "" && f["route"] == "" {
		return Flow{}, false
	}
	out := Flow{Source: SrcAgentgateway, Via: f["gateway"], Route: f["route"], Method: f["http.method"],
		Path: StripQuery(f["http.path"]), SrcIdentity: f["jwt.sub"], Protocol: "http"}
	out.SrcIP, _ = SplitHostPort(f["src.addr"])
	ep := f["endpoint"]
	if sel := f["inferencepool.selected_endpoint"]; sel != "" {
		ep = sel
	}
	if h, p := SplitHostPort(ep); h != "" {
		if isIP(h) {
			out.DstIP = h
		} else {
			out.DstHost = h
		}
		out.DstPort = p
	}
	host := first(f["http.host"], f["tls.sni"])
	if h, _ := SplitHostPort(host); h != "" {
		host = h
	}
	if prov := first(f["gen_ai.provider.name"], f["llm.provider"]); prov != "" {
		out.Protocol = "llm"
		if host == "" {
			host = prov
		}
	}
	if host != "" {
		out.DstHost = host
		if ns := ServiceNamespace(host); ns != "" {
			out.DstNamespace = ns
		}
	}
	if m := first(f["mcp.method.name"], f["mcp.method"]); m != "" {
		out.Protocol = "mcp"
		out.Method = m
		out.Tool = first(f["gen_ai.tool.name"], f["mcp.tool.name"], f["mcp.tool"])
		if t := f["mcp.target"]; t != "" && out.Tool != "" && !strings.Contains(out.Tool, t) {
			out.Tool = t + "/" + out.Tool // multiplexed MCP: tool names are per target
		}
	} else if m := f["a2a.method"]; m != "" {
		out.Protocol = "a2a"
		out.Method = m
	} else if p := strings.ToLower(f["protocol"]); p == "grpc" || p == "tcp" {
		out.Protocol = p
	}
	switch f["http.status"] {
	case "401", "403":
		out.Verdict = Denied
	}
	out.Count = 1
	return out, true
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" && s != "-" {
			return s
		}
	}
	return ""
}
