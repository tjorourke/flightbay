package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/posture"
	"github.com/tjorourke/flightbay/internal/reach"
	"github.com/tjorourke/flightbay/internal/store"
)

// newObjectKinds are fixes that add an object rather than change one. For
// objects declared in plain YAML they are also written as proposals next to
// the source file's directory.
var newObjectKinds = map[string]bool{"NetworkPolicy": true, "AuthorizationPolicy": true, "PeerAuthentication": true, "PodDisruptionBudget": true}

var placeholder = regexp.MustCompile(`<([a-z][a-z0-9 _-]*)>`)

// fill puts an object's names into a fix snippet's placeholders.
func fill(fix string, f Failure) string {
	return placeholder.ReplaceAllStringFunc(fix, func(p string) string {
		key := strings.Trim(p, "<>")
		ns, name := f.Item.Namespace, f.Item.Name
		if f.Item.Kind == "Namespace" {
			ns, name = f.Item.Name, ""
		}
		switch key {
		case "ns", "namespace":
			if ns != "" {
				return ns
			}
		case "name", "agent", "svc", "service", "deployment", "workload", "app", "gateway", "backend", "route", "policy", "server":
			if name != "" {
				return name
			}
		}
		return p
	})
}

// FixPackResult lists what a fix pack wrote.
type FixPackResult struct {
	Files     []string
	Proposals []string
}

// WriteFixPack writes one YAML file per failing check into dir, with the
// affected objects' names filled in (Agent reach fixes come fully written),
// proposals for new objects next to plain-YAML sources under proposals/, and
// a README.md listing each check, the objects and their source files. The
// repository's own files are never changed.
func WriteFixPack(dir string, g Gate, ws *store.Workspace) (*FixPackResult, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	checks := map[string]posture.Check{}
	for _, c := range g.Checks {
		checks[c.ID] = c
	}
	byCheck := map[string][]Failure{}
	var order []string
	for _, f := range g.Reported() {
		if byCheck[f.Check] == nil {
			order = append(order, f.Check)
		}
		byCheck[f.Check] = append(byCheck[f.Check], f)
	}
	reachFix := map[string]string{} // check|ns/name -> filled fix from Agent reach
	if hasReachCheck(order) {
		for _, a := range reach.Evaluate(ws).Agents {
			for _, fd := range a.Findings {
				if fd.Fix != "" {
					for _, id := range reachCheckFor(fd.Title) {
						reachFix[id+"|"+a.Namespace+"/"+a.Name] = fd.Fix
					}
				}
			}
		}
	}
	res := &FixPackResult{}
	var readme strings.Builder
	readme.WriteString("# Flightbay fix pack\n\n")
	if g.Repo != nil {
		fmt.Fprintf(&readme, "Generated from %s", g.Repo.Name)
		if g.Repo.Commit != "" {
			fmt.Fprintf(&readme, " at commit %s", short(g.Repo.Commit))
		}
		readme.WriteString(" by `flightbay scan --fix-pack`.\n\n")
	}
	readme.WriteString("Each file below holds the YAML for one failing Scorecard check, with names filled in where Flightbay knows them. Review every snippet before you apply it: placeholders still in angle brackets need a value, and the fix belongs in the chart values, overlay or manifest that declares the object (see the Source column).\n\n")
	readme.WriteString("Files under `proposals/` mirror the repository layout. They are new objects (NetworkPolicy, AuthorizationPolicy, PeerAuthentication, PodDisruptionBudget) for objects declared in plain YAML, placed in the same directory as the source file. Copy them over the repository root to raise a pull request; nothing in the repository was changed.\n\n")
	sort.SliceStable(order, func(i, j int) bool {
		return severityRank[checks[order[i]].Severity] > severityRank[checks[order[j]].Severity]
	})
	for _, id := range order {
		c := checks[id]
		fs := byCheck[id]
		var body strings.Builder
		fmt.Fprintf(&body, "# %s (%s, %s)\n# %s\n# Guide: %s\n", c.Title, id, c.Severity, oneLine(c.Why), c.Source)
		seen := map[string]bool{}
		for _, f := range fs {
			fix := reachFix[id+"|"+f.Item.Namespace+"/"+f.Item.Name]
			if fix == "" {
				fix = fill(c.Fix, f)
			}
			if seen[fix] {
				continue
			}
			seen[fix] = true
			fmt.Fprintf(&body, "---\n# %s", f.Object())
			if f.File != "" {
				fmt.Fprintf(&body, " (declared in %s)", location(f))
			}
			body.WriteString("\n" + asYAMLDoc(fix) + "\n")
			if kind := fixKind(fix); newObjectKinds[kind] && strings.HasPrefix(f.Renderer, "yaml") && f.File != "" && f.Item.Name != "" {
				name := fmt.Sprintf("flightbay-%s-%s.yaml", strings.ReplaceAll(id, ".", "-"), sanitize(f.Item.Name))
				p := filepath.Join(dir, "proposals", filepath.FromSlash(filepath.Dir(f.File)), name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return nil, err
				}
				head := fmt.Sprintf("# Proposed by Flightbay for %s: %s\n# Guide: %s\n", f.Object(), c.Title, c.Source)
				if err := os.WriteFile(p, []byte(head+asYAMLDoc(fix)+"\n"), 0o644); err != nil {
					return nil, err
				}
				rel, _ := filepath.Rel(dir, p)
				res.Proposals = append(res.Proposals, filepath.ToSlash(rel))
			}
		}
		fn := strings.ReplaceAll(id, ".", "-") + ".yaml"
		if err := os.WriteFile(filepath.Join(dir, fn), []byte(body.String()), 0o644); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, fn)
		fmt.Fprintf(&readme, "## %s\n\n`%s`, %s. File: [%s](%s). Guide: %s\n\n| Object | Source | Detail |\n| --- | --- | --- |\n", c.Title, id, c.Severity, fn, fn, c.Source)
		for _, f := range fs {
			fmt.Fprintf(&readme, "| %s | %s | %s |\n", mdCell(f.Object()), mdCell(location(f)), mdCell(f.Detail))
		}
		readme.WriteString("\n")
	}
	if len(order) == 0 {
		readme.WriteString("No failing checks: nothing to fix.\n")
	}
	if len(res.Proposals) > 0 {
		readme.WriteString("## Proposals\n\n")
		for _, p := range res.Proposals {
			fmt.Fprintf(&readme, "- `%s`\n", p)
		}
		readme.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme.String()), 0o644); err != nil {
		return nil, err
	}
	return res, nil
}

func hasReachCheck(ids []string) bool {
	for _, id := range ids {
		if strings.HasPrefix(id, "agentic.agent-") {
			return true
		}
	}
	return false
}

// reachCheckFor maps an Agent reach finding title to the Scorecard checks
// built from it.
func reachCheckFor(title string) []string {
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "inherits internet"):
		return []string{"agentic.agent-transitive-fetcher"}
	case strings.Contains(t, "internet"), strings.Contains(t, "egress is not limited"):
		return []string{"agentic.agent-internet-reach"}
	case strings.HasPrefix(t, "can write to"):
		return []string{"agentic.agent-write-sensitive"}
	case strings.Contains(t, "shares its identity"), strings.Contains(t, "also mount"):
		return []string{"agentic.agent-no-shared-channel"}
	case strings.HasPrefix(t, "can reach") && strings.Contains(t, "services"):
		return []string{"agentic.agent-least-reach"}
	}
	return nil
}

func location(f Failure) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

// asYAMLDoc keeps a YAML fix as it is and turns shell commands into comments,
// so every file in the pack is valid YAML.
func asYAMLDoc(fix string) string {
	fix = strings.TrimRight(fix, "\n")
	if fixKind(fix) != "" {
		return fix
	}
	var b strings.Builder
	for _, l := range strings.Split(fix, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			b.WriteString(l + "\n")
		} else {
			b.WriteString("# " + l + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

var kindLine = regexp.MustCompile(`(?m)^kind:\s*([A-Za-z]+)`)

func fixKind(fix string) string {
	if m := kindLine.FindStringSubmatch(fix); m != nil && strings.Contains(fix, "apiVersion:") {
		return m[1]
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func mdCell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(oneLine(s), "|", "\\|"), "`", "'")
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
