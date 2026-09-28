package store

import (
	"fmt"
	"strings"
	"time"
)

// AgwRequest is a group of agentgateway request log lines that agree on
// every field the activity map keeps. Token counts and cost are summed over
// the group rather than grouped on.
type AgwRequest struct {
	Cluster   string            `json:"cluster"`
	Namespace string            `json:"namespace"` // of the gateway pod that logged it
	Pod       string            `json:"pod"`
	Fields    map[string]string `json:"fields"`
	Count     int               `json:"count"`

	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	CacheRead    int64   `json:"cacheRead,omitempty"`
	CacheWrite   int64   `json:"cacheWrite,omitempty"`
	Cost         float64 `json:"cost,omitempty"`

	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	// Error is one example of the error field, when the group has one.
	Error string `json:"error,omitempty"`
}

// AgwRequestFields are the request log fields AgentgatewayRequests groups on.
// Names come from agentgateway's request logger
// (crates/agentgateway/src/telemetry/log.rs); older builds used mcp.method
// and mcp.tool, and llm.provider before gen_ai.provider.name.
var AgwRequestFields = []string{
	"gateway", "listener", "route", "endpoint", "src.addr", "src.identity", "jwt.sub",
	"http.method", "http.host", "http.status", "protocol", "reason",
	"mcp.method.name", "mcp.method", "mcp.target", "mcp.resource.type", "gen_ai.tool.name", "mcp.tool.name", "mcp.tool", "gen_ai.prompt.name",
	"a2a.method", "gen_ai.operation.name", "gen_ai.provider.name", "llm.provider", "gen_ai.request.model", "gen_ai.response.model",
}

// AgentgatewayRequests groups the agentgateway request logs (scope "request",
// text or JSON) of a cluster ("" for all). Ephemeral source ports are dropped
// so a caller's connections fall into one group.
func (w *Workspace) AgentgatewayRequests(cluster string) []AgwRequest {
	var sel []string
	for _, k := range AgwRequestFields {
		expr := fmt.Sprintf("coalesce(fields['%s'], '')", k)
		if k == "src.addr" {
			expr = fmt.Sprintf(`regexp_replace(%s, ':[0-9]+$', '')`, expr)
		}
		sel = append(sel, expr)
	}
	num := func(k, typ string) string {
		return fmt.Sprintf("coalesce(sum(try_cast(fields['%s'] AS %s)), 0)", k, typ)
	}
	q := `SELECT cluster, coalesce(namespace, ''), coalesce(pod, ''), ` + strings.Join(sel, ", ") + `, count(*),
	    ` + num("gen_ai.usage.input_tokens", "BIGINT") + `::BIGINT, ` + num("gen_ai.usage.output_tokens", "BIGINT") + `::BIGINT,
	    ` + num("gen_ai.usage.cache_read.input_tokens", "BIGINT") + `::BIGINT, ` + num("gen_ai.usage.cache_creation.input_tokens", "BIGINT") + `::BIGINT,
	    ` + num("agw.ai.usage.cost.total", "DOUBLE") + `::DOUBLE,
	    coalesce(min(ts) FILTER (WHERE ts > 0), 0), coalesce(max(ts), 0), coalesce(any_value(fields['error']), '')
	  FROM log_lines
	  WHERE scope = 'request' AND (fields['gateway'] IS NOT NULL OR fields['src.addr'] IS NOT NULL) AND (? = '' OR cluster = ?)
	  GROUP BY ALL`
	rows, err := w.ldb.db.Query(q, cluster, cluster)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AgwRequest
	vals := make([]string, len(AgwRequestFields))
	for rows.Next() {
		var r AgwRequest
		var first, last int64
		ptrs := []any{&r.Cluster, &r.Namespace, &r.Pod}
		for i := range vals {
			ptrs = append(ptrs, &vals[i])
		}
		ptrs = append(ptrs, &r.Count, &r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheWrite, &r.Cost, &first, &last, &r.Error)
		if rows.Scan(ptrs...) != nil {
			continue
		}
		r.Fields = make(map[string]string, len(vals))
		for i, k := range AgwRequestFields {
			if v := vals[i]; v != "" && v != "-" {
				r.Fields[k] = v
			}
		}
		if first > 0 {
			r.First = time.Unix(0, first).UTC()
		}
		if last > 0 {
			r.Last = time.Unix(0, last).UTC()
		}
		out = append(out, r)
	}
	return out
}
