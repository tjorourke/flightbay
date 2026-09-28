package reach

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func (e *engine) findings(w *workload, a Agent, reach []pair, all []*workload, declSvcs, declHosts map[string]bool) []Finding {
	c := e.c
	var out []Finding
	add := func(sev, title, detail, fix string) {
		out = append(out, Finding{Severity: sev, Title: title, Detail: detail, Fix: fix, Source: guide})
	}
	// What the agent needs: what it is configured to call, plus what it did call.
	var needed []*service
	for _, d := range reach {
		k := key(d.sv.ns, d.sv.name)
		for _, ed := range a.Direct {
			if ed.To == "svc:"+d.sv.id && (ed.Observed > 0 || declSvcs[k]) && !has(d.sv.classes, "dns") {
				needed = append(needed, d.sv)
			}
		}
	}
	egressFix := e.egressPolicy(w, needed, declHosts)

	if len(a.Identity.HostAccess) > 0 {
		add("critical", "Runs with host access",
			fmt.Sprintf("%s has %s. A process that can reach the host needs no network path to escalate.", w.name, strings.Join(a.Identity.HostAccess, ", ")),
			"spec:\n  hostNetwork: false\n  containers:\n  - securityContext: {privileged: false, allowPrivilegeEscalation: false}")
	}

	if a.Internet == Allowed && w.agentType == "agentgateway" {
		add("high", "Egress is not limited to the gateway's backends",
			fmt.Sprintf("%s needs the internet for its LLM providers and MCP upstreams, but nothing stops it reaching any other address. Allow only the backends it is configured for.", w.name),
			egressFix)
	} else if a.Internet == Allowed {
		add("critical", "Reaches the internet directly",
			fmt.Sprintf("Nothing limits where %s can connect outside the cluster. A prompt injection or a compromised tool can send data anywhere. Route model and tool calls through agentgateway and deny everything else.", w.name),
			egressFix)
	} else if a.Internet == Unknown {
		add("medium", "Internet access could not be determined",
			"NetworkPolicies were not captured, so whether this agent can reach the internet is unknown. Re-capture without excluding networkpolicies.", "")
	}

	fetchers := map[string]Path{}
	for _, p := range a.Paths {
		if p.Kind == "fetcher" && len(p.Hops) > 2 {
			if _, ok := fetchers[p.Hops[1]]; !ok {
				fetchers[p.Hops[1]] = p
			}
		}
	}
	for _, name := range sortedKeys(fetchers) {
		p := fetchers[name]
		sv := c.svcByKey[name]
		add("critical", "Inherits internet access through "+name,
			fmt.Sprintf("%s can call %s, which fetches or proxies URLs and can itself reach the internet: %s. An SSRF or proxy feature there gives the agent the internet, as the package registry did in the OpenAI and Hugging Face incident.", w.name, name, strings.Join(p.Hops, " → ")),
			e.readOnlyPolicy(w, sv)+"\n---\n# and give "+name+" an egress allowlist of the upstreams it really needs\n"+e.egressPolicyFor(sv))
	}

	// Write access to sensitive destinations, grouped.
	type target struct {
		sv    *service
		cls   string
		verbs string
		with  []string // other agents that can write to it as well
	}
	var writes []target
	for _, d := range reach {
		if d.h.verdict != Allowed || !canWrite(d.sv, d.h) {
			continue
		}
		t := target{sv: d.sv, verbs: "any method"}
		switch {
		case has(d.sv.classes, "fetcher"):
			t.cls = "fetches from elsewhere"
		case has(d.sv.classes, "credentials"):
			t.cls = "credential service"
		case has(d.sv.classes, "store"):
			t.cls = "data store"
		default:
			continue
		}
		if d.h.methods != nil {
			t.verbs = strings.Join(d.h.methods, ", ")
		}
		for _, o := range all {
			if o == w || o.agentType == "agentgateway" {
				continue
			}
			backend := false
			for _, b := range d.sv.backends {
				if b == o {
					backend = true
				}
			}
			if !backend && canWrite(d.sv, e.hop(o, d.sv)) {
				t.with = append(t.with, o.ns+"/"+o.name)
			}
		}
		writes = append(writes, t)
	}
	sort.SliceStable(writes, func(i, j int) bool {
		rank := map[string]int{"fetches from elsewhere": 0, "credential service": 1, "data store": 2}
		return rank[writes[i].cls] < rank[writes[j].cls]
	})
	if len(writes) > 0 {
		var lines []string
		for i, t := range writes {
			if i == 12 {
				lines = append(lines, fmt.Sprintf("and %d more", len(writes)-12))
				break
			}
			lines = append(lines, fmt.Sprintf("%s (%s, %s)", svcLabel(t.sv), t.cls, t.verbs))
		}
		first := writes[0]
		fix := e.readOnlyPolicy(w, first.sv)
		if first.cls == "data store" && !isHTTP(first.sv) {
			fix = e.callersPolicy(w, first.sv)
		}
		title := "Can write to " + svcLabel(first.sv)
		if len(writes) > 1 {
			title = fmt.Sprintf("Can write to %d sensitive services", len(writes))
		}
		add("high", title,
			fmt.Sprintf("The network lets %s change data on: %s. Only the application's own authentication stands in the way. Write access nobody audited is how the incident's agents turned a package registry into a message board. Allow only the methods and callers each one needs (the fix shows the first; repeat for the others).", w.name, strings.Join(lines, "; ")), fix)
	}
	var shared []string
	for _, t := range writes {
		if len(t.with) > 0 {
			shared = append(shared, fmt.Sprintf("%s (with %d other agent%s: %s)", svcLabel(t.sv), len(t.with), plural(len(t.with)), strings.Join(firstN(t.with, 3), ", ")))
		}
	}
	if len(shared) > 0 {
		add("high", fmt.Sprintf("Shares %d writable store%s with other agents", len(shared), plural(len(shared))),
			"Anything one agent can write and another can read is a channel between them, whether intended or not: "+strings.Join(firstN(shared, 8), "; ")+".",
			"# give each agent its own store, database or prefix, or make the shared one read-only for agents\n"+e.readOnlyPolicy(w, writes[0].sv))
	}
	if len(a.Identity.SharedVolumes) > 0 {
		add("high", "Mounts a volume other agents also mount", strings.Join(a.Identity.SharedVolumes, "; "), "# one PersistentVolumeClaim per agent, or mount shared data read-only\nvolumeMounts:\n- {name: shared, mountPath: /data, readOnly: true}")
	}

	total := 0
	for _, sv := range c.services {
		if !has(sv.classes, "dns") && !has(sv.classes, "apiserver") {
			total++
		}
	}
	if a.Counts.Direct > len(needed)+2 && a.Counts.Direct*2 >= total && total > 4 {
		add("high", fmt.Sprintf("Can reach %d of %d Services", a.Counts.Direct, total),
			fmt.Sprintf("%s is configured to call %d of them and called %d in the capture. Everything else is reach it does not need, and every one of those services lends it whatever it can reach in turn (%d Services in total).", w.name, len(declSvcs), a.Counts.Observed, a.Counts.Closure),
			egressFix)
	}
	if a.Counts.NeverUsed > 0 && a.Observed {
		list := a.NeverUsed
		if len(list) > 10 {
			list = append(append([]string{}, list[:10]...), fmt.Sprintf("and %d more", len(a.NeverUsed)-10))
		}
		add("medium", fmt.Sprintf("%d allowed destinations were never used", a.Counts.NeverUsed),
			"Allowed, not configured, and not called while the capture's access logs were recorded: "+strings.Join(list, ", ")+". These are the cheapest paths to close.", egressFix)
	}

	switch {
	case a.Metadata == Allowed && (c.platform == "eks" || c.platform == "gke" || c.platform == "aks"):
		add("high", "Can reach the cloud metadata endpoint",
			"169.254.169.254 hands out the node's cloud credentials. Block it twice, with a NetworkPolicy and at the cloud layer (IMDSv2 with hop limit 1, or Workload Identity).",
			metadataPolicy(w))
	case a.Metadata == Allowed && c.platform != "kind":
		add("medium", "Would reach a cloud metadata endpoint",
			"No NetworkPolicy blocks 169.254.169.254 for this agent. On a cloud node that hands out node credentials.", metadataPolicy(w))
	}

	if !w.inMesh && !w.gateway {
		add("high", "Not in the mesh",
			fmt.Sprintf("%s has no mesh identity: its traffic is not encrypted by ztunnel and no AuthorizationPolicy can allow or deny it by name.", w.name),
			fmt.Sprintf("kubectl label namespace %s istio.io/dataplane-mode=ambient", w.ns))
	}
	var sameSA []string
	for _, o := range all {
		if o != w && o.ns == w.ns && o.sa == w.sa && o.agentType != "agentgateway" {
			sameSA = append(sameSA, o.name)
		}
	}
	if len(sameSA) > 0 {
		add("high", "Shares its identity with other agents",
			fmt.Sprintf("%s runs as %s, like %s. Every policy that allows one of them allows all of them, and anything one writes the others can read as the same identity. Give each agent its own service account.", w.name, a.Identity.ServiceAccount, strings.Join(sameSA, ", ")),
			fmt.Sprintf("apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: %s, namespace: %s}\nautomountServiceAccountToken: false\n---\n# and in the %s pod template:\nspec:\n  serviceAccountName: %s", w.name, w.ns, w.name, w.name))
	}
	for _, dg := range a.Identity.Dangerous {
		add("high", "Service account can "+strings.SplitN(dg, ": ", 2)[1],
			fmt.Sprintf("%s runs as %s, which can %s. A file-read bug in the agent becomes that permission.", w.name, a.Identity.ServiceAccount, dg), "# remove the rule, or split it into a separate service account the agent does not run as")
	}
	if a.Identity.TokenMounted && len(a.Identity.RBAC) == 0 && a.Identity.RBACComplete {
		add("medium", "Mounts an API token it has no use for",
			fmt.Sprintf("%s gets a Kubernetes API token automatically but its service account is granted nothing. Stop mounting it.", w.name),
			fmt.Sprintf("apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: %s, namespace: %s}\nautomountServiceAccountToken: false", w.sa, w.ns))
	}
	if a.Identity.CloudRole != "" {
		add("medium", "Holds a cloud identity",
			fmt.Sprintf("%s: a stolen service account token is also a cloud credential. Scope the role to what the agent needs.", a.Identity.CloudRole), "")
	}
	if len(a.Identity.Secrets) > 0 && (w.agentType == "kagent-agent" || w.agentType == "possible-agent") {
		add("low", fmt.Sprintf("Can read %d Secret(s)", len(a.Identity.Secrets)),
			"Mounted or in its environment: "+strings.Join(a.Identity.Secrets, ", ")+". Keep only what the agent uses; prefer short-lived tokens from agentgateway (token exchange) to static keys.", "")
	}
	if a.Identity.Sandbox == "" && (w.agentType == "kagent-agent" || w.agentType == "possible-agent") {
		add("low", "Shares the node kernel",
			"The agent runs in an ordinary container. A kernel exploit is one step to root on the node, as in the incident. Run agents that execute code under a sandbox runtime.",
			"spec:\n  runtimeClassName: gvisor")
	}
	sort.SliceStable(out, func(i, j int) bool { return sevRank[out[i].Severity] < sevRank[out[j].Severity] })
	return out
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}

func sortedKeys[T any](mp map[string]T) []string {
	var out []string
	for k := range mp {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isHTTP(sv *service) bool {
	for _, p := range sv.ports {
		n := strings.ToLower(p.name + " " + p.appProtocol)
		if strings.Contains(n, "http") || strings.Contains(n, "grpc") || p.port == 80 || p.port == 8080 || p.port == 443 || p.port == 8081 {
			return true
		}
	}
	return false
}

// ---------- fix generation ----------

func podSelectorYAML(w *workload, indent string) string {
	sel := strMap(path(w.obj.Obj, "spec", "selector", "matchLabels"))
	if len(sel) == 0 {
		for _, k := range []string{"app", "app.kubernetes.io/name", "app.kubernetes.io/instance"} {
			if v := w.labels[k]; v != "" {
				sel = map[string]string{k: v}
				break
			}
		}
	}
	if len(sel) == 0 {
		return indent + "podSelector: {}   # narrow this to the agent's pods"
	}
	var parts []string
	for _, k := range sortedKeys(sel) {
		parts = append(parts, k+": "+yamlStr(sel[k]))
	}
	return indent + "podSelector:\n" + indent + "  matchLabels: {" + strings.Join(parts, ", ") + "}"
}

func yamlStr(v string) string {
	if v == "" || strings.ContainsAny(v, ":{}[],&*#?|<>=!%@`'\" ") {
		return strconv.Quote(v)
	}
	return v
}

func (e *engine) egressPolicy(w *workload, needed []*service, hosts map[string]bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: %s-egress, namespace: %s}\nspec:\n", w.name, w.ns)
	b.WriteString(podSelectorYAML(w, "  ") + "\n")
	b.WriteString("  policyTypes: [Egress]\n  egress:\n  - to:   # cluster DNS\n    - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: kube-system}}\n      podSelector: {matchLabels: {k8s-app: kube-dns}}\n    ports: [{port: 53, protocol: UDP}, {port: 53, protocol: TCP}]\n")
	seen := map[string]bool{}
	for _, sv := range needed {
		if seen[sv.id] || len(sv.selector) == 0 {
			continue
		}
		seen[sv.id] = true
		var sel []string
		for _, k := range sortedKeys(sv.selector) {
			sel = append(sel, k+": "+yamlStr(sv.selector[k]))
		}
		fmt.Fprintf(&b, "  - to:   # %s\n    - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: %s}}\n      podSelector: {matchLabels: {%s}}\n", svcLabel(sv), sv.ns, strings.Join(sel, ", "))
		var ports []string
		for _, p := range sv.ports {
			ports = append(ports, "{port: "+portYAML(p.target)+"}")
		}
		if w.inMesh && len(sv.backends) > 0 && sv.backends[0].inMesh {
			ports = append(ports, "{port: 15008}   # ambient HBONE")
		}
		if len(ports) > 0 {
			fmt.Fprintf(&b, "    ports: [%s]\n", strings.Join(ports, ", "))
		}
	}
	if len(needed) == 0 {
		b.WriteString("  # add one rule per Service the agent needs; nothing it called or declared was found\n")
	}
	if len(hosts) > 0 {
		fmt.Fprintf(&b, "# External hosts it is configured to call (%s): send them through agentgateway\n# and allow only the gateway here, rather than opening 0.0.0.0/0.\n", strings.Join(sortedKeys(hosts), ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func portYAML(p string) string {
	if _, err := strconv.Atoi(p); err == nil {
		return p
	}
	return strconv.Quote(p)
}

// egressPolicyFor is an egress allowlist skeleton for a fetcher's own pods.
func (e *engine) egressPolicyFor(sv *service) string {
	if sv == nil || len(sv.backends) == 0 {
		return "# (no captured pods behind the service)"
	}
	b := sv.backends[0]
	return fmt.Sprintf("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: %s-egress, namespace: %s}\nspec:\n%s\n  policyTypes: [Egress]\n  egress:\n  - to: [{ipBlock: {cidr: <upstream CIDR>}}]   # only the upstream indexes it proxies\n    ports: [{port: 443}]", b.name, b.ns, podSelectorYAML(b, "  "))
}

func (e *engine) principal(w *workload) string { return e.td + "/" + w.principal() }

// readOnlyPolicy lets the agent use only GET and HEAD on a service, enforced
// by a waypoint (L7 rules need one in ambient).
func (e *engine) readOnlyPolicy(w *workload, sv *service) string {
	if sv == nil {
		return ""
	}
	var b strings.Builder
	if sv.waypoint == "" {
		fmt.Fprintf(&b, "# %s has no waypoint, and method rules need one:\n#   istioctl waypoint apply -n %s\n#   kubectl label svc %s -n %s istio.io/use-waypoint=waypoint\n", svcLabel(sv), sv.ns, sv.name, sv.ns)
	}
	fmt.Fprintf(&b, "apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: %s-read-only-for-%s, namespace: %s}\nspec:\n  targetRefs:\n  - {kind: Service, group: \"\", name: %s}\n  action: ALLOW\n  rules:\n  - from:\n    - source: {principals: [%q]}\n    to:\n    - operation:\n        methods: [GET, HEAD]\n        # paths: [\"/api/pypi/*\", \"/api/npm/*\"]   # narrow to what it reads\n# every other caller and method is now denied at the waypoint: PUT, POST, MKCOL, admin APIs",
		sv.name, w.name, sv.ns, sv.name, e.principal(w))
	return b.String()
}

// callersPolicy limits a non-HTTP store to named callers (L4, at ztunnel).
func (e *engine) callersPolicy(w *workload, sv *service) string {
	sel := ""
	for _, k := range sortedKeys(sv.selector) {
		sel += k + ": " + yamlStr(sv.selector[k]) + ", "
	}
	return fmt.Sprintf("apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: %s-callers, namespace: %s}\nspec:\n  selector:\n    matchLabels: {%s}\n  action: ALLOW\n  rules:\n  - from:\n    - source:\n        principals:\n        - %q   # remove if the agent should not use it\n        # - <the workloads that own this store>",
		sv.name, sv.ns, strings.TrimSuffix(sel, ", "), e.principal(w))
}

func metadataPolicy(w *workload) string {
	return fmt.Sprintf("apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: deny-metadata-endpoint, namespace: %s}\nspec:\n  podSelector: {}\n  policyTypes: [Egress]\n  egress:\n  - to:\n    - ipBlock:\n        cidr: 0.0.0.0/0\n        except: [169.254.169.254/32, 169.254.170.2/32]", w.ns)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), fmt.Sprintf("and %d more", len(xs)-n))
}
