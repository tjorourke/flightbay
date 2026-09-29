package posture

import (
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/features"
	"github.com/tjorourke/flightbay/internal/reach"
	"github.com/tjorourke/flightbay/internal/solodocs"
	"github.com/tjorourke/flightbay/internal/store"
)

// reachChecks turns agent reach into Scorecard checks (Flightbay Enterprise).
// The Agent reach page has the per-agent graph and the generated fixes.
func reachChecks(ws *store.Workspace) []Check {
	if !features.Enabled(features.Reach) {
		return nil
	}
	r := reach.Evaluate(ws)
	var agents []reach.Agent
	for _, a := range r.Agents {
		if a.Type != "agentgateway" {
			agents = append(agents, a)
		}
	}
	if len(agents) == 0 {
		return nil
	}
	item := func(a reach.Agent, st Status, detail string) Item {
		it := Item{Cluster: a.Cluster, Namespace: a.Namespace, Name: a.Name, Kind: a.Kind, Status: st, Detail: detail}
		if a.Ref != nil {
			it.Group, it.Resource, it.Kind = a.Ref.Group, a.Ref.Resource, a.Ref.Kind
		}
		return it
	}
	hasFinding := func(a reach.Agent, prefix string) (reach.Finding, bool) {
		for _, f := range a.Findings {
			if strings.HasPrefix(f.Title, prefix) {
				return f, true
			}
		}
		return reach.Finding{}, false
	}
	const see = "\n# The Agent reach page (#reach) has this agent's generated fix, with its names filled in."
	var out []Check
	mk := func(id, cat, sev, title, why, fix, docs string, eval func(c *Check)) {
		c := Check{ID: id, Area: "agentic", Category: cat, Severity: sev, Title: title, Why: why, Fix: fix + see, Source: kbBase + "zero-trust-agents-kubernetes/", Docs: docs}
		eval(&c)
		out = append(out, c)
	}
	mk("agentic.agent-internet-reach", "Isolation", "critical", "Agents cannot reach the internet directly",
		"An agent with open egress can send anything a prompt injection or a compromised tool gives it to any address.",
		"apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: <agent>-egress, namespace: <ns>}\nspec:\n  podSelector: {matchLabels: {app: <agent>}}\n  policyTypes: [Egress]\n  egress:\n  - to: [{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: kube-system}}, podSelector: {matchLabels: {k8s-app: kube-dns}}}]\n    ports: [{port: 53, protocol: UDP}]\n  # plus one rule per Service it needs; external calls go through agentgateway",
		solodocs.AgentgatewayRoot+"integrations/agentic-mesh/egress/",
		func(c *Check) {
			for _, a := range agents {
				switch a.Internet {
				case reach.Allowed:
					c.Items = append(c.Items, item(a, Fail, "no egress control: "+a.Summary))
				case reach.Denied:
					c.Items = append(c.Items, item(a, Pass, "internet egress blocked"))
				}
			}
		})
	mk("agentic.agent-transitive-fetcher", "Isolation", "critical", "Agents cannot access the internet through a registry or proxy",
		"A reachable registry, proxy or scraper may fetch external URLs for the agent. Restrict the service's egress and check whether it accepts agent-supplied URLs.",
		"# at the registry's waypoint: allow the agent GET and HEAD only\nspec:\n  targetRefs: [{kind: Service, group: \"\", name: <registry>}]\n  action: ALLOW\n  rules:\n  - from: [{source: {principals: [\"<td>/ns/<ns>/sa/<agent>\"]}}]\n    to: [{operation: {methods: [GET, HEAD]}}]\n# and give the registry its own egress allowlist",
		solodocs.Istio+"security/authorization/waypoint-authz/",
		func(c *Check) {
			for _, a := range agents {
				if f, ok := hasFinding(a, "Inherits internet access"); ok {
					c.Items = append(c.Items, item(a, Fail, f.Title+": "+pathOf(a, "fetcher")))
				} else if f, ok := hasFinding(a, "Can reach an external registry"); ok {
					c.Items = append(c.Items, item(a, Fail, f.Title+": "+pathOf(a, "registry")))
				} else if a.Internet != reach.Unknown {
					c.Items = append(c.Items, item(a, Pass, "no fetcher on its call list reaches the internet"))
				}
			}
		})
	mk("agentic.agent-write-sensitive", "Authorization", "high", "Agents cannot write to data stores, registries or credential services they only need to read",
		"Write permissions allow an agent to change shared data. Restrict each destination to the methods and callers it requires.",
		"# per destination: an AuthorizationPolicy at its waypoint with methods: [GET, HEAD] for the agent's principal",
		solodocs.Istio+"security/authorization/waypoint-authz/",
		func(c *Check) {
			for _, a := range agents {
				if f, ok := hasFinding(a, "Can write to"); ok {
					c.Items = append(c.Items, item(a, Fail, f.Title))
				} else {
					c.Items = append(c.Items, item(a, Pass, "no write access to sensitive services"))
				}
			}
		})
	mk("agentic.agent-no-shared-channel", "Isolation", "high", "Agents do not share an identity, a volume or a writable store",
		"Anything one agent can write and another read is a channel between them; a shared service account makes every allow rule apply to all of them.",
		"# one ServiceAccount, PersistentVolumeClaim and store (or prefix) per agent",
		solodocs.Istio+"security/workload-identity/",
		func(c *Check) {
			for _, a := range agents {
				var hits []string
				for _, p := range []string{"Shares its identity", "Mounts a volume other agents", "Shares "} {
					if f, ok := hasFinding(a, p); ok && !contains(hits, f.Title) {
						hits = append(hits, f.Title)
					}
				}
				if len(hits) > 0 {
					c.Items = append(c.Items, item(a, Fail, strings.Join(hits, "; ")))
				} else {
					c.Items = append(c.Items, item(a, Pass, "nothing shared with other agents"))
				}
			}
		})
	mk("agentic.agent-least-reach", "Isolation", "medium", "Agents reach only what they use",
		"Reachable services can provide indirect access to further destinations. Review access against the agent's configuration and observed calls.",
		"# default-deny egress for the agent namespace, then one rule per destination it uses",
		solodocs.Istio+"security/authorization/ztunnel-authz/",
		func(c *Check) {
			for _, a := range agents {
				d := fmt.Sprintf("reaches %d of %d Services directly, %d in total", a.Counts.Direct, a.Counts.Services, a.Counts.Closure)
				switch {
				case a.Counts.Services > 4 && a.Counts.Direct*2 >= a.Counts.Services:
					c.Items = append(c.Items, item(a, Fail, d))
				case a.Counts.NeverUsed > 0 && a.Observed:
					c.Items = append(c.Items, item(a, Warn, fmt.Sprintf("%s; %d allowed but not observed in this capture", d, a.Counts.NeverUsed)))
				default:
					c.Items = append(c.Items, item(a, Pass, d))
				}
			}
		})
	for i := range out {
		if len(out[i].Items) == 0 {
			out[i].Status, out[i].Reason = NA, "reach could not be evaluated (policy types not captured)"
		}
	}
	return out
}

func pathOf(a reach.Agent, kind string) string {
	for _, p := range a.Paths {
		if p.Kind == kind {
			return strings.Join(p.Hops, " → ")
		}
	}
	return ""
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
