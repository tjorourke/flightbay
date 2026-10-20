package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// MaxRevisions is how many revisions are kept per object. Older ones are
// dropped from the front.
const MaxRevisions = 50

// Revision is one observed state of an object: the initial list, a watch
// update that changed its configuration, or its deletion.
type Revision struct {
	ResourceVersion string    // the cluster's resourceVersion, "" for a deletion (the watch event is not kept)
	Generation      int64     // metadata.generation, 0 when the kind has none
	At              time.Time // when Flightbay observed it
	Event           string    // added, updated or deleted
	Initial         bool      // part of the first list (or the captured bundle)
	SpecHash        string    // sha256 of the configuration snapshot
	// Object is the configuration snapshot: apiVersion, kind, metadata name,
	// namespace, labels and annotations, and every other top-level field
	// except status (spec, data, rules, ...). It is redacted as stored.
	Object map[string]any

	raw map[string]any // initial revisions keep the stored object and build the snapshot lazily
}

// RevisionRef identifies the object a revision list belongs to.
type RevisionRef struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type revisionHistory struct {
	ref  RevisionRef
	revs []Revision
}

func revKey(cluster, group, kind, ns, name string) string {
	return cluster + "\x00" + group + "\x00" + kind + "\x00" + ns + "\x00" + name
}

// revisionSnapshot is the part of an object a revision compares: everything
// but status, and from metadata only the name, namespace, labels and
// annotations. resourceVersion, managedFields, uid and timestamps change
// without the configuration changing.
func revisionSnapshot(obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		switch k {
		case "status", "metadata":
			continue
		}
		out[k] = v
	}
	md, _ := obj["metadata"].(map[string]any)
	m := map[string]any{}
	for _, k := range []string{"name", "namespace", "labels", "annotations"} {
		if v, ok := md[k]; ok && v != nil {
			m[k] = v
		}
	}
	out["metadata"] = m
	return out
}

func snapshotHash(snap map[string]any) string {
	b, _ := json.Marshal(snap) // map keys are sorted, so equal content gives equal bytes
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func objGeneration(obj map[string]any) int64 {
	md, _ := obj["metadata"].(map[string]any)
	switch g := md["generation"].(type) {
	case int64:
		return g
	case int:
		return int64(g)
	case float64:
		return int64(g)
	case json.Number:
		n, _ := g.Int64()
		return n
	}
	return 0
}

func objClusterRV(obj map[string]any) string {
	md, _ := obj["metadata"].(map[string]any)
	if v, ok := md["resourceVersion"]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// filled returns r with its snapshot and hash computed.
func (r Revision) filled() Revision {
	if r.Object == nil && r.raw != nil {
		r.Object = revisionSnapshot(r.raw)
	}
	if r.SpecHash == "" && r.Object != nil {
		r.SpecHash = snapshotHash(r.Object)
	}
	r.raw = nil
	return r
}

// recordInitialLocked notes an object from a list or a bundle. It costs a
// pointer: the snapshot and hash are built when they are first needed. An
// object that already has history (a later list) is compared like an update.
func (w *Workspace) recordInitialLocked(ref RevisionRef, obj map[string]any, rv string, now time.Time) {
	if w.revisions == nil {
		w.revisions = map[string]*revisionHistory{}
	}
	k := revKey(ref.Cluster, ref.Group, ref.Kind, ref.Namespace, ref.Name)
	if h := w.revisions[k]; h != nil && len(h.revs) > 0 {
		snap := revisionSnapshot(obj)
		w.recordLocked(ref, snap, snapshotHash(snap), objGeneration(obj), rv, now)
		return
	}
	w.revisions[k] = &revisionHistory{ref: ref, revs: []Revision{{ResourceVersion: rv, Generation: objGeneration(obj), At: now, Event: "added", Initial: true, raw: obj}}}
}

// recordLocked appends an added or updated revision unless the configuration
// and generation are unchanged (a status-only update).
func (w *Workspace) recordLocked(ref RevisionRef, snap map[string]any, hash string, gen int64, rv string, now time.Time) {
	if w.revisions == nil {
		w.revisions = map[string]*revisionHistory{}
	}
	k := revKey(ref.Cluster, ref.Group, ref.Kind, ref.Namespace, ref.Name)
	h := w.revisions[k]
	if h == nil {
		h = &revisionHistory{ref: ref}
		w.revisions[k] = h
	}
	h.ref = ref
	event := "added"
	if n := len(h.revs); n > 0 {
		last := h.revs[n-1].filled()
		h.revs[n-1] = last
		if last.Event != "deleted" {
			if last.SpecHash == hash && last.Generation == gen {
				return
			}
			event = "updated"
		}
	}
	h.revs = append(h.revs, Revision{ResourceVersion: rv, Generation: gen, At: now, Event: event, SpecHash: hash, Object: snap})
	if len(h.revs) > MaxRevisions {
		h.revs = append(h.revs[:0:0], h.revs[len(h.revs)-MaxRevisions:]...)
	}
}

// recordDeleteLocked appends a deletion carrying the last known configuration.
func (w *Workspace) recordDeleteLocked(ref RevisionRef, now time.Time) {
	h := w.revisions[revKey(ref.Cluster, ref.Group, ref.Kind, ref.Namespace, ref.Name)]
	if h == nil || len(h.revs) == 0 {
		return
	}
	last := h.revs[len(h.revs)-1].filled()
	h.revs[len(h.revs)-1] = last
	if last.Event == "deleted" {
		return
	}
	h.revs = append(h.revs, Revision{Generation: last.Generation, At: now, Event: "deleted", SpecHash: last.SpecHash, Object: last.Object})
	if len(h.revs) > MaxRevisions {
		h.revs = append(h.revs[:0:0], h.revs[len(h.revs)-MaxRevisions:]...)
	}
}

// ObjectRevisions returns the recorded revisions of one object, oldest
// first. group "" matches the core group, and otherwise the only group
// that has an object of that kind and name. Deleted objects keep their
// history. In a bundle the list holds the single captured revision.
func (w *Workspace) ObjectRevisions(cluster, group, kind, ns, name string) (RevisionRef, []Revision, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	h := w.revisions[revKey(cluster, group, kind, ns, name)]
	if h == nil && group == "" {
		for _, c := range w.revisions {
			if c.ref.Cluster == cluster && c.ref.Kind == kind && c.ref.Namespace == ns && c.ref.Name == name {
				if h != nil {
					return RevisionRef{}, nil, false // ambiguous: the caller must name the group
				}
				h = c
			}
		}
	}
	if h == nil {
		return RevisionRef{}, nil, false
	}
	out := make([]Revision, len(h.revs))
	for i, r := range h.revs {
		out[i] = r.filled()
	}
	return h.ref, out, true
}
