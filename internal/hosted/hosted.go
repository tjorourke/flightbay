// Package hosted runs flightbay-desk as a shared service for support
// engineers: many bundles, each opened in its own workspace under /w/<id>/,
// behind an SSO proxy that asserts who the user is.
//
// Security model:
//   - Identity comes only from trusted headers set by an authenticating proxy
//     (oauth2-proxy or Solo SSO) in front of this server. The listener must not
//     be reachable except through that proxy.
//   - Access is per support case. Members of the support groups can open every
//     case; an ACL file can grant named users or groups specific cases.
//   - The case is fixed when an upload is created, checked against the
//     uploader's access before any byte is accepted, and checked again against
//     the case recorded in bundle.json. A mismatch rejects the bundle.
//   - Bundles are stored as uploaded (age-encrypted when the customer used
//     --encrypt-to). Workspaces live in memory and are rebuilt on demand, so the
//     server holds no derived data it cannot recreate.
//   - There is no shell in hosted mode. A hosted shell is remote code
//     execution by design and needs a sandboxed pod per session.
//   - Every upload, open, download, deletion and denied request is written to
//     an append-only audit log.
package hosted

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/server"
	"github.com/tjorourke/flightbay/internal/store"
)

// Config configures a hosted desk.
type Config struct {
	DataDir       string        // bundles, metadata and the audit log
	UserHeader    string        // e.g. X-Forwarded-Email
	GroupsHeader  string        // e.g. X-Forwarded-Groups (comma separated)
	SupportGroups []string      // groups that may open every case
	ACLFile       string        // optional per-case grants (YAML)
	Retention     time.Duration // delete bundles this long after upload (0 = keep)
	IdleEvict     time.Duration // drop an unused workspace from memory after this
	Identities    []age.Identity
	MaxLogLines   int
	MaxBundle     int64 // largest accepted upload in bytes (0 = no limit)
	Region        string
}

// ACL grants cases to users or groups.
type ACL struct {
	Cases map[string]struct {
		Users  []string `json:"users"`
		Groups []string `json:"groups"`
	} `json:"cases"`
}

// Meta is stored as <id>.json next to each bundle.
type Meta struct {
	ID       string    `json:"id"`
	Case     string    `json:"case"`
	Uploader string    `json:"uploader"`
	Created  time.Time `json:"created"`
	Size     int64     `json:"size"`
	Complete bool      `json:"complete"` // upload finished
	File     string    `json:"file"`
	Status   string    `json:"status"` // uploading, loaded, verified, failed, rejected
	Error    string    `json:"error,omitempty"`
	Region   string    `json:"region,omitempty"`
}

type entry struct {
	mu       sync.Mutex
	cond     *sync.Cond
	meta     Meta
	aborted  bool
	ws       *store.Workspace
	srv      *server.Server
	handler  http.Handler
	cancel   context.CancelFunc
	lastUsed time.Time
}

// Hosted is the multi-bundle server.
type Hosted struct {
	cfg     Config
	mu      sync.Mutex
	entries map[string]*entry
	acl     ACL
	audit   *os.File
	auditMu sync.Mutex
	now     func() time.Time
}

var caseRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var idRe = regexp.MustCompile(`^[a-f0-9]{16}$`)

// New opens the data directory and reloads bundle metadata.
func New(cfg Config) (*Hosted, error) {
	if cfg.UserHeader == "" {
		return nil, errors.New("hosted mode needs --user-header: identity must come from the SSO proxy in front of this server")
	}
	if cfg.IdleEvict == 0 {
		cfg.IdleEvict = 30 * time.Minute
	}
	for _, d := range []string{"bundles", "work"} {
		if err := os.MkdirAll(filepath.Join(cfg.DataDir, d), 0o700); err != nil {
			return nil, err
		}
	}
	h := &Hosted{cfg: cfg, entries: map[string]*entry{}, now: time.Now}
	if cfg.ACLFile != "" {
		b, err := os.ReadFile(cfg.ACLFile)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(b, &h.acl); err != nil {
			return nil, fmt.Errorf("%s: %w", cfg.ACLFile, err)
		}
	}
	f, err := os.OpenFile(filepath.Join(cfg.DataDir, "audit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	h.audit = f
	metas, _ := filepath.Glob(filepath.Join(cfg.DataDir, "bundles", "*.json"))
	for _, p := range metas {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(b, &m) != nil || !idRe.MatchString(m.ID) {
			continue
		}
		if !m.Complete {
			m.Status = "uploading" // resumable after a restart
		}
		e := &entry{meta: m}
		e.cond = sync.NewCond(&e.mu)
		h.entries[m.ID] = e
	}
	return h, nil
}

// Close flushes the audit log.
func (h *Hosted) Close() error { return h.audit.Close() }

type user struct {
	name   string
	groups []string
}

func (h *Hosted) user(r *http.Request) (user, bool) {
	u := strings.TrimSpace(r.Header.Get(h.cfg.UserHeader))
	if u == "" {
		return user{}, false
	}
	var gs []string
	if h.cfg.GroupsHeader != "" {
		for _, g := range strings.Split(r.Header.Get(h.cfg.GroupsHeader), ",") {
			if g = strings.TrimSpace(g); g != "" {
				gs = append(gs, g)
			}
		}
	}
	return user{name: u, groups: gs}, true
}

func (h *Hosted) canAccess(u user, caseID string) bool {
	for _, g := range u.groups {
		for _, sg := range h.cfg.SupportGroups {
			if g == sg {
				return true
			}
		}
	}
	grant, ok := h.acl.Cases[caseID]
	if !ok {
		return false
	}
	for _, x := range grant.Users {
		if strings.EqualFold(x, u.name) {
			return true
		}
	}
	for _, g := range u.groups {
		for _, x := range grant.Groups {
			if g == x {
				return true
			}
		}
	}
	return false
}

func (h *Hosted) isSupport(u user) bool {
	for _, g := range u.groups {
		for _, sg := range h.cfg.SupportGroups {
			if g == sg {
				return true
			}
		}
	}
	return false
}

// Audit records one event.
func (h *Hosted) Audit(u, action, caseID, bundle, detail string, r *http.Request) {
	rec := map[string]string{"time": h.now().UTC().Format(time.RFC3339Nano), "user": u, "action": action, "case": caseID, "bundle": bundle, "detail": detail}
	if r != nil {
		rec["path"], rec["remote"] = r.URL.Path, r.Header.Get("X-Forwarded-For")
	}
	b, _ := json.Marshal(rec)
	h.auditMu.Lock()
	_, _ = h.audit.Write(append(b, '\n'))
	h.auditMu.Unlock()
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *Hosted) saveMeta(e *entry) {
	e.mu.Lock()
	b, _ := json.MarshalIndent(e.meta, "", "  ")
	id := e.meta.ID
	e.mu.Unlock()
	p := filepath.Join(h.cfg.DataDir, "bundles", id+".json")
	_ = os.WriteFile(p+".tmp", b, 0o600)
	_ = os.Rename(p+".tmp", p)
}

func (h *Hosted) bundlePath(e *entry) string {
	return filepath.Join(h.cfg.DataDir, "bundles", e.meta.File)
}

// growing reads a bundle file that may still be uploading: at the current end
// it waits for more bytes until the upload completes or is aborted. This is
// what lets the importer work while the upload is still running.
type growing struct {
	e   *entry
	f   *os.File
	off int64
	ctx context.Context
}

func (g *growing) Read(p []byte) (int, error) {
	for {
		n, err := g.f.ReadAt(p, g.off)
		if n > 0 {
			g.off += int64(n)
			return n, nil
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		g.e.mu.Lock()
		switch {
		case g.e.aborted || g.ctx.Err() != nil:
			g.e.mu.Unlock()
			return 0, errors.New("upload aborted")
		case g.e.meta.Complete && g.off >= g.e.meta.Size:
			g.e.mu.Unlock()
			return 0, io.EOF
		}
		g.e.cond.Wait()
		g.e.mu.Unlock()
	}
}

// open starts (or returns) the workspace for a bundle.
func (h *Hosted) open(e *entry) http.Handler {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastUsed = h.now()
	if e.handler != nil {
		return e.handler
	}
	ws := store.New()
	ws.SetMaxLogLines(h.cfg.MaxLogLines)
	tok := newID() + newID()
	srv := &server.Server{WS: ws, Token: tok, WorkDir: filepath.Join(h.cfg.DataDir, "work", e.meta.ID), Terminal: false}
	inner := srv.Handler()
	e.ws, e.srv = ws, srv
	// The workspace server authenticates with its own token; the hosted layer
	// has already authenticated the user and checked the case.
	e.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.Header.Set("Authorization", "Bearer "+tok)
		inner.ServeHTTP(w, r2)
	})
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go h.importBundle(ctx, e, ws)
	return e.handler
}

func (h *Hosted) importBundle(ctx context.Context, e *entry, ws *store.Workspace) {
	f, err := os.Open(h.bundlePath(e))
	if err != nil {
		h.setStatus(e, "failed", err.Error())
		return
	}
	defer f.Close()
	opts := importer.Options{Source: e.meta.File}
	opts.Reader.Identities = h.cfg.Identities
	// Check the case in bundle.json as soon as the header arrives.
	go func() {
		for i := 0; i < 600 && ctx.Err() == nil; i++ {
			if hd := ws.Header(); hd != nil {
				if hd.Case != e.meta.Case {
					h.Audit("-", "reject", e.meta.Case, e.meta.ID, fmt.Sprintf("bundle.json case %q does not match the upload case", hd.Case), nil)
					h.setStatus(e, "rejected", fmt.Sprintf("bundle.json names case %q, not %q", hd.Case, e.meta.Case))
					h.abort(e)
				}
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	err = importer.Run(ctx, &growing{e: e, f: f, ctx: ctx}, ws, opts)
	e.mu.Lock()
	rejected := e.meta.Status == "rejected"
	e.mu.Unlock()
	switch {
	case rejected:
	case err != nil:
		h.setStatus(e, "failed", err.Error())
	default:
		h.setStatus(e, "verified", "")
	}
}

func (h *Hosted) setStatus(e *entry, status, msg string) {
	e.mu.Lock()
	e.meta.Status, e.meta.Error = status, msg
	e.mu.Unlock()
	h.saveMeta(e)
}

func (h *Hosted) abort(e *entry) {
	e.mu.Lock()
	e.aborted = true
	if e.cancel != nil {
		e.cancel()
	}
	e.cond.Broadcast()
	e.mu.Unlock()
}

func (h *Hosted) get(id string) *entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entries[id]
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jerr(w http.ResponseWriter, code int, msg string) {
	jsonOut(w, code, map[string]string{"error": msg})
}

// Handler returns the hosted HTTP handler.
func (h *Hosted) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u, ok := h.user(r)
		if !ok {
			http.Error(w, "flightbay: no identity from the SSO proxy", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(indexHTML))
		case r.URL.Path == "/api/me":
			jsonOut(w, 200, map[string]any{"user": u.name, "groups": u.groups, "support": h.isSupport(u), "region": h.cfg.Region, "retentionDays": int(h.cfg.Retention.Hours() / 24)})
		case r.URL.Path == "/api/bundles" && r.Method == http.MethodGet:
			h.list(w, u)
		case r.URL.Path == "/api/uploads" && r.Method == http.MethodPost:
			h.createUpload(w, r, u)
		case strings.HasPrefix(r.URL.Path, "/api/uploads/"):
			h.upload(w, r, u, strings.TrimPrefix(r.URL.Path, "/api/uploads/"))
		case strings.HasPrefix(r.URL.Path, "/api/bundles/") && r.Method == http.MethodDelete:
			h.delete(w, r, u, strings.TrimPrefix(r.URL.Path, "/api/bundles/"))
		case strings.HasPrefix(r.URL.Path, "/w/"):
			h.workspace(w, r, u)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func (h *Hosted) list(w http.ResponseWriter, u user) {
	h.mu.Lock()
	var out []Meta
	for _, e := range h.entries {
		e.mu.Lock()
		m := e.meta
		e.mu.Unlock()
		if h.canAccess(u, m.Case) {
			out = append(out, m)
		}
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	if out == nil {
		out = []Meta{}
	}
	jsonOut(w, 200, out)
}

func (h *Hosted) createUpload(w http.ResponseWriter, r *http.Request, u user) {
	caseID := r.URL.Query().Get("case")
	name := filepath.Base(r.URL.Query().Get("name"))
	if !caseRe.MatchString(caseID) {
		jerr(w, 400, "a valid ?case= is required")
		return
	}
	if !h.canAccess(u, caseID) {
		h.Audit(u.name, "denied-upload", caseID, "", "", r)
		jerr(w, 403, "you do not have access to case "+caseID)
		return
	}
	ext := ".tar.zst"
	if strings.HasSuffix(name, ".age") {
		ext += ".age"
	}
	id := newID()
	e := &entry{meta: Meta{ID: id, Case: caseID, Uploader: u.name, Created: h.now().UTC(), File: id + ext, Status: "uploading", Region: h.cfg.Region}}
	e.cond = sync.NewCond(&e.mu)
	f, err := os.OpenFile(h.bundlePath(e), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	f.Close()
	h.mu.Lock()
	h.entries[id] = e
	h.mu.Unlock()
	h.saveMeta(e)
	h.Audit(u.name, "upload-start", caseID, id, name, r)
	jsonOut(w, 201, map[string]string{"id": id, "upload": "/api/uploads/" + id, "workspace": "/w/" + id + "/"})
}

// upload handles resumable chunks: HEAD returns Upload-Offset; PUT appends a
// chunk that must start at the current offset; Upload-Complete: true ends it.
func (h *Hosted) upload(w http.ResponseWriter, r *http.Request, u user, id string) {
	e := h.get(id)
	if e == nil || !h.canAccess(u, e.meta.Case) {
		h.Audit(u.name, "denied-upload", "", id, "", r)
		jerr(w, 404, "no such upload")
		return
	}
	e.mu.Lock()
	size, complete, aborted := e.meta.Size, e.meta.Complete, e.aborted
	e.mu.Unlock()
	w.Header().Set("Upload-Offset", strconv.FormatInt(size, 10))
	switch r.Method {
	case http.MethodHead, http.MethodGet:
		w.WriteHeader(200)
		return
	case http.MethodPut, http.MethodPatch:
	default:
		jerr(w, 405, "use PUT with Upload-Offset")
		return
	}
	if complete || aborted {
		jerr(w, 409, "upload already finished")
		return
	}
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off != size {
		jerr(w, 409, fmt.Sprintf("Upload-Offset must be %d", size))
		return
	}
	f, err := os.OpenFile(h.bundlePath(e), os.O_WRONLY, 0o600)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	defer f.Close()
	body := io.Reader(r.Body)
	if h.cfg.MaxBundle > 0 {
		body = io.LimitReader(r.Body, h.cfg.MaxBundle-size+1)
	}
	buf := make([]byte, 256<<10)
	pos := off
	started := false
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := f.WriteAt(buf[:n], pos); err != nil {
				jerr(w, 500, err.Error())
				return
			}
			pos += int64(n)
			e.mu.Lock()
			e.meta.Size = pos
			e.cond.Broadcast() // wake the importer
			e.mu.Unlock()
			if !started {
				started = true
				h.open(e) // start importing as soon as the first bytes land
			}
			if h.cfg.MaxBundle > 0 && pos > h.cfg.MaxBundle {
				h.setStatus(e, "rejected", "bundle larger than the server limit")
				h.abort(e)
				jerr(w, 413, "bundle too large")
				return
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// Interrupted: keep what arrived; the client resumes from Upload-Offset.
			h.saveMeta(e)
			return
		}
	}
	if r.Header.Get("Upload-Complete") == "true" {
		e.mu.Lock()
		e.meta.Complete = true
		e.cond.Broadcast()
		e.mu.Unlock()
		h.Audit(u.name, "upload-complete", e.meta.Case, id, strconv.FormatInt(pos, 10)+" bytes", r)
	}
	h.saveMeta(e)
	w.Header().Set("Upload-Offset", strconv.FormatInt(pos, 10))
	jsonOut(w, 200, map[string]any{"offset": pos, "complete": r.Header.Get("Upload-Complete") == "true"})
}

func (h *Hosted) delete(w http.ResponseWriter, r *http.Request, u user, id string) {
	e := h.get(id)
	if e == nil || !h.canAccess(u, e.meta.Case) || !h.isSupport(u) {
		h.Audit(u.name, "denied-delete", "", id, "", r)
		jerr(w, 404, "no such bundle")
		return
	}
	h.remove(e)
	h.Audit(u.name, "delete", e.meta.Case, id, "", r)
	jsonOut(w, 200, map[string]string{"deleted": id})
}

func (h *Hosted) remove(e *entry) {
	h.abort(e)
	h.mu.Lock()
	delete(h.entries, e.meta.ID)
	h.mu.Unlock()
	_ = os.Remove(h.bundlePath(e))
	_ = os.Remove(filepath.Join(h.cfg.DataDir, "bundles", e.meta.ID+".json"))
	_ = os.RemoveAll(filepath.Join(h.cfg.DataDir, "work", e.meta.ID))
}

func (h *Hosted) workspace(w http.ResponseWriter, r *http.Request, u user) {
	rest := strings.TrimPrefix(r.URL.Path, "/w/")
	id, sub, _ := strings.Cut(rest, "/")
	e := h.get(id)
	if e == nil || !h.canAccess(u, e.meta.Case) {
		h.Audit(u.name, "denied-open", "", id, "", r)
		http.NotFound(w, r) // do not reveal that another case's bundle exists
		return
	}
	if sub == "" && !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, "/w/"+id+"/", http.StatusFound)
		return
	}
	switch {
	case sub == "download":
		h.Audit(u.name, "download", e.meta.Case, id, "", r)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "flightbay-"+e.meta.Case+"-"+e.meta.File))
		http.ServeFile(w, r, h.bundlePath(e))
		return
	case sub == "api/terminal":
		jerr(w, 403, "the terminal is not available in hosted mode; download the bundle and use flightbay-desk open")
		return
	case sub == "" || sub == "index.html":
		h.Audit(u.name, "open", e.meta.Case, id, "", r)
	}
	handler := h.open(e)
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + sub
	r2.URL.RawPath = ""
	handler.ServeHTTP(w, r2)
}

// Sweep applies retention and evicts idle workspaces. Run it periodically.
func (h *Hosted) Sweep() {
	now := h.now()
	h.mu.Lock()
	var all []*entry
	for _, e := range h.entries {
		all = append(all, e)
	}
	h.mu.Unlock()
	for _, e := range all {
		e.mu.Lock()
		created, handler, last, complete := e.meta.Created, e.handler, e.lastUsed, e.meta.Complete
		e.mu.Unlock()
		if h.cfg.Retention > 0 && now.Sub(created) > h.cfg.Retention {
			h.remove(e)
			h.Audit("-", "retention-delete", e.meta.Case, e.meta.ID, fmt.Sprintf("uploaded %s", created.Format(time.RFC3339)), nil)
			continue
		}
		if handler != nil && complete && now.Sub(last) > h.cfg.IdleEvict {
			e.mu.Lock()
			if e.cancel != nil {
				e.cancel()
			}
			e.handler, e.ws, e.srv, e.cancel = nil, nil, nil, nil
			e.mu.Unlock()
		}
	}
}

const indexHTML = `<!doctype html><html lang="en-GB"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Flightbay · Cases</title>
<style>
body{margin:0;background:#0d1215;color:#c9d6de;font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
main{max-width:1100px;margin:0 auto;padding:32px 24px}h1{font-size:24px;margin:0 0 4px;color:#fff}p.sub{margin:0 0 24px;color:#8a9aa5}
.panel{background:#131a1e;border:1px solid #243038;border-radius:10px;padding:18px;margin-bottom:18px}
table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:9px 8px;border-bottom:1px solid #1f2a31}th{font-size:11px;letter-spacing:.06em;text-transform:uppercase;color:#7c8c97}
a{color:#72d6be}input,button{font:inherit;border-radius:6px;border:1px solid #2c3a43;background:#0f1519;color:#dde7ee;padding:8px 10px}button{cursor:pointer;background:#1d3a33;border-color:#2d5a4f}
.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center}.muted{color:#7c8c97}.ok{color:#72d6be}.bad{color:#f07178}.warn{color:#ffcb6b}
progress{width:260px}code{font-family:ui-monospace,Menlo,monospace}</style></head><body><main>
<h1>Flightbay cases</h1><p class="sub" id="who">Loading…</p>
<section class="panel"><h2 style="margin-top:0;font-size:16px">Upload a bundle</h2>
<div class="row"><input id="case" placeholder="Case number" size="14"><input type="file" id="file" accept=".zst,.age"><button id="go">Upload</button><progress id="prog" value="0" max="1" hidden></progress><span id="msg" class="muted"></span></div>
<p class="muted" style="margin:10px 0 0">The workspace opens as soon as the first bytes arrive; you can browse and search while the rest uploads. Interrupted uploads resume from where they stopped.</p></section>
<section class="panel"><table><thead><tr><th>Case</th><th>Uploaded</th><th>By</th><th>Size</th><th>Status</th><th></th></tr></thead><tbody id="rows"></tbody></table></section>
</main><script>
const $=s=>document.querySelector(s);const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
async function load(){const me=await (await fetch('api/me')).json();$('#who').textContent='Signed in as '+me.user+(me.support?' (support)':'')+(me.region?' · region '+me.region:'')+(me.retentionDays?' · bundles are deleted '+me.retentionDays+' days after upload':'');
const rows=await (await fetch('api/bundles')).json();$('#rows').innerHTML=rows.map(b=>'<tr><td><code>'+esc(b.case)+'</code></td><td>'+esc(b.created.slice(0,16).replace('T',' '))+' UTC</td><td>'+esc(b.uploader)+'</td><td>'+(b.size/1048576).toFixed(1)+' MiB</td><td class="'+({verified:'ok',failed:'bad',rejected:'bad',uploading:'warn'}[b.status]||'')+'">'+esc(b.status)+(b.error?' · '+esc(b.error):'')+'</td><td><a href="w/'+b.id+'/">Open</a> · <a href="w/'+b.id+'/download">Download</a></td></tr>').join('')||'<tr><td colspan="6" class="muted">No bundles you can access.</td></tr>';}
async function upload(){const f=$('#file').files[0],c=$('#case').value.trim();if(!f||!c){$('#msg').textContent='Choose a case and a file';return}
const r=await fetch('api/uploads?case='+encodeURIComponent(c)+'&name='+encodeURIComponent(f.name),{method:'POST'});const j=await r.json();if(!r.ok){$('#msg').textContent=j.error;return}
const url=j.upload.slice(1);$('#prog').hidden=false;let off=0,opened=false;const chunk=8<<20;
while(off<f.size){const end=Math.min(off+chunk,f.size);try{const pr=await fetch(url,{method:'PUT',headers:{'Upload-Offset':String(off),'Upload-Complete':end===f.size?'true':'false'},body:f.slice(off,end)});
if(pr.status===409){off=Number((await fetch(url,{method:'HEAD'})).headers.get('Upload-Offset'));continue}if(!pr.ok){$('#msg').textContent=(await pr.json()).error;return}off=end}catch(e){await new Promise(r=>setTimeout(r,2000));off=Number((await fetch(url,{method:'HEAD'})).headers.get('Upload-Offset'));continue}
$('#prog').value=off/f.size;$('#msg').textContent=(off/1048576).toFixed(1)+' of '+(f.size/1048576).toFixed(1)+' MiB';if(!opened){opened=true;$('#msg').innerHTML+=' · <a target="_blank" href="'+j.workspace.slice(1)+'">open workspace now</a>'}}
$('#msg').innerHTML='Uploaded · <a href="'+j.workspace.slice(1)+'">open workspace</a>';load()}
$('#go').onclick=upload;load();setInterval(load,5000);
</script></body></html>`
