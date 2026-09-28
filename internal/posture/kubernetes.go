package posture

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
)

// k8sNow is the clock for date-based checks (version EOL, certificate
// expiry). Tests replace it.
var k8sNow = time.Now

const clusterScopedWhy = "ClusterRoles and ClusterRoleBindings were not captured; re-capture with --cluster-scoped"

// isKubernetes reports whether pods were captured with full detail.
func isKubernetes(e env) bool {
	if ok, _ := e.fullDetail(); !ok {
		return false
	}
	return len(e.pods()) > 0
}

// ---------- container and security-context helpers ----------

type ktr struct {
	c    map[string]any
	name string
	kind string // container, init, sidecar, ephemeral
}

func kContainers(ps map[string]any) []ktr {
	var out []ktr
	for _, x := range containers(ps) {
		k := x.kind
		if k == "init" && s(x.c["restartPolicy"]) == "Always" {
			k = "sidecar"
		}
		out = append(out, ktr{x.c, s(x.c["name"]), k})
	}
	return out
}

func (k ktr) label() string {
	switch k.kind {
	case "init":
		return "init container " + k.name
	case "sidecar":
		return "sidecar " + k.name
	case "ephemeral":
		return "ephemeral container " + k.name
	}
	return "container " + k.name
}

func (k ktr) long() bool { return k.kind == "container" || k.kind == "sidecar" }

func (k ktr) sc() map[string]any { return m(k.c["securityContext"]) }

// effective returns a security-context field from the container, else the pod.
func effective(ps map[string]any, k ktr, key string) any {
	if v, ok := k.sc()[key]; ok && v != nil {
		return v
	}
	return m(ps["securityContext"])[key]
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func boolp(v any) (val, set bool) {
	b, ok := v.(bool)
	return b, ok
}

func strList(v any) []string {
	var out []string
	for _, x := range l(v) {
		out = append(out, s(x))
	}
	return out
}

func capName(c string) string { return strings.TrimPrefix(strings.ToUpper(c), "CAP_") }

// sysComponent names the Istio or Solo component a container belongs to,
// for the documented exceptions. It matches the workload and the container
// name, never the namespace alone.
func sysComponent(o *store.Object, k ktr) string {
	w := istioSystemWorkload(o)
	img := s(k.c["image"])
	switch {
	case w == "ztunnel" && k.name == "istio-proxy":
		return "ztunnel"
	case w == "istio-cni" && k.name == "install-cni":
		return "istio-cni"
	case k.name == "istio-init" && k.kind == "init":
		return "istio-init"
	case k.name == "istio-proxy" && w != "ztunnel":
		return "istio-proxy"
	case k.name == "agentgateway" && strings.Contains(img, "agentgateway") && !strings.Contains(img, "controller"):
		return "agentgateway"
	case strings.Contains(o.Name, "node-exporter") || strings.Contains(img, "node-exporter"):
		return "node-exporter"
	}
	return ""
}

// verdict folds several findings on one workload into one item.
type verdict struct {
	st    Status
	bad   []string
	notes []string
}

func newVerdict() *verdict { return &verdict{st: Pass} }

func (v *verdict) add(st Status, msg string) {
	if statusRank[st] < statusRank[v.st] {
		v.st = st
	}
	if msg == "" {
		return
	}
	if st == Pass {
		v.notes = append(v.notes, msg)
	} else {
		v.bad = append(v.bad, msg)
	}
}

func (v *verdict) detail(ok string) string {
	if len(v.bad) > 0 {
		return strings.Join(v.bad, "; ")
	}
	if len(v.notes) > 0 {
		return strings.Join(dedupe(v.notes), "; ")
	}
	return ok
}

func annotations(o *store.Object) map[string]any { return m(path(o.Obj, "metadata", "annotations")) }

func templateAnnotations(o *store.Object) map[string]any {
	if o.Kind == "Pod" {
		return annotations(o)
	}
	if o.Kind == "CronJob" {
		return m(path(o.Obj, "spec", "jobTemplate", "spec", "template", "metadata", "annotations"))
	}
	return m(path(o.Obj, "spec", "template", "metadata", "annotations"))
}

// soloReleases are Helm release names of Istio and Solo components whose
// RBAC and settings come from the chart.
var soloReleases = map[string]bool{"istiod": true, "istio-base": true, "istio-cni": true, "ztunnel": true, "kgateway": true, "agentgateway": true,
	"enterprise-agentgateway": true, "kagent": true, "agentregistry": true, "gloo-operator": true, "cert-manager": true, "gloo-platform": true, "management": true}

var soloCharts = []string{"istiod-", "base-", "cni-", "ztunnel-", "kgateway", "enterprise-kgateway", "agentgateway", "enterprise-agentgateway", "kagent", "kmcp",
	"agentregistry", "gloo-platform", "gloo-operator", "cert-manager", "management-"}

// soloManaged reports whether an object was created by an Istio or Solo
// chart or controller.
func soloManaged(o *store.Object) bool {
	if o == nil {
		return false
	}
	if soloReleases[s(annotations(o)["meta.helm.sh/release-name"])] {
		return true
	}
	lb := labels(o)
	for _, c := range soloCharts {
		if strings.HasPrefix(lb["helm.sh/chart"], c) {
			return true
		}
	}
	if lb["app.kubernetes.io/part-of"] == "istio" || lb["operator.istio.io/component"] != "" || lb["gateway.solo.io/extension"] != "" ||
		lb["app.kubernetes.io/managed-by"] == "agentgateway" || lb["app.kubernetes.io/managed-by"] == "kgateway" {
		return true
	}
	if k, _ := ownerOf(o); k == "Gateway" {
		return true
	}
	return false
}

func imageBase(img string) string {
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:]
	}
	if i := strings.Index(img, ":"); i >= 0 {
		img = img[:i]
	}
	return img
}

// ---------- RBAC model ----------

type rbacModel struct {
	e        env
	roles    []*store.Object
	bindings []*store.Object
	byRef    map[string]*store.Object
	nsOK     bool
	nsWhy    string
	clOK     bool
}

func newRBAC(e env) *rbacModel {
	r := &rbacModel{e: e, byRef: map[string]*store.Object{}}
	ok1, why1 := e.evaluable("rbac.authorization.k8s.io", "roles", "Role", true)
	ok2, why2 := e.evaluable("rbac.authorization.k8s.io", "rolebindings", "RoleBinding", true)
	r.nsOK, r.nsWhy = ok1 && ok2, strings.TrimSpace(why1+" "+why2)
	c1, _ := e.evaluable("rbac.authorization.k8s.io", "clusterroles", "ClusterRole", false)
	c2, _ := e.evaluable("rbac.authorization.k8s.io", "clusterrolebindings", "ClusterRoleBinding", false)
	r.clOK = c1 && c2
	for _, o := range e.list("rbac.authorization.k8s.io", "roles") {
		r.roles = append(r.roles, o)
		r.byRef["Role/"+o.Namespace+"/"+o.Name] = o
	}
	for _, o := range e.list("rbac.authorization.k8s.io", "clusterroles") {
		r.roles = append(r.roles, o)
		r.byRef["ClusterRole//"+o.Name] = o
	}
	r.bindings = append(r.bindings, e.list("rbac.authorization.k8s.io", "rolebindings")...)
	r.bindings = append(r.bindings, e.list("rbac.authorization.k8s.io", "clusterrolebindings")...)
	return r
}

func (r *rbacModel) roleOf(b *store.Object) *store.Object {
	rr := m(b.Obj["roleRef"])
	if s(rr["kind"]) == "ClusterRole" {
		return r.byRef["ClusterRole//"+s(rr["name"])]
	}
	return r.byRef["Role/"+b.Namespace+"/"+s(rr["name"])]
}

func systemRBACName(n string) bool {
	return strings.HasPrefix(n, "system:") || strings.HasPrefix(n, "kubeadm:")
}

func subjectNS(b *store.Object, sm map[string]any) string {
	if ns := s(sm["namespace"]); ns != "" {
		return ns
	}
	return b.Namespace
}

func systemSubject(b *store.Object, sm map[string]any) bool {
	if s(sm["kind"]) == "ServiceAccount" {
		return platformNS(subjectNS(b, sm))
	}
	return strings.HasPrefix(s(sm["name"]), "system:")
}

func subjectName(b *store.Object, sm map[string]any) string {
	if s(sm["kind"]) == "ServiceAccount" {
		return "ServiceAccount " + subjectNS(b, sm) + "/" + s(sm["name"])
	}
	return s(sm["kind"]) + " " + s(sm["name"])
}

// userSubjects returns the non-system subjects of a binding.
func userSubjects(b *store.Object) []map[string]any {
	var out []map[string]any
	for _, sb := range l(b.Obj["subjects"]) {
		sm := m(sb)
		if !systemSubject(b, sm) {
			out = append(out, sm)
		}
	}
	return out
}

func systemBinding(b *store.Object) bool {
	return systemRBACName(b.Name) || (b.Namespace != "" && platformNS(b.Namespace))
}

// boundTo returns the bindings that reference a role and have user subjects.
func (r *rbacModel) boundTo(role *store.Object) []*store.Object {
	var out []*store.Object
	for _, b := range r.bindings {
		if systemBinding(b) || len(userSubjects(b)) == 0 {
			continue
		}
		if r.roleOf(b) == role {
			out = append(out, b)
		}
	}
	return out
}

func subjectsText(bs []*store.Object) string {
	var n []string
	seen := map[string]bool{}
	for _, b := range bs {
		for _, sm := range userSubjects(b) {
			x := subjectName(b, sm)
			if !seen[x] {
				seen[x] = true
				n = append(n, x)
			}
		}
	}
	return strings.Join(n, ", ")
}

func has(list []string, vals ...string) bool {
	for _, x := range list {
		for _, v := range vals {
			if x == v {
				return true
			}
		}
	}
	return false
}

// ruleGrants reports whether an RBAC rule grants any of verbs on
// group/resource. resourceNames-restricted rules count only when named is true.
func ruleGrants(rule map[string]any, named bool, group, resource string, verbs ...string) bool {
	if !named && len(l(rule["resourceNames"])) > 0 {
		return false
	}
	res := strList(rule["resources"])
	parent := resource
	if i := strings.Index(resource, "/"); i >= 0 {
		parent = resource[:i]
	}
	if !has(strList(rule["apiGroups"]), group, "*") || !has(res, resource, "*", parent+"/*") {
		return false
	}
	return has(strList(rule["verbs"]), append(verbs, "*")...)
}

func roleRules(role *store.Object) []map[string]any {
	var out []map[string]any
	for _, r := range l(role.Obj["rules"]) {
		out = append(out, m(r))
	}
	return out
}

func (r *rbacModel) saBound(ns, name string) (bool, string) {
	for _, b := range r.bindings {
		for _, sb := range l(b.Obj["subjects"]) {
			sm := m(sb)
			if s(sm["kind"]) == "ServiceAccount" && s(sm["name"]) == name && subjectNS(b, sm) == ns {
				return true, b.Kind + " " + strings.TrimPrefix(b.Namespace+"/"+b.Name, "/")
			}
		}
	}
	return false, ""
}

// ---------- the checks ----------

var safeSysctls = map[string]bool{"kernel.shm_rmid_forced": true, "net.ipv4.ip_local_port_range": true, "net.ipv4.tcp_syncookies": true,
	"net.ipv4.ping_group_range": true, "net.ipv4.ip_unprivileged_port_start": true, "net.ipv4.ip_local_reserved_ports": true,
	"net.ipv4.tcp_keepalive_time": true, "net.ipv4.tcp_fin_timeout": true, "net.ipv4.tcp_keepalive_intvl": true,
	"net.ipv4.tcp_keepalive_probes": true, "net.ipv4.tcp_rmem": true, "net.ipv4.tcp_wmem": true}

var dangerousCaps = map[string]bool{"ALL": true, "SYS_ADMIN": true, "NET_ADMIN": true, "SYS_PTRACE": true, "SYS_MODULE": true, "DAC_READ_SEARCH": true, "BPF": true, "PERFMON": true}

var expectedCaps = map[string]map[string]bool{
	"ztunnel":   {"NET_ADMIN": true, "SYS_ADMIN": true, "NET_RAW": true},
	"istio-cni": {"NET_ADMIN": true, "NET_RAW": true, "SYS_PTRACE": true, "SYS_ADMIN": true, "DAC_OVERRIDE": true},
}

// hostPaths the Istio node agents mount by design.
var expectedHostPaths = map[string]map[string]bool{
	"ztunnel":   {"/var/run/ztunnel": true},
	"istio-cni": {"/opt/cni/bin": true, "/etc/cni/net.d": true, "/var/run/istio-cni": true, "/var/run/netns": true, "/var/run/ztunnel": true, "/proc": true},
}

func sensitiveHostPath(p string) bool {
	switch p {
	case "/", "/etc", "/root", "/var/run/docker.sock", "/run/docker.sock":
		return true
	}
	for _, pre := range []string{"/run/containerd", "/var/run/containerd", "/var/run/crio", "/run/crio", "/var/lib/kubelet", "/etc/kubernetes", "/root/"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

func kubernetesChecks(e env) []Check {
	var out []Check
	full, fullWhy := e.fullDetail()
	add := func(sp spec, needsSpec bool, eval func(*Check)) {
		c := sp.check("kubernetes")
		if needsSpec && !full {
			c.Status, c.Reason = NA, fullWhy
		} else {
			eval(&c)
		}
		out = append(out, c)
	}
	na := func(c *Check, why string) { c.Status, c.Reason = NA, why }

	var sec []*store.Object // workloads outside platform namespaces
	for _, o := range e.workloads() {
		if !platformNS(o.Namespace) && podSpec(o) != nil {
			sec = append(sec, o)
		}
	}
	noWorkloads := "no workloads outside platform namespaces"
	perContainer := func(c *Check, ok string, eval func(o *store.Object, ps map[string]any, k ktr, sys string, v *verdict)) {
		if len(sec) == 0 {
			na(c, noWorkloads)
			return
		}
		for _, o := range sec {
			ps := podSpec(o)
			v := newVerdict()
			for _, k := range kContainers(ps) {
				eval(o, ps, k, sysComponent(o, k), v)
			}
			c.Items = append(c.Items, e.item(o, v.st, v.detail(ok)))
		}
	}

	// ----- workload security -----

	add(spec{"k8s.no-privileged", "Workload security", "critical", "No privileged containers",
		"A privileged container has every capability and the host's devices: it is root on the node.",
		"securityContext:\n  privileged: false\n  procMount: Default\n  appArmorProfile: {type: RuntimeDefault}",
		"https://kubernetes.io/docs/concepts/security/pod-security-standards/"}, true, func(c *Check) {
		perContainer(c, "not privileged", func(o *store.Object, ps map[string]any, k ktr, sys string, v *verdict) {
			if p, _ := boolp(k.sc()["privileged"]); p {
				if sys == "istio-cni" {
					v.add(Warn, k.label()+" is privileged; current istio-cni charts do not need it")
				} else {
					v.add(Fail, k.label()+" is privileged")
				}
			}
			if s(k.sc()["procMount"]) == "Unmasked" {
				v.add(Warn, k.label()+" uses procMount Unmasked")
			}
			aa := s(m(effective(ps, k, "appArmorProfile"))["type"])
			if aa == "" {
				aa = s(templateAnnotations(o)["container.apparmor.security.beta.kubernetes.io/"+k.name])
			}
			if strings.EqualFold(aa, "unconfined") {
				if sys == "istio-cni" {
					v.add(Pass, "istio-cni runs AppArmor unconfined by design")
				} else {
					v.add(Warn, k.label()+" runs AppArmor unconfined")
				}
			}
		})
		// Unsafe sysctls are pod level.
		for i, o := range sec {
			var bad []string
			for _, sc := range l(path(podSpec(o), "securityContext", "sysctls")) {
				if n := s(m(sc)["name"]); !safeSysctls[n] {
					bad = append(bad, n)
				}
			}
			if len(bad) > 0 {
				it := &c.Items[i]
				msg := "unsafe sysctls " + strings.Join(bad, ", ")
				if it.Status == Pass {
					it.Status, it.Detail = Warn, msg
				} else {
					it.Detail += "; " + msg
				}
			}
		}
	})

	add(spec{"k8s.host-access", "Isolation", "high", "No host namespaces, host ports or sensitive host paths",
		"hostNetwork, hostPID, hostIPC and host paths such as the container runtime socket or /var/lib/kubelet let a pod step out onto the node.",
		"spec:\n  hostNetwork: false\n  hostPID: false\n  hostIPC: false\n  volumes:\n  - name: data\n    emptyDir: {}   # instead of hostPath",
		meshBase + "ztunnel-native-egress/"}, true, func(c *Check) {
		if len(sec) == 0 {
			na(c, noWorkloads)
			return
		}
		for _, o := range sec {
			ps := podSpec(o)
			v := newVerdict()
			sys := istioSystemWorkload(o)
			nodeExp := false
			for _, k := range kContainers(ps) {
				if sysComponent(o, k) == "node-exporter" {
					nodeExp = true
				}
			}
			for _, f := range []string{"hostNetwork", "hostPID", "hostIPC"} {
				on, _ := boolp(ps[f])
				if !on {
					continue
				}
				switch {
				case nodeExp && f != "hostIPC":
					v.add(Pass, "node-exporter needs "+f)
				case sys == "istio-cni" && f == "hostNetwork":
					v.add(Warn, "istio-cni uses hostNetwork; current charts do not set it")
				case sys == "ztunnel":
					v.add(Fail, "ztunnel uses "+f+"; it must run in its own network namespace")
				default:
					v.add(Fail, f+": true")
				}
			}
			for _, k := range kContainers(ps) {
				for _, p := range l(k.c["ports"]) {
					if hp, ok := num(m(p)["hostPort"]); ok && hp > 0 {
						v.add(Fail, fmt.Sprintf("%s binds hostPort %d", k.label(), int(hp)))
					}
				}
			}
			// Which hostPath volumes are mounted writable, and by whom.
			writable := map[string]string{}
			for _, k := range kContainers(ps) {
				for _, vm := range l(k.c["volumeMounts"]) {
					vmm := m(vm)
					if ro, _ := boolp(vmm["readOnly"]); !ro {
						if _, seen := writable[s(vmm["name"])]; !seen {
							writable[s(vmm["name"])] = k.name
						}
					}
				}
			}
			var expected []string
			comp := ""
			for _, k := range kContainers(ps) {
				if x := sysComponent(o, k); x == "ztunnel" || x == "istio-cni" {
					comp = x
				}
			}
			for _, vol := range l(ps["volumes"]) {
				vm := m(vol)
				hp := m(vm["hostPath"])
				if hp == nil {
					continue
				}
				p := strings.TrimSuffix(s(hp["path"]), "/")
				if p == "" {
					p = "/"
				}
				who, rw := writable[s(vm["name"])]
				switch {
				case comp != "" && expectedHostPaths[comp][p] && !(p == "/proc" && rw):
					expected = append(expected, p)
				case p == "/proc" && rw:
					v.add(Fail, "container "+who+" mounts host /proc writable")
				case sensitiveHostPath(p):
					v.add(Fail, "mounts host path "+p)
				case nodeExp && !rw:
					v.add(Pass, "node-exporter reads "+p)
				case rw:
					v.add(Warn, "container "+who+" mounts host path "+p+" writable")
				default:
					v.add(Pass, "reads host path "+p)
				}
			}
			if len(expected) > 0 {
				v.add(Pass, comp+" mounts its expected host paths "+strings.Join(expected, ", "))
			}
			c.Items = append(c.Items, e.item(o, v.st, v.detail("no host access")))
		}
	})

	add(spec{"k8s.privilege-escalation", "Workload security", "high", "allowPrivilegeEscalation is false",
		"Without it a setuid binary in the image can gain more privileges than the process started with.",
		"securityContext:\n  allowPrivilegeEscalation: false", "https://kubernetes.io/docs/concepts/security/pod-security-standards/"}, true, func(c *Check) {
		perContainer(c, "allowPrivilegeEscalation: false everywhere", func(o *store.Object, ps map[string]any, k ktr, sys string, v *verdict) {
			val, set := boolp(k.sc()["allowPrivilegeEscalation"])
			switch {
			case sys == "ztunnel" || sys == "istio-cni":
				v.add(Pass, sys+" is allowed escalation by design: it needs its added capabilities")
			case set && !val:
			case set && val:
				v.add(Fail, k.label()+" sets allowPrivilegeEscalation: true")
			default:
				v.add(Warn, k.label()+" leaves allowPrivilegeEscalation unset")
			}
		})
	})

	add(spec{"k8s.run-as-non-root", "Workload security", "medium", "Containers run as a non-root user",
		"A process running as uid 0 is root on the node the moment it escapes the container.",
		"securityContext:\n  runAsNonRoot: true\n  runAsUser: 10001", kbBase + "sovereign-ai-uk-eks/"}, true, func(c *Check) {
		perContainer(c, "non-root", func(o *store.Object, ps map[string]any, k ktr, sys string, v *verdict) {
			uid, uidSet := num(effective(ps, k, "runAsUser"))
			nonRoot, nrSet := boolp(effective(ps, k, "runAsNonRoot"))
			switch {
			case (sys == "ztunnel" || sys == "istio-cni") && uidSet && uid == 0:
				v.add(Pass, sys+" runs as root by design")
			case sys == "istio-init" && (!uidSet || uid == 0):
				v.add(Warn, "istio-init runs as root; use istio-cni instead of the init container")
			case uidSet && uid == 0:
				v.add(Fail, k.label()+" runs as uid 0")
			case uidSet && uid > 0:
			case nrSet && nonRoot:
			case nrSet && !nonRoot:
				v.add(Fail, k.label()+" sets runAsNonRoot: false")
			default:
				v.add(Warn, k.label()+" sets no user; depends on the image USER")
			}
		})
	})

	add(spec{"k8s.capabilities", "Workload security", "high", "Capabilities are dropped, not added",
		"Capabilities such as SYS_ADMIN or NET_ADMIN are most of root. Drop ALL and add back only what the process needs.",
		"securityContext:\n  capabilities:\n    drop: [ALL]\n    add: [NET_BIND_SERVICE]   # only if it binds a port below 1024",
		meshBase + "istio-ambient-gke-autopilot/"}, true, func(c *Check) {
		perContainer(c, "drops ALL, adds nothing risky", func(o *store.Object, ps map[string]any, k ktr, sys string, v *verdict) {
			caps := m(k.sc()["capabilities"])
			var dangerous, other, expected []string
			for _, a := range strList(caps["add"]) {
				a = capName(a)
				switch {
				case expectedCaps[sys][a]:
					expected = append(expected, a)
				case sys == "istio-init" && (a == "NET_ADMIN" || a == "NET_RAW"):
					v.add(Warn, "istio-init adds "+a+"; use istio-cni instead of the init container")
				case dangerousCaps[a]:
					dangerous = append(dangerous, a)
				case a != "NET_BIND_SERVICE":
					other = append(other, a)
				}
			}
			if len(expected) > 0 {
				v.add(Pass, sys+" adds "+strings.Join(expected, ", ")+" by design")
			}
			if len(dangerous) > 0 {
				v.add(Fail, k.label()+" adds "+strings.Join(dangerous, ", "))
			}
			if len(other) > 0 {
				v.add(Warn, k.label()+" adds "+strings.Join(other, ", "))
			}
			dropAll := false
			for _, d := range strList(caps["drop"]) {
				if capName(d) == "ALL" {
					dropAll = true
				}
			}
			if !dropAll && sys != "istio-init" {
				v.add(Warn, k.label()+" does not drop ALL")
			}
		})
	})

	add(spec{"k8s.seccomp", "Workload security", "medium", "A seccomp profile is set",
		"Without seccomp every syscall is open to the container, including the rarely used ones most kernel exploits go through.",
		"spec:\n  securityContext:\n    seccompProfile: {type: RuntimeDefault}", "https://kubernetes.io/docs/tutorials/security/seccomp/"}, true, func(c *Check) {
		perContainer(c, "seccomp confined", func(o *store.Object, ps map[string]any, k ktr, sys string, v *verdict) {
			t := s(m(effective(ps, k, "seccompProfile"))["type"])
			switch {
			case t == "RuntimeDefault" || t == "Localhost":
			case t == "Unconfined":
				v.add(Fail, k.label()+" runs seccomp Unconfined")
			case sys == "ztunnel" || sys == "istio-cni" || sys == "istio-proxy" || sys == "istio-init" || istioSystemWorkload(o) == "istiod":
				v.add(Pass, "no seccomp profile on "+k.name+", left unset by the Istio chart or injector; not flagged")
			default:
				v.add(Warn, k.label()+" has no seccomp profile")
			}
		})
	})

	add(spec{"k8s.readonly-rootfs", "Workload security", "low", "Root filesystems are read-only",
		"A read-only root filesystem stops an attacker writing tools or changing binaries inside a running container.",
		"securityContext:\n  readOnlyRootFilesystem: true\nvolumeMounts:\n- {name: tmp, mountPath: /tmp}   # emptyDir for scratch space",
		"https://kubernetes.io/docs/tasks/configure-pod-container/security-context/"}, true, func(c *Check) {
		perContainer(c, "read-only root filesystem", func(o *store.Object, ps map[string]any, k ktr, sys string, v *verdict) {
			if !k.long() {
				return
			}
			if ro, _ := boolp(k.sc()["readOnlyRootFilesystem"]); ro {
				return
			}
			if sys == "istio-cni" {
				v.add(Pass, "istio-cni has a writable root filesystem by chart default; not flagged")
				return
			}
			v.add(Warn, k.label()+" has a writable root filesystem")
		})
	})

	// ----- identity -----

	rb := newRBAC(e)
	agentNS := map[string]bool{}
	for _, a := range e.list("kagent.dev", "agents") {
		agentNS[a.Namespace] = true
	}
	saName := func(o *store.Object) string {
		ps := podSpec(o)
		n := s(ps["serviceAccountName"])
		if n == "" {
			n = s(ps["serviceAccount"])
		}
		if n == "" {
			n = "default"
		}
		return n
	}

	add(spec{"k8s.default-serviceaccount", "Identity", "medium", "Workloads use their own ServiceAccount",
		"Every pod in a namespace that uses the default ServiceAccount shares one identity, so any permission given to one is given to all.",
		"apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: <app>, namespace: <ns>}\nautomountServiceAccountToken: false\n---\nspec:\n  template:\n    spec:\n      serviceAccountName: <app>",
		"https://kubernetes.io/docs/concepts/security/service-accounts/"}, true, func(c *Check) {
		if len(sec) == 0 {
			na(c, noWorkloads)
			return
		}
		granted := map[string]string{} // namespace -> binding
		if rb.nsOK {
			for _, b := range rb.bindings {
				if systemBinding(b) {
					continue
				}
				for _, sb := range l(b.Obj["subjects"]) {
					sm := m(sb)
					if s(sm["kind"]) == "ServiceAccount" && s(sm["name"]) == "default" && !platformNS(subjectNS(b, sm)) {
						granted[subjectNS(b, sm)] = b.Kind + " " + strings.TrimPrefix(b.Namespace+"/"+b.Name, "/")
						c.Items = append(c.Items, e.item(b, Fail, "grants "+s(path(b.Obj, "roleRef", "kind"))+" "+s(path(b.Obj, "roleRef", "name"))+" to the default ServiceAccount of "+subjectNS(b, sm)))
					}
				}
			}
		}
		for _, o := range sec {
			n := saName(o)
			switch {
			case n != "default":
				c.Items = append(c.Items, e.item(o, Pass, "ServiceAccount "+n))
			case granted[o.Namespace] != "":
				c.Items = append(c.Items, e.item(o, Fail, "runs as the default ServiceAccount, which "+granted[o.Namespace]+" grants permissions to"))
			default:
				c.Items = append(c.Items, e.item(o, Warn, "runs as the default ServiceAccount"))
			}
		}
	})

	add(spec{"k8s.sa-token-automount", "Identity", "medium", "API tokens are mounted only where they are used",
		"A mounted token is a credential for the Kubernetes API. If the ServiceAccount has no role it is not needed, and it is one more thing to steal.",
		"apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: <app>}\nautomountServiceAccountToken: false",
		"https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/"}, true, func(c *Check) {
		if len(sec) == 0 {
			na(c, noWorkloads)
			return
		}
		if !rb.nsOK {
			na(c, rb.nsWhy)
			return
		}
		mounted := func(o *store.Object) (bool, string) {
			ps := podSpec(o)
			from := "template"
			if pods := e.podsOf(o); len(pods) > 0 {
				ps, from = m(pods[0].Obj["spec"]), "pod"
			}
			for _, vol := range l(ps["volumes"]) {
				vm := m(vol)
				pr := m(vm["projected"])
				if pr == nil {
					continue
				}
				if strings.HasPrefix(s(vm["name"]), "kube-api-access-") {
					return true, "kube-api-access volume"
				}
				for _, src := range l(pr["sources"]) {
					if t := m(m(src)["serviceAccountToken"]); t != nil && s(t["audience"]) == "" {
						return true, "projected token " + s(vm["name"]) + " without an audience"
					}
				}
			}
			if from == "pod" {
				return false, ""
			}
			if v, set := boolp(ps["automountServiceAccountToken"]); set {
				return v, "automountServiceAccountToken on the pod template"
			}
			if sa := e.ws.Get(e.cluster, "", "serviceaccounts", o.Namespace, saName(o)); sa != nil {
				if v, set := boolp(sa.Obj["automountServiceAccountToken"]); set {
					return v, "automountServiceAccountToken on the ServiceAccount"
				}
			}
			return true, "automount is on by default"
		}
		for _, o := range sec {
			on, how := mounted(o)
			n := saName(o)
			if !on {
				c.Items = append(c.Items, e.item(o, Pass, "no API token mounted"))
				continue
			}
			if istioSystemWorkload(o) != "" {
				c.Items = append(c.Items, e.item(o, Pass, "API token mounted by the Istio chart or controller default; not flagged"))
				continue
			}
			if ok, by := rb.saBound(o.Namespace, n); ok {
				c.Items = append(c.Items, e.item(o, Pass, "ServiceAccount "+n+" is bound by "+by))
				continue
			}
			if rb.clOK {
				c.Items = append(c.Items, e.item(o, Warn, fmt.Sprintf("token mounted (%s) but ServiceAccount %s has no RoleBinding or ClusterRoleBinding", how, n)))
			} else {
				c.Items = append(c.Items, e.item(o, NA, fmt.Sprintf("token mounted (%s) and ServiceAccount %s has no RoleBinding; not certain, because ClusterRoleBindings were not captured (--cluster-scoped)", how, n)))
			}
		}
		if !rb.clOK {
			c.Reason = "ClusterRoleBindings were not captured (re-capture with --cluster-scoped), so a ServiceAccount without a RoleBinding may still be bound cluster-wide"
		}
	})

	builtinRole := map[string]bool{"admin": true, "edit": true, "view": true, "cluster-admin": true}
	rbacRoles := func(c *Check) []*store.Object {
		if !rb.nsOK {
			na(c, rb.nsWhy)
			return nil
		}
		var out []*store.Object
		for _, r := range rb.roles {
			if systemRBACName(r.Name) || builtinRole[r.Name] || (r.Namespace != "" && platformNS(r.Namespace)) {
				continue
			}
			if len(rb.boundTo(r)) > 0 {
				out = append(out, r)
			}
		}
		if !rb.clOK {
			c.Reason = "only namespaced Roles were checked: " + clusterScopedWhy
		}
		return out
	}

	add(spec{"k8s.rbac-wildcards", "Identity", "high", "No wildcard RBAC rules",
		"verbs: [\"*\"] or resources: [\"*\"] grants every permission that exists now and every one added later.",
		"rules:\n- apiGroups: [\"\"]\n  resources: [configmaps]\n  verbs: [get, list, watch]", "https://kubernetes.io/docs/concepts/security/rbac-good-practices/"}, true, func(c *Check) {
		roles := rbacRoles(c)
		for _, r := range roles {
			v := newVerdict()
			for _, rule := range roleRules(r) {
				groups := strList(rule["apiGroups"])
				wild := ""
				switch {
				case has(strList(rule["verbs"]), "*"):
					wild = "verbs * on " + strings.Join(strList(rule["resources"]), ",") + " in groups [" + strings.Join(groups, ",") + "]"
				case has(strList(rule["resources"]), "*") && has(groups, "", "*"):
					wild = "resources * in groups [" + strings.Join(groups, ",") + "]"
				}
				if wild == "" {
					continue
				}
				if soloManaged(r) && !has(groups, "", "*") {
					v.add(Warn, "chart role has "+wild)
				} else {
					v.add(Fail, wild)
				}
			}
			c.Items = append(c.Items, e.item(r, v.st, v.detail("no wildcards")+"; bound to "+subjectsText(rb.boundTo(r))))
		}
		if len(c.Items) == 0 && c.Status != NA {
			na(c, strings.TrimSpace("no custom roles bound to non-system subjects. "+c.Reason))
		}
	})

	add(spec{"k8s.cluster-admin-bindings", "Identity", "critical", "cluster-admin is not granted to workloads or groups",
		"A ServiceAccount with cluster-admin turns any compromise of that pod into full control of the cluster.",
		"# replace with a role scoped to what the subject needs\nkubectl delete clusterrolebinding <name>", meshBase + "airgap-image-management/"}, true, func(c *Check) {
		if !rb.nsOK {
			na(c, rb.nsWhy)
			return
		}
		for _, b := range rb.bindings {
			if s(path(b.Obj, "roleRef", "name")) != "cluster-admin" || s(path(b.Obj, "roleRef", "kind")) != "ClusterRole" || systemRBACName(b.Name) {
				continue
			}
			v := newVerdict()
			for _, sb := range l(b.Obj["subjects"]) {
				sm := m(sb)
				name := s(sm["name"])
				switch s(sm["kind"]) {
				case "ServiceAccount":
					if subjectNS(b, sm) == "kube-system" {
						v.add(Pass, "provider ServiceAccount "+name)
					} else {
						v.add(Fail, "ServiceAccount "+subjectNS(b, sm)+"/"+name+" has cluster-admin")
					}
				case "Group":
					if name == "system:masters" || strings.HasPrefix(name, "system:") {
						v.add(Pass, "group "+name)
					} else {
						v.add(Fail, "group "+name+" has cluster-admin")
					}
				case "User":
					if strings.HasPrefix(name, "system:") {
						v.add(Pass, "user "+name)
					} else {
						v.add(Warn, "user "+name+" has cluster-admin")
					}
				}
			}
			c.Items = append(c.Items, e.item(b, v.st, v.detail("system subjects only")))
		}
		if len(c.Items) == 0 {
			if rb.clOK {
				c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Pass, Detail: "no binding grants cluster-admin to a non-system subject"})
			} else {
				na(c, "no RoleBinding grants cluster-admin, but "+clusterScopedWhy)
			}
		}
	})

	add(spec{"k8s.rbac-secret-read", "Identity", "high", "Secret read access is narrow",
		"get, list or watch on secrets reads every credential in scope; list and watch return values, not just names.",
		"rules:\n- apiGroups: [\"\"]\n  resources: [secrets]\n  resourceNames: [<the one secret>]\n  verbs: [get]", "https://kubernetes.io/docs/concepts/security/rbac-good-practices/"}, true, func(c *Check) {
		if !rb.nsOK {
			na(c, rb.nsWhy)
			return
		}
		unknown := 0
		for _, b := range rb.bindings {
			if systemBinding(b) || len(userSubjects(b)) == 0 {
				continue
			}
			role := rb.roleOf(b)
			if role == nil {
				unknown++
				continue
			}
			if systemRBACName(role.Name) || builtinRole[role.Name] && role.Name != "admin" && role.Name != "edit" {
				continue
			}
			reads := false
			for _, rule := range roleRules(role) {
				if ruleGrants(rule, false, "", "secrets", "get", "list", "watch") {
					reads = true
				}
			}
			who := subjectsText([]*store.Object{b})
			scope := "in " + b.Namespace
			if b.Kind == "ClusterRoleBinding" {
				scope = "cluster-wide"
			}
			switch {
			case !reads:
				c.Items = append(c.Items, e.item(b, Pass, "no Secret read for "+who))
			case soloManaged(b) || soloManaged(role):
				c.Items = append(c.Items, e.item(b, Pass, "Istio or Solo controller "+who+" reads Secrets "+scope+" by design"))
			case b.Kind == "ClusterRoleBinding":
				c.Items = append(c.Items, e.item(b, Fail, who+" can read every Secret in the cluster via "+role.Kind+" "+role.Name))
			default:
				c.Items = append(c.Items, e.item(b, Warn, who+" can read every Secret in "+b.Namespace+" via "+role.Kind+" "+role.Name))
			}
		}
		if !rb.clOK {
			c.Reason = "only RoleBindings were checked: " + clusterScopedWhy
			if unknown > 0 {
				c.Reason += fmt.Sprintf("; %d RoleBindings reference a ClusterRole whose rules were not captured", unknown)
			}
		}
		if len(c.Items) == 0 {
			na(c, strings.TrimSpace("no bindings to non-system subjects. "+c.Reason))
		}
	})

	add(spec{"k8s.rbac-escalation", "Identity", "high", "No RBAC paths to privilege escalation",
		"Creating pods with Secret read, escalate, bind, impersonate, pods/exec, serviceaccounts/token and nodes/proxy each let a subject become someone more powerful.",
		"# split the permissions across roles, or remove the verb\nrules:\n- apiGroups: [\"\"]\n  resources: [pods]\n  verbs: [get, list, watch]", "https://kubernetes.io/docs/concepts/security/rbac-good-practices/#privilege-escalation-risks"}, true, func(c *Check) {
		roles := rbacRoles(c)
		for _, r := range roles {
			var found []string
			podsCreate, secretsRead := false, false
			for _, rule := range roleRules(r) {
				if ruleGrants(rule, true, "", "pods", "create") {
					podsCreate = true
				}
				if ruleGrants(rule, false, "", "secrets", "get", "list") {
					secretsRead = true
				}
				for _, vb := range []string{"escalate", "bind", "impersonate"} {
					if has(strList(rule["verbs"]), vb) {
						found = append(found, vb+" on "+strings.Join(strList(rule["resources"]), ","))
					}
				}
				if ruleGrants(rule, true, "", "pods/exec", "create") {
					found = append(found, "create pods/exec")
				}
				if ruleGrants(rule, true, "", "serviceaccounts/token", "create") {
					found = append(found, "create serviceaccounts/token")
				}
				if ruleGrants(rule, true, "", "nodes/proxy", "get", "create", "update", "patch") {
					found = append(found, "nodes/proxy")
				}
			}
			if podsCreate && secretsRead {
				found = append([]string{"create pods together with Secret read"}, found...)
			}
			found = dedupe(found)
			who := "; bound to " + subjectsText(rb.boundTo(r))
			switch {
			case len(found) == 0:
				c.Items = append(c.Items, e.item(r, Pass, "no escalation paths"+who))
			case soloManaged(r):
				c.Items = append(c.Items, e.item(r, Warn, "controller role grants "+strings.Join(found, ", ")+who))
			default:
				c.Items = append(c.Items, e.item(r, Fail, strings.Join(found, ", ")+who))
			}
		}
		if len(c.Items) == 0 && c.Status != NA {
			na(c, strings.TrimSpace("no custom roles bound to non-system subjects. "+c.Reason))
		}
	})

	add(spec{"k8s.secrets-in-env", "Identity", "low", "Credentials are not held in environment variables",
		"Environment variables leak into crash dumps, child processes and debug endpoints; a literal value is also readable by anyone who can read the Deployment.",
		"volumeMounts:\n- {name: creds, mountPath: /var/run/secrets/app, readOnly: true}\nvolumes:\n- name: creds\n  secret: {secretName: app-creds}",
		"https://kubernetes.io/docs/concepts/security/secrets-good-practices/"}, true, func(c *Check) {
		if len(sec) == 0 {
			na(c, noWorkloads)
			return
		}
		for _, o := range sec {
			if agentNS[o.Namespace] {
				continue // agentic.no-literal-credentials covers agent namespaces
			}
			v := newVerdict()
			solo := soloManaged(o)
			for _, k := range kContainers(podSpec(o)) {
				var refs []string
				for _, ev := range l(k.c["env"]) {
					em := m(ev)
					name := s(em["name"])
					if sk := m(path(em, "valueFrom", "secretKeyRef")); sk != nil {
						refs = append(refs, name+" from Secret "+s(sk["name"]))
						continue
					}
					val := s(em["value"])
					if !strings.Contains(val, "[REDACTED") {
						continue
					}
					switch {
					case sensitiveEnv.MatchString(name) && settingEnv.MatchString(name):
					case solo:
						v.add(Pass, name+" is a chart setting whose name looks like a credential")
					case sensitiveEnv.MatchString(name) || keyLike.MatchString(val):
						v.add(Fail, k.label()+" env "+name+" has a literal value (redacted in the capture)")
					}
				}
				for _, ef := range l(k.c["envFrom"]) {
					if sr := m(m(ef)["secretRef"]); sr != nil {
						refs = append(refs, "all keys of Secret "+s(sr["name"]))
					}
				}
				switch {
				case len(refs) > 0 && solo:
					v.add(Pass, k.label()+" reads "+strings.Join(refs, ", ")+" into env as the Istio or Solo chart sets it")
				case len(refs) > 0:
					v.add(Warn, k.label()+" reads "+strings.Join(refs, ", ")+" into env")
				}
			}
			c.Items = append(c.Items, e.item(o, v.st, v.detail("no credentials in env")))
		}
		if len(agentNS) > 0 {
			c.Reason = "namespaces with kagent Agents are scored by agentic.no-literal-credentials instead"
		}
	})

	// ----- isolation -----

	nsl, nsKnown := e.nsLabels()
	podNS := map[string][]*store.Object{}
	for _, p := range e.pods() {
		podNS[p.Namespace] = append(podNS[p.Namespace], p)
	}
	nodeAgentNS := map[string]string{}
	for _, o := range e.workloads() {
		if w := istioSystemWorkload(o); w == "ztunnel" || w == "istio-cni" {
			nodeAgentNS[o.Namespace] = w
		}
	}
	// appNS: namespaces running application pods, not platform or Istio's own.
	appNS := func() []string {
		var out []string
		for ns, pods := range podNS {
			if platformNS(ns) || ns == "istio-system" {
				continue
			}
			allSys := true
			for _, p := range pods {
				if w := istioSystemWorkload(p); w != "ztunnel" && w != "istio-cni" && w != "istiod" {
					allSys = false
				}
			}
			if !allSys {
				out = append(out, ns)
			}
		}
		sort.Strings(out)
		return out
	}()

	add(spec{"k8s.pod-security-admission", "Isolation", "high", "Namespaces enforce a Pod Security Standard",
		"Pod Security Admission is the built-in guard that refuses privileged pods. Without an enforce label a namespace accepts anything.",
		"kubectl label namespace <ns> pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/warn=restricted",
		meshBase + "ztunnel-native-egress/"}, false, func(c *Check) {
		nss, ok := e.namespaces()
		if !ok {
			na(c, e.noNSWhy())
			return
		}
		for _, n := range nss {
			if platformNS(n.Name) || len(podNS[n.Name]) == 0 {
				continue
			}
			lvl := labels(n)["pod-security.kubernetes.io/enforce"]
			agent := nodeAgentNS[n.Name]
			switch {
			case agent != "" && (lvl == "privileged" || lvl == ""):
				msg := "hosts " + agent + ", which needs the privileged level"
				if lvl == "" {
					msg += "; label it privileged explicitly and keep applications elsewhere"
				}
				c.Items = append(c.Items, e.item(n, Pass, msg))
			case lvl == "restricted":
				c.Items = append(c.Items, e.item(n, Pass, "enforce: restricted"))
			case lvl == "baseline" && n.Name == "agentgateway-system":
				c.Items = append(c.Items, e.item(n, Pass, "enforce: baseline, as agentgateway-system requires"))
			case lvl == "baseline":
				c.Items = append(c.Items, e.item(n, Warn, "enforce: baseline; restricted is achievable for most applications"))
			case lvl == "privileged":
				c.Items = append(c.Items, e.item(n, Fail, "enforce: privileged admits any pod"))
			default:
				c.Items = append(c.Items, e.item(n, Fail, "no enforce label: any pod is admitted unless the API server sets a cluster-wide default, which a snapshot cannot see"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no non-platform namespaces with pods")
		}
	})

	npOK, npWhy := e.absenceOK("networking.k8s.io", "networkpolicies", "NetworkPolicy", true)
	ingressNP := func(np *store.Object) bool {
		sp := m(np.Obj["spec"])
		types := strList(sp["policyTypes"])
		return len(types) == 0 || has(types, "Ingress")
	}
	isDefaultDenyAP := func(ap *store.Object) bool {
		sp := m(ap.Obj["spec"])
		act := s(sp["action"])
		return sp["selector"] == nil && sp["targetRefs"] == nil && sp["targetRef"] == nil && len(l(sp["rules"])) == 0 && (act == "" || act == "ALLOW")
	}

	add(spec{"k8s.netpol-default-deny", "Isolation", "medium", "Application namespaces deny ingress by default",
		"Without a default-deny NetworkPolicy every pod accepts connections from every other pod in the cluster.",
		"apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: default-deny-ingress, namespace: <ns>}\nspec:\n  podSelector: {}\n  policyTypes: [Ingress]",
		meshBase + "zero-trust-agents-kubernetes/"}, true, func(c *Check) {
		if !npOK {
			na(c, npWhy)
			return
		}
		for _, ns := range appNS {
			nps := e.ws.List(e.cluster, "networking.k8s.io", "networkpolicies", ns, nil)
			deny := ""
			for _, np := range nps {
				sp := m(np.Obj["spec"])
				if len(m(sp["podSelector"])) == 0 && has(strList(sp["policyTypes"]), "Ingress") && len(l(sp["ingress"])) == 0 {
					deny = np.Name
				}
			}
			switch {
			case deny != "":
				c.Items = append(c.Items, e.item(nsObj(ns), Pass, "default-deny NetworkPolicy "+deny))
			case len(nps) > 0:
				c.Items = append(c.Items, e.item(nsObj(ns), Warn, fmt.Sprintf("%d NetworkPolicies, none of them a namespace-wide default-deny", len(nps))))
			default:
				apDeny := ""
				if nsKnown && nsl[ns]["istio.io/dataplane-mode"] == "ambient" {
					for _, ap := range e.ws.List(e.cluster, "security.istio.io", "authorizationpolicies", ns, nil) {
						if isDefaultDenyAP(ap) {
							apDeny = ap.Name
						}
					}
				}
				if apDeny != "" {
					c.Items = append(c.Items, e.item(nsObj(ns), Warn, "no NetworkPolicy, but ambient AuthorizationPolicy "+apDeny+" denies by default"))
				} else {
					c.Items = append(c.Items, e.item(nsObj(ns), Fail, fmt.Sprintf("no NetworkPolicy: %d pods accept traffic from anywhere", len(podNS[ns]))))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no application namespaces with pods")
		}
	})

	add(spec{"k8s.netpol-ambient-compatible", "Isolation", "medium", "NetworkPolicies in ambient namespaces allow HBONE and kubelet probes",
		"In ambient, traffic arrives on HBONE port 15008 and kubelet probes come from 169.254.7.127. An ingress policy that blocks either breaks the mesh or the probes.",
		"spec:\n  podSelector: {}\n  policyTypes: [Ingress]\n  ingress:\n  - ports: [{port: 15008, protocol: TCP}]\n  - from: [{ipBlock: {cidr: 169.254.7.127/32}}]",
		"https://istio.io/latest/docs/ambient/usage/networkpolicy/"}, true, func(c *Check) {
		if !npOK {
			na(c, npWhy)
			return
		}
		if !nsKnown {
			na(c, e.noNSWhy())
			return
		}
		allowsPort := func(np *store.Object) bool {
			for _, r := range l(path(np.Obj, "spec", "ingress")) {
				ports := l(m(r)["ports"])
				if len(ports) == 0 {
					return true
				}
				for _, p := range ports {
					pm := m(p)
					pn, _ := num(pm["port"])
					end, hasEnd := num(pm["endPort"])
					proto := s(pm["protocol"])
					if (proto == "" || proto == "TCP") && (pn == 15008 || (hasEnd && pn <= 15008 && end >= 15008) || pm["port"] == nil) {
						return true
					}
				}
			}
			return false
		}
		allowsProbe := func(np *store.Object) bool {
			for _, r := range l(path(np.Obj, "spec", "ingress")) {
				from := l(m(r)["from"])
				if len(from) == 0 {
					return true
				}
				for _, f := range from {
					cidr := s(path(f, "ipBlock", "cidr"))
					if cidr == "169.254.7.127/32" || cidr == "0.0.0.0/0" || cidr == "169.254.0.0/16" {
						return true
					}
				}
			}
			return false
		}
		for ns, lb := range nsl {
			if lb["istio.io/dataplane-mode"] != "ambient" {
				continue
			}
			nps := e.ws.List(e.cluster, "networking.k8s.io", "networkpolicies", ns, nil)
			for _, np := range nps {
				if !ingressNP(np) {
					continue
				}
				sel := m(path(np.Obj, "spec", "podSelector"))
				var missing []string
				selected := 0
				for _, p := range podNS[ns] {
					if !selects(sel, labels(p)) {
						continue
					}
					selected++
					port, probe := false, false
					for _, other := range nps {
						if ingressNP(other) && selects(m(path(other.Obj, "spec", "podSelector")), labels(p)) {
							port = port || allowsPort(other)
							probe = probe || allowsProbe(other)
						}
					}
					if !port {
						missing = append(missing, "HBONE 15008 to "+p.Name)
					}
					if !probe {
						missing = append(missing, "probes from 169.254.7.127 to "+p.Name)
					}
				}
				switch {
				case selected == 0:
				case len(missing) > 0:
					if len(missing) > 4 {
						missing = append(missing[:4], fmt.Sprintf("and %d more", len(missing)-4))
					}
					c.Items = append(c.Items, e.item(np, Fail, "blocks "+strings.Join(missing, ", ")))
				default:
					c.Items = append(c.Items, e.item(np, Pass, "allows HBONE and kubelet probes"))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no ingress NetworkPolicies select pods in ambient namespaces")
		}
	})

	add(spec{"k8s.service-exposure", "Isolation", "high", "Only gateways are exposed outside the cluster",
		"A LoadBalancer or NodePort Service is reachable from outside the cluster. Istio debug and xDS ports, or an application bypassing the gateway, should never be.",
		"metadata:\n  annotations:\n    service.beta.kubernetes.io/aws-load-balancer-scheme: internal   # or the provider equivalent\nspec:\n  type: ClusterIP   # route through the Gateway instead\n  loadBalancerSourceRanges: [10.0.0.0/8]",
		meshBase + "istio-ambient-crds/"}, true, func(c *Check) {
		ok, why := e.evaluable("", "services", "Service", true)
		if !ok {
			na(c, why)
			return
		}
		debugPorts := map[float64]bool{15000: true, 15004: true, 15010: true, 15014: true, 15020: true}
		internalAnn := map[string]string{"service.beta.kubernetes.io/aws-load-balancer-internal": "", "service.beta.kubernetes.io/aws-load-balancer-scheme": "internal",
			"networking.gke.io/load-balancer-type": "internal", "cloud.google.com/load-balancer-type": "internal", "service.beta.kubernetes.io/azure-load-balancer-internal": "true",
			"service.beta.kubernetes.io/oci-load-balancer-internal": "true", "service.kubernetes.io/ibm-load-balancer-cloud-provider-ip-type": "private"}
		relays := map[string]bool{"gloo-mesh-mgmt-server": true, "gloo-telemetry-gateway": true, "solo-enterprise-telemetry-gateway": true}
		for _, sv := range e.list("", "services") {
			if platformNS(sv.Namespace) || !exposedService(sv) {
				continue
			}
			sp := m(sv.Obj["spec"])
			typ := s(sp["type"])
			sel := m(sp["selector"])
			var pods []*store.Object
			if len(sel) > 0 {
				for _, p := range podNS[sv.Namespace] {
					if selects(sel, labels(p)) {
						pods = append(pods, p)
					}
				}
			}
			ctrlPlane := false
			hitsCore := ""
			for _, p := range pods {
				w := istioSystemWorkload(p)
				if w == "istiod" || w == "ztunnel" {
					hitsCore = w
				}
				img := ""
				for _, ct := range l(path(p.Obj, "spec", "containers")) {
					img += s(m(ct)["image"]) + " "
				}
				if w == "istiod" || strings.Contains(img, "controller") && (strings.Contains(img, "agentgateway") || strings.Contains(img, "kgateway")) {
					ctrlPlane = true
				}
			}
			var ports, bad []string
			var has15012 bool
			for _, p := range l(sp["ports"]) {
				pn, _ := num(m(p)["port"])
				ports = append(ports, strconv.Itoa(int(pn)))
				if debugPorts[pn] || (ctrlPlane && (pn == 8080 || pn == 9091)) {
					bad = append(bad, strconv.Itoa(int(pn)))
				}
				if pn == 15012 || pn == 15017 {
					has15012 = true
				}
			}
			lb := labels(sv)
			desc := typ + " on ports " + strings.Join(ports, ",")
			eastWest := lb["istio"] == "eastwestgateway" || strings.Contains(lb["gateway.networking.k8s.io/gateway-class-name"], "east-west")
			internal := false
			for k, want := range internalAnn {
				if v, ok := annotations(sv)[k]; ok && (want == "" || strings.EqualFold(s(v), want)) {
					internal = true
				}
			}
			ranges := strList(sp["loadBalancerSourceRanges"])
			restricted := len(ranges) > 0 && !has(ranges, "0.0.0.0/0", "::/0")
			switch {
			case hitsCore != "":
				c.Items = append(c.Items, e.item(sv, Fail, desc+" selects "+hitsCore+" pods"))
			case len(bad) > 0:
				c.Items = append(c.Items, e.item(sv, Fail, desc+" exposes Istio debug or control ports "+strings.Join(bad, ",")))
			case eastWest && has15012:
				c.Items = append(c.Items, e.item(sv, Warn, "east-west gateway "+desc+" also exposes istiod ports 15012/15017"))
			case eastWest || lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["gateway.istio.io/managed"] != "" || lb["istio"] == "ingressgateway":
				c.Items = append(c.Items, e.item(sv, Pass, "gateway "+desc))
			case typ == "LoadBalancer" && (internal || restricted):
				c.Items = append(c.Items, e.item(sv, Warn, desc+", internal or source-restricted; route it through a Gateway"))
			case typ == "LoadBalancer" && relays[sv.Name]:
				c.Items = append(c.Items, e.item(sv, Warn, "multicluster relay "+desc+"; restrict it with loadBalancerSourceRanges"))
			case typ == "LoadBalancer":
				c.Items = append(c.Items, e.item(sv, Fail, desc+" is open to any source and bypasses the gateway"))
			default:
				c.Items = append(c.Items, e.item(sv, Warn, desc+" (NodePort or externalIPs)"))
			}
		}
		if len(c.Items) == 0 {
			c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Pass, Detail: "no LoadBalancer, NodePort or externalIPs Services outside platform namespaces"})
		}
	})

	// ----- resilience -----

	add(spec{"k8s.resource-requests", "Resilience", "medium", "Containers set CPU and memory requests and a memory limit",
		"Without requests the scheduler packs pods blindly and they are first to be evicted; without a memory limit one leak can take the node down.",
		"resources:\n  requests: {cpu: 100m, memory: 128Mi}\n  limits: {memory: 256Mi}", kbBase + "agentgateway-deployment-sizing/"}, true, func(c *Check) {
		if len(sec) == 0 {
			na(c, noWorkloads)
			return
		}
		for _, o := range sec {
			ps := podSpec(o)
			if pods := e.podsOf(o); len(pods) > 0 {
				ps = m(pods[0].Obj["spec"]) // admitted spec: LimitRange defaults and injected sidecars
			}
			v := newVerdict()
			for _, k := range kContainers(ps) {
				if !k.long() {
					continue
				}
				res := m(k.c["resources"])
				var miss []string
				for _, r := range []string{"cpu", "memory"} {
					if m(res["requests"])[r] == nil {
						miss = append(miss, "requests."+r)
					}
				}
				if len(miss) > 0 {
					v.add(Fail, k.label()+" has no "+strings.Join(miss, " or "))
				} else if m(res["limits"])["memory"] == nil {
					if w := istioSystemWorkload(o); w == "ztunnel" || w == "istio-cni" || w == "istiod" {
						v.add(Pass, w+" runs without a memory limit by chart default, so it is not OOM-killed under load")
					} else {
						v.add(Warn, k.label()+" has no memory limit")
					}
				}
			}
			c.Items = append(c.Items, e.item(o, v.st, v.detail("requests and memory limit set")))
		}
	})

	add(spec{"k8s.probes", "Resilience", "medium", "Long-running containers have readiness probes",
		"Without a readiness probe a pod gets traffic before it can serve it, and keeps getting it after it stops.",
		"readinessProbe:\n  httpGet: {path: /ready, port: 8080}\nlivenessProbe:\n  httpGet: {path: /healthz, port: 8080}\n  failureThreshold: 6",
		"https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/"}, true, func(c *Check) {
		for _, o := range sec {
			if o.Kind != "Deployment" && o.Kind != "StatefulSet" && o.Kind != "DaemonSet" {
				continue
			}
			owner, _ := ownerOf(o)
			chart := soloManaged(o) || owner == "Agent" || istioSystemWorkload(o) != ""
			v := newVerdict()
			for _, k := range kContainers(podSpec(o)) {
				if !k.long() {
					continue
				}
				sys := sysComponent(o, k)
				if sys == "istio-proxy" || sys == "ztunnel" {
					continue
				}
				rp, lp, sp := k.c["readinessProbe"] != nil, k.c["livenessProbe"] != nil, k.c["startupProbe"] != nil
				switch {
				case !rp && !sp:
					v.add(Fail, k.label()+" has no readiness or startup probe")
				case rp && !lp && chart:
					v.add(Pass, "readiness probe only, as the Istio or Solo chart or controller sets it")
				case rp && !lp:
					v.add(Warn, k.label()+" has a readiness probe but no liveness probe")
				}
			}
			c.Items = append(c.Items, e.item(o, v.st, v.detail("probes set")))
		}
		if len(c.Items) == 0 {
			na(c, "no Deployments, StatefulSets or DaemonSets outside platform namespaces")
		}
	})

	type haW struct {
		o      *store.Object
		what   string
		strict bool
		reps   int
		hpa    *store.Object
	}
	var ha []haW
	for _, o := range sec {
		what, strict := haScope(o)
		if what == "" {
			continue
		}
		reps := 1
		if r, ok := num(path(o.Obj, "spec", "replicas")); ok {
			reps = int(r)
		}
		h := haW{o: o, what: what, strict: strict, reps: reps}
		for _, hp := range e.ws.List(e.cluster, "autoscaling", "horizontalpodautoscalers", o.Namespace, nil) {
			t := m(path(hp.Obj, "spec", "scaleTargetRef"))
			if s(t["kind"]) == o.Kind && s(t["name"]) == o.Name {
				h.hpa = hp
				min := 1
				if mr, ok := num(path(hp.Obj, "spec", "minReplicas")); ok {
					min = int(mr)
				}
				if min > h.reps {
					h.reps = min
				}
			}
		}
		ha = append(ha, h)
	}
	noHA := "no istiod, gateways, waypoints, gateway or kagent controllers, agentregistry or keycloak outside platform namespaces"

	add(spec{"k8s.ha-replicas", "Resilience", "high", "Critical components run more than one replica",
		"One istiod or one gateway replica means a node drain or a crash takes the control plane or all ingress with it.",
		"spec:\n  replicas: 2\n---\napiVersion: autoscaling/v2\nkind: HorizontalPodAutoscaler\nspec:\n  minReplicas: 2", meshBase + "istio-ambient-gke-autopilot/"}, true, func(c *Check) {
		for _, h := range ha {
			v := newVerdict()
			src := "spec.replicas"
			if h.hpa != nil {
				src = "max of spec.replicas and HPA minReplicas"
			}
			if h.reps < 2 {
				if h.strict {
					v.add(Fail, fmt.Sprintf("%s runs %d replica (%s)", h.what, h.reps, src))
				} else {
					v.add(Warn, fmt.Sprintf("%s runs %d replica (%s)", h.what, h.reps, src))
				}
			}
			if h.hpa != nil {
				if st, _, msg, found := condition(h.hpa, "ScalingActive"); found && st == "False" {
					if len(msg) > 140 {
						msg = msg[:140] + "..."
					}
					v.add(Fail, "HPA "+h.hpa.Name+" is not scaling: "+msg)
				}
			}
			c.Items = append(c.Items, e.item(h.o, v.st, v.detail(fmt.Sprintf("%s runs %d replicas", h.what, h.reps))))
		}
		if len(c.Items) == 0 {
			na(c, noHA)
		}
	})

	add(spec{"k8s.pdb", "Resilience", "medium", "Critical components have a working PodDisruptionBudget",
		"A PDB keeps a drain from evicting every replica at once; one that allows no disruptions blocks node upgrades instead.",
		"apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata: {name: istiod, namespace: istio-system}\nspec:\n  minAvailable: 1\n  selector: {matchLabels: {app: istiod}}",
		"https://kubernetes.io/docs/tasks/run-application/configure-pdb/"}, true, func(c *Check) {
		ok, why := e.absenceOK("policy", "poddisruptionbudgets", "PodDisruptionBudget", true)
		if !ok {
			na(c, why)
			return
		}
		for _, h := range ha {
			tl := podTemplateLabels(h.o)
			var pdb *store.Object
			for _, p := range e.ws.List(e.cluster, "policy", "poddisruptionbudgets", h.o.Namespace, nil) {
				if sel := m(path(p.Obj, "spec", "selector")); len(sel) > 0 && selects(sel, tl) {
					pdb = p
				}
			}
			if h.reps < 2 {
				if pdb == nil {
					continue
				}
				minAv := path(pdb.Obj, "spec", "minAvailable")
				maxUn := path(pdb.Obj, "spec", "maxUnavailable")
				mv, isNum := num(minAv)
				xv, xNum := num(maxUn)
				if (isNum && mv >= 1) || s(minAv) == "100%" || (xNum && xv == 0) || s(maxUn) == "0%" {
					c.Items = append(c.Items, e.item(h.o, Fail, "one replica with PDB "+pdb.Name+" that allows no eviction: blocks drains"))
				} else {
					c.Items = append(c.Items, e.item(h.o, Pass, "one replica; PDB "+pdb.Name+" allows eviction"))
				}
				continue
			}
			if pdb == nil {
				c.Items = append(c.Items, e.item(h.o, Fail, fmt.Sprintf("%d replicas and no PodDisruptionBudget", h.reps)))
				continue
			}
			st := m(pdb.Obj["status"])
			allowed, _ := num(st["disruptionsAllowed"])
			cur, _ := num(st["currentHealthy"])
			des, _ := num(st["desiredHealthy"])
			if st != nil && allowed == 0 && cur >= des && cur > 0 {
				c.Items = append(c.Items, e.item(h.o, Fail, "PDB "+pdb.Name+" allows 0 disruptions while all pods are healthy: blocks drains"))
			} else {
				c.Items = append(c.Items, e.item(h.o, Pass, "PDB "+pdb.Name))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no multi-replica critical components, and no single-replica ones with a PDB")
		}
	})

	nodesOK, _ := e.evaluable("", "nodes", "Node", false)
	zoneOf := map[string]string{}
	if nodesOK {
		for _, n := range e.list("", "nodes") {
			zoneOf[n.Name] = labels(n)["topology.kubernetes.io/zone"]
		}
	}

	add(spec{"k8s.spread", "Resilience", "medium", "Replicas are spread across nodes and zones",
		"Replicas on one node share its fate: two istiod pods on the same node are one failure away from none.",
		"spec:\n  topologySpreadConstraints:\n  - maxSkew: 1\n    topologyKey: kubernetes.io/hostname\n    whenUnsatisfiable: ScheduleAnyway\n    labelSelector: {matchLabels: {app: <app>}}",
		"https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/"}, true, func(c *Check) {
		clusterZones := map[string]bool{}
		for _, z := range zoneOf {
			if z != "" {
				clusterZones[z] = true
			}
		}
		for _, h := range ha {
			pods := e.podsOf(h.o)
			nodes, zones := map[string]bool{}, map[string]bool{}
			for _, p := range pods {
				if n := s(path(p.Obj, "spec", "nodeName")); n != "" {
					nodes[n] = true
					if z := zoneOf[n]; z != "" {
						zones[z] = true
					}
				}
			}
			scheduled := 0
			for range nodes {
				scheduled++
			}
			switch {
			case len(pods) < 2:
				continue
			case len(nodes) == 1:
				c.Items = append(c.Items, e.item(h.o, Fail, fmt.Sprintf("all %d replicas on node %s", len(pods), keys(nodes)[0])))
			case len(clusterZones) > 1 && len(zones) == 1:
				c.Items = append(c.Items, e.item(h.o, Warn, fmt.Sprintf("replicas on %d nodes but all in zone %s", scheduled, keys(zones)[0])))
			default:
				c.Items = append(c.Items, e.item(h.o, Pass, fmt.Sprintf("replicas on %d nodes", scheduled)))
			}
		}
		if len(c.Items) == 0 {
			if e.declaredOnly() {
				na(c, DeclaredWhy+": pods and the nodes they run on exist only in a running cluster")
			} else {
				na(c, "no critical component has more than one captured replica")
			}
		} else if !nodesOK {
			c.Reason = "zone spread not checked: Nodes were not captured (re-capture with --cluster-scoped)"
		}
	})

	// ----- supply chain -----

	add(spec{"k8s.image-pinned", "Supply chain", "high", "Images are pinned by digest",
		"A tag can be moved to different content at any time; only a digest guarantees every replica runs the image you tested.",
		"image: registry.example.com/app:1.4.2@sha256:<digest>", meshBase + "airgap-image-management/"}, true, func(c *Check) {
		if len(sec) == 0 {
			na(c, noWorkloads)
			return
		}
		digests := map[string]map[string]bool{}
		for _, p := range e.pods() {
			if platformNS(p.Namespace) {
				continue
			}
			img := map[string]string{}
			for _, k := range kContainers(m(p.Obj["spec"])) {
				img[k.name] = s(k.c["image"])
			}
			st := m(p.Obj["status"])
			for _, key := range []string{"containerStatuses", "initContainerStatuses"} {
				for _, cs := range l(st[key]) {
					cm := m(cs)
					id := s(cm["imageID"])
					if i := strings.Index(id, "sha256:"); i >= 0 && img[s(cm["name"])] != "" {
						ref := img[s(cm["name"])]
						if digests[ref] == nil {
							digests[ref] = map[string]bool{}
						}
						digests[ref][id[i:]] = true
					}
				}
			}
		}
		for _, o := range sec {
			v := newVerdict()
			var tagged []string
			for _, k := range kContainers(podSpec(o)) {
				img := s(k.c["image"])
				ref := img
				if i := strings.Index(ref, "@"); i >= 0 {
					ref = ref[:i]
				}
				tag := ""
				if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
					tag = ref[i+1:]
				}
				switch {
				case strings.Contains(img, "@sha256:"):
				case tag == "" || tag == "latest":
					v.add(Fail, k.label()+" uses "+img+" (no tag or :latest)")
				default:
					tagged = append(tagged, imageBase(img)+":"+tag)
				}
				if n := len(digests[img]); n > 1 {
					v.add(Fail, fmt.Sprintf("%s resolved to %d different digests across pods", img, n))
				}
			}
			if len(tagged) > 0 {
				v.add(Warn, "pinned by tag only: "+strings.Join(dedupe(tagged), ", "))
			}
			c.Items = append(c.Items, e.item(o, v.st, v.detail("pinned by digest")))
		}
	})

	// ----- platform -----

	var serverMinor int
	var gitVersion string
	{
		var ver struct {
			GitVersion string `json:"gitVersion"`
		}
		if raw := e.ws.Version(e.cluster); len(raw) > 0 && json.Unmarshal(raw, &ver) == nil {
			gitVersion = ver.GitVersion
			serverMinor = minorOf(gitVersion)
		}
	}

	add(spec{"k8s.version-supported", "Platform", "high", "Kubernetes is a supported release",
		"An end-of-life release gets no security fixes. Istio and Solo releases also support only recent Kubernetes versions.",
		"# plan the upgrade one minor version at a time\nkubectl version", "https://kubernetes.io/releases/"}, false, func(c *Check) {
		if e.runtimeNA(c, "the cluster version") {
			return
		}
		if gitVersion == "" || serverMinor == 0 {
			na(c, "the cluster /version was not captured")
			return
		}
		it := Item{Cluster: e.cluster, Kind: "Cluster", Name: e.cluster}
		managed := ""
		gv := strings.ToLower(gitVersion)
		switch {
		case strings.Contains(gv, "eks"):
			managed = "EKS"
		case strings.Contains(gv, "gke"):
			managed = "GKE"
		}
		if nodesOK && managed == "" {
			for _, n := range e.list("", "nodes") {
				if strings.HasPrefix(s(path(n.Obj, "spec", "providerID")), "azure://") {
					managed = "AKS"
				}
			}
		}
		eol, known := k8sEOL[serverMinor]
		now := k8sNow()
		switch {
		case !known && serverMinor > maxKnownMinor():
			it.Status, it.Detail = Pass, gitVersion+" is newer than the release table"
		case !known:
			it.Status, it.Detail = Fail, gitVersion+" is long past end of life"
		default:
			end, _ := time.Parse("2006-01-02", eol)
			days := int(end.Sub(now).Hours() / 24)
			switch {
			case days < 0:
				it.Status, it.Detail = Fail, fmt.Sprintf("%s reached upstream end of life on %s", gitVersion, eol)
			case days <= 90:
				it.Status, it.Detail = Warn, fmt.Sprintf("%s reaches upstream end of life on %s (%d days)", gitVersion, eol, days)
			default:
				it.Status, it.Detail = Pass, fmt.Sprintf("%s is supported upstream until %s", gitVersion, eol)
			}
		}
		if managed != "" && it.Status == Fail {
			it.Status = Warn
			it.Detail += "; " + managed + " may still cover it under extended support, at extra cost"
		}
		c.Items = append(c.Items, it)
	})

	add(spec{"k8s.node-health", "Platform", "high", "Nodes are Ready, without pressure, on a supported kubelet",
		"A node under memory or disk pressure evicts pods; a kubelet outside the version skew policy is unsupported.",
		"kubectl describe node <node>\nkubectl drain <node> --ignore-daemonsets   # then fix or replace it",
		"https://kubernetes.io/releases/version-skew-policy/"}, true, func(c *Check) {
		if e.runtimeNA(c, "node health") {
			return
		}
		if !nodesOK {
			na(c, "Nodes were not captured; re-capture with --cluster-scoped")
			return
		}
		for _, n := range e.list("", "nodes") {
			v := newVerdict()
			if st, reason, _, found := condition(n, "Ready"); !found || st != "True" {
				v.add(Fail, "Ready is "+strings.TrimSpace(st+" "+reason))
			}
			for _, cond := range []string{"MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable"} {
				if st, _, _, _ := condition(n, cond); st == "True" {
					v.add(Fail, cond)
				}
			}
			if u, _ := boolp(path(n.Obj, "spec", "unschedulable")); u {
				v.add(Warn, "cordoned (unschedulable)")
			}
			kv := s(path(n.Obj, "status", "nodeInfo", "kubeletVersion"))
			if km := minorOf(kv); km > 0 && serverMinor > 0 {
				switch {
				case km > serverMinor:
					v.add(Fail, fmt.Sprintf("kubelet %s is newer than the API server %s", kv, gitVersion))
				case serverMinor-km > 3:
					v.add(Fail, fmt.Sprintf("kubelet %s is more than three minor versions behind %s", kv, gitVersion))
				}
			}
			c.Items = append(c.Items, e.item(n, v.st, v.detail("Ready, kubelet "+kv)))
		}
		if len(c.Items) == 0 {
			na(c, "no Nodes in the capture")
		}
	})

	add(spec{"k8s.deprecated-apis", "Platform", "medium", "Applied manifests use current API versions",
		"A manifest on a removed API fails the next time it is applied, often in the middle of an upgrade.",
		"kubectl convert -f manifest.yaml --output-version <group>/<current version>", "https://kubernetes.io/docs/reference/using-api/deprecation-guide/"}, false, func(c *Check) {
		checked := 0
		if e.declaredOnly() {
			// A repo scan holds the manifests themselves: check every object's
			// declared apiVersion, which is what the next apply will send.
			for _, o := range e.ws.AllObjects(e.cluster) {
				av := s(o.Obj["apiVersion"])
				if av == "" || s(annotations(o)[store.SynthesisedAnnotation]) == "true" {
					continue
				}
				checked++
				if d, bad := deprecatedAPI(av, o.Kind); bad {
					st := Warn
					if d.removed != "" {
						st = Fail
					}
					c.Items = append(c.Items, e.item(o, st, fmt.Sprintf("declared as %s %s: %s", av, o.Kind, d.text())))
				}
			}
			if len(c.Items) == 0 && checked > 0 {
				c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Pass, Detail: fmt.Sprintf("%d declared objects use current APIs", checked)})
			}
			if checked == 0 {
				na(c, "no objects declared")
			}
			return
		}
		for _, o := range e.ws.AllObjects(e.cluster) {
			la := s(annotations(o)["kubectl.kubernetes.io/last-applied-configuration"])
			if la == "" {
				continue
			}
			var applied struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
			}
			if json.Unmarshal([]byte(la), &applied) != nil || applied.APIVersion == "" {
				continue
			}
			checked++
			if d, bad := deprecatedAPI(applied.APIVersion, applied.Kind); bad {
				st := Warn
				if d.removed != "" {
					st = Fail
				}
				c.Items = append(c.Items, e.item(o, st, fmt.Sprintf("last applied as %s %s: %s", applied.APIVersion, applied.Kind, d.text())))
			}
		}
		if checked == 0 {
			na(c, "no objects carry kubectl.kubernetes.io/last-applied-configuration; objects applied by Helm, operators or server-side apply cannot be checked")
			return
		}
		if len(c.Items) == 0 {
			c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Pass, Detail: fmt.Sprintf("%d objects applied with kubectl use current APIs", checked)})
		}
		c.Reason = fmt.Sprintf("checked %d objects that carry kubectl.kubernetes.io/last-applied-configuration; objects applied by Helm, operators or server-side apply are not covered", checked)
	})

	add(spec{"k8s.helm-releases", "Platform", "medium", "Helm releases are deployed, not failed or stuck",
		"A failed or pending release leaves the chart half applied, and Helm refuses the next upgrade until it is fixed.",
		"helm history <release> -n <ns>\nhelm rollback <release> <last good revision> -n <ns>", "https://helm.sh/docs/helm/helm_history/"}, false, func(c *Check) {
		if e.runtimeNA(c, "Helm release status") {
			return
		}
		ok, why := e.evaluable("", "secrets", "Secret", true)
		if !ok {
			na(c, why)
			return
		}
		latest := map[string]*store.Object{}
		rev := map[string]int{}
		for _, sc := range e.list("", "secrets") {
			if s(sc.Obj["type"]) != "helm.sh/release.v1" {
				continue
			}
			lb := labels(sc)
			n, _ := strconv.Atoi(lb["version"])
			if n == 0 {
				if f, ok := num(path(sc.Obj, "x-flightbay", "helm", "revision")); ok {
					n = int(f)
				}
			}
			key := sc.Namespace + "/" + lb["name"]
			if lb["name"] == "" {
				key = sc.Namespace + "/" + s(path(sc.Obj, "x-flightbay", "helm", "release"))
			}
			if n >= rev[key] {
				rev[key], latest[key] = n, sc
			}
		}
		var ks []string
		for k := range latest {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			sc := latest[k]
			st := labels(sc)["status"]
			if st == "" {
				st = s(path(sc.Obj, "x-flightbay", "helm", "status"))
			}
			chart := s(path(sc.Obj, "x-flightbay", "helm", "chart"))
			if cv := s(path(sc.Obj, "x-flightbay", "helm", "chartVersion")); cv != "" {
				chart += " " + cv
			}
			d := fmt.Sprintf("release %s revision %d: %s", k, rev[k], st)
			if chart != "" {
				d += " (" + chart + ")"
			}
			switch {
			case st == "deployed" || st == "uninstalled":
				c.Items = append(c.Items, e.item(sc, Pass, d))
			case st == "failed" || strings.HasPrefix(st, "pending"):
				c.Items = append(c.Items, e.item(sc, Fail, d))
			default:
				c.Items = append(c.Items, e.item(sc, Warn, d))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no Helm release Secrets in the capture")
		}
	})

	add(spec{"k8s.cert-expiry", "Platform", "high", "Certificates are not close to expiry",
		"When the mesh root or a serving certificate expires, mTLS and TLS handshakes fail everywhere at once.",
		"kubectl get certificate -A\n# for the Istio root: rotate with an intermediate before it expires\nkubectl get configmap istio-ca-root-cert -n <ns> -o jsonpath='{.data.root-cert\\.pem}' | openssl x509 -noout -enddate",
		"https://istio.io/latest/docs/ops/configuration/security/root-transition/"}, true, func(c *Check) {
		if e.runtimeNA(c, "certificate expiry (status.notAfter and the istiod root)") {
			return
		}
		now := k8sNow()
		for _, cert := range e.list("cert-manager.io", "certificates") {
			v := newVerdict()
			if st, reason, _, found := condition(cert, "Ready"); found && st == "False" {
				v.add(Fail, "Ready False ("+reason+")")
			}
			notAfter := s(path(cert.Obj, "status", "notAfter"))
			if t, err := time.Parse(time.RFC3339, notAfter); err == nil {
				days := int(t.Sub(now).Hours() / 24)
				switch {
				case days < 14:
					v.add(Fail, fmt.Sprintf("expires %s (%d days)", notAfter, days))
				case days < 30:
					v.add(Warn, fmt.Sprintf("expires %s (%d days)", notAfter, days))
				default:
					v.add(Pass, "")
				}
			}
			c.Items = append(c.Items, e.item(cert, v.st, v.detail("valid until "+notAfter)))
		}
		seen := map[[32]byte]bool{}
		for _, cm := range e.list("", "configmaps") {
			if cm.Name != "istio-ca-root-cert" {
				continue
			}
			data := s(path(cm.Obj, "data", "root-cert.pem"))
			if data == "" {
				continue
			}
			sum := sha256.Sum256([]byte(data))
			if seen[sum] {
				continue
			}
			seen[sum] = true
			rest := []byte(data)
			parsed := 0
			v := newVerdict()
			soonest := ""
			for {
				var blk *pem.Block
				blk, rest = pem.Decode(rest)
				if blk == nil {
					break
				}
				crt, err := x509.ParseCertificate(blk.Bytes)
				if err != nil {
					continue
				}
				parsed++
				days := int(crt.NotAfter.Sub(now).Hours() / 24)
				name := crt.Subject.CommonName
				if name == "" && len(crt.Subject.Organization) > 0 {
					name = crt.Subject.Organization[0]
				}
				when := fmt.Sprintf("root %q expires %s (%d days)", name, crt.NotAfter.Format("2006-01-02"), days)
				switch {
				case days < 30:
					v.add(Fail, when)
				case days < 90:
					v.add(Warn, when)
				default:
					if soonest == "" {
						soonest = when
					}
				}
			}
			if parsed == 0 {
				c.Items = append(c.Items, e.item(cm, NA, "root-cert.pem could not be parsed"))
				continue
			}
			c.Items = append(c.Items, e.item(cm, v.st, v.detail(strings.Replace(soonest, "expires", "valid until", 1))))
		}
		if len(c.Items) == 0 {
			na(c, "no cert-manager Certificates and no istio-ca-root-cert ConfigMap with certificate data in the capture")
		}
	})

	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range in {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func keys(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// haScope names the critical components whose replica count matters.
// strict components fail with one replica; the others warn.
func haScope(o *store.Object) (what string, strict bool) {
	if o.Kind != "Deployment" && o.Kind != "StatefulSet" {
		return "", false
	}
	lb := podTemplateLabels(o)
	switch istioSystemWorkload(o) {
	case "istiod":
		return "istiod", true
	case "gateway":
		if strings.Contains(lb["gateway.networking.k8s.io/gateway-class-name"], "waypoint") || lb["gateway.istio.io/managed"] == "istio.io-mesh-controller" || lb["istio.io/waypoint-for"] != "" {
			return "waypoint", false
		}
		return "gateway", true
	}
	var imgs []string
	for _, k := range kContainers(podSpec(o)) {
		if k.long() {
			imgs = append(imgs, s(k.c["image"]))
		}
	}
	name, app := o.Name, lb["app.kubernetes.io/name"]
	for _, n := range []string{"agentgateway", "enterprise-agentgateway", "kgateway", "enterprise-kgateway"} {
		if name == n || app == n {
			return "gateway controller", false
		}
	}
	for _, img := range imgs {
		base := imageBase(img)
		switch {
		case base == "kagent-enterprise-controller" || (base == "controller" && strings.Contains(img, "kagent")):
			return "kagent controller", false
		case strings.Contains(img, "agentregistry"):
			return "agentregistry", false
		case strings.Contains(img, "keycloak/keycloak"):
			return "keycloak", false
		}
	}
	return "", false
}

func minorOf(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 || parts[0] != "1" {
		return 0
	}
	digits := parts[1]
	for i, r := range digits {
		if r < '0' || r > '9' {
			digits = digits[:i]
			break
		}
	}
	n, _ := strconv.Atoi(digits)
	return n
}

// k8sEOL is the upstream end-of-life date per minor release, from
// https://kubernetes.io/releases/ (checked 2026-09-28; endoflife.date agrees
// except for 1.27/1.28, where it lists the scheduled rather than final date).
var k8sEOL = map[int]string{
	37: "2027-10-28", 36: "2027-06-28", 35: "2027-02-28", 34: "2026-10-27", 33: "2026-06-28",
	32: "2026-02-28", 31: "2025-11-11", 30: "2025-07-15", 29: "2025-02-28", 28: "2024-10-22",
	27: "2024-07-16", 26: "2024-02-28", 25: "2023-10-28", 24: "2023-07-28", 23: "2023-02-28",
}

func maxKnownMinor() int {
	mx := 0
	for k := range k8sEOL {
		if k > mx {
			mx = k
		}
	}
	return mx
}

type depInfo struct{ removed, replacement string }

func (d depInfo) text() string {
	if d.removed != "" {
		return "removed in Kubernetes " + d.removed + "; use " + d.replacement
	}
	return "deprecated; use " + d.replacement
}

// removedAPIs are group versions removed from Kubernetes, from the
// deprecated API migration guide.
var removedAPIs = map[string]depInfo{
	"extensions/v1beta1":                   {"1.22", "apps/v1 or networking.k8s.io/v1"},
	"apps/v1beta1":                         {"1.16", "apps/v1"},
	"apps/v1beta2":                         {"1.16", "apps/v1"},
	"networking.k8s.io/v1beta1":            {"1.22", "networking.k8s.io/v1"},
	"rbac.authorization.k8s.io/v1beta1":    {"1.22", "rbac.authorization.k8s.io/v1"},
	"rbac.authorization.k8s.io/v1alpha1":   {"1.22", "rbac.authorization.k8s.io/v1"},
	"admissionregistration.k8s.io/v1beta1": {"1.22", "admissionregistration.k8s.io/v1"},
	"apiextensions.k8s.io/v1beta1":         {"1.22", "apiextensions.k8s.io/v1"},
	"certificates.k8s.io/v1beta1":          {"1.22", "certificates.k8s.io/v1"},
	"coordination.k8s.io/v1beta1":          {"1.22", "coordination.k8s.io/v1"},
	"scheduling.k8s.io/v1beta1":            {"1.22", "scheduling.k8s.io/v1"},
	"batch/v1beta1":                        {"1.25", "batch/v1"},
	"policy/v1beta1":                       {"1.25", "policy/v1 (PodSecurityPolicy has no replacement: use Pod Security Admission)"},
	"autoscaling/v2beta1":                  {"1.25", "autoscaling/v2"},
	"autoscaling/v2beta2":                  {"1.26", "autoscaling/v2"},
	"discovery.k8s.io/v1beta1":             {"1.25", "discovery.k8s.io/v1"},
	"events.k8s.io/v1beta1":                {"1.25", "events.k8s.io/v1"},
	"node.k8s.io/v1beta1":                  {"1.25", "node.k8s.io/v1"},
	"flowcontrol.apiserver.k8s.io/v1beta1": {"1.26", "flowcontrol.apiserver.k8s.io/v1"},
	"flowcontrol.apiserver.k8s.io/v1beta2": {"1.29", "flowcontrol.apiserver.k8s.io/v1"},
	"flowcontrol.apiserver.k8s.io/v1beta3": {"1.32", "flowcontrol.apiserver.k8s.io/v1"},
}

func deprecatedAPI(apiVersion, kind string) (depInfo, bool) {
	if d, ok := removedAPIs[apiVersion]; ok {
		return d, true
	}
	switch apiVersion {
	case "storage.k8s.io/v1beta1":
		if kind == "CSIStorageCapacity" {
			return depInfo{"1.27", "storage.k8s.io/v1"}, true
		}
		return depInfo{"1.22", "storage.k8s.io/v1"}, true
	case "networking.istio.io/v1alpha3", "networking.istio.io/v1beta1":
		return depInfo{"", "networking.istio.io/v1"}, true
	case "security.istio.io/v1beta1":
		return depInfo{"", "security.istio.io/v1"}, true
	case "telemetry.istio.io/v1alpha1":
		return depInfo{"", "telemetry.istio.io/v1"}, true
	case "gateway.networking.k8s.io/v1beta1", "gateway.networking.k8s.io/v1alpha2":
		switch kind {
		case "Gateway", "GatewayClass", "HTTPRoute", "GRPCRoute":
			return depInfo{"", "gateway.networking.k8s.io/v1"}, true
		}
	}
	return depInfo{}, false
}
