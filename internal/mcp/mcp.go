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

// Server serves MCP requests for one desk, or with Desks set, for whichever
// open desk is selected (the stdio server).
type Server struct {
	BaseURL string
	Token   string
	Version string
	HTTP    *http.Client
	// Desks lists the desks open on this machine, most recently opened
	// first. It is read on every call and replaces BaseURL and Token.
	Desks func() ([]Desk, error)

	selected string // WorkDir chosen with the workspaces tool
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
	// links picks resources out of a result for the client to follow
	// (resource_link content items), such as a finding's object.
	links func(result string) []resource
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
			Description: "Deterministic analyzer findings (crash loops, failing routes and policies, ambient and xDS problems, certificate expiry, version skew, repeated errors), each with evidence, a log query and a next step, and for Solo components (mesh, waypoints, agentgateway) a docs link to the Solo product documentation page that helps with it. Findings are signals, not verdicts.",
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
				out := []map[string]any{}
				for _, f := range fs {
					if f["severity"] == arg(a, "severity") {
						out = append(out, f)
					}
				}
				b, _ := json.MarshalIndent(out, "", " ")
				return string(b), nil
			},
			links: findingLinks},
		{Name: "timeline", Title: "Timeline",
			Description: "Events, container exits, rollouts and error spikes in time order, each with its object and a log query for the moment it happened. Use it to find what failed first and whether a change came before it.",
			InputSchema: obj(map[string]any{"cluster": str("cluster; omit for all"), "kind": str("event, termination, rollout or error-spike; omit for all"), "since": str("RFC 3339 time; only items at or after it")}),
			call: func(s *Server, a map[string]any) (string, error) {
				body, err := s.get("/api/timeline", url.Values{"cluster": {arg(a, "cluster")}})
				if err != nil || (arg(a, "kind") == "" && arg(a, "since") == "") {
					return body, err
				}
				var items []map[string]any
				if json.Unmarshal([]byte(body), &items) != nil {
					return body, nil
				}
				keep := []map[string]any{}
				for _, it := range items {
					t, _ := it["time"].(string)
					if (arg(a, "kind") == "" || it["kind"] == arg(a, "kind")) && (arg(a, "since") == "" || t >= arg(a, "since")) {
						keep = append(keep, it)
					}
				}
				b, _ := json.MarshalIndent(keep, "", " ")
				return string(b), nil
			}},
		{Name: "coverage", Title: "Coverage",
			Description: "What each collection produced and its state (complete, failed, excluded by the customer, truncated, removed or trimmed in review) with the reason, plus the collection log and the redaction report. Check it before saying something does not exist: a type or log stream that was not collected says nothing either way.",
			InputSchema: obj(map[string]any{"cluster": str("cluster; omit for all"), "state": str("only collections in this state, e.g. failed or excluded"), "type": str("resource, events, logs, diagnostic or flows")}),
			call: func(s *Server, a map[string]any) (string, error) {
				body, err := s.get("/api/coverage", nil)
				if err != nil || (arg(a, "cluster") == "" && arg(a, "state") == "" && arg(a, "type") == "") {
					return body, err
				}
				var c map[string]any
				if json.Unmarshal([]byte(body), &c) != nil {
					return body, nil
				}
				keep := []any{}
				cov, _ := c["coverage"].([]any)
				for _, x := range cov {
					m, _ := x.(map[string]any)
					if (arg(a, "cluster") == "" || m["cluster"] == arg(a, "cluster")) && (arg(a, "state") == "" || m["state"] == arg(a, "state")) && (arg(a, "type") == "" || m["type"] == arg(a, "type")) {
						keep = append(keep, x)
					}
				}
				c["coverage"] = keep
				// A filtered call keeps only the log entries that did not
				// succeed; the full log is thousands of "ok" lines.
				var notOK []any
				log, _ := c["collectionLog"].([]any)
				for _, l := range log {
					m, _ := l.(map[string]any)
					if r, _ := m["result"].(string); !strings.HasPrefix(r, "ok") && (arg(a, "cluster") == "" || m["cluster"] == arg(a, "cluster")) {
						notOK = append(notOK, l)
					}
				}
				c["collectionLog"] = notOK
				b, _ := json.MarshalIndent(c, "", " ")
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
			Description: "Scores the snapshot against Solo's best practices in four areas: agentic (agentgateway, kagent, MCP, A2A and agentregistry: authentication, tool allowlists, passthrough tokens, guardrails, budgets, credentials, policy status), Istio (ambient enrolment, STRICT mTLS, default-deny, waypoint wiring, trust domain, control-plane HA, CA and multicluster peering), ingress (Gateway API and kgateway: TLS, certificates, ReferenceGrants, HA, auth) and Kubernetes (privileged pods, capabilities, RBAC, Pod Security Admission, NetworkPolicy, exposure, requests, replicas, pinned images, versions, Helm). Returns a score and grade per area, and every check with its status, affected objects, why it matters, the fix YAML, source (the Solo field guide that walks through it) and docs (the official Solo product documentation page on docs.solo.io, or kubernetes.io for plain Kubernetes checks). When the desk runs with Flightbay Enterprise, it also returns priorities: the problems ranked by area score points recovered if fixed. Start from those.",
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
			Description: "For every agent, MCP server and agent gateway in the snapshot: what it can reach on the network, what those destinations can reach on its behalf (a package registry or proxy that can fetch from the internet lends that reach, as in the OpenAI and Hugging Face incident), whether it can write to data stores or registries, what credentials it carries (service account token, RBAC, cloud role, Secrets), what it actually called in the access logs, and the YAML to lock each path down, each finding with source (the field guide) and docs (the Solo product documentation page). Omit agent for the list; pass namespace/name for one agent in full. Needs Flightbay Enterprise.",
			InputSchema: obj(map[string]any{"agent": str("namespace/name of one agent; omit for all")}),
			call: func(s *Server, a map[string]any) (string, error) {
				q := url.Values{}
				if v := arg(a, "agent"); v != "" {
					q.Set("agent", v)
				}
				return s.get("/api/reach", q)
			}},
		{Name: "agent_activity", Title: "Agent and tool activity",
			Description: "From agentgateway request logs (gateways and agentgateway waypoints): who called which MCP tool, model or agent, as whom (mesh identity, JWT subject or address, resolved to a workload), how often, what was refused and why (with the agentgateway policies on the route, gateway or Service that could have refused it), what failed, and tokens and cost per model and caller. Tools are checked against the tools each kagent MCP server lists and against each declarative Agent's toolNames. With a baseline, tools added, removed or whose description changed (a rug pull). Pass view to trim: calls, tools, models, denials or changes. Needs Flightbay Enterprise.",
			InputSchema: obj(map[string]any{"view": str("calls, tools, models, denials or changes; omit for everything")}),
			call: func(s *Server, a map[string]any) (string, error) {
				body, err := s.get("/api/activity", nil)
				if err != nil || arg(a, "view") == "" {
					return body, err
				}
				var r map[string]any
				if json.Unmarshal([]byte(body), &r) != nil {
					return body, nil
				}
				keep := map[string][]string{"calls": {"callers", "targets", "edges", "findings"}, "tools": {"targets", "tools"}, "models": {"targets", "callers"},
					"denials": {"denials", "callers", "targets"}, "changes": {"changes", "findings"}}[arg(a, "view")]
				if keep == nil {
					return "", fmt.Errorf("view must be calls, tools, models, denials or changes")
				}
				out := map[string]any{"totals": r["totals"], "notes": r["notes"], "coverage": r["coverage"]}
				for _, k := range keep {
					out[k] = r[k]
				}
				b, _ := json.MarshalIndent(out, "", " ")
				return string(b), nil
			}},
		{Name: "traffic_map", Title: "Ambient traffic map",
			Description: "Every connection ztunnel logged between workloads and Services: connections, refusals, HBONE (mutual TLS) versus plaintext, the waypoint that carried it, and the verdict of the captured NetworkPolicies and AuthorizationPolicies from the Agent reach evaluator. Edges are flagged plaintext (between two mesh workloads), plaintext-in (from outside the mesh), unpoliced (no policy looks at it), denied, or mismatch (logs and policy disagree). Pass flag to keep only those edges. Needs Flightbay Enterprise.",
			InputSchema: obj(map[string]any{"flag": str("plaintext, plaintext-in, unpoliced, denied or mismatch")}),
			call: func(s *Server, a map[string]any) (string, error) {
				body, err := s.get("/api/traffic", nil)
				if err != nil || arg(a, "flag") == "" {
					return body, err
				}
				var r map[string]any
				if json.Unmarshal([]byte(body), &r) != nil {
					return body, nil
				}
				var keep []any
				for _, e := range r["edges"].([]any) {
					for _, f := range e.(map[string]any)["flags"].([]any) {
						if f == arg(a, "flag") {
							keep = append(keep, e)
							break
						}
					}
				}
				r["edges"] = keep
				b, _ := json.MarshalIndent(r, "", " ")
				return string(b), nil
			}},
		{Name: "explain_connection", Title: "Why was this allowed or denied?",
			Description: "For one source workload and one destination Service: every enforcement point in the order traffic meets it (NetworkPolicy egress and ingress, mTLS, ztunnel L4 authorization, waypoint L7 authorization), the policies each one read and what it decided, conditions that cannot be evaluated from a snapshot, what the access logs saw for the pair, and the smallest YAML that would allow it when it is denied for want of an ALLOW rule. from is ns/workload, a pod or a SPIFFE ID; to is ns/service, a host name, or a workload. Needs Flightbay Enterprise.",
			InputSchema: obj(map[string]any{"from": str("source workload: ns/name, pod, or spiffe:// ID"), "to": str("destination Service: ns/name or host"), "cluster": str("cluster, when there are several")}, "from", "to"),
			call: func(s *Server, a map[string]any) (string, error) {
				return s.get("/api/explain", url.Values{"from": {arg(a, "from")}, "to": {arg(a, "to")}, "cluster": {arg(a, "cluster")}})
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
			},
			links: objectLinks},
		{Name: "proxy_config", Title: "Proxy and control-plane dumps",
			Description: "Captured Envoy, ztunnel and agentgateway config dumps and istiod debug output. With no pod, lists what was captured. With a pod, returns a summary (workloads, services, listeners, routes, clusters, policies, certificates, xDS sync state). With file, returns that captured file (e.g. config_dump.json, syncz.json).",
			InputSchema: obj(map[string]any{"pod": str("pod name or prefix"), "namespace": str("namespace"), "cluster": str("cluster"), "file": str("captured file name")}),
			call:        (*Server).proxyConfig},
		{Name: "compare_baseline", Title: "Changes since an earlier bundle",
			Description: "When the desk was opened with --baseline, what changed since that earlier bundle: objects added, removed and changed, image versions, and findings that appeared or went away. Absence is only reported where both bundles covered it.",
			InputSchema: obj(map[string]any{}), call: func(s *Server, _ map[string]any) (string, error) { return s.get("/api/baseline", nil) }},
	}
}

// tools is the tool set this server offers: workspaces only makes sense
// when there is more than one desk to choose from.
func (s *Server) tools() []tool {
	ts := tools()
	if s.Desks != nil {
		ts = append([]tool{{Name: "workspaces", Title: "Open desks",
			Description: "The Flightbay desks open on this machine (each one a bundle or a live mirror) and which one every other tool reads, most recently opened first. Pass use (a number from the list, a bundle file name or a live context) to switch. Call this when the user names a bundle or when more than one desk may be open.",
			InputSchema: obj(map[string]any{"use": str("desk to read from now on: list number, bundle file name or live context; omit to list")}),
			call:        func(s *Server, a map[string]any) (string, error) { return s.workspaces(a) }}}, ts...)
	}
	return ts
}

const instructions = "Read-only access to an offline Flightbay snapshot of Kubernetes clusters, usually captured in a customer's environment. The clusters themselves are not reachable from here: answer only from these tools and resources, and never run kubectl, cloud CLIs or other commands against a cluster, or call the desk's HTTP API, in their place. If a tool says no desk is open or cannot reach it, tell the user and ask them to open the bundle (`flightbay ui BUNDLE`). Start with workspace_summary, then list_findings. Use the triage prompt for a guided investigation. Objects, log streams, proxy dumps, findings and Scorecard checks are also flightbay:// resources you can read or attach."

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

// get fetches a desk API path and returns it as indented JSON text.
func (s *Server) get(path string, q url.Values) (string, error) {
	b, err := s.getRaw(path, q)
	if err != nil {
		return "", err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, b, "", " ") == nil {
		b = pretty.Bytes()
	}
	return string(b), nil
}

// getText fetches a desk API path and returns the body as it came.
func (s *Server) getText(path string, q url.Values) (string, error) {
	b, err := s.getRaw(path, q)
	return string(b), err
}

// getRaw fetches a desk API path.
func (s *Server) getRaw(path string, q url.Values) ([]byte, error) {
	base, token, err := s.desk()
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	c := s.HTTP
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the Flightbay desk at %s (%v); it has probably closed. Ask the user to open the bundle again with `flightbay ui BUNDLE`. %s", base, err, noFallback)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("the Flightbay desk at %s did not accept this MCP server's token: it was restarted, or FLIGHTBAY_URL and FLIGHTBAY_TOKEN name another desk. The user does not need to sign in: restarting the MCP server (or the agent) picks up the open desk. %s", base, noFallback)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("flightbay-desk %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
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
			"capabilities":    map[string]any{"tools": map[string]any{}, "prompts": map[string]any{}, "resources": map[string]any{}, "completions": map[string]any{}},
			"serverInfo":      map[string]any{"name": "flightbay", "title": "Solo flightbay", "version": s.Version},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolList()}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		for _, t := range s.tools() {
			if t.Name == p.Name {
				text, err := t.call(s, p.Arguments)
				if err != nil {
					return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}, nil
				}
				content := []any{map[string]any{"type": "text", "text": clip(text)}}
				if t.links != nil {
					for _, l := range t.links(text) {
						content = append(content, map[string]any{"type": "resource_link", "uri": l.URI, "name": l.Name, "title": l.Title, "description": l.Description, "mimeType": l.MimeType})
					}
				}
				return map[string]any{"content": content}, nil
			}
		}
		return nil, &rpcError{Code: -32602, Message: "unknown tool " + p.Name}
	case "prompts/list":
		return map[string]any{"prompts": prompts()}, nil
	case "prompts/get":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		if p.Name == "" {
			p.Name = "triage" // older clients asked for the only prompt without naming it
		}
		res, err := s.getPrompt(p.Name, p.Arguments)
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		return res, nil
	case "resources/list":
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(req.Params, &p)
		res, err := s.listResources(p.Cursor)
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: err.Error()}
		}
		return res, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": templates}, nil
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.URI == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid params: uri is required"}
		}
		c, err := s.readResource(p.URI)
		if err != nil {
			return nil, &rpcError{Code: -32002, Message: err.Error()}
		}
		return map[string]any{"contents": []any{c}}, nil
	case "completion/complete":
		var p struct {
			Ref struct {
				Type string `json:"type"`
				URI  string `json:"uri"`
				Name string `json:"name"`
			} `json:"ref"`
			Argument struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"argument"`
			Context struct {
				Arguments map[string]string `json:"arguments"`
			} `json:"context"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		var vals []string
		var err error
		switch p.Ref.Type {
		case "ref/resource":
			vals, err = s.complete(p.Ref.URI, p.Argument.Name, p.Argument.Value, p.Context.Arguments)
		case "ref/prompt":
			vals, err = s.completePrompt(p.Argument.Name, p.Argument.Value, p.Context.Arguments)
		}
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: err.Error()}
		}
		total := len(vals)
		if len(vals) > 100 {
			vals = vals[:100]
		}
		if vals == nil {
			vals = []string{}
		}
		return map[string]any{"completion": map[string]any{"values": vals, "total": total, "hasMore": total > len(vals)}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) toolList() []tool {
	ts := s.tools()
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
