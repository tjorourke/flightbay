package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/ambient"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Recover only the separately labelled Bookinfo demo, never an application
// namespace identified by name alone. Forgetting local history must not make
// the installer unable to remove its own live demo.
func recoverBookinfoRemoval(ctx context.Context, c ambientConnection, cluster, contextName, namespace string) (*ambientRecord, error) {
	if namespace == "" || namespace == "default" || namespace == "istio-system" || namespace == "istio-eastwest" || strings.HasPrefix(namespace, "kube-") {
		return nil, nil
	}
	nss := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	ns, err := nss.Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lb := ns.GetLabels()
	if ns.GetUID() == "" || lb["flightbay.dev/demo"] != "bookinfo" || lb["app.kubernetes.io/managed-by"] != "flightbay" || lb["flightbay.dev/test-id"] == "" {
		return nil, nil
	}
	if ns.GetDeletionTimestamp() != nil {
		return nil, fmt.Errorf("Bookinfo namespace %s is still deleting; wait for it to disappear", namespace)
	}
	anchor, err := nss.Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if anchor.GetUID() == "" {
		return nil, fmt.Errorf("cluster identity is missing; cannot recover Bookinfo removal")
	}
	d, err := ambient.BookinfoSettings(ambient.BookinfoRequest{Stage: "workloads", WaypointName: lb["flightbay.dev/waypoint-name"], WaypointScope: lb["flightbay.dev/waypoint-scope"], Traffic: true})
	if err != nil {
		return nil, err
	}
	p := &ambient.Plan{ID: lb["flightbay.dev/test-id"], ServiceUID: string(anchor.GetUID()) + "/" + string(ns.GetUID()), Request: ambient.Request{Cluster: cluster, Namespace: namespace, Tasks: []string{"bookinfo-demo"}, Demo: &d}}
	allowed, err := bookinfoAllowedObjects(p)
	if err != nil {
		return nil, err
	}
	rec := &ambientRecord{Plan: p, Context: contextName, Server: c.host, ApplyState: "applied", Created: []ambientCreated{}}
	planID := ""
	// Keep the waypoint first so reverse-order cleanup removes its consumers
	// before it. Ignore controller children and configuration added afterwards;
	// the explicitly reviewed demo namespace deletion removes those as well.
	for _, gvk := range []schema.GroupVersionKind{
		{Group: ambient.GatewayGroup, Version: "v1", Kind: "Gateway"},
		{Version: "v1", Kind: "ServiceAccount"},
		{Version: "v1", Kind: "Service"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: ambient.GatewayGroup, Version: "v1", Kind: "HTTPRoute"},
	} {
		mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items, err := c.client.Resource(mapping.Resource).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=flightbay,flightbay.dev/demo=bookinfo"})
		if err != nil {
			return nil, fmt.Errorf("discover live Bookinfo %s: %w", gvk.Kind, err)
		}
		sort.Slice(items.Items, func(i, j int) bool { return items.Items[i].GetName() < items.Items[j].GetName() })
		for _, o := range items.Items {
			if !allowed[gvk.GroupVersion().String()+"/"+gvk.Kind+"/"+o.GetName()] {
				continue
			}
			id := o.GetLabels()["flightbay.dev/test-id"]
			if id == "" || o.GetUID() == "" || (planID != "" && id != planID) {
				return nil, fmt.Errorf("Bookinfo ownership is incomplete or spans different installations; inspect %s %s/%s before removal", gvk.Kind, namespace, o.GetName())
			}
			planID = id
			if gvk.Kind == "Gateway" {
				class := ambient.S(ambient.Path(o.Object, "spec", "gatewayClassName"))
				if class == "" || (d.WaypointClass != "" && d.WaypointClass != class) {
					return nil, fmt.Errorf("Bookinfo waypoints have inconsistent implementations")
				}
				d.WaypointClass = class
			}
			rec.Created = append(rec.Created, ambientCreated{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Namespace: namespace, Name: o.GetName(), UID: o.GetUID()})
		}
	}
	if planID != "" {
		p.ID = planID
	}
	// Bind confirmation to the exact live namespace, cluster, object UIDs and
	// waypoint settings. A replacement between preview and remove gets a new ID.
	b, err := json.Marshal(struct {
		Record *ambientRecord
		Labels map[string]string
	}{rec, lb})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	rec.Token = hex.EncodeToString(sum[:24])
	return rec, nil
}
