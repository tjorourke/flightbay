package ambient

import "testing"

func TestL4AdmissionUnionAndDestinationScope(t *testing.T) {
	for _, tc := range []struct {
		name, action, principal, state string
		extra                          bool
	}{
		{"named caller only", "ALLOW", "cluster.local/ns/app/sa/client", "blocked", false},
		{"additive admission", "ALLOW", "cluster.local/ns/app/sa/client", "admitted", true},
		{"matching deny", "DENY", "cluster.local/ns/app/sa/wp", "blocked", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			withWaypointIdentity(ws)
			put(ws, "discovery.k8s.io", "endpointslices", "EndpointSlice", object("EndpointSlice", "front-1", nil))
			policy := object("AuthorizationPolicy", "caller", map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}}, "action": tc.action, "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": []any{tc.principal}}}}}}})
			put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", policy)
			if tc.extra {
				put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", object("AuthorizationPolicy", "another", map[string]any{"rules": []any{map[string]any{}}}))
			}
			svc := ws.Get("c", "", "services", "app", "front")
			if got := analyseL4(ws, svc, "cluster.local/ns/app/sa/wp"); got.State != tc.state {
				t.Fatalf("%+v", got)
			}
			if got := analyseL4(ws, svc, ""); got.State == "admitted" {
				t.Fatal("missing identity passed")
			}
		})
	}
}

func TestWaypointLockChecksEveryAllowBranch(t *testing.T) {
	wp := "cluster.local/ns/app/sa/wp"
	lock := AuthzPolicy{Action: "ALLOW", Enforced: "ztunnel", Rules: []AccessRule{{From: []AccessSource{{Principals: []string{wp}}}}}}
	if !l4ExclusiveWaypoint([]AuthzPolicy{lock}, wp) {
		t.Fatal("exact lock not recognised")
	}
	for _, other := range []AuthzPolicy{
		{Action: "ALLOW", Enforced: "ztunnel", Rules: []AccessRule{{}}},
		{Action: "ALLOW", Enforced: "ztunnel", Rules: []AccessRule{{From: []AccessSource{{Namespaces: []string{"app"}}}}}},
		{Action: "ALLOW", Enforced: "ztunnel", Rules: []AccessRule{{From: []AccessSource{{Principals: []string{"cluster.local/ns/app/sa/client"}}}}}},
	} {
		if l4ExclusiveWaypoint([]AuthzPolicy{lock, other}, wp) {
			t.Fatal("additive caller admitted but lock claimed")
		}
	}
	lock.Rules = append(lock.Rules, AccessRule{})
	if l4ExclusiveWaypoint([]AuthzPolicy{lock}, wp) {
		t.Fatal("extra rule ignored")
	}
}
