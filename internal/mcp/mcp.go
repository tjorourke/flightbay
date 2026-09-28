// Package mcp exposes an open Flightbay workspace to AI agents as a Model
// Context Protocol server over stdio. It is a thin client of the desk's HTTP
// API, so it sees exactly what the UI sees: the redacted snapshot, coverage
// and findings. Nothing leaves the machine unless the engineer points the
// agent at a hosted model.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProtocolVersion is the MCP revision this server speaks.
const ProtocolVersion = "2025-06-18"

const maxText = 60000

// Server serves MCP requests for one desk.
type Server struct {
	BaseURL string
	Token   string
	Version string
	HTTP    *http.Client
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	call        func(s *Server, args map[string]any) (string, error)
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func arg(a map[string]any, k string) string {
	switch v := a[k].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprint(int64(v))
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

var readOnly = map[string]any{"readOnlyHint": true, "openWorldHint": false}

func tools() []tool {
	return []tool{
		{Name: "workspace_summary", Title: "Workspace summary",
			Description: "Case, clusters, capture time, collection policy (what the customer allowed), import and verification state, and finding counts. Call this first: it says what is and is not in the snapshot.",
			InputSchema: obj(map[string]any{}), call: func(s *Server, _ map[string]any) (string, error) { return s.get("/api/workspace", nil) }},
		{Name: "list_findings", Title: "Findings",
			Description: "Deterministic analyzer findings (crash loops, failing routes and policies, ambient and xDS problems, certificate expiry, version skew, repeated errors), each with evidence, a log query and a next step. Findings are signals, not verdicts.",
			InputSchema: obj(map[string]any{"cluster": str("cluster name; omit for all"), "severity": str("critical, warning or info")}),
			call: func(s *Server, a map[string]any) (string, error) {
				body, err := s.get("/api/findings", url.Values{"cluster": {arg(a, "cluster")}})
				if err != nil || arg(a, "severity") == "" {
					return body, err
				}
				var fs []map[string]any
				if json.Unmarshal([]byte(body), &fs) != nil {
					return body, nil
				}
				var out []map[string]any
				for _, f := range fs {
					if f["severity"] == arg(a, "severity") {
						out = append(out, f)
					}
				}
				b, _ := json.MarshalIndent(out, "", " ")
				return string(b), nil
			}},
		{Name: "search_logs", Title: "Search logs",
			Description: `Search every captured log line. Query syntax: level:error ns:payments pod:checkout* container:app stream:previous trace_id:abc "exact phrase" -exclude since:15m, space = AND, OR between terms. Returns newest first with ids for log_context, a total and facets. An empty result says whether logs were excluded by the customer or are still loading.`,
			InputSchema: obj(map[string]any{"query": str("log query"), "cluster": str("cluster"), "namespace": str("namespace"), "pod": str("pod"), "limit": integer("max lines, default 50")}, "query"),
			call: func(s *Server, a map[string]any) (string, error) {
				limit := arg(a, "limit")
				if limit == "" {
					limit = "50"
				}
				return s.get("/api/logs/search", url.Values{"q": {arg(a, "query")}, "cluster": {arg(a, "cluster")}, "namespace": {arg(a, "namespace")}, "pod": {arg(a, "pod")}, "limit": {limit}})
			}},
		{Name: "log_context", Title: "Lines around a log line",
			Description: "The lines before and after one log line (by id from search_logs) in its own container stream.",
			InputSchema: obj(map[string]any{"id": integer("log line id"), "lines": integer("lines either side, default 10")}, "id"),
			call: func(s *Server, a map[string]any) (string, error) {
				n := arg(a, "lines")
				if n == "" {
					n = "10"
				}
				return s.get("/api/logs/context", url.Values{"id": {arg(a, "id")}, "n": {n}})
			}},
		{Name: "search_objects", Title: "Search objects",
			Description: `Find captured objects of any kind by name, namespace, labels or anything in their spec and status (images, hostnames, status reasons). Syntax: kind:pod ns:payments name:checkout* label:app=checkout "CrashLoopBackOff" -kind:event. Returns matches ranked by name, then label, then content, with a snippet and counts per kind.`,
			InputSchema: obj(map[string]any{"query": str("object query"), "cluster": str("cluster"), "limit": integer("max results, default 50")}, "query"),
			call: func(s *Server, a map[string]any) (string, error) {
				limit := arg(a, "limit")
				if limit == "" {
					limit = "50"
				}
				return s.get("/api/objects/search", url.Values{"q": {arg(a, "query")}, "cluster": {arg(a, "cluster")}, "limit": {limit}})
			}},
		{Name: "run_sql", Title: "Read-only SQL",
			Description: "Run one read-only DuckDB SQL statement. Tables: logs (id, time, cluster, namespace, pod, container, stream, node, level, scope, msg, fields map, raw), objects (cluster, api_group, version, resource, kind, namespace, name, labels map, object JSON text: use object->>'$.status.phase'), events (cluster, namespace, type, reason, object_kind, object_name, message, count, last_seen), flows (observed traffic from Envoy and CoreDNS logs and imported flow logs: cluster, source, src_ip, src_namespace, src_pod, dst_ip, dst_port, dst_host, dst_namespace, dst_workload, protocol, method, path, tool, via, verdict, count, file, origin; sum(count) per group). Use it for counts, grouping and joins the other tools cannot do.",
			InputSchema: obj(map[string]any{"query": str("SELECT ..."), "limit": integer("max rows, default 200")}, "query"),
			call: func(s *Server, a map[string]any) (string, error) {
				limit := arg(a, "limit")
				if limit == "" {
					limit = "200"
				}
				return s.get("/api/sql", url.Values{"q": {arg(a, "query")}, "limit": {limit}})
			}},
		{Name: "best_practices", Title: "Best-practice scores",
			Description: "Scores the snapshot against Solo's best practices in four areas: agentic (agentgateway, kagent, MCP, A2A and agentregistry: authentication, tool allowlists, passthrough tokens, guardrails, budgets, credentials, policy status), Istio (ambient enrolment, STRICT mTLS, default-deny, waypoint wiring, trust domain, control-plane HA, CA and multicluster peering), ingress (Gateway API and kgateway: TLS, certificates, ReferenceGrants, HA, auth) and Kubernetes (privileged pods, capabilities, RBAC, Pod Security Admission, NetworkPolicy, exposure, requests, replicas, pinned images, versions, Helm). Returns a score and grade per area, and every check with its status, affected objects, why it matters and the fix YAML. When the desk runs with Flightbay Enterprise, it also returns priorities: the problems ranked by area score points recovered if fixed. Start from those.",
			InputSchema: obj(map[string]any{"area": str("agentic, ambient, gateway or kubernetes; omit for all"), "status": str("fail, warn, pass or na; omit for all")}),
			call: func(s *Server, a map[string]any) (string, error) {
				body, err := s.get("/api/posture", nil)
				if err != nil || (arg(a, "area") == "" && arg(a, "status") == "") {
					return body, err
				}
				var r map[string]any
				if json.Unmarshal([]byte(body), &r) != nil {
					return body, nil
				}
				var keep []any
				for _, c := range r["checks"].([]any) {
					cm := c.(map[string]any)
					if (arg(a, "area") == "" || cm["area"] == arg(a, "area")) && (arg(a, "status") == "" || cm["status"] == arg(a, "status")) {
						keep = append(keep, c)
					}
				}
				r["checks"] = keep
				b, _ := json.MarshalIndent(r, "", " ")
				return string(b), nil
			}},
		{Name: "agent_reach", Title: "Agent reach (blast radius)",
			Description: "For every agent, MCP server and agent gateway in the snapshot: what it can reach on the network, what those destinations can reach on its behalf (a package registry or proxy that can fetch from the internet lends that reach, as in the OpenAI and Hugging Face incident), whether it can write to data stores or registries, what credentials it carries (service account token, RBAC, cloud role, Secrets), what it actually called in the access logs, and the YAML to lock each path down. Omit agent for the list; pass namespace/name for one agent in full. Needs Flightbay Enterprise.",
			InputSchema: obj(map[string]any{"agent": str("namespace/name of one agent; omit for all")}),
			call: func(s *Server, a map[string]any) (string, error) {
				q := url.Values{}
				if v := arg(a, "agent"); v != "" {
					q.Set("agent", v)
				}
				return s.get("/api/reach", q)
			}},
		{Name: "list_resource_types", Title: "Resource types",
			Description: "Captured resource types with counts and collection state (complete, excluded by customer, failed, truncated, loading). A type that is excluded or failed says nothing about whether objects exist.",
			InputSchema: obj(map[string]any{"cluster": str("cluster")}),
			call: func(s *Server, a map[string]any) (string, error) {
				return s.get("/api/resources", url.Values{"cluster": {arg(a, "cluster")}})
			}},
		{Name: "list_objects", Title: "List objects",
			Description: "Objects of one type, with the same columns kubectl prints. Use group \"\" for core types (pods, services).",
			InputSchema: obj(map[string]any{"resource": str("plural resource, e.g. pods, httproutes"), "group": str("API group, e.g. gateway.networking.k8s.io"), "cluster": str("cluster"), "namespace": str("namespace"), "filter": str("words to match in the row")}, "resource"),
			call: func(s *Server, a map[string]any) (string, error) {
				return s.get("/api/objects", url.Values{"resource": {arg(a, "resource")}, "group": {arg(a, "group")}, "cluster": {arg(a, "cluster")}, "namespace": {arg(a, "namespace")}, "q": {arg(a, "filter")}})
			}},
		{Name: "get_object", Title: "Get object",
			Description: "One object as captured (redacted YAML), its related objects (owners, children, selected pods, routes and policies pointing at it), its events and, for pods, its log streams.",
			InputSchema: obj(map[string]any{"cluster": str("cluster"), "group": str("API group, \"\" for core"), "resource": str("plural resource"), "namespace": str("namespace"), "name": str("name")}, "cluster", "resource", "name"),
			call: func(s *Server, a map[string]any) (string, error) {
				body, err := s.get("/api/object", url.Values{"cluster": {arg(a, "cluster")}, "group": {arg(a, "group")}, "resource": {arg(a, "resource")}, "namespace": {arg(a, "namespace")}, "name": {arg(a, "name")}})
				if err != nil {
					return "", err
				}
				var d map[string]any
				if json.Unmarshal([]byte(body), &d) != nil {
					return body, nil
				}
				delete(d, "json") // the YAML carries the same content
				if o, ok := d["object"].(map[string]any); ok {
					delete(o, "object")
				}
				b, _ := json.MarshalIndent(d, "", " ")
				return string(b), nil
			}},
		{Name: "proxy_config", Title: "Proxy and control-plane dumps",
			Description: "Captured Envoy, ztunnel and agentgateway config dumps and istiod debug output. With no pod, lists what was captured. With a pod, returns a summary (workloads, services, listeners, routes, clusters, policies, certificates, xDS sync state). With file, returns that captured file (e.g. config_dump.json, syncz.json).",
			InputSchema: obj(map[string]any{"pod": str("pod name or prefix"), "namespace": str("namespace"), "cluster": str("cluster"), "file": str("captured file name")}),
			call:        (*Server).proxyConfig},
		{Name: "compare_baseline", Title: "Changes since an earlier bundle",
			Description: "When the desk was opened with --baseline, what changed since that earlier bundle: objects added, removed and changed, image versions, and findings that appeared or went away. Absence is only reported where both bundles covered it.",
			InputSchema: obj(map[string]any{}), call: func(s *Server, _ map[string]any) (string, error) { return s.get("/api/baseline", nil) }},
	}
}

const triagePrompt = `You are triaging a Solo.io support case from a Flightbay snapshot: an offline, redacted capture of one or more Kubernetes clusters running Istio ambient, agentgateway, kgateway or kagent. You cannot change the clusters. Work only from the snapshot.

1. Call workspace_summary. Note what the customer excluded, anything still loading, and whether the bundle verified. Never treat excluded or uncollected data as evidence of absence.
2. Call list_findings and best_practices (open MCP endpoints, missing mTLS or default-deny are often the story). Start from critical, then warning findings. Findings are signals, not conclusions.
3. For each promising finding, gather evidence (search_objects finds any object by name, label or content; run_sql answers counting and grouping questions): search_logs with its logQuery, then log_context around the key lines; get_object for the objects involved and their related objects and events; proxy_config for the proxies, ztunnel or istiod involved (listeners, routes, certificates, xDS sync).
4. Follow identifiers across pods (trace_id, request id, connection id) with search_logs.
5. If compare_baseline has data, check whether the problem started with a change.

Reply with:
- Most likely root cause, stated plainly, with a confidence level.
- Evidence: each item cites where it came from (finding id, object kind/namespace/name, log line id with pod and timestamp, or proxy dump file).
- What the snapshot cannot tell you (excluded or missing data) and what to ask the customer for.
- Suggested next steps for the customer.
Do not invent data that is not in the snapshot.`

func (s *Server) get(path string, q url.Values) (string, error) {
	u := strings.TrimRight(s.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	c := s.HTTP
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach flightbay-desk at %s (is a workspace open?): %w", s.BaseURL, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("flightbay-desk %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, b, "", " ") == nil {
		b = pretty.Bytes()
	}
	return string(b), nil
}

func (s *Server) proxyConfig(a map[string]any) (string, error) {
	list, err := s.get("/api/proxies", url.Values{"cluster": {arg(a, "cluster")}})
	if err != nil {
		return "", err
	}
	pod := arg(a, "pod")
	if pod == "" {
		return list, nil
	}
	var l struct {
		Components []struct{ Cluster, Kind, Namespace, Pod string } `json:"components"`
	}
	_ = json.Unmarshal([]byte(list), &l)
	var match []url.Values
	for _, c := range l.Components {
		if strings.HasPrefix(c.Pod, pod) && (arg(a, "namespace") == "" || c.Namespace == arg(a, "namespace")) {
			match = append(match, url.Values{"cluster": {c.Cluster}, "kind": {c.Kind}, "namespace": {c.Namespace}, "pod": {c.Pod}})
		}
	}
	switch len(match) {
	case 0:
		return "", fmt.Errorf("no captured diagnostics for pod %q; call proxy_config with no pod to list them", pod)
	case 1:
	default:
		return "", fmt.Errorf("%q matches %d pods; give the full pod name or a namespace", pod, len(match))
	}
	if f := arg(a, "file"); f != "" {
		match[0].Set("file", f)
		return s.get("/api/proxy/file", match[0])
	}
	return s.get("/api/proxy", match[0])
}

func clip(s string) string {
	if len(s) <= maxText {
		return s
	}
	return s[:maxText] + fmt.Sprintf("\n… truncated: %d of %d bytes shown. Narrow the request (filter, namespace, limit, section).", maxText, len(s))
}

func (s *Server) handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		v := ProtocolVersion
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" && p.ProtocolVersion < ProtocolVersion {
			v = p.ProtocolVersion // speak the client's older revision; the surface used here is unchanged
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}, "prompts": map[string]any{}},
			"serverInfo":      map[string]any{"name": "flightbay", "title": "Flightbay by Solo", "version": s.Version},
			"instructions":    "Read-only access to an offline Flightbay snapshot of Kubernetes clusters. Start with workspace_summary, then list_findings. Use the triage prompt for a guided investigation.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolList()}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		for _, t := range tools() {
			if t.Name == p.Name {
				text, err := t.call(s, p.Arguments)
				if err != nil {
					return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}, nil
				}
				return map[string]any{"content": []any{map[string]any{"type": "text", "text": clip(text)}}}, nil
			}
		}
		return nil, &rpcError{Code: -32602, Message: "unknown tool " + p.Name}
	case "prompts/list":
		return map[string]any{"prompts": []any{map[string]any{"name": "triage", "title": "Triage this snapshot",
			"description": "Guided root-cause investigation that cites evidence from findings, objects, logs and proxy dumps."}}}, nil
	case "prompts/get":
		return map[string]any{"description": "Triage this snapshot", "messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": triagePrompt}}}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func toolList() []tool {
	ts := tools()
	for i := range ts {
		ts[i].Annotations = readOnly
	}
	return ts
}

// HandleHTTP implements the streamable HTTP transport with plain JSON
// responses: one JSON-RPC message per POST. Notifications get 202.
func (s *Server) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "MCP over HTTP: POST a JSON-RPC message", http.StatusMethodNotAllowed)
		return
	}
	var req request
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res, rerr := s.handle(req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr})
}

// Serve reads newline-delimited JSON-RPC from r and writes responses to w
// until r ends.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(req.ID) == 0 { // notification: no reply
			continue
		}
		res, rerr := s.handle(req)
		if err := enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr}); err != nil {
			return err
		}
	}
	return sc.Err()
}
