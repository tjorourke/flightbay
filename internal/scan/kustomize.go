package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

var kustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// kustFile returns the kustomization file in dir, or "".
func kustFile(dir string) string {
	for _, n := range kustomizationNames {
		if p := filepath.Join(dir, n); isFile(p) {
			return p
		}
	}
	return ""
}

// kustomization is the part of a kustomization file a scan reads before
// building: what it pulls in.
type kustomization struct {
	Resources  []string `json:"resources"`
	Bases      []string `json:"bases"`
	Components []string `json:"components"`
	Namespace  string   `json:"namespace"`
	HelmCharts []any    `json:"helmCharts"`
}

func readKustomization(dir string) (*kustomization, error) {
	p := kustFile(dir)
	if p == "" {
		return nil, fmt.Errorf("no kustomization in %s", dir)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var k kustomization
	if err := yaml.Unmarshal(b, &k); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &k, nil
}

func (k *kustomization) refs() []string {
	return append(append(append([]string{}, k.Resources...), k.Bases...), k.Components...)
}

func remoteRef(r string) bool {
	return strings.Contains(r, "://") || strings.HasPrefix(r, "github.com/") || strings.HasPrefix(r, "git@") || strings.HasPrefix(r, "gitlab.com/") || strings.HasPrefix(r, "bitbucket.org/")
}

// kustRemote walks local bases and returns the first remote reference, which
// a scan will not fetch.
func kustRemote(dir string, seen map[string]bool) string {
	if seen[dir] {
		return ""
	}
	seen[dir] = true
	k, err := readKustomization(dir)
	if err != nil {
		return ""
	}
	for _, r := range k.refs() {
		if remoteRef(r) {
			return r
		}
		p := filepath.Join(dir, r)
		if isDir(p) && kustFile(p) != "" {
			if x := kustRemote(p, seen); x != "" {
				return x
			}
		}
	}
	return ""
}

// originFS serves the top kustomization with originAnnotations switched on,
// so every built object says which file it came from. Nothing is written.
type originFS struct {
	filesys.FileSystem
	top string
}

func (o originFS) ReadFile(path string) ([]byte, error) {
	b, err := o.FileSystem.ReadFile(path)
	if err != nil || filepath.Clean(path) != o.top {
		return b, err
	}
	var k map[string]any
	if yaml.Unmarshal(b, &k) != nil || k == nil {
		return b, nil
	}
	meta := asList(k["buildMetadata"])
	for _, m := range meta {
		if asStr(m) == "originAnnotations" {
			return b, nil
		}
	}
	k["buildMetadata"] = append(meta, "originAnnotations")
	return yaml.Marshal(k)
}

const originAnnotation = "config.kubernetes.io/origin"

// renderKustomize builds an overlay with krusty. Each object's source is the
// file it came from (from the origin annotation, then removed), with its line.
func (s *scanner) renderKustomize(dir, renderer, namespace string) ([]rendered, error) {
	if r := kustRemote(dir, map[string]bool{}); r != "" {
		return nil, fmt.Errorf("remote resource %s: a scan does not fetch from the network", r)
	}
	top := kustFile(dir)
	fs := originFS{FileSystem: filesys.MakeFsOnDisk(), top: filepath.Clean(top)}
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	rm, err := k.Run(fs, dir)
	if err != nil {
		return nil, err
	}
	var out []rendered
	lines := map[string][]byte{}
	for _, r := range rm.Resources() {
		obj, err := r.Map()
		if err != nil {
			return out, err
		}
		src, line := s.rel(dir), 0
		md := asMap(obj["metadata"])
		ann := asMap(md["annotations"])
		if o := asStr(ann[originAnnotation]); o != "" {
			var origin struct {
				Path string `json:"path"`
				Repo string `json:"repo"`
			}
			if yaml.Unmarshal([]byte(o), &origin) == nil && origin.Path != "" && origin.Repo == "" {
				abs := filepath.Join(dir, origin.Path)
				src = s.rel(abs)
				if _, ok := lines[abs]; !ok {
					lines[abs], _ = os.ReadFile(abs)
				}
				line = lineOf(lines[abs], asStr(obj["kind"]), asStr(md["name"]))
			}
			delete(ann, originAnnotation)
			if len(ann) == 0 {
				delete(md, "annotations")
			}
		}
		out = append(out, rendered{obj: obj, source: src, line: line, renderer: renderer, namespace: namespace})
	}
	return out, nil
}

// lineOf finds kind/name in a file, falling back to the only document of
// that kind (kustomize prefixes and suffixes change names).
func lineOf(data []byte, kind, name string) int {
	if len(data) == 0 {
		return 0
	}
	docs, _, _ := parseManifests(data)
	var only, n int
	for _, d := range docs {
		if asStr(d.obj["kind"]) != kind {
			continue
		}
		dn := asStr(asMap(d.obj["metadata"])["name"])
		if dn == name {
			return d.line
		}
		if dn != "" && strings.Contains(name, dn) {
			only = d.line
			n = 1
			continue
		}
		if only == 0 {
			only = d.line
		}
		n++
	}
	if n == 1 {
		return only
	}
	return 0
}
