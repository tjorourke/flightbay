package posture

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/analyze"
	"github.com/tjorourke/flightbay/internal/features"
	"github.com/tjorourke/flightbay/internal/reach"
	"github.com/tjorourke/flightbay/internal/solodocs"
)

// Checks with no docs page: nothing on docs.solo.io covers them and the
// upstream page they would link to is on a host we do not link docs to.
var noDocs = map[string]string{
	"k8s.helm-releases": "Helm release state; the only reference is helm.sh, already the guide link",
}

// allChecks evaluates the rich fixture and an ingress fixture with Agent
// reach on, which between them produce every check the build has.
func allChecks(t *testing.T) []Check {
	t.Helper()
	defer features.Set(nil)
	features.Set(map[string]bool{features.Reach: true})
	var out []Check
	seen := map[string]bool{}
	for _, r := range []Report{Evaluate(richFixture(t)), Evaluate(ingressWS(t, map[string]any{"name": "http", "port": float64(80), "protocol": "HTTP"}))} {
		for _, c := range r.Checks {
			if !seen[c.ID] {
				seen[c.ID] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func allowedHost(link string) bool {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" {
		return false
	}
	for _, h := range solodocs.AllowedHosts {
		if u.Host == h {
			return true
		}
	}
	return false
}

// Every check links to a docs page, on docs.solo.io unless it is about plain
// Kubernetes (area kubernetes, or a Kubernetes object such as a
// NetworkPolicy), where kubernetes.io is allowed.
func TestDocsLinksAllowedHosts(t *testing.T) {
	checks := allChecks(t)
	if len(checks) < 135 {
		t.Fatalf("only %d checks produced; the fixture no longer covers every area", len(checks))
	}
	solo := 0
	for _, c := range checks {
		if c.Docs == "" {
			if _, ok := noDocs[c.ID]; !ok {
				t.Errorf("%s: no docs link", c.ID)
			}
			continue
		}
		if !allowedHost(c.Docs) {
			t.Errorf("%s: docs link %q is not on %v", c.ID, c.Docs, solodocs.AllowedHosts)
		}
		if solodocs.IsSolo(c.Docs) {
			solo++
		} else if c.Area != "kubernetes" && c.ID != "agentic.metadata-endpoint-blocked" {
			t.Errorf("%s: %s checks link to the Solo docs, not %q", c.ID, c.Area, c.Docs)
		}
		if c.Docs == c.Source && solodocs.IsSolo(c.Docs) {
			t.Errorf("%s: docs link repeats the guide link %q", c.ID, c.Docs)
		}
	}
	for _, d := range reach.DocsLinks() {
		if !allowedHost(d) {
			t.Errorf("reach: docs link %q is not on an allowed host", d)
		}
	}
	for _, d := range analyze.DocsLinks() {
		if !allowedHost(d) || !solodocs.IsSolo(d) {
			t.Errorf("analyze: docs link %q is not on docs.solo.io", d)
		}
	}
	t.Logf("%d checks, %d link to docs.solo.io", len(checks), solo)
}

// FLIGHTBAY_CHECK_DOCS_LINKS=1 go test ./internal/posture -run DocsLinks -v
//
// Fetches every distinct docs and guide link (GET, following redirects) and
// fails on anything that does not end in a 200, or that lands on a redirect
// stub page instead of the article.
func TestDocsLinksLive(t *testing.T) {
	if os.Getenv("FLIGHTBAY_CHECK_DOCS_LINKS") == "" {
		t.Skip("set FLIGHTBAY_CHECK_DOCS_LINKS=1 to fetch every docs and guide link")
	}
	set := map[string]bool{reach.Guide: true}
	for _, c := range allChecks(t) {
		for _, u := range []string{c.Source, c.Docs} {
			if u != "" {
				set[strings.SplitN(u, "#", 2)[0]] = true
			}
		}
	}
	for _, u := range append(reach.DocsLinks(), analyze.DocsLinks()...) {
		set[u] = true
	}
	var links []string
	for u := range set {
		links = append(links, u)
	}
	sort.Strings(links)

	client := &http.Client{Timeout: 30 * time.Second}
	stub := regexp.MustCompile(`<title>\s*https?://`)
	var mu sync.Mutex
	var failed []string
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, u := range links {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			msg := ""
			for try := 0; try < 3; try++ {
				msg = fetch(client, u, stub)
				if msg == "" || !strings.HasPrefix(msg, "error") {
					break
				}
				time.Sleep(time.Second)
			}
			if msg != "" {
				mu.Lock()
				failed = append(failed, u+": "+msg)
				mu.Unlock()
			}
		}(u)
	}
	wg.Wait()
	sort.Strings(failed)
	fmt.Printf("checked %d distinct links, %d failed\n", len(links), len(failed))
	for _, f := range failed {
		fmt.Println("  FAIL " + f)
	}
	if len(failed) > 0 {
		t.Fatalf("%d of %d links failed", len(failed), len(links))
	}
}

func fetch(client *http.Client, u string, stub *regexp.Regexp) string {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "flightbay-docs-link-check")
	resp, err := client.Do(req)
	if err != nil {
		return "error " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("HTTP %d (at %s)", resp.StatusCode, resp.Request.URL)
	}
	if stub.Match(body) {
		return "a redirect stub page, not an article (at " + resp.Request.URL.String() + ")"
	}
	return ""
}
