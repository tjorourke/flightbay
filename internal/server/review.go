package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/store"
)

// ReviewSession is review mode (flightbay review): the customer decides what
// to send to Solo and exports a trimmed, signed bundle.
type ReviewSession struct {
	Source  string         // the original bundle file
	Out     string         // where the export goes
	Options review.Options // identities, signer, recipients, trusted keys, tool
	Plan    review.Plan    // starting plan (reviewer name)

	mu       sync.Mutex
	exported []reviewExport
}

type reviewExport struct {
	Path      string         `json:"path"`
	Bytes     int64          `json:"bytes"`
	SHA256    string         `json:"sha256"`
	Signed    bool           `json:"signed"`
	KeyID     string         `json:"keyId,omitempty"`
	Signer    string         `json:"signer,omitempty"`
	Encrypted bool           `json:"encrypted"`
	At        time.Time      `json:"at"`
	Ledger    *review.Ledger `json:"ledger"`
}

func (s *Server) planPath() string { return filepath.Join(s.WorkDir, "review-plan.json") }

// currentPlan is the last plan the UI saved, or the starting plan.
func (s *Server) currentPlan() review.Plan {
	if b, err := os.ReadFile(s.planPath()); err == nil {
		var p review.Plan
		if json.Unmarshal(b, &p) == nil {
			return p
		}
	}
	return s.Review.Plan
}

func (s *Server) readPlan(w http.ResponseWriter, r *http.Request) (*review.Plan, bool) {
	if r.Method != http.MethodPost {
		jerr(w, http.StatusMethodNotAllowed, "POST a review plan")
		return nil, false
	}
	if !sameOrigin(r) {
		jerr(w, http.StatusForbidden, "cross-origin request refused")
		return nil, false
	}
	var p review.Plan
	if err := json.NewDecoder(io.LimitReader(r.Body, 32<<20)).Decode(&p); err != nil {
		jerr(w, 400, "unreadable plan: "+err.Error())
		return nil, false
	}
	if err := p.Validate(); err != nil {
		jerr(w, 400, err.Error())
		return nil, false
	}
	if b, err := json.MarshalIndent(p, "", "  "); err == nil {
		_ = os.WriteFile(s.planPath(), b, 0o600)
	}
	return &p, true
}

func (s *Server) reviewOn(w http.ResponseWriter) bool {
	if s.Review == nil {
		jerr(w, http.StatusNotFound, "not in review mode; open the bundle with flightbay review")
		return false
	}
	return true
}

type rvNamespace struct {
	Name       string `json:"name"`
	Objects    int    `json:"objects"`
	LogStreams int    `json:"logStreams"`
	LogLines   int    `json:"logLines"`
	Events     int    `json:"events"`
}

type rvType struct {
	Group      string `json:"group"`
	Resource   string `json:"resource"`
	Kind       string `json:"kind"`
	Namespaced bool   `json:"namespaced"`
	Objects    int    `json:"objects"`
}

type rvStream struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Stream    string `json:"stream"`
	Lines     int    `json:"lines"`
}

type rvFile struct {
	Kind      string `json:"kind,omitempty"` // proxies or control-plane
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Component string `json:"component,omitempty"`
	File      string `json:"file"`
	Bytes     int    `json:"bytes"`
}

type rvCluster struct {
	Name        string        `json:"name"`
	Namespaces  []rvNamespace `json:"namespaces"`
	Types       []rvType      `json:"types"`
	Streams     []rvStream    `json:"streams"`
	Diagnostics []rvFile      `json:"diagnostics"`
	Flows       []rvFile      `json:"flows"`
	Events      int           `json:"events"`
}

// reviewInventory lists what the bundle holds, grouped the way the Review
// page offers it for removal.
func (s *Server) reviewInventory() []rvCluster {
	var out []rvCluster
	for _, c := range s.WS.Clusters() {
		rc := rvCluster{Name: c, Namespaces: []rvNamespace{}, Types: []rvType{}, Streams: []rvStream{}, Diagnostics: []rvFile{}, Flows: []rvFile{}}
		nss := map[string]*rvNamespace{}
		ns := func(n string) *rvNamespace {
			if nss[n] == nil {
				nss[n] = &rvNamespace{Name: n}
			}
			return nss[n]
		}
		types := map[string]*rvType{}
		for _, o := range s.WS.AllObjects(c) {
			k := o.Group + "/" + o.Resource
			if types[k] == nil {
				types[k] = &rvType{Group: o.Group, Resource: o.Resource, Kind: o.Kind, Namespaced: o.Namespace != ""}
			}
			types[k].Objects++
			if o.Namespace != "" {
				ns(o.Namespace).Objects++
			} else if o.Group == "" && o.Resource == "namespaces" {
				ns(o.Name)
			}
		}
		for _, e := range s.WS.Events(c) {
			md, _ := e.Obj["metadata"].(map[string]any)
			if n, _ := md["namespace"].(string); n != "" {
				ns(n).Events++
			}
		}
		rc.Events = len(s.WS.Events(c))
		for _, cv := range s.WS.Coverage() {
			if cv.Cluster != c {
				continue
			}
			switch cv.Type {
			case bundle.CoverageLogs:
				if cv.State == bundle.StateRemoved || len(cv.Chunks) == 0 {
					continue
				}
				rc.Streams = append(rc.Streams, rvStream{Namespace: cv.Namespace, Pod: cv.Pod, Container: cv.Container, Stream: cv.Stream, Lines: cv.Items})
				n := ns(cv.Namespace)
				n.LogStreams++
				n.LogLines += cv.Items
			case bundle.CoverageFlows:
				for _, ch := range cv.Chunks {
					if info, ok := bundle.ParsePath(ch); ok {
						rc.Flows = append(rc.Flows, rvFile{Kind: info.Sub, File: info.File})
					}
				}
			}
		}
		for _, d := range s.WS.Diagnostics(c) {
			for f, b := range d.Files {
				rc.Diagnostics = append(rc.Diagnostics, rvFile{Kind: d.Kind, Namespace: d.Namespace, Pod: d.Pod, Component: d.Component, File: f, Bytes: len(b)})
			}
		}
		for _, n := range nss {
			rc.Namespaces = append(rc.Namespaces, *n)
		}
		sort.Slice(rc.Namespaces, func(i, j int) bool { return rc.Namespaces[i].Name < rc.Namespaces[j].Name })
		for _, t := range types {
			rc.Types = append(rc.Types, *t)
		}
		sort.Slice(rc.Types, func(i, j int) bool {
			if rc.Types[i].Kind != rc.Types[j].Kind {
				return rc.Types[i].Kind < rc.Types[j].Kind
			}
			return rc.Types[i].Group < rc.Types[j].Group
		})
		sort.Slice(rc.Streams, func(i, j int) bool {
			a, b := rc.Streams[i], rc.Streams[j]
			return a.Namespace+"/"+a.Pod+"/"+a.Container+"/"+a.Stream < b.Namespace+"/"+b.Pod+"/"+b.Container+"/"+b.Stream
		})
		sort.Slice(rc.Diagnostics, func(i, j int) bool {
			a, b := rc.Diagnostics[i], rc.Diagnostics[j]
			return a.Namespace+"/"+a.Pod+"/"+a.File < b.Namespace+"/"+b.Pod+"/"+b.File
		})
		out = append(out, rc)
	}
	return out
}

func (s *Server) reviewState(w http.ResponseWriter, r *http.Request) {
	if !s.reviewOn(w) {
		return
	}
	if r.URL.Query().Get("format") == "yaml" {
		b, _ := yaml.Marshal(s.currentPlan())
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="review-plan.yaml"`)
		_, _ = w.Write(b)
		return
	}
	rs := s.Review
	out := map[string]any{"source": rs.Source, "out": rs.Out, "plan": s.currentPlan(), "clusters": s.reviewInventory(),
		"encrypted": len(rs.Options.Recipients) > 0}
	if k := rs.Options.Signer; k != nil {
		out["signer"] = map[string]string{"name": k.Signer, "keyId": k.ID()}
	}
	rs.mu.Lock()
	out["exports"] = append([]reviewExport{}, rs.exported...)
	rs.mu.Unlock()
	writeJSON(w, 200, out)
}

func (s *Server) reviewOpen() (io.ReadCloser, error) { return os.Open(s.Review.Source) }

// reviewPreview runs the review into a discarding writer and returns the
// ledger: exactly what the export would remove.
func (s *Server) reviewPreview(w http.ResponseWriter, r *http.Request) {
	if !s.reviewOn(w) {
		return
	}
	p, ok := s.readPlan(w, r)
	if !ok {
		return
	}
	f, err := s.reviewOpen()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	defer f.Close()
	o := s.Review.Options
	o.Signer, o.Recipients, o.Compression = nil, nil, "fastest"
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	led, err := review.Apply(ctx, f, io.Discard, p, o)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ledger": led, "summary": led.Summary()})
}

func (s *Server) reviewExport(w http.ResponseWriter, r *http.Request) {
	if !s.reviewOn(w) {
		return
	}
	p, ok := s.readPlan(w, r)
	if !ok {
		return
	}
	rs := s.Review
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	led, err := review.WriteFile(ctx, rs.Source, rs.Out, p, rs.Options)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	ex := reviewExport{Path: rs.Out, Encrypted: len(rs.Options.Recipients) > 0, At: time.Now().UTC(), Ledger: led}
	if f, err := os.Open(rs.Out); err == nil {
		h := sha256.New()
		ex.Bytes, _ = io.Copy(h, f)
		f.Close()
		ex.SHA256 = hex.EncodeToString(h.Sum(nil))
	}
	if k := rs.Options.Signer; k != nil {
		ex.Signed, ex.KeyID, ex.Signer = true, k.ID(), k.Signer
	}
	rs.mu.Lock()
	rs.exported = append(rs.exported, ex)
	rs.mu.Unlock()
	writeJSON(w, 200, ex)
}

// reviewLines turns a log search into line references for the plan.
func (s *Server) reviewLines(w http.ResponseWriter, r *http.Request) {
	if !s.reviewOn(w) {
		return
	}
	q := r.URL.Query()
	if id := q.Get("id"); id != "" {
		n, _ := strconv.ParseInt(id, 10, 64)
		l, err := s.WS.LineLocation(n)
		if err != nil {
			jerr(w, 404, "no such line")
			return
		}
		writeJSON(w, 200, map[string]any{"total": 1, "lines": []review.LineRef{{Chunk: l.Chunk, Index: l.Index, SHA256: review.TextSHA(l.Raw)}}})
		return
	}
	f := store.LogFilter{Query: q.Get("q"), Cluster: q.Get("cluster"), Namespace: q.Get("namespace"), Pod: q.Get("pod"), Container: q.Get("container"),
		Level: q.Get("level"), Stream: q.Get("stream"), Workload: q.Get("workload")}
	if v := q.Get("from"); v != "" {
		f.From, _ = time.Parse(time.RFC3339Nano, v)
	}
	if v := q.Get("to"); v != "" {
		f.To, _ = time.Parse(time.RFC3339Nano, v)
	}
	if strings.TrimSpace(f.Query) == "" && f.Namespace == "" && f.Pod == "" && f.Container == "" && f.Level == "" && f.Workload == "" && f.From.IsZero() && f.To.IsZero() {
		jerr(w, 400, "narrow the search first; to remove every log line, remove the log streams on the Review page")
		return
	}
	const max = 20000
	locs, total, err := s.WS.LineLocations(f, max)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	if total > max {
		jerr(w, 400, strconv.Itoa(total)+" lines match; narrow the search, or add a pattern on the Review page to drop matching lines")
		return
	}
	refs := make([]review.LineRef, 0, len(locs))
	for _, l := range locs {
		refs = append(refs, review.LineRef{Chunk: l.Chunk, Index: l.Index, SHA256: review.TextSHA(l.Raw)})
	}
	writeJSON(w, 200, map[string]any{"total": total, "lines": refs})
}
