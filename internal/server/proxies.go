package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/meshdiag"
	"github.com/tjorourke/flightbay/internal/store"
)

type diagFile struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type diagRow struct {
	Cluster   string         `json:"cluster"`
	Kind      string         `json:"kind"`
	Namespace string         `json:"namespace"`
	Pod       string         `json:"pod"`
	Component string         `json:"component"`
	Image     string         `json:"image"`
	Version   string         `json:"version"`
	Node      string         `json:"node"`
	Files     []diagFile     `json:"files"`
	Counts    map[string]int `json:"counts,omitempty"`
	Expiring  int            `json:"expiring"`
	Expired   int            `json:"expired"`
}

// diagCoverage indexes diagnostic coverage by cluster/kind/ns/pod.
func (s *Server) diagCoverage() map[string][]bundle.Coverage {
	out := map[string][]bundle.Coverage{}
	for _, c := range s.WS.Coverage() {
		if c.Type != bundle.CoverageDiagnostic {
			continue
		}
		kind := "proxies"
		if strings.Contains(c.ID, ".control-plane.") {
			kind = "control-plane"
		}
		k := c.Cluster + "/" + kind + "/" + c.Namespace + "/" + c.Pod
		out[k] = append(out[k], c)
	}
	return out
}

func (s *Server) diagRow(d store.Diagnostic, cov []bundle.Coverage, withCounts bool) diagRow {
	row := diagRow{Cluster: d.Cluster, Kind: d.Kind, Namespace: d.Namespace, Pod: d.Pod, Component: d.Component, Image: d.Image, Version: d.Version, Node: d.Node}
	seen := map[string]bool{}
	for _, c := range cov {
		f := diagFile{Name: c.Resource, State: c.State, Reason: c.Reason}
		if b, ok := d.Files[c.Resource]; ok {
			f.Bytes = len(b)
		}
		if row.Component == "" {
			row.Component = c.Container
		}
		seen[c.Resource] = true
		row.Files = append(row.Files, f)
	}
	for name, b := range d.Files {
		if !seen[name] {
			row.Files = append(row.Files, diagFile{Name: name, Bytes: len(b), State: bundle.StateComplete})
		}
	}
	sort.Slice(row.Files, func(i, j int) bool { return row.Files[i].Name < row.Files[j].Name })
	if row.Files == nil {
		row.Files = []diagFile{}
	}
	if withCounts && len(d.Files) > 0 {
		sum := meshdiag.Summarise(row.Component, d.Files, s.snapshot())
		row.Counts = sum.Counts
		for _, c := range sum.Certs {
			switch {
			case c.NotAfter.IsZero():
			case c.HoursLeft <= 0:
				row.Expired++
			case c.Root && c.DaysLeft < 30:
				row.Expiring++
			}
		}
	}
	return row
}

// proxies lists every captured component, including the ones whose
// collection failed or was excluded, so absence is never silent.
func (s *Server) proxies(w http.ResponseWriter, r *http.Request) {
	cluster := r.URL.Query().Get("cluster")
	cov := s.diagCoverage()
	var rows []diagRow
	done := map[string]bool{}
	for _, d := range s.WS.Diagnostics(cluster) {
		k := d.Cluster + "/" + d.Kind + "/" + d.Namespace + "/" + d.Pod
		done[k] = true
		rows = append(rows, s.diagRow(d, cov[k], true))
	}
	for k, cs := range cov {
		if done[k] || (cluster != "" && cs[0].Cluster != cluster) {
			continue
		}
		parts := strings.SplitN(k, "/", 4)
		rows = append(rows, s.diagRow(store.Diagnostic{Cluster: parts[0], Kind: parts[1], Namespace: parts[2], Pod: parts[3]}, cs, false))
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		return a.Cluster+"/"+a.Component+"/"+a.Namespace+"/"+a.Pod < b.Cluster+"/"+b.Component+"/"+b.Namespace+"/"+b.Pod
	})
	summaries := map[string]json.RawMessage{}
	for _, c := range s.WS.Clusters() {
		if raw := s.WS.DiagnosticSummary(c); raw != nil && (cluster == "" || c == cluster) {
			summaries[c] = raw
		}
	}
	pol := s.WS.Policy()
	collected := pol != nil && pol.PortForward && (pol.Has("proxies") || pol.Has("control-plane") || pol.Has("metrics"))
	if rows == nil {
		rows = []diagRow{}
	}
	writeJSON(w, 200, map[string]any{"components": rows, "summaries": summaries, "selected": collected, "loading": s.WS.State().Phase == store.PhaseLoading})
}

func (s *Server) diagFromQuery(w http.ResponseWriter, r *http.Request) *store.Diagnostic {
	q := r.URL.Query()
	d := s.WS.Diagnostic(q.Get("cluster"), q.Get("kind"), q.Get("namespace"), q.Get("pod"))
	if d == nil {
		jerr(w, http.StatusNotFound, "no diagnostics captured for this pod")
	}
	return d
}

// proxy returns the parsed summary of one component pod.
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	d := s.diagFromQuery(w, r)
	if d == nil {
		return
	}
	k := d.Cluster + "/" + d.Kind + "/" + d.Namespace + "/" + d.Pod
	row := s.diagRow(*d, s.diagCoverage()[k], false)
	writeJSON(w, 200, map[string]any{"component": row, "summary": meshdiag.Summarise(d.Component, d.Files, s.snapshot())})
}

// proxyFile returns one captured file as captured (already redacted).
func (s *Server) proxyFile(w http.ResponseWriter, r *http.Request) {
	d := s.diagFromQuery(w, r)
	if d == nil {
		return
	}
	name := r.URL.Query().Get("file")
	b, ok := d.Files[name]
	if !ok {
		jerr(w, http.StatusNotFound, "file not captured")
		return
	}
	ct := "text/plain; charset=utf-8"
	if strings.HasSuffix(name, ".json") {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(b)
}
