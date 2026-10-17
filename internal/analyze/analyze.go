// Package analyze turns captured evidence into findings. Findings are signals
// to investigate, each linked to the objects and log lines behind it. Rules
// based on absence only run once the relevant collection is complete.
package analyze

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/logs"
	"github.com/tjorourke/flightbay/internal/store"
)

// Severities.
const (
	Critical = "critical"
	Warning  = "warning"
	Info     = "info"
)

// Ref points at a captured object.
type Ref struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Finding is one analyzer result.
type Finding struct {
	ID          string   `json:"id"`
	Rule        string   `json:"rule"`
	Severity    string   `json:"severity"`
	Title       string   `json:"title"`
	Detail      string   `json:"detail"`
	Cluster     string   `json:"cluster"`
	Object      *Ref     `json:"object,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	LogQuery    string   `json:"logQuery,omitempty"`
	NextStep    string   `json:"nextStep,omitempty"`
	Docs        string   `json:"docs,omitempty"` // product documentation for the next step
	Count       int      `json:"count,omitempty"`
	Provisional bool     `json:"provisional"`
}

func ref(o *store.Object) *Ref {
	return &Ref{Cluster: o.Cluster, Group: o.Group, Resource: o.Resource, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
}

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }
func s(v any) string         { x, _ := v.(string); return x }

func num(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int64:
		return int(t)
	case int:
		return t
	}
	return 0
}

// Run evaluates every rule against the workspace.
func Run(ws *store.Workspace) []Finding {
	st := ws.State()
	provisional := st.Phase != store.PhaseComplete || !st.Verified
	if ws.IsLive() {
		// A live mirror has no bundle checksum to verify. PhaseLive means its
		// initial lists have finished; individual coverage still gates absence rules.
		provisional = st.Phase != store.PhaseLive
	}
	var out []Finding
	for _, cluster := range ws.Clusters() {
		out = append(out, workloads(ws, cluster)...)
		out = append(out, gatewayRules(ws, cluster)...)
		out = append(out, policies(ws, cluster)...)
		out = append(out, mesh(ws, cluster)...)
		out = append(out, certificates(ws, cluster)...)
		out = append(out, xds(ws, cluster)...)
		out = append(out, waypoints(ws, cluster)...)
		out = append(out, cni(ws, cluster)...)
		out = append(out, agentgatewaySkew(ws, cluster)...)
		out = append(out, gatewayAPI(ws, cluster)...)
	}
	out = append(out, signatures(ws)...)
	if h := ws.Header(); h.Declared() {
		repo := "the repository"
		if h.Repo != nil && h.Repo.Name != "" {
			repo = h.Repo.Name
		}
		out = append(out, Finding{Rule: "scan.declared-only", Severity: Info,
			Title:  "Declared state only (repo scan) of " + repo,
			Detail: "This bundle was built from a code repository, so the rules on pod status, restarts, image pulls, scheduling, logs, proxy and certificate diagnostics and policy conditions do not apply. They run on a capture of the cluster."})
	}
	for _, c := range ws.Coverage() {
		if c.State == "failed" {
			target := c.Resource
			if c.Type == "logs" {
				target = fmt.Sprintf("logs %s/%s/%s (%s)", c.Namespace, c.Pod, c.Container, c.Stream)
			}
			if c.Type == "input" {
				out = append(out, Finding{Rule: "scan.render-failed", Severity: Warning, Cluster: c.Cluster,
					Title:    fmt.Sprintf("%s %s could not be rendered", c.Renderer, c.Source),
					Detail:   c.Reason + ". Objects it declares are missing from this scan.",
					Evidence: []string{"coverage " + c.ID}})
				continue
			}
			out = append(out, Finding{Rule: "collection.gap", Severity: Info, Cluster: c.Cluster,
				Title:    fmt.Sprintf("%s was not collected on %s", target, c.Cluster),
				Detail:   c.Reason + ". Missing data is a collection gap, not proof that the object or log line was absent.",
				Evidence: []string{"coverage " + c.ID}})
		}
	}
	rank := map[string]int{Critical: 0, Warning: 1, Info: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	for i := range out {
		out[i].ID = fmt.Sprintf("f%d", i+1)
		out[i].Provisional = provisional
		if out[i].Docs == "" {
			out[i].Docs = DocsFor(out[i])
		}
	}
	return out
}

func workloads(ws *store.Workspace, cluster string) []Finding {
	var out []Finding
	snap := snapshotTime(ws)
	for _, o := range ws.List(cluster, "", "pods", "", nil) {
		status := m(o.Obj["status"])
		for _, c := range append(l(status["containerStatuses"]), l(status["initContainerStatuses"])...) {
			cm := m(c)
			name := s(cm["name"])
			waiting := m(m(cm["state"])["waiting"])
			last := m(m(cm["lastState"])["terminated"])
			restarts := num(cm["restartCount"])
			// The kubelet often garbage-collects the previous container's log
			// during a crash loop, leaving the crash output in the current stream.
			q := fmt.Sprintf("pod:%s container:%s", o.Name, name)
			next := "Read the container's logs to see why it exited."
			if ws.HasStream(cluster, o.Namespace, o.Name, name, "previous") {
				q += " stream:previous"
				next = "Read the previous container's logs to see why it exited."
			}
			term := m(m(cm["state"])["terminated"])
			// A capture can land between a crash and the back-off, when the
			// state is terminated rather than waiting: repeated non-zero exits
			// are the same crash loop.
			failedNow := term != nil && num(term["exitCode"]) != 0
			failedBefore := last != nil && num(last["exitCode"]) != 0
			exiting := failedNow && (restarts >= 3 || (restarts >= 1 && failedBefore))
			// Or caught in the few seconds it runs between crashes: several
			// restarts, a non-zero last exit, and started just before capture.
			justRestarted := false
			if run := m(m(cm["state"])["running"]); run != nil && restarts >= 3 && failedBefore {
				if t, err := time.Parse(time.RFC3339, s(run["startedAt"])); err == nil && snap.Sub(t) < 5*time.Minute && snap.Sub(t) > -time.Minute {
					justRestarted = true
				}
			}
			switch reason := s(waiting["reason"]); {
			case reason == "CrashLoopBackOff" || exiting || justRestarted:
				ev := "state.waiting.reason: CrashLoopBackOff"
				detail := fmt.Sprintf("Container %s has restarted %d times and is in CrashLoopBackOff.", name, restarts)
				if justRestarted {
					ev = fmt.Sprintf("state.running since %s, last exit code %d", s(m(m(cm["state"])["running"])["startedAt"]), num(last["exitCode"]))
					detail = fmt.Sprintf("Container %s has restarted %d times and had just started again when the snapshot was taken.", name, restarts)
				} else if reason != "CrashLoopBackOff" {
					ev = fmt.Sprintf("state.terminated: %s (exit code %d)", s(term["reason"]), num(term["exitCode"]))
					detail = fmt.Sprintf("Container %s has restarted %d times and had just exited again when the snapshot was taken.", name, restarts)
				}
				if lr := s(last["reason"]); lr != "" {
					detail += fmt.Sprintf(" Last termination: %s (exit code %d).", lr, num(last["exitCode"]))
				}
				out = append(out, Finding{Rule: "workload.crashloop", Severity: Critical, Cluster: cluster, Object: ref(o),
					Title: fmt.Sprintf("%s/%s is crash looping", o.Namespace, o.Name), Detail: detail, LogQuery: q,
					NextStep: next,
					Evidence: []string{fmt.Sprintf("status.containerStatuses[%s].restartCount: %d", name, restarts), ev}})
			case reason == "ImagePullBackOff" || reason == "ErrImagePull":
				out = append(out, Finding{Rule: "workload.image-pull", Severity: Warning, Cluster: cluster, Object: ref(o),
					Title: fmt.Sprintf("%s/%s cannot pull its image", o.Namespace, o.Name), Detail: s(waiting["message"]),
					Evidence: []string{"state.waiting.reason: " + reason}, NextStep: "Check the image reference, registry access and pull secrets."})
			case restarts >= 5 && s(last["reason"]) != "OOMKilled":
				out = append(out, Finding{Rule: "workload.restarts", Severity: Warning, Cluster: cluster, Object: ref(o),
					Title: fmt.Sprintf("%s/%s container %s restarted %d times", o.Namespace, o.Name, name, restarts), LogQuery: q})
			}
			if s(last["reason"]) == "OOMKilled" {
				out = append(out, Finding{Rule: "workload.oomkilled", Severity: Warning, Cluster: cluster, Object: ref(o),
					Title:  fmt.Sprintf("%s/%s container %s was OOMKilled", o.Namespace, o.Name, name),
					Detail: "The last container instance exceeded its memory limit.", Evidence: []string{"lastState.terminated.reason: OOMKilled"},
					NextStep: "Compare the memory limit with actual usage, and look for a leak in the previous logs.", LogQuery: q})
			}
		}
		if s(status["phase"]) == "Pending" {
			for _, c := range l(status["conditions"]) {
				cm := m(c)
				if s(cm["type"]) == "PodScheduled" && s(cm["status"]) == "False" {
					out = append(out, Finding{Rule: "workload.unschedulable", Severity: Warning, Cluster: cluster, Object: ref(o),
						Title: fmt.Sprintf("%s/%s cannot be scheduled", o.Namespace, o.Name), Detail: s(cm["message"]),
						Evidence: []string{"condition PodScheduled=False reason " + s(cm["reason"])}, NextStep: "Compare the pod's requests with node capacity."})
				}
			}
		}
	}
	return out
}

var routeKinds = []struct{ group, resource string }{
	{"gateway.networking.k8s.io", "httproutes"}, {"gateway.networking.k8s.io", "grpcroutes"},
	{"gateway.networking.k8s.io", "tcproutes"}, {"gateway.networking.k8s.io", "tlsroutes"},
}

func gatewayRules(ws *store.Workspace, cluster string) []Finding {
	var out []Finding
	svcState := ws.CollectionState(cluster, "", "services", "Service", true)
	for _, rk := range routeKinds {
		for _, o := range ws.List(cluster, rk.group, rk.resource, "", nil) {
			for _, p := range l(m(o.Obj["status"])["parents"]) {
				for _, c := range l(m(p)["conditions"]) {
					cm := m(c)
					if (s(cm["type"]) == "Accepted" || s(cm["type"]) == "ResolvedRefs") && s(cm["status"]) == "False" {
						out = append(out, Finding{Rule: "gateway.route-condition", Severity: Warning, Cluster: cluster, Object: ref(o),
							Title:    fmt.Sprintf("%s %s/%s: %s=False", o.Kind, o.Namespace, o.Name, s(cm["type"])),
							Detail:   strings.TrimSpace(s(cm["reason"]) + ": " + s(cm["message"])),
							Evidence: []string{fmt.Sprintf("status.parents[].conditions %s=False reason %s", s(cm["type"]), s(cm["reason"]))}})
					}
				}
			}
			if svcState.State != store.StateComplete {
				continue // absence cannot be judged from incomplete or excluded coverage
			}
			for _, rule := range l(m(o.Obj["spec"])["rules"]) {
				for _, b := range l(m(rule)["backendRefs"]) {
					bm := m(b)
					if k := s(bm["kind"]); k != "" && k != "Service" {
						continue
					}
					if g := s(bm["group"]); g != "" {
						continue
					}
					ns := s(bm["namespace"])
					if ns == "" {
						ns = o.Namespace
					}
					name := s(bm["name"])
					if name != "" && ws.Get(cluster, "", "services", ns, name) == nil {
						out = append(out, Finding{Rule: "gateway.unresolved-backend", Severity: Warning, Cluster: cluster, Object: ref(o),
							Title:    fmt.Sprintf("%s %s/%s references missing Service %s", o.Kind, o.Namespace, o.Name, name),
							Detail:   missingServiceDetail(ws, ns, name, cluster),
							NextStep: "Compare with other clusters, or check whether the backend was renamed.",
							Evidence: []string{fmt.Sprintf("spec.rules[].backendRefs[].name: %s", name)}})
					}
				}
			}
		}
	}
	for _, o := range ws.List(cluster, "gateway.networking.k8s.io", "gateways", "", nil) {
		for _, c := range l(m(o.Obj["status"])["conditions"]) {
			cm := m(c)
			if (s(cm["type"]) == "Programmed" || s(cm["type"]) == "Accepted") && s(cm["status"]) == "False" {
				out = append(out, Finding{Rule: "gateway.not-programmed", Severity: Warning, Cluster: cluster, Object: ref(o),
					Title:  fmt.Sprintf("Gateway %s/%s: %s=False", o.Namespace, o.Name, s(cm["type"])),
					Detail: strings.TrimSpace(s(cm["reason"]) + " " + s(cm["message"]))})
			}
		}
	}
	return out
}

func missingServiceDetail(ws *store.Workspace, ns, name, cluster string) string {
	if ws.Header().Declared() {
		return fmt.Sprintf("No Service %s/%s is declared in the scanned paths. It may come from a chart or repository that was not scanned.", ns, name)
	}
	return fmt.Sprintf("No Service %s/%s was captured, and the Service collection for %s completed.", ns, name, cluster)
}

func policies(ws *store.Workspace, cluster string) []Finding {
	var out []Finding
	for _, r := range ws.Resources(cluster) {
		if !(strings.HasSuffix(r.Group, "agentgateway.dev") || strings.HasSuffix(r.Group, "kgateway.dev") || strings.HasSuffix(r.Kind, "Policy")) {
			continue
		}
		for _, o := range ws.List(cluster, r.Group, r.Resource, "", nil) {
			status := m(o.Obj["status"])
			conds := l(status["conditions"])
			for _, a := range l(status["ancestors"]) {
				conds = append(conds, l(m(a)["conditions"])...)
			}
			for _, c := range conds {
				cm := m(c)
				if s(cm["type"]) == "Accepted" && s(cm["status"]) == "False" {
					out = append(out, Finding{Rule: "policy.not-accepted", Severity: Warning, Cluster: cluster, Object: ref(o),
						Title:  fmt.Sprintf("%s %s/%s is not Accepted", o.Kind, o.Namespace, o.Name),
						Detail: strings.TrimSpace(s(cm["reason"]) + " " + s(cm["message"])), NextStep: "Check the policy's targetRefs point at an existing object."})
					break
				}
			}
		}
	}
	return out
}

func tag(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i >= 0 && !strings.Contains(image[i:], "/") {
		return image[i+1:]
	}
	return ""
}

func minor(t string) string {
	parts := strings.SplitN(strings.TrimPrefix(t, "v"), ".", 3)
	if len(parts) < 2 {
		return t
	}
	return parts[0] + "." + parts[1]
}

func mesh(ws *store.Workspace, cluster string) []Finding {
	var out []Finding
	versions := map[string]string{}
	var ztunnels int
	for _, o := range ws.List(cluster, "", "pods", "", nil) {
		app := o.Labels()["app"]
		if app == "ztunnel" {
			ztunnels++
		}
		if app != "istiod" && app != "ztunnel" {
			continue
		}
		for _, c := range l(m(o.Obj["spec"])["containers"]) {
			if t := tag(s(m(c)["image"])); t != "" {
				versions[app] = t
			}
		}
	}
	if a, b := versions["istiod"], versions["ztunnel"]; a != "" && b != "" && minor(a) != minor(b) {
		out = append(out, Finding{Rule: "mesh.version-skew", Severity: Warning, Cluster: cluster,
			Title:    fmt.Sprintf("istiod %s and ztunnel %s differ on %s", a, b, cluster),
			Detail:   "Control plane and ztunnel minor versions differ. Check this is a supported skew during an upgrade.",
			Evidence: []string{"istiod image tag " + a, "ztunnel image tag " + b}})
	}
	pol := ws.Policy()
	podsDone := ws.CollectionState(cluster, "", "pods", "Pod", true).State == store.StateComplete
	if podsDone && ztunnels == 0 && (pol == nil || pol.NamespaceAllowed("istio-system")) {
		for _, n := range ws.List(cluster, "", "namespaces", "", nil) {
			if n.Labels()["istio.io/dataplane-mode"] == "ambient" {
				out = append(out, Finding{Rule: "mesh.ambient-no-ztunnel", Severity: Critical, Cluster: cluster, Object: ref(n),
					Title:  fmt.Sprintf("Namespace %s is labelled for ambient but no ztunnel pod was captured", n.Name),
					Detail: "Workloads in this namespace will not be captured by the mesh without a ztunnel on their node."})
			}
		}
	}
	return out
}

func signatures(ws *store.Workspace) []Finding {
	type key struct{ cluster, ns, container, sig string }
	groups := map[key][]*logs.Record{}
	var order []key
	// Grouping by signature needs the message text, so it samples up to
	// 200,000 error lines rather than holding every line in memory.
	errs, _ := ws.FindLogs("level:error", 200000)
	for _, r := range errs {
		k := key{r.Cluster, r.Namespace, r.Container, logs.Signature(r.Msg)}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []Finding
	for _, k := range order {
		rs := groups[k]
		if len(rs) < 3 {
			continue
		}
		words := strings.Fields(rs[0].Msg)
		if len(words) > 4 {
			words = words[:4]
		}
		phrase := strings.ReplaceAll(strings.Join(words, " "), `"`, "")
		pods := map[string]bool{}
		for _, r := range rs {
			pods[r.Pod] = true
		}
		out = append(out, Finding{Rule: "logs.error-signature", Severity: Info, Cluster: k.cluster, Count: len(rs),
			Title:    fmt.Sprintf("%d× %s", len(rs), truncate(k.sig, 90)),
			Detail:   fmt.Sprintf("Repeated error in container %s (%s) across %d pod(s).", k.container, k.ns, len(pods)),
			LogQuery: fmt.Sprintf(`level:error ns:%s container:%s "%s"`, k.ns, k.container, phrase),
			Object:   &Ref{Cluster: k.cluster, Resource: "pods", Kind: "Pod", Namespace: k.ns, Name: rs[0].Pod}})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
