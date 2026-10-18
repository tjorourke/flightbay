package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		{Name: "ambient_inventory", Title: "Ambient inventory and attachment map", Description: "Captured ambient objects, Services, waypoint choices, detected OSS/Enterprise implementations, cluster peering and global Service publication topology. Configuration relationships, not observed traffic. Filter by cluster and namespace to keep responses small.", InputSchema: obj(scope()), call: func(s *Server, a map[string]any) (string, error) { return s.ambientInventory(a, false) }},
		{Name: "ambient_issues", Title: "Known ambient failures", Description: "The same confirmed failures shown in Ambient Needs attention and Findings, with cluster, affected resource, check ID, evidence, suggested repair and verification. Missing collection data is not a confirmed failure. Use ambient_investigate on the returned subject.", InputSchema: obj(scope()), call: func(s *Server, a map[string]any) (string, error) { return s.ambientInventory(a, true) }},
		{Name: "ambient_investigate", Title: "Investigate an ambient resource", Description: "Full resource investigation: enrolment, waypoint binding and traffic type, listener and ParentRef errors, Accepted/ResolvedRefs status and generation, backend permissions, endpoints, policies, detected implementation, Service DNS, related objects, editable configuration and recovery choices. Includes suggested fixes and official documentation. Always specify the cluster.", InputSchema: obj(target(), "cluster", "kind", "name"), call: func(s *Server, a map[string]any) (string, error) {
			q := url.Values{}
			for _, k := range []string{"cluster", "namespace", "kind", "name", "route"} {
				q.Set(k, arg(a, k))
			}
			return s.get("/api/ambient/investigate", q)
		}},
		{Name: "ambient_capabilities", Title: "Supported ambient configuration tasks", Description: "Tasks supported by the captured Service's actual waypoint/controller and installed API edition, including global publication, policies, routing and resilience. Unsupported tasks include a reason; do not invent configuration for them.", InputSchema: obj(map[string]any{"cluster": str("captured cluster"), "namespace": str("Service namespace"), "service": str("Service name")}, "cluster", "namespace", "service"), call: func(s *Server, a map[string]any) (string, error) {
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

// The sole POST allowed by the MCP adapter generates a preview in memory.
// There is deliberately no arbitrary endpoint, apply, validate or probe tool.
func (s *Server) previewPlan(v any) (string, error) {
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
	req, e := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/api/ambient/plan", bytes.NewReader(b))
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
