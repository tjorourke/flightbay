package web

import (
	"regexp"
	"strings"
	"testing"
)

// These are shipped features, not optional development-only scripts. A build
// must retain their entry points as well as their source files.
func TestAmbientFeatureAssetsAreBundledAndLoaded(t *testing.T) {
	index, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"ambient.js", "ambient-configure.js", "ambient-map.js",
		"global-publication.js", "bookinfo-demo.js", "request-path.js",
		"request-path-map.js", "request-path.css", "request-path-transport.css", "http-test.js",
		"http-mesh-test.js", "http-mesh-test.css",
	} {
		if !strings.Contains(string(index), `"static/`+name+`"`) {
			t.Errorf("feature asset %s is no longer loaded by the desk", name)
		}
	}
	for _, match := range regexp.MustCompile(`(?:src|href)="static/([^"]+)"`).FindAllSubmatch(index, -1) {
		name := string(match[1])
		if _, err := FS.ReadFile(name); err != nil {
			t.Errorf("referenced asset %s is not embedded: %v", name, err)
		}
	}
}
