package ambient

import "testing"

func TestMethodsCallerRoundTrip(t *testing.T) {
	for _, source := range []map[string]any{{"principals": []any{"cluster.local/ns/app/sa/client"}}, {"serviceAccounts": []any{"app/client"}}} {
		ws := fixture(t)
		old := object("AuthorizationPolicy", "methods", map[string]any{"targetRefs": []any{map[string]any{"kind": "Service", "group": "", "name": "front"}}, "action": "ALLOW", "rules": []any{map[string]any{"from": []any{map[string]any{"source": source}}, "to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET"}, "paths": []any{"/private/*"}, "ports": []any{"8080"}}}}, "when": []any{map[string]any{"key": "request.headers[x-team]", "values": []any{"sre"}}}}}})
		put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", old)
		svc := Target(ws, "c", "Service", "app", "front")
		var req Request
		for _, e := range editableConfigurations(ws, svc) {
			if e.Ref.Name == "methods" {
				req = *e.Request
			}
		}
		if len(req.CallerSources) != 1 {
			t.Fatal("restricted callers were not loaded", req)
		}
		p, err := Generate(ws, req)
		if err != nil || len(p.Changes) != 0 {
			t.Fatalf("round trip %v %+v", err, p)
		}
		req.Methods = []string{"POST"}
		p, err = Generate(ws, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Changes[0].Fields {
			if f.Path == "/spec/rules" {
				rules := L(f.After)
				if !Equal(Path(M(L(M(rules[0])["from"])[0]), "source"), source) {
					t.Fatal("callers changed", f)
				}
				if ports := strs(Path(M(L(M(rules[0])["to"])[0]), "operation", "ports")); len(ports) != 1 || ports[0] != "8080" {
					t.Fatal("existing port scope changed", f)
				}
			}
		}
		req.CallersEdited = true
		req.CallerSources = nil
		p, err = Generate(ws, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Changes[0].Fields {
			if f.Path == "/spec/rules" && M(L(f.After)[0])["from"] != nil {
				t.Fatal("explicit Everyone not applied")
			}
		}
	}
}
