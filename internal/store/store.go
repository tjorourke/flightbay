// Package store holds an imported workspace in memory. Data becomes visible
// one complete chunk at a time, and every change bumps a revision so readers
// can describe a consistent view while an import is still running.
package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
)

// Import phases.
const (
	PhaseWaiting   = "waiting"
	PhaseLoading   = "loading"
	PhaseComplete  = "complete"
	PhaseFailed    = "failed"
	PhaseCancelled = "cancelled"
)

// Collection states shown to users.
const (
	StateComplete     = "complete"
	StateLoading      = "loading"
	StateExcluded     = "excluded"
	StateFailed       = "failed"
	StateTruncated    = "truncated"
	StateNotCollected = "not-collected"
	// StateRemoved: the customer took the whole type out in review.
	StateRemoved = "removed"
	// StateTrimmed: the customer removed some objects of the type in review;
	// the rest are here, but absence proves nothing.
	StateTrimmed = "trimmed"
)

// ImportState is the progress of the running import.
type ImportState struct {
	Phase         string     `json:"phase"`
	Source        string     `json:"source"`
	BytesReceived int64      `json:"bytesReceived"`
	Members       int        `json:"members"`
	Objects       int        `json:"objects"`
	LogLines      int        `json:"logLines"`
	LogsSkipped   int        `json:"logsSkipped,omitempty"` // lines not indexed because of the memory budget
	Events        int        `json:"events"`
	CoverageSeen  int        `json:"coverageSeen"`
	Verified      bool       `json:"verified"`
	Error         string     `json:"error,omitempty"`
	Started       time.Time  `json:"started"`
	Finished      time.Time  `json:"finished,omitempty"`
	Revision      int64      `json:"revision"`
	Deferred      []string   `json:"deferred,omitempty"`
	Live          *LiveState `json:"live,omitempty"` // set by flightbay-desk live
}

// Object is one captured Kubernetes object.
type Object struct {
	Cluster    string         `json:"cluster"`
	Group      string         `json:"group"`
	Version    string         `json:"version"`
	Resource   string         `json:"resource"`
	Kind       string         `json:"kind"`
	Namespaced bool           `json:"namespaced"`
	Namespace  string         `json:"namespace"`
	Name       string         `json:"name"`
	RV         int64          `json:"rv"`
	Obj        map[string]any `json:"object"`
}

// Labels returns metadata.labels as strings.
func (o *Object) Labels() map[string]string {
	out := map[string]string{}
	md, _ := o.Obj["metadata"].(map[string]any)
	l, _ := md["labels"].(map[string]any)
	for k, v := range l {
		out[k], _ = v.(string)
	}
	return out
}

// Event is a captured Kubernetes event.
type Event struct {
	Cluster string         `json:"cluster"`
	Obj     map[string]any `json:"object"`
	RV      int64          `json:"-"` // revision the event was stored at; watches use it to spot updates
}

type typeKey struct{ cluster, group, resource string }

// Workspace is one opened bundle.
type Workspace struct {
	liveGate          sync.RWMutex
	livePauseMu       sync.Mutex
	liveResume        chan struct{}
	livePaused        bool // protected by mu; reported independently of collector status
	mu                sync.RWMutex
	header            *bundle.Header
	policy            *policy.Policy
	manifest          *bundle.Manifest
	objects           map[string]*Object
	byType            map[typeKey][]*Object
	resources         map[string]map[string]bundle.APIResource
	versions          map[string]json.RawMessage
	coverage          map[string]bundle.Coverage
	events            []Event
	ldb               *logDB
	streamSeq         map[string]int // lines loaded per container stream
	collection        []json.RawMessage
	redaction         json.RawMessage
	state             ImportState
	rev               int64
	nextID            int64
	subs              map[int]chan struct{}
	subID             int
	chunks            map[string]bool
	crds              map[string]map[string]map[string]any // cluster -> crd name -> slim CRD
	logEnd            time.Time                            // newest log timestamp, the "now" for since: queries
	logStart          time.Time                            // oldest log timestamp, the histogram's left edge
	diags             map[string]*Diagnostic               // cluster/kind/ns/pod -> captured diagnostics
	diagnosticSummary map[string]json.RawMessage           // cluster -> diagnostic summary
	trust             Trust                                // signature and review ledger
	revisions         map[string]*revisionHistory          // cluster/group/kind/ns/name -> observed revisions (revisions.go)
}

// Diagnostic is one component's captured admin output.
type Diagnostic struct {
	Cluster   string            `json:"cluster"`
	Kind      string            `json:"kind"` // proxies or control-plane
	Namespace string            `json:"namespace"`
	Pod       string            `json:"pod"`
	Component string            `json:"component"`
	Container string            `json:"container"`
	Image     string            `json:"image"`
	Version   string            `json:"version"`
	Node      string            `json:"node"`
	Files     map[string][]byte `json:"-"`
}

// New returns an empty workspace with an in-memory database (tests, diffs).
func New() *Workspace {
	w, err := Open("")
	if err != nil {
		panic("flightbay: cannot start the in-memory log database: " + err.Error())
	}
	return w
}

// Open returns an empty workspace whose logs, objects and events are stored in
// the DuckDB file at path (replaced if it exists). "" keeps it in memory.
func Open(path string) (*Workspace, error) {
	ldb, err := openDB(path)
	if err != nil {
		return nil, err
	}
	return &Workspace{ldb: ldb, streamSeq: map[string]int{}, objects: map[string]*Object{}, byType: map[typeKey][]*Object{}, resources: map[string]map[string]bundle.APIResource{},
		versions: map[string]json.RawMessage{}, coverage: map[string]bundle.Coverage{},
		subs: map[int]chan struct{}{}, state: ImportState{Phase: PhaseWaiting}, chunks: map[string]bool{},
		crds: map[string]map[string]map[string]any{}, diags: map[string]*Diagnostic{}, diagnosticSummary: map[string]json.RawMessage{}}, nil
}

// commit must be called with the write lock held.
func (w *Workspace) commit() {
	w.rev++
	w.state.Revision = w.rev
	for _, ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel signalled after every commit.
func (w *Workspace) Subscribe() (<-chan struct{}, func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := make(chan struct{}, 1)
	id := w.subID
	w.subID++
	w.subs[id] = ch
	return ch, func() {
		w.mu.Lock()
		delete(w.subs, id)
		w.mu.Unlock()
	}
}

// Revision is the current import revision.
func (w *Workspace) Revision() int64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.rev
}

// SetHeader records bundle.json and the resolved collection policy.
func (w *Workspace) SetHeader(h *bundle.Header) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.header = h
	w.policy = nil
	if len(h.Policy) > 0 {
		var p policy.Policy
		if json.Unmarshal(h.Policy, &p) == nil {
			w.policy = &p
		}
	}
	w.commit()
}

// Header returns bundle.json, nil before it arrives.
func (w *Workspace) Header() *bundle.Header {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.header
}

// Policy returns the customer's collection policy.
func (w *Workspace) Policy() *policy.Policy {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.policy
}

// SetManifest records manifest.json.
func (w *Workspace) SetManifest(m *bundle.Manifest) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.manifest = m
	w.commit()
}

// Manifest returns manifest.json, nil until the end of the stream.
func (w *Workspace) Manifest() *bundle.Manifest {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.manifest
}

// SetState updates import progress.
func (w *Workspace) SetState(fn func(*ImportState)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn(&w.state)
	w.commit()
}

// State returns import progress.
func (w *Workspace) State() ImportState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	state := w.state
	if state.Live != nil {
		live := *state.Live
		live.Paused = w.livePaused
		state.Live = &live
	}
	return state
}

func objKey(cluster, group, resource, ns, name string) string {
	return cluster + "\x00" + group + "\x00" + resource + "\x00" + ns + "\x00" + name
}

// RegisterResources records discovery for a cluster.
func (w *Workspace) RegisterResources(cluster string, rs []bundle.APIResource) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range rs {
		w.registerLocked(cluster, r)
	}
	w.commit()
}

func (w *Workspace) registerLocked(cluster string, r bundle.APIResource) {
	if w.resources[cluster] == nil {
		w.resources[cluster] = map[string]bundle.APIResource{}
	}
	key := r.Group + "/" + r.Resource
	if old, ok := w.resources[cluster][key]; ok {
		if r.Kind == "" {
			r.Kind = old.Kind
		}
		if len(r.ShortNames) == 0 {
			r.ShortNames = old.ShortNames
		}
		if r.SingularName == "" {
			r.SingularName = old.SingularName
		}
	}
	w.resources[cluster][key] = r
}

func diagKey(cluster, kind, ns, pod string) string {
	return cluster + "/" + kind + "/" + ns + "/" + pod
}

// AddDiagnostic records a diagnostic file (or meta.json) for a component pod.
func (w *Workspace) AddDiagnostic(cluster, kind, ns, pod, file string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	k := diagKey(cluster, kind, ns, pod)
	d := w.diags[k]
	if d == nil {
		d = &Diagnostic{Cluster: cluster, Kind: kind, Namespace: ns, Pod: pod, Files: map[string][]byte{}}
		w.diags[k] = d
	}
	if file == "meta.json" {
		var meta struct{ Component, Container, Image, Version, Node string }
		if json.Unmarshal(data, &meta) == nil {
			d.Component, d.Container, d.Image, d.Version, d.Node = meta.Component, meta.Container, meta.Image, meta.Version, meta.Node
		}
	} else {
		d.Files[file] = data
	}
	w.commit()
}

// Diagnostics lists captured components, optionally for one cluster.
func (w *Workspace) Diagnostics(cluster string) []Diagnostic {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []Diagnostic
	for _, d := range w.diags {
		if cluster == "" || d.Cluster == cluster {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Cluster+a.Component+a.Namespace+a.Pod < b.Cluster+b.Component+b.Namespace+b.Pod
	})
	return out
}

// Diagnostic returns one component pod's capture, or nil.
func (w *Workspace) Diagnostic(cluster, kind, ns, pod string) *Diagnostic {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if d := w.diags[diagKey(cluster, kind, ns, pod)]; d != nil {
		c := *d
		return &c
	}
	return nil
}

// SetDiagnosticSummary records the summary of detected diagnostic components.
func (w *Workspace) SetDiagnosticSummary(cluster string, raw json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.diagnosticSummary[cluster] = raw
	w.commit()
}

// DiagnosticSummary returns the diagnostic component summary for a cluster.
func (w *Workspace) DiagnosticSummary(cluster string) json.RawMessage {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.diagnosticSummary[cluster]
}

// AddCRD records a collected CustomResourceDefinition. Most schemas are stripped;
// HTTPRoute retains its schema for installed routing capability checks.
func (w *Workspace) AddCRD(cluster string, crd map[string]any) {
	md, _ := crd["metadata"].(map[string]any)
	name, _ := md["name"].(string)
	if name == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.crds[cluster] == nil {
		w.crds[cluster] = map[string]map[string]any{}
	}
	w.crds[cluster][name] = crd
	w.commit()
}

// CRDs returns the collected CRD definitions for a cluster, sorted by name.
func (w *Workspace) CRDs(cluster string) []map[string]any {
	w.mu.RLock()
	defer w.mu.RUnlock()
	names := make([]string, 0, len(w.crds[cluster]))
	for n := range w.crds[cluster] {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, w.crds[cluster][n])
	}
	return out
}

// CRD returns the definition for resource.group, or nil.
func (w *Workspace) CRD(cluster, group, resource string) map[string]any {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.crds[cluster][resource+"."+group]
}

// SetVersion records a cluster's /version.
func (w *Workspace) SetVersion(cluster string, raw json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.versions[cluster] = raw
	w.commit()
}

// Version returns a cluster's /version response.
func (w *Workspace) Version(cluster string) json.RawMessage {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.versions[cluster]
}

// AddObjects commits one resource chunk.
func (w *Workspace) AddObjects(cluster string, r bundle.APIResource, items []map[string]any, chunk string) {
	w.mirrorObjects(cluster, r, items) // the SQL copy, before resourceVersion is rewritten below
	w.mu.Lock()
	defer w.mu.Unlock()
	w.registerLocked(cluster, r)
	tk := typeKey{cluster, r.Group, r.Resource}
	now := time.Now().UTC()
	for _, it := range items {
		md, _ := it["metadata"].(map[string]any)
		ns, _ := md["namespace"].(string)
		name, _ := md["name"].(string)
		kind, _ := it["kind"].(string)
		if kind == "" {
			kind = r.Kind
		}
		w.rev++
		o := &Object{Cluster: cluster, Group: r.Group, Version: r.Version, Resource: r.Resource, Kind: kind, Namespaced: r.Namespaced,
			Namespace: ns, Name: name, RV: w.rev, Obj: it}
		if name != "" {
			w.recordInitialLocked(RevisionRef{cluster, r.Group, r.Resource, kind, ns, name}, it, objClusterRV(it), now)
		}
		if md != nil {
			md["resourceVersion"] = fmt.Sprint(o.RV)
		}
		k := objKey(cluster, r.Group, r.Resource, ns, name)
		if _, exists := w.objects[k]; !exists {
			w.byType[tk] = append(w.byType[tk], o)
			w.state.Objects++
		} else {
			for i, e := range w.byType[tk] {
				if e.Namespace == ns && e.Name == name {
					w.byType[tk][i] = o
				}
			}
		}
		w.objects[k] = o
	}
	w.commit()
}

// Clusters returns cluster names in bundle order.
func (w *Workspace) Clusters() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	if w.header != nil {
		for _, c := range w.header.Contexts {
			c = bundle.SafeID(c)
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	var extra []string
	for c := range w.resources {
		if !seen[c] {
			seen[c] = true
			extra = append(extra, c)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// Resources returns the resource types known for a cluster.
func (w *Workspace) Resources(cluster string) []bundle.APIResource {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []bundle.APIResource
	for _, r := range w.resources[cluster] {
		out = append(out, r)
	}
	out = append(out, w.virtualTypesLocked(cluster)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// FindResource resolves "pods", "po", "deployments.apps" or a kind for a cluster.
func (w *Workspace) FindResource(cluster, name string) (bundle.APIResource, bool) {
	name = strings.ToLower(name)
	for _, r := range w.Resources(cluster) {
		if r.Resource == name || r.Name() == name || strings.ToLower(r.Kind) == name || r.SingularName == name {
			return r, true
		}
		for _, s := range r.ShortNames {
			if s == name {
				return r, true
			}
		}
	}
	return bundle.APIResource{}, false
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// List returns objects of one type, optionally in one namespace and matching labels.
func (w *Workspace) List(cluster, group, resource, ns string, labels map[string]string) []*Object {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []*Object
	objs := w.byType[typeKey{cluster, group, resource}]
	if v, ok := w.virtualLocked(cluster, group, resource); ok {
		objs = v
	}
	for _, o := range objs {
		if ns != "" && o.Namespace != ns {
			continue
		}
		if len(labels) > 0 && !labelsMatch(o.Labels(), labels) {
			continue
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// AllObjects returns every object in a cluster ("" for all clusters).
func (w *Workspace) AllObjects(cluster string) []*Object {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []*Object
	for _, o := range w.objects {
		if cluster == "" || o.Cluster == cluster {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RV < out[j].RV })
	return out
}

// Get returns one object or nil.
func (w *Workspace) Get(cluster, group, resource, ns, name string) *Object {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if o := w.objects[objKey(cluster, group, resource, ns, name)]; o != nil {
		return o
	}
	if v, ok := w.virtualLocked(cluster, group, resource); ok {
		for _, o := range v {
			if o.Namespace == ns && o.Name == name {
				return o
			}
		}
	}
	return nil
}

// SetCoverage records a coverage record.
func (w *Workspace) SetCoverage(c bundle.Coverage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coverage[c.ID] = c
	w.state.CoverageSeen++
	w.commit()
}

// Coverage returns all coverage records.
func (w *Workspace) Coverage() []bundle.Coverage {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]bundle.Coverage, 0, len(w.coverage))
	for _, c := range w.coverage {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Collection is the state of one resource collection.
type Collection struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Items  int    `json:"items"`
}

// CollectionState tells loading, excluded, failed, truncated and complete apart.
func (w *Workspace) CollectionState(cluster, group, resource, kind string, namespaced bool) Collection {
	w.mu.RLock()
	defer w.mu.RUnlock()
	g := group
	if g == "" {
		g = "core"
	}
	if group == "" && resource == "namespaces" && !w.realNamespacesLocked(cluster) {
		return Collection{State: StateComplete, Reason: "synthesised from the namespaces of captured objects; namespaces themselves were not captured"}
	}
	if group == "" && resource == "events" && len(w.byType[typeKey{cluster, "", "events"}]) == 0 {
		for _, c := range w.coverage {
			if c.Type == bundle.CoverageEvents && c.Cluster == cluster {
				return Collection{State: c.State, Reason: c.Reason, Items: c.Items}
			}
		}
		return Collection{State: StateComplete, Reason: "captured events"}
	}
	for _, c := range w.coverage {
		if c.Type == bundle.CoverageResource && c.Cluster == cluster && c.Resource == resource && (c.Group == group || (group == "" && c.Group == "")) {
			st := c.State
			switch st {
			case bundle.StateComplete:
				st = StateComplete
			case bundle.StateFailed:
				st = StateFailed
			case bundle.StateTruncated:
				st = StateTruncated
			case bundle.StateRemoved:
				st = StateRemoved
			}
			if t := c.Trimmed; t != nil && t.Objects > 0 && st != StateRemoved {
				return Collection{State: StateTrimmed, Reason: fmt.Sprintf("%d removed by the customer before sending; the rest are here", t.Objects), Items: c.Items}
			}
			return Collection{State: st, Reason: c.Reason, Items: c.Items}
		}
	}
	_ = g
	if w.policy != nil {
		cat := policy.CatObjects
		if !(w.policy.Plan().Selected(cat) && w.policy.ResourceAllowed(resource, group, kind, namespaced)) &&
			!(group == "" && resource == "nodes" && w.policy.Plan().Selected(policy.CatNodes)) {
			return Collection{State: StateExcluded, Reason: "excluded by the customer's collection policy"}
		}
	}
	if w.state.Phase == PhaseLoading || w.state.Phase == PhaseWaiting {
		return Collection{State: StateLoading, Reason: "not loaded yet"}
	}
	return Collection{State: StateNotCollected, Reason: "no collection record in this bundle"}
}

// AddEvents commits one events chunk.
func (w *Workspace) AddEvents(cluster string, items []map[string]any) {
	w.mirrorEvents(cluster, items)
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, it := range items {
		w.events = append(w.events, Event{Cluster: cluster, Obj: it, RV: w.rev + 1}) // the revision commit() moves to
	}
	w.state.Events += len(items)
	w.commit()
}

// Events returns captured events for a cluster ("" for all).
func (w *Workspace) Events(cluster string) []Event {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var out []Event
	for _, e := range w.events {
		if cluster == "" || e.Cluster == cluster {
			out = append(out, e)
		}
	}
	return out
}

// AddCollectionLog appends collection-log.jsonl lines.
func (w *Workspace) AddCollectionLog(lines []json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.collection = append(w.collection, lines...)
	w.commit()
}

// CollectionLog returns the collection log.
func (w *Workspace) CollectionLog() []json.RawMessage {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]json.RawMessage{}, w.collection...)
}

// SetRedaction records the redaction report.
func (w *Workspace) SetRedaction(raw json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.redaction = raw
	w.commit()
}

// Redaction returns the redaction report.
func (w *Workspace) Redaction() json.RawMessage {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.redaction
}

// MarkChunk records that a bundle member has been committed, so a retried or
// repeated import does not add it twice.
func (w *Workspace) MarkChunk(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.chunks[name] = true
}

// HasChunk reports whether a bundle member was already committed.
func (w *Workspace) HasChunk(name string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.chunks[name]
}
