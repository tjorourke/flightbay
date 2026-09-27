package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
)

func resolve(t *testing.T, args ...string) (*policy.Policy, error) {
	t.Helper()
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	var pf PolicyFlags
	pf.Register(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return pf.Resolve(fs)
}

func TestFlagsRefineProfileAndConfig(t *testing.T) {
	p, err := resolve(t, "--profile", "standard", "--namespaces", "payments,istio-*", "--exclude-resources", "configmaps", "--since", "2h", "--no-logs", "--case", "123")
	if err != nil {
		t.Fatal(err)
	}
	if p.Has(policy.CatLogs) || p.Logs.Since != "2h" || p.Case != "123" || len(p.Namespaces) != 2 || p.ResourceAllowed("configmaps", "", "ConfigMap", true) {
		t.Fatalf("flags not applied: %+v", p)
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	_ = os.WriteFile(cfg, []byte("profile: standard\nnamespaces: [payments]\nexcludeResources: [secrets]\nlogs:\n  since: 6h\n"), 0o600)
	p, err = resolve(t, "--config", cfg, "--exclude-resources", "configmaps")
	if err != nil {
		t.Fatal(err)
	}
	if p.Logs.Since != "6h" || len(p.Namespaces) != 1 {
		t.Fatalf("unchanged flags overrode the config: %+v", p)
	}
	if p.ResourceAllowed("secrets", "", "Secret", true) || p.ResourceAllowed("configmaps", "", "ConfigMap", true) {
		t.Fatalf("exclusions from config and flags should both apply: %v", p.ExcludeResources)
	}
	p, _ = resolve(t, "--profile", "custom", "--collect", "objects,events")
	if !p.Has(policy.CatObjects) || p.Has(policy.CatLogs) {
		t.Fatalf("--collect not applied: %v", p.Categories)
	}
	if _, err := resolve(t, "--secrets", "values"); err == nil {
		t.Fatal("secret values must be rejected")
	}
	p, _ = resolve(t, "--portforward", "--collect", "proxies")
	if !p.PortForward {
		t.Fatal("--portforward not applied")
	}
}

func testBundle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, _ := bundle.NewWriter(&buf, bundle.WriterOptions{})
	p, _ := policy.Profile("standard")
	pb, _ := json.Marshal(p)
	_ = w.WriteHeader(bundle.Header{Case: "01234567", Contexts: []string{"mesh1"}, Policy: pb, CaptureStart: time.Now(), Unavailable: []string{"proxies: needs port-forward"}})
	_ = w.AddJSON(bundle.ResourcePath("mesh1", "", "v1", "pods", 1), map[string]any{"items": []any{map[string]any{"kind": "Pod"}}})
	_ = w.Add(bundle.LogPath("mesh1", "payments", "a", "app", "current", 1), []byte("2026-09-27T10:00:00Z x\n"))
	_ = w.Coverage(bundle.Coverage{ID: "mesh1.resource.core.v1.pods", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", Items: 1, State: bundle.StateComplete})
	_ = w.Coverage(bundle.Coverage{ID: "mesh1.resource.core.v1.secrets", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "secrets", State: bundle.StateFailed, Reason: "forbidden"})
	_ = w.Add(bundle.RedactionName, []byte(`{"total":7,"locations":{"clusters/mesh1/logs/x":{"authorization":7}}}`))
	_ = w.Add(bundle.CollectionLog, []byte(`{"operation":"list","target":"secrets","result":"failed","error":"forbidden"}`+"\n"))
	_ = w.Close(bundle.Manifest{Complete: true})
	return buf.Bytes()
}

func TestInspect(t *testing.T) {
	var out bytes.Buffer
	err := Inspect(&out, bytes.NewReader(testBundle(t)), InspectOptions{Contents: true, Redactions: true, Policy: true})
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"Case: 01234567", "Verified: yes", "pods", "complete", "secrets", "failed: forbidden", "Redactions: 7", "authorization", "proxies: needs port-forward", "Secret values: never collected", "clusters/mesh1/logs/payments/a/app/current/part-000001.log"} {
		if !strings.Contains(s, want) {
			t.Fatalf("inspect output missing %q:\n%s", want, s)
		}
	}
	data := testBundle(t)
	out.Reset()
	if err := Inspect(&out, bytes.NewReader(data[:len(data)/2]), InspectOptions{}); err == nil {
		t.Fatalf("truncated bundle passed inspection:\n%s", out.String())
	}
}

func TestExtract(t *testing.T) {
	dir := t.TempDir()
	if err := Extract(bytes.NewReader(testBundle(t)), dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "clusters/mesh1/resources/core/v1/pods/part-000001.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, bundle.ManifestName)); err != nil {
		t.Fatal(err)
	}
}

func TestDeskClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tk" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/logs/search":
			if r.URL.Query().Get("q") != "level:error" {
				t.Errorf("query not passed: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"total":1,"records":[{"ts":"2026-09-27T10:07:12Z","cluster":"mesh1","namespace":"payments","pod":"p","container":"app","level":"error","msg":"boom"}],"loading":true,"message":""}`))
		case "/api/findings":
			_, _ = w.Write([]byte(`[{"severity":"critical","title":"p is crash looping","cluster":"mesh1","provisional":true}]`))
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := Query(&out, srv.URL, "tk", "level:error", 50); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "payments/p/app") || !strings.Contains(out.String(), "boom") || !strings.Contains(out.String(), "still loading") {
		t.Fatalf("query output %q", out.String())
	}
	out.Reset()
	if err := Findings(&out, srv.URL, "tk"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "CRITICAL") || !strings.Contains(out.String(), "provisional") {
		t.Fatalf("findings output %q", out.String())
	}
	if err := Query(&out, srv.URL, "bad", "x", 1); err == nil {
		t.Fatal("expected auth error")
	}
}
