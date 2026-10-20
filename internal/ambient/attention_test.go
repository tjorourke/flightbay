package ambient

import (
	"reflect"
	"strings"
	"testing"
)

func stepIDs(steps []Step) []string {
	out := []string{}
	for _, s := range steps {
		out = append(out, s.ID)
	}
	return out
}

func TestSuppressConsequences(t *testing.T) {
	st := func(id, state string) Step { return Step{ID: id, State: state} }
	tests := []struct {
		name string
		in   []Step
		want []string
	}{
		{"no pods drops endpoints", []Step{st("workloads", "fail"), st("endpoints", "fail")}, []string{"workloads"}},
		{"endpoints kept when workloads only unknown", []Step{st("workloads", "unknown"), st("endpoints", "fail")}, []string{"workloads", "endpoints"}},
		{"endpoints kept alone", []Step{st("endpoints", "fail")}, []string{"endpoints"}},
		{"unresolved route parent drops its status", []Step{
			st("route-app-r-0", "fail"), st("route-app-r-0-status-0-Accepted", "fail"), st("route-app-r-0-status-0-ResolvedRefs", "fail"),
			st("route-app-r-0-listener", "fail"), st("route-app-r-0-eligibility", "fail"), st("route-app-r-1-status-0-Accepted", "fail"),
		}, []string{"route-app-r-0", "route-app-r-1-status-0-Accepted"}},
		{"resolved parent keeps status failures", []Step{st("route-app-r-0", "warning"), st("route-app-r-0-status-0-Accepted", "fail")}, []string{"route-app-r-0", "route-app-r-0-status-0-Accepted"}},
		{"listener rejection drops Accepted only", []Step{
			st("route-app-r-0", "pass"), st("route-app-r-0-eligibility", "fail"), st("route-app-r-0-status-0-Accepted", "fail"), st("route-app-r-0-status-0-ResolvedRefs", "fail"),
		}, []string{"route-app-r-0", "route-app-r-0-eligibility", "route-app-r-0-status-0-ResolvedRefs"}},
		{"waypoint not Programmed drops binding", []Step{st("waypoint-bound-istio.io/WaypointBound", "fail"), st("waypoint-status-Programmed", "fail")}, []string{"waypoint-status-Programmed"}},
		{"waypoint not Accepted drops Programmed and binding", []Step{st("waypoint-status-Accepted", "fail"), st("waypoint-status-Programmed", "fail"), st("waypoint-bound-istio.io/WaypointBound", "fail")}, []string{"waypoint-status-Accepted"}},
		{"ingress class waypoint drops binding", []Step{st("waypoint", "fail"), st("waypoint-bound-istio.io/WaypointBound", "fail")}, []string{"waypoint"}},
		{"healthy waypoint keeps binding failure", []Step{st("waypoint-status-Programmed", "pass"), st("waypoint-bound-istio.io/WaypointBound", "fail")}, []string{"waypoint-status-Programmed", "waypoint-bound-istio.io/WaypointBound"}},
		{"Gateway not Accepted drops Programmed", []Step{st("gateway-status-Accepted", "fail"), st("gateway-status-Programmed", "fail")}, []string{"gateway-status-Accepted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stepIDs(suppressConsequences(tt.in)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func issueIDs(issues []AmbientIssue, name string) []string {
	out := []string{}
	for _, is := range issues {
		if is.Subject.Name == name {
			out = append(out, is.Step.ID)
		}
	}
	return out
}

func TestKnownIssuesReportsNoPodsOnce(t *testing.T) {
	ws := fixture(t)
	got := issueIDs(Issues(ws), "stable")
	if !reflect.DeepEqual(got, []string{"workloads"}) {
		t.Fatalf("Service with no Pods must be one issue, got %v", got)
	}
}

func TestKnownIssuesWaypointNotProgrammedHidesBinding(t *testing.T) {
	ws := fixture(t)
	wp := Target(ws, "c", "Gateway", "app", "wp")
	L(Path(wp.Obj, "status", "conditions"))[1] = map[string]any{"type": "Programmed", "status": "False", "observedGeneration": 1, "reason": "NoAddress"}
	svc := Target(ws, "c", "Service", "app", "front")
	svc.Obj["status"] = map[string]any{"conditions": []any{map[string]any{"type": "istio.io/WaypointBound", "status": "False", "observedGeneration": 1, "reason": "NotReady", "message": "Service does not use waypoint"}}}
	ids := issueIDs(Issues(ws), "front")
	for _, id := range ids {
		if strings.HasPrefix(id, "waypoint-bound-") {
			t.Fatalf("binding failure is a consequence of the waypoint: %v", ids)
		}
	}
	found := false
	for _, is := range Issues(ws) {
		if is.Step.ID == "waypoint-status-Programmed" {
			found = true
		}
	}
	if !found {
		t.Fatal("waypoint Programmed failure must be reported")
	}
}

func TestKnownIssuesCollectsActionableWarningsOnly(t *testing.T) {
	ws := fixture(t)
	deny := object("AuthorizationPolicy", "deny-delete", map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"kind": "Service", "group": "", "name": "front"}}, "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"DELETE"}}}}}}})
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", deny)
	var bypass *AmbientIssue
	for _, is := range Issues(ws) {
		is := is
		if is.Step.State == "warning" {
			switch {
			case is.Step.ID == "require-waypoint" && is.Subject.Name == "front":
				bypass = &is
			case is.Step.ID == "require-waypoint":
				t.Fatalf("bypass warning without an L7 policy: %+v", is.Subject)
			case is.Step.ID == "enrolment" || is.Step.ID == "waypoint" || strings.HasPrefix(is.Step.ID, "pod-") || strings.HasPrefix(is.Step.ID, "policy-deny"):
				t.Fatalf("informational warning collected: %s", is.Step.ID)
			}
		}
	}
	if bypass == nil || !strings.HasPrefix(bypass.Step.Detail, "L7 DENY can be bypassed") {
		t.Fatalf("L7 DENY bypass warning missing: %+v", bypass)
	}
}

func TestBuildAttentionGroupsByService(t *testing.T) {
	mk := func(cluster, state, ns, name, id string) AmbientIssue {
		return AmbientIssue{Subject: Ref{Cluster: cluster, Kind: "Service", Namespace: ns, Name: name}, Step: Step{ID: id, Title: id, State: state, Detail: id + " detail\nmore", SuggestedFix: &FixGuide{ID: id}}}
	}
	route := AmbientIssue{Subject: Ref{Cluster: "m1", Kind: "HTTPRoute", Namespace: "bookinfo", Name: "reviews"}, Step: Step{ID: "route-bookinfo-reviews-0", State: "fail", Detail: "parent missing"}}
	rows := BuildAttention([]AmbientIssue{
		mk("m2", "warning", "bookinfo", "productpage", "require-waypoint"),
		mk("m2", "fail", "bookinfo", "reviews", "workloads"),
		mk("m1", "fail", "bookinfo", "reviews", "route-bookinfo-r-0"),
		mk("m1", "fail", "bookinfo", "reviews", "workloads"),
		mk("m1", "fail", "bookinfo", "ratings", "workloads"),
		route,
	}, "")
	if len(rows) != 4 {
		t.Fatalf("want 4 entries, got %+v", rows)
	}
	r := rows[0]
	if r.Ref.Name != "reviews" || r.Ref.Kind != "Service" || r.Ref.Cluster != "" || !reflect.DeepEqual(r.Clusters, []string{"m1", "m2"}) || r.Cluster != "m1" ||
		r.State != "fail" || r.CheckID != "workloads" || r.Detail != "workloads detail" || r.More != 1 || len(r.Checks) != 3 || !r.HasFix {
		t.Fatalf("grouped row: %+v", r)
	}
	if rows[1].Ref.Name != "ratings" || rows[2].Ref.Kind != "HTTPRoute" || rows[3].State != "warn" || rows[3].Ref.Name != "productpage" {
		t.Fatalf("order: %+v", rows)
	}
	if got := BuildAttention([]AmbientIssue{mk("m1", "fail", "a", "x", "i"), mk("m1", "fail", "b", "y", "i"), mk("m1", "fail", "c", "z", "i")}, "b, c"); len(got) != 2 {
		t.Fatalf("comma namespace filter: %+v", got)
	}
}

func TestOverviewAttentionSummaryAndRowFlags(t *testing.T) {
	ws := fixture(t)
	deny := object("AuthorizationPolicy", "deny-delete", map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"kind": "Service", "group": "", "name": "front"}}, "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"DELETE"}}}}}}})
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", deny)
	ov := Overview(ws, "")
	services := 0
	seen := map[string]bool{}
	for _, row := range ov.NeedsAttention {
		key := row.Ref.Kind + "/" + row.Ref.Namespace + "/" + row.Ref.Name
		if seen[key] {
			t.Fatalf("object repeated in Needs attention: %s", key)
		}
		seen[key] = true
		if row.Ref.Kind == "Service" {
			services++
		}
	}
	na := ov.Summary.NeedsAttention
	if na.Services != services || na.Services != na.Fail+na.Warn || na.Warn < 1 || na.Fail < 2 {
		t.Fatalf("summary %+v does not match %d Service entries: %+v", na, services, ov.NeedsAttention)
	}
	rows := map[string]ServiceRow{}
	for _, r := range ov.Services {
		rows[r.Ref.Name] = r
	}
	if !rows["front"].Configured || rows["stable"].Configured || !rows["front"].InAmbient || !rows["stable"].InAmbient {
		t.Fatalf("row flags: front=%+v stable=%+v", rows["front"], rows["stable"])
	}
	var app *NamespaceRow
	for i := range ov.Namespaces {
		if ov.Namespaces[i].Name == "app" {
			app = &ov.Namespaces[i]
		}
	}
	if app == nil || app.System || !app.InAmbient || app.Services != 3 || app.Configured != 1 {
		t.Fatalf("namespace row: %+v", app)
	}
	if got := Overview(ws, "other"); len(got.Services) != 0 || len(got.NeedsAttention) != 0 || got.Summary.NeedsAttention.Services != 0 {
		t.Fatalf("namespace filter: %+v", got)
	}
}

func TestIsSystemNamespace(t *testing.T) {
	for ns, want := range map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true, "local-path-storage": true, "istio-system": true, "istio-eastwest": true, "istio-gateways": true, "bookinfo": false, "default": false, "kagent": false} {
		if IsSystemNamespace(ns) != want {
			t.Errorf("%s: want %v", ns, want)
		}
	}
}
