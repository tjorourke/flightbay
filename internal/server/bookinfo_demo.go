package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tjorourke/flightbay/internal/ambient"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (s *Server) bookinfoOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "bookinfo"
	}
	writeJSON(w, 200, ambient.BookinfoOptions(s.WS, ns))
}
func (s *Server) bookinfoPreflight(ctx context.Context, c ambientConnection, p *ambient.Plan) error {
	q := p.Request
	settings, err := ambient.BookinfoSettings(*q.Demo)
	if err != nil {
		return err
	}
	anchor := s.WS.Get(q.Cluster, "", "namespaces", "", "kube-system")
	if anchor == nil || ambient.S(ambient.Path(anchor.Obj, "metadata", "uid")) == "" {
		return fmt.Errorf("capture kube-system Namespace identity before installing")
	}
	namespaces := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	actual, err := namespaces.Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(actual.GetUID()) != ambient.S(ambient.Path(anchor.Obj, "metadata", "uid")) {
		return fmt.Errorf("selected context is not the captured cluster; kube-system identity differs")
	}
	actual, err = namespaces.Get(ctx, q.Namespace, metav1.GetOptions{})
	if q.Demo.Stage == "namespace" {
		if err == nil {
			return fmt.Errorf("namespace %s already exists; it will not be overwritten", q.Namespace)
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		captured := s.WS.Get(q.Cluster, "", "namespaces", "", q.Namespace)
		if captured == nil || string(actual.GetUID()) != ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) {
			return fmt.Errorf("demo namespace changed or capture has not refreshed")
		}
		labels := actual.GetLabels()
		binding := labels["istio.io/use-waypoint"]
		if labels["flightbay.dev/demo"] != "bookinfo" || labels["app.kubernetes.io/managed-by"] != "flightbay" || labels["istio.io/dataplane-mode"] != "ambient" || (binding != "" && binding != ambient.BookinfoNamespaceBinding(settings)) || binding != captured.Labels()["istio.io/use-waypoint"] || (labels["flightbay.dev/waypoint-scope"] != "" && labels["flightbay.dev/waypoint-scope"] != settings.WaypointScope) || (labels["flightbay.dev/waypoint-name"] != "" && labels["flightbay.dev/waypoint-name"] != settings.WaypointName) {
			return fmt.Errorf("namespace is not an unchanged installer-owned ambient demo namespace")
		}
	}
	gc, err := c.client.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gatewayclasses"}).Get(ctx, q.Demo.WaypointClass, metav1.GetOptions{})
	if err != nil {
		return err
	}
	controller := ambient.S(ambient.Path(gc.Object, "spec", "controllerName"))
	if !strings.Contains(controller, "mesh-controller") && !strings.Contains(controller, "agentgateway") {
		return fmt.Errorf("selected GatewayClass is not a supported waypoint controller")
	}
	return ambientChangesPreflight(ctx, c, p)
}
