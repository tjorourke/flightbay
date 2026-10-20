package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
)

// PhaseLive is a workspace that mirrors a running cluster: the initial list
// has completed and watches keep it up to date.
const PhaseLive = "live"

// SourceLive marks a header written by flightbay-desk live.
const SourceLive = "live"

// LiveState describes the connection behind a live workspace.
type LiveState struct {
	Paused          bool               `json:"paused"`
	Clusters        []LiveClusterState `json:"clusters,omitempty"`
	Live            bool               `json:"live"`
	Context         string             `json:"context"`
	Server          string             `json:"server"`
	Connected       bool               `json:"connected"`
	LastEvent       time.Time          `json:"lastEvent"`
	Reconnects      int64              `json:"reconnects"`
	RetryingStreams int64              `json:"retryingStreams"`
	Watches         int                `json:"watches"`
	ObjectsWatched  int                `json:"objectsWatched"`
	LogStreams      int                `json:"logStreams"`
	LogStreamsTotal int                `json:"logStreamsTotal"`
	LogLinesDropped int64              `json:"logLinesDropped"`
	MaxLogStreams   int                `json:"maxLogStreams"`
	MaxLogLines     int                `json:"maxLogLines"`
	Diagnostics     bool               `json:"diagnostics"`
	Requests        map[string]int64   `json:"requests,omitempty"` // requests made to the cluster, by verb
	Refused         int64              `json:"refused"`            // requests the read-only guard refused
}

// BeginLiveIngest blocks live data commits while ingestion is paused. Watches
// retain their resourceVersions and use their normal reconnect/relist recovery.
// Waiting is cancellable so closing a paused desk never strands its readers.
func (w *Workspace) BeginLiveIngest(ctx context.Context) (func(), bool) {
	for {
		w.livePauseMu.Lock()
		resume := w.liveResume
		if resume == nil {
			w.liveGate.RLock()
			w.livePauseMu.Unlock()
			if ctx.Err() != nil {
				w.liveGate.RUnlock()
				return nil, false
			}
			return w.liveGate.RUnlock, true
		}
		w.livePauseMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-resume:
		}
	}
}

// TryLiveIngest lets periodic flushes leave bounded buffers alone while paused.
func (w *Workspace) TryLiveIngest() (func(), bool) {
	w.livePauseMu.Lock()
	defer w.livePauseMu.Unlock()
	if w.liveResume != nil {
		return nil, false
	}
	w.liveGate.RLock()
	return w.liveGate.RUnlock, true
}

// SetLivePaused waits for active data commits (including queued logs) before
// reporting paused. Existing captured data remains readable throughout.
func (w *Workspace) SetLivePaused(paused bool) {
	w.livePauseMu.Lock()
	defer w.livePauseMu.Unlock()
	if paused == (w.liveResume != nil) {
		return
	}
	w.liveGate.Lock()
	defer w.liveGate.Unlock()
	if paused {
		w.liveResume = make(chan struct{})
		w.WaitLogs()
	} else {
		close(w.liveResume)
		w.liveResume = nil
	}
	w.mu.Lock()
	w.livePaused = paused
	w.commit()
	w.mu.Unlock()
}

type LiveClusterState struct {
	LiveState
	Phase string `json:"phase"`
	Error string `json:"error,omitempty"`
}

// IsLive reports whether the workspace mirrors a running cluster, as opposed
// to a bundle (including a bundle saved from a live workspace).
func (w *Workspace) IsLive() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.header != nil && w.header.Source == SourceLive && w.manifest == nil
}

// logNowLocked is the "now" that since: log queries count back from: the
// newest captured line for a bundle, the wall clock for a live workspace.
func (w *Workspace) logNowLocked() time.Time {
	if w.header != nil && w.header.Source == SourceLive && w.manifest == nil {
		return time.Now().UTC()
	}
	return w.logEnd
}

func objName(obj map[string]any) (ns, name string) {
	md, _ := obj["metadata"].(map[string]any)
	ns, _ = md["namespace"].(string)
	name, _ = md["name"].(string)
	return
}

// UpsertObject adds or replaces one object, as a watch ADDED or MODIFIED
// event does. The object gets a new resource version, the revision moves on
// and the SQL copy is replaced. It reports whether the object is new.
func (w *Workspace) UpsertObject(cluster string, r bundle.APIResource, obj map[string]any) bool {
	ns, name := objName(obj)
	if name == "" {
		return false
	}
	w.deleteObjectRows(cluster, r.Group, r.Resource, ns, name)
	w.mirrorObjects(cluster, r, []map[string]any{obj})
	// The revision snapshot and its hash are built before the lock, and the
	// cluster's resourceVersion is read before it is rewritten below.
	snap := revisionSnapshot(obj)
	hash, gen, clusterRV := snapshotHash(snap), objGeneration(obj), objClusterRV(obj)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.registerLocked(cluster, r)
	kind, _ := obj["kind"].(string)
	if kind == "" {
		kind = r.Kind
	}
	w.recordLocked(RevisionRef{cluster, r.Group, r.Resource, kind, ns, name}, snap, hash, gen, clusterRV, time.Now().UTC())
	w.rev++
	o := &Object{Cluster: cluster, Group: r.Group, Version: r.Version, Resource: r.Resource, Kind: kind, Namespaced: r.Namespaced,
		Namespace: ns, Name: name, RV: w.rev, Obj: obj}
	if md, _ := obj["metadata"].(map[string]any); md != nil {
		md["resourceVersion"] = fmt.Sprint(o.RV)
	}
	tk := typeKey{cluster, r.Group, r.Resource}
	k := objKey(cluster, r.Group, r.Resource, ns, name)
	_, exists := w.objects[k]
	if !exists {
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
	w.commit()
	return !exists
}

// DeleteObject removes one object, as a watch DELETED event does, and
// reports whether it was present.
func (w *Workspace) DeleteObject(cluster, group, resource, ns, name string) bool {
	w.mu.Lock()
	k := objKey(cluster, group, resource, ns, name)
	old, ok := w.objects[k]
	if !ok {
		w.mu.Unlock()
		return false
	}
	w.recordDeleteLocked(RevisionRef{cluster, group, resource, old.Kind, ns, name}, time.Now().UTC())
	delete(w.objects, k)
	tk := typeKey{cluster, group, resource}
	list := w.byType[tk]
	for i, e := range list {
		if e.Namespace == ns && e.Name == name {
			w.byType[tk] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	w.state.Objects--
	w.commit()
	w.mu.Unlock()
	w.deleteObjectRows(cluster, group, resource, ns, name)
	return true
}

func (w *Workspace) deleteObjectRows(cluster, group, resource, ns, name string) {
	if !haveDuckDB() {
		return
	}
	_, _ = w.ldb.db.Exec("DELETE FROM objects WHERE cluster = ? AND api_group = ? AND resource = ? AND namespace = ? AND name = ?", cluster, group, resource, ns, name)
}

func eventKey(cluster, ns, name string) string { return cluster + "\x00" + ns + "\x00" + name }

// UpsertEvent adds an event or replaces the one with the same name (a
// repeated event is the same object with a higher count).
func (w *Workspace) UpsertEvent(cluster string, obj map[string]any) {
	ns, name := objName(obj)
	if name == "" {
		w.AddEvents(cluster, []map[string]any{obj})
		return
	}
	w.deleteEventRows(cluster, ns, name)
	w.mirrorEvents(cluster, []map[string]any{obj})
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rev++
	e := Event{Cluster: cluster, Obj: obj, RV: w.rev}
	if i, ok := w.eventIndexLocked(cluster, ns, name); ok {
		w.events[i] = e
	} else {
		w.events = append(w.events, e)
		w.state.Events++
	}
	w.commit()
}

// DeleteEvent removes an event, reporting whether it was present.
func (w *Workspace) DeleteEvent(cluster, ns, name string) bool {
	w.mu.Lock()
	i, ok := w.eventIndexLocked(cluster, ns, name)
	if !ok {
		w.mu.Unlock()
		return false
	}
	w.events = append(w.events[:i:i], w.events[i+1:]...)
	w.state.Events--
	w.commit()
	w.mu.Unlock()
	w.deleteEventRows(cluster, ns, name)
	return true
}

func (w *Workspace) eventIndexLocked(cluster, ns, name string) (int, bool) {
	for i := len(w.events) - 1; i >= 0; i-- {
		e := w.events[i]
		if e.Cluster != cluster {
			continue
		}
		if n, nm := objName(e.Obj); n == ns && nm == name {
			return i, true
		}
	}
	return 0, false
}

func (w *Workspace) deleteEventRows(cluster, ns, name string) {
	if !haveDuckDB() {
		return
	}
	_, _ = w.ldb.db.Exec("DELETE FROM events WHERE cluster = ? AND namespace = ? AND json_extract_string(event, '$.metadata.name') = ?", cluster, ns, name)
}

// PutCoverage records or replaces a coverage record without counting it as
// a newly received member (live workspaces rewrite their records).
func (w *Workspace) PutCoverage(c bundle.Coverage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.coverage[c.ID]; !ok {
		w.state.CoverageSeen++
	}
	w.coverage[c.ID] = c
	w.commit()
}

// CoverageRecord returns one coverage record by id.
func (w *Workspace) CoverageRecord(id string) (bundle.Coverage, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	c, ok := w.coverage[id]
	return c, ok
}

// RemoveDiagnostic drops a component pod's captured diagnostics so a fresh
// capture replaces them rather than mixing with them.
func (w *Workspace) RemoveDiagnostic(cluster, kind, ns, pod string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.diags, diagKey(cluster, kind, ns, pod))
	for id, c := range w.coverage {
		if c.Type == bundle.CoverageDiagnostic && c.Cluster == cluster && c.Namespace == ns && c.Pod == pod {
			delete(w.coverage, id)
		}
	}
	w.commit()
}

// RegisteredResources returns the resource types recorded by discovery or by
// objects, without the virtual types the workspace synthesises.
func (w *Workspace) RegisteredResources(cluster string) []bundle.APIResource {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]bundle.APIResource, 0, len(w.resources[cluster]))
	for _, r := range w.resources[cluster] {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// LogStreamKey names one container log stream.
type LogStreamKey struct {
	Cluster, Namespace, Pod, Container, Stream string
}

// AllLogStreams lists every stream with lines loaded.
func (w *Workspace) AllLogStreams() []LogStreamKey {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]LogStreamKey, 0, len(w.streamSeq))
	for k, n := range w.streamSeq {
		if n == 0 {
			continue
		}
		p := strings.SplitN(k, "/", 5)
		if len(p) == 5 {
			out = append(out, LogStreamKey{p[0], p[1], p[2], p[3], p[4]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Cluster+"/"+a.Namespace+"/"+a.Pod+"/"+a.Container+"/"+a.Stream < b.Cluster+"/"+b.Namespace+"/"+b.Pod+"/"+b.Container+"/"+b.Stream
	})
	return out
}

// TrimLogs deletes the oldest log lines so at most max remain, and returns
// how many were dropped. Live workspaces call it to keep the database bounded.
func (w *Workspace) TrimLogs(max int) int {
	if max <= 0 || !haveDuckDB() {
		return 0
	}
	w.ldb.pending.Wait()
	var total int
	if err := w.ldb.db.QueryRow("SELECT count(*) FROM log_lines").Scan(&total); err != nil || total <= max {
		return 0
	}
	var cut int64
	if err := w.ldb.db.QueryRow("SELECT id FROM log_lines ORDER BY id DESC LIMIT 1 OFFSET ?", max).Scan(&cut); err != nil {
		return 0
	}
	res, err := w.ldb.db.Exec("DELETE FROM log_lines WHERE id <= ?", cut)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	var minTS int64
	_ = w.ldb.db.QueryRow("SELECT coalesce(min(ts), 0) FROM log_lines WHERE ts <> 0").Scan(&minTS)
	w.mu.Lock()
	w.state.LogLines -= int(n)
	if w.state.LogLines < 0 {
		w.state.LogLines = 0
	}
	if minTS > 0 {
		w.logStart = time.Unix(0, minTS).UTC()
	}
	w.commit()
	w.mu.Unlock()
	return int(n)
}

// objectRows is a helper for tests: how many SQL rows mirror an object.
func (w *Workspace) objectRows(cluster, group, resource, ns, name string) int {
	var n int
	_ = w.ldb.db.QueryRow("SELECT count(*) FROM objects WHERE cluster = ? AND api_group = ? AND resource = ? AND namespace = ? AND name = ?", cluster, group, resource, ns, name).Scan(&n)
	return n
}

// eventJSON is used by tests to read the SQL copy of an event.
func (w *Workspace) eventRows(cluster, ns, name string) []string {
	rows, err := w.ldb.db.Query("SELECT event FROM events WHERE cluster = ? AND namespace = ? AND json_extract_string(event, '$.metadata.name') = ?", cluster, ns, name)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// LogStreamLines returns every stream with lines loaded and how many, for
// workspaces (a live mirror) whose coverage does not list streams one by one.
func (w *Workspace) LogStreamLines() map[LogStreamKey]int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make(map[LogStreamKey]int, len(w.streamSeq))
	for k, n := range w.streamSeq {
		if n == 0 {
			continue
		}
		if p := strings.SplitN(k, "/", 5); len(p) == 5 {
			out[LogStreamKey{p[0], p[1], p[2], p[3], p[4]}] = n
		}
	}
	return out
}
