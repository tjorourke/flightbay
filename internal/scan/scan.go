// Package scan builds a Flightbay bundle from a code repository instead of a
// live cluster: plain manifests, Helm charts, Kustomize overlays and the
// Argo CD and Flux objects that point at them. The bundle is a normal one
// (header, discovery, resources, coverage, manifest with SHA-256s) marked
// source "repo", so the desk, the Scorecard, reach, search, SQL and Compare
// all work on it unchanged. It is declared state: there is no status, no
// pods and no logs, and the checks that need them say so.
package scan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/redact"
)

// Annotations every rendered object carries, so the desk and the CI report
// can say where it came from.
const (
	SourceAnnotation     = "flightbay.dev/source"      // repo-relative file (the template, for Helm)
	LineAnnotation       = "flightbay.dev/source-line" // line of the document, when known
	RenderedByAnnotation = "flightbay.dev/rendered-by" // yaml, helm <chart>, kustomize <dir>, argocd ..., flux ...
)

// Options control a scan.
type Options struct {
	Paths       []string // files or directories inside one repository; default "."
	Root        string   // the repository root that source paths are relative to; default the Git top level, else the scanned directory
	Values      []string // FILE (every chart) or CHARTDIR=FILE
	Set         []string // key=value, as helm --set, for every chart
	Overlays    []string // kustomize directories to build; default every overlay that is not only a base
	Env         string   // pick overlays/<env>, values-<env>.yaml, envs/<env>
	Release     string   // Helm release name; default the chart name
	Namespace   string   // namespace for namespaced objects without one; default "default"
	ContextName string   // the synthetic cluster name; default repo:<name>
	RepoURL     string   // treat GitOps sources with this URL as this repository
	HelmFetch   bool     // allow helm dependency update (network)
	Version     string   // collector version for the header
	Log         io.Writer
}

// Result describes a finished scan.
type Result struct {
	Header    bundle.Header
	Cluster   string
	Objects   int
	Inputs    []bundle.Coverage
	Resources []bundle.APIResource
	Picked    []string // environment choices, as printed
	Bundle    []byte   // the bundle, tar.zst
}

type rendered struct {
	obj       map[string]any
	source    string // repo-relative path
	line      int
	renderer  string
	namespace string // default for namespaced objects without one
	forceNS   bool   // Flux targetNamespace overrides the object's namespace
}

type plainFile struct {
	abs  string
	docs []doc
	err  error
	skip int
}

type scanner struct {
	opt      Options
	git      gitInfo
	root     string
	allowed  map[string]bool // files Git does not ignore; nil = not a Git repo
	ignore   []string        // .gitignore patterns when not a Git repo
	charts   []string
	kusts    []string
	files    []string
	plain    map[string]*plainFile
	claimed  map[string]string // file -> what renders it
	dirsDone map[string]string // chart or overlay dir -> what rendered it
	objects  []rendered
	inputs   []bundle.Coverage
	picks    []string
	queue    []rendered // GitOps objects still to follow
	followed map[string]bool
	kp       kustPlan
}

func (s *scanner) logf(format string, a ...any) {
	if s.opt.Log != nil {
		fmt.Fprintf(s.opt.Log, format+"\n", a...)
	}
}

func (s *scanner) picked(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	s.picks = append(s.picks, msg)
	s.logf("  --env %s: %s", s.opt.Env, msg)
}

func (s *scanner) rel(p string) string {
	r, err := filepath.Rel(s.root, p)
	if err != nil || strings.HasPrefix(r, "..") {
		return filepath.ToSlash(p)
	}
	if r == "." {
		return "."
	}
	return filepath.ToSlash(r)
}

func (s *scanner) input(c bundle.Coverage) {
	c.Type = bundle.CoverageInput
	if c.ID == "" {
		c.ID = "input." + c.Renderer + "." + c.Source
	}
	s.inputs = append(s.inputs, c)
}

// Run scans the repository and returns the bundle.
func Run(o Options) (*Result, error) {
	if len(o.Paths) == 0 {
		o.Paths = []string{"."}
	}
	var abs []string
	for _, p := range o.Paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if r, err := filepath.EvalSymlinks(a); err == nil {
			a = r
		}
		if _, err := os.Stat(a); err != nil {
			return nil, err
		}
		abs = append(abs, a)
	}
	start := abs[0]
	if !isDir(start) {
		start = filepath.Dir(start)
	}
	s := &scanner{opt: o, git: readGit(start), plain: map[string]*plainFile{}, claimed: map[string]string{}, dirsDone: map[string]string{}, followed: map[string]bool{}}
	s.root = s.git.root
	if s.git.commit == "" && len(abs) == 1 && isDir(abs[0]) {
		s.root = abs[0]
	}
	if o.Root != "" {
		r, err := filepath.Abs(o.Root)
		if err != nil {
			return nil, err
		}
		if e, err := filepath.EvalSymlinks(r); err == nil {
			r = e
		}
		s.root = r
	}
	s.loadIgnore(abs)
	for _, a := range abs {
		s.walk(a)
	}
	s.logf("Scanning %s: %d manifest files, %d Helm charts, %d kustomizations", s.root, len(s.files), len(s.charts), len(s.kusts))
	s.planKustomize()
	for _, c := range s.charts {
		s.claimTree(c, "helm "+s.rel(c))
	}
	s.parsePlain()
	// GitOps objects in plain manifests are followed first, so the paths they
	// render get their values, overlays and target namespace.
	s.drainQueue()
	for _, k := range s.kustTargets() {
		if by, done := s.dirsDone[k]; done {
			s.input(bundle.Coverage{Source: s.rel(k), Renderer: "kustomize", State: bundle.StateExcluded, Reason: "rendered by " + by})
			continue
		}
		s.buildKustomize(k, "kustomize "+s.rel(k), "", false)
		s.drainQueue()
	}
	for _, c := range s.charts {
		if by, done := s.dirsDone[c]; done {
			s.input(bundle.Coverage{Source: s.rel(c), Renderer: "helm", State: bundle.StateExcluded, Reason: "rendered by " + by})
			continue
		}
		s.buildChart(helmJob{dir: c, renderer: "helm " + s.rel(c)})
		s.drainQueue()
	}
	for _, f := range s.files {
		if s.claimed[f] != "" {
			continue
		}
		s.addPlain(s.plain[f], "yaml", "", false)
	}
	s.drainQueue()
	return s.write()
}

// ---------- discovery ----------

func (s *scanner) loadIgnore(paths []string) {
	if s.git.commit != "" {
		args := []string{"ls-files", "-z", "-co", "--exclude-standard", "--"}
		for _, p := range paths {
			args = append(args, p)
		}
		out := git(s.root, args...)
		s.allowed = map[string]bool{}
		for _, f := range strings.Split(out, "\x00") {
			if f != "" {
				s.allowed[filepath.Join(s.root, f)] = true
			}
		}
		return
	}
	b, err := os.ReadFile(filepath.Join(s.root, ".gitignore"))
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "!") {
			s.ignore = append(s.ignore, l)
		}
	}
}

func (s *scanner) ignored(p string, dir bool) bool {
	if s.allowed != nil {
		return !dir && !s.allowed[p]
	}
	rel := s.rel(p)
	for _, pat := range s.ignore {
		dirOnly := strings.HasSuffix(pat, "/")
		pat = strings.Trim(pat, "/")
		if dirOnly && !dir {
			continue
		}
		if strings.Contains(pat, "/") {
			if ok, _ := filepath.Match(pat, rel); ok {
				return true
			}
		} else if ok, _ := filepath.Match(pat, filepath.Base(p)); ok {
			return true
		}
	}
	return false
}

var manifestExt = map[string]bool{".yaml": true, ".yml": true, ".json": true}

const maxManifestBytes = 8 << 20

func (s *scanner) walk(start string) {
	if !isDir(start) {
		if !s.ignored(start, false) {
			s.files = append(s.files, start)
		}
		return
	}
	_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != start && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || s.ignored(p, true)) {
				return filepath.SkipDir
			}
			if isFile(filepath.Join(p, "Chart.yaml")) {
				s.charts = append(s.charts, p)
				return filepath.SkipDir // subcharts are rendered with their parent
			}
			if kustFile(p) != "" {
				s.kusts = append(s.kusts, p)
			}
			return nil
		}
		if !d.Type().IsRegular() || !manifestExt[strings.ToLower(filepath.Ext(name))] || s.ignored(p, false) {
			return nil
		}
		s.files = append(s.files, p)
		return nil
	})
}

// claimTree marks every manifest file under dir as rendered by something
// else (a chart, an overlay, a GitOps source), so it is not read twice.
func (s *scanner) claimTree(dir, by string) {
	prefix := dir + string(filepath.Separator)
	for _, f := range s.files {
		if strings.HasPrefix(f, prefix) && s.claimed[f] == "" {
			s.claimed[f] = by
		}
	}
}

func (s *scanner) claimDir(dir, by string) {
	for _, f := range s.files {
		if filepath.Dir(f) == dir && s.claimed[f] == "" {
			s.claimed[f] = by
		}
	}
}

// ---------- kustomize ----------

type kustPlan struct {
	baseOf    map[string]bool
	component map[string]bool
}

func (s *scanner) planKustomize() {
	s.kplan().baseOf, s.kplan().component = map[string]bool{}, map[string]bool{}
	for _, dir := range s.kusts {
		s.claimDir(dir, "kustomize "+s.rel(dir))
		k, err := readKustomization(dir)
		if err != nil {
			continue
		}
		if b, _ := os.ReadFile(kustFile(dir)); strings.Contains(string(b), "kind: Component") {
			s.kplan().component[dir] = true
		}
		for _, r := range k.refs() {
			if remoteRef(r) {
				continue
			}
			p := filepath.Clean(filepath.Join(dir, r))
			if isDir(p) && kustFile(p) != "" {
				s.kplan().baseOf[p] = true
			} else if isFile(p) && s.claimed[p] == "" {
				s.claimed[p] = "kustomize " + s.rel(dir)
			}
		}
	}
}

func (s *scanner) kplan() *kustPlan { return &s.kp }

// envOf names the environment an overlay is for, from layouts such as
// overlays/prod, envs/prod, environments/prod and clusters/prod.
func envOf(dir string) string {
	switch filepath.Base(filepath.Dir(dir)) {
	case "overlays", "envs", "env", "environments", "clusters", "stages":
		return filepath.Base(dir)
	}
	return ""
}

func (s *scanner) kustTargets() []string {
	if len(s.opt.Overlays) > 0 {
		var out []string
		for _, o := range s.opt.Overlays {
			p := firstDir(o, filepath.Join(s.root, o))
			if p == "" || kustFile(p) == "" {
				s.input(bundle.Coverage{Source: o, Renderer: "kustomize", State: bundle.StateFailed, Reason: "--overlay " + o + ": no kustomization.yaml there"})
				continue
			}
			out = append(out, p)
		}
		for _, k := range s.kusts {
			if !contains(out, k) && !s.kplan().baseOf[k] {
				s.input(bundle.Coverage{Source: s.rel(k), Renderer: "kustomize", State: bundle.StateExcluded, Reason: "not one of the --overlay directories"})
			}
		}
		return out
	}
	var out []string
	for _, k := range s.kusts {
		switch {
		case s.kplan().component[k]:
			continue
		case s.kplan().baseOf[k]:
			continue // built through the overlays that use it
		}
		if s.opt.Env != "" {
			if e := envOf(k); e != "" {
				if e != s.opt.Env {
					s.input(bundle.Coverage{Source: s.rel(k), Renderer: "kustomize", State: bundle.StateExcluded, Reason: "overlay for environment " + e + ", not --env " + s.opt.Env})
					continue
				}
				s.picked("overlay %s", s.rel(k))
			}
		}
		out = append(out, k)
	}
	return out
}

func firstDir(paths ...string) string {
	for _, p := range paths {
		if isDir(p) {
			a, _ := filepath.Abs(p)
			return a
		}
	}
	return ""
}

func (s *scanner) buildKustomize(dir, renderer, ns string, force bool) {
	s.dirsDone[dir] = renderer
	s.logf("  kustomize %s", s.rel(dir))
	objs, err := s.renderKustomize(dir, renderer, ns)
	cov := bundle.Coverage{Source: s.rel(dir), Renderer: "kustomize", State: bundle.StateComplete}
	if strings.HasPrefix(renderer, "argocd") || strings.HasPrefix(renderer, "flux") {
		cov.Renderer, cov.Reason = strings.Fields(renderer)[0], renderer
		cov.ID = "input." + renderer + "." + s.rel(dir)
	}
	if err != nil {
		cov.State, cov.Reason = bundle.StateFailed, strings.TrimSpace(cov.Reason+" "+err.Error())
	}
	for i := range objs {
		objs[i].forceNS = force
	}
	cov.Items = s.add(objs)
	s.input(cov)
}

// ---------- Helm ----------

func (s *scanner) buildChart(j helmJob) {
	s.dirsDone[j.dir] = j.renderer
	cov := bundle.Coverage{Source: s.rel(j.dir), Renderer: "helm", State: bundle.StateComplete}
	if strings.HasPrefix(j.renderer, "argocd") || strings.HasPrefix(j.renderer, "flux") {
		cov.Renderer, cov.ID = strings.Fields(j.renderer)[0], "input."+j.renderer+"."+s.rel(j.dir)
	}
	if b, err := os.ReadFile(filepath.Join(j.dir, "Chart.yaml")); err == nil && strings.Contains(string(b), "type: library") {
		cov.State, cov.Reason = bundle.StateExcluded, "library chart: nothing to render on its own"
		s.input(cov)
		return
	}
	files, missing := s.chartValueFiles(j.dir)
	j.valueFiles = append(files, j.valueFiles...)
	j.set = append(append([]string{}, s.opt.Set...), j.set...)
	s.logf("  helm %s", s.rel(j.dir))
	res, err := s.renderHelm(j)
	var notes []string
	if len(missing) > 0 {
		notes = append(notes, "values files not found: "+strings.Join(missing, ", "))
	}
	if res != nil {
		notes = append(notes, res.notes...)
		cov.Namespace = res.ns
	}
	if len(j.valueFiles) > 0 {
		var rel []string
		for _, f := range j.valueFiles {
			rel = append(rel, s.rel(f))
		}
		notes = append([]string{"values " + strings.Join(rel, ", ")}, notes...)
	}
	if err != nil {
		cov.State = bundle.StateFailed
		notes = append(notes, err.Error())
	}
	if res != nil {
		cov.Items = s.add(res.objects)
	}
	cov.Reason = strings.Join(notes, "; ")
	s.input(cov)
}

// ---------- plain manifests ----------

func (s *scanner) parsePlain() {
	for _, f := range s.files {
		pf := &plainFile{abs: f}
		s.plain[f] = pf
		st, err := os.Stat(f)
		if err != nil {
			pf.err = err
			continue
		}
		if st.Size() > maxManifestBytes {
			pf.err = fmt.Errorf("larger than %d bytes", maxManifestBytes)
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			pf.err = err
			continue
		}
		pf.docs, pf.skip, pf.err = parseManifests(b)
		if s.claimed[f] == "" {
			for _, d := range pf.docs {
				if gitopsKind(d.obj) {
					s.queue = append(s.queue, rendered{obj: d.obj, source: s.rel(f), line: d.line, renderer: "yaml"})
				}
			}
		}
	}
}

func (s *scanner) addPlain(pf *plainFile, renderer, ns string, force bool) int {
	if pf == nil {
		return 0
	}
	cov := bundle.Coverage{Source: s.rel(pf.abs), Renderer: "yaml", State: bundle.StateComplete}
	if renderer != "yaml" {
		cov.Reason = renderer
	}
	switch {
	case pf.err == errNotK8s:
		cov.State, cov.Reason = bundle.StateExcluded, pf.err.Error()
		s.input(cov)
		return 0
	case pf.err != nil && len(pf.docs) == 0:
		cov.State, cov.Reason = bundle.StateFailed, pf.err.Error()
		s.input(cov)
		return 0
	case pf.err != nil:
		cov.State, cov.Reason = bundle.StateTruncated, pf.err.Error()
	}
	var objs []rendered
	for _, d := range pf.docs {
		objs = append(objs, rendered{obj: d.obj, source: s.rel(pf.abs), line: d.line, renderer: renderer, namespace: ns, forceNS: force})
	}
	cov.Items = s.add(objs)
	if pf.skip > 0 {
		cov.Reason = strings.TrimSpace(cov.Reason + fmt.Sprintf(" (%d non-Kubernetes documents skipped)", pf.skip))
	}
	s.input(cov)
	return cov.Items
}

// add keeps rendered objects and queues any GitOps objects among them.
func (s *scanner) add(objs []rendered) int {
	for _, o := range objs {
		if o.renderer != "yaml" && gitopsKind(o.obj) {
			s.queue = append(s.queue, o)
		}
	}
	s.objects = append(s.objects, objs...)
	return len(objs)
}

// ---------- writing the bundle ----------

type objKey struct{ group, kind, ns, name string }

func (s *scanner) write() (*Result, error) {
	rd, err := redact.New(nil)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(s.root)
	if s.git.remote != "" {
		n := NormalizeRepoURL(s.git.remote)
		name = n[strings.LastIndex(n, "/")+1:]
	}
	ctx := s.opt.ContextName
	if ctx == "" {
		ctx = "repo:" + name
	}
	cluster := bundle.SafeID(ctx)
	res := newResolver()
	for _, o := range s.objects {
		if asStr(o.obj["kind"]) == "CustomResourceDefinition" {
			res.addCRD(o.obj)
		}
	}
	type group struct {
		r     bundle.APIResource
		items []any
	}
	groups := map[string]*group{}
	var order []string
	seen := map[objKey]string{}
	dups := map[string]int{}
	var crds []map[string]any
	unnamed := map[string]int{}
	for _, o := range s.objects {
		obj := o.obj
		r, _ := res.resolve(asStr(obj["apiVersion"]), asStr(obj["kind"]))
		md := asMap(obj["metadata"])
		if md == nil {
			md = map[string]any{}
			obj["metadata"] = md
		}
		nm := asStr(md["name"])
		if nm == "" {
			if g := asStr(md["generateName"]); g != "" {
				nm = g + "declared"
				md["name"] = nm
			} else {
				unnamed[o.source]++
				continue
			}
		}
		ns := asStr(md["namespace"])
		if r.Namespaced {
			if o.forceNS && o.namespace != "" {
				ns = o.namespace
			}
			if ns == "" {
				ns = o.namespace
			}
			if ns == "" {
				ns = s.opt.Namespace
			}
			if ns == "" {
				ns = "default"
			}
			md["namespace"] = ns
		} else if ns != "" {
			delete(md, "namespace")
			ns = ""
		}
		k := objKey{r.Group, r.Kind, ns, nm}
		if first, dup := seen[k]; dup {
			dups[fmt.Sprintf("%s %s: also in %s (kept the first)", r.Kind, strings.TrimPrefix(ns+"/"+nm, "/"), first)]++
			continue
		}
		seen[k] = o.source
		if r.Kind == "CustomResourceDefinition" {
			crds = append(crds, obj)
		}
		loc := fmt.Sprintf("clusters/%s/resources/%s", cluster, r.Name())
		var clean map[string]any
		if r.Group == "" && r.Resource == "secrets" {
			clean = rd.Secret(loc, obj, redact.SecretKeys, false)
		} else {
			clean = rd.Object(loc, obj)
		}
		cmd := asMap(clean["metadata"])
		ann := asMap(cmd["annotations"])
		if ann == nil {
			ann = map[string]any{}
		}
		ann[SourceAnnotation] = o.source
		if o.line > 0 {
			ann[LineAnnotation] = strconv.Itoa(o.line)
		}
		ann[RenderedByAnnotation] = o.renderer
		cmd["annotations"] = ann
		id := r.Group + "/" + r.Version + "/" + r.Resource
		g := groups[id]
		if g == nil {
			g = &group{r: r}
			groups[id] = g
			order = append(order, id)
		}
		g.items = append(g.items, clean)
	}
	for src, n := range unnamed {
		s.input(bundle.Coverage{ID: "input.unnamed." + src, Source: src, Renderer: "yaml", State: bundle.StateExcluded, Items: n, Reason: "objects without metadata.name were skipped"})
	}
	if len(dups) > 0 {
		var msgs []string
		n := 0
		for m, c := range dups {
			msgs = append(msgs, m)
			n += c
		}
		sort.Strings(msgs)
		if len(msgs) > 20 {
			msgs = append(msgs[:20], fmt.Sprintf("and %d more", len(msgs)-20))
		}
		s.input(bundle.Coverage{ID: "input.duplicates", Source: ".", Renderer: "scan", State: bundle.StateExcluded, Items: n,
			Reason: "the same object is rendered more than once (pick one environment with --env or --overlay): " + strings.Join(msgs, "; ")})
	}
	sort.Strings(order)

	var buf bytes.Buffer
	w, err := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "default"})
	if err != nil {
		return nil, err
	}
	opts := map[string]any{}
	if len(s.opt.Values) > 0 {
		opts["values"] = s.opt.Values
	}
	if len(s.opt.Set) > 0 {
		var keys []string
		for _, kv := range s.opt.Set {
			keys = append(keys, redactSet(kv))
		}
		opts["set"] = keys // values of --set are never recorded
	}
	for k, v := range map[string]string{"env": s.opt.Env, "release": s.opt.Release, "namespace": s.opt.Namespace, "repoURL": s.opt.RepoURL} {
		if v != "" {
			opts[k] = v
		}
	}
	if len(s.opt.Overlays) > 0 {
		opts["overlays"] = s.opt.Overlays
	}
	if s.opt.HelmFetch {
		opts["helmFetch"] = true
	}
	var paths []string
	for _, p := range s.opt.Paths {
		a, _ := filepath.Abs(p)
		if r, err := filepath.EvalSymlinks(a); err == nil {
			a = r
		}
		paths = append(paths, s.rel(a))
	}
	opts["paths"] = paths
	h := bundle.Header{
		Collector: "flightbay scan " + s.opt.Version, CaptureStart: time.Now().UTC(), Contexts: []string{ctx}, Source: bundle.SourceRepo,
		Repo:        &bundle.RepoInfo{Name: name, Path: s.root, Remote: safeRemote(s.git.remote), Commit: s.git.commit, Branch: s.git.branch, Dirty: s.git.dirty, Options: opts},
		Unavailable: []string{"status, pods, logs, events and runtime diagnostics: this is declared state from a repository"},
	}
	if err := w.WriteHeader(h); err != nil {
		return nil, err
	}
	var apis []bundle.APIResource
	for _, id := range order {
		apis = append(apis, groups[id].r)
	}
	if apis == nil {
		apis = []bundle.APIResource{}
	}
	if err := w.AddJSON(bundle.DiscoveryPath(cluster, "apiresources.json"), apis); err != nil {
		return nil, err
	}
	for _, c := range crds {
		n := asStr(asMap(c["metadata"])["name"])
		if n == "" {
			continue
		}
		if err := w.AddJSON(bundle.DiscoveryPath(cluster, "crds/"+bundle.SafeID(n)+".json"), collect.SlimCRD(c)); err != nil {
			return nil, err
		}
	}
	total := 0
	for _, id := range order {
		g := groups[id]
		r := g.r
		var chunks []string
		for part, i := 1, 0; i < len(g.items); part, i = part+1, i+500 {
			end := min(i+500, len(g.items))
			pth := bundle.ResourcePath(cluster, r.Group, r.Version, r.Resource, part)
			l := map[string]any{"apiVersion": "v1", "kind": "List", "items": g.items[i:end],
				"x-flightbay": map[string]any{"group": r.Group, "version": r.Version, "resource": r.Resource, "kind": r.Kind, "namespaced": r.Namespaced}}
			if err := w.AddJSON(pth, l); err != nil {
				return nil, err
			}
			chunks = append(chunks, pth)
		}
		total += len(g.items)
		gn := r.Group
		if gn == "" {
			gn = "core"
		}
		if err := w.Coverage(bundle.Coverage{ID: fmt.Sprintf("%s.resource.%s.%s.%s", cluster, gn, r.Version, r.Resource), Cluster: cluster, Type: bundle.CoverageResource,
			Group: r.Group, Version: r.Version, Resource: r.Resource, Kind: r.Kind, Namespaced: r.Namespaced, Chunks: chunks, Items: len(g.items), State: bundle.StateComplete,
			Reason: "declared in the repository"}); err != nil {
			return nil, err
		}
	}
	var log strings.Builder
	usedIDs := map[string]int{}
	for i := range s.inputs {
		c := &s.inputs[i]
		c.Cluster = cluster
		id := bundle.SafeID(c.ID)
		if n := usedIDs[id]; n > 0 {
			id = fmt.Sprintf("%s.%d", id, n+1)
		}
		usedIDs[bundle.SafeID(c.ID)]++
		c.ID = id
		if err := w.Coverage(*c); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(map[string]any{"time": h.CaptureStart, "cluster": cluster, "operation": "render", "target": c.Source, "result": resultWord(c), "error": failText(c)})
		log.Write(b)
		log.WriteByte('\n')
	}
	if err := w.Add(bundle.CollectionLog, []byte(log.String())); err != nil {
		return nil, err
	}
	rep, _ := json.MarshalIndent(rd.Report(), "", "  ")
	if err := w.Add(bundle.RedactionName, rep); err != nil {
		return nil, err
	}
	if err := w.Close(bundle.Manifest{Complete: true}); err != nil {
		return nil, err
	}
	return &Result{Header: h, Cluster: cluster, Objects: total, Inputs: s.inputs, Resources: apis, Picked: s.picks, Bundle: buf.Bytes()}, nil
}

func resultWord(c *bundle.Coverage) string {
	switch c.State {
	case bundle.StateComplete:
		return fmt.Sprintf("ok: %d objects (%s)", c.Items, c.Renderer)
	case bundle.StateExcluded:
		return "skipped: " + c.Reason
	}
	return c.State
}

func failText(c *bundle.Coverage) string {
	if c.State == bundle.StateFailed || c.State == bundle.StateTruncated {
		return c.Reason
	}
	return ""
}

// FileName is the default bundle name for a scan.
func FileName(r *Result, now time.Time) string {
	name := "repo"
	if r.Header.Repo != nil {
		name = r.Header.Repo.Name
	}
	return fmt.Sprintf("flightbay-scan-%s-%s.tar.zst", bundle.SafeID(name), now.UTC().Format("20060102T150405Z"))
}
