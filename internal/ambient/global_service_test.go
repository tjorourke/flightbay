package ambient

import (
	"strings"
	"testing"
)

func TestGlobalPublicationIsScopedAndIdempotent(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}}
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("unknown installation should not claim support")
	}
	pilot := object("Pod", "istiod", map[string]any{"containers": []any{map[string]any{"name": "discovery", "image": "us-docker.pkg.dev/soloio-img/istio/pilot:1.30.4-solo-distroless", "args": []any{"discovery", "--domain", "corp.internal"}}}})
	M(pilot["metadata"])["namespace"] = "istio-system"
	M(pilot["metadata"])["labels"] = map[string]any{"app": "istiod"}
	put(ws, "", "pods", "Pod", pilot)
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry")
	put(ws, "admin.solo.io", "segments", "Segment")
	svc := Target(ws, "c", "Service", "app", "front")
	M(svc.Obj["metadata"])["labels"] = map[string]any{"app": "front", "team": "keep", "solo.io/service-takeover": "true"}
	before := Copy(svc.Obj)
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || len(p.Changes[0].Fields) != 1 || len(p.Objects) != 0 {
		t.Fatalf("unexpected change scope: %+v", p)
	}
	f := p.Changes[0].Fields[0]
	if f.Path != "/metadata/labels/solo.io~1service-scope" || f.After != "global" || f.BeforePresent {
		t.Fatalf("wrong publication field: %+v", f)
	}
	if !Equal(before, svc.Obj) {
		t.Fatal("generating mutated the capture")
	}
	if !strings.Contains(p.YAML, "solo.io/service-scope: global") || strings.Contains(p.YAML, "selector:") {
		t.Fatal(p.YAML)
	}
	M(svc.Obj["metadata"])["labels"].(map[string]any)["solo.io/service-scope"] = "global"
	p, err = Generate(ws, req)
	if err != nil || len(p.Changes) != 0 {
		t.Fatalf("publication is not idempotent: %v", err)
	}
	M(svc.Obj["metadata"])["labels"].(map[string]any)["solo.io/service-scope"] = "global-only"
	p, err = Generate(ws, req)
	if err != nil || len(p.Changes) != 0 {
		t.Fatal("legacy takeover behaviour was rewritten")
	}
}

func TestServiceDNSUsesCapturedDomainsAndPublishedAliases(t *testing.T) {
	ws := fixture(t)
	svc := Target(ws, "c", "Service", "app", "front")
	d := serviceDNS(ws, svc)
	if d.DomainObserved || d.LocalName != "front.app.svc.cluster.local" || len(d.GlobalNames) != 0 {
		t.Fatalf("default DNS must be labelled assumed: %+v", d)
	}
	coreDNS := object("ConfigMap", "coredns", nil)
	M(coreDNS["metadata"])["namespace"] = "kube-system"
	coreDNS["data"] = map[string]any{"Corefile": ".:53 {\n kubernetes dns.example in-addr.arpa ip6.arpa {\n pods insecure\n }\n}\n"}
	put(ws, "", "configmaps", "ConfigMap", coreDNS)
	if observed := serviceDNS(ws, svc); !observed.DomainObserved || observed.LocalName != "front.app.svc.dns.example" {
		t.Fatalf("CoreDNS suffix ignored: %+v", observed)
	}
	pod := object("Pod", "istiod", map[string]any{"containers": []any{map[string]any{"args": []any{"--domain=private.example"}}}})
	M(pod["metadata"])["namespace"] = "istio-system"
	M(pod["metadata"])["labels"] = map[string]any{"app": "istiod"}
	put(ws, "", "pods", "Pod", pod)
	entry := object("ServiceEntry", "autogen.front", map[string]any{"hosts": []any{"front.app.mesh.local", "custom.global.example"}})
	M(entry["metadata"])["labels"] = map[string]any{"solo.io/parent-service": "front", "solo.io/parent-service-namespace": "app", "solo.io/service-scope": "global"}
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry", entry)
	d = serviceDNS(ws, svc)
	if !d.DomainObserved || d.LocalName != "front.app.svc.private.example" || len(d.GlobalNames) != 2 || d.GlobalNames[1] != "front.app.mesh.local" {
		t.Fatalf("captured names not used: %+v", d)
	}
	other := object("Pod", "other-istiod", map[string]any{"containers": []any{map[string]any{"args": []any{"--domain", "different.example"}}}})
	M(other["metadata"])["namespace"] = "istio-system"
	M(other["metadata"])["labels"] = map[string]any{"app": "istiod"}
	put(ws, "", "pods", "Pod", other)
	if d = serviceDNS(ws, svc); d.LocalName != "" || d.DomainObserved {
		t.Fatal("conflicting suffixes were guessed")
	}
}
