package store

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

func objWS(t *testing.T) *Workspace {
	ws := New()
	pods := bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true, ShortNames: []string{"po"}}
	svcs := bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true, ShortNames: []string{"svc"}}
	routes := bundle.APIResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true}
	ws.RegisterResources("mesh1", []bundle.APIResource{pods, svcs, routes})
	ws.AddObjects("mesh1", pods, []map[string]any{
		{"kind": "Pod", "metadata": map[string]any{"name": "checkout-7d9f", "namespace": "payments", "labels": map[string]any{"app": "checkout"}},
			"spec": map[string]any{"containers": []any{map[string]any{"image": "shop/checkout:2.8.1"}}}, "status": map[string]any{"containerStatuses": []any{map[string]any{"state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}}}}},
		{"kind": "Pod", "metadata": map[string]any{"name": "cart-1", "namespace": "payments", "labels": map[string]any{"app": "cart"}}},
		{"kind": "Pod", "metadata": map[string]any{"name": "istiod-1", "namespace": "istio-system", "labels": map[string]any{"app": "istiod"}}},
	}, "p")
	ws.AddObjects("mesh1", svcs, []map[string]any{{"kind": "Service", "metadata": map[string]any{"name": "checkout", "namespace": "payments", "labels": map[string]any{"app": "checkout"}}}}, "s")
	ws.AddObjects("mesh1", routes, []map[string]any{{"kind": "HTTPRoute", "metadata": map[string]any{"name": "shop", "namespace": "payments"},
		"spec": map[string]any{"hostnames": []any{"shop.example.com"}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "checkout"}}}}}}}, "r")
	return ws
}

func names(r *ObjectResult) string {
	var n []string
	for _, h := range r.Results {
		n = append(n, h.Kind+"/"+h.Name)
	}
	return strings.Join(n, ",")
}

func TestSearchObjects(t *testing.T) {
	ws := objWS(t)
	for q, want := range map[string]string{
		`checkout`:                      "Service/checkout,Pod/checkout-7d9f,HTTPRoute/shop", // exact name first, then name contains, then content
		`kind:po`:                       "Pod/istiod-1,Pod/cart-1,Pod/checkout-7d9f",
		`kind:pod ns:payments`:          "Pod/cart-1,Pod/checkout-7d9f",
		`label:app=checkout`:            "Pod/checkout-7d9f,Service/checkout",
		`CrashLoopBackOff`:              "Pod/checkout-7d9f",
		`"shop.example.com"`:            "HTTPRoute/shop",
		`kind:httproute`:                "HTTPRoute/shop",
		`name:ist*`:                     "Pod/istiod-1",
		`checkout -kind:svc`:            "Pod/checkout-7d9f,HTTPRoute/shop",
		`shop/checkout:2.8.1`:           "Pod/checkout-7d9f",
		`kind:pod label:app -ns:istio*`: "Pod/cart-1,Pod/checkout-7d9f",
	} {
		r, err := ws.SearchObjects(q, "", 50)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		got := names(r)
		if strings.HasPrefix(q, "kind:") || strings.HasPrefix(q, "label:") || strings.HasPrefix(q, "name:") {
			// no free text: order is kind, namespace, name
			if len(strings.Split(got, ",")) != len(strings.Split(want, ",")) {
				t.Errorf("%q: got %s, want %s", q, got, want)
			}
			continue
		}
		if got != want {
			t.Errorf("%q: got %s, want %s", q, got, want)
		}
	}
	r, _ := ws.SearchObjects("CrashLoopBackOff", "", 10)
	if r.Results[0].Match != "content" || !strings.Contains(r.Results[0].Snippet, "CrashLoopBackOff") || r.ByKind["Pod"] != 1 {
		t.Fatalf("snippet/byKind: %+v", r)
	}
	if r, _ := ws.SearchObjects("nothing-like-this", "", 10); r.Total != 0 || r.Message == "" {
		t.Fatalf("empty: %+v", r)
	}
}
