package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func bookinfoRecordID(r *ambientRecord) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(r.Token)))[:20]
}
func bookinfoAllowedObjects(p *ambient.Plan) (map[string]bool, error) {
	if p == nil || p.Request.Demo == nil || p.Request.Demo.Stage != "workloads" || p.ID == "" || p.Request.Namespace == "" {
		return nil, fmt.Errorf("not a Bookinfo workload installation")
	}
	d, err := ambient.BookinfoSettings(*p.Request.Demo)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	add := func(api, kind string, names ...string) {
		for _, n := range names {
			allowed[api+"/"+kind+"/"+n] = true
		}
	}
	add("v1", "Service", "productpage", "details", "ratings", "reviews", "reviews-v1", "reviews-v2")
	add("v1", "ServiceAccount", "bookinfo-productpage", "bookinfo-details", "bookinfo-ratings", "bookinfo-reviews")
	add("apps/v1", "Deployment", "productpage-v1", "details-v1", "ratings-v1", "reviews-v1", "reviews-v2")
	if d.Traffic {
		add("v1", "ServiceAccount", "bookinfo-demo-client")
		add("apps/v1", "Deployment", "demo-client")
	}
	if d.WaypointScope == "service" {
		for _, svc := range []string{"productpage", "details", "ratings", "reviews"} {
			add(ambient.GatewayGroup+"/v1", "Gateway", ambient.BookinfoWaypointName(d, svc))
		}
	} else {
		add(ambient.GatewayGroup+"/v1", "Gateway", d.WaypointName)
	}
	add(ambient.GatewayGroup+"/v1", "HTTPRoute", "reviews-mesh-split")
	return allowed, nil
}

// Called before any undo/delete, including the generic Active tests endpoint.
// Never trust a name alone: the recorded plan, namespace, UID and live labels
// must all identify this installation, and shared waypoints need no outsiders.
func bookinfoDeletePreflight(ctx context.Context, c ambientConnection, r *ambientRecord) error {
	allowed, err := bookinfoAllowedObjects(r.Plan)
	if err != nil {
		return err
	}
	ns := r.Plan.Request.Namespace
	for _, o := range r.Created {
		if o.Namespace != ns || o.UID == "" || !allowed[o.APIVersion+"/"+o.Kind+"/"+o.Name] {
			return fmt.Errorf("refusing non-Bookinfo resource in deletion record: %s %s/%s", o.Kind, o.Namespace, o.Name)
		}
	}
	for _, ch := range r.Modified {
		if ch.Ref.Kind != "Namespace" || ch.Ref.Name != ns || ch.Ref.Namespace != "" {
			return fmt.Errorf("Bookinfo removal cannot modify unrelated resources")
		}
		for _, f := range ch.Fields {
			if f.Path != "/metadata/labels/istio.io~1use-waypoint" {
				return fmt.Errorf("Bookinfo removal cannot undo unrelated namespace fields")
			}
		}
	}
	namespace, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	identity := strings.SplitN(r.Plan.ServiceUID, "/", 2)
	if len(identity) != 2 || string(namespace.GetUID()) != identity[1] || namespace.GetLabels()["flightbay.dev/demo"] != "bookinfo" {
		return fmt.Errorf("demo namespace identity/ownership changed; no resources removed")
	}
	anchor, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(anchor.GetUID()) != identity[0] {
		return fmt.Errorf("context does not identify the original demo cluster")
	}
	gateways := map[string]string{}
	services := map[string]bool{}
	for _, o := range r.Created {
		if o.Namespace != ns || o.UID == "" || !allowed[o.APIVersion+"/"+o.Kind+"/"+o.Name] {
			return fmt.Errorf("refusing non-Bookinfo resource in deletion record: %s %s/%s", o.Kind, o.Namespace, o.Name)
		}
		res, e := ambientResource(c, o.APIVersion, o.Kind, o.Namespace)
		if e != nil {
			return e
		}
		live, e := res.Get(ctx, o.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			continue
		}
		if e != nil {
			return e
		}
		lb := live.GetLabels()
		if live.GetUID() != o.UID || lb["flightbay.dev/demo"] != "bookinfo" || lb["app.kubernetes.io/managed-by"] != "flightbay" || lb["flightbay.dev/test-id"] != r.Plan.ID {
			return fmt.Errorf("ownership or UID differs for %s %s/%s; nothing removed", o.Kind, ns, o.Name)
		}
		if o.Kind == "Gateway" {
			if lb["istio.io/waypoint-for"] != "service" || ambient.S(ambient.Path(live.Object, "spec", "gatewayClassName")) != r.Plan.Request.Demo.WaypointClass {
				return fmt.Errorf("waypoint scope or implementation changed for %s; inspect it before removal", o.Name)
			}
			gateways[o.Name] = string(o.UID)
		}
		if o.Kind == "Service" {
			services[ns+"/"+o.Name] = true
		}
	}
	if len(gateways) == 0 {
		return nil
	}
	nss, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot verify shared waypoint consumers: %w", err)
	}
	defaults := map[string]map[string]string{}
	for _, n := range nss.Items {
		defaults[n.GetName()] = n.GetLabels()
	}
	bound := func(labels map[string]string, ownNS string) bool {
		name, wn := labels["istio.io/use-waypoint"], labels["istio.io/use-waypoint-namespace"]
		if name == "" {
			name = defaults[ownNS]["istio.io/use-waypoint"]
			wn = defaults[ownNS]["istio.io/use-waypoint-namespace"]
		}
		if wn == "" {
			wn = ownNS
		}
		return wn == ns && gateways[name] != "" && name != "none"
	}
	svcs, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	// Anything inside the demo namespace goes when the namespace is deleted,
	// so only callers from other namespaces can block removal.
	for _, s := range svcs.Items {
		if services[s.GetNamespace()+"/"+s.GetName()] || s.GetNamespace() == ns {
			continue
		}
		generated := false
		for _, owner := range s.GetOwnerReferences() {
			generated = generated || (owner.Kind == "Gateway" && s.GetNamespace() == ns && gateways[owner.Name] == string(owner.UID))
		}
		if !generated && bound(s.GetLabels(), s.GetNamespace()) {
			return fmt.Errorf("waypoint also serves unrelated Service %s/%s; move its binding before removing this demo", s.GetNamespace(), s.GetName())
		}
	}
	if routeMapping, e := c.mapper.RESTMapping(schema.GroupKind{Group: ambient.GatewayGroup, Kind: "HTTPRoute"}); e == nil {
		routes, e := c.client.Resource(routeMapping.Resource).List(ctx, metav1.ListOptions{})
		if e != nil {
			return e
		}
		own := map[string]bool{}
		for _, o := range r.Created {
			if o.Kind == "HTTPRoute" {
				own[o.Namespace+"/"+o.Name] = true
			}
		}
		for _, route := range routes.Items {
			if own[route.GetNamespace()+"/"+route.GetName()] || route.GetNamespace() == ns {
				continue
			}
			for _, p := range ambient.L(ambient.Path(route.Object, "spec", "parentRefs")) {
				ref := ambient.M(p)
				kind := ambient.S(ref["kind"])
				if kind == "" {
					kind = "Gateway"
				}
				wn := ambient.S(ref["namespace"])
				if wn == "" {
					wn = route.GetNamespace()
				}
				if kind == "Gateway" && wn == ns && gateways[ambient.S(ref["name"])] != "" {
					return fmt.Errorf("waypoint is also a parent of unrelated HTTPRoute %s/%s; nothing removed", route.GetNamespace(), route.GetName())
				}
			}
		}
	} else if !meta.IsNoMatchError(e) {
		return e
	}
	mapping, e := c.mapper.RESTMapping(schema.GroupKind{Group: "networking.istio.io", Kind: "ServiceEntry"})
	if meta.IsNoMatchError(e) {
		return nil
	}
	if e != nil {
		return e
	}
	entries, e := c.client.Resource(mapping.Resource).List(ctx, metav1.ListOptions{})
	if e != nil {
		return e
	}
	for _, se := range entries.Items {
		lb := se.GetLabels()
		generated := lb["solo.io/parent-service-namespace"] == ns || se.GetNamespace() == ns
		if !generated && bound(lb, se.GetNamespace()) {
			return fmt.Errorf("waypoint also serves unrelated ServiceEntry %s/%s; nothing removed", se.GetNamespace(), se.GetName())
		}
	}
	return nil
}

func bookinfoClearLegacyBinding(ctx context.Context, c ambientConnection, r *ambientRecord) error {
	d, _ := ambient.BookinfoSettings(*r.Plan.Request.Demo)
	if d.WaypointScope != "namespace" {
		return nil
	}
	ownedGateway := false
	for _, o := range r.Created {
		ownedGateway = ownedGateway || o.Kind == "Gateway" && o.Namespace == r.Plan.Request.Namespace && o.Name == d.WaypointName
	}
	if !ownedGateway {
		return nil
	}
	ns, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, r.Plan.Request.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if ns.GetLabels()["istio.io/use-waypoint"] != d.WaypointName {
		return nil
	}
	ch := ambient.Change{Ref: ambient.Ref{Cluster: r.Plan.Request.Cluster, Kind: "Namespace", Resource: "namespaces", Name: ns.GetName()}, APIVersion: "v1", UID: string(ns.GetUID()), Fields: []ambient.FieldChange{{Path: "/metadata/labels/istio.io~1use-waypoint", Before: d.WaypointName, BeforePresent: true, AfterPresent: false}}}
	_, err = ambientPatch(ctx, c, ch, false, false)
	return err
}

func (s *Server) bookinfoRemoval(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Operation string   `json:"operation"`
		Cluster   string   `json:"cluster"`
		Namespace string   `json:"namespace"`
		Context   string   `json:"context"`
		IDs       []string `json:"ids"`
		Confirm   bool     `json:"confirm"`
	}
	if !ambientBody(w, r, &q) {
		return
	}
	if q.Operation != "preview" && q.Operation != "remove" {
		jerr(w, 400, "choose preview or remove")
		return
	}
	if q.Operation == "remove" && (!q.Confirm || len(q.IDs) == 0) {
		jerr(w, 400, "preview and explicitly confirm the exact Bookinfo installation first")
		return
	}
	s.ambientS.mu.Lock()
	defer s.ambientS.mu.Unlock()
	if err := s.ambientRecords(); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	c, err := s.ambientConnect(q.Context)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	chosen := []*ambientRecord{}
	descriptions := []map[string]any{}
	for _, rec := range s.ambientS.records {
		p := rec.Plan
		if p.Request.Demo == nil || p.Request.Demo.Stage != "workloads" || p.Request.Cluster != q.Cluster || p.Request.Namespace != q.Namespace || rec.Context != q.Context || len(rec.Created) == 0 {
			continue
		}
		id := bookinfoRecordID(rec)
		if q.Operation == "remove" {
			found := false
			for _, wanted := range q.IDs {
				found = found || wanted == id
			}
			if !found {
				continue
			}
		}
		if rec.Server != c.host {
			jerr(w, 409, "context now names a different API server")
			return
		}
		if err := bookinfoDeletePreflight(ctx, c, rec); err != nil {
			jerr(w, 409, err.Error())
			return
		}
		chosen = append(chosen, rec)
		descriptions = append(descriptions, map[string]any{"id": id, "planId": p.ID, "resources": rec.Created})
	}
	if q.Operation == "preview" {
		writeJSON(w, 200, map[string]any{"cluster": q.Cluster, "namespace": q.Namespace, "context": q.Context, "installations": descriptions, "note": "Removes the recorded Bookinfo resources, then deletes namespace " + q.Namespace + " that Flightbay created for it, with anything else inside it (for example policies you added while testing). Mesh infrastructure outside the namespace is kept."})
		return
	}
	if len(chosen) != len(q.IDs) {
		jerr(w, 409, "the previewed installation list changed; preview removal again")
		return
	}
	for _, rec := range chosen {
		if err := cleanupAmbient(ctx, c, rec); err != nil {
			_ = s.saveAmbientRecord(rec)
			jerr(w, 409, err.Error())
			return
		}
		rec.ApplyState = "removed"
		if err := s.saveAmbientRecord(rec); err != nil {
			jerr(w, 500, err.Error())
			return
		}
	}
	// The installer created the namespace, so removing Bookinfo removes it too,
	// guarded by the namespace UID recorded at install time and its labels.
	nsDeleted := false
	for _, rec := range chosen {
		identity := strings.SplitN(rec.Plan.ServiceUID, "/", 2)
		if len(identity) != 2 || identity[1] == "" {
			continue
		}
		res := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
		ns, err := res.Get(ctx, q.Namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			nsDeleted = true
			break
		}
		if err != nil || string(ns.GetUID()) != identity[1] || ns.GetLabels()["flightbay.dev/demo"] != "bookinfo" || ns.GetLabels()["app.kubernetes.io/managed-by"] != "flightbay" {
			continue
		}
		uid := ns.GetUID()
		if err := res.Delete(ctx, q.Namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			jerr(w, 409, "workloads removed, but deleting namespace "+q.Namespace+" failed: "+err.Error())
			return
		}
		nsDeleted = true
		break
	}
	if nsDeleted {
		for _, rec := range s.ambientS.records {
			p := rec.Plan
			// Every change recorded in the deleted namespace went with it.
			if p.Request.Cluster == q.Cluster && p.Request.Namespace == q.Namespace && rec.ApplyState != "removed" {
				rec.ApplyState = "removed"
				_ = s.saveAmbientRecord(rec)
			}
		}
		writeJSON(w, 200, map[string]any{"message": "Bookinfo removed, including namespace " + q.Namespace + ". Everything left in it was deleted with it."})
		return
	}
	writeJSON(w, 200, map[string]any{"message": "Recorded Bookinfo resources removed. The namespace was kept because it no longer matches the one Flightbay created."})
}
