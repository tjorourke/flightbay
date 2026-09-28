package scan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	helmcli "helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/strvals"
	"sigs.k8s.io/yaml"
)

// NamespaceAnnotation on a Chart.yaml sets the namespace a scan renders the
// chart into when --namespace is not given.
const NamespaceAnnotation = "flightbay.dev/namespace"

// helmJob is one chart render: the chart directory plus the values to use.
type helmJob struct {
	dir         string   // absolute chart directory
	release     string   // release name, default the chart name
	namespace   string   // "" = options or annotation or "default"
	valueFiles  []string // absolute paths, applied after values.yaml in order
	inline      []map[string]any
	set         []string // key=value, as helm --set
	renderer    string   // label for the source annotation
	contextNote string   // e.g. "argocd Application apps/foo"
}

// helmResult is what rendering a chart produced.
type helmResult struct {
	objects []rendered
	notes   []string // missing dependencies and similar, recorded in coverage
	name    string
	ns      string
}

var sourceComment = regexp.MustCompile(`(?m)^# Source: (.+)$`)

// renderHelm renders a chart the way `helm template --include-crds` does,
// with no cluster: Install with DryRun and ClientOnly.
func (s *scanner) renderHelm(j helmJob) (*helmResult, error) {
	chartDir := j.dir
	res := &helmResult{}
	if s.opt.HelmFetch {
		tmp, err := os.MkdirTemp("", "flightbay-chart-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		dst := filepath.Join(tmp, filepath.Base(chartDir))
		if err := copyDir(chartDir, dst); err != nil {
			return nil, err
		}
		settings := helmcli.New()
		m := &downloader.Manager{Out: io.Discard, ChartPath: dst, Getters: getter.All(settings),
			RepositoryConfig: settings.RepositoryConfig, RepositoryCache: settings.RepositoryCache, SkipUpdate: false}
		if err := m.Update(); err != nil {
			res.notes = append(res.notes, "helm dependency update failed: "+err.Error())
		} else {
			chartDir = dst
		}
	}
	ch, err := loader.LoadDir(chartDir)
	if err != nil {
		return nil, fmt.Errorf("load chart: %w", err)
	}
	res.name = ch.Name()
	if reqs := ch.Metadata.Dependencies; len(reqs) > 0 {
		if err := action.CheckDependencies(ch, reqs); err != nil {
			res.notes = append(res.notes, strings.ReplaceAll(err.Error(), "\n", " ")+" (vendor them into charts/ with helm dependency build, or pass --helm-fetch)")
		}
	}
	vals, err := s.helmValues(ch, j)
	if err != nil {
		return nil, err
	}
	ns := j.namespace
	if ns == "" {
		ns = s.opt.Namespace
	}
	if ns == "" && ch.Metadata.Annotations != nil {
		ns = ch.Metadata.Annotations[NamespaceAnnotation]
	}
	if ns == "" {
		ns = "default"
	}
	res.ns = ns
	rel := j.release
	if rel == "" {
		rel = s.opt.Release
	}
	if rel == "" {
		rel = ch.Name()
	}
	cfg := &action.Configuration{Log: func(string, ...interface{}) {}}
	in := action.NewInstall(cfg)
	in.DryRun, in.ClientOnly, in.IncludeCRDs, in.Replace = true, true, true, true
	in.DisableHooks = false
	in.ReleaseName, in.Namespace = rel, ns
	r, err := in.Run(ch, vals)
	if err != nil {
		return res, fmt.Errorf("render: %w", err)
	}
	chartRel := s.rel(j.dir)
	manifest := r.Manifest
	for _, h := range r.Hooks {
		if isTestHook(h) {
			continue // helm test pods are not part of the deployed state
		}
		manifest += "\n---\n# Source: " + h.Path + "\n" + h.Manifest
	}
	for _, part := range splitSources(manifest) {
		docs, _, err := parseManifests([]byte(part.body))
		if err != nil && err != errNotK8s {
			res.notes = append(res.notes, part.source+": "+err.Error())
			continue
		}
		src := templatePath(chartRel, ch.Name(), part.source)
		for _, d := range docs {
			res.objects = append(res.objects, rendered{obj: d.obj, source: src, renderer: j.renderer, namespace: ns})
		}
	}
	return res, nil
}

// helmValues merges values.yaml (by the chart itself), the value files, the
// inline values and --set, in that order.
func (s *scanner) helmValues(ch *chart.Chart, j helmJob) (map[string]any, error) {
	base := map[string]any{}
	for _, f := range j.valueFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("values %s: %w", f, err)
		}
		var cur map[string]any
		if err := yaml.Unmarshal(b, &cur); err != nil {
			return nil, fmt.Errorf("values %s: %w", f, err)
		}
		base = mergeMaps(base, cur)
	}
	for _, v := range j.inline {
		base = mergeMaps(base, v)
	}
	for _, kv := range j.set {
		if err := strvals.ParseInto(kv, base); err != nil {
			return nil, fmt.Errorf("--set %s: %w", redactSet(kv), err)
		}
	}
	return chartutil.CoalesceValues(ch, base)
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if bv, ok := v.(map[string]any); ok {
			if av, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(av, bv)
				continue
			}
		}
		out[k] = v
	}
	return out
}

type sourcePart struct{ source, body string }

// splitSources splits a rendered manifest on its "# Source:" comments.
func splitSources(manifest string) []sourcePart {
	idx := sourceComment.FindAllStringSubmatchIndex(manifest, -1)
	var out []sourcePart
	for i, m := range idx {
		end := len(manifest)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		body := manifest[m[1]:end]
		body = strings.TrimSuffix(strings.TrimRight(body, "\n"), "---")
		out = append(out, sourcePart{source: manifest[m[2]:m[3]], body: body})
	}
	if len(idx) == 0 && strings.TrimSpace(manifest) != "" {
		out = append(out, sourcePart{source: "", body: manifest})
	}
	return out
}

// templatePath maps "mychart/charts/sub/templates/x.yaml" to the file in the
// repository: <chart dir>/charts/sub/templates/x.yaml.
func templatePath(chartRel, chartName, src string) string {
	if src == "" {
		return chartRel
	}
	rest := strings.TrimPrefix(src, chartName+"/")
	return filepath.ToSlash(filepath.Join(chartRel, rest))
}

func isTestHook(h *release.Hook) bool {
	for _, e := range h.Events {
		if e == release.HookTest {
			return true
		}
	}
	return false
}

func redactSet(kv string) string {
	if i := strings.Index(kv, "="); i >= 0 {
		return kv[:i] + "=[REDACTED]"
	}
	return kv
}

// chartValueFiles picks the value files for a chart: --values entries that
// name this chart (DIR=FILE), then global ones, resolved first inside the
// chart directory and then relative to the working directory, then the
// environment's values file if --env is set.
func (s *scanner) chartValueFiles(dir string) (files []string, missing []string) {
	for _, v := range s.opt.Values {
		target, file := "", v
		if i := strings.Index(v, "="); i > 0 {
			target, file = v[:i], v[i+1:]
		}
		if target != "" {
			abs, _ := filepath.Abs(target)
			if abs != dir && filepath.Join(s.root, target) != dir {
				continue
			}
		}
		if p := firstExisting(filepath.Join(dir, file), file, filepath.Join(s.root, file)); p != "" {
			files = append(files, p)
		} else {
			missing = append(missing, file)
		}
	}
	if s.opt.Env != "" {
		for _, n := range []string{"values-" + s.opt.Env + ".yaml", "values." + s.opt.Env + ".yaml", "values-" + s.opt.Env + ".yml", filepath.Join("envs", s.opt.Env, "values.yaml"), filepath.Join(s.opt.Env, "values.yaml")} {
			if p := filepath.Join(dir, n); isFile(p) && !contains(files, p) {
				files = append(files, p)
				s.picked("chart %s: values file %s", s.rel(dir), s.rel(p))
				break
			}
		}
	}
	return files, missing
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if isFile(p) {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	return ""
}

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }

func contains(list []string, x string) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
