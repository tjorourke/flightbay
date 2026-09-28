package posture

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// declaredWS is a repo scan: a Deployment and a policy, no pods, no status.
func declaredWS(t *testing.T) *store.Workspace {
	ws := store.New()
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Source: bundle.SourceRepo, Repo: &bundle.RepoInfo{Name: "r"}})
	dep := bundle.APIResource{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true}
	ns := res("", "namespaces", "Namespace", false)
	pol := res("agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", true)
	for _, r := range []bundle.APIResource{dep, ns, pol} {
		complete(ws, r)
	}
	ws.AddObjects("c", ns, []map[string]any{obj("Namespace", "", "shop", nil, nil)}, "n")
	ws.AddObjects("c", dep, []map[string]any{{"kind": "Deployment", "apiVersion": "apps/v1",
		"metadata": map[string]any{"name": "cart", "namespace": "shop", "annotations": map[string]any{"flightbay.dev/source": "k8s/cart.yaml"}},
		"spec": map[string]any{"replicas": float64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": "cart"}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "cart"}},
				"spec": map[string]any{"containers": []any{map[string]any{"name": "cart", "image": "cart:1", "securityContext": map[string]any{"privileged": true}}}}}}}}, "d")
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete; s.Verified = true })
	return ws
}

func TestDeclaredStateIsNotACapture(t *testing.T) {
	r := Evaluate(declaredWS(t))
	k := false
	for _, a := range r.Areas {
		if a.Area == "kubernetes" && a.Present {
			k = true
		}
	}
	if !k {
		t.Fatal("the Kubernetes area should be present from workload templates alone")
	}
	for _, id := range []string{"k8s.node-health", "k8s.version-supported", "k8s.helm-releases", "k8s.cert-expiry"} {
		c := find(r, id)
		if c.Status != NA || !strings.HasPrefix(c.Reason, DeclaredWhy) {
			t.Errorf("%s = %s %q, want N/A: %s", id, c.Status, c.Reason, DeclaredWhy)
		}
	}
	// pod-spec checks run on the template and name the Deployment
	priv := Check{}
	for _, c := range r.Checks {
		for _, it := range c.Items {
			if it.Status == Fail && strings.Contains(strings.ToLower(it.Detail), "privileged") {
				priv = c
			}
		}
	}
	if priv.ID == "" {
		t.Fatal("the privileged container in the template should fail a check")
	}
	for _, it := range priv.Items {
		if it.Status == Fail && (it.Kind != "Deployment" || it.Group != "apps" || it.Name != "cart") {
			t.Errorf("%s item = %+v, want the Deployment", priv.ID, it)
		}
	}
	for _, c := range r.Checks {
		if strings.Contains(c.Reason, "not captured") {
			t.Errorf("%s: %q on a repo scan", c.ID, c.Reason)
		}
	}
}

// The same objects in a capture are judged as a capture: no N/A for
// declared state.
func TestCaptureIsNotDeclared(t *testing.T) {
	ws := declaredWS(t)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}})
	r := Evaluate(ws)
	if c := find(r, "k8s.helm-releases"); strings.HasPrefix(c.Reason, DeclaredWhy) {
		t.Errorf("a capture should not say %q", c.Reason)
	}
}
