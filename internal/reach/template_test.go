package reach

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// A repo scan has no pods: reach must build its workloads from the
// Deployment, StatefulSet, DaemonSet, Job and CronJob templates.
func TestWorkloadsFromTemplates(t *testing.T) {
	ws := store.New()
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Source: bundle.SourceRepo, Repo: &bundle.RepoInfo{Name: "r"}})
	rDep := res("apps", "v1", "deployments", "Deployment")
	rCron := res("batch", "v1", "cronjobs", "CronJob")
	for _, r := range []bundle.APIResource{rNS, rSvc, rNP, rAP, rDep, rCron} {
		ws.SetCoverage(bundle.Coverage{ID: "c." + r.Resource, Cluster: "c", Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, State: bundle.StateComplete})
	}
	amb := map[string]any{"istio.io/dataplane-mode": "ambient"}
	ws.AddObjects("c", rNS, []map[string]any{o("Namespace", "", "agents", amb, nil), o("Namespace", "", "tools", nil, nil)}, "ns")
	tpl := func(app, sa, image string) map[string]any {
		return map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": app}},
			"spec": map[string]any{"serviceAccountName": sa, "containers": []any{map[string]any{"name": "c", "image": image}}}}
	}
	ws.AddObjects("c", rDep, []map[string]any{
		o("Deployment", "agents", "research-agent", nil, map[string]any{"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "research-agent"}}, "template": tpl("research-agent", "research-agent", "python:3.12")}}),
		o("Deployment", "tools", "postgres", nil, map[string]any{"spec": map[string]any{"template": tpl("postgres", "", "postgres:16")}}),
	}, "dep")
	ws.AddObjects("c", rCron, []map[string]any{
		o("CronJob", "tools", "backup", nil, map[string]any{"spec": map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": tpl("backup", "backup", "busybox")}}}}),
	}, "cron")
	ws.AddObjects("c", rSvc, []map[string]any{svc("tools", "postgres", map[string]any{"app": "postgres"}, 5432, nil)}, "svc")

	c := newCluster(ws, "c")
	byName := map[string]*workload{}
	for _, w := range c.works {
		byName[w.kind+"/"+w.name] = w
	}
	a := byName["Deployment/research-agent"]
	if a == nil || byName["Deployment/postgres"] == nil || byName["CronJob/backup"] == nil {
		t.Fatalf("workloads = %v, want both Deployments and the CronJob", byName)
	}
	if a.sa != "research-agent" || a.labels["app"] != "research-agent" || !a.inMesh {
		t.Errorf("research-agent: sa %q labels %v inMesh %v", a.sa, a.labels, a.inMesh)
	}
	if byName["Deployment/postgres"].sa != "default" {
		t.Errorf("postgres sa = %q, want default", byName["Deployment/postgres"].sa)
	}
	if sv := c.svcByKey["tools/postgres"]; sv == nil || len(sv.backends) != 1 {
		t.Errorf("the postgres Service should select the templated workload: %+v", sv)
	}
	r := Evaluate(ws)
	found := false
	for _, ag := range r.Agents {
		if ag.Name == "research-agent" {
			found = true
			if ag.Internet != Allowed {
				t.Errorf("research-agent internet = %s, want allowed (no NetworkPolicy declared)", ag.Internet)
			}
		}
	}
	if !found {
		t.Errorf("research-agent should be evaluated as an agent; agents = %+v", r.Agents)
	}
}
