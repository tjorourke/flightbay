package ambient

import (
	"strings"
	"testing"
)

func TestL4ServiceAccountTask(t *testing.T) {
	ws := fixture(t)
	ns := Target(ws, "c", "Namespace", "", "app")
	delete(M(Path(ns.Obj, "metadata", "labels")), "istio.io/use-waypoint")
	put(ws, "", "serviceaccounts", "ServiceAccount", object("ServiceAccount", "client", nil))
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"l4-allow-service-account"}, SourceServiceAccount: "app/client"}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.YAML, "targetRefs") || !strings.Contains(p.YAML, "serviceAccounts:") || !strings.Contains(p.YAML, "app/client") {
		t.Fatal(p.YAML)
	}
	policy := ParseAuthzPolicy(Ref{Namespace: "app"}, M(p.Objects[0]["spec"]), "ztunnel")
	for _, tc := range []struct {
		sa     string
		status int
	}{{"client", 200}, {"other", 403}} {
		v := decide([]AuthzPolicy{policy}, reqModel{Principal: "cluster.local/ns/app/sa/" + tc.sa, Namespace: "app", SourceKnown: true})
		if !v.Known || v.Status != tc.status {
			t.Fatal(v)
		}
	}
	capturePlanObjects(t, ws, p)
	if again, err := Generate(ws, req); err != nil || len(again.Objects)+len(again.Changes) != 0 {
		t.Fatalf("repeat %v %+v", err, again)
	}
	M(Path(ns.Obj, "metadata", "labels"))["istio.io/use-waypoint"] = "wp"
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("direct-path task enabled behind waypoint")
	}
}

func TestMeshStrictRequiresRootEvidenceAndExplicitDefaultEdit(t *testing.T) {
	ws := fixture(t)
	ns := object("Namespace", "istio-system", nil)
	M(ns["metadata"])["namespace"] = ""
	put(ws, "", "namespaces", "Namespace", ns)
	req := Request{Cluster: "c", Namespace: "istio-system", Tasks: []string{"mesh-strict-mtls"}}
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("guessed mesh root")
	}
	cm := object("ConfigMap", "istio", nil)
	M(cm["metadata"])["namespace"] = "istio-system"
	cm["data"] = map[string]any{"mesh": "rootNamespace: istio-system\n"}
	put(ws, "", "configmaps", "ConfigMap", cm)
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.YAML, "selector") || !strings.Contains(p.YAML, "mode: STRICT") || !strings.Contains(p.YAML, "namespace: istio-system") {
		t.Fatal(p.YAML)
	}
	old := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "PERMISSIVE"}})
	M(old["metadata"])["namespace"] = "istio-system"
	put(ws, SecurityGroup, "peerauthentications", "PeerAuthentication", old)
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("competing root default created")
	}
	state := meshMTLSState(ws, "c")
	req.Edit = state.Defaults[0].Request.Edit
	p, err = Generate(ws, req)
	if err != nil || len(p.Objects) != 0 || len(p.Changes) != 1 || p.Changes[0].Fields[0].Path != "/spec/mtls/mode" {
		t.Fatalf("%v %+v", err, p)
	}
	req.Edit.Fingerprint = "stale"
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("stale edit accepted")
	}
	cm["data"] = map[string]any{"mesh": "rootNamespace: custom-root\n"}
	put(ws, "", "configmaps", "ConfigMap", cm)
	if _, err := Generate(ws, Request{Cluster: "c", Namespace: "istio-system", Tasks: req.Tasks}); err == nil {
		t.Fatal("wrong root called mesh-wide")
	}
}
