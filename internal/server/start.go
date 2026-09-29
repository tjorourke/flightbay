package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/age"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/scan"
	"github.com/tjorourke/flightbay/web"
)

// StartServer serves the start page: what flightbay ui shows when it is
// given nothing to open. There is no workspace yet, so none of the workspace
// APIs exist here. When the user picks something, Launch replaces this
// process with the desk for it, on the same address and with the same token.
type StartServer struct {
	Token   string
	WorkDir string // uploads, clones and scanned bundles go here; the next desk uses it as its workspace
	// Dirs are searched for recent bundles (default: the working directory
	// and ~/Downloads). WorkDir/uploads is always searched.
	Dirs []string
	// Identities open encrypted bundles given with -i when the start page
	// was launched.
	Identities []age.Identity
	// MaxUpload caps an uploaded bundle, in bytes (default 8 GiB).
	MaxUpload int64
	// Version goes into the header of scanned bundles.
	Version string
	// LivePlan returns the dry-run plan and RBAC for a live request,
	// as flightbay-desk live --dry-run --json prints it.
	LivePlan func(ctx context.Context, req LiveRequest) (json.RawMessage, error)
	// LiveAccess asks the cluster whether the context's credentials can read
	// cluster-scoped objects and port-forward, so the start page can turn
	// those options on by default and off, with the reason, when they cannot.
	LiveAccess func(ctx context.Context, req LiveRequest) (LiveAccess, error)
	// Launch hands over to the desk for an action. It is called after the
	// response has been sent and does not return on success.
	Launch func(a StartAction) error

	launching atomic.Bool
	scanMu    sync.Mutex
}

// LiveRequest is the Live card's form.
type LiveRequest struct {
	Kubeconfig        string `json:"kubeconfig,omitempty"`
	Context           string `json:"context"`
	Namespaces        string `json:"namespaces,omitempty"`        // comma separated, globs allowed; empty = all
	ExcludeNamespaces string `json:"excludeNamespaces,omitempty"` // comma separated, globs allowed
	ClusterScoped     bool   `json:"clusterScoped,omitempty"`     // --cluster-scoped
	SoloDiagnostics   bool   `json:"soloDiagnostics,omitempty"`   // --solo-diagnostics
}

// LiveAccess is what the context's credentials allow of the live options.
// A reason is set when the option is not allowed.
type LiveAccess struct {
	ClusterScoped      bool   `json:"clusterScoped"`
	ClusterScopedWhy   string `json:"clusterScopedWhy,omitempty"`
	SoloDiagnostics    bool   `json:"soloDiagnostics"`
	SoloDiagnosticsWhy string `json:"soloDiagnosticsWhy,omitempty"`
}

// ScanRequest is the Repo card's form.
type ScanRequest struct {
	Source    string   `json:"source"` // a local directory or a Git URL
	Values    []string `json:"values,omitempty"`
	Set       []string `json:"set,omitempty"`
	Overlay   string   `json:"overlay,omitempty"`
	Env       string   `json:"env,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Release   string   `json:"release,omitempty"`
}

// StartAction is what the start page hands over to.
type StartAction struct {
	Kind     string       `json:"kind"`               // "open", "review" or "live"
	Path     string       `json:"path,omitempty"`     // the bundle, for open and review
	Identity string       `json:"identity,omitempty"` // an age identity file for an encrypted bundle
	Live     *LiveRequest `json:"live,omitempty"`
	// Scanned is set when Path is a bundle the start page just scanned.
	Scanned bool `json:"scanned,omitempty"`
}

func authedWith(r *http.Request, token string) bool {
	check := func(v string) bool { return v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(token)) == 1 }
	if check(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		return true
	}
	for _, c := range r.Cookies() {
		if (c.Name == cookieName(r) || c.Name == CookieName) && check(c.Value) {
			return true
		}
	}
	return false
}

func serveIcons(mux *http.ServeMux) {
	for _, f := range []string{"favicon.ico", "favicon.svg", "favicon-32.png", "apple-touch-icon.png"} {
		name := f
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
			b, err := web.FS.ReadFile(name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
			w.Header().Set("Cache-Control", "max-age=86400")
			_, _ = w.Write(b)
		})
	}
}

// Handler serves the start page, its static files, /docs and /api/start/.
func (s *StartServer) Handler() http.Handler {
	mux := http.NewServeMux()
	serveIcons(mux)
	docs := (&Server{}).docs
	mux.HandleFunc("/docs", docs)
	mux.HandleFunc("/docs/", docs)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("token"); t != "" {
			if subtle.ConstantTimeCompare([]byte(t), []byte(s.Token)) != 1 {
				http.Error(w, "flightbay: invalid token. Open the URL printed by flightbay ui.", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: s.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if !authedWith(r, s.Token) {
			http.Error(w, "flightbay: not signed in. Open the URL printed by flightbay ui (it contains a one-time token).", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := web.FS.ReadFile("start.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	static := http.FileServer(http.FS(web.FS))
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		if !authedWith(r, s.Token) {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/static")
		static.ServeHTTP(w, r2)
	})
	api := func(p string, post bool, h http.HandlerFunc) {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			if !authedWith(r, s.Token) {
				jerr(w, http.StatusUnauthorized, "not signed in")
				return
			}
			if post {
				if r.Method != http.MethodPost {
					jerr(w, http.StatusMethodNotAllowed, "POST only")
					return
				}
				if !sameOrigin(r) {
					jerr(w, http.StatusForbidden, "cross-origin request refused")
					return
				}
			}
			h(w, r)
		})
	}
	api("/api/start", false, s.info)
	api("/api/start/contexts", false, s.contexts)
	api("/api/start/recent", false, s.recent)
	api("/api/start/live-plan", true, s.livePlan)
	api("/api/start/live", true, s.live)
	api("/api/start/live-access", true, s.liveAccess)
	api("/api/start/upload", true, s.upload)
	api("/api/start/open", true, s.open)
	api("/api/start/scan", true, s.scan)
	// No workspace: the desk's APIs answer 404 here, so the page's poll of
	// /api/workspace only succeeds once the next desk is serving.
	return securityHeaders(mux)
}

func (s *StartServer) info(w http.ResponseWriter, _ *http.Request) {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	writeJSON(w, 200, map[string]any{"start": true, "workdir": s.WorkDir, "cwd": cwd, "home": home, "dirs": s.recentDirs(),
		"identities": len(s.Identities), "maxUpload": s.maxUpload(), "version": s.Version})
}

func (s *StartServer) maxUpload() int64 {
	if s.MaxUpload > 0 {
		return s.MaxUpload
	}
	return 8 << 30
}

// ---- Live ----

type kubeContext struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Server    string `json:"server,omitempty"`
	User      string `json:"user,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// KubeContexts lists the contexts in a kubeconfig ("" = $KUBECONFIG or
// ~/.kube/config), the files read and the current context.
func KubeContexts(kubeconfig string) (ctxs []kubeContext, current string, files []string, err error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = expandHome(kubeconfig)
		files = []string{rules.ExplicitPath}
	} else {
		for _, f := range rules.GetLoadingPrecedence() {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
	}
	raw, err := rules.Load()
	if err != nil {
		return nil, "", files, err
	}
	for name, c := range raw.Contexts {
		kc := kubeContext{Name: name, Cluster: c.Cluster, User: c.AuthInfo, Namespace: c.Namespace}
		if cl := raw.Clusters[c.Cluster]; cl != nil {
			kc.Server = cl.Server
		}
		ctxs = append(ctxs, kc)
	}
	sort.Slice(ctxs, func(i, j int) bool { return ctxs[i].Name < ctxs[j].Name })
	return ctxs, raw.CurrentContext, files, nil
}

func (s *StartServer) contexts(w http.ResponseWriter, r *http.Request) {
	kc := strings.TrimSpace(r.URL.Query().Get("kubeconfig"))
	ctxs, cur, files, err := KubeContexts(kc)
	out := map[string]any{"contexts": ctxs, "current": cur, "files": files}
	if ctxs == nil {
		out["contexts"] = []kubeContext{}
	}
	if err != nil {
		out["error"] = err.Error()
	} else if len(ctxs) == 0 {
		out["error"] = "the kubeconfig has no contexts"
	}
	writeJSON(w, 200, out)
}

func (s *StartServer) readLive(w http.ResponseWriter, r *http.Request) (*LiveRequest, bool) {
	var req LiveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jerr(w, 400, "body must be a JSON live request")
		return nil, false
	}
	req.Context = strings.TrimSpace(req.Context)
	req.Kubeconfig = expandHome(strings.TrimSpace(req.Kubeconfig))
	req.Namespaces = normaliseList(req.Namespaces)
	req.ExcludeNamespaces = normaliseList(req.ExcludeNamespaces)
	ctxs, cur, _, err := KubeContexts(req.Kubeconfig)
	if err != nil {
		jerr(w, 400, "cannot read the kubeconfig: "+err.Error())
		return nil, false
	}
	if req.Context == "" {
		req.Context = cur
	}
	for _, c := range ctxs {
		if c.Name == req.Context {
			return &req, true
		}
	}
	jerr(w, 400, fmt.Sprintf("context %q is not in the kubeconfig", req.Context))
	return nil, false
}

// normaliseList turns "a, b ,c" or one per line into "a,b,c".
func normaliseList(v string) string {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == '\t' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}

func (s *StartServer) livePlan(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readLive(w, r)
	if !ok {
		return
	}
	if s.LivePlan == nil {
		jerr(w, 501, "this start page cannot plan live mode")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	plan, err := s.LivePlan(ctx, *req)
	if err != nil {
		jerr(w, 502, fmt.Sprintf("%s: %v", req.Context, err))
		return
	}
	writeJSON(w, 200, map[string]any{"request": req, "plan": plan})
}

func (s *StartServer) liveAccess(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readLive(w, r)
	if !ok {
		return
	}
	if s.LiveAccess == nil {
		jerr(w, 501, "this start page cannot check access")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	a, err := s.LiveAccess(ctx, *req)
	if err != nil {
		jerr(w, 502, fmt.Sprintf("%s: %v", req.Context, err))
		return
	}
	writeJSON(w, 200, a)
}

func (s *StartServer) live(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readLive(w, r)
	if !ok {
		return
	}
	s.handOff(w, StartAction{Kind: "live", Live: req})
}

// ---- Files ----

func (s *StartServer) uploadDir() string { return filepath.Join(s.WorkDir, "uploads") }

func (s *StartServer) recentDirs() []string {
	dirs := s.Dirs
	if dirs == nil {
		if wd, err := os.Getwd(); err == nil {
			dirs = append(dirs, wd)
		}
		if h, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(h, "Downloads"))
		}
	}
	return append(append([]string{}, dirs...), s.uploadDir())
}

func (s *StartServer) recent(w http.ResponseWriter, _ *http.Request) {
	out := listBundles(s.recentDirs(), []string{"flightbay-*.tar.zst", "*.tar.zst.age"}, s.Identities, nil)
	writeJSON(w, 200, map[string]any{"files": out, "dirs": s.recentDirs()})
}

// bundleName is the file extension check for uploads and paths.
func bundleName(name string) bool {
	return strings.HasSuffix(name, ".tar.zst") || strings.HasSuffix(name, ".tar.zst.age") || strings.HasSuffix(name, ".age")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// upload streams a bundle from the browser into WorkDir/uploads:
//
//	POST /api/start/upload?name=flightbay-X.tar.zst   (body: the file)
func (s *StartServer) upload(w http.ResponseWriter, r *http.Request) {
	name := unsafeName.ReplaceAllString(filepath.Base(r.URL.Query().Get("name")), "_")
	if name == "" || strings.HasPrefix(name, ".") || !bundleName(name) {
		jerr(w, 400, "only Flightbay bundles can be uploaded: a .tar.zst or .tar.zst.age file")
		return
	}
	max := s.maxUpload()
	if r.ContentLength > max {
		jerr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than the %d MiB limit; open it by path instead", max>>20))
		return
	}
	if err := os.MkdirAll(s.uploadDir(), 0o700); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	final := filepath.Join(s.uploadDir(), name)
	part := final + ".partial"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, max))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			jerr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the file is larger than the %d MiB limit; open it by path instead", max>>20))
			return
		}
		jerr(w, 400, "upload failed: "+err.Error())
		return
	}
	h, enc, herr := readBundleHeader(part, s.Identities)
	if herr != nil && !enc {
		os.Remove(part)
		jerr(w, 400, "not a Flightbay bundle: "+herr.Error())
		return
	}
	if err := os.Rename(part, final); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	c := candidate{Path: final, Name: name, Dir: s.uploadDir(), Size: n, Modified: time.Now().UTC(), Encrypted: enc}
	if h != nil {
		c.Case, c.BundleID, c.Captured, c.Reviewed, c.Source = h.Case, h.BundleID, h.CaptureStart, h.Review != nil, h.Source
		for _, ctx := range h.Contexts {
			c.Clusters = append(c.Clusters, bundle.SafeID(ctx))
		}
	} else {
		c.Unreadable = herr.Error()
	}
	writeJSON(w, 200, map[string]any{"file": c})
}

func loadIdentityFile(p string) ([]age.Identity, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return ids, nil
}

// open hands over to the desk on a bundle, to investigate or to review:
//
//	POST /api/start/open {"path": "...", "review": false, "identity": "~/.flightbay/support.key"}
func (s *StartServer) open(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path     string `json:"path"`
		Review   bool   `json:"review"`
		Identity string `json:"identity"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		jerr(w, 400, `body must be {"path": "/path/to/bundle.tar.zst"}`)
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
	case fi.IsDir():
		jerr(w, 400, p+" is a directory; pick a bundle file, or use Open a repo to scan a directory")
		return
	case !bundleName(p):
		jerr(w, 400, "not a Flightbay bundle: expected a .tar.zst or .tar.zst.age file")
		return
	}
	ids := s.Identities
	idFile := expandHome(strings.TrimSpace(body.Identity))
	if idFile != "" {
		more, err := loadIdentityFile(idFile)
		if err != nil {
			jerr(w, 400, "identity: "+err.Error())
			return
		}
		ids = append(append([]age.Identity{}, ids...), more...)
		if abs, err := filepath.Abs(idFile); err == nil {
			idFile = abs
		}
	}
	if _, enc, err := readBundleHeader(p, ids); err != nil {
		if enc {
			jerr(w, 400, "this bundle is encrypted; give the identity file that opens it (the key from flightbay-desk keygen)")
			return
		}
		jerr(w, 400, "not a readable Flightbay bundle: "+err.Error())
		return
	}
	kind := "open"
	if body.Review {
		kind = "review"
	}
	s.handOff(w, StartAction{Kind: kind, Path: p, Identity: idFile})
}

// handOff answers the browser, then calls Launch. The page polls
// /api/workspace until the next desk answers on the same address.
func (s *StartServer) handOff(w http.ResponseWriter, a StartAction) {
	if s.Launch == nil {
		jerr(w, 501, "this start page cannot open anything")
		return
	}
	if !s.launching.CompareAndSwap(false, true) {
		jerr(w, http.StatusConflict, "already opening something; wait for it to load")
		return
	}
	writeJSON(w, 200, map[string]any{"opening": true, "action": a})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(150 * time.Millisecond) // let the response reach the browser
		if err := s.Launch(a); err != nil {
			log.Printf("flightbay: could not open %s: %v", a.Kind, err)
			s.launching.Store(false)
		}
	}()
}

// ---- Repo ----

var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/]`)

// IsGitURL reports whether a Repo source should be cloned.
func IsGitURL(s string) bool {
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return scpLike.MatchString(s)
}

// ndjson writes one JSON object per line and flushes it, so the page can
// show progress while a clone or scan runs.
type ndjson struct {
	mu sync.Mutex
	w  http.ResponseWriter
}

func (n *ndjson) send(v any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	b, _ := json.Marshal(v)
	_, _ = n.w.Write(append(b, '\n'))
	if f, ok := n.w.(http.Flusher); ok {
		f.Flush()
	}
}

// progress is an io.Writer that sends each line as {"log": line}.
type progress struct {
	n    *ndjson
	buf  []byte
	seen map[string]bool
}

func (p *progress) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := strings.IndexByte(string(p.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(p.buf[:i]), "\r")
		p.buf = p.buf[i+1:]
		if strings.TrimSpace(line) != "" && !p.seen[line] {
			p.seen[line] = true
			p.n.send(map[string]string{"log": line})
		}
	}
	return len(b), nil
}

type scanFailure struct {
	Source   string `json:"source"`
	Renderer string `json:"renderer"`
	Reason   string `json:"reason"`
}

// scan clones (for a URL) and scans a repository in process, streaming
// progress as NDJSON, then hands over to the desk on the bundle it wrote:
//
//	POST /api/start/scan {"source": "https://github.com/org/repo", "values": [...], ...}
//
// Lines are {"log": "..."}, then one {"error": "...", "failed": [...]} or
// {"done": true, "bundle": "...", "objects": N, "failed": [...], "opening": true}.
func (s *StartServer) scan(w http.ResponseWriter, r *http.Request) {
	var req ScanRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Source) == "" {
		jerr(w, 400, `body must be {"source": "a directory or a Git URL"}`)
		return
	}
	if !s.scanMu.TryLock() {
		jerr(w, http.StatusConflict, "a scan is already running")
		return
	}
	defer s.scanMu.Unlock()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	out := &ndjson{w: w}
	res, bundlePath, failed, err := s.runScan(r.Context(), req, &progress{n: out, seen: map[string]bool{}})
	if err != nil {
		out.send(map[string]any{"error": err.Error(), "failed": failed})
		return
	}
	done := map[string]any{"done": true, "bundle": bundlePath, "objects": res.Objects, "kinds": len(res.Resources),
		"inputs": len(res.Inputs), "failed": failed, "picked": res.Picked}
	if s.Launch == nil || !s.launching.CompareAndSwap(false, true) {
		done["opening"] = false
		out.send(done)
		return
	}
	done["opening"] = true
	out.send(done)
	go func() {
		time.Sleep(150 * time.Millisecond)
		if err := s.Launch(StartAction{Kind: "open", Path: bundlePath, Scanned: true}); err != nil {
			log.Printf("flightbay: could not open %s: %v", bundlePath, err)
			s.launching.Store(false)
		}
	}()
}

// scanLog serialises the standard logger's output (the Helm SDK writes its
// warnings there) into the running scan's progress.
var scanLog sync.Mutex

func (s *StartServer) runScan(ctx context.Context, req ScanRequest, logw io.Writer) (*scan.Result, string, []scanFailure, error) {
	src := strings.TrimSpace(req.Source)
	var dir string
	if IsGitURL(src) {
		d, err := s.clone(ctx, src, logw)
		if err != nil {
			return nil, "", nil, err
		}
		dir = d
	} else {
		if strings.HasPrefix(src, "-") {
			return nil, "", nil, fmt.Errorf("%q is not a directory or a Git URL", src)
		}
		dir = expandHome(strings.Trim(src, `"'`))
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		st, err := os.Stat(dir)
		if err != nil {
			return nil, "", nil, fmt.Errorf("no such directory: %s", dir)
		}
		if !st.IsDir() {
			return nil, "", nil, fmt.Errorf("%s is a file; give the repository directory, or use Open a file for a bundle", dir)
		}
	}
	o := scan.Options{Paths: []string{dir}, Values: clean(req.Values), Set: clean(req.Set), Env: strings.TrimSpace(req.Env),
		Namespace: strings.TrimSpace(req.Namespace), Release: strings.TrimSpace(req.Release), Version: s.Version, Log: logw}
	if ov := strings.TrimSpace(req.Overlay); ov != "" {
		if !filepath.IsAbs(ov) {
			ov = filepath.Join(dir, ov)
		}
		o.Overlays = []string{ov}
	}
	fmt.Fprintf(logw, "Scanning %s\n", dir)
	scanLog.Lock()
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(prefixWriter{"  helm: ", logw})
	res, err := scan.Run(o)
	log.SetOutput(prevOut)
	log.SetFlags(prevFlags)
	scanLog.Unlock()
	if err != nil {
		return nil, "", nil, err
	}
	var failed []scanFailure
	for _, in := range res.Inputs {
		if in.State == bundle.StateFailed {
			failed = append(failed, scanFailure{Source: in.Source, Renderer: in.Renderer, Reason: in.Reason})
		}
	}
	fmt.Fprintf(logw, "Scanned %s: %d objects of %d kinds from %d inputs (%d failed)\n", res.Header.Repo.Name, res.Objects, len(res.Resources), len(res.Inputs), len(failed))
	if res.Objects == 0 {
		msg := "no Kubernetes manifests found in " + dir
		if len(failed) > 0 {
			msg = fmt.Sprintf("nothing rendered from %s: %d %s failed", dir, len(failed), scan.Plural(len(failed), "input", "inputs"))
		}
		return res, "", failed, errors.New(msg)
	}
	if err := os.MkdirAll(s.WorkDir, 0o700); err != nil {
		return res, "", failed, err
	}
	p := filepath.Join(s.WorkDir, scan.FileName(res, time.Now()))
	if err := os.WriteFile(p, res.Bundle, 0o600); err != nil {
		return res, "", failed, err
	}
	fmt.Fprintf(logw, "Wrote %s (%d bytes)\n", p, len(res.Bundle))
	return res, p, failed, nil
}

type prefixWriter struct {
	prefix string
	w      io.Writer
}

func (p prefixWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		fmt.Fprintf(p.w, "%s%s\n", p.prefix, strings.TrimPrefix(line, "warning: "))
	}
	return len(b), nil
}

func clean(v []string) []string {
	var out []string
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// clone runs git clone --depth 1 into WorkDir/repos/NAME. git uses the
// user's own credentials (credential helper, SSH agent) and never prompts.
func (s *StartServer) clone(ctx context.Context, url string, logw io.Writer) (string, error) {
	name := strings.TrimSuffix(path.Base(strings.TrimRight(strings.ReplaceAll(url, ":", "/"), "/")), ".git")
	name = unsafeName.ReplaceAllString(name, "_")
	if name == "" || name == "." || name == ".." {
		name = "repo"
	}
	root := filepath.Join(s.WorkDir, "repos")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, name+"-")
	if err != nil {
		return "", err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git is not installed; clone the repository yourself and give its directory")
	}
	fmt.Fprintf(logw, "git clone --depth 1 %s\n", url)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--", url, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var last []string
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fmt.Fprintf(logw, "  %s\n", line)
		if !strings.HasPrefix(line, "Cloning into") {
			last = append(last, line)
		}
	}
	if err := cmd.Wait(); err != nil {
		os.RemoveAll(dir)
		why := strings.Join(tail(last, 3), " ")
		if ctx.Err() != nil {
			why = "timed out after 3 minutes"
		}
		if why == "" {
			why = err.Error()
		}
		return "", fmt.Errorf("could not clone %s: %s", url, why)
	}
	return dir, nil
}

func tail(v []string, n int) []string {
	if len(v) > n {
		return v[len(v)-n:]
	}
	return v
}
