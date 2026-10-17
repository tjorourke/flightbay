package collect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/policy"
)

func TestMultipleKubeconfigsResolveWithoutShadowing(t *testing.T) {
	dir := t.TempDir()
	paths := []string{}
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(dir, name)
		config := fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: %s\nclusters:\n- name: %s\n  cluster: {server: 'https://%s.example'}\ncontexts:\n- name: %s\n  context: {cluster: %s, user: %s}\nusers:\n- name: %s\n  user: {token: private-test-token}\n", name, name, name, name, name, name, name)
		if err := os.WriteFile(path, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	joined := strings.Join(paths, string(os.PathListSeparator))
	got, err := ResolveContexts(joined, []string{"one", "two"})
	if err != nil || len(got) != 2 {
		t.Fatalf("contexts: %v %v", got, err)
	}
	p, _ := policy.Profile("standard")
	for _, name := range got {
		cfg, err := RestConfig(joined, name, p, "test")
		if err != nil || cfg.Host != "https://"+name+".example" {
			t.Fatalf("wrong target for %s: %v", name, err)
		}
	}
	b, _ := os.ReadFile(paths[0])
	duplicate := filepath.Join(dir, "duplicate")
	_ = os.WriteFile(duplicate, b, 0600)
	if _, err := ResolveContexts(joined+string(os.PathListSeparator)+duplicate, []string{"one"}); err == nil || !strings.Contains(err.Error(), "duplicate kubeconfig") {
		t.Fatal("silently shadowed conflicting config names")
	}
}
