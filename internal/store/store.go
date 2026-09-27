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
	"github.com/tjorourke/flightbay/internal/logs"
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
)

// ImportState is the progress of the running import.
type ImportState struct {
	Phase         string    `json:"phase"`
	Source        string    `json:"source"`
	BytesReceived int64     `json:"bytesReceived"`
	Members       int       `json:"members"`
	Objects       int       `json:"objects"`
	LogLines      int       `json:"logLines"`
	Events        int       `json:"events"`
	CoverageSeen  int       `json:"coverageSeen"`
	Verified      bool      `json:"verified"`
	Error         string    `json:"error,omitempty"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished,omitempty"`
	Revision      int64     `json:"revision"`
	Deferred      []string  `json:"deferred,omitempty"`
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
}

type typeKey struct{ cluster, group, resource string }

// Workspace is one opened bundle.
type Workspace struct {
	mu         sync.RWMutex
	header     *bundle.Header
	policy     *policy.Policy
	manifest   *bundle.Manifest
	objects    map[string]*Object
	byType     map[typeKey][]*Object
	resources  map[string]map[string]bundle.APIResource
	versions   map[string]json.RawMessage
	coverage   map[string]bundle.Coverage
	events     []Event
	records    []*logs.Record
	streamRecs map[string][]*logs.Record
	collection []json.RawMessage
	redaction  json.RawMessage
	state      ImportState
	rev        int64
	nextID     int64
	subs       map[int]chan struct{}
	subID      int
	chunks     map[string]bool
	crds       map[string]map[string]map[string]any // cluster -> crd name -> slim CRD
	diags      map[string]*Diagnostic               // cluster/kind/ns/pod -> captured diagnostics
	soloSum    map[string]json.RawMessage           // cluster -> solo/summary.json
}

// Diagnostic is one Solo component's captured admin output.
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

// New returns an empty workspace.
func New() *Workspace {
	return &Workspace{objects: map[string]*Object{}, byType: map[typeKey][]*Object{}, resources: map[string]map[string]bundle.APIResource{},
		versions: map[string]json.RawMessage{}, coverage: map[string]bundle.Coverage{}, streamRecs: map[string][]*logs.Record{},
		subs: map[int]chan struct{}{}, state: ImportState{Phase: PhaseWaiting}, chunks: map[string]bool{},
		crds: map[string]map[string]map[string]any{}, diags: map[string]*Diagnostic{}, soloSum: map[string]json.RawMessage{}}
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
	return w.state
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

// SetSoloSummary records solo/summary.json for a cluster.
func (w *Workspace) SetSoloSummary(cluster string, raw json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.soloSum[cluster] = raw
	w.commit()
}

// SoloSummary returns solo/summary.json for a cluster.
func (w *Workspace) SoloSummary(cluster string) json.RawMessage {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.soloSum[cluster]
}

// AddCRD records a collected CustomResourceDefinition (schema stripped).
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
	w.mu.Lock()
	defer w.mu.Unlock()
	w.registerLocked(cluster, r)
	tk := typeKey{cluster, r.Group, r.Resource}
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
	for _, o := range w.byType[typeKey{cluster, group, resource}] {
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
	return w.objects[objKey(cluster, group, resource, ns, name)]
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
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, it := range items {
		w.events = append(w.events, Event{Cluster: cluster, Obj: it})
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

func streamKey(cluster, ns, pod, container, stream string) string {
	return cluster + "/" + ns + "/" + pod + "/" + container + "/" + stream
}

// AddLogLines parses and commits one log chunk.
func (w *Workspace) AddLogLines(cluster, ns, pod, container, stream string, lines []string, chunk string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := streamKey(cluster, ns, pod, container, stream)
	seq := len(w.streamRecs[key])
	node := ""
	if o := w.objects[objKey(cluster, "", "pods", ns, pod)]; o != nil {
		spec, _ := o.Obj["spec"].(map[string]any)
		node, _ = spec["nodeName"].(string)
	}
	for _, l := range lines {
		if l == "" {
			continue
		}
		ts, rest := logs.SplitTimestamp(l)
		p := logs.Parse(rest)
		w.nextID++
		rec := &logs.Record{ID: w.nextID, Seq: seq, TS: ts, Cluster: cluster, Namespace: ns, Pod: pod, Container: container, Stream: stream,
			Node: node, Level: p.Level, Scope: p.Scope, Msg: p.Msg, Fields: p.Fields, Raw: rest, Chunk: chunk}
		seq++
		w.records = append(w.records, rec)
		w.streamRecs[key] = append(w.streamRecs[key], rec)
	}
	w.state.LogLines = len(w.records)
	w.commit()
}

// LogStream returns a captured container stream in line order.
func (w *Workspace) LogStream(cluster, ns, pod, container, stream string) []*logs.Record {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]*logs.Record{}, w.streamRecs[streamKey(cluster, ns, pod, container, stream)]...)
}

// Streams lists the captured streams for a pod.
func (w *Workspace) Streams(cluster, ns, pod string) []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	prefix := cluster + "/" + ns + "/" + pod + "/"
	var out []string
	for k := range w.streamRecs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out
}

// LogFilter selects log records.
type LogFilter struct {
	Query     string
	Cluster   string
	Namespace string
	Pod       string
	Container string
	Level     string
	Stream    string
	From, To  time.Time
	Limit     int
	Offset    int
}

// Bucket is one histogram bar.
type Bucket struct {
	Start time.Time `json:"start"`
	Count int       `json:"count"`
}

// LogResult is a consistent page of search results.
type LogResult struct {
	Total     int                       `json:"total"`
	Records   []*logs.Record            `json:"records"`
	Facets    map[string]map[string]int `json:"facets"`
	Histogram []Bucket                  `json:"histogram"`
	Revision  int64                     `json:"revision"`
	Scanned   int                       `json:"scanned"`
	Loading   bool                      `json:"loading"`
	Message   string                    `json:"message,omitempty"`
	From      time.Time                 `json:"from"`
	To        time.Time                 `json:"to"`
}

// SearchLogs evaluates a query over every committed record.
func (w *Workspace) SearchLogs(f LogFilter) (*LogResult, error) {
	q, err := logs.ParseQuery(f.Query)
	if err != nil {
		return nil, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	var end time.Time
	for _, r := range w.records {
		if r.TS.After(end) {
			end = r.TS
		}
	}
	q.Reference = end
	res := &LogResult{Facets: map[string]map[string]int{"level": {}, "namespace": {}, "pod": {}, "container": {}, "stream": {}, "cluster": {}},
		Revision: w.rev, Scanned: len(w.records), Loading: w.state.Phase == PhaseLoading || w.state.Phase == PhaseWaiting}
	var matched, inRange []*logs.Record
	for _, r := range w.records {
		if f.Cluster != "" && r.Cluster != f.Cluster {
			continue
		}
		if !q.Match(r) {
			continue
		}
		// Facets ignore the facet filters themselves so users can widen a selection.
		res.Facets["level"][r.Level]++
		res.Facets["namespace"][r.Namespace]++
		res.Facets["pod"][r.Pod]++
		res.Facets["container"][r.Container]++
		res.Facets["stream"][r.Stream]++
		res.Facets["cluster"][r.Cluster]++
		if (f.Namespace != "" && r.Namespace != f.Namespace) || (f.Pod != "" && r.Pod != f.Pod) || (f.Container != "" && r.Container != f.Container) ||
			(f.Level != "" && r.Level != f.Level) || (f.Stream != "" && r.Stream != f.Stream) {
			continue
		}
		matched = append(matched, r)
		if (!f.From.IsZero() && r.TS.Before(f.From)) || (!f.To.IsZero() && r.TS.After(f.To)) {
			continue
		}
		inRange = append(inRange, r)
	}
	res.Histogram, res.From, res.To = histogram(matched, 48)
	logs.SortNewestFirst(inRange)
	res.Total = len(inRange)
	if f.Limit <= 0 {
		f.Limit = 200
	}
	if f.Offset < len(inRange) {
		endIdx := f.Offset + f.Limit
		if endIdx > len(inRange) {
			endIdx = len(inRange)
		}
		res.Records = inRange[f.Offset:endIdx]
	}
	if res.Total == 0 {
		switch {
		case w.policy != nil && !w.policy.Plan().Selected(policy.CatLogs):
			res.Message = "Logs were excluded by the customer's collection policy."
		case res.Loading:
			res.Message = "No matches in the data loaded so far."
		default:
			res.Message = "No matches in the captured logs."
		}
	}
	return res, nil
}

func histogram(rs []*logs.Record, n int) ([]Bucket, time.Time, time.Time) {
	var lo, hi time.Time
	for _, r := range rs {
		if r.TS.IsZero() {
			continue
		}
		if lo.IsZero() || r.TS.Before(lo) {
			lo = r.TS
		}
		if r.TS.After(hi) {
			hi = r.TS
		}
	}
	if lo.IsZero() {
		return nil, lo, hi
	}
	span := hi.Sub(lo)
	if span < time.Minute {
		span = time.Minute
	}
	step := span / time.Duration(n)
	if step <= 0 {
		step = time.Second
	}
	out := make([]Bucket, n)
	for i := range out {
		out[i].Start = lo.Add(time.Duration(i) * step)
	}
	for _, r := range rs {
		if r.TS.IsZero() {
			continue
		}
		i := int(r.TS.Sub(lo) / step)
		if i >= n {
			i = n - 1
		}
		out[i].Count++
	}
	return out, lo, hi
}

// LogContext returns up to n lines either side of a record in its stream.
func (w *Workspace) LogContext(id int64, n int) []*logs.Record {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, r := range w.records {
		if r.ID != id {
			continue
		}
		s := w.streamRecs[streamKey(r.Cluster, r.Namespace, r.Pod, r.Container, r.Stream)]
		lo, hi := r.Seq-n, r.Seq+n+1
		if lo < 0 {
			lo = 0
		}
		if hi > len(s) {
			hi = len(s)
		}
		return append([]*logs.Record{}, s[lo:hi]...)
	}
	return nil
}

// AllLogs returns every committed record (read-only).
func (w *Workspace) AllLogs() []*logs.Record {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]*logs.Record{}, w.records...)
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
