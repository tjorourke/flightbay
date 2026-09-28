package scan

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/posture"
)

func gate(t *testing.T) Gate {
	t.Helper()
	root := sample(t)
	_, ws := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "manifests")}})
	return Evaluate(ws)
}

func TestSARIF(t *testing.T) {
	g := gate(t)
	if len(g.Failures) == 0 {
		t.Fatal("the sample should fail some checks")
	}
	var buf bytes.Buffer
	if err := g.Write(&buf, "sarif", "high", "test"); err != nil {
		t.Fatal(err)
	}
	var log map[string]any
	if err := json.Unmarshal(buf.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	if log["version"] != "2.1.0" || !strings.Contains(asStr(log["$schema"]), "sarif-2.1.0") {
		t.Fatalf("version/schema = %v %v", log["version"], log["$schema"])
	}
	runs := asList(log["runs"])
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := asMap(runs[0])
	driver := asMap(asMap(run["tool"])["driver"])
	if driver["name"] != "Flightbay" {
		t.Errorf("driver = %v", driver["name"])
	}
	rules := map[string]map[string]any{}
	for i, r := range asList(driver["rules"]) {
		rm := asMap(r)
		for _, k := range []string{"id", "shortDescription", "fullDescription", "helpUri", "help", "defaultConfiguration"} {
			if rm[k] == nil {
				t.Errorf("rule %d has no %s", i, k)
			}
		}
		if !strings.HasPrefix(asStr(rm["helpUri"]), "http") {
			t.Errorf("rule %v helpUri = %v", rm["id"], rm["helpUri"])
		}
		rules[asStr(rm["id"])] = rm
	}
	results := asList(run["results"])
	if len(results) != len(g.Failures) {
		t.Errorf("results = %d, want one per failing item (%d)", len(results), len(g.Failures))
	}
	for i, r := range results {
		rm := asMap(r)
		id := asStr(rm["ruleId"])
		if rules[id] == nil {
			t.Errorf("result %d rule %s is not in the rules", i, id)
		}
		if int(rm["ruleIndex"].(float64)) >= len(rules) {
			t.Errorf("result %d ruleIndex out of range", i)
		}
		if l := asStr(rm["level"]); l != "error" && l != "warning" && l != "note" {
			t.Errorf("result %d level %q", i, l)
		}
		if asStr(asMap(rm["message"])["text"]) == "" {
			t.Errorf("result %d has no message", i)
		}
		locs := asList(rm["locations"])
		if len(locs) != 1 {
			t.Fatalf("result %d locations = %v", i, locs)
		}
		pl := asMap(asMap(locs[0])["physicalLocation"])
		uri := asStr(asMap(pl["artifactLocation"])["uri"])
		line := asMap(pl["region"])["startLine"].(float64)
		if uri == "" || line < 1 {
			t.Errorf("result %d location %s:%v", i, uri, line)
		}
		if id == "k8s.probes" && (uri != "manifests/app.yaml" || line != 9) {
			t.Errorf("probe result at %s:%v, want manifests/app.yaml:9", uri, line)
		}
	}
}

func TestFailOnExitCodes(t *testing.T) {
	g := gate(t)
	worst := ""
	for _, f := range g.Failures {
		if severityRank[f.Severity] > severityRank[worst] {
			worst = f.Severity
		}
	}
	if worst != "high" {
		t.Fatalf("worst failure in the sample = %q, want high (the demo namespace has no Pod Security label)", worst)
	}
	for sev, want := range map[string]int{"critical": 0, "high": 1, "medium": 1, "low": 1, "": 0} {
		if got := g.ExitCode(sev); got != want {
			t.Errorf("--fail-on %q exit = %d, want %d", sev, got, want)
		}
	}
	// a baseline with the same failures leaves nothing new
	base := g
	if n := g.MarkNew(base, "base"); n != 0 || g.ExitCode("low") != 0 {
		t.Errorf("against itself: %d new, exit %d", n, g.ExitCode("low"))
	}
	if n := g.MarkNew(Gate{}, "empty"); n != len(g.Failures) || g.ExitCode("high") != 1 {
		t.Errorf("against nothing: %d new of %d", n, len(g.Failures))
	}
}

func TestGithubAnnotations(t *testing.T) {
	g := gate(t)
	var buf bytes.Buffer
	if err := g.Write(&buf, "github", "high", "test"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "::error file=manifests/app.yaml,line=1,title=Flightbay k8s.pod-security-admission::") {
		t.Errorf("no error annotation for the high failure:\n%s", out)
	}
	if !strings.Contains(out, "::warning file=manifests/app.yaml,line=9,") {
		t.Errorf("no warning annotation for the medium probe failure:\n%s", out)
	}
}

func TestFixPack(t *testing.T) {
	root := sample(t)
	_, ws := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "manifests")}})
	g := Evaluate(ws)
	dir := t.TempDir()
	res, err := WriteFixPack(dir, g, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Fatal("no fix files written")
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(readme), "k8s.probes") || !strings.Contains(string(readme), "manifests/app.yaml:9") {
		t.Errorf("README does not list the probe check and its source:\n%s", readme)
	}
	for _, f := range res.Files {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if _, _, err := parseManifests(b); err != nil && err != errNotK8s {
			t.Errorf("%s is not valid YAML: %v", f, err)
		}
	}
	np, err := os.ReadFile(filepath.Join(dir, "k8s-netpol-default-deny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(np), "namespace: demo") {
		t.Errorf("default-deny fix should have the namespace filled in:\n%s", np)
	}
	found := false
	for _, p := range res.Proposals {
		if strings.HasPrefix(p, "proposals/manifests/") && strings.Contains(p, "netpol-default-deny") {
			found = true
		}
	}
	if !found {
		t.Errorf("proposals = %v, want a NetworkPolicy next to manifests/", res.Proposals)
	}
	// the repository is untouched
	if entries, _ := os.ReadDir(filepath.Join(root, "manifests")); len(entries) != 3 {
		t.Errorf("the scanned repository changed: %d files in manifests/", len(entries))
	}
}

func TestGitInfoAndBaseRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := sample(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("remote", "add", "origin", "git@github.com:example/repo-sample.git")
	run("add", ".")
	run("commit", "-q", "-m", "base")
	// the change adds a privileged container
	p := filepath.Join(root, "manifests", "app.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "        image: nginx:1.27\n", "        image: nginx:1.27\n        securityContext:\n          privileged: true\n", 1)), 0o644)
	run("commit", "-q", "-am", "privileged")

	o := Options{Paths: []string{filepath.Join(root, "manifests")}}
	r, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	h := r.Header
	if h.Repo.Commit == "" || h.Repo.Branch != "main" || h.Repo.Dirty || NormalizeRepoURL(h.Repo.Remote) != "github.com/example/repo-sample" {
		t.Errorf("repo info = %+v", h.Repo)
	}
	base, err := RunAtRef(o, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := Load(r.Bundle)
	bws, _ := Load(base.Bundle)
	g, bg := Evaluate(ws), Evaluate(bws)
	n := g.MarkNew(bg, "HEAD~1")
	if n == 0 {
		t.Fatal("the privileged container should be a new failure")
	}
	for _, f := range g.Reported() {
		if f.Item.Name != "web" {
			t.Errorf("new failure on %s, want only on the changed Deployment web: %+v", f.Object(), f)
		}
	}
	var sawPriv bool
	for _, f := range g.Reported() {
		if strings.Contains(f.Check, "privileged") || strings.Contains(f.Detail, "privileged") {
			sawPriv = true
		}
	}
	if !sawPriv {
		t.Errorf("new failures %v do not include the privileged container", g.Reported())
	}
	if out, _ := exec.Command("git", "-C", root, "worktree", "list").Output(); strings.Count(string(out), "\n") != 1 {
		t.Errorf("temporary worktree left behind:\n%s", out)
	}
	_ = posture.Fail
}

func TestNormalizeRepoURL(t *testing.T) {
	want := "github.com/org/repo"
	for _, u := range []string{"https://github.com/org/repo.git", "git@github.com:org/repo.git", "ssh://git@github.com/org/repo", "https://user:tok@github.com/org/repo/", "HTTPS://GitHub.com/Org/Repo"} {
		if got := NormalizeRepoURL(u); got != want {
			t.Errorf("%s -> %s", u, got)
		}
	}
	if got := safeRemote("https://user:tok@github.com/org/repo.git"); strings.Contains(got, "tok") {
		t.Errorf("safeRemote kept the token: %s", got)
	}
}
