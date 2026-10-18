package mcp

import (
	"fmt"
	"strings"
)

type promptArg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}

type prompt struct {
	Name        string      `json:"name"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Arguments   []promptArg `json:"arguments,omitempty"`
	// text builds the instruction from the arguments; attach lists
	// resources embedded in the prompt so the client starts with them.
	text   func(a map[string]string) string
	attach func(a map[string]string) []string
}

const evidenceRules = `
Cite every claim: a finding id, an object as kind namespace/name, a log line id with its pod and time, or a proxy dump file. Say what the snapshot cannot tell you (excluded, failed or truncated collections are in the coverage resource) and what to ask the customer for. Do not invent data that is not in the snapshot.`

func prompts() []prompt {
	return []prompt{
		{Name: "ambient_triage", Title: "Investigate ambient failures", Description: "Find failures across clusters, trace attachments and propose an evidence-based repair preview.", Arguments: []promptArg{{Name: "symptom", Description: "optional symptom or resource"}, {Name: "cluster", Description: "optional captured cluster"}}, text: func(a map[string]string) string {
			return fmt.Sprintf(`Investigate Istio ambient in this Flightbay workspace. Symptom: %s. Cluster: %s.
1. Call workspace_summary to establish live mirror versus offline capture, clusters and coverage.
2. Call ambient_issues, filtered by cluster if provided. Report confirmed failures before asking the user to choose a resource.
3. Call ambient_investigate for each relevant subject using its exact cluster, kind, namespace and name. Distinguish missing evidence, stale status and optional configuration from proven failures.
4. Use ambient_inventory for peering/global publication topology; investigation includes Service DNS and detected controller/edition. Use proxy_config and logs for delivered configuration and observed traffic.
5. Use ambient_capabilities and ambient_plan_preview for supported fixes. For Scorecard entries use best_practices and scorecard_fix_preview. Do not infer frontend intent from a backend or invent caller principals.
6. Present exact YAML, affected cluster and resources, field changes, verification steps and the UI review link. MCP does not apply, validate against Kubernetes, issue HTTP tests or undo changes. The user reviews and confirms in the UI.
HTTP test results held only in the browser are not part of the MCP workspace; request logs or ask the user to share them.`+evidenceRules, a["symptom"], a["cluster"])
		}},
		{Name: "triage", Title: "Triage this snapshot",
			Description: "Guided root-cause investigation that cites evidence from findings, objects, logs and proxy dumps.",
			Arguments:   []promptArg{{Name: "symptom", Description: "what the customer reported, e.g. 503s from checkout since 14:00"}},
			text: func(a map[string]string) string {
				t := triagePrompt
				if a["symptom"] != "" {
					t = "The customer reports: " + a["symptom"] + "\nFocus the investigation on that symptom.\n\n" + t
				}
				return t
			},
			attach: func(map[string]string) []string { return []string{scheme + "workspace", scheme + "findings"} }},
		{Name: "pod_debug", Title: "Why is this pod unhealthy?",
			Description: "Works through one pod: its spec and status, events, previous and current logs, owner and the Services and policies that select it.",
			Arguments:   []promptArg{{Name: "namespace", Description: "pod namespace", Required: true}, {Name: "pod", Description: "pod name", Required: true}, {Name: "cluster", Description: "cluster, when the snapshot has more than one"}},
			text: func(a map[string]string) string {
				return fmt.Sprintf(`Find out why pod %s/%s is unhealthy.

1. get_object for the pod (cluster %q, resource pods). Read its status: phase, conditions, each container's state, lastState, exit code and restart count. Note its owner and the related objects.
2. Read its events. Then read the log streams it lists: the previous stream of a restarted container holds the crash, the current one the recovery. Use search_logs with pod:%s level:error for anything across containers, and log_context around the key lines.
3. list_findings and keep those naming this pod or its owner.
4. If it is behind a Service, waypoint or gateway, get_object those and check selectors, ports and policies. For an ambient or agentgateway pod, proxy_config shows its xDS state.
5. The timeline tool puts restarts, rollouts and error spikes in order; check whether a rollout or config change came first.

Reply with the cause, the evidence and the fix.`+evidenceRules, a["namespace"], a["pod"], a["cluster"], a["pod"])
			}},
		{Name: "explain_denial", Title: "Why was this call denied?",
			Description: "Explains why traffic from one workload to another was denied or failed: authorization policies, NetworkPolicy, waypoint and gateway routes, mTLS, and the access logs that show it.",
			Arguments:   []promptArg{{Name: "from", Description: "source as namespace/workload", Required: true}, {Name: "to", Description: "destination as namespace/service, host or URL", Required: true}},
			text: func(a map[string]string) string {
				return fmt.Sprintf(`Explain why %s cannot reach %s.

1. explain_connection from %q to %q. It evaluates the captured policies and says which rule decided.
2. Confirm it in the logs: search_logs for the destination in ztunnel, waypoint and agentgateway access logs (response flags, RBAC denials, 403, 503, connection reset), and log_context around the first denial.
3. get_object each policy it names and check selectors, principals, namespaces and ports against the source's service account. Check PeerAuthentication mode and whether both ends are enrolled in ambient.
4. traffic_map shows whether the path was seen at all.

Reply with the rule that decided, the evidence, and the smallest policy change that allows the call without widening access for anything else.`+evidenceRules, a["from"], a["to"], a["from"], a["to"])
			}},
		{Name: "security_review", Title: "Security review",
			Description: "Reviews the snapshot against security and operational checks and turns failing checks into a ranked list of fixes.",
			text: func(map[string]string) string {
				return `Review this snapshot's security posture.

1. Read the Scorecard (best_practices). Start from the priorities list when it is there, then every fail, then warn. For the agentic area, agent_reach shows what each agent, MCP server and gateway can reach and with which credentials.
2. For each failing check, confirm it: get_object the affected objects and check the check's claim is true of them. Drop anything that coverage says was not collected rather than calling it absent.
3. Group the fixes: what an attacker could do today, what widens the blast radius, and hygiene.

Reply with a ranked table (issue, affected objects, risk, fix YAML) and a short summary per area.` + evidenceRules
			},
			attach: func(map[string]string) []string { return []string{scheme + "scorecard", scheme + "coverage"} }},
		{Name: "agent_review", Title: "Review one agent",
			Description: "What one agent, MCP server or agent gateway can reach, which credentials it carries, what it actually called, and how to lock it down.",
			Arguments:   []promptArg{{Name: "agent", Description: "namespace/name of the agent, MCP server or gateway", Required: true}},
			text: func(a map[string]string) string {
				return fmt.Sprintf(`Review agent %s.

1. agent_reach with agent %q: network reach, reach it borrows through proxies and registries, write access, credentials, and the YAML to close each path.
2. agent_activity for the tools and models it called and the calls that were denied or failed.
3. get_object the agent and the policies, routes and backends in front of it. Check authentication, tool allowlists, passthrough tokens, guardrails and budgets.
4. Compare what it can do with what it did: every reachable path it never used is a candidate to close.

Reply with the paths to close first, the evidence, and the fix YAML.`+evidenceRules, a["agent"], a["agent"])
			}},
		{Name: "what_changed", Title: "What changed?",
			Description: "Compares this snapshot with the baseline bundle and ties the changes to the findings and the timeline.",
			text: func(map[string]string) string {
				return `Work out what changed and whether it caused the problem.

1. compare_baseline. If it has no data the desk was opened without --baseline: say so and fall back to the timeline tool (rollouts, config changes and restarts in order).
2. For each changed object that matters (routes, policies, gateways, deployments, images), get_object it and read the change.
3. Line the changes up with the timeline and with findings that appeared since the baseline.

Reply with the changes most likely to explain the problem, in time order, with evidence.` + evidenceRules
			}},
		{Name: "incident_timeline", Title: "Incident timeline",
			Description: "Builds a minute-by-minute account of an incident from events, restarts, rollouts, error spikes and log lines.",
			Arguments:   []promptArg{{Name: "since", Description: "start of the window, e.g. 2026-09-28T14:00:00Z or 30m"}},
			text: func(a map[string]string) string {
				w := "the whole capture"
				if a["since"] != "" {
					w = "everything since " + a["since"]
				}
				return fmt.Sprintf(`Build an incident timeline covering %s.

1. The timeline tool gives events, container exits, rollouts and error spikes in order.
2. For each spike or exit, search_logs in that minute (level:error, then the pod) and log_context around the first error, to find what failed first.
3. Follow request and trace ids across pods with search_logs.

Reply with a table (time, what happened, where, evidence) and mark the first failure and the trigger if the snapshot shows one.`+evidenceRules, w)
			},
			attach: func(map[string]string) []string { return []string{scheme + "timeline"} }},
	}
}

// getPrompt renders a prompt with its arguments and embedded resources.
func (s *Server) getPrompt(name string, args map[string]string) (map[string]any, error) {
	for _, p := range prompts() {
		if p.Name != name {
			continue
		}
		var missing []string
		for _, a := range p.Arguments {
			if a.Required && strings.TrimSpace(args[a.Name]) == "" {
				missing = append(missing, a.Name)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("prompt %s needs %s", name, strings.Join(missing, ", "))
		}
		msgs := []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": p.text(args)}}}
		if p.attach != nil {
			for _, u := range p.attach(args) {
				// A resource that cannot be read is left out; the prompt
				// still names the tool that fetches it.
				if c, err := s.readResource(u); err == nil {
					msgs = append(msgs, map[string]any{"role": "user", "content": map[string]any{"type": "resource", "resource": c}})
				}
			}
		}
		return map[string]any{"description": p.Title, "messages": msgs}, nil
	}
	return nil, fmt.Errorf("unknown prompt %s", name)
}
