package diff

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

var (
	deploy = bundle.APIResource{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true}
	svc    = bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}
)

func ws(t *testing.T, id string, namespaces []string, image string, services []string, rv string) *store.Workspace {
	w := store.New()
	p, _ := policy.Profile("standard")
	p.Namespaces = namespaces
	b, _ := json.Marshal(p)
	w.SetHeader(&bundle.Header{BundleID: id, Contexts: []string{"mesh1"}, Policy: b, CaptureStart: time.Now()})
	w.AddObjects("mesh1", deploy, []map[string]any{{"kind": "Deployment", "metadata": map[string]any{"name": "checkout", "namespace": "payments", "resourceVersion": rv, "uid": "u" + rv},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": image}}}}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Available", "status": "True", "lastTransitionTime": rv}}}}}, "d")
	var objs []map[string]any
	for _, s := range services {
		ns, name, _ := strings.Cut(s, "/")
		objs = append(objs, map[string]any{"kind": "Service", "metadata": map[string]any{"name": name, "namespace": ns}})
	}
	w.AddObjects("mesh1", svc, objs, "s")
	for _, r := range []bundle.APIResource{deploy, svc} {
		w.SetCoverage(bundle.Coverage{ID: "mesh1." + r.Resource, Cluster: "mesh1", Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, State: bundle.StateComplete})
	}
	return w
}

func TestCompareBundles(t *testing.T) {
	before := ws(t, "a", []string{"payments", "legacy"}, "shop/checkout:2.8.1", []string{"payments/checkout", "payments/checkout-v2", "legacy/old"}, "1")
	after := ws(t, "b", []string{"payments"}, "shop/checkout:2.9.0", []string{"payments/checkout", "payments/cart"}, "2")
	r := Compare(before, after)
	got := map[string]string{}
	for _, c := range r.Changes {
		got[c.Namespace+"/"+c.Name] = c.Change
	}
	if got["payments/cart"] != "added" || got["payments/checkout-v2"] != "removed" || got["payments/checkout"] != "changed" {
		t.Fatalf("changes %v", got)
	}
	if _, ok := got["legacy/old"]; ok {
		t.Fatal("an object in a namespace the later bundle did not capture was reported as removed")
	}
	if len(r.NotComparable) != 1 || !strings.Contains(r.NotComparable[0], "legacy") {
		t.Fatalf("not comparable %v", r.NotComparable)
	}
	if len(r.Versions) != 1 || r.Versions[0].Before != "shop/checkout:2.8.1" || r.Versions[0].After != "shop/checkout:2.9.0" {
		t.Fatalf("versions %+v", r.Versions)
	}
	for _, c := range r.Changes {
		if c.Name == "checkout" && c.Kind == "Deployment" {
			if strings.Join(c.Fields, ",") != "spec.template.spec" {
				t.Fatalf("resourceVersion, uid or transition time counted as a change: %v", c.Fields)
			}
		}
	}
	if !strings.Contains(r.Text(false), "2.8.1 -> shop/checkout:2.9.0") {
		t.Fatalf("text report:\n%s", r.Text(false))
	}
}

func TestIdenticalCapturesDiffToNothing(t *testing.T) {
	a := ws(t, "a", []string{"payments"}, "x:1", []string{"payments/checkout"}, "1")
	b := ws(t, "b", []string{"payments"}, "x:1", []string{"payments/checkout"}, "7")
	if r := Compare(a, b); len(r.Changes) != 0 {
		t.Fatalf("noise reported as change: %+v", r.Changes)
	}
}
