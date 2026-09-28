package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/diff"
	"github.com/tjorourke/flightbay/internal/store"
)

type baselineState struct {
	mu      sync.RWMutex
	ws      *store.Workspace
	path    string
	loading string // path being loaded, "" when idle
	started time.Time
	err     string
}

func (s *Server) baseline() *baselineState { return &s.base }

// SetBaseline attaches an earlier bundle to compare the workspace against.
// A result for a path that is no longer the one being loaded is dropped, so
// picking a second bundle while the first loads keeps the second.
func (s *Server) SetBaseline(ws *store.Workspace, path string) {
	b := s.baseline()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loading != "" && b.loading != path {
		return
	}
	if old := b.ws; old != nil && old != ws {
		// A comparison may still be reading the old one; free it later.
		go func() { time.Sleep(time.Minute); _ = old.Close() }()
	}
	b.ws, b.path, b.loading, b.err = ws, path, "", ""
}

// BaselineLoading records that path is being loaded as the baseline.
func (s *Server) BaselineLoading(path string) {
	b := s.baseline()
	b.mu.Lock()
	b.loading, b.started, b.err = path, time.Now().UTC(), ""
	b.mu.Unlock()
}

// BaselineFailed records that loading path as the baseline failed.
func (s *Server) BaselineFailed(path string, err error) {
	b := s.baseline()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loading == path {
		b.loading, b.err = "", err.Error()
	}
}

func (s *Server) baselineDiff(w http.ResponseWriter, _ *http.Request) {
	b := s.baseline()
	b.mu.RLock()
	bws, path, loading, started, lerr := b.ws, b.path, b.loading, b.started, b.err
	b.mu.RUnlock()
	out := map[string]any{"available": false, "canLoad": s.LoadBaseline != nil}
	if lerr != "" {
		out["error"] = lerr
	}
	if loading != "" {
		out["loading"], out["loadingPath"], out["loadingSince"] = true, loading, started
		if bws == nil {
			writeJSON(w, 200, out)
			return
		}
	}
	if bws == nil {
		writeJSON(w, 200, out)
		return
	}
	out["available"], out["path"], out["report"] = true, path, diff.Compare(bws, s.WS)
	out["workspaceLoading"] = s.WS.State().Phase == store.PhaseLoading
	if loading == "" {
		out["loading"] = false
	}
	writeJSON(w, 200, out)
}

// candidate is a bundle file the Compare view offers as a baseline.
type candidate struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	Modified    time.Time `json:"modified"`
	Case        string    `json:"case,omitempty"`
	BundleID    string    `json:"bundleId,omitempty"`
	Clusters    []string  `json:"clusters,omitempty"`
	Captured    time.Time `json:"captured,omitempty"`
	Encrypted   bool      `json:"encrypted,omitempty"`
	Unreadable  string    `json:"unreadable,omitempty"`
	SameCluster bool      `json:"sameCluster"`
	Current     bool      `json:"current,omitempty"` // loaded as the baseline now
}

// openBundlePath is the bundle the workspace was opened from, "" for a stream.
func (s *Server) openBundlePath() string {
	p := s.BundlePath
	if p == "" {
		p = s.WS.State().Source
	}
	if p == "" || p == "-" {
		return ""
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func (s *Server) candidateDirs() []string {
	var dirs []string
	if p := s.openBundlePath(); p != "" {
		dirs = append(dirs, filepath.Dir(p))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, "Downloads"))
	}
	dirs = append(dirs, s.BaselineDirs...)
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if a, err := filepath.Abs(d); err == nil {
			d = a
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// readHeader reads bundle.json, the first member, without the rest.
func (s *Server) readHeader(path string) (*bundle.Header, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	r, err := bundle.NewReader(io.LimitReader(f, 64<<20), bundle.ReaderOptions{Identities: s.Identities, MaxMemberBytes: 8 << 20})
	if err != nil {
		return nil, strings.Contains(err.Error(), "encrypted"), err
	}
	if _, err := r.Next(); err != nil {
		return nil, r.Encrypted, err
	}
	return r.Header(), r.Encrypted, nil
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// baselineCandidates lists bundles near the open one that could be loaded as
// a baseline: its directory, the working directory and ~/Downloads.
func (s *Server) baselineCandidates(w http.ResponseWriter, _ *http.Request) {
	open := s.openBundlePath()
	b := s.baseline()
	b.mu.RLock()
	curPath := b.path
	b.mu.RUnlock()
	mine := map[string]bool{}
	for _, c := range s.WS.Clusters() {
		mine[c] = true
	}
	seen := map[string]bool{}
	out := []candidate{}
	for _, dir := range s.candidateDirs() {
		for _, pat := range []string{"*.tar.zst", "*.tar.zst.age"} {
			ms, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, m := range ms {
				abs, err := filepath.Abs(m)
				if err != nil || seen[abs] || len(out) >= 200 {
					continue
				}
				seen[abs] = true
				fi, err := os.Stat(abs)
				if err != nil || !fi.Mode().IsRegular() || sameFile(abs, open) {
					continue
				}
				c := candidate{Path: abs, Name: filepath.Base(abs), Size: fi.Size(), Modified: fi.ModTime().UTC(), Current: sameFile(abs, curPath)}
				h, enc, err := s.readHeader(abs)
				c.Encrypted = enc
				if err != nil {
					c.Unreadable = err.Error()
				} else {
					c.Case, c.BundleID, c.Captured = h.Case, h.BundleID, h.CaptureStart
					for _, ctx := range h.Contexts {
						id := bundle.SafeID(ctx)
						c.Clusters = append(c.Clusters, id)
						c.SameCluster = c.SameCluster || mine[id]
					}
				}
				out = append(out, c)
			}
		}
	}
	when := func(c candidate) time.Time {
		if !c.Captured.IsZero() {
			return c.Captured
		}
		return c.Modified
	}
	sort.SliceStable(out, func(i, j int) bool { return when(out[i]).After(when(out[j])) })
	writeJSON(w, 200, map[string]any{"candidates": out, "open": open, "dirs": s.candidateDirs(), "canLoad": s.LoadBaseline != nil})
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// baselineLoad starts loading a bundle file as the baseline:
//
//	POST /api/baseline/load {"path": "/path/to/before.tar.zst"}
func (s *Server) baselineLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jerr(w, http.StatusMethodNotAllowed, "POST {\"path\": ...} to load a baseline")
		return
	}
	if !sameOrigin(r) {
		jerr(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if s.LoadBaseline == nil {
		jerr(w, http.StatusConflict, "this desk cannot load a baseline; restart it with --baseline FILE")
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		jerr(w, 400, "body must be {\"path\": \"/path/to/bundle.tar.zst\"}")
		return
	}
	p := expandHome(strings.Trim(strings.TrimSpace(body.Path), `"'`))
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		jerr(w, 400, "no such file: "+p)
		return
	case err != nil:
		jerr(w, 400, err.Error())
		return
	case !fi.Mode().IsRegular():
		jerr(w, 400, p+" is not a file")
		return
	case sameFile(p, s.openBundlePath()):
		jerr(w, 400, "that is the bundle already open; pick an earlier capture")
		return
	}
	if _, _, err := s.readHeader(p); err != nil {
		jerr(w, 400, "not a readable Flightbay bundle: "+err.Error())
		return
	}
	s.BaselineLoading(p)
	if err := s.LoadBaseline(p); err != nil {
		s.BaselineFailed(p, err)
		jerr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"loading": true, "path": p})
}
