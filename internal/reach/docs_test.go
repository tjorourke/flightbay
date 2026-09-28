package reach

import (
	"net/url"
	"testing"

	"github.com/tjorourke/flightbay/internal/solodocs"
)

func allowedDocsHost(t *testing.T, what, link string) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" {
		t.Errorf("%s: docs link %q is not an https URL", what, link)
		return
	}
	for _, h := range solodocs.AllowedHosts {
		if u.Host == h {
			return
		}
	}
	t.Errorf("%s: docs link %q is not on an allowed host %v", what, link, solodocs.AllowedHosts)
}

// Every finding links to the field guide and to a docs page on an allowed host.
func TestFindingsHaveDocs(t *testing.T) {
	for _, d := range DocsLinks() {
		allowedDocsHost(t, "reach.DocsLinks", d)
	}
	n := 0
	for _, ws := range []struct{ name, fixes string }{{"open", ""}, {"ext-registry", "ext-registry"}} {
		w := incident(t, ws.fixes)
		if ws.name == "ext-registry" {
			w = add(t, w, artifactorySecret())
		}
		for _, a := range Evaluate(w).Agents {
			for _, f := range a.Findings {
				n++
				if f.Source == "" {
					t.Errorf("%s: finding %q has no guide", a.Name, f.Title)
				}
				if f.Docs == "" {
					t.Errorf("%s: finding %q has no docs link", a.Name, f.Title)
					continue
				}
				allowedDocsHost(t, a.Name+": "+f.Title, f.Docs)
			}
		}
	}
	if n == 0 {
		t.Fatal("the fixtures produced no findings")
	}
}
