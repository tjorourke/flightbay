package ambient

import "testing"

func TestInventorySeparatesExportLabelsFromGlobalAliasAllocation(t *testing.T) {
	ws := fixture(t)
	svc := Target(ws, "c", "Service", "app", "front")
	M(svc.Obj["metadata"])["labels"] = map[string]any{"solo.io/service-scope": "global"}
	entry := object("ServiceEntry", "autogen.app.front", map[string]any{"hosts": []any{"front.app.mesh.internal"}, "resolution": "STATIC"})
	M(entry["metadata"])["namespace"] = "istio-system"
	M(entry["metadata"])["labels"] = map[string]any{"solo.io/parent-service": "front", "solo.io/parent-service-namespace": "app", "solo.io/service-scope": "global"}
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry", entry)
	find := func() *ServiceDNS {
		for _, s := range List(ws).Services {
			if s.Cluster == "c" && s.Namespace == "app" && s.Name == "front" {
				return s.Publication
			}
		}
		return nil
	}
	dns := find()
	if dns == nil || len(dns.GlobalAliases) != 1 || len(dns.GlobalAliases[0].Addresses) != 0 {
		t.Fatalf("label was treated as allocation: %+v", dns)
	}
	stored := ws.Get("c", "networking.istio.io", "serviceentries", "istio-system", "autogen.app.front")
	stored.Obj["status"] = map[string]any{"addresses": []any{map[string]any{"host": "front.app.mesh.internal", "value": "240.240.0.7"}}}
	dns = find()
	if len(dns.GlobalAliases[0].Addresses) != 1 || dns.GlobalAliases[0].Addresses[0] != "240.240.0.7" {
		t.Fatalf("allocation evidence missing: %+v", dns)
	}
}
