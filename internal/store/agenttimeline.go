package store

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// TimelineFilter picks one workload's traffic out of the logs: its ztunnel
// access lines by pod name, and the agentgateway requests it made by mesh
// identity (a SPIFFE ID ending in /ns/<ns>/sa/<sa>) or pod address.
type TimelineFilter struct {
	Cluster        string
	Pods           []string
	IdentitySuffix string // "/ns/kagent/sa/tom-demo"
	IPs            []string
	Buckets        int // default 60
}

// TimeBucket is one slice of the window.
type TimeBucket struct {
	Start       time.Time `json:"start"`
	Connections int       `json:"connections"` // ztunnel connections from the workload
	Requests    int       `json:"requests"`    // agentgateway requests it made
	ToolCalls   int       `json:"toolCalls"`
	LLMCalls    int       `json:"llmCalls"`
	Refused     int       `json:"refused"` // refused by ztunnel or agentgateway policy
	Errors      int       `json:"errors"`
	Tokens      int64     `json:"tokens"`
}

// TimeEvent is the first time something was seen in the window.
type TimeEvent struct {
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"` // connection, tool, model, refusal
	What  string    `json:"what"`
	Count int       `json:"count"`
}

// Timeline is a workload's activity over the log window.
type Timeline struct {
	From    time.Time    `json:"from"`
	To      time.Time    `json:"to"`
	Step    int64        `json:"stepSeconds"`
	Buckets []TimeBucket `json:"buckets"`
	Events  []TimeEvent  `json:"events"`
}

func sqlList(xs []string) string {
	if len(xs) == 0 {
		return "NULL"
	}
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "'" + strings.ReplaceAll(x, "'", "''") + "'"
	}
	return strings.Join(q, ", ")
}

// Agentgateway reasons that mean policy refused the request.
const agwDeny = "('JwtAuth', 'Oidc', 'BasicAuth', 'APIKeyAuth', 'ExtAuth', 'Authorization', 'RateLimit', 'Guardrail')"

// AgentTimeline returns one workload's connections, calls and refusals over
// the log window, and the first time each destination, tool, model and
// refusal was seen.
func (w *Workspace) AgentTimeline(f TimelineFilter) Timeline {
	tl := Timeline{Buckets: []TimeBucket{}, Events: []TimeEvent{}}
	if f.Buckets <= 0 {
		f.Buckets = 60
	}
	zt := fmt.Sprintf(`scope IN ('access', 'http_access') AND fields['src.workload'] IN (%s)`, sqlList(f.Pods))
	var idc []string
	if f.IdentitySuffix != "" {
		idc = append(idc, fmt.Sprintf(`fields['src.identity'] LIKE '%%%s'`, strings.ReplaceAll(f.IdentitySuffix, "'", "''")))
	}
	if len(f.IPs) > 0 {
		idc = append(idc, fmt.Sprintf(`regexp_replace(coalesce(fields['src.addr'], ''), ':[0-9]+$', '') IN (%s)`, sqlList(f.IPs)))
	}
	agw := "FALSE"
	if len(idc) > 0 {
		agw = "scope = 'request' AND (" + strings.Join(idc, " OR ") + ")"
	}
	where := fmt.Sprintf("cluster = ? AND ts > 0 AND ((%s) OR (%s))", zt, agw)
	var lo, hi int64
	if err := w.ldb.db.QueryRow(`SELECT coalesce(min(ts), 0), coalesce(max(ts), 0) FROM log_lines WHERE `+where, f.Cluster).Scan(&lo, &hi); err != nil || lo == 0 {
		return tl
	}
	span := hi - lo + 1
	step := (span + int64(f.Buckets) - 1) / int64(f.Buckets)
	if step < int64(time.Second) {
		step = int64(time.Second)
	}
	n := int(span/step) + 1
	tl.From, tl.To, tl.Step = time.Unix(0, lo).UTC(), time.Unix(0, hi).UTC(), step/int64(time.Second)
	for i := 0; i < n; i++ {
		tl.Buckets = append(tl.Buckets, TimeBucket{Start: time.Unix(0, lo+int64(i)*step).UTC()})
	}
	refusedZt := `(fields['response_code'] IN ('401', '403') OR coalesce(fields['error'], '') ILIKE '%policy%')`
	refusedAgw := `(fields['reason'] IN ` + agwDeny + ` OR (fields['reason'] IS NULL AND fields['http.status'] IN ('401', '403', '429')))`
	q := fmt.Sprintf(`SELECT ((ts - %d) // %d)::INTEGER AS b,
	    greatest(count(*) FILTER (WHERE %s AND scope = 'access' AND fields['direction'] = 'outbound'), count(*) FILTER (WHERE %s AND scope = 'access' AND coalesce(fields['direction'], '') <> 'outbound')),
	    greatest(count(*) FILTER (WHERE %s AND scope = 'http_access' AND fields['direction'] = 'outbound'), count(*) FILTER (WHERE %s AND scope = 'http_access' AND coalesce(fields['direction'], '') <> 'outbound')),
	    greatest(count(*) FILTER (WHERE %s AND %s AND fields['direction'] = 'outbound'), count(*) FILTER (WHERE %s AND %s AND coalesce(fields['direction'], '') <> 'outbound')),
	    count(*) FILTER (WHERE %s),
	    count(*) FILTER (WHERE %s AND fields['mcp.method.name'] = 'tools/call'),
	    count(*) FILTER (WHERE %s AND coalesce(fields['gen_ai.provider.name'], fields['llm.provider'], fields['gen_ai.request.model']) IS NOT NULL),
	    count(*) FILTER (WHERE %s AND %s),
	    count(*) FILTER (WHERE %s AND NOT %s AND (fields['reason'] IN ('MCP', 'UpstreamFailure', 'Timeout', 'NoHealthyBackend', 'NotFound', 'Internal', 'ExtProc') OR try_cast(fields['http.status'] AS INTEGER) >= 500)),
	    coalesce(sum(try_cast(fields['gen_ai.usage.input_tokens'] AS BIGINT) + coalesce(try_cast(fields['gen_ai.usage.output_tokens'] AS BIGINT), 0)) FILTER (WHERE %s), 0)::BIGINT
	  FROM log_lines WHERE %s GROUP BY 1`, lo, step,
		zt, zt, zt, zt, zt, refusedZt, zt, refusedZt,
		agw, agw, agw, agw, refusedAgw, agw, refusedAgw, agw, where)
	rows, err := w.ldb.db.Query(q, f.Cluster)
	if err != nil {
		return tl
	}
	for rows.Next() {
		var b, conn, httpReq, ztRef, req, tools, llm, ref, errs int
		var tok int64
		if rows.Scan(&b, &conn, &httpReq, &ztRef, &req, &tools, &llm, &ref, &errs, &tok) != nil || b < 0 || b >= n {
			continue
		}
		if conn == 0 {
			conn = httpReq // builds that only log HTTP requests
		}
		x := &tl.Buckets[b]
		x.Connections, x.Requests, x.ToolCalls, x.LLMCalls, x.Refused, x.Errors, x.Tokens = conn, req, tools, llm, ztRef+ref, errs, tok
	}
	rows.Close()
	// First sightings.
	ev := fmt.Sprintf(`
	  SELECT 'connection', coalesce(fields['dst.service'], fields['dst.addr'], ''), min(ts), count(*) FROM log_lines WHERE cluster = ? AND ts > 0 AND %s GROUP BY 2
	  UNION ALL SELECT 'tool', coalesce(fields['mcp.target'] || '/', '') || fields['gen_ai.tool.name'], min(ts), count(*) FROM log_lines WHERE cluster = ? AND ts > 0 AND %s AND fields['gen_ai.tool.name'] IS NOT NULL GROUP BY 2
	  UNION ALL SELECT 'model', coalesce(fields['gen_ai.response.model'], fields['gen_ai.request.model']), min(ts), count(*) FROM log_lines WHERE cluster = ? AND ts > 0 AND %s AND coalesce(fields['gen_ai.response.model'], fields['gen_ai.request.model']) IS NOT NULL GROUP BY 2
	  UNION ALL SELECT 'refusal', coalesce(fields['reason'], 'HTTP ' || fields['http.status']) || ' at ' || coalesce(fields['route'], fields['gateway'], ''), min(ts), count(*) FROM log_lines WHERE cluster = ? AND ts > 0 AND %s AND %s GROUP BY 2
	  UNION ALL SELECT 'refusal', 'refused by ' || coalesce(fields['dst.service'], fields['dst.addr'], ''), min(ts), count(*) FROM log_lines WHERE cluster = ? AND ts > 0 AND %s AND %s GROUP BY 2`,
		zt, agw, agw, agw, refusedAgw, zt, refusedZt)
	rows, err = w.ldb.db.Query(ev, f.Cluster, f.Cluster, f.Cluster, f.Cluster, f.Cluster)
	if err != nil {
		return tl
	}
	defer rows.Close()
	for rows.Next() {
		var e TimeEvent
		var at int64
		if rows.Scan(&e.Kind, &e.What, &at, &e.Count) != nil || e.What == "" {
			continue
		}
		e.At = time.Unix(0, at).UTC()
		tl.Events = append(tl.Events, e)
	}
	sort.Slice(tl.Events, func(i, j int) bool {
		if !tl.Events[i].At.Equal(tl.Events[j].At) {
			return tl.Events[i].At.Before(tl.Events[j].At)
		}
		return tl.Events[i].What < tl.Events[j].What
	})
	return tl
}
