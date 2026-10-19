package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfiles(t *testing.T) {
	min, err := Profile("minimal")
	if err != nil {
		t.Fatal(err)
	}
	if !min.Has(CatObjects) || !min.Has(CatEvents) || min.Has(CatLogs) || min.ObjectDetail != DetailMetadata {
		t.Fatalf("minimal profile wrong: %+v", min)
	}
	std, _ := Profile("standard")
	if !std.Has(CatLogs) || std.Has(CatProxies) || std.Has(CatNodes) || std.ClusterScoped || std.Secrets != "keys" || std.ConfigMaps != "scrubbed" {
		t.Fatalf("standard profile wrong: %+v", std)
	}
	custom, _ := Profile("custom")
	if len(custom.Categories) != 0 {
		t.Fatalf("custom should preselect nothing: %+v", custom.Categories)
	}
	if _, err := Profile("everything"); err == nil {
		t.Fatal("expected unknown profile error")
	}
}

func TestConfigFileThenOverridesAndExclusionsWin(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "collection.yaml")
	_ = os.WriteFile(cfg, []byte(`
profile: standard
namespaces: ["payments", "istio-*"]
excludeNamespaces: ["istio-canary"]
resources: ["pods", "configmaps", "httproutes.gateway.networking.k8s.io"]
excludeResources: ["configmaps"]
logs:
  since: 2h
  maxBytes: 1048576
`), 0o600)
	p, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Logs.Since != "2h" || p.Logs.MaxBytes != 1<<20 {
		t.Fatalf("logs config not applied: %+v", p.Logs)
	}
	p.Namespaces = append(p.Namespaces, "istio-canary") // an override cannot beat an exclusion
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !p.NamespaceAllowed("istio-system") || !p.NamespaceAllowed("payments") {
		t.Fatal("included namespaces rejected")
	}
	if p.NamespaceAllowed("istio-canary") || p.NamespaceAllowed("default") {
		t.Fatal("excluded or unlisted namespace allowed")
	}
	if !p.ResourceAllowed("pods", "", "Pod", true) || !p.ResourceAllowed("httproutes", "gateway.networking.k8s.io", "HTTPRoute", true) {
		t.Fatal("allowlisted resource rejected")
	}
	if p.ResourceAllowed("configmaps", "", "ConfigMap", true) {
		t.Fatal("excluded resource allowed")
	}
	if p.ResourceAllowed("services", "", "Service", true) {
		t.Fatal("unlisted resource allowed")
	}
}

func TestClusterScopedNeedsOptIn(t *testing.T) {
	p, _ := Profile("standard")
	if p.ResourceAllowed("namespaces", "", "Namespace", false) {
		t.Fatal("cluster-scoped allowed without opt-in")
	}
	p.ClusterScoped = true
	if !p.ResourceAllowed("clusterroles", "rbac.authorization.k8s.io", "ClusterRole", false) {
		t.Fatal("cluster-scoped rejected after opt-in")
	}
}

func TestSecretsOmitExcludesResource(t *testing.T) {
	p, _ := Profile("standard")
	p.Secrets = "omit"
	if p.ResourceAllowed("secrets", "", "Secret", true) {
		t.Fatal("secrets omitted but resource allowed")
	}
	p.Secrets = "values"
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("expected secrets validation error, got %v", err)
	}
}

func TestPlanExplainsUnavailableDiagnostics(t *testing.T) {
	p, _ := Profile("standard")
	p.Categories = append(p.Categories, CatProxies)
	p.PortForward = false
	plan := p.Plan()
	if plan.Selected(CatProxies) {
		t.Fatal("proxies should be unavailable without port-forward")
	}
	if len(plan.Unavailable) == 0 || !strings.Contains(plan.Unavailable[0], "port-forward") {
		t.Fatalf("expected explanation, got %v", plan.Unavailable)
	}
	if plan.Selected(CatNodes) {
		t.Fatal("nodes were never requested")
	}
}

func TestLogStreams(t *testing.T) {
	p, _ := Profile("standard")
	if !p.Stream("current") || !p.Stream("previous") || p.Stream("ephemeral") {
		t.Fatalf("unexpected default streams %v", p.Logs.Streams)
	}
	p.Logs.Streams = []string{"sideways"}
	if err := p.Validate(); err == nil {
		t.Fatal("expected invalid stream error")
	}
}

func TestRBACNamespacedAndMinimal(t *testing.T) {
	p, _ := Profile("standard")
	p.Namespaces = []string{"payments"}
	p.Resources = []string{"pods", "deployments.apps"}
	p.Logs.Streams = []string{"current"}
	out := p.RBAC(nil)
	for _, want := range []string{"kind: Role", "namespace: payments", "pods/log", "- deployments", "- apps", "kind: RoleBinding"} {
		if !strings.Contains(out, want) {
			t.Fatalf("RBAC missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"'*'", "portforward"} {
		if strings.Contains(out, bad) {
			t.Fatalf("RBAC should not contain %q:\n%s", bad, out)
		}
	}
	// cluster reader for the RBAC types (roles etc) + the namespace reader
	if strings.Count(out, "kind: ClusterRole\nmetadata") != 2 || !strings.Contains(out, "name: flightbay-cluster-reader") ||
		!strings.Contains(out, "name: flightbay-namespace-reader") ||
		!strings.Contains(out, "resourceNames:\n    - payments\n    verbs: [get]") {
		t.Fatalf("expected cluster-reader (for RBAC ids) + namespace-reader:\n%s", out)
	}
}

func TestRBACNoLogsNoPodLog(t *testing.T) {
	p, _ := Profile("minimal")
	p.Resources = []string{"pods"}
	if strings.Contains(p.RBAC(nil), "pods/log") {
		t.Fatal("pods/log granted without logs")
	}
}

func TestRBACWildcardFlagsExclusions(t *testing.T) {
	p, _ := Profile("standard")
	p.ExcludeResources = []string{"configmaps"}
	out := p.RBAC(nil)
	if !strings.Contains(out, "kind: ClusterRole") || !strings.Contains(out, "'*'") {
		t.Fatalf("expected wildcard ClusterRole:\n%s", out)
	}
	if !strings.Contains(out, "cannot express exclusions") {
		t.Fatalf("expected an exclusion warning:\n%s", out)
	}
	out = p.RBAC([]APIResource{{Group: "", Resource: "pods", Kind: "Pod", Namespaced: true}, {Group: "", Resource: "configmaps", Kind: "ConfigMap", Namespaced: true}})
	if strings.Contains(out, "configmaps") || strings.Contains(out, "'*'") {
		t.Fatalf("discovered RBAC should be explicit and exclude configmaps:\n%s", out)
	}
}

func TestPortForwardRBACOnlyWhenNeeded(t *testing.T) {
	p, _ := Profile("standard")
	p.Categories = append(p.Categories, CatProxies)
	p.PortForward = true
	if !strings.Contains(p.RBAC(nil), "pods/portforward") {
		t.Fatal("portforward missing")
	}
}

func TestHelmNeedsSecretKeys(t *testing.T) {
	p, _ := Profile("standard")
	if !p.Plan().Selected(CatHelm) {
		t.Fatal("standard should include helm")
	}
	p.Secrets = "metadata"
	if p.Plan().Selected(CatHelm) {
		t.Fatal("helm must be unavailable in metadata-only secret mode")
	}
}

func TestRBACGrantsOnlySelectedCRDDefinitions(t *testing.T) {
	p, _ := Profile("standard")
	p.Namespaces = []string{"payments"}
	out := p.RBAC([]APIResource{
		{Group: "", Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true},
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true},
		{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true},
	})
	if !strings.Contains(out, "name: flightbay-crd-reader") || !strings.Contains(out, "- httproutes.gateway.networking.k8s.io") {
		t.Fatalf("CRD rule missing:\n%s", out)
	}
	if strings.Contains(out, "deployments.apps") {
		t.Fatalf("built-in group listed as a CRD:\n%s", out)
	}
	if !strings.Contains(out, "verbs: [get]") {
		t.Fatalf("CRD rule must be get only:\n%s", out)
	}
	p.Resources = []string{"pods", "deployments.apps"}
	if out := p.RBAC(nil); strings.Contains(out, "customresourcedefinitions") {
		t.Fatalf("no custom resources selected, yet CRDs granted:\n%s", out)
	}
}

func TestRBACIdentityWithoutClusterScoped(t *testing.T) {
	p, _ := Profile("standard")
	p.ClusterScoped = false
	p.Resources = []string{"pods"}
	if !p.RBACIdentityAllowed("clusterroles", "rbac.authorization.k8s.io") {
		t.Fatal("clusterroles identity allowed without cluster scoped or resources list")
	}
	if !p.RBACIdentityAllowed("serviceaccounts", "") {
		t.Fatal("sa allowed")
	}
	out := p.RBAC(nil)
	if !strings.Contains(out, "clusterroles") || !strings.Contains(out, "rolebindings") {
		t.Fatalf("rbac identities missing from yaml:\n%s", out)
	}
	if strings.Contains(out, "create") || strings.Contains(out, "update") {
		t.Fatal("no write verbs on rbac identities")
	}
	p.ExcludeResources = []string{"clusterroles"}
	if p.RBACIdentityAllowed("clusterroles", "rbac.authorization.k8s.io") {
		t.Fatal("excluded should be false")
	}
	out = p.RBAC(nil)
	if strings.Contains(out, "clusterroles") && !strings.Contains(out, "clusterrolebindings") {
		// still may have others
	}
	if strings.Contains(out, "name: flightbay-reader") && strings.Contains(out, "clusterroles") {
		t.Fatalf("excluded clusterroles should be omitted from rbac:\n%s", out)
	}
}
