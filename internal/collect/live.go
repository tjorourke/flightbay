package collect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/logs"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
	"github.com/tjorourke/flightbay/internal/store"
)

// LiveOptions tune a live mirror.
type LiveOptions struct {
	Context       string         // kubeconfig context; the cluster is named after it
	Server        string         // API server URL, for display
	Version       string         // flightbay-desk version, recorded in the header
	MaxLogStreams int            // container streams followed at once (default 100)
	LogTail       int64          // lines of history per stream when following starts (default 500)
	MaxLogLines   int            // lines kept in the workspace; the oldest go first (default 1,000,000)
	RingLines     int            // lines buffered per stream between flushes (default 10,000)
	FlushEvery    time.Duration  // how often buffered lines are stored (default 1s)
	Rotate        time.Duration  // over the stream cap, how often the lower-priority streams take turns (default 5m)
	Guard         *ReadOnlyGuard // counts requests; nil if the caller did not install one
	Progress      func(string)
}

func (o *LiveOptions) defaults() {
	if o.MaxLogStreams <= 0 {
		o.MaxLogStreams = 100
	}
	if o.LogTail == 0 {
		o.LogTail = 500
	}
	if o.MaxLogLines <= 0 {
		o.MaxLogLines = 1_000_000
	}
	if o.RingLines <= 0 {
		o.RingLines = 10_000
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = time.Second
	}
	if o.Rotate <= 0 {
		o.Rotate = 5 * time.Minute
	}
}

// Live keeps a workspace in step with a running cluster: an initial list of
// every selected type, then watches from that list's resourceVersion. It
// uses the collector's policy, scope and redactor, so what enters the
// workspace is exactly what a capture of the cluster would hold at that
// moment. It only reads: list, watch, get and pods/log, plus port-forward
// for on-demand diagnostics when the policy allows it.
type Live struct {
	C  *Collector // Policy and Redactor; Writer is not used
	WS *store.Workspace
	CL Clients
	O  LiveOptions

	cluster     string
	kinds       map[string]string // group/Kind -> resource, for diagnostics filtering
	lastEvent   atomic.Int64
	reconnects  atomic.Int64
	failing     atomic.Int64
	watches     atomic.Int64
	lastPublish atomic.Int64
	logs        *logManager
	diagMu      sync.Mutex

	covMu  sync.Mutex
	covSet map[string]bundle.Coverage

	rvMu sync.Mutex
	rvs  map[string]map[string]string // type/scope -> ns/name -> cluster resourceVersion
}

// Cluster is the name the mirrored cluster has in the workspace.
func (l *Live) Cluster() string { return bundle.SafeID(l.O.Context) }

// DiagnosticsEnabled reports whether on-demand diagnostics can run.
func (l *Live) DiagnosticsEnabled() bool {
	return l.C.diagSelected() && l.C.Policy.PortForward && l.CL.Forward != nil
}

func (l *Live) progress(format string, a ...any) {
	if l.O.Progress != nil {
		l.O.Progress(fmt.Sprintf(format, a...))
	}
}

func (l *Live) touch() { l.lastEvent.Store(time.Now().UnixNano()) }

// Run mirrors the cluster until ctx ends. It returns once the context is
// cancelled, or early if the cluster cannot be reached at all.
func (l *Live) Run(ctx context.Context) error {
	l.C.defaults()
	l.O.defaults()
	l.cluster = l.Cluster()
	l.covSet = map[string]bundle.Coverage{}
	l.rvs = map[string]map[string]string{}
	p := l.C.Policy
	plan := p.Plan()
	pol, _ := json.Marshal(p)
	var planned []string
	for _, cat := range plan.Categories {
		planned = append(planned, string(cat))
	}
	start := time.Now().UTC()
	l.WS.SetHeader(&bundle.Header{Case: p.Case, Collector: "flightbay-desk live " + l.O.Version, CaptureStart: start,
		Contexts: []string{l.O.Context}, Policy: pol, Planned: planned, Unavailable: plan.Unavailable, Source: store.SourceLive})
	ls := l.liveState(true)
	l.WS.SetState(func(s *store.ImportState) {
		s.Phase, s.Source, s.Started = store.PhaseLoading, "live:"+l.O.Context, start
		s.Live = ls
	})
	l.progress("discovery")
	if v, err := l.CL.Discovery.ServerVersion(); err == nil {
		b, _ := json.Marshal(v)
		l.WS.SetVersion(l.cluster, b)
	} else {
		l.C.record(LogEntry{Cluster: l.cluster, Operation: "version", Result: "failed", Error: err.Error()})
	}
	all, err := l.C.discover(l.cluster, l.CL)
	if err != nil {
		l.WS.SetState(func(s *store.ImportState) {
			s.Phase, s.Error = store.PhaseFailed, "cannot reach the cluster: "+err.Error()
		})
		return err
	}
	l.kinds = map[string]string{}
	var selected []bundle.APIResource
	for _, r := range all {
		l.kinds[r.Group+"/"+r.Kind] = r.Resource
		if l.C.objectAllowed(r) {
			selected = append(selected, r)
		}
	}
	l.WS.RegisterResources(l.cluster, selected)
	l.readCRDs(ctx, selected)

	l.logs = newLogManager(l)
	wantPods := plan.Selected(policy.CatLogs) || l.C.diagSelected()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // initial lists in parallel, politely
	launch := func(f *feed) {
		wg.Add(1)
		var once sync.Once
		f.initDone = func() { once.Do(wg.Done) }
		go func() {
			sem <- struct{}{}
			released := false
			release := func() {
				if !released {
					released = true
					<-sem
				}
			}
			f.afterInit = release
			l.runFeed(ctx, f)
			release()
			f.initDone()
		}()
	}
	storedPods := false
	for _, r := range selected {
		r := r
		isPods := r.Group == "" && r.Resource == "pods"
		if isPods && l.C.clientFor(r) == "dynamic" {
			storedPods = true
		}
		for _, ns := range l.C.scopes(r.Namespaced) {
			launch(l.objectFeed(r, ns, true, isPods && l.C.clientFor(r) == "dynamic" && wantPods))
		}
	}
	if wantPods && !storedPods {
		// Logs and diagnostics need pods even when pod objects are not kept.
		r := bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}
		for _, ns := range l.C.scopes(true) {
			launch(l.objectFeed(r, ns, false, true))
		}
	}
	if plan.Selected(policy.CatEvents) {
		for _, ns := range l.C.scopes(true) {
			launch(l.eventFeed(ns))
		}
	}
	go l.namespaceLoop(ctx, selected)

	initDone := make(chan struct{})
	go func() { wg.Wait(); close(initDone) }()
	status := time.NewTicker(time.Second)
	defer status.Stop()
waitInit:
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-initDone:
			break waitInit
		case <-status.C:
			l.publish(false)
		}
	}
	ls = l.liveState(true)
	l.WS.SetState(func(s *store.ImportState) {
		s.Phase, s.Finished = store.PhaseLive, time.Now().UTC()
		s.Live = ls
	})
	l.progress("live: initial list complete in %s", time.Since(start).Round(time.Millisecond))
	if plan.Selected(policy.CatLogs) {
		go l.logs.run(ctx)
	}
	if l.DiagnosticsEnabled() {
		go func() {
			if err := l.refreshAllDiagnostics(ctx); err != nil && ctx.Err() == nil {
				l.progress("diagnostics: %v", err)
			}
		}()
	}
	redactTick := time.NewTicker(10 * time.Second)
	defer redactTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-status.C:
			l.publish(false)
		case <-redactTick.C:
			rep, _ := json.Marshal(l.C.Redactor.Report())
			if string(rep) != string(l.WS.Redaction()) {
				l.WS.SetRedaction(rep)
			}
			l.flushCollectionLog()
		}
	}
}

func (l *Live) flushCollectionLog() {
	l.C.mu.Lock()
	entries := l.C.entries
	l.C.entries = nil
	l.C.mu.Unlock()
	if len(entries) == 0 {
		return
	}
	var lines []json.RawMessage
	for _, e := range entries {
		b, _ := json.Marshal(e)
		lines = append(lines, b)
	}
	l.WS.AddCollectionLog(lines)
}

func (l *Live) liveState(connected bool) *store.LiveState {
	st := &store.LiveState{Live: true, Context: l.O.Context, Server: l.O.Server, Connected: connected && l.failing.Load() == 0,
		Reconnects: l.reconnects.Load(), Watches: int(l.watches.Load()), MaxLogStreams: l.O.MaxLogStreams, MaxLogLines: l.O.MaxLogLines,
		Diagnostics: l.DiagnosticsEnabled()}
	if t := l.lastEvent.Load(); t > 0 {
		st.LastEvent = time.Unix(0, t).UTC()
	}
	st.ObjectsWatched = l.WS.State().Objects
	if l.logs != nil {
		st.LogStreams, st.LogStreamsTotal, st.LogLinesDropped = l.logs.counts()
	}
	if l.O.Guard != nil {
		st.Requests, st.Refused = l.O.Guard.Counts()
	}
	return st
}

// publish records the connection state in the workspace. Only a change that
// matters, or 15 s passing, commits: every commit is a new revision, and
// views and watches wake on revisions.
func (l *Live) publish(force bool) {
	st := l.liveState(true)
	cur := l.WS.State().Live
	same := cur != nil && cur.Connected == st.Connected && cur.Reconnects == st.Reconnects && cur.Watches == st.Watches &&
		cur.ObjectsWatched == st.ObjectsWatched && cur.LogStreams == st.LogStreams && cur.LogStreamsTotal == st.LogStreamsTotal &&
		cur.LogLinesDropped == st.LogLinesDropped && cur.Refused == st.Refused && cur.LastEvent.Equal(st.LastEvent) &&
		sumRequests(cur.Requests) == sumRequests(st.Requests)
	if same && !force && time.Since(time.Unix(0, l.lastPublish.Load())) < 15*time.Second {
		return
	}
	l.lastPublish.Store(time.Now().UnixNano())
	l.WS.SetState(func(s *store.ImportState) { s.Live = st })
}

func sumRequests(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}

// readCRDs reads the definitions of the selected custom resource types once,
// as the collector does, so printer columns work.
func (l *Live) readCRDs(ctx context.Context, selected []bundle.APIResource) {
	if l.CL.Dynamic == nil {
		return
	}
	for _, r := range selected {
		if !strings.Contains(r.Group, ".") {
			continue
		}
		name := r.Resource + "." + r.Group
		u, err := l.CL.Dynamic.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			l.C.record(LogEntry{Cluster: l.cluster, Operation: "crd", Target: name, Result: "failed", Error: err.Error()})
			if apierrors.IsForbidden(err) {
				return
			}
			continue
		}
		l.WS.AddCRD(l.cluster, SlimCRD(u.Object))
	}
}

// namespaceLoop keeps the Namespace objects in scope, which a namespaced run
// reads by name (their labels decide ambient enrolment). They change rarely,
// so they are polled with get rather than watched, which keeps the RBAC to
// get on those names.
func (l *Live) namespaceLoop(ctx context.Context, selected []bundle.APIResource) {
	if !l.C.Policy.Plan().Selected(policy.CatObjects) || resourceMatchAny(l.C.Policy.ExcludeResources, "namespaces") {
		return
	}
	for _, r := range selected {
		if r.Group == "" && r.Resource == "namespaces" {
			return // watched as a selected type (--cluster-scoped)
		}
	}
	r := bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace", SingularName: "namespace", ShortNames: []string{"ns"}}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	loc := fmt.Sprintf("clusters/%s/resources/namespaces", l.cluster)
	cov := bundle.Coverage{ID: l.cluster + ".resource.core.v1.namespaces", Cluster: l.cluster, Type: bundle.CoverageResource, Version: "v1", Resource: "namespaces", Kind: "Namespace",
		State: bundle.StateComplete, Reason: "namespaces in scope only, read every 60s"}
	seen := map[string]string{}
	poll := func() {
		found := map[string]bool{}
		upsert := func(obj map[string]any) {
			_, name := objNsName(obj)
			found[name] = true
			rv := fmt.Sprint(asMap(obj["metadata"])["resourceVersion"])
			if seen[name] == rv {
				return
			}
			seen[name] = rv
			l.WS.UpsertObject(l.cluster, r, l.C.transform(loc, r, obj))
			l.touch()
		}
		failed := ""
		if names := l.C.Policy.ExplicitNamespaces(); len(names) > 0 && !hasGlob(names) {
			for _, n := range names {
				if !l.C.Policy.NamespaceAllowed(n) {
					continue
				}
				u, err := l.CL.Dynamic.Resource(gvr).Get(ctx, n, metav1.GetOptions{})
				if err != nil {
					if !apierrors.IsNotFound(err) {
						failed = reason(err)
						found[n] = true // keep what we have
					}
					continue
				}
				upsert(u.Object)
			}
		} else {
			ul, err := l.CL.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{})
			if err != nil {
				failed = reason(err)
				return
			}
			for i := range ul.Items {
				if l.C.Policy.NamespaceAllowed(ul.Items[i].GetName()) {
					upsert(ul.Items[i].Object)
				}
			}
		}
		for name := range seen {
			if !found[name] {
				delete(seen, name)
				l.WS.DeleteObject(l.cluster, "", "namespaces", "", name)
				l.touch()
			}
		}
		c := cov
		if failed != "" {
			c.State, c.Reason = bundle.StateFailed, failed
		}
		c.Items = len(seen)
		if old, ok := l.WS.CoverageRecord(c.ID); !ok || old.State != c.State || old.Items != c.Items || old.Reason != c.Reason {
			l.WS.PutCoverage(c)
		}
	}
	poll()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			poll()
		}
	}
}

func objNsName(obj map[string]any) (string, string) {
	md := asMap(obj["metadata"])
	ns, _ := md["namespace"].(string)
	name, _ := md["name"].(string)
	return ns, name
}

// feed is one list-then-watch loop: one resource type in one namespace scope.
type feed struct {
	r        bundle.APIResource
	ns       string
	label    string // label selector (namespaced objects only)
	metadata bool   // read through the metadata client
	name     string // for coverage and logs

	initial             func(items []map[string]any) // the first successful list
	relist              func(items []map[string]any) // a later full list after 410 Gone: reconcile
	upsert              func(obj map[string]any)     // ADDED or MODIFIED
	remove              func(obj map[string]any)     // DELETED
	listed              func(items int, err error)   // after every list, for coverage
	initDone, afterInit func()
}

func (l *Live) objectFeed(r bundle.APIResource, ns string, keep, pods bool) *feed {
	f := &feed{r: r, ns: ns, metadata: l.C.clientFor(r) == "metadata", name: r.Name()}
	if r.Namespaced {
		f.label = l.C.Policy.LabelSelector
	}
	loc := fmt.Sprintf("clusters/%s/resources/%s", l.cluster, r.Name())
	scopeKey := r.Name() + "|" + ns
	transform := func(obj map[string]any) map[string]any { return l.C.transform(loc, r, obj) }
	filter := func(items []map[string]any) []map[string]any {
		out := items[:0]
		for _, it := range items {
			if l.C.keep(r, it) {
				out = append(out, it)
			}
		}
		return out
	}
	f.initial = func(items []map[string]any) {
		items = filter(items)
		l.rememberAll(scopeKey, items)
		if pods {
			for _, it := range items {
				l.logs.podUpdate(it)
			}
		}
		if !keep {
			return
		}
		for i := 0; i < len(items); i += 500 {
			j := min(i+500, len(items))
			chunk := make([]map[string]any, 0, j-i)
			for _, it := range items[i:j] {
				chunk = append(chunk, transform(it))
			}
			l.WS.AddObjects(l.cluster, r, chunk, fmt.Sprintf("live:%s:%s:%d", r.Name(), ns, i/500))
		}
	}
	f.upsert = func(obj map[string]any) {
		if !l.C.keep(r, obj) {
			return
		}
		ons, name := objNsName(obj)
		rv := fmt.Sprint(asMap(obj["metadata"])["resourceVersion"])
		if !l.remember(scopeKey, ons+"/"+name, rv) {
			return // unchanged (a relist replaying what we have)
		}
		if pods {
			l.logs.podUpdate(obj)
		}
		if keep {
			l.WS.UpsertObject(l.cluster, r, transform(obj))
		}
		l.touch()
	}
	f.remove = func(obj map[string]any) {
		ons, name := objNsName(obj)
		l.forget(scopeKey, ons+"/"+name)
		if pods {
			l.logs.podDelete(ons, name)
		}
		if keep {
			l.WS.DeleteObject(l.cluster, r.Group, r.Resource, ons, name)
		}
		l.touch()
	}
	f.relist = func(items []map[string]any) {
		items = filter(items)
		have := map[string]bool{}
		for _, it := range items {
			ons, name := objNsName(it)
			have[ons+"/"+name] = true
			f.upsert(it)
		}
		for _, k := range l.known(scopeKey) {
			if !have[k] {
				ons, name, _ := strings.Cut(k, "/")
				f.remove(map[string]any{"metadata": map[string]any{"namespace": ons, "name": name}})
			}
		}
	}
	if keep {
		id := fmt.Sprintf("%s.resource.%s.%s.%s", l.cluster, groupName(r.Group), r.Version, r.Resource)
		f.listed = func(n int, err error) {
			l.coverage(bundle.Coverage{ID: id, Cluster: l.cluster, Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource,
				Kind: r.Kind, Namespaced: r.Namespaced, State: bundle.StateComplete, Reason: "watched live"}, ns, n, err)
		}
	}
	return f
}

func (l *Live) eventFeed(ns string) *feed {
	r := bundle.APIResource{Version: "v1", Resource: "events", Kind: "Event", Namespaced: true}
	f := &feed{r: r, ns: ns, name: "events"}
	loc := fmt.Sprintf("clusters/%s/events", l.cluster)
	scopeKey := "events|" + ns
	f.initial = func(items []map[string]any) {
		var kept []map[string]any
		for _, it := range items {
			if l.C.keep(r, it) {
				kept = append(kept, l.C.Redactor.Object(loc, it))
			}
		}
		l.rememberAll(scopeKey, kept)
		for _, it := range kept {
			l.WS.UpsertEvent(l.cluster, it)
		}
	}
	f.upsert = func(obj map[string]any) {
		if !l.C.keep(r, obj) {
			return
		}
		ons, name := objNsName(obj)
		if !l.remember(scopeKey, ons+"/"+name, fmt.Sprint(asMap(obj["metadata"])["resourceVersion"])) {
			return
		}
		l.WS.UpsertEvent(l.cluster, l.C.Redactor.Object(loc, obj))
		l.touch()
	}
	f.remove = func(obj map[string]any) {
		ons, name := objNsName(obj)
		l.forget(scopeKey, ons+"/"+name)
		l.WS.DeleteEvent(l.cluster, ons, name)
		l.touch()
	}
	f.relist = func(items []map[string]any) {
		have := map[string]bool{}
		for _, it := range items {
			ons, name := objNsName(it)
			have[ons+"/"+name] = true
			f.upsert(it)
		}
		for _, k := range l.known(scopeKey) {
			if !have[k] {
				ons, name, _ := strings.Cut(k, "/")
				f.remove(map[string]any{"metadata": map[string]any{"namespace": ons, "name": name}})
			}
		}
	}
	f.listed = func(n int, err error) {
		l.coverage(bundle.Coverage{ID: l.cluster + ".events", Cluster: l.cluster, Type: bundle.CoverageEvents, Version: "v1", Resource: "events", Kind: "Event",
			Namespaced: true, State: bundle.StateComplete, Reason: "watched live"}, ns, n, err)
	}
	return f
}

// coverage merges one namespace scope's list result into the type's record.
func (l *Live) coverage(c bundle.Coverage, ns string, n int, err error) {
	l.covMu.Lock()
	key := c.ID + "|" + ns
	part := c
	part.Items = n
	if err != nil {
		part.State, part.Reason = bundle.StateFailed, reason(err)
	}
	l.covSet[key] = part
	merged := c
	merged.Items = 0
	for k, v := range l.covSet {
		if !strings.HasPrefix(k, c.ID+"|") {
			continue
		}
		merged.Items += v.Items
		if v.State == bundle.StateFailed {
			merged.State, merged.Reason = v.State, v.Reason
		}
	}
	l.covMu.Unlock()
	if old, ok := l.WS.CoverageRecord(merged.ID); ok && old.State == merged.State && old.Reason == merged.Reason && old.Items == merged.Items {
		return
	}
	l.WS.PutCoverage(merged)
}

func (l *Live) remember(scope, key, rv string) bool {
	l.rvMu.Lock()
	defer l.rvMu.Unlock()
	m := l.rvs[scope]
	if m == nil {
		m = map[string]string{}
		l.rvs[scope] = m
	}
	if rv != "" && rv != "<nil>" && m[key] == rv {
		return false
	}
	m[key] = rv
	return true
}

func (l *Live) rememberAll(scope string, items []map[string]any) {
	for _, it := range items {
		ns, name := objNsName(it)
		l.remember(scope, ns+"/"+name, fmt.Sprint(asMap(it["metadata"])["resourceVersion"]))
	}
}

func (l *Live) forget(scope, key string) {
	l.rvMu.Lock()
	defer l.rvMu.Unlock()
	delete(l.rvs[scope], key)
}

func (l *Live) known(scope string) []string {
	l.rvMu.Lock()
	defer l.rvMu.Unlock()
	out := make([]string, 0, len(l.rvs[scope]))
	for k := range l.rvs[scope] {
		out = append(out, k)
	}
	return out
}

// listAll reads every page of one type in one scope and returns the items
// and the list's resourceVersion, the point the watch resumes from.
func (l *Live) listAll(ctx context.Context, f *feed) ([]map[string]any, string, error) {
	gvr := schema.GroupVersionResource{Group: f.r.Group, Version: f.r.Version, Resource: f.r.Resource}
	opts := metav1.ListOptions{Limit: 500, LabelSelector: f.label}
	var out []map[string]any
	for {
		var cont, rv string
		if f.metadata {
			var res = l.CL.Metadata.Resource(gvr)
			var ml *metav1.PartialObjectMetadataList
			var err error
			if f.ns != "" {
				ml, err = res.Namespace(f.ns).List(ctx, opts)
			} else {
				ml, err = res.List(ctx, opts)
			}
			if err != nil {
				return nil, "", err
			}
			for i := range ml.Items {
				m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&ml.Items[i])
				if err != nil {
					return nil, "", err
				}
				m["apiVersion"], m["kind"] = f.r.GroupVersion(), f.r.Kind
				out = append(out, m)
			}
			cont, rv = ml.Continue, ml.ResourceVersion
		} else {
			res := l.CL.Dynamic.Resource(gvr)
			var ul *unstructured.UnstructuredList
			var err error
			if f.ns != "" {
				ul, err = res.Namespace(f.ns).List(ctx, opts)
			} else {
				ul, err = res.List(ctx, opts)
			}
			if err != nil {
				return nil, "", err
			}
			for i := range ul.Items {
				out = append(out, ul.Items[i].Object)
			}
			cont, rv = ul.GetContinue(), ul.GetResourceVersion()
		}
		if cont == "" {
			return out, rv, nil
		}
		opts.Continue = cont
	}
}

func (l *Live) openWatch(ctx context.Context, f *feed, rv string) (watch.Interface, error) {
	gvr := schema.GroupVersionResource{Group: f.r.Group, Version: f.r.Version, Resource: f.r.Resource}
	timeout := int64(300 + rand.Intn(300)) // spread reconnects, as reflectors do
	opts := metav1.ListOptions{ResourceVersion: rv, AllowWatchBookmarks: true, LabelSelector: f.label, TimeoutSeconds: &timeout}
	if f.metadata {
		res := l.CL.Metadata.Resource(gvr)
		if f.ns != "" {
			return res.Namespace(f.ns).Watch(ctx, opts)
		}
		return res.Watch(ctx, opts)
	}
	res := l.CL.Dynamic.Resource(gvr)
	if f.ns != "" {
		return res.Namespace(f.ns).Watch(ctx, opts)
	}
	return res.Watch(ctx, opts)
}

func (l *Live) toMap(f *feed, o runtime.Object) (map[string]any, bool) {
	switch t := o.(type) {
	case *unstructured.Unstructured:
		return t.Object, true
	case *metav1.PartialObjectMetadata:
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(t)
		if err != nil {
			return nil, false
		}
		m["apiVersion"], m["kind"] = f.r.GroupVersion(), f.r.Kind
		return m, true
	}
	return nil, false
}

func gone(err error) bool {
	return apierrors.IsResourceExpired(err) || apierrors.IsGone(err)
}

// runFeed lists, then watches from the list's resourceVersion until ctx
// ends. A dropped watch resumes from the last version seen; an expired one
// (410 Gone) relists and reconciles, so nothing is missed or left behind.
func (l *Live) runFeed(ctx context.Context, f *feed) {
	backoff := time.Second
	var rv string
	first := true
	failed := false
	fail := func(what string, err error) {
		if !failed {
			failed = true
			l.failing.Add(1)
		}
		l.reconnects.Add(1)
		l.C.record(LogEntry{Cluster: l.cluster, Operation: what, Target: f.name, Namespace: f.ns, Result: "failed", Error: err.Error()})
	}
	ok := func() {
		if failed {
			failed = false
			l.failing.Add(-1)
		}
	}
	defer func() {
		if failed {
			l.failing.Add(-1)
		}
	}()
	sleep := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		return true
	}
	for ctx.Err() == nil {
		if rv == "" {
			items, v, err := l.listAll(ctx, f)
			if f.listed != nil {
				f.listed(len(items), err)
			}
			if err != nil {
				if first {
					first = false
					f.initDone()
					f.afterInit()
				}
				if ctx.Err() != nil {
					return
				}
				if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) || apierrors.IsMethodNotSupported(err) {
					// Not a connection problem: try again rarely, and do not
					// mark the mirror as disconnected for it.
					l.C.record(LogEntry{Cluster: l.cluster, Operation: "list", Target: f.name, Namespace: f.ns, Result: "failed", Error: err.Error()})
					select {
					case <-ctx.Done():
						return
					case <-time.After(5 * time.Minute):
					}
					continue
				}
				fail("list", err)
				if !sleep() {
					return
				}
				continue
			}
			if first {
				f.initial(items)
				first = false
				f.initDone()
				f.afterInit()
			} else {
				f.relist(items)
			}
			rv = v
			ok()
			backoff = time.Second
		}
		w, err := l.openWatch(ctx, f, rv)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if gone(err) {
				rv = ""
				continue
			}
			fail("watch", err)
			if !sleep() {
				return
			}
			continue
		}
		ok()
		l.watches.Add(1)
		opened := time.Now()
		rv = l.consume(ctx, f, w, rv)
		l.watches.Add(-1)
		w.Stop()
		// A watch that ends at once (an error event, a proxy closing it) must
		// not turn into a tight reconnect loop.
		if time.Since(opened) < time.Second {
			if !sleep() {
				return
			}
		} else {
			backoff = time.Second
		}
	}
}

// consume applies watch events until the watch ends, returning the version
// to resume from ("" to relist).
func (l *Live) consume(ctx context.Context, f *feed, w watch.Interface, rv string) string {
	for {
		select {
		case <-ctx.Done():
			return rv
		case ev, open := <-w.ResultChan():
			if !open {
				return rv
			}
			switch ev.Type {
			case watch.Error:
				err := apierrors.FromObject(ev.Object)
				if gone(err) {
					return ""
				}
				l.reconnects.Add(1)
				l.C.record(LogEntry{Cluster: l.cluster, Operation: "watch", Target: f.name, Namespace: f.ns, Result: "failed", Error: err.Error()})
				return rv
			case watch.Bookmark:
				if m, ok := l.toMap(f, ev.Object); ok {
					if v := fmt.Sprint(asMap(m["metadata"])["resourceVersion"]); v != "" {
						rv = v
					}
				}
			case watch.Added, watch.Modified, watch.Deleted:
				m, ok := l.toMap(f, ev.Object)
				if !ok {
					continue
				}
				if v, _ := asMap(m["metadata"])["resourceVersion"].(string); v != "" {
					rv = v
				}
				if ev.Type == watch.Deleted {
					f.remove(m)
				} else {
					f.upsert(m)
				}
			}
		}
	}
}

// ---------- logs ----------

type streamRef struct{ ns, pod, container string }

func (s streamRef) String() string { return s.ns + "/" + s.pod + "/" + s.container }

type livePod struct {
	ns, name   string
	running    bool
	ready      bool
	restarts   map[string]int64
	containers []string
	start      string
}

// ring keeps the newest lines of one stream between flushes.
type ring struct {
	mu      sync.Mutex
	lines   []string
	max     int
	dropped int64
}

func (r *ring) push(s string) {
	r.mu.Lock()
	if len(r.lines) >= r.max {
		copy(r.lines, r.lines[1:])
		r.lines = r.lines[:len(r.lines)-1]
		r.dropped++
	}
	r.lines = append(r.lines, s)
	r.mu.Unlock()
}

func (r *ring) drain() ([]string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, d := r.lines, r.dropped
	r.lines, r.dropped = nil, 0
	return out, d
}

type follower struct {
	ref     streamRef
	cancel  context.CancelFunc
	buf     *ring
	done    chan struct{}
	dropped int64
	lines   int64
}

type logManager struct {
	l                    *Live
	mu                   sync.Mutex
	pods                 map[string]*livePod // ns/name
	followers            map[streamRef]*follower
	lastTS               map[streamRef]time.Time // survives rotation, so a resumed stream does not repeat
	prevAt               map[streamRef]int64     // restart count whose previous container was fetched
	covNote              map[string]string       // coverage id -> last reason written
	candidates, followed int
	dropped              int64
	rotation             int
	lastRot              time.Time
	kick                 chan struct{}
	prevSem              chan struct{}
	ctx                  context.Context
}

func newLogManager(l *Live) *logManager {
	return &logManager{l: l, pods: map[string]*livePod{}, followers: map[streamRef]*follower{}, lastTS: map[streamRef]time.Time{},
		prevAt: map[streamRef]int64{}, covNote: map[string]string{}, kick: make(chan struct{}, 1), prevSem: make(chan struct{}, 4)}
}

func (m *logManager) counts() (int, int, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.followed, m.candidates, m.dropped
}

func parseLivePod(obj map[string]any) *livePod {
	pi := parsePod(obj)
	p := &livePod{ns: pi.ns, name: pi.name, running: pi.running, restarts: map[string]int64{}}
	status := asMap(obj["status"])
	p.start, _ = status["startTime"].(string)
	ready := true
	sts, _ := status["containerStatuses"].([]any)
	for _, s := range sts {
		sm := asMap(s)
		if r, _ := sm["ready"].(bool); !r {
			ready = false
		}
	}
	for _, cond := range asSlice(status["conditions"]) {
		cm := asMap(cond)
		if cm["type"] == "Ready" && cm["status"] != "True" {
			ready = false
		}
	}
	p.ready = ready && len(sts) > 0
	for _, c := range pi.containers {
		if c.init || strings.HasPrefix(c.name, "ephemeral:") {
			continue
		}
		p.containers = append(p.containers, c.name)
		p.restarts[c.name] = c.restarts
	}
	return p
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func (m *logManager) podUpdate(obj map[string]any) {
	p := parseLivePod(obj)
	if p.name == "" {
		return
	}
	pol := m.l.C.Policy
	m.mu.Lock()
	old := m.pods[p.ns+"/"+p.name]
	m.pods[p.ns+"/"+p.name] = p
	var prevs []streamRef
	if pol.Plan().Selected(policy.CatLogs) && pol.Stream("previous") && globAny(pol.Logs.Pods, p.name) {
		for _, c := range p.containers {
			ref := streamRef{p.ns, p.name, c}
			n := p.restarts[c]
			was := int64(0)
			if old != nil {
				was = old.restarts[c]
			}
			// A restart since we last looked, or restarts that happened before
			// we started: fetch the previous container once, as collect does.
			if n > 0 && (n > was || old == nil) && m.prevAt[ref] < n && globAny(pol.Logs.Containers, c) {
				m.prevAt[ref] = n
				prevs = append(prevs, ref)
			}
		}
	}
	ctx := m.ctx
	m.mu.Unlock()
	for _, ref := range prevs {
		go m.fetchPrevious(ctx, ref)
	}
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *logManager) podDelete(ns, name string) {
	m.mu.Lock()
	delete(m.pods, ns+"/"+name)
	m.mu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *logManager) run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	// Restarts seen during the initial list are fetched now that we have a context.
	var prevs []streamRef
	for ref := range m.prevAt {
		prevs = append(prevs, ref)
	}
	m.mu.Unlock()
	for _, ref := range prevs {
		go m.fetchPrevious(ctx, ref)
	}
	flush := time.NewTicker(m.l.O.FlushEvery)
	defer flush.Stop()
	resched := time.NewTicker(2 * time.Second)
	defer resched.Stop()
	m.reconcile(ctx)
	pending := false
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, f := range m.followers {
				f.cancel()
			}
			m.mu.Unlock()
			return
		case <-m.kick:
			pending = true
		case <-resched.C:
			if pending || time.Since(m.lastRot) >= m.l.O.Rotate {
				pending = false
				m.reconcile(ctx)
			}
		case <-flush.C:
			m.flush()
		}
	}
}

// reconcile decides which streams to follow. Under the cap every running
// container is followed. Over it, pods that are not Ready or have restarted
// come first; the rest take turns, rotating every Rotate interval.
func (m *logManager) reconcile(ctx context.Context) {
	pol := m.l.C.Policy
	m.mu.Lock()
	var urgent, rest []streamRef
	for _, p := range m.pods {
		if !p.running || !globAny(pol.Logs.Pods, p.name) || !pol.Stream("current") {
			continue
		}
		restarted := false
		for _, n := range p.restarts {
			restarted = restarted || n > 0
		}
		for _, c := range p.containers {
			if !globAny(pol.Logs.Containers, c) {
				continue
			}
			ref := streamRef{p.ns, p.name, c}
			if !p.ready || restarted {
				urgent = append(urgent, ref)
			} else {
				rest = append(rest, ref)
			}
		}
	}
	byName := func(s []streamRef) {
		sort.Slice(s, func(i, j int) bool { return s[i].String() < s[j].String() })
	}
	byName(urgent)
	byName(rest)
	max := m.l.O.MaxLogStreams
	want := map[streamRef]bool{}
	var notFollowed []streamRef
	for _, r := range urgent {
		if len(want) < max {
			want[r] = true
		} else {
			notFollowed = append(notFollowed, r)
		}
	}
	free := max - len(want)
	if free > 0 && len(rest) > free {
		if time.Since(m.lastRot) >= m.l.O.Rotate {
			if !m.lastRot.IsZero() {
				m.rotation++
			}
			m.lastRot = time.Now()
		}
		off := (m.rotation * free) % len(rest)
		rest = append(rest[off:], rest[:off]...)
	} else if m.lastRot.IsZero() {
		m.lastRot = time.Now()
	}
	for _, r := range rest {
		if len(want) < max {
			want[r] = true
		} else {
			notFollowed = append(notFollowed, r)
		}
	}
	var start []*follower
	var stopped []streamRef
	for ref, f := range m.followers {
		if !want[ref] {
			f.cancel()
			delete(m.followers, ref)
			stopped = append(stopped, ref)
		}
	}
	for ref := range want {
		if _, ok := m.followers[ref]; !ok {
			fctx, cancel := context.WithCancel(ctx)
			f := &follower{ref: ref, cancel: cancel, buf: &ring{max: m.l.O.RingLines}, done: make(chan struct{})}
			m.followers[ref] = f
			start = append(start, f)
			go func() { m.follow(fctx, f); close(f.done) }()
		}
	}
	m.candidates, m.followed = len(urgent)+len(rest), len(m.followers)
	m.mu.Unlock()
	cl := m.l.cluster
	for _, f := range start {
		m.streamCoverage(f.ref, bundle.StateComplete, "following live", -1)
	}
	for _, ref := range stopped {
		m.mu.Lock()
		p := m.pods[ref.ns+"/"+ref.pod]
		m.mu.Unlock()
		switch {
		case p == nil:
			m.streamCoverage(ref, bundle.StateComplete, "pod gone; the lines already read are kept", -1)
		case !p.running:
			m.streamCoverage(ref, bundle.StateComplete, "pod not running; the lines already read are kept", -1)
		default:
			m.streamCoverage(ref, bundle.StateTruncated, fmt.Sprintf("paused: more than %d streams (--max-log-streams); streams take turns every %s", max, m.l.O.Rotate), -1)
		}
	}
	for _, ref := range notFollowed {
		m.streamCoverage(ref, bundle.StateTruncated, fmt.Sprintf("not followed: more than %d streams (--max-log-streams). Pods that are not Ready or have restarted come first; the rest take turns every %s", max, m.l.O.Rotate), -1)
	}
	_ = cl
}

// streamCoverage writes a log coverage record when its state or reason
// changes (items < 0 keeps the current count).
func (m *logManager) streamCoverage(ref streamRef, state, why string, items int) {
	m.coverageFor(ref, "current", state, why, items)
}

func (m *logManager) coverageFor(ref streamRef, stream, state, why string, items int) {
	cl := m.l.cluster
	id := bundle.SafeID(fmt.Sprintf("%s.logs.%s.%s.%s.%s", cl, ref.ns, ref.pod, ref.container, stream))
	m.mu.Lock()
	note := state + "|" + why
	if items >= 0 {
		note += fmt.Sprint("|", items)
	}
	if m.covNote[id] == note {
		m.mu.Unlock()
		return
	}
	m.covNote[id] = note
	m.mu.Unlock()
	c, ok := m.l.WS.CoverageRecord(id)
	if !ok {
		c = bundle.Coverage{ID: id, Cluster: cl, Type: bundle.CoverageLogs, Namespace: ref.ns, Pod: ref.pod, Container: ref.container, Stream: stream}
	}
	c.State, c.Reason = state, why
	if items >= 0 {
		c.Items = items
	}
	m.l.WS.PutCoverage(c)
}

// follow streams one container like kubectl logs -f, reconnecting from the
// newest line seen, until its context is cancelled.
func (m *logManager) follow(ctx context.Context, f *follower) {
	pol := m.l.C.Policy
	lr := m.l.C.Redactor.Lines(fmt.Sprintf("clusters/%s/logs/%s/%s/%s", m.l.cluster, f.ref.ns, f.ref.pod, f.ref.container))
	backoff := time.Second
	for ctx.Err() == nil {
		m.mu.Lock()
		last := m.lastTS[f.ref]
		m.mu.Unlock()
		opts := &corev1.PodLogOptions{Container: f.ref.container, Follow: true, Timestamps: true}
		if last.IsZero() {
			if since := pol.SinceDuration(); since > 0 {
				secs := int64(since.Seconds())
				opts.SinceSeconds = &secs
			}
			if m.l.O.LogTail > 0 {
				t := m.l.O.LogTail
				opts.TailLines = &t
			}
		} else {
			st := metav1.NewTime(last)
			opts.SinceTime = &st
		}
		got, err := m.readFollow(ctx, f, opts, last, lr)
		if ctx.Err() != nil {
			return
		}
		if got > 0 {
			backoff = time.Second
		}
		if err != nil {
			m.streamCoverage(f.ref, bundle.StateFailed, "retrying: "+err.Error(), -1)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if got == 0 {
			backoff = min(backoff*2, 30*time.Second)
		}
	}
}

func (m *logManager) readFollow(ctx context.Context, f *follower, opts *corev1.PodLogOptions, last time.Time, lr *redact.LineRedactor) (int, error) {
	rc, err := m.l.CL.Logs.Stream(ctx, f.ref.ns, f.ref.pod, opts)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 256<<10)
	got := 0
	first := true
	for {
		line, rerr := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			if first {
				first = false
				for _, pre := range kubeletErrorPrefixes {
					if strings.HasPrefix(line, pre) {
						return 0, errors.New("kubelet: " + line)
					}
				}
			}
			ts, _ := logs.SplitTimestamp(line)
			if !last.IsZero() && !ts.IsZero() && !ts.After(last) {
				continue // already read before the reconnect
			}
			if len(line) > 64<<10 {
				line = line[:64<<10] + " [flightbay: line truncated]"
			}
			f.buf.push(lr.Line(line))
			got++
			if !ts.IsZero() {
				m.mu.Lock()
				m.lastTS[f.ref] = ts
				m.mu.Unlock()
				last = ts
			}
		}
		if rerr != nil {
			if ctx.Err() != nil || errors.Is(rerr, io.EOF) {
				return got, nil
			}
			return got, rerr
		}
	}
}

// flush stores what every stream buffered since the last flush, then keeps
// the workspace under the total line cap.
func (m *logManager) flush() {
	m.mu.Lock()
	fs := make([]*follower, 0, len(m.followers))
	for _, f := range m.followers {
		fs = append(fs, f)
	}
	m.mu.Unlock()
	for _, f := range fs {
		lines, dropped := f.buf.drain()
		if len(lines) > 0 {
			m.l.WS.QueueLogLines(m.l.cluster, f.ref.ns, f.ref.pod, f.ref.container, "current", lines, "live", nil)
			f.lines += int64(len(lines))
		}
		if dropped > 0 {
			f.dropped += dropped
			m.mu.Lock()
			m.dropped += dropped
			m.mu.Unlock()
			m.streamCoverage(f.ref, bundle.StateTruncated, fmt.Sprintf("following live; %d lines dropped because they arrived faster than they could be stored", f.dropped), -1)
		}
	}
	max := m.l.O.MaxLogLines
	if n := m.l.WS.State().LogLines; n > max+max/10 {
		if d := m.l.WS.TrimLogs(max); d > 0 {
			m.mu.Lock()
			m.dropped += int64(d)
			total := m.dropped
			m.mu.Unlock()
			m.l.WS.PutCoverage(bundle.Coverage{ID: m.l.cluster + ".logs.retention", Cluster: m.l.cluster, Type: bundle.CoverageLogs, State: bundle.StateTruncated,
				Reason: fmt.Sprintf("the oldest lines are dropped to keep at most %d (--max-log-lines); %d dropped so far", max, total)})
		}
	}
}

// fetchPrevious reads a restarted container's previous log once.
func (m *logManager) fetchPrevious(ctx context.Context, ref streamRef) {
	if ctx == nil {
		return // picked up by run once logs start
	}
	select {
	case m.prevSem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-m.prevSem }()
	p := podInfo{ns: ref.ns, name: ref.pod}
	opts := &corev1.PodLogOptions{Container: ref.container, Previous: true, Timestamps: true}
	if mb := m.l.C.Policy.Logs.MaxBytes; mb > 0 {
		opts.LimitBytes = &mb
	}
	var lines []string
	var kubeletErr string
	var err error
	for attempt := 0; attempt <= m.l.C.LogRetries; attempt++ {
		lines, _, kubeletErr, err = m.l.C.readStream(ctx, m.l.cluster, m.l.CL, p, ref.container, opts)
		if err == nil && kubeletErr == "" {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.l.C.LogRetryDelay):
		}
	}
	if err != nil || kubeletErr != "" {
		msg := kubeletErr
		if err != nil {
			msg = err.Error()
		}
		m.coverageFor(ref, "previous", bundle.StateFailed, "previous container log: "+msg, -1)
		return
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\n")
	}
	m.l.WS.QueueLogLines(m.l.cluster, ref.ns, ref.pod, ref.container, "previous", lines, "live-previous", nil)
	m.l.touch()
	m.coverageFor(ref, "previous", bundle.StateComplete, "read after a restart; each restart adds that container's log", len(lines))
}

// ---------- diagnostics on demand ----------

var errNoDiagnostics = errors.New("diagnostics are off: start flightbay-desk live with --solo-diagnostics")

// RefreshDiagnostics runs the collector's diagnostics for one pod now, over
// port-forward, and replaces that pod's diagnostic files in the workspace.
func (l *Live) RefreshDiagnostics(ctx context.Context, ns, pod string) (int, error) {
	if !l.DiagnosticsEnabled() {
		return 0, errNoDiagnostics
	}
	if !l.C.Policy.NamespaceAllowed(ns) {
		return 0, fmt.Errorf("namespace %s is outside the live scope", ns)
	}
	o := l.WS.Get(l.cluster, "", "pods", ns, pod)
	if o == nil {
		return 0, fmt.Errorf("pod %s/%s is not in the live mirror", ns, pod)
	}
	p := parsePod(o.Obj)
	if len(detect(p)) == 0 {
		return 0, fmt.Errorf("%s/%s is not a Solo component (ztunnel, Envoy, agentgateway, istiod or a gateway controller)", ns, pod)
	}
	return l.diagnose(ctx, []podInfo{p}, false)
}

func (l *Live) refreshAllDiagnostics(ctx context.Context) error {
	var pods []podInfo
	for _, o := range l.WS.List(l.cluster, "", "pods", "", nil) {
		p := parsePod(o.Obj)
		if len(detect(p)) > 0 {
			pods = append(pods, p)
		}
	}
	if len(pods) == 0 {
		return nil
	}
	_, err := l.diagnose(ctx, pods, true)
	return err
}

// diagnose runs collectDiagnostics into an in-memory bundle and moves the
// result into the workspace, so the endpoint list, filtering and redaction
// are exactly the collector's.
func (l *Live) diagnose(ctx context.Context, pods []podInfo, summary bool) (int, error) {
	l.diagMu.Lock()
	defer l.diagMu.Unlock()
	var buf bytes.Buffer
	w, err := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest"})
	if err != nil {
		return 0, err
	}
	if err := w.WriteHeader(bundle.Header{Contexts: []string{l.O.Context}}); err != nil {
		return 0, err
	}
	c := &Collector{Policy: l.C.Policy, Redactor: l.C.Redactor, Writer: w, Version: l.C.Version, MaxDiagBytes: l.C.MaxDiagBytes}
	c.defaults()
	if err := c.collectDiagnostics(ctx, l.cluster, l.CL, pods, l.kinds); err != nil {
		return 0, err
	}
	if err := w.Close(bundle.Manifest{}); err != nil {
		return 0, err
	}
	r, err := bundle.NewReader(&buf, bundle.ReaderOptions{})
	if err != nil {
		return 0, err
	}
	for _, p := range pods {
		for _, kind := range []string{"proxies", "control-plane"} {
			l.WS.RemoveDiagnostic(l.cluster, kind, p.ns, p.name)
		}
	}
	files := 0
	for {
		m, err := r.Next()
		if err != nil {
			break
		}
		info, ok := bundle.ParsePath(m.Name)
		if !ok {
			continue
		}
		switch info.Kind {
		case bundle.PathSolo:
			if info.Sub == "" {
				if summary && info.File == "summary.json" {
					l.WS.SetSoloSummary(l.cluster, json.RawMessage(m.Data))
				}
				continue
			}
			l.WS.AddDiagnostic(l.cluster, info.Sub, info.Namespace, info.Pod, info.File, m.Data)
			if info.File != "meta.json" {
				files++
			}
		case bundle.PathCoverage:
			var cv bundle.Coverage
			if json.Unmarshal(m.Data, &cv) == nil {
				l.WS.PutCoverage(cv)
			}
		}
	}
	c.mu.Lock()
	entries := c.entries
	c.mu.Unlock()
	if len(entries) > 0 {
		var lines []json.RawMessage
		for _, e := range entries {
			b, _ := json.Marshal(e)
			lines = append(lines, b)
		}
		l.WS.AddCollectionLog(lines)
	}
	l.touch()
	return files, nil
}
