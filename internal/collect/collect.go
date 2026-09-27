// Package collect reads a Kubernetes cluster through the resolved collection
// policy, redacts in memory and streams the result into a bundle.
package collect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
)

// Discovery is the subset of the discovery client the collector uses.
type Discovery interface {
	ServerVersion() (*version.Info, error)
	ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error)
}

// LogSource streams container logs.
type LogSource interface {
	Stream(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
}

// Clients are the API clients for one cluster.
type Clients struct {
	Discovery Discovery
	Dynamic   dynamic.Interface
	Metadata  metadata.Interface
	Logs      LogSource
}

// LogEntry is one line of collection-log.jsonl.
type LogEntry struct {
	Time      time.Time `json:"time"`
	Cluster   string    `json:"cluster"`
	Operation string    `json:"operation"`
	Target    string    `json:"target,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	Result    string    `json:"result"`
	Error     string    `json:"error,omitempty"`
}

// Collector writes one bundle, possibly covering several clusters.
type Collector struct {
	Policy        *policy.Policy
	Redactor      *redact.Redactor
	Writer        *bundle.Writer
	Version       string
	ChunkItems    int           // objects per resource chunk
	ChunkBytes    int           // approximate bytes per chunk
	LogChunkBytes int           // bytes per log chunk
	LogRetries    int           // retries when the kubelet cannot return a stream (container restarting)
	LogRetryDelay time.Duration // wait between those retries
	Progress      func(string)

	mu         sync.Mutex
	entries    []LogEntry
	budgetHit  bool
	incomplete bool
}

func (c *Collector) defaults() {
	if c.ChunkItems <= 0 {
		c.ChunkItems = 500
	}
	if c.ChunkBytes <= 0 {
		c.ChunkBytes = 4 << 20
	}
	if c.LogChunkBytes <= 0 {
		c.LogChunkBytes = 1 << 20
	}
	if c.LogRetries == 0 {
		c.LogRetries = 3
	}
	if c.LogRetryDelay == 0 {
		c.LogRetryDelay = 1500 * time.Millisecond
	}
}

func (c *Collector) record(e LogEntry) {
	e.Time = time.Now().UTC()
	c.mu.Lock()
	c.entries = append(c.entries, e)
	c.mu.Unlock()
}

func (c *Collector) progress(format string, a ...any) {
	if c.Progress != nil {
		c.Progress(fmt.Sprintf(format, a...))
	}
}

// Begin writes bundle.json with the resolved policy and planned coverage.
func (c *Collector) Begin(contexts []string) error {
	c.defaults()
	pol, err := json.Marshal(c.Policy)
	if err != nil {
		return err
	}
	plan := c.Policy.Plan()
	var planned []string
	for _, cat := range plan.Categories {
		planned = append(planned, string(cat))
	}
	return c.Writer.WriteHeader(bundle.Header{
		Case: c.Policy.Case, Collector: "flightbay " + c.Version, CaptureStart: time.Now().UTC(),
		Contexts: contexts, Policy: pol, Planned: planned, Unavailable: plan.Unavailable,
	})
}

// Finish writes the reports and the manifest.
func (c *Collector) Finish() error {
	c.mu.Lock()
	entries := append([]LogEntry{}, c.entries...)
	complete := !c.budgetHit && !c.incomplete
	c.mu.Unlock()
	var sb strings.Builder
	for _, e := range entries {
		b, _ := json.Marshal(e)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := c.Writer.Add(bundle.CollectionLog, []byte(sb.String())); err != nil {
		return err
	}
	rep, _ := json.MarshalIndent(c.Redactor.Report(), "", "  ")
	if err := c.Writer.Add(bundle.RedactionName, rep); err != nil {
		return err
	}
	return c.Writer.Close(bundle.Manifest{Complete: complete})
}

// MarkIncomplete records that a cluster could not be collected at all.
func (c *Collector) MarkIncomplete(cluster string, err error) {
	c.mu.Lock()
	c.incomplete = true
	c.mu.Unlock()
	c.record(LogEntry{Cluster: cluster, Operation: "connect", Result: "failed", Error: err.Error()})
}

func (c *Collector) overBudget() bool {
	if c.Policy.MaxBundleBytes <= 0 || c.Writer == nil {
		return false
	}
	if c.Writer.CompressedBytes() >= c.Policy.MaxBundleBytes {
		c.mu.Lock()
		c.budgetHit = true
		c.mu.Unlock()
		return true
	}
	return false
}

var priority = map[string]int{
	"pods": 0, "services": 1, "deployments.apps": 2, "replicasets.apps": 3, "statefulsets.apps": 4, "daemonsets.apps": 5,
	"gateways.gateway.networking.k8s.io": 6, "httproutes.gateway.networking.k8s.io": 7, "grpcroutes.gateway.networking.k8s.io": 8,
	"endpointslices.discovery.k8s.io": 9, "configmaps": 10, "secrets": 11,
}

func rank(r bundle.APIResource) int {
	if p, ok := priority[r.Name()]; ok {
		return p
	}
	return 100
}

// discover returns every listable, policy-allowed resource at its preferred version.
func (c *Collector) discover(cluster string, cl Clients) ([]bundle.APIResource, error) {
	groups, lists, err := cl.Discovery.ServerGroupsAndResources()
	if err != nil {
		c.record(LogEntry{Cluster: cluster, Operation: "discovery", Result: "partial", Error: err.Error()})
		if len(lists) == 0 {
			return nil, err
		}
	}
	preferred := map[string]string{}
	for _, g := range groups {
		if g != nil && g.PreferredVersion.GroupVersion != "" {
			preferred[g.Name] = g.PreferredVersion.GroupVersion
		}
	}
	seen := map[string]bool{}
	var out []bundle.APIResource
	for _, l := range lists {
		if l == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		if p, ok := preferred[gv.Group]; ok && p != l.GroupVersion {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") || !hasVerb(r.Verbs, "list") {
				continue
			}
			key := r.Name + "." + gv.Group
			if seen[key] {
				continue
			}
			if r.Name == "events" && (gv.Group == "" || gv.Group == "events.k8s.io") {
				continue // collected by the events category
			}
			ar := bundle.APIResource{Group: gv.Group, Version: gv.Version, Resource: r.Name, Kind: r.Kind, Namespaced: r.Namespaced,
				SingularName: r.SingularName, ShortNames: r.ShortNames, Categories: r.Categories}
			seen[key] = true
			out = append(out, ar)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return out[i].Name() < out[j].Name()
	})
	return out, nil
}

func hasVerb(verbs []string, v string) bool {
	for _, x := range verbs {
		if x == v {
			return true
		}
	}
	return false
}

func (c *Collector) objectAllowed(r bundle.APIResource) bool {
	pl := c.Policy.Plan()
	if r.Group == "" && r.Resource == "nodes" && pl.Selected(policy.CatNodes) {
		return true
	}
	return pl.Selected(policy.CatObjects) && c.Policy.ResourceAllowed(r.Resource, r.Group, r.Kind, r.Namespaced)
}

// DryRunPlan is what --dry-run prints. It is built from discovery only.
type DryRunPlan struct {
	Cluster     string   `json:"cluster"`
	Resources   []string `json:"resources"`
	Namespaces  []string `json:"namespaces"`
	Categories  []string `json:"categories"`
	Unavailable []string `json:"unavailable,omitempty"`
	LogStreams  []string `json:"logStreams,omitempty"`
	LogSince    string   `json:"logSince,omitempty"`
	LogMaxBytes int64    `json:"logMaxBytesPerStream,omitempty"`
	RBAC        string   `json:"rbac"`
}

// DryRun resolves the selection against discovery without reading any payloads.
func (c *Collector) DryRun(_ context.Context, cluster string, cl Clients) (*DryRunPlan, error) {
	all, err := c.discover(cluster, cl)
	if err != nil {
		return nil, err
	}
	plan := c.Policy.Plan()
	out := &DryRunPlan{Cluster: cluster, Unavailable: plan.Unavailable, Namespaces: c.Policy.Namespaces}
	if len(out.Namespaces) == 0 {
		out.Namespaces = []string{"all namespaces"}
	}
	var disc []policy.APIResource
	for _, r := range all {
		disc = append(disc, policy.APIResource{Group: r.Group, Version: r.Version, Resource: r.Resource, Kind: r.Kind, Namespaced: r.Namespaced})
		if c.objectAllowed(r) {
			out.Resources = append(out.Resources, r.Name())
		}
	}
	for _, cat := range plan.Categories {
		out.Categories = append(out.Categories, string(cat))
	}
	if plan.Selected(policy.CatLogs) {
		out.LogStreams, out.LogSince, out.LogMaxBytes = c.Policy.Logs.Streams, c.Policy.Logs.Since, c.Policy.Logs.MaxBytes
	}
	out.RBAC = c.Policy.RBAC(disc)
	return out, nil
}

// podInfo is what log collection needs from a pod.
type podInfo struct {
	ns, name   string
	containers []containerInfo
}

type containerInfo struct {
	name     string
	init     bool
	restarts int64
}

// CollectCluster collects one cluster into the bundle.
func (c *Collector) CollectCluster(ctx context.Context, cluster string, cl Clients) error {
	c.defaults()
	cluster = bundle.SafeID(cluster)
	plan := c.Policy.Plan()
	c.progress("%s: discovery", cluster)
	if v, err := cl.Discovery.ServerVersion(); err == nil {
		_ = c.Writer.AddJSON(bundle.DiscoveryPath(cluster, "version.json"), v)
	} else {
		c.record(LogEntry{Cluster: cluster, Operation: "version", Result: "failed", Error: err.Error()})
	}
	all, err := c.discover(cluster, cl)
	if err != nil {
		c.MarkIncomplete(cluster, err)
		return nil
	}
	var selected []bundle.APIResource
	for _, r := range all {
		if c.objectAllowed(r) {
			selected = append(selected, r)
		}
	}
	if err := c.Writer.AddJSON(bundle.DiscoveryPath(cluster, "apiresources.json"), selected); err != nil {
		return err
	}
	if err := c.collectCRDs(ctx, cluster, cl, selected); err != nil {
		return err
	}
	var pods []podInfo
	wantPods := plan.Selected(policy.CatLogs)
	collectedPods := false
	early, late := split(selected)
	for _, r := range early {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := c.collectResource(ctx, cluster, cl, r, wantPods && r.Group == "" && r.Resource == "pods")
		if err != nil {
			return err
		}
		if r.Group == "" && r.Resource == "pods" && c.clientFor(r) == "dynamic" {
			pods, collectedPods = p, true
		}
	}
	if plan.Selected(policy.CatEvents) {
		if err := c.collectEvents(ctx, cluster, cl); err != nil {
			return err
		}
	}
	if wantPods {
		if !collectedPods {
			pods = c.listPodsForLogs(ctx, cluster, cl)
		}
		if err := c.collectLogs(ctx, cluster, cl, pods); err != nil {
			return err
		}
	}
	for _, r := range late {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := c.collectResource(ctx, cluster, cl, r, false); err != nil {
			return err
		}
	}
	return c.Writer.Flush()
}

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// collectCRDs fetches the definition of each selected custom resource type, so
// the desk can serve it and print the same columns kubectl would. Only the
// selected types are read, one GET each; the schema is dropped because it is
// large and adds nothing for an investigation.
func (c *Collector) collectCRDs(ctx context.Context, cluster string, cl Clients, selected []bundle.APIResource) error {
	if cl.Dynamic == nil {
		return nil
	}
	denied := false
	for _, r := range selected {
		if !strings.Contains(r.Group, ".") || denied {
			continue
		}
		name := r.Resource + "." + r.Group
		u, err := cl.Dynamic.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue // a built-in or aggregated API, not a CRD
		}
		if err != nil {
			if apierrors.IsForbidden(err) {
				denied = true // one entry, not one per type
			}
			c.record(LogEntry{Cluster: cluster, Operation: "crd", Target: name, Result: "failed", Error: err.Error()})
			continue
		}
		if err := c.Writer.AddJSON(bundle.DiscoveryPath(cluster, "crds/"+bundle.SafeID(name)+".json"), SlimCRD(u.Object)); err != nil {
			return err
		}
	}
	return nil
}

// SlimCRD keeps the parts of a CRD the desk uses: names, scope and the
// per-version printer columns.
func SlimCRD(obj map[string]any) map[string]any {
	md, _ := obj["metadata"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)
	var versions []any
	vs, _ := spec["versions"].([]any)
	for _, v := range vs {
		vm, _ := v.(map[string]any)
		keep := map[string]any{}
		for _, k := range []string{"name", "served", "storage", "deprecated", "deprecationWarning", "additionalPrinterColumns"} {
			if x, ok := vm[k]; ok {
				keep[k] = x
			}
		}
		versions = append(versions, keep)
	}
	return map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata":    map[string]any{"name": md["name"], "creationTimestamp": md["creationTimestamp"], "uid": md["uid"], "labels": md["labels"]},
		"spec":        map[string]any{"group": spec["group"], "names": spec["names"], "scope": spec["scope"], "versions": versions},
		"x-flightbay": map[string]any{"schemaCollected": false},
	}
}

func split(rs []bundle.APIResource) (early, late []bundle.APIResource) {
	for _, r := range rs {
		if rank(r) < 100 {
			early = append(early, r)
		} else {
			late = append(late, r)
		}
	}
	return
}

func (c *Collector) clientFor(r bundle.APIResource) string {
	if c.Policy.ObjectDetail == policy.DetailMetadata {
		return "metadata"
	}
	if r.Group == "" && r.Resource == "secrets" && c.Policy.Secrets == "metadata" {
		return "metadata"
	}
	if r.Group == "" && r.Resource == "configmaps" && c.Policy.ConfigMaps == "metadata" {
		return "metadata"
	}
	return "dynamic"
}

// scopes returns the namespaces to query: explicit names query one by one;
// otherwise a single cluster-wide list is filtered client-side.
func (c *Collector) scopes(namespaced bool) []string {
	if !namespaced {
		return []string{""}
	}
	if ns := c.Policy.ExplicitNamespaces(); len(ns) > 0 {
		var out []string
		for _, n := range ns {
			if c.Policy.NamespaceAllowed(n) {
				out = append(out, n)
			}
		}
		return out
	}
	return []string{""}
}

func (c *Collector) keep(r bundle.APIResource, obj map[string]any) bool {
	md, _ := obj["metadata"].(map[string]any)
	ns, _ := md["namespace"].(string)
	name, _ := md["name"].(string)
	if r.Namespaced {
		return c.Policy.NamespaceAllowed(ns)
	}
	if r.Group == "" && r.Resource == "namespaces" {
		return c.Policy.NamespaceAllowed(name)
	}
	return true
}

func (c *Collector) list(ctx context.Context, cl Clients, r bundle.APIResource, ns string, fn func(map[string]any)) error {
	gvr := schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}
	opts := metav1.ListOptions{Limit: 500}
	if r.Namespaced && c.Policy.LabelSelector != "" {
		opts.LabelSelector = c.Policy.LabelSelector
	}
	for {
		var cont string
		if c.clientFor(r) == "metadata" {
			if cl.Metadata == nil {
				return fmt.Errorf("metadata client unavailable")
			}
			var res metadata.ResourceInterface = cl.Metadata.Resource(gvr)
			if ns != "" {
				res = cl.Metadata.Resource(gvr).Namespace(ns)
			}
			l, err := res.List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&l.Items[i])
				if err != nil {
					return err
				}
				m["apiVersion"], m["kind"] = r.GroupVersion(), r.Kind
				fn(m)
			}
			cont = l.Continue
		} else {
			var res dynamic.ResourceInterface = cl.Dynamic.Resource(gvr)
			if ns != "" {
				res = cl.Dynamic.Resource(gvr).Namespace(ns)
			}
			l, err := res.List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				fn(l.Items[i].Object)
			}
			cont = l.GetContinue()
		}
		if cont == "" {
			return nil
		}
		opts.Continue = cont
	}
}

func (c *Collector) transform(loc string, r bundle.APIResource, obj map[string]any) map[string]any {
	if r.Group == "" && r.Resource == "secrets" {
		mode := redact.SecretKeys
		if c.Policy.Secrets == "metadata" || c.Policy.ObjectDetail == policy.DetailMetadata {
			mode = redact.SecretMetadata
		}
		return c.Redactor.Secret(loc, obj, mode, c.Policy.Plan().Selected(policy.CatHelm))
	}
	if c.Policy.ObjectDetail == policy.DetailMetadata {
		obj = map[string]any{"apiVersion": obj["apiVersion"], "kind": obj["kind"], "metadata": obj["metadata"]}
	}
	return c.Redactor.Object(loc, obj)
}

func reason(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "forbidden: " + err.Error()
	case apierrors.IsNotFound(err):
		return "not found: " + err.Error()
	default:
		return err.Error()
	}
}

type chunker struct {
	c       *Collector
	path    func(int) string
	list    map[string]any
	items   []any
	size    int
	part    int
	chunks  []string
	count   int
	stopped bool
}

func (k *chunker) add(obj map[string]any) error {
	if k.stopped {
		return nil
	}
	b, _ := json.Marshal(obj)
	k.items = append(k.items, obj)
	k.size += len(b)
	k.count++
	if len(k.items) >= k.c.ChunkItems || k.size >= k.c.ChunkBytes {
		return k.flush()
	}
	return nil
}

func (k *chunker) flush() error {
	if len(k.items) == 0 {
		return nil
	}
	if k.c.overBudget() {
		k.stopped = true
		k.count -= len(k.items)
		k.items = nil
		return nil
	}
	k.part++
	name := k.path(k.part)
	l := map[string]any{"items": k.items}
	for kk, v := range k.list {
		l[kk] = v
	}
	if err := k.c.Writer.AddJSON(name, l); err != nil {
		return err
	}
	k.chunks = append(k.chunks, name)
	k.items, k.size = nil, 0
	return nil
}

func (c *Collector) collectResource(ctx context.Context, cluster string, cl Clients, r bundle.APIResource, keepPods bool) ([]podInfo, error) {
	id := fmt.Sprintf("%s.resource.%s.%s.%s", cluster, groupName(r.Group), r.Version, r.Resource)
	cov := bundle.Coverage{ID: id, Cluster: cluster, Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, Kind: r.Kind, Namespaced: r.Namespaced, State: bundle.StateComplete}
	if c.overBudget() {
		cov.State, cov.Reason = bundle.StateTruncated, "bundle size budget reached before this collection"
		return nil, c.Writer.Coverage(cov)
	}
	c.progress("%s: %s", cluster, r.Name())
	loc := fmt.Sprintf("clusters/%s/resources/%s", cluster, r.Name())
	k := &chunker{c: c, path: func(p int) string { return bundle.ResourcePath(cluster, r.Group, r.Version, r.Resource, p) },
		list: map[string]any{"apiVersion": "v1", "kind": "List", "x-flightbay": map[string]any{"group": r.Group, "version": r.Version, "resource": r.Resource, "kind": r.Kind, "namespaced": r.Namespaced}}}
	var pods []podInfo
	var addErr error
	for _, ns := range c.scopes(r.Namespaced) {
		err := c.list(ctx, cl, r, ns, func(obj map[string]any) {
			if addErr != nil || !c.keep(r, obj) {
				return
			}
			if keepPods {
				pods = append(pods, parsePod(obj))
			}
			addErr = k.add(c.transform(loc, r, obj))
		})
		if addErr != nil {
			return nil, addErr
		}
		if err != nil {
			cov.State, cov.Reason = bundle.StateFailed, reason(err)
			c.record(LogEntry{Cluster: cluster, Operation: "list", Target: r.Name(), Namespace: ns, Result: "failed", Error: err.Error()})
			break
		}
	}
	if err := k.flush(); err != nil {
		return nil, err
	}
	if k.stopped && cov.State == bundle.StateComplete {
		cov.State, cov.Reason = bundle.StateTruncated, "bundle size budget reached"
	}
	cov.Chunks, cov.Items = k.chunks, k.count
	if cov.State == bundle.StateComplete {
		c.record(LogEntry{Cluster: cluster, Operation: "list", Target: r.Name(), Result: fmt.Sprintf("ok: %d items", k.count)})
	}
	return pods, c.Writer.Coverage(cov)
}

func groupName(g string) string {
	if g == "" {
		return "core"
	}
	return g
}

func parsePod(obj map[string]any) podInfo {
	md, _ := obj["metadata"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	p := podInfo{}
	p.ns, _ = md["namespace"].(string)
	p.name, _ = md["name"].(string)
	restarts := map[string]int64{}
	for _, key := range []string{"containerStatuses", "initContainerStatuses"} {
		list, _ := status[key].([]any)
		for _, s := range list {
			m, _ := s.(map[string]any)
			n, _ := m["name"].(string)
			switch v := m["restartCount"].(type) {
			case int64:
				restarts[n] = v
			case float64:
				restarts[n] = int64(v)
			case int:
				restarts[n] = int64(v)
			}
		}
	}
	for key, init := range map[string]bool{"initContainers": true, "containers": false, "ephemeralContainers": true} {
		list, _ := spec[key].([]any)
		for _, cc := range list {
			m, _ := cc.(map[string]any)
			n, _ := m["name"].(string)
			if key == "ephemeralContainers" {
				n = "ephemeral:" + n
			}
			p.containers = append(p.containers, containerInfo{name: n, init: init, restarts: restarts[strings.TrimPrefix(n, "ephemeral:")]})
		}
	}
	sort.Slice(p.containers, func(i, j int) bool { return p.containers[i].name < p.containers[j].name })
	return p
}

func (c *Collector) listPodsForLogs(ctx context.Context, cluster string, cl Clients) []podInfo {
	r := bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}
	var pods []podInfo
	for _, ns := range c.scopes(true) {
		gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
		var res dynamic.ResourceInterface = cl.Dynamic.Resource(gvr)
		if ns != "" {
			res = cl.Dynamic.Resource(gvr).Namespace(ns)
		}
		l, err := res.List(ctx, metav1.ListOptions{LabelSelector: c.Policy.LabelSelector})
		if err != nil {
			c.record(LogEntry{Cluster: cluster, Operation: "list", Target: "pods (for logs)", Namespace: ns, Result: "failed", Error: err.Error()})
			continue
		}
		for i := range l.Items {
			if c.keep(r, l.Items[i].Object) {
				pods = append(pods, parsePod(l.Items[i].Object))
			}
		}
	}
	return pods
}

func (c *Collector) collectEvents(ctx context.Context, cluster string, cl Clients) error {
	r := bundle.APIResource{Version: "v1", Resource: "events", Kind: "Event", Namespaced: true}
	cov := bundle.Coverage{ID: cluster + ".events", Cluster: cluster, Type: bundle.CoverageEvents, Version: "v1", Resource: "events", Kind: "Event", Namespaced: true, State: bundle.StateComplete}
	c.progress("%s: events", cluster)
	loc := fmt.Sprintf("clusters/%s/events", cluster)
	k := &chunker{c: c, path: func(p int) string { return bundle.EventsPath(cluster, p) }, list: map[string]any{"apiVersion": "v1", "kind": "EventList"}}
	var addErr error
	for _, ns := range c.scopes(true) {
		gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
		var res dynamic.ResourceInterface = cl.Dynamic.Resource(gvr)
		if ns != "" {
			res = cl.Dynamic.Resource(gvr).Namespace(ns)
		}
		l, err := res.List(ctx, metav1.ListOptions{})
		if err != nil {
			cov.State, cov.Reason = bundle.StateFailed, reason(err)
			c.record(LogEntry{Cluster: cluster, Operation: "list", Target: "events", Namespace: ns, Result: "failed", Error: err.Error()})
			break
		}
		for i := range l.Items {
			if addErr == nil && c.keep(r, l.Items[i].Object) {
				addErr = k.add(c.Redactor.Object(loc, l.Items[i].Object))
			}
		}
		if addErr != nil {
			return addErr
		}
	}
	if err := k.flush(); err != nil {
		return err
	}
	if k.stopped && cov.State == bundle.StateComplete {
		cov.State, cov.Reason = bundle.StateTruncated, "bundle size budget reached"
	}
	cov.Chunks, cov.Items = k.chunks, k.count
	return c.Writer.Coverage(cov)
}

func globAny(patterns []string, s string) bool {
	if len(patterns) == 0 {
		return true
	}
	p := &policy.Policy{Namespaces: patterns}
	return p.NamespaceAllowed(s)
}

func (c *Collector) collectLogs(ctx context.Context, cluster string, cl Clients, pods []podInfo) error {
	since := c.Policy.SinceDuration()
	for _, p := range pods {
		if !globAny(c.Policy.Logs.Pods, p.name) {
			continue
		}
		for _, ct := range p.containers {
			if strings.HasPrefix(ct.name, "ephemeral:") {
				if !c.Policy.Stream("ephemeral") {
					continue
				}
			} else if ct.init && !c.Policy.Stream("init") {
				continue
			}
			name := strings.TrimPrefix(ct.name, "ephemeral:")
			if !globAny(c.Policy.Logs.Containers, name) {
				continue
			}
			streams := []string{}
			if c.Policy.Stream("current") || ct.init {
				streams = append(streams, "current")
			}
			if c.Policy.Stream("previous") && ct.restarts > 0 {
				streams = append(streams, "previous")
			}
			for _, s := range streams {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := c.collectStream(ctx, cluster, cl, p, name, s, since); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *Collector) collectStream(ctx context.Context, cluster string, cl Clients, p podInfo, container, stream string, since time.Duration) error {
	id := fmt.Sprintf("%s.logs.%s.%s.%s.%s", cluster, p.ns, p.name, container, stream)
	cov := bundle.Coverage{ID: id, Cluster: cluster, Type: bundle.CoverageLogs, Namespace: p.ns, Pod: p.name, Container: container, Stream: stream, State: bundle.StateComplete}
	if c.overBudget() {
		cov.State, cov.Reason = bundle.StateTruncated, "bundle size budget reached before this collection"
		return c.Writer.Coverage(cov)
	}
	opts := &corev1.PodLogOptions{Container: container, Previous: stream == "previous", Timestamps: true}
	if since > 0 {
		secs := int64(since.Seconds())
		opts.SinceSeconds = &secs
	}
	var lines []string
	var truncated bool
	for attempt := 0; ; attempt++ {
		var kubeletErr string
		var err error
		lines, truncated, kubeletErr, err = c.readStream(ctx, cluster, cl, p, container, opts)
		if err == nil && kubeletErr == "" {
			break
		}
		if kubeletErr != "" && attempt < c.LogRetries && ctx.Err() == nil {
			// Usually a container restarting under us: the kubelet cannot
			// read the previous container until the new one settles.
			select {
			case <-ctx.Done():
			case <-time.After(c.LogRetryDelay):
			}
			continue
		}
		msg := kubeletErr
		if err != nil {
			msg = err.Error()
			cov.Reason = reason(err)
		} else {
			cov.Reason = "kubelet could not return the logs: " + kubeletErr
		}
		if strings.HasPrefix(msg, "stream interrupted") {
			cov.Reason = msg
			break // keep what was read before the interruption
		}
		cov.State = bundle.StateFailed
		c.record(LogEntry{Cluster: cluster, Operation: "logs", Target: p.name + "/" + container + " (" + stream + ")", Namespace: p.ns, Result: "failed", Error: msg})
		return c.Writer.Coverage(cov)
	}
	if cov.Reason != "" {
		cov.State = bundle.StateFailed
		c.record(LogEntry{Cluster: cluster, Operation: "logs", Target: p.name + "/" + container + " (" + stream + ")", Namespace: p.ns, Result: "partial", Error: cov.Reason})
	}
	max := c.Policy.Logs.MaxBytes
	part := 0
	var buf strings.Builder
	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		part++
		name := bundle.LogPath(cluster, p.ns, p.name, container, stream, part)
		if err := c.Writer.Add(name, []byte(buf.String())); err != nil {
			return err
		}
		cov.Chunks = append(cov.Chunks, name)
		buf.Reset()
		return nil
	}
	for _, l := range lines {
		buf.WriteString(l)
		if buf.Len() >= c.LogChunkBytes {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	cov.Items = len(lines)
	if truncated && cov.State == bundle.StateComplete {
		cov.State, cov.Reason = bundle.StateTruncated, fmt.Sprintf("kept the last %d bytes (per-stream limit)", max)
	}
	return c.Writer.Coverage(cov)
}

// kubeletErrorPrefixes are bodies the kubelet returns instead of log lines
// when it cannot read a container's log file. They arrive with HTTP 200 and no
// timestamp, so they would otherwise be archived as if the app printed them.
var kubeletErrorPrefixes = []string{
	"unable to retrieve container logs for ",
	"failed to try resolving symlinks in path ",
	"failed to get parse function: ",
}

// readStream reads and redacts one log stream, keeping the last MaxBytes.
// kubeletErr is set when the whole body is a kubelet error message.
func (c *Collector) readStream(ctx context.Context, cluster string, cl Clients, p podInfo, container string, opts *corev1.PodLogOptions) (lines []string, truncated bool, kubeletErr string, err error) {
	rc, err := cl.Logs.Stream(ctx, p.ns, p.name, opts)
	if err != nil {
		return nil, false, "", err
	}
	defer rc.Close()
	lr := c.Redactor.Lines(fmt.Sprintf("clusters/%s/logs/%s/%s/%s", cluster, p.ns, p.name, container))
	max := c.Policy.Logs.MaxBytes
	var size int64
	br := bufio.NewReaderSize(rc, 256<<10)
	first, firstIsErr, nlines := true, false, 0
	for {
		line, rerr := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			nlines++
			if first {
				first = false
				for _, pre := range kubeletErrorPrefixes {
					firstIsErr = firstIsErr || strings.HasPrefix(line, pre)
				}
			}
			if len(line) > 64<<10 {
				line = line[:64<<10] + " [flightbay: line truncated]"
			}
			line = lr.Line(line) + "\n"
			lines = append(lines, line)
			size += int64(len(line))
			for max > 0 && size > max && len(lines) > 0 {
				size -= int64(len(lines[0]))
				lines = lines[1:]
				truncated = true
			}
		}
		if rerr == io.EOF {
			if firstIsErr && nlines == 1 && len(lines) == 1 {
				return nil, false, strings.TrimSpace(lines[0]), nil
			}
			return lines, truncated, "", nil
		}
		if rerr != nil {
			return lines, truncated, "", fmt.Errorf("stream interrupted: %w", rerr)
		}
	}
}

// Warner records API server warnings (for example deprecations) in the
// collection log instead of printing them. Use it as rest.Config.WarningHandler.
type Warner struct {
	C       *Collector
	Cluster string
	seen    sync.Map
}

// HandleWarningHeader implements rest.WarningHandler.
func (w *Warner) HandleWarningHeader(code int, agent, text string) {
	if code != 299 || text == "" {
		return
	}
	if _, dup := w.seen.LoadOrStore(text, true); dup {
		return
	}
	w.C.record(LogEntry{Cluster: w.Cluster, Operation: "api-warning", Result: "warning", Error: text})
}
