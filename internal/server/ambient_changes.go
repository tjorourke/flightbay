package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/tjorourke/flightbay/internal/ambient"
)

func ambientChangeResource(c ambientConnection, ch ambient.Change) (dynamic.ResourceInterface, error) {
	gv, e := schema.ParseGroupVersion(ch.APIVersion)
	if e != nil {
		return nil, e
	}
	mapping, e := c.mapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: ch.Ref.Kind}, gv.Version)
	if e != nil {
		return nil, e
	}
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		if ch.Ref.Kind != "Namespace" || gv.Group != "" {
			return nil, fmt.Errorf("only scoped namespace label changes may target cluster-scoped objects")
		}
		return c.client.Resource(mapping.Resource), nil
	}
	return c.client.Resource(mapping.Resource).Namespace(ch.Ref.Namespace), nil
}
func ambientPatch(ctx context.Context, c ambientConnection, ch ambient.Change, undo, dry bool) (*unstructured.Unstructured, error) {
	res, e := ambientChangeResource(c, ch)
	if e != nil {
		return nil, e
	}
	live, e := res.Get(ctx, ch.Ref.Name, metav1.GetOptions{})
	if e != nil {
		return nil, e
	}
	if ch.UID == "" || string(live.GetUID()) != ch.UID {
		return nil, fmt.Errorf("%s %s UID differs; no fields were changed", ch.Ref.Kind, ch.Ref.Name)
	}
	if live.GetResourceVersion() == "" {
		return nil, fmt.Errorf("server supplied no resourceVersion for atomic field updates")
	}
	ops := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": ch.UID}, {"op": "test", "path": "/metadata/resourceVersion", "value": live.GetResourceVersion()}}
	labelsAdded := false
	for _, f := range ch.Fields {
		before, beforePresent, after, afterPresent := f.Before, f.BeforePresent, f.After, f.AfterPresent
		if undo {
			before, beforePresent, after, afterPresent = f.After, f.AfterPresent, f.Before, f.BeforePresent
		}
		current, present := ambient.Pointer(live.Object, f.Path)
		if present != beforePresent || (present && !ambient.Equal(current, before)) {
			return nil, fmt.Errorf("%s %s field %s changed outside this test; refusing to overwrite it", ch.Ref.Kind, ch.Ref.Name, f.Path)
		}
		if afterPresent {
			if strings.HasPrefix(f.Path, "/metadata/labels/") {
				if _, ok := ambient.Pointer(live.Object, "/metadata/labels"); !ok && !labelsAdded {
					ops = append(ops, map[string]any{"op": "add", "path": "/metadata/labels", "value": map[string]any{}})
					labelsAdded = true
				}
			}
			ops = append(ops, map[string]any{"op": "add", "path": f.Path, "value": after})
		} else if present {
			ops = append(ops, map[string]any{"op": "remove", "path": f.Path})
		}
	}
	b, e := json.Marshal(ops)
	if e != nil {
		return nil, e
	}
	opts := metav1.PatchOptions{FieldManager: "flightbay-tests"}
	if dry {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	return res.Patch(ctx, ch.Ref.Name, types.JSONPatchType, b, opts)
}
func ambientChangesPreflight(ctx context.Context, c ambientConnection, p *ambient.Plan) error {
	for _, ch := range p.Changes {
		res, e := ambientChangeResource(c, ch)
		if e != nil {
			return e
		}
		live, e := res.Get(ctx, ch.Ref.Name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		if ch.UID == "" || string(live.GetUID()) != ch.UID {
			return fmt.Errorf("%s %s does not match the captured UID", ch.Ref.Kind, ch.Ref.Name)
		}
		for _, f := range ch.Fields {
			v, ok := ambient.Pointer(live.Object, f.Path)
			if ok != f.BeforePresent || (ok && !ambient.Equal(v, f.Before)) {
				return fmt.Errorf("%s %s field %s no longer matches the preview; refresh the workspace", ch.Ref.Kind, ch.Ref.Name, f.Path)
			}
		}
	}
	return nil
}
func waitAmbientWaypoint(ctx context.Context, c ambientConnection, p *ambient.Plan) error {
	if p.Request.Demo != nil && p.Request.Demo.Stage == "workloads" {
		for _, o := range p.Objects {
			if ambient.S(o["kind"]) == "Gateway" {
				sub := &ambient.Plan{Request: ambient.Request{Namespace: p.Request.Namespace, Waypoint: &ambient.WaypointRequest{Name: ambient.S(ambient.Path(o, "metadata", "name")), Namespace: p.Request.Namespace}}}
				if err := waitAmbientWaypoint(ctx, c, sub); err != nil {
					return err
				}
			}
		}
		return nil
	}
	w := p.Request.Waypoint
	if w == nil {
		return nil
	}
	ns := w.Namespace
	if ns == "" {
		ns = p.Request.Namespace
	}
	res := c.client.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}).Namespace(ns)
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for {
		obj, e := res.Get(ctx, w.Name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		for _, v := range ambient.L(ambient.Path(obj.Object, "status", "conditions")) {
			m := ambient.M(v)
			if ambient.S(m["type"]) == "Programmed" && ambient.S(m["status"]) == "True" && (ambient.N(m["observedGeneration"]) == 0 || int64(ambient.N(m["observedGeneration"])) == obj.GetGeneration()) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("waypoint did not report Programmed before the test deadline; bindings were not changed")
		case <-time.After(time.Second):
		}
	}
}
func undoAmbientChanges(ctx context.Context, c ambientConnection, rec *ambientRecord) error {
	left := []ambient.Change{}
	var failures []string
	for i := len(rec.Modified) - 1; i >= 0; i-- {
		ch := rec.Modified[i]
		_, e := ambientPatch(ctx, c, ch, true, false)
		if apierrors.IsNotFound(e) {
			continue
		}
		if e != nil {
			left = append(left, ch)
			failures = append(failures, e.Error())
		}
	}
	rec.Modified = left
	if len(failures) > 0 {
		return fmt.Errorf("undo needs attention: %s; created resources are retained until bindings can be restored", strings.Join(failures, "; "))
	}
	return nil
}
