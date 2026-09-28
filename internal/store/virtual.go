package store

import (
	"sort"

	"github.com/tjorourke/flightbay/internal/bundle"
)

// Namespaces are cluster-scoped, so a default capture does not list them, and
// events are stored apart from objects. Both are still worth browsing, so the
// workspace offers them as types: namespaces synthesised from the captured
// objects (marked as such), events from the captured events.
var (
	virtualNamespaces = bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace", SingularName: "namespace", ShortNames: []string{"ns"}}
	virtualEvents     = bundle.APIResource{Version: "v1", Resource: "events", Kind: "Event", Namespaced: true, SingularName: "event", ShortNames: []string{"ev"}}
)

// SynthesisedAnnotation marks objects the workspace built rather than captured.
const SynthesisedAnnotation = "flightbay.dev/synthesised"

func (w *Workspace) realNamespacesLocked(cluster string) bool {
	return len(w.byType[typeKey{cluster, "", "namespaces"}]) > 0
}

// virtualLocked returns the virtual objects for a type, or ok=false when the
// type is not virtual in this cluster.
func (w *Workspace) virtualLocked(cluster, group, resource string) ([]*Object, bool) {
	if group != "" {
		return nil, false
	}
	switch {
	case resource == "namespaces" && !w.realNamespacesLocked(cluster):
		counts := map[string]int{}
		for k, objs := range w.byType {
			if k.cluster != cluster {
				continue
			}
			for _, o := range objs {
				if o.Namespace != "" {
					counts[o.Namespace]++
				}
			}
		}
		names := make([]string, 0, len(counts))
		for n := range counts {
			names = append(names, n)
		}
		sort.Strings(names)
		out := make([]*Object, 0, len(names))
		for _, n := range names {
			out = append(out, &Object{Cluster: cluster, Version: "v1", Resource: "namespaces", Kind: "Namespace", Name: n,
				Obj: map[string]any{"apiVersion": "v1", "kind": "Namespace",
					"metadata": map[string]any{"name": n, "annotations": map[string]any{
						SynthesisedAnnotation: "true",
						"flightbay.dev/note":  "Namespaces were not captured (cluster-scoped objects need --cluster-scoped). Built from the namespaces of captured objects; labels such as istio.io/dataplane-mode are unknown."}},
					"status":              map[string]any{"phase": "Active"},
					"x-flightbay-objects": counts[n]}})
		}
		return out, true
	case resource == "events" && len(w.byType[typeKey{cluster, "", "events"}]) == 0:
		var out []*Object
		for i, e := range w.events {
			if e.Cluster != cluster {
				continue
			}
			md, _ := e.Obj["metadata"].(map[string]any)
			ns, _ := md["namespace"].(string)
			name, _ := md["name"].(string)
			if e.Obj["kind"] == nil {
				e.Obj["apiVersion"], e.Obj["kind"] = "v1", "Event"
			}
			out = append(out, &Object{Cluster: cluster, Version: "v1", Resource: "events", Kind: "Event", Namespaced: true, Namespace: ns, Name: name, RV: int64(i + 1), Obj: e.Obj})
		}
		return out, true
	}
	return nil, false
}

// virtualTypesLocked lists the virtual types present in a cluster.
func (w *Workspace) virtualTypesLocked(cluster string) []bundle.APIResource {
	var out []bundle.APIResource
	if !w.realNamespacesLocked(cluster) && len(w.resources[cluster]) > 0 {
		out = append(out, virtualNamespaces)
	}
	if _, real := w.resources[cluster]["/events"]; !real {
		for _, e := range w.events {
			if e.Cluster == cluster {
				out = append(out, virtualEvents)
				break
			}
		}
	}
	return out
}
