package scan

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func git(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitInfo describes the repository holding path. root is path itself when
// it is not in a Git repository.
type gitInfo struct {
	root, remote, commit, branch string
	dirty                        bool
}

func readGit(path string) gitInfo {
	g := gitInfo{root: path}
	top := git(path, "rev-parse", "--show-toplevel")
	if top == "" {
		return g
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	g.root = top
	g.commit = git(top, "rev-parse", "HEAD")
	g.branch = git(top, "rev-parse", "--abbrev-ref", "HEAD")
	g.remote = git(top, "remote", "get-url", "origin")
	g.dirty = git(top, "status", "--porcelain", "--untracked-files=no") != ""
	return g
}

var scpLike = regexp.MustCompile(`^[\w.-]+@([\w.-]+):(.+)$`)

// NormalizeRepoURL reduces the forms a Git remote is written in
// (https://host/org/repo.git, git@host:org/repo, ssh://git@host/org/repo)
// to host/org/repo for comparison.
func NormalizeRepoURL(u string) string {
	u = strings.TrimSpace(u)
	if m := scpLike.FindStringSubmatch(u); m != nil {
		u = m[1] + "/" + m[2]
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "@"); i >= 0 && i < strings.Index(u+"/", "/") {
		u = u[i+1:]
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return strings.ToLower(u)
}

// safeRemote drops credentials from a remote URL before it is recorded.
func safeRemote(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 && at < strings.Index(rest+"/", "/") {
			return u[:i+3] + rest[at+1:]
		}
	}
	return u
}
