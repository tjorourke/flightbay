// Package importer streams a bundle into a workspace. Each tar member is
// committed as soon as it has been read in full, so engineers can browse and
// search while the rest of the bundle is still arriving.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/sign"
	"github.com/tjorourke/flightbay/internal/store"
)

// Options configure an import.
type Options struct {
	Reader   bundle.ReaderOptions
	Source   string
	Throttle int64 // bytes per second; 0 = unlimited (used to rehearse slow uploads)
	// Skip defers categories ("objects", "logs", "events", "diagnostics"): their members are
	// still verified but not loaded. A later Run without Skip loads them.
	Skip map[string]bool
	// Trusted are the public keys a signature is checked against.
	Trusted []sign.PublicKey
}

func category(kind string) string {
	switch kind {
	case bundle.PathResource:
		return "objects"
	case bundle.PathLog:
		return "logs"
	case bundle.PathEvents:
		return "events"
	case bundle.PathDiagnostics:
		return "diagnostics"
	case bundle.PathFlows:
		return "flows"
	}
	return ""
}

type countingReader struct {
	ctx   context.Context
	r     io.Reader
	n     atomic.Int64
	rate  int64
	start time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if c.rate > 0 {
		max := int(c.rate / 20)
		if max < 512 {
			max = 512
		}
		if len(p) > max {
			p = p[:max]
		}
	}
	n, err := c.r.Read(p)
	total := c.n.Add(int64(n))
	if c.rate > 0 && n > 0 {
		want := time.Duration(float64(total) / float64(c.rate) * float64(time.Second))
		if sleep := want - time.Since(c.start); sleep > 0 {
			select {
			case <-time.After(sleep):
			case <-c.ctx.Done():
				return n, c.ctx.Err()
			}
		}
	}
	return n, err
}

type chunkList struct {
	Items []map[string]any `json:"items"`
	Meta  *struct {
		Group      string `json:"group"`
		Version    string `json:"version"`
		Resource   string `json:"resource"`
		Kind       string `json:"kind"`
		Namespaced bool   `json:"namespaced"`
	} `json:"x-flightbay"`
}

// Run imports src into ws and returns once the stream ends. The workspace is
// usable throughout; its state says whether the result was verified.
func Run(ctx context.Context, src io.Reader, ws *store.Workspace, o Options) error {
	cr := &countingReader{ctx: ctx, r: src, rate: o.Throttle, start: time.Now()}
	ws.SetState(func(s *store.ImportState) {
		s.Phase, s.Source, s.Started, s.Finished = store.PhaseLoading, o.Source, time.Now().UTC(), time.Time{}
		s.Error, s.Verified, s.Members, s.BytesReceived = "", false, 0, 0
	})
	deferred := map[string]bool{}
	fail := func(err error) error {
		ws.WaitLogs()
		ws.SetState(func(s *store.ImportState) {
			s.BytesReceived = cr.n.Load()
			s.Finished = time.Now().UTC()
			s.Verified = false
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				s.Phase = store.PhaseCancelled
				s.Error = "import cancelled; the workspace holds only the data loaded before cancellation"
			} else {
				s.Phase = store.PhaseFailed
				s.Error = err.Error()
			}
		})
		return err
	}
	r, err := bundle.NewReader(cr, o.Reader)
	if err != nil {
		return fail(err)
	}
	for {
		m, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(err)
		}
		info, known := bundle.ParsePath(m.Name)
		cat := ""
		if known {
			cat = category(info.Kind)
		}
		switch {
		case cat != "" && o.Skip[cat]:
			deferred[cat] = true
		case cat != "" && ws.HasChunk(m.Name):
			// already committed by an earlier import of the same bundle
		default:
			if err := apply(ws, m, r); err != nil {
				return fail(fmt.Errorf("%s: %w", m.Name, err))
			}
			if cat != "" {
				ws.MarkChunk(m.Name)
			}
		}
		ws.SetState(func(s *store.ImportState) { s.Members++; s.BytesReceived = cr.n.Load() })
	}
	if err := r.Verify(); err != nil {
		if r.Signed() {
			ws.SetSignature(sign.Result{Signed: true, Error: "the bundle failed verification, so its signature cannot vouch for it"})
		}
		return fail(err)
	}
	if r.Signed() {
		ws.SetSignature(r.Signature(o.Trusted))
	} else {
		ws.SetSignature(sign.Result{})
	}
	ws.WaitLogs() // every queued log chunk is committed before "complete"
	if e := ws.State().Error; strings.HasPrefix(e, "log store") {
		return fail(errors.New(e))
	}
	ws.SetState(func(s *store.ImportState) {
		s.Phase, s.Verified, s.Finished, s.BytesReceived = store.PhaseComplete, true, time.Now().UTC(), cr.n.Load()
		s.Deferred = nil
		for _, c := range []string{"objects", "logs", "events", "diagnostics"} {
			if deferred[c] {
				s.Deferred = append(s.Deferred, c)
			}
		}
	})
	return nil
}

func apply(ws *store.Workspace, m *bundle.Member, r *bundle.Reader) error {
	info, ok := bundle.ParsePath(m.Name)
	if !ok {
		return nil // unknown members are verified but ignored, for forward compatibility
	}
	switch info.Kind {
	case bundle.PathHeader:
		ws.SetHeader(r.Header())
	case bundle.PathManifest:
		ws.SetManifest(r.Manifest())
	case bundle.PathDiscovery:
		switch info.File {
		case "version.json":
			ws.SetVersion(info.Cluster, json.RawMessage(m.Data))
		case "apiresources.json":
			var rs []bundle.APIResource
			if err := json.Unmarshal(m.Data, &rs); err != nil {
				return err
			}
			ws.RegisterResources(info.Cluster, rs)
		default:
			if strings.HasPrefix(info.File, "crds/") {
				var crd map[string]any
				if err := json.Unmarshal(m.Data, &crd); err != nil {
					return err
				}
				ws.AddCRD(info.Cluster, crd)
			}
		}
	case bundle.PathResource:
		var l chunkList
		if err := json.Unmarshal(m.Data, &l); err != nil {
			return err
		}
		res := bundle.APIResource{Group: info.Group, Version: info.Version, Resource: info.Resource}
		if l.Meta != nil {
			res.Kind, res.Namespaced = l.Meta.Kind, l.Meta.Namespaced
		}
		if known, ok := ws.FindResource(info.Cluster, res.Name()); ok {
			if res.Kind == "" {
				res.Kind = known.Kind
			}
			if l.Meta == nil {
				res.Namespaced = known.Namespaced
			}
			res.ShortNames, res.SingularName, res.Categories = known.ShortNames, known.SingularName, known.Categories
		}
		ws.AddObjects(info.Cluster, res, l.Items, m.Name)
	case bundle.PathEvents:
		var l chunkList
		if err := json.Unmarshal(m.Data, &l); err != nil {
			return err
		}
		ws.AddEvents(info.Cluster, l.Items)
	case bundle.PathLog:
		lines := strings.Split(strings.TrimRight(string(m.Data), "\n"), "\n")
		ws.QueueLogLines(info.Cluster, info.Namespace, info.Pod, info.Container, info.Stream, lines, m.Name, nil)
	case bundle.PathCoverage:
		var c bundle.Coverage
		if err := json.Unmarshal(m.Data, &c); err != nil {
			return err
		}
		ws.SetCoverage(c)
	case bundle.PathCollection:
		var lines []json.RawMessage
		for _, l := range bytes.Split(bytes.TrimSpace(m.Data), []byte("\n")) {
			if len(l) > 0 {
				lines = append(lines, json.RawMessage(append([]byte{}, l...)))
			}
		}
		ws.AddCollectionLog(lines)
	case bundle.PathRedaction:
		ws.SetRedaction(json.RawMessage(m.Data))
	case bundle.PathLedger:
		ws.SetLedger(json.RawMessage(m.Data))
	case bundle.PathFlows:
		// A flow log attached at collection. A file that no longer parses is
		// recorded, not fatal: the rest of the bundle is still good.
		if _, err := ws.ImportFlowFile(info.Cluster, info.File, bytes.NewReader(m.Data), info.Sub, store.OriginBundle); err != nil {
			ws.AddCollectionLog([]json.RawMessage{mustJSON(map[string]string{"cluster": info.Cluster, "operation": "import-flows", "target": info.File, "result": "failed", "error": err.Error()})})
		}
	case bundle.PathDiagnostics:
		if info.Sub == "" {
			if info.File == "summary.json" {
				ws.SetDiagnosticSummary(info.Cluster, json.RawMessage(m.Data))
			}
			return nil
		}
		ws.AddDiagnostic(info.Cluster, info.Sub, info.Namespace, info.Pod, info.File, m.Data)
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
