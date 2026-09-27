// Package server is the flightbay-desk HTTP server: the JSON API behind the
// web UI, the embedded UI itself, the fake kube-apiserver mounted at /k8s/
// and a real terminal over a websocket.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"github.com/tjorourke/flightbay/internal/diff"
	"github.com/tjorourke/flightbay/internal/mcp"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/analyze"
	"github.com/tjorourke/flightbay/internal/apiserver"
	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
	"github.com/tjorourke/flightbay/web"
)

// CookieName holds the per-process session token in the browser.
const CookieName = "flightbay_token"

// Server serves one workspace.
type Server struct {
	WS         *store.Workspace
	Token      string
	WorkDir    string
	BaseURL    string // e.g. http://127.0.0.1:8484, used in the kubeconfig
	Terminal   bool
	Shell      string
	DeskBinary string       // path to flightbay-desk, used by the fbq/fbfind helpers
	Reload     func() error // re-reads the bundle to load deferred categories; nil if unsupported
	// KubeURL and KubeCA point kubeconfigs at the TLS listener serving the
	// snapshot API. kubectl only sends kubeconfig tokens over TLS.
	KubeURL string
	KubeCA  []byte

	once sync.Once
	api  http.Handler
	term *termSession
	mu   sync.Mutex
	base baselineState
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jerr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) authed(r *http.Request) bool {
	check := func(v string) bool { return v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(s.Token)) == 1 }
	if check(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		return true
	}
	if c, err := r.Cookie(CookieName); err == nil && check(c.Value) {
		return true
	}
	return false
}

// Close stops the terminal session.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term != nil {
		s.term.close()
	}
}

// KubeconfigPath is where Prepare writes the kubeconfig.
func (s *Server) KubeconfigPath() string { return filepath.Join(s.WorkDir, "kubeconfig") }

// Prepare writes the kubeconfig and the shell helpers into the work directory.
func (s *Server) Prepare() error {
	if err := os.MkdirAll(filepath.Join(s.WorkDir, "bin"), 0o700); err != nil {
		return err
	}
	kubeURL := s.KubeURL
	if kubeURL == "" {
		kubeURL = strings.TrimRight(s.BaseURL, "/") + "/k8s"
	}
	kc, err := apiserver.Kubeconfig(kubeURL, s.Token, s.WS.Clusters(), s.KubeCA)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.KubeconfigPath(), kc, 0o600); err != nil {
		return err
	}
	helpers := map[string]string{"fbq": "query", "fbfind": "findings", "fbpc": "proxy-config", "fbsearch": "search", "fbsql": "sql"}
	for name, sub := range helpers {
		script := fmt.Sprintf("#!/bin/sh\n# Flightbay helper: runs `flightbay-desk %s` against this workspace.\nexec %q %s \"$@\"\n", sub, s.DeskBinary, sub)
		if err := os.WriteFile(filepath.Join(s.WorkDir, "bin", name), []byte(script), 0o700); err != nil {
			return err
		}
	}
	return nil
}

// ShellEnv is the environment for shells that talk to this workspace.
func (s *Server) ShellEnv() []string {
	snap := apiserver.SnapshotTime(s.WS).Format("2006-01-02T15:04Z")
	label := "snapshot"
	if h := s.WS.Header(); h != nil && h.Case != "" {
		label = "case " + h.Case
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "KUBECONFIG=") || strings.HasPrefix(kv, "PS1=") || strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"KUBECONFIG="+s.KubeconfigPath(),
		"PATH="+filepath.Join(s.WorkDir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FLIGHTBAY_URL="+strings.TrimRight(s.BaseURL, "/"),
		"FLIGHTBAY_TOKEN="+s.Token,
		"TERM=xterm-256color",
		fmt.Sprintf(`PS1=\[\e[36m\]flightbay\[\e[0m\] %s \[\e[33m\]@%s\[\e[0m\] \W $ `, label, snap),
	)
}

// KubeHandler serves the snapshot API at the root, for the TLS listener.
func (s *Server) KubeHandler() http.Handler {
	return (&apiserver.Server{WS: s.WS, Token: s.Token}).Handler()
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler {
	s.once.Do(func() {
		s.api = http.StripPrefix("/k8s", (&apiserver.Server{WS: s.WS, Token: s.Token}).Handler())
	})
	static, _ := fs.Sub(web.FS, ".")
	files := http.FileServer(http.FS(static))
	mux := http.NewServeMux()
	mux.Handle("/k8s/", s.api)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("token"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(s.Token)) != 1 {
				http.Error(w, "flightbay: invalid token. Open the URL printed by flightbay-desk.", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: CookieName, Value: s.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if !s.authed(r) {
			http.Error(w, "flightbay: not signed in. Open the URL printed by flightbay-desk (it contains a one-time token).", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := web.FS.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/static")
		files.ServeHTTP(w, r2)
	})
	api := func(path string, h http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if !s.authed(r) {
				jerr(w, http.StatusUnauthorized, "not signed in")
				return
			}
			h(w, r)
		})
	}
	api("/api/workspace", s.workspace)
	api("/api/state/stream", s.stateStream)
	api("/api/resources", s.resources)
	api("/api/objects", s.objects)
	api("/api/object", s.object)
	api("/api/logs/search", s.logSearch)
	api("/api/logs/context", s.logContext)
	api("/api/findings", s.findings)
	api("/api/timeline", s.timeline)
	api("/api/diff", s.diff)
	api("/api/search", s.search)
	api("/api/objects/search", s.objectSearch)
	api("/api/sql", s.sqlQuery)
	api("/api/coverage", s.coverage)
	api("/api/terminal", s.terminal)
	api("/api/load", s.load)
	api("/api/proxies", s.proxies)
	api("/api/proxy", s.proxy)
	api("/api/proxy/file", s.proxyFile)
	api("/api/baseline", s.baselineDiff)
	// MCP for agents that connect over HTTP (kagent RemoteMCPServer, hosted
	// desks). Same token as the API; tools call back into this server.
	api("/mcp", func(w http.ResponseWriter, r *http.Request) {
		m := &mcp.Server{BaseURL: "http://workspace", Token: s.Token, Version: "desk", HTTP: &http.Client{Transport: inProcess{mux}}}
		m.HandleHTTP(w, r)
	})
	return securityHeaders(mux)
}

// inProcess serves requests with a handler directly, so MCP tools read the
// workspace without a network hop (and work behind a hosted path prefix).
type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) snapshot() time.Time { return apiserver.SnapshotTime(s.WS) }

func (s *Server) workspace(w http.ResponseWriter, r *http.Request) {
	h := s.WS.Header()
	out := map[string]any{"state": s.WS.State(), "snapshot": s.snapshot(), "terminal": s.Terminal, "kubeconfig": s.KubeconfigPath()}
	if h != nil {
		out["case"], out["bundleId"], out["collector"], out["captureStart"] = h.Case, h.BundleID, h.Collector, h.CaptureStart
		out["contexts"], out["planned"], out["unavailable"] = h.Contexts, h.Planned, h.Unavailable
		out["policy"] = s.WS.Policy()
	}
	if m := s.WS.Manifest(); m != nil {
		out["captureEnd"], out["complete"], out["collectionErrors"] = m.CaptureEnd, m.Complete, m.Errors
	}
	findings := analyze.Run(s.WS)
	fc := map[string]int{"critical": 0, "warning": 0, "info": 0, "total": len(findings)}
	perCluster := map[string]int{}
	for _, f := range findings {
		fc[f.Severity]++
		if f.Severity != analyze.Info {
			perCluster[f.Cluster]++
		}
	}
	out["findings"] = fc
	var top []analyze.Finding
	for _, f := range findings {
		if len(top) < 5 && f.Severity != analyze.Info {
			top = append(top, f)
		}
	}
	out["topFindings"] = top
	var clusters []map[string]any
	for _, c := range s.WS.Clusters() {
		var ver struct {
			GitVersion string `json:"gitVersion"`
			Platform   string `json:"platform"`
		}
		_ = json.Unmarshal(s.WS.Version(c), &ver)
		pods := s.WS.List(c, "", "pods", "", nil)
		running := 0
		nss := map[string]bool{}
		for _, p := range pods {
			if st, _ := p.Obj["status"].(map[string]any); st != nil && st["phase"] == "Running" {
				running++
			}
		}
		objs := s.WS.AllObjects(c)
		for _, o := range objs {
			if o.Namespace != "" {
				nss[o.Namespace] = true
			}
		}
		gaps := 0
		for _, cv := range s.WS.Coverage() {
			if cv.Cluster == c && cv.State != bundle.StateComplete {
				gaps++
			}
		}
		clusters = append(clusters, map[string]any{"name": c, "version": ver.GitVersion, "platform": ver.Platform, "pods": len(pods), "podsRunning": running,
			"nodes": len(s.WS.List(c, "", "nodes", "", nil)), "objects": len(objs), "namespaces": len(nss), "findings": perCluster[c], "gaps": gaps})
	}
	out["clusters"] = clusters
	var red struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(s.WS.Redaction(), &red)
	out["redactionTotal"] = red.Total
	writeJSON(w, 200, out)
}

func (s *Server) stateStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		jerr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch, cancel := s.WS.Subscribe()
	defer cancel()
	send := func() bool {
		b, _ := json.Marshal(s.WS.State())
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send() {
		return
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			time.Sleep(150 * time.Millisecond) // coalesce bursts of commits
			if !send() {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *Server) clustersFor(c string) []string {
	if c != "" {
		return []string{c}
	}
	return s.WS.Clusters()
}

var stateRank = map[string]int{store.StateFailed: 0, store.StateTruncated: 1, store.StateLoading: 2, store.StateExcluded: 3, store.StateNotCollected: 4, store.StateComplete: 5}

func (s *Server) resources(w http.ResponseWriter, r *http.Request) {
	type row struct {
		bundle.APIResource
		Name   string `json:"name"`
		Count  int    `json:"count"`
		State  string `json:"state"`
		Reason string `json:"reason,omitempty"`
	}
	agg := map[string]*row{}
	for _, c := range s.clustersFor(r.URL.Query().Get("cluster")) {
		for _, res := range s.WS.Resources(c) {
			key := res.Name()
			st := s.WS.CollectionState(c, res.Group, res.Resource, res.Kind, res.Namespaced)
			rw, ok := agg[key]
			if !ok {
				rw = &row{APIResource: res, Name: key, State: st.State, Reason: st.Reason}
				agg[key] = rw
			} else if stateRank[st.State] < stateRank[rw.State] {
				rw.State, rw.Reason = st.State, st.Reason
			}
			rw.Count += len(s.WS.List(c, res.Group, res.Resource, "", nil))
		}
	}
	out := make([]*row, 0, len(agg))
	for _, v := range agg {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, 200, out)
}

func (s *Server) objects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	group, resource, ns, filter := q.Get("group"), q.Get("resource"), q.Get("namespace"), strings.ToLower(q.Get("q"))
	type row struct {
		Cluster   string `json:"cluster"`
		Group     string `json:"group"`
		Resource  string `json:"resource"`
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Cells     []any  `json:"cells"`
	}
	var cols []string
	rows := []row{}
	states := map[string]store.Collection{}
	for _, c := range s.clustersFor(q.Get("cluster")) {
		res, ok := s.WS.FindResource(c, resource)
		if group != "" || resource != "" {
			found := false
			for _, rr := range s.WS.Resources(c) {
				if rr.Group == group && rr.Resource == resource {
					res, found = rr, true
				}
			}
			ok = found
		}
		if !ok {
			continue
		}
		states[c] = s.WS.CollectionState(c, res.Group, res.Resource, res.Kind, res.Namespaced)
		names, cells := apiserver.Columns(res, s.WS.CRD(c, res.Group, res.Resource), s.snapshot())
		cols = names
		for _, o := range s.WS.List(c, res.Group, res.Resource, ns, nil) {
			cl := cells(o.Obj)
			if filter != "" {
				hay := strings.ToLower(o.Namespace + " " + fmt.Sprint(cl...) + " " + labelsString(o.Labels()))
				ok := true
				for _, word := range strings.Fields(filter) {
					ok = ok && strings.Contains(hay, strings.TrimPrefix(word, "label:"))
				}
				if !ok {
					continue
				}
			}
			rows = append(rows, row{Cluster: c, Group: o.Group, Resource: o.Resource, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Cells: cl})
		}
	}
	if cols == nil {
		cols = []string{"Name", "Age"}
	}
	writeJSON(w, 200, map[string]any{"columns": cols, "rows": rows, "states": states, "revision": s.WS.Revision()})
}

func labelsString(l map[string]string) string {
	var parts []string
	for k, v := range l {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func (s *Server) object(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	o := s.WS.Get(q.Get("cluster"), q.Get("group"), q.Get("resource"), q.Get("namespace"), q.Get("name"))
	if o == nil {
		jerr(w, 404, "object not found in this snapshot")
		return
	}
	y, _ := yaml.Marshal(o.Obj)
	j, _ := json.MarshalIndent(o.Obj, "", "  ")
	var events []map[string]any
	for _, e := range s.WS.Events(o.Cluster) {
		io, _ := e.Obj["involvedObject"].(map[string]any)
		if io["name"] == o.Name && io["kind"] == o.Kind && (io["namespace"] == o.Namespace || o.Namespace == "") {
			events = append(events, e.Obj)
		}
	}
	var streams []string
	if o.Group == "" && o.Resource == "pods" {
		streams = s.WS.Streams(o.Cluster, o.Namespace, o.Name)
	}
	writeJSON(w, 200, map[string]any{"object": o, "yaml": string(y), "json": string(j), "related": related(s.WS, o), "events": events, "streams": streams,
		"collection": s.WS.CollectionState(o.Cluster, o.Group, o.Resource, o.Kind, o.Namespaced)})
}

func (s *Server) logSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.LogFilter{Query: q.Get("q"), Cluster: q.Get("cluster"), Namespace: q.Get("namespace"), Pod: q.Get("pod"), Container: q.Get("container"), Level: q.Get("level"), Stream: q.Get("stream")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	if v := q.Get("from"); v != "" {
		f.From, _ = time.Parse(time.RFC3339Nano, v)
	}
	if v := q.Get("to"); v != "" {
		f.To, _ = time.Parse(time.RFC3339Nano, v)
	}
	res, err := s.WS.SearchLogs(f)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) logContext(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 {
		n = 5
	}
	writeJSON(w, 200, s.WS.LogContext(id, n))
}

func (s *Server) findings(w http.ResponseWriter, r *http.Request) {
	c := r.URL.Query().Get("cluster")
	out := []analyze.Finding{}
	for _, f := range analyze.Run(s.WS) {
		if c == "" || f.Cluster == c {
			out = append(out, f)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	type side struct {
		Cluster string `json:"cluster"`
		Found   bool   `json:"found"`
		YAML    string `json:"yaml"`
	}
	var out []side
	for _, c := range s.WS.Clusters() {
		o := s.WS.Get(c, q.Get("group"), q.Get("resource"), q.Get("namespace"), q.Get("name"))
		if o == nil {
			out = append(out, side{Cluster: c})
			continue
		}
		clean := diff.Clean(o.Obj)
		y, _ := yaml.Marshal(clean)
		out = append(out, side{Cluster: c, Found: true, YAML: string(y)})
	}
	writeJSON(w, 200, map[string]any{"clusters": out})
}

// search is the quick jump used by the ⌘K palette: the same engine as the
// Search page, limited to the best matches.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := []map[string]string{}
	if q == "" {
		writeJSON(w, 200, out)
		return
	}
	res, err := s.WS.SearchObjects(q, "", 30)
	if err != nil {
		writeJSON(w, 200, out)
		return
	}
	for _, h := range res.Results {
		out = append(out, map[string]string{"cluster": h.Cluster, "group": h.Group, "resource": h.Resource, "kind": h.Kind, "namespace": h.Namespace, "name": h.Name, "match": h.Match, "snippet": h.Snippet})
	}
	writeJSON(w, 200, out)
}

// objectSearch serves the Search page.
func (s *Server) objectSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := s.WS.SearchObjects(q.Get("q"), q.Get("cluster"), limit)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// sqlQuery runs one read-only SQL statement against the workspace database.
func (s *Server) sqlQuery(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if r.Method == http.MethodPost {
		var body struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			jerr(w, 400, "body must be {\"query\": \"SELECT ...\"}")
			return
		}
		query = body.Query
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.WS.SQL(query, limit)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) coverage(w http.ResponseWriter, r *http.Request) {
	var red any
	_ = json.Unmarshal(s.WS.Redaction(), &red)
	var log []any
	for _, l := range s.WS.CollectionLog() {
		var v any
		if json.Unmarshal(l, &v) == nil {
			log = append(log, v)
		}
	}
	var unavailable []string
	if h := s.WS.Header(); h != nil {
		unavailable = h.Unavailable
	}
	writeJSON(w, 200, map[string]any{"coverage": s.WS.Coverage(), "collectionLog": log, "redaction": red, "state": s.WS.State(), "unavailable": unavailable, "policy": s.WS.Policy()})
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	return strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://") == r.Host
}

func (s *Server) load(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jerr(w, http.StatusMethodNotAllowed, "POST to load deferred categories")
		return
	}
	if !sameOrigin(r) {
		jerr(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if s.Reload == nil {
		jerr(w, http.StatusConflict, "this workspace was opened from a stream; deferred data cannot be re-read")
		return
	}
	go func() { _ = s.Reload() }()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "loading deferred categories"})
}
