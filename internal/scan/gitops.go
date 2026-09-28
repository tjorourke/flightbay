package scan

import (
	"fmt"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/bundle"
)

// gitopsKind reports whether obj is an Argo CD or Flux object whose source
// a scan follows.
func gitopsKind(obj map[string]any) bool {
	g, _ := groupVersion(asStr(obj["apiVersion"]))
	switch g + "/" + asStr(obj["kind"]) {
	case "argoproj.io/Application", "argoproj.io/ApplicationSet", "helm.toolkit.fluxcd.io/HelmRelease", "kustomize.toolkit.fluxcd.io/Kustomization":
		return true
	}
	return false
}

func objRef(obj map[string]any) string {
	md := asMap(obj["metadata"])
	ns := asStr(md["namespace"])
	if ns != "" {
		ns += "/"
	}
	return asStr(obj["kind"]) + " " + ns + asStr(md["name"])
}

// sameRepo reports whether a GitOps source URL is the repository being scanned.
func (s *scanner) sameRepo(u string) bool {
	if u == "" {
		return false
	}
	n := NormalizeRepoURL(u)
	return (s.git.remote != "" && n == NormalizeRepoURL(s.git.remote)) || (s.opt.RepoURL != "" && n == NormalizeRepoURL(s.opt.RepoURL))
}

func (s *scanner) drainQueue() {
	for len(s.queue) > 0 {
		o := s.queue[0]
		s.queue = s.queue[1:]
		key := objRef(o.obj) + "@" + o.source
		if s.followed[key] {
			continue
		}
		s.followed[key] = true
		s.follow(o)
	}
}

func (s *scanner) unresolved(o rendered, why string) {
	s.input(bundle.Coverage{ID: "input.gitops." + objRef(o.obj), Source: o.source, Renderer: gitopsTool(o.obj), State: bundle.StateExcluded,
		Reason: objRef(o.obj) + ": unresolved source: " + why})
	s.logf("  %s (%s): unresolved source: %s", objRef(o.obj), o.source, why)
}

func gitopsTool(obj map[string]any) string {
	if strings.HasPrefix(asStr(obj["apiVersion"]), "argoproj.io") {
		return "argocd"
	}
	return "flux"
}

// follow renders what an Argo CD or Flux object deploys, when its source is
// a path in this repository.
func (s *scanner) follow(o rendered) {
	obj := o.obj
	spec := asMap(obj["spec"])
	switch asStr(obj["kind"]) {
	case "Application":
		s.followArgo(o, spec, "argocd Application "+nsName(obj))
	case "ApplicationSet":
		tpl := asMap(asMap(spec["template"])["spec"])
		b, _ := yaml.Marshal(tpl)
		if strings.Contains(string(b), "{{") {
			s.unresolved(o, "the ApplicationSet template uses generator parameters, and a scan does not expand generators")
			return
		}
		s.followArgo(o, tpl, "argocd ApplicationSet "+nsName(obj))
	case "Kustomization":
		s.followFluxKustomization(o, spec)
	case "HelmRelease":
		s.followHelmRelease(o, spec)
	}
}

func nsName(obj map[string]any) string {
	md := asMap(obj["metadata"])
	if ns := asStr(md["namespace"]); ns != "" {
		return ns + "/" + asStr(md["name"])
	}
	return asStr(md["name"])
}

func (s *scanner) repoPath(p string) (string, bool) {
	p = strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/")
	abs := filepath.Clean(filepath.Join(s.root, p))
	if !strings.HasPrefix(abs+string(filepath.Separator), s.root+string(filepath.Separator)) {
		return "", false
	}
	return abs, isDir(abs)
}

func (s *scanner) followArgo(o rendered, spec map[string]any, label string) {
	ns := asStr(asMap(spec["destination"])["namespace"])
	sources := asList(spec["sources"])
	if src := asMap(spec["source"]); src != nil {
		sources = append([]any{src}, sources...)
	}
	if len(sources) == 0 {
		s.unresolved(o, "no source")
		return
	}
	refs := map[string]map[string]any{} // multi-source $ref -> source
	for _, x := range sources {
		if r := asStr(asMap(x)["ref"]); r != "" {
			refs[r] = asMap(x)
		}
	}
	for _, x := range sources {
		src := asMap(x)
		if asStr(src["ref"]) != "" && asStr(src["path"]) == "" && asStr(src["chart"]) == "" {
			continue // a values-only source for $ref value files
		}
		url := asStr(src["repoURL"])
		if asStr(src["chart"]) != "" {
			s.unresolved(o, "chart "+asStr(src["chart"])+" from the Helm repository "+url)
			continue
		}
		if !s.sameRepo(url) {
			s.unresolved(o, "repository "+url+" is not the one being scanned (pass --repo-url if it is)")
			continue
		}
		dir, ok := s.repoPath(asStr(src["path"]))
		if !ok {
			s.unresolved(o, "path "+asStr(src["path"])+" does not exist in this repository")
			continue
		}
		s.renderDir(o, dir, ns, false, label, func(j *helmJob) {
			h := asMap(src["helm"])
			j.release = asStr(h["releaseName"])
			for _, vf := range asList(h["valueFiles"]) {
				f := asStr(vf)
				if strings.HasPrefix(f, "$") {
					ref, rest, _ := strings.Cut(strings.TrimPrefix(f, "$"), "/")
					if rs := refs[ref]; rs != nil && s.sameRepo(asStr(rs["repoURL"])) {
						j.valueFiles = append(j.valueFiles, filepath.Join(s.root, rest))
					}
					continue
				}
				j.valueFiles = append(j.valueFiles, filepath.Join(dir, f))
			}
			if v := asStr(h["values"]); v != "" {
				var m map[string]any
				if yaml.Unmarshal([]byte(v), &m) == nil {
					j.inline = append(j.inline, m)
				}
			}
			if v := asMap(h["valuesObject"]); v != nil {
				j.inline = append(j.inline, v)
			}
			for _, p := range asList(h["parameters"]) {
				pm := asMap(p)
				j.set = append(j.set, asStr(pm["name"])+"="+fmt.Sprint(pm["value"]))
			}
		}, asMap(src["directory"])["recurse"] == true)
	}
}

func (s *scanner) fluxSource(o rendered, ref map[string]any) (string, bool) {
	kind, name := asStr(ref["kind"]), asStr(ref["name"])
	if kind != "GitRepository" {
		s.unresolved(o, "source "+kind+" "+name+" is not a Git repository")
		return "", false
	}
	for _, x := range s.objects {
		if asStr(x.obj["kind"]) == "GitRepository" && asStr(asMap(x.obj["metadata"])["name"]) == name {
			url := asStr(asMap(x.obj["spec"])["url"])
			if s.sameRepo(url) {
				return url, true
			}
			s.unresolved(o, "GitRepository "+name+" points at "+url+", not the repository being scanned (pass --repo-url if it is)")
			return "", false
		}
	}
	for _, pf := range s.plain {
		for _, d := range pf.docs {
			if asStr(d.obj["kind"]) == "GitRepository" && asStr(asMap(d.obj["metadata"])["name"]) == name {
				url := asStr(asMap(d.obj["spec"])["url"])
				if s.sameRepo(url) {
					return url, true
				}
				s.unresolved(o, "GitRepository "+name+" points at "+url+", not the repository being scanned (pass --repo-url if it is)")
				return "", false
			}
		}
	}
	if s.opt.RepoURL != "" {
		return s.opt.RepoURL, true // --repo-url: the named source is this repository
	}
	s.unresolved(o, "GitRepository "+name+" is not in this repository (pass --repo-url to treat it as this one)")
	return "", false
}

func (s *scanner) followFluxKustomization(o rendered, spec map[string]any) {
	if _, ok := s.fluxSource(o, asMap(spec["sourceRef"])); !ok {
		return
	}
	dir, ok := s.repoPath(asStr(spec["path"]))
	if !ok {
		s.unresolved(o, "path "+asStr(spec["path"])+" does not exist in this repository")
		return
	}
	ns := asStr(spec["targetNamespace"])
	s.renderDir(o, dir, ns, ns != "", "flux Kustomization "+nsName(o.obj), nil, true)
}

func (s *scanner) followHelmRelease(o rendered, spec map[string]any) {
	cs := asMap(asMap(spec["chart"])["spec"])
	if cs == nil {
		s.unresolved(o, "chartRef sources (OCIRepository, HelmChart) are not followed")
		return
	}
	if _, ok := s.fluxSource(o, asMap(cs["sourceRef"])); !ok {
		return
	}
	dir, ok := s.repoPath(asStr(cs["chart"]))
	if !ok || !isFile(filepath.Join(dir, "Chart.yaml")) {
		s.unresolved(o, "chart path "+asStr(cs["chart"])+" is not a chart in this repository")
		return
	}
	ns := asStr(spec["targetNamespace"])
	if ns == "" {
		ns = asStr(asMap(o.obj["metadata"])["namespace"])
	}
	s.renderDir(o, dir, ns, false, "flux HelmRelease "+nsName(o.obj), func(j *helmJob) {
		j.release = asStr(spec["releaseName"])
		if j.release == "" {
			j.release = asStr(asMap(o.obj["metadata"])["name"])
			if t := asStr(spec["targetNamespace"]); t != "" {
				j.release = t + "-" + j.release
			}
		}
		for _, vf := range asList(cs["valuesFiles"]) {
			j.valueFiles = append(j.valueFiles, filepath.Join(s.root, asStr(vf)))
		}
		if v := asMap(spec["values"]); v != nil {
			j.inline = append(j.inline, v)
		}
		if len(asList(spec["valuesFrom"])) > 0 {
			s.logf("  %s: valuesFrom (ConfigMaps and Secrets in the cluster) is not read", objRef(o.obj))
		}
	}, false)
}

// renderDir renders a GitOps source path: a chart, an overlay or a directory
// of plain manifests.
func (s *scanner) renderDir(o rendered, dir, ns string, force bool, label string, helm func(*helmJob), recurse bool) {
	if by, done := s.dirsDone[dir]; done {
		s.input(bundle.Coverage{ID: "input.gitops." + label, Source: s.rel(dir), Renderer: gitopsTool(o.obj), State: bundle.StateExcluded, Reason: label + ": already rendered by " + by})
		return
	}
	switch {
	case isFile(filepath.Join(dir, "Chart.yaml")):
		j := helmJob{dir: dir, namespace: ns, renderer: label}
		if helm != nil {
			helm(&j)
		}
		s.claimTree(dir, label)
		s.buildChart(j)
	case kustFile(dir) != "":
		s.claimTree(dir, label)
		s.claimDir(dir, label)
		s.buildKustomize(dir, label, ns, force)
	default:
		s.dirsDone[dir] = label
		n := 0
		for _, f := range s.files {
			in := filepath.Dir(f) == dir || (recurse && strings.HasPrefix(f, dir+string(filepath.Separator)))
			if !in {
				continue
			}
			if by := s.claimed[f]; by != "" && by != label {
				continue
			}
			s.claimed[f] = label
			n += s.addPlain(s.plain[f], label, ns, force)
		}
		if n == 0 {
			s.input(bundle.Coverage{ID: "input.gitops." + label, Source: s.rel(dir), Renderer: gitopsTool(o.obj), State: bundle.StateExcluded, Reason: label + ": no manifests found in " + s.rel(dir)})
		}
	}
}
