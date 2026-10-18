package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func ambientTools() []tool {
	scope := func() map[string]any {
		return map[string]any{"cluster": str("captured cluster; omit for all"), "namespace": str("namespace; omit for all")}
	}
	target := func() map[string]any {
		return map[string]any{"cluster": str("exact captured cluster"), "namespace": str("namespace, empty for a Namespace object"), "kind": str("Service, Namespace, Pod, Gateway, HTTPRoute or ServiceEntry"), "name": str("resource name"), "route": str("optional HTTPRoute name for a Service investigation")}
	}
	return []tool{
		{Name: "bookinfo_demo", Title: "Packaged Bookinfo demo and installation preview", Description: "With no request, show bundled demo YAML, images, implementation choices and namespace occupancy. With request, preview a bookinfo-demo plan with demo stage, waypointClass, waypointScope (namespace or service), waypointName (shared name or dedicated prefix), global and traffic options. Creates new waypoint plans, not selection of existing instances. Read-only preview only; installation and removal require the UI confirmations. Existing resources are never overwritten.", InputSchema: obj(map[string]any{"namespace": str("catalogue namespace; default bookinfo"), "request": obj(map[string]any{"cluster": str("captured cluster"), "namespace": str("a new application namespace, e.g. bookinfo-demo"), "tasks": map[string]any{"type": "array", "items": str("bookinfo-demo")}, "demo": obj(map[string]any{"stage": str("namespace or workloads"), "waypointClass": str("captured waypoint implementation"), "waypointScope": str("namespace: one shared new waypoint; service: four dedicated new waypoints"), "waypointName": str("new shared waypoint name or per-Service prefix; default bookinfo-waypoint"), "global": map[string]any{"type": "boolean"}, "traffic": map[string]any{"type": "boolean"}}, "stage", "waypointClass")}, "cluster", "namespace", "tasks", "demo")}), call: func(s *Server, a map[string]any) (string, error) {
			if a["request"] != nil {
				return s.previewPlan(a["request"])
			}
			return s.get("/api/ambient/bookinfo", url.Values{"namespace": {arg(a, "namespace")}})
		}},
		{Name: "ambient_request_path", Title: "Trace an ambient request path", Description: "With no request, discover captured caller and destination choices; narrow with cluster, namespace and query (names or DNS), limit defaults to 20 per list. With request, reconstruct configured hops and compare matching flow records and per-caller policy analysis. Use exact source/destination references and optional host, Service port, method and path. No network probe or cluster mutation.", InputSchema: pathToolSchema(), call: func(s *Server, a map[string]any) (string, error) {
			if a["request"] == nil {
				return s.pathChoices(a)
			}
			return s.postCaptured("/api/ambient/request-path", a["request"])
		}},
		{Name: "ambient_inventory", Title: "Ambient inventory and attachment map", Description: "Captured ambient objects, Services, waypoint choices, detected OSS/Enterprise implementations, cluster peering and global Service publication topology. Configuration relationships, not observed traffic. Filter by cluster and namespace to keep responses small.", InputSchema: obj(scope()), call: func(s *Server, a map[string]any) (string, error) { return s.ambientInventory(a, false) }},
		{Name: "ambient_issues", Title: "Known ambient failures", Description: "The same confirmed failures shown in Ambient Needs attention and Findings, with cluster, affected resource, check ID, evidence, suggested repair and verification. Missing collection data is not a confirmed failure. Use ambient_investigate on the returned subject.", InputSchema: obj(scope()), call: func(s *Server, a map[string]any) (string, error) { return s.ambientInventory(a, true) }},
		{Name: "ambient_investigate", Title: "Investigate an ambient resource", Description: "Full resource investigation: enrolment, waypoint binding and traffic type, listener and ParentRef errors, Accepted/ResolvedRefs status and generation, backend permissions, endpoints, policies, detected implementation, Service DNS, related objects, editable configuration and recovery choices. Includes suggested fixes and official documentation. Always specify the cluster.", InputSchema: obj(target(), "cluster", "kind", "name"), call: func(s *Server, a map[string]any) (string, error) {
			q := url.Values{}
			for _, k := range []string{"cluster", "namespace", "kind", "name", "route"} {
				q.Set(k, arg(a, k))
			}
			return s.get("/api/ambient/investigate", q)
		}},
		{Name: "ambient_capabilities", Title: "Supported ambient configuration tasks", Description: "Tasks supported by the captured Service's actual waypoint/controller and installed API edition: waypoint-only authorization, JWT, global publication, policies, routing and resilience. Retries, mirroring and header routing require installed HTTPRoute schema evidence. Unsupported or UNVERIFIED tasks include a reason; do not invent configuration for them.", InputSchema: obj(map[string]any{"cluster": str("captured cluster"), "namespace": str("Service namespace"), "service": str("Service name")}, "cluster", "namespace", "service"), call: func(s *Server, a map[string]any) (string, error) {
			return s.get("/api/ambient/capabilities", url.Values{"cluster": {arg(a, "cluster")}, "namespace": {arg(a, "namespace")}, "service": {arg(a, "service")}})
		}},
		{Name: "ambient_plan_preview", Title: "Preview ambient repair YAML", Description: "Generate a plan from captured evidence only: exact YAML, field-level before/after changes, notes and plan ID. No dry-run, apply, cleanup or cluster request. Supply the same request as Configure: cluster, namespace, service, tasks, and relevant options (waypoint, parent, edit, backends). Discover valid options with ambient_investigate and ambient_capabilities. Human review, real-context validation and Apply confirmation remain in the UI.", InputSchema: obj(map[string]any{"request": obj(map[string]any{"cluster": str("captured cluster"), "namespace": str("target namespace"), "service": str("Service name"), "tasks": map[string]any{"type": "array", "items": str("configuration task ID")}}, "cluster", "namespace", "tasks")}, "request"), call: func(s *Server, a map[string]any) (string, error) { return s.previewPlan(a["request"]) }},
		{Name: "scorecard_fix_preview", Title: "Preview one Scorecard fix", Description: "Generate exact YAML for one failing Scorecard entry. Supported recipes: ambient.default-deny, ambient.mtls-strict (new namespace policy), k8s.netpol-default-deny and mesh.global-service-authz (explicit caller principals required). Other checks return why more choices are needed. Read-only preview; applying requires the UI confirmation.", InputSchema: obj(map[string]any{"check": str("Scorecard check ID"), "target": obj(map[string]any{"cluster": str("captured cluster"), "kind": str("Namespace or Service"), "group": str("API group, empty for core"), "resource": str("namespaces or services"), "namespace": str("namespace, empty for Namespace"), "name": str("name")}, "cluster", "kind", "resource", "name"), "principals": map[string]any{"type": "array", "items": str("trust-domain/ns/namespace/sa/service-account")}}, "check", "target"), call: func(s *Server, a map[string]any) (string, error) {
			ref, ok := a["target"].(map[string]any)
			if !ok {
				return "", fmt.Errorf("target must be an object")
			}
			ns := arg(ref, "namespace")
			if arg(ref, "kind") == "Namespace" {
				ns = arg(ref, "name")
			}
			return s.previewPlan(map[string]any{"cluster": ref["cluster"], "namespace": ns, "targetKind": ref["kind"], "targetName": ref["name"], "tasks": []string{"scorecard"}, "scorecard": map[string]any{"check": a["check"], "target": ref, "principals": a["principals"]}})
		}},
		{Name: "ambient_change_history", Title: "Recorded ambient changes", Description: "Read locally recorded applied test plans: context, plan ID, created and modified resource identities. Excludes validation tokens and credentials. These are historical records, not fresh cluster status. HTTP request results retained only in the browser are not available here; use captured logs for request evidence.", InputSchema: obj(map[string]any{}), call: func(s *Server, _ map[string]any) (string, error) {
			b, e := s.getRaw("/api/ambient/tests", nil)
			if e != nil {
				return "", e
			}
			var records []map[string]any
			if e = json.Unmarshal(b, &records); e != nil {
				return "", e
			}
			out := []any{}
			for _, r := range records {
				p, _ := r["plan"].(map[string]any)
				out = append(out, map[string]any{"context": r["context"], "planId": p["id"], "created": r["created"], "modified": r["modified"]})
			}
			return prettyJSON(out)
		}},
	}
}

func pathToolSchema() map[string]any {
	ref := func() map[string]any {
		return obj(map[string]any{"cluster": str("captured cluster"), "group": str("API group, empty for core"), "resource": str("plural resource"), "kind": str("resource kind"), "namespace": str("namespace"), "name": str("name")}, "cluster", "kind", "resource", "namespace", "name")
	}
	return obj(map[string]any{"cluster": str("filter choices by cluster"), "namespace": str("filter choices by namespace"), "query": str("filter choices by name, kind or hostname"), "limit": integer("choices per list, default 20; maximum 50"), "request": obj(map[string]any{"source": ref(), "destination": ref(), "host": str("destination hostname"), "port": integer("Service port"), "method": str("optional HTTP method"), "path": str("optional HTTP path")}, "source", "destination")})
}
func (s *Server) pathChoices(a map[string]any) (string, error) {
	b, err := s.getRaw("/api/ambient/request-path/options", nil)
	if err != nil {
		return "", err
	}
	var data map[string][]map[string]any
	if err = json.Unmarshal(b, &data); err != nil {
		return "", err
	}
	limit := 20
	if n, e := strconv.Atoi(arg(a, "limit")); e == nil && n > 0 {
		limit = n
	}
	if limit > 50 {
		limit = 50
	}
	out := map[string]any{}
	for _, key := range []string{"callers", "destinations"} {
		rows := []map[string]any{}
		matched := 0
		for _, row := range data[key] {
			if arg(a, "cluster") != "" && row["cluster"] != arg(a, "cluster") {
				continue
			}
			if arg(a, "namespace") != "" && row["namespace"] != arg(a, "namespace") {
				continue
			}
			text, _ := json.Marshal(row)
			if query := strings.ToLower(arg(a, "query")); query != "" && !strings.Contains(strings.ToLower(string(text)), query) {
				continue
			}
			matched++
			if len(rows) < limit {
				rows = append(rows, row)
			}
		}
		out[key] = rows
		out[key+"Total"] = matched
	}
	out["note"] = "Use cluster, namespace or query to narrow choices. Pass exact references in request to analyse a path."
	return prettyJSON(out)
}

func prettyJSON(v any) (string, error) { b, e := json.MarshalIndent(v, "", " "); return string(b), e }
func (s *Server) ambientInventory(a map[string]any, issuesOnly bool) (string, error) {
	b, e := s.getRaw("/api/ambient", nil)
	if e != nil {
		return "", e
	}
	var data map[string]any
	if e = json.Unmarshal(b, &data); e != nil {
		return "", e
	}
	filter := func(v any, subject bool) []any {
		out := []any{}
		xs, _ := v.([]any)
		for _, x := range xs {
			m, _ := x.(map[string]any)
			if subject {
				m, _ = m["subject"].(map[string]any)
			}
			if (arg(a, "cluster") == "" || m["cluster"] == arg(a, "cluster")) && (arg(a, "namespace") == "" || m["namespace"] == arg(a, "namespace") || m["kind"] == "Namespace" && m["name"] == arg(a, "namespace")) {
				out = append(out, x)
			}
		}
		return out
	}
	if issuesOnly {
		return prettyJSON(map[string]any{"issues": filter(data["issues"], true), "fixGuides": data["fixGuides"], "note": "Confirmed captured configuration failures only; use ambient_investigate and coverage for missing evidence."})
	}
	delete(data, "issues")
	delete(data, "fixGuides")
	for _, k := range []string{"objects", "services", "routes", "namespaces", "waypoints"} {
		data[k] = filter(data[k], false)
	}
	if cluster := arg(a, "cluster"); cluster != "" {
		envs, _ := data["environments"].([]any)
		kept := []any{}
		for _, x := range envs {
			m, _ := x.(map[string]any)
			if m["cluster"] == cluster {
				kept = append(kept, x)
			}
		}
		data["environments"] = kept
	}
	return prettyJSON(data)
}

// POSTs from MCP are limited to captured path analysis and in-memory previews.
// There is deliberately no arbitrary endpoint, apply, validate or probe tool.
func (s *Server) previewPlan(v any) (string, error) { return s.postCaptured("/api/ambient/plan", v) }
func (s *Server) postCaptured(path string, v any) (string, error) {
	if path != "/api/ambient/plan" && path != "/api/ambient/request-path" {
		return "", fmt.Errorf("only captured analysis and preview endpoints are allowed")
	}
	if _, ok := v.(map[string]any); !ok {
		return "", fmt.Errorf("request must be an object")
	}
	base, token, e := s.desk()
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	req, e := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+path, bytes.NewReader(b))
	if e != nil {
		return "", e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", strings.TrimRight(base, "/"))
	c := s.HTTP
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, e := c.Do(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if e != nil {
		return "", e
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("preview refused (%d): %s", resp.StatusCode, body)
	}
	return string(body), nil
}
