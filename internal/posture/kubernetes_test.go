package posture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	collectpolicy "github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

var (
	k8s_rDeploy = res("apps", "deployments", "Deployment", true)
	rDS         = res("apps", "daemonsets", "DaemonSet", true)
	k8s_rPods   = res("", "pods", "Pod", true)
	rNS         = res("", "namespaces", "Namespace", false)
	k8s_rSvc    = res("", "services", "Service", true)
	rNP         = res("networking.k8s.io", "networkpolicies", "NetworkPolicy", true)
	k8s_rPDB    = res("policy", "poddisruptionbudgets", "PodDisruptionBudget", true)
	rHPA        = res("autoscaling", "horizontalpodautoscalers", "HorizontalPodAutoscaler", true)
	rRole       = res("rbac.authorization.k8s.io", "roles", "Role", true)
	rRB         = res("rbac.authorization.k8s.io", "rolebindings", "RoleBinding", true)
	rCR         = res("rbac.authorization.k8s.io", "clusterroles", "ClusterRole", false)
	rCRB        = res("rbac.authorization.k8s.io", "clusterrolebindings", "ClusterRoleBinding", false)
	rSec        = res("", "secrets", "Secret", true)
	rCM         = res("", "configmaps", "ConfigMap", true)
	rSA         = res("", "serviceaccounts", "ServiceAccount", true)
	k8s_rAP     = res("security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true)
)

// k8sWS builds a workspace with the standard profile, optionally with
// cluster-scoped capture and metadata-only detail.
func k8sWS(t *testing.T, clusterScoped, metadata bool) *store.Workspace {
	t.Helper()
	ws := store.New()
	p, _ := collectpolicy.Profile("standard")
	p.ClusterScoped = clusterScoped
	if metadata {
		p.ObjectDetail = collectpolicy.DetailMetadata
	}
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	all := []bundle.APIResource{k8s_rDeploy, rDS, k8s_rPods, rNS, k8s_rSvc, rNP, k8s_rPDB, rHPA, rRole, rRB, rSec, rCM, rSA, k8s_rAP}
	if clusterScoped {
		all = append(all, rCR, rCRB)
	}
	for _, r := range all {
		complete(ws, r)
	}
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete; s.Verified = true })
	return ws
}

func ctr(name, image string, sc map[string]any) map[string]any {
	c := map[string]any{"name": name, "image": image,
		"resources":      map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "10Mi"}, "limits": map[string]any{"memory": "20Mi"}},
		"readinessProbe": map[string]any{}, "livenessProbe": map[string]any{}}
	if sc != nil {
		c["securityContext"] = sc
	}
	return c
}

func hardened() map[string]any {
	return map[string]any{"allowPrivilegeEscalation": false, "runAsNonRoot": true, "readOnlyRootFilesystem": true,
		"capabilities": map[string]any{"drop": []any{"ALL"}}, "seccompProfile": map[string]any{"type": "RuntimeDefault"}}
}

func workload(kind, ns, name string, lbl map[string]any, pod map[string]any, replicas int) map[string]any {
	if lbl == nil {
		lbl = map[string]any{"app": name}
	}
	sp := map[string]any{"selector": map[string]any{"matchLabels": lbl}, "template": map[string]any{"metadata": map[string]any{"labels": lbl}, "spec": pod}}
	if kind == "Deployment" {
		sp["replicas"] = float64(replicas)
	}
	return obj(kind, ns, name, nil, sp)
}

func podOf(ns, name string, lbl map[string]any, ps map[string]any, node string) map[string]any {
	o := obj("Pod", ns, name, lbl, ps)
	ps["nodeName"] = node
	m(o["metadata"])["ownerReferences"] = []any{map[string]any{"kind": "ReplicaSet", "name": name + "-rs", "controller": true}}
	return o
}

func itemStatus(t *testing.T, c Check, name string) (Status, string) {
	t.Helper()
	for _, it := range c.Items {
		if it.Name == name {
			return it.Status, it.Detail
		}
	}
	t.Fatalf("%s: no item for %s: %+v", c.ID, name, c.Items)
	return "", ""
}

func k8sCheck(t *testing.T, ws *store.Workspace, id string) Check {
	t.Helper()
	for _, c := range kubernetesChecks(env{ws: ws, cluster: "c"}) {
		if c.ID == id {
			finish(&c)
			return c
		}
	}
	t.Fatalf("no check %s", id)
	return Check{}
}

func k8s_expect(t *testing.T, c Check, name string, want Status, contains string) {
	t.Helper()
	st, d := itemStatus(t, c, name)
	if st != want || !strings.Contains(d, contains) {
		t.Errorf("%s %s: got %s %q, want %s containing %q", c.ID, name, st, d, want, contains)
	}
}

func TestKubernetesWorkloadSecurity(t *testing.T) {
	ws := k8sWS(t, false, false)
	zt := hardened()
	zt["capabilities"] = map[string]any{"add": []any{"NET_ADMIN", "SYS_ADMIN", "NET_RAW"}, "drop": []any{"ALL"}}
	zt["runAsUser"], zt["runAsNonRoot"], zt["allowPrivilegeEscalation"] = float64(0), false, true
	ztPod := func() map[string]any {
		return map[string]any{"serviceAccountName": "ztunnel", "containers": []any{ctr("istio-proxy", "istio/ztunnel:1.29.2", zt)},
			"volumes": []any{map[string]any{"name": "sock", "hostPath": map[string]any{"path": "/var/run/ztunnel"}}}}
	}
	ztBad := ztPod()
	ztBad["hostNetwork"] = true
	bad := map[string]any{"privileged": true, "allowPrivilegeEscalation": true, "runAsUser": float64(0),
		"capabilities": map[string]any{"add": []any{"NET_ADMIN"}}, "seccompProfile": map[string]any{"type": "Unconfined"}}
	good := map[string]any{"securityContext": map[string]any{"runAsNonRoot": true}, "serviceAccountName": "shop",
		"containers": []any{ctr("app", "shop:1.0@sha256:abc", hardened())}}
	ws.AddObjects("c", rDS, []map[string]any{
		workload("DaemonSet", "istio-system", "ztunnel", map[string]any{"app": "ztunnel"}, ztPod(), 0),
		workload("DaemonSet", "rogue", "ztunnel", map[string]any{"app": "ztunnel"}, ztBad, 0),
	}, "ds")
	ws.AddObjects("c", k8s_rDeploy, []map[string]any{
		workload("Deployment", "shop", "evil", nil, map[string]any{"containers": []any{ctr("app", "evil:latest", bad)},
			"volumes": []any{map[string]any{"name": "d", "hostPath": map[string]any{"path": "/var/run/docker.sock"}}}}, 1),
		workload("Deployment", "shop", "good", nil, good, 1),
		workload("Deployment", "kube-system", "provider", nil, map[string]any{"containers": []any{ctr("x", "x:latest", bad)}}, 1),
	}, "d")
	ws.AddObjects("c", k8s_rPods, []map[string]any{podOf("shop", "good-1", map[string]any{"app": "good"}, good, "n1")}, "p")

	c := k8sCheck(t, ws, "k8s.no-privileged")
	k8s_expect(t, c, "evil", Fail, "privileged")
	k8s_expect(t, c, "good", Pass, "")
	for _, it := range c.Items {
		if it.Namespace == "kube-system" {
			t.Fatal("platform namespaces must be excluded")
		}
	}
	c = k8sCheck(t, ws, "k8s.capabilities")
	k8s_expect(t, c, "evil", Fail, "NET_ADMIN")
	for _, it := range c.Items {
		if it.Name == "ztunnel" && it.Status != Pass {
			t.Errorf("ztunnel with NET_ADMIN must not be flagged: %+v", it)
		}
	}
	c = k8sCheck(t, ws, "k8s.host-access")
	k8s_expect(t, c, "evil", Fail, "/var/run/docker.sock")
	var ztNet, ztOK bool
	for _, it := range c.Items {
		if it.Name == "ztunnel" && it.Namespace == "rogue" {
			ztNet = it.Status == Fail && strings.Contains(it.Detail, "hostNetwork")
		}
		if it.Name == "ztunnel" && it.Namespace == "istio-system" {
			ztOK = it.Status == Pass
		}
	}
	if !ztNet || !ztOK {
		t.Errorf("ztunnel host access: hostNetwork flagged=%v, expected mounts passed=%v %+v", ztNet, ztOK, c.Items)
	}
	c = k8sCheck(t, ws, "k8s.privilege-escalation")
	k8s_expect(t, c, "evil", Fail, "true")
	k8s_expect(t, c, "good", Pass, "")
	c = k8sCheck(t, ws, "k8s.run-as-non-root")
	k8s_expect(t, c, "evil", Fail, "uid 0")
	k8s_expect(t, c, "good", Pass, "")
	c = k8sCheck(t, ws, "k8s.seccomp")
	k8s_expect(t, c, "evil", Fail, "Unconfined")
	k8s_expect(t, c, "good", Pass, "")
	c = k8sCheck(t, ws, "k8s.image-pinned")
	k8s_expect(t, c, "evil", Fail, "latest")
	k8s_expect(t, c, "good", Pass, "digest")
	c = k8sCheck(t, ws, "k8s.default-serviceaccount")
	k8s_expect(t, c, "evil", Warn, "default")
	k8s_expect(t, c, "good", Pass, "shop")
}

func TestKubernetesResilience(t *testing.T) {
	ws := k8sWS(t, false, false)
	istiod := map[string]any{"serviceAccountName": "istiod", "containers": []any{ctr("discovery", "istio/pilot:1.29", hardened())}}
	noReq := map[string]any{"containers": []any{map[string]any{"name": "app", "image": "a:1"}}}
	ws.AddObjects("c", k8s_rDeploy, []map[string]any{
		workload("Deployment", "istio-system", "istiod", map[string]any{"app": "istiod"}, istiod, 1),
		workload("Deployment", "istio-alt", "istiod", map[string]any{"app": "istiod"}, istiod, 2),
		workload("Deployment", "shop", "bare", nil, noReq, 1),
	}, "d")
	ws.AddObjects("c", k8s_rPods, []map[string]any{
		podOf("istio-alt", "istiod-a", map[string]any{"app": "istiod"}, map[string]any{"containers": []any{}}, "n1"),
		podOf("istio-alt", "istiod-b", map[string]any{"app": "istiod"}, map[string]any{"containers": []any{}}, "n1"),
	}, "p")
	c := k8sCheck(t, ws, "k8s.ha-replicas")
	for _, it := range c.Items {
		want := map[string]Status{"istio-system": Fail, "istio-alt": Pass}[it.Namespace]
		if it.Status != want {
			t.Errorf("ha %s: %s %s", it.Namespace, it.Status, it.Detail)
		}
	}
	c = k8sCheck(t, ws, "k8s.pdb")
	if c.Status != Fail || !strings.Contains(c.Items[0].Detail, "no PodDisruptionBudget") {
		t.Errorf("pdb: %+v", c)
	}
	ws.AddObjects("c", k8s_rPDB, []map[string]any{obj("PodDisruptionBudget", "istio-alt", "istiod", nil,
		map[string]any{"minAvailable": float64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": "istiod"}}})}, "pdb")
	if c = k8sCheck(t, ws, "k8s.pdb"); c.Status != Pass {
		t.Errorf("pdb with a budget: %+v", c)
	}
	c = k8sCheck(t, ws, "k8s.spread")
	k8s_expect(t, c, "istiod", Fail, "n1")
	c = k8sCheck(t, ws, "k8s.resource-requests")
	k8s_expect(t, c, "bare", Fail, "requests.cpu")
	c = k8sCheck(t, ws, "k8s.probes")
	k8s_expect(t, c, "bare", Fail, "no readiness")
}

func TestKubernetesIsolation(t *testing.T) {
	ws := k8sWS(t, false, false)
	ws.AddObjects("c", rNS, []map[string]any{
		obj("Namespace", "", "shop", map[string]any{"pod-security.kubernetes.io/enforce": "restricted"}, nil),
		obj("Namespace", "", "ai", map[string]any{"istio.io/dataplane-mode": "ambient"}, nil),
		obj("Namespace", "", "istio-system", nil, nil),
	}, "ns")
	ws.AddObjects("c", rDS, []map[string]any{workload("DaemonSet", "istio-system", "ztunnel", map[string]any{"app": "ztunnel"},
		map[string]any{"containers": []any{ctr("istio-proxy", "ztunnel:1", nil)}}, 0)}, "ds")
	ws.AddObjects("c", k8s_rPods, []map[string]any{
		obj("Pod", "shop", "cart", map[string]any{"app": "cart"}, map[string]any{}),
		obj("Pod", "ai", "agent", map[string]any{"app": "agent"}, map[string]any{}),
		obj("Pod", "ai", "gw", map[string]any{"app": "gw"}, map[string]any{}),
		obj("Pod", "istio-system", "ztunnel-x", map[string]any{"app": "ztunnel"}, map[string]any{}),
	}, "p")
	deny := map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}}
	ws.AddObjects("c", rNP, []map[string]any{obj("NetworkPolicy", "shop", "deny", nil, deny), obj("NetworkPolicy", "ai", "deny", nil, deny)}, "np")
	ws.AddObjects("c", k8s_rSvc, []map[string]any{
		obj("Service", "ai", "gw", map[string]any{"gateway.networking.k8s.io/gateway-name": "gw"}, map[string]any{"type": "LoadBalancer", "selector": map[string]any{"app": "gw"}, "ports": []any{map[string]any{"port": float64(443)}}}),
		obj("Service", "ai", "agent-lb", nil, map[string]any{"type": "LoadBalancer", "selector": map[string]any{"app": "agent"}, "ports": []any{map[string]any{"port": float64(8080)}}}),
		obj("Service", "ai", "debug", nil, map[string]any{"type": "NodePort", "selector": map[string]any{"app": "agent"}, "ports": []any{map[string]any{"port": float64(15000)}}}),
	}, "svc")

	c := k8sCheck(t, ws, "k8s.pod-security-admission")
	k8s_expect(t, c, "shop", Pass, "restricted")
	k8s_expect(t, c, "ai", Fail, "no enforce label")
	k8s_expect(t, c, "istio-system", Pass, "ztunnel")
	c = k8sCheck(t, ws, "k8s.netpol-default-deny")
	k8s_expect(t, c, "shop", Pass, "deny")
	c = k8sCheck(t, ws, "k8s.netpol-ambient-compatible")
	k8s_expect(t, c, "deny", Fail, "15008")
	ws.AddObjects("c", rNP, []map[string]any{obj("NetworkPolicy", "ai", "mesh", nil, map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"},
		"ingress": []any{map[string]any{"ports": []any{map[string]any{"port": float64(15008), "protocol": "TCP"}}},
			map[string]any{"from": []any{map[string]any{"ipBlock": map[string]any{"cidr": "169.254.7.127/32"}}}}}})}, "np2")
	if c = k8sCheck(t, ws, "k8s.netpol-ambient-compatible"); c.Status != Pass {
		t.Errorf("ambient-compatible with allowances: %+v", c.Items)
	}
	c = k8sCheck(t, ws, "k8s.service-exposure")
	k8s_expect(t, c, "gw", Pass, "gateway")
	k8s_expect(t, c, "agent-lb", Fail, "any source")
	k8s_expect(t, c, "debug", Fail, "15000")
}

func TestKubernetesIdentityAndClusterScope(t *testing.T) {
	ws := k8sWS(t, false, false)
	secretRead := []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"get", "list"}}}
	ws.AddObjects("c", rRole, []map[string]any{
		obj("Role", "shop", "reader", nil, nil),
		obj("Role", "istio-system", "istiod", map[string]any{"app.kubernetes.io/part-of": "istio"}, nil),
		obj("Role", "shop", "god", nil, nil),
	}, "r")
	for _, o := range ws.List("c", "rbac.authorization.k8s.io", "roles", "", nil) {
		o.Obj["rules"] = secretRead
		if o.Name == "god" {
			o.Obj["rules"] = []any{map[string]any{"apiGroups": []any{"*"}, "resources": []any{"*"}, "verbs": []any{"*"}}}
		}
	}
	binding := func(kind, ns, name, roleKind, role, saNS, sa string) map[string]any {
		return map[string]any{"kind": kind, "metadata": map[string]any{"name": name, "namespace": ns},
			"roleRef": map[string]any{"kind": roleKind, "name": role}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": sa, "namespace": saNS}}}
	}
	ws.AddObjects("c", rRB, []map[string]any{
		binding("RoleBinding", "shop", "reader", "Role", "reader", "shop", "app"),
		binding("RoleBinding", "istio-system", "istiod", "Role", "istiod", "istio-system", "istiod"),
		binding("RoleBinding", "shop", "god", "Role", "god", "shop", "ops"),
	}, "rb")
	for _, o := range ws.List("c", "rbac.authorization.k8s.io", "rolebindings", "istio-system", nil) {
		m(o.Obj["metadata"])["labels"] = map[string]any{"app.kubernetes.io/part-of": "istio"}
	}
	c := k8sCheck(t, ws, "k8s.rbac-secret-read")
	k8s_expect(t, c, "reader", Warn, "every Secret in shop")
	k8s_expect(t, c, "istiod", Pass, "by design")
	c = k8sCheck(t, ws, "k8s.rbac-wildcards")
	k8s_expect(t, c, "god", Fail, "verbs *")
	k8s_expect(t, c, "reader", Pass, "no wildcards")
	c = k8sCheck(t, ws, "k8s.rbac-escalation")
	k8s_expect(t, c, "god", Fail, "create pods together with Secret read")
	c = k8sCheck(t, ws, "k8s.cluster-admin-bindings")
	if c.Status != NA || !strings.Contains(c.Reason, "--cluster-scoped") {
		t.Errorf("cluster-admin without cluster-scoped capture must be n/a with the flag: %+v", c)
	}
	if c = k8sCheck(t, ws, "k8s.node-health"); c.Status != NA || !strings.Contains(c.Reason, "--cluster-scoped") {
		t.Errorf("node health without nodes: %+v", c)
	}

	ws2 := k8sWS(t, true, false)
	ws2.AddObjects("c", rCRB, []map[string]any{
		binding("ClusterRoleBinding", "", "ci-admin", "ClusterRole", "cluster-admin", "ci", "deployer"),
		binding("ClusterRoleBinding", "", "system:kcm", "ClusterRole", "cluster-admin", "kube-system", "x"),
	}, "crb")
	c = k8sCheck(t, ws2, "k8s.cluster-admin-bindings")
	k8s_expect(t, c, "ci-admin", Fail, "ci/deployer")
	if len(c.Items) != 1 {
		t.Errorf("system bindings must be skipped: %+v", c.Items)
	}

	// Literal credential in env.
	ws.AddObjects("c", k8s_rDeploy, []map[string]any{workload("Deployment", "shop", "db", nil, map[string]any{"containers": []any{
		map[string]any{"name": "pg", "image": "pg:1", "env": []any{map[string]any{"name": "DB_PASSWORD", "value": "[REDACTED]"}, map[string]any{"name": "TOKEN_URL", "value": "[REDACTED]"}}}}}, 1),
		workload("Deployment", "shop", "clean", nil, map[string]any{"containers": []any{map[string]any{"name": "a", "image": "a:1"}}}, 1)}, "d")
	c = k8sCheck(t, ws, "k8s.secrets-in-env")
	k8s_expect(t, c, "db", Fail, "DB_PASSWORD")
	k8s_expect(t, c, "clean", Pass, "")
	if _, d := itemStatus(t, c, "db"); strings.Contains(d, "TOKEN_URL") {
		t.Errorf("a setting name must not be reported: %s", d)
	}
}

func TestKubernetesPlatform(t *testing.T) {
	defer func(f func() time.Time) { k8sNow = f }(k8sNow)
	k8sNow = func() time.Time { return time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) }
	ver := func(v string) Status {
		ws := k8sWS(t, false, false)
		ws.SetVersion("c", json.RawMessage(`{"gitVersion":"`+v+`"}`))
		c := k8sCheck(t, ws, "k8s.version-supported")
		return c.Status
	}
	if ver("v1.35.0") != Pass || ver("v1.34.2") != Warn || ver("v1.30.1") != Fail || ver("v1.30.1-eks-abc") != Warn {
		t.Errorf("version: 1.35 %s, 1.34 %s, 1.30 %s, 1.30-eks %s", ver("v1.35.0"), ver("v1.34.2"), ver("v1.30.1"), ver("v1.30.1-eks-abc"))
	}

	ws := k8sWS(t, false, false)
	helm := func(ns, name, v, st string) map[string]any {
		return map[string]any{"kind": "Secret", "type": "helm.sh/release.v1", "metadata": map[string]any{"name": "sh.helm.release.v1." + name + ".v" + v, "namespace": ns,
			"labels": map[string]any{"name": name, "version": v, "status": st, "owner": "helm"}}}
	}
	ws.AddObjects("c", rSec, []map[string]any{helm("a", "app", "1", "deployed"), helm("a", "app", "2", "failed"), helm("b", "ok", "3", "deployed"), helm("b", "ok", "2", "superseded")}, "s")
	c := k8sCheck(t, ws, "k8s.helm-releases")
	if len(c.Items) != 2 {
		t.Fatalf("one item per release: %+v", c.Items)
	}
	k8s_expect(t, c, "sh.helm.release.v1.app.v2", Fail, "failed")
	k8s_expect(t, c, "sh.helm.release.v1.ok.v3", Pass, "deployed")

	// Istio root certificate close to expiry.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: k8sNow().Add(-time.Hour),
		NotAfter: k8sNow().Add(10 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	pemData := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	cm := map[string]any{"kind": "ConfigMap", "metadata": map[string]any{"name": "istio-ca-root-cert", "namespace": "shop"}, "data": map[string]any{"root-cert.pem": pemData}}
	cm2 := map[string]any{"kind": "ConfigMap", "metadata": map[string]any{"name": "istio-ca-root-cert", "namespace": "ai"}, "data": map[string]any{"root-cert.pem": pemData}}
	ws.AddObjects("c", rCM, []map[string]any{cm, cm2}, "cm")
	c = k8sCheck(t, ws, "k8s.cert-expiry")
	if len(c.Items) != 1 || c.Items[0].Status != Fail || !strings.Contains(c.Items[0].Detail, "10 days") {
		t.Errorf("root cert expiring in 10 days, deduplicated: %+v", c.Items)
	}

	// Deprecated API in last-applied-configuration.
	d := workload("Deployment", "shop", "old", nil, map[string]any{"containers": []any{}}, 1)
	m(d["metadata"])["annotations"] = map[string]any{"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"extensions/v1beta1","kind":"Deployment"}`}
	ws.AddObjects("c", k8s_rDeploy, []map[string]any{d}, "d")
	c = k8sCheck(t, ws, "k8s.deprecated-apis")
	k8s_expect(t, c, "old", Fail, "removed in Kubernetes 1.22")
	if !strings.Contains(c.Reason, "checked 1 objects") {
		t.Errorf("coverage must be stated: %q", c.Reason)
	}
}

func TestKubernetesMetadataOnly(t *testing.T) {
	ws := k8sWS(t, false, true)
	ws.AddObjects("c", k8s_rPods, []map[string]any{obj("Pod", "shop", "cart", nil, nil)}, "p")
	e := env{ws: ws, cluster: "c"}
	if isKubernetes(e) {
		t.Error("metadata-only capture must not count as present")
	}
	for _, c := range kubernetesChecks(e) {
		if c.ID == "k8s.no-privileged" || c.ID == "k8s.capabilities" || c.ID == "k8s.resource-requests" {
			if c.Status != NA || !strings.Contains(c.Reason, "--object-detail full") {
				t.Errorf("%s under metadata detail: %s %q", c.ID, c.Status, c.Reason)
			}
		}
	}
	full := k8sWS(t, false, false)
	full.AddObjects("c", k8s_rPods, []map[string]any{obj("Pod", "shop", "cart", nil, map[string]any{})}, "p")
	r := Evaluate(full)
	for _, a := range r.Areas {
		if a.Area == "kubernetes" && !a.Present {
			t.Errorf("full-detail pods must make the area present: %+v", a)
		}
	}
}
