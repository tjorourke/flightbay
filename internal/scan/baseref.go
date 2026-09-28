package scan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RunAtRef scans the same paths, with the same options, at another commit
// (such as origin/main), from a temporary git worktree that is removed
// afterwards. It is how --new-only --base-ref finds what a change introduces.
func RunAtRef(o Options, ref string) (*Result, error) {
	if len(o.Paths) == 0 {
		o.Paths = []string{"."}
	}
	first, err := filepath.Abs(o.Paths[0])
	if err != nil {
		return nil, err
	}
	if !isDir(first) {
		first = filepath.Dir(first)
	}
	g := readGit(first)
	if g.commit == "" {
		return nil, fmt.Errorf("--base-ref needs a Git repository; %s is not in one", first)
	}
	tmp, err := os.MkdirTemp("", "flightbay-base-")
	if err != nil {
		return nil, err
	}
	wt := filepath.Join(tmp, "wt")
	defer func() {
		_ = exec.Command("git", "-C", g.root, "worktree", "remove", "--force", wt).Run()
		os.RemoveAll(tmp)
	}()
	if out, err := exec.Command("git", "-C", g.root, "worktree", "add", "--detach", wt, ref).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git worktree add %s: %v: %s", ref, err, strings.TrimSpace(string(out)))
	}
	base := o
	base.Paths = nil
	for _, p := range o.Paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if r, err := filepath.EvalSymlinks(a); err == nil {
			a = r
		}
		rel, err := filepath.Rel(g.root, a)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%s is outside the repository %s", p, g.root)
		}
		bp := filepath.Join(wt, rel)
		if _, err := os.Stat(bp); err != nil {
			continue // the path is new in this change: nothing to compare with
		}
		base.Paths = append(base.Paths, bp)
	}
	remap := func(list []string) []string {
		var out []string
		for _, v := range list {
			if a, err := filepath.Abs(v); err == nil {
				if rel, err := filepath.Rel(g.root, a); err == nil && !strings.HasPrefix(rel, "..") {
					v = filepath.Join(wt, rel)
				}
			}
			out = append(out, v)
		}
		return out
	}
	base.Overlays = remap(o.Overlays)
	base.Log = nil
	if base.RepoURL == "" {
		base.RepoURL = g.remote
	}
	if len(base.Paths) == 0 {
		return nil, nil
	}
	return Run(base)
}
