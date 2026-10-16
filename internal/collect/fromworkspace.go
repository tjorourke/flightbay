package collect

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// ExportOptions configure ExportWorkspace.
type ExportOptions struct {
	Collector   string // recorded in bundle.json, e.g. "flightbay-desk live 0.1.0"
	Compression string // as for collect; default "better" (a live save should be quick)
	ChunkItems  int    // objects per resource member (default 500)
	LogChunk    int    // bytes per log member (default 1 MiB)
}

// ExportWorkspace writes what a workspace holds as a normal bundle, in the
// collector's member layout: discovery, objects per resource, events, logs
// per stream, diagnostics, coverage, the collection log, the redaction report
// and the manifest. flightbay inspect verifies it and flightbay-desk open
// opens it. It is how a live mirror is saved: "capture now" without asking
// the cluster again. Everything in the workspace was redacted on the way in.
func ExportWorkspace(ws *store.Workspace, dst io.Writer, o ExportOptions) error {
	if o.Compression == "" {
		o.Compression = "better"
	}
	if o.ChunkItems <= 0 {
		o.ChunkItems = 500
	}
	if o.LogChunk <= 0 {
		o.LogChunk = 1 << 20
	}
	src := ws.Header()
	if src == nil {
		return fmt.Errorf("the workspace has no header yet")
	}
	w, err := bundle.NewWriter(dst, bundle.WriterOptions{Compression: o.Compression})
	if err != nil {
		return err
	}
	h := *src
	h.BundleID = ""
	if h.Source == "" {
		h.Source = store.SourceLive
	}
	if o.Collector != "" {
		h.Collector = o.Collector
	}
	if err := w.WriteHeader(h); err != nil {
		return err
	}
	written := map[string]bool{} // coverage ids rewritten below
	cov := func(c bundle.Coverage) error {
		written[bundle.SafeID(c.ID)] = true
		return w.Coverage(c)
	}
	existing := map[string]bundle.Coverage{}
	for _, c := range ws.Coverage() {
		existing[c.ID] = c
	}
	for _, cluster := range ws.Clusters() {
		if v := ws.Version(cluster); len(v) > 0 {
			if err := w.Add(bundle.DiscoveryPath(cluster, "version.json"), v); err != nil {
				return err
			}
		}
		res := ws.RegisteredResources(cluster)
		if err := w.AddJSON(bundle.DiscoveryPath(cluster, "apiresources.json"), res); err != nil {
			return err
		}
		for _, crd := range ws.CRDs(cluster) {
			name, _ := asMap(crd["metadata"])["name"].(string)
			if name == "" {
				continue
			}
			if err := w.AddJSON(bundle.DiscoveryPath(cluster, "crds/"+bundle.SafeID(name)+".json"), crd); err != nil {
				return err
			}
		}
		// Objects, one resource type at a time.
		for _, r := range res {
			objs := ws.List(cluster, r.Group, r.Resource, "", nil)
			id := bundle.SafeID(fmt.Sprintf("%s.resource.%s.%s.%s", cluster, groupName(r.Group), r.Version, r.Resource))
			c, ok := existing[id]
			if !ok && len(objs) == 0 {
				continue
			}
			if !ok {
				c = bundle.Coverage{ID: id, Cluster: cluster, Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource,
					Kind: r.Kind, Namespaced: r.Namespaced, State: bundle.StateComplete}
			}
			c.Chunks, c.Items = nil, len(objs)
			meta := map[string]any{"group": r.Group, "version": r.Version, "resource": r.Resource, "kind": r.Kind, "namespaced": r.Namespaced}
			for i, part := 0, 1; i < len(objs); i, part = i+o.ChunkItems, part+1 {
				j := min(i+o.ChunkItems, len(objs))
				items := make([]map[string]any, 0, j-i)
				for _, ob := range objs[i:j] {
					items = append(items, ob.Obj)
				}
				name := bundle.ResourcePath(cluster, r.Group, r.Version, r.Resource, part)
				if err := w.AddJSON(name, map[string]any{"apiVersion": "v1", "kind": "List", "x-flightbay": meta, "items": items}); err != nil {
					return err
				}
				c.Chunks = append(c.Chunks, name)
			}
			if err := cov(c); err != nil {
				return err
			}
		}
		// Events.
		evs := ws.Events(cluster)
		evID := cluster + ".events"
		if c, ok := existing[evID]; ok || len(evs) > 0 {
			if !ok {
				c = bundle.Coverage{ID: evID, Cluster: cluster, Type: bundle.CoverageEvents, Version: "v1", Resource: "events", Kind: "Event", Namespaced: true, State: bundle.StateComplete}
			}
			c.Chunks, c.Items = nil, len(evs)
			for i, part := 0, 1; i < len(evs); i, part = i+o.ChunkItems, part+1 {
				j := min(i+o.ChunkItems, len(evs))
				items := make([]map[string]any, 0, j-i)
				for _, e := range evs[i:j] {
					items = append(items, e.Obj)
				}
				name := bundle.EventsPath(cluster, part)
				if err := w.AddJSON(name, map[string]any{"apiVersion": "v1", "kind": "EventList", "items": items}); err != nil {
					return err
				}
				c.Chunks = append(c.Chunks, name)
			}
			if err := cov(c); err != nil {
				return err
			}
		}
	}
	// Logs, one stream at a time, as the kubelet returned them (timestamp first).
	for _, s := range ws.AllLogStreams() {
		recs := ws.LogStream(s.Cluster, s.Namespace, s.Pod, s.Container, s.Stream)
		id := bundle.SafeID(fmt.Sprintf("%s.logs.%s.%s.%s.%s", s.Cluster, s.Namespace, s.Pod, s.Container, s.Stream))
		c, ok := existing[id]
		if !ok {
			c = bundle.Coverage{ID: id, Cluster: s.Cluster, Type: bundle.CoverageLogs, Namespace: s.Namespace, Pod: s.Pod, Container: s.Container, Stream: s.Stream, State: bundle.StateComplete}
		}
		c.Chunks, c.Items = nil, len(recs)
		var buf strings.Builder
		part := 0
		flush := func() error {
			if buf.Len() == 0 {
				return nil
			}
			part++
			name := bundle.LogPath(s.Cluster, s.Namespace, s.Pod, s.Container, s.Stream, part)
			if err := w.Add(name, []byte(buf.String())); err != nil {
				return err
			}
			c.Chunks = append(c.Chunks, name)
			buf.Reset()
			return nil
		}
		for _, r := range recs {
			if !r.TS.IsZero() {
				buf.WriteString(r.TS.Format(time.RFC3339Nano))
				buf.WriteByte(' ')
			}
			buf.WriteString(r.Raw)
			buf.WriteByte('\n')
			if buf.Len() >= o.LogChunk {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}
		if len(c.Chunks) == 0 {
			continue
		}
		if err := cov(c); err != nil {
			return err
		}
	}
	// Diagnostics as captured.
	for _, cluster := range ws.Clusters() {
		for _, d := range ws.Diagnostics(cluster) {
			dir := bundle.DiagnosticsPath(cluster, d.Kind, d.Namespace, d.Pod, "")
			if err := w.AddJSON(dir+"meta.json", DiagMeta{Component: d.Component, Container: d.Container, Image: d.Image, Version: d.Version, Node: d.Node}); err != nil {
				return err
			}
			files := make([]string, 0, len(d.Files))
			for f := range d.Files {
				files = append(files, f)
			}
			sort.Strings(files)
			for _, f := range files {
				if err := w.Add(dir+f, d.Files[f]); err != nil {
					return err
				}
			}
		}
		if s := ws.DiagnosticSummary(cluster); len(s) > 0 {
			if err := w.Add(bundle.DiagnosticsPath(cluster, "", "", "", "summary.json"), s); err != nil {
				return err
			}
		}
	}
	// Every other coverage record (diagnostics, streams not followed,
	// namespaces, retention notes), so the saved bundle explains its gaps.
	ids := make([]string, 0, len(existing))
	for id := range existing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if written[bundle.SafeID(id)] {
			continue
		}
		c := existing[id]
		if c.Type == bundle.CoverageLogs && c.Pod != "" {
			c.Chunks = nil // no lines were stored for this stream
		}
		if err := cov(c); err != nil {
			return err
		}
	}
	var sb strings.Builder
	for _, l := range ws.CollectionLog() {
		sb.Write(l)
		sb.WriteByte('\n')
	}
	b, _ := json.Marshal(map[string]any{"time": time.Now().UTC(), "cluster": strings.Join(h.Contexts, ","), "operation": "save", "result": "ok: saved from the live workspace"})
	sb.Write(b)
	sb.WriteByte('\n')
	if err := w.Add(bundle.CollectionLog, []byte(sb.String())); err != nil {
		return err
	}
	rep := ws.Redaction()
	if len(rep) == 0 {
		rep = json.RawMessage(`{"total":0,"locations":{}}`)
	}
	if err := w.Add(bundle.RedactionName, rep); err != nil {
		return err
	}
	return w.Close(bundle.Manifest{Complete: true})
}
