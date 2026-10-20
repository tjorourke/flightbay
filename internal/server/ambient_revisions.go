package server

import (
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/diff"
	"github.com/tjorourke/flightbay/internal/store"
)

// flightbayWindow is how long before a revision was observed a Flightbay
// apply may have finished and still be credited with it. flightbaySkew allows
// for the watch event arriving before the apply record is stamped.
const (
	flightbayWindow = 30 * time.Second
	flightbaySkew   = 5 * time.Second
)

type ambientRevisionOut struct {
	ResourceVersion string `json:"resourceVersion"`
	Generation      int64  `json:"generation"`
	At              string `json:"at"`
	Event           string `json:"event"`
	Initial         bool   `json:"initial"`
	SpecHash        string `json:"specHash"`
	Diff            string `json:"diff"`
	YAML            string `json:"yaml"`
	FlightbayToken  string `json:"flightbayToken,omitempty"`
}

type ambientRevisionApply struct {
	Token      string `json:"token"`
	ApplyState string `json:"applyState"`
	At         string `json:"at"`
	Context    string `json:"context"`
	Op         string `json:"op"` // created, modified or deleted
	finished   time.Time
}

type ambientRevisionsOut struct {
	Mode      string                 `json:"mode"` // live or snapshot
	Ref       store.RevisionRef      `json:"ref"`
	Revisions []ambientRevisionOut   `json:"revisions"` // newest first
	Flightbay []ambientRevisionApply `json:"flightbay"` // newest first
}

// ambientRevisions serves the object page's History tab: the revisions the
// live mirror observed for one object, each with a diff to the one before,
// and Flightbay's own applies that touched exactly this object.
func (s *Server) ambientRevisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	if s.WS == nil {
		jerr(w, 404, "no workspace")
		return
	}
	q := r.URL.Query()
	cluster, kind, ns, name, group := q.Get("cluster"), q.Get("kind"), q.Get("namespace"), q.Get("name"), q.Get("group")
	if cluster == "" {
		if cs := s.WS.Clusters(); len(cs) == 1 {
			cluster = cs[0]
		}
	}
	if cluster == "" || kind == "" || name == "" {
		jerr(w, 400, "cluster, kind and name are required")
		return
	}
	if kind == "Namespace" {
		ns = ""
	}
	if !q.Has("group") {
		if o := ambient.Target(s.WS, cluster, kind, ns, name); o != nil {
			group = o.Group
		}
	}
	ref, revs, ok := s.WS.ObjectRevisions(cluster, group, kind, ns, name)
	if !ok {
		jerr(w, 404, kind+" "+ns+"/"+name+" has no recorded revisions in cluster "+cluster)
		return
	}
	out := ambientRevisionsOut{Mode: "live", Ref: ref, Revisions: []ambientRevisionOut{}, Flightbay: s.ambientRefApplies(ref)}
	if !s.WS.IsLive() {
		// A bundle has no watches: the captured state is the only revision.
		out.Mode = "snapshot"
		last := revs[len(revs)-1]
		last.Initial = true
		if h := s.WS.Header(); h != nil && !h.CaptureStart.IsZero() {
			last.At = h.CaptureStart
		}
		revs = []store.Revision{last}
	}
	prev := ""
	for i, rev := range revs {
		y := revisionYAML(rev.Object)
		o := ambientRevisionOut{ResourceVersion: rev.ResourceVersion, Generation: rev.Generation, At: rev.At.UTC().Format(time.RFC3339),
			Event: rev.Event, Initial: rev.Initial, SpecHash: rev.SpecHash, YAML: y}
		if i > 0 {
			to := y
			if rev.Event == "deleted" {
				to = "" // the object is gone: show every line removed
			}
			o.Diff = diff.Unified(prev, to, revisionLabel(revs[i-1]), revisionLabel(rev), 3)
		}
		o.FlightbayToken = creditApply(rev, out.Flightbay)
		out.Revisions = append(out.Revisions, o)
		prev = y
	}
	for i, j := 0, len(out.Revisions)-1; i < j; i, j = i+1, j-1 {
		out.Revisions[i], out.Revisions[j] = out.Revisions[j], out.Revisions[i]
	}
	writeJSON(w, 200, out)
}

func revisionYAML(obj map[string]any) string {
	if obj == nil {
		return ""
	}
	b, err := yaml.Marshal(obj)
	if err != nil {
		return ""
	}
	return string(b)
}

func revisionLabel(r store.Revision) string {
	if r.Event == "deleted" {
		return "deleted " + r.At.UTC().Format(time.RFC3339)
	}
	if r.ResourceVersion == "" {
		return r.At.UTC().Format(time.RFC3339)
	}
	return "resourceVersion " + r.ResourceVersion
}

// creditApply returns the token of the Flightbay apply that most plausibly
// produced rev: one that touched this ref with a matching operation and
// finished no more than flightbayWindow before the revision was observed.
func creditApply(rev store.Revision, applies []ambientRevisionApply) string {
	if rev.Initial {
		return ""
	}
	best, bestGap := "", time.Duration(-1)
	for _, a := range applies {
		if a.finished.IsZero() || (a.Op == "deleted") != (rev.Event == "deleted") {
			continue
		}
		gap := rev.At.Sub(a.finished)
		if gap < -flightbaySkew || gap > flightbayWindow {
			continue
		}
		if gap < 0 {
			gap = -gap
		}
		if bestGap < 0 || gap < bestGap {
			best, bestGap = a.Token, gap
		}
	}
	return best
}

// ambientRecordCluster is the workspace cluster an apply record ran
// against: the live cluster its context and server map to (the same check
// /api/ambient/contexts uses), else the cluster its plan was built for.
func (s *Server) ambientRecordCluster(rec *ambientRecord) string {
	if c := s.ambientLiveCluster(rec.Context, rec.Server); c != "" {
		return c
	}
	if rec.Plan != nil {
		return rec.Plan.Request.Cluster
	}
	return ""
}

func apiGroup(apiVersion string) string {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return ""
	}
	return gv.Group
}

// ambientRefApplies lists the Flightbay apply records that created, modified
// or deleted exactly ref: cluster, group, kind, namespace and name all match.
func (s *Server) ambientRefApplies(ref store.RevisionRef) []ambientRevisionApply {
	out := []ambientRevisionApply{}
	s.ambientS.mu.Lock()
	defer s.ambientS.mu.Unlock()
	if err := s.ambientRecords(); err != nil {
		return out
	}
	for _, rec := range s.ambientS.records {
		recCluster := s.ambientRecordCluster(rec)
		op := ""
		for _, c := range rec.Created {
			if recCluster == ref.Cluster && apiGroup(c.APIVersion) == ref.Group && c.Kind == ref.Kind && c.Namespace == ref.Namespace && c.Name == ref.Name {
				op = "created"
			}
		}
		for _, c := range rec.Modified {
			cl := c.Ref.Cluster
			if cl == "" {
				cl = recCluster
			}
			g := c.Ref.Group
			if g == "" && c.APIVersion != "" {
				g = apiGroup(c.APIVersion)
			}
			if cl == ref.Cluster && g == ref.Group && c.Ref.Kind == ref.Kind && c.Ref.Namespace == ref.Namespace && c.Ref.Name == ref.Name {
				op = "modified"
				if c.Delete {
					op = "deleted"
				}
			}
		}
		if op == "" {
			continue
		}
		at := rec.ApplyFinished
		if at.IsZero() {
			at = rec.ApplyStarted
		}
		if at.IsZero() {
			at = rec.Validated
		}
		a := ambientRevisionApply{Token: rec.Token, ApplyState: rec.ApplyState, Context: rec.Context, Op: op, finished: rec.ApplyFinished}
		if !at.IsZero() {
			a.At = at.UTC().Format(time.RFC3339)
		}
		if rec.ApplyState != "applied" && rec.ApplyState != "removed" {
			a.finished = time.Time{} // never applied, or failed and rolled back: no revision is credited to it
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].Token < out[j].Token
	})
	return out
}
