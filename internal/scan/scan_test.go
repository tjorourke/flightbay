package scan

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/posture"
	"github.com/tjorourke/flightbay/internal/store"
)

const sampleURL = "https://github.com/example/repo-sample.git"

// sample copies testdata/repo-sample into a temporary directory that is not
// a Git repository, so its root is the scan root.
func sample(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "repo-sample")
	if err := copyDir("testdata/repo-sample", dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

func scanWS(t *testing.T, o Options) (*Result, *store.Workspace) {
	t.Helper()
	if o.RepoURL == "" {
		o.RepoURL = sampleURL
	}
	r, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := Load(r.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return r, ws
}

func get(t *testing.T, ws *store.Workspace, group, resource, ns, name string) *store.Object {
	t.Helper()
	o := ws.Get(ws.Clusters()[0], group, resource, ns, name)
	if o == nil {
		var have []string
		for _, x := range ws.AllObjects(ws.Clusters()[0]) {
			have = append(have, x.Kind+" "+x.Namespace+"/"+x.Name)
		}
		t.Fatalf("%s %s/%s not in the scan; have %s", resource, ns, name, strings.Join(have, ", "))
	}
	return o
}

func ann(o *store.Object) map[string]any { return asMap(asMap(o.Obj["metadata"])["annotations"]) }

func input(r *Result, source string) *bundle.Coverage {
	for i := range r.Inputs {
		if r.Inputs[i].Source == source {
			return &r.Inputs[i]
		}
	}
	return nil
}

func TestParseManifests(t *testing.T) {
	docs, skipped, err := parseManifests([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: a}\n---\n# comment only\n---\nfoo: bar\n---\napiVersion: v1\nkind: List\nitems:\n- apiVersion: v1\n  kind: Service\n  metadata: {name: b}\n- kind: NoVersion\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || asStr(docs[0].obj["kind"]) != "ConfigMap" || asStr(docs[1].obj["kind"]) != "Service" {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].line != 1 || docs[1].line != 12 {
		t.Errorf("lines = %d, %d; want 1, 12", docs[0].line, docs[1].line)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (foo: bar and the item without apiVersion)", skipped)
	}
	if _, _, err := parseManifests([]byte("name: build\non: [push]\n")); err != errNotK8s {
		t.Errorf("non-Kubernetes YAML: err = %v, want errNotK8s", err)
	}
	if docs, _, err := parseManifests([]byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"x"}}`)); err != nil || len(docs) != 1 {
		t.Errorf("JSON manifest: %v %v", docs, err)
	}
}

func TestYAMLDiscovery(t *testing.T) {
	root := sample(t)
	r, ws := scanWS(t, Options{Paths: []string{root}})
	d := get(t, ws, "apps", "deployments", "demo", "web")
	if a := ann(d); a[SourceAnnotation] != "manifests/app.yaml" || a[LineAnnotation] != "9" || a[RenderedByAnnotation] != "yaml" {
		t.Errorf("source annotations = %v", a)
	}
	get(t, ws, "", "configmaps", "demo", "web-config") // from kind: List
	sa := get(t, ws, "", "serviceaccounts", "demo", "web")
	if ann(sa)[LineAnnotation] != "12" {
		t.Errorf("List item line = %v, want 12", ann(sa)[LineAnnotation])
	}
	if c := input(r, "manifests/not-k8s.yaml"); c == nil || c.State != bundle.StateExcluded || !strings.Contains(c.Reason, "not a Kubernetes manifest") {
		t.Errorf("not-k8s.yaml coverage = %+v", c)
	}
	for _, o := range ws.AllObjects(ws.Clusters()[0]) {
		if o.Namespaced && o.Namespace == "" {
			t.Errorf("%s %s has no namespace", o.Kind, o.Name)
		}
		if s := asStr(ann(o)[SourceAnnotation]); s == "" && asStr(ann(o)[store.SynthesisedAnnotation]) != "true" {
			t.Errorf("%s %s/%s has no source annotation", o.Kind, o.Namespace, o.Name)
		}
	}
	h := ws.Header()
	if !h.Declared() || h.Repo == nil || h.Repo.Name != "repo-sample" || ws.Clusters()[0] != "repo_repo-sample" {
		t.Errorf("header = %+v repo = %+v clusters = %v", h, h.Repo, ws.Clusters())
	}
	// discovery comes from the kinds found, with built-in scope
	var ns, dep bool
	for _, res := range ws.Resources(ws.Clusters()[0]) {
		if res.Resource == "namespaces" && !res.Namespaced {
			ns = true
		}
		if res.Resource == "deployments" && res.Group == "apps" && res.Namespaced {
			dep = true
		}
	}
	if !ns || !dep {
		t.Errorf("apiresources: namespaces cluster-scoped %v, deployments namespaced %v", ns, dep)
	}
}

func TestHelmChart(t *testing.T) {
	root := sample(t)
	r, ws := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "charts")}, Release: "rel", Namespace: "apps"})
	d := get(t, ws, "apps", "deployments", "apps", "rel-api")
	if n := asMap(d.Obj["spec"])["replicas"]; n != float64(1) {
		t.Errorf("replicas = %v, want 1 from values.yaml", n)
	}
	if a := ann(d); a[SourceAnnotation] != "charts/mychart/templates/deployment.yaml" || !strings.HasPrefix(asStr(a[RenderedByAnnotation]), "helm charts/mychart") {
		t.Errorf("deployment annotations = %v", a)
	}
	cm := get(t, ws, "", "configmaps", "apps", "rel-sub")
	if msg := asMap(cm.Obj["data"])["message"]; msg != "hello" {
		t.Errorf("subchart message = %v, want the parent's value hello", msg)
	}
	if ann(cm)[SourceAnnotation] != "charts/mychart/charts/sub/templates/cm.yaml" {
		t.Errorf("subchart source = %v", ann(cm)[SourceAnnotation])
	}
	if o := ws.Get(ws.Clusters()[0], "", "pods", "apps", "rel-test"); o != nil {
		t.Error("helm test hook pod should not be part of declared state")
	}
	c := input(r, "charts/mychart")
	if c == nil || c.State != bundle.StateComplete || !strings.Contains(c.Reason, "missingdep") {
		t.Errorf("chart coverage = %+v, want complete with the missing dependency noted", c)
	}

	// values files, --set and --env
	vals := filepath.Join(t.TempDir(), "extra.yaml")
	os.WriteFile(vals, []byte("image: busybox:1.37\n"), 0o644)
	_, ws2 := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "charts")}, Env: "prod", Values: []string{vals}, Set: []string{"sub.message=from-set"}})
	d2 := get(t, ws2, "apps", "deployments", "default", "mychart-api")
	spec := asMap(d2.Obj["spec"])
	if spec["replicas"] != float64(3) {
		t.Errorf("--env prod replicas = %v, want 3 from values-prod.yaml", spec["replicas"])
	}
	img := asStr(asMap(asList(asMap(asMap(spec["template"])["spec"])["containers"])[0])["image"])
	if img != "busybox:1.37" {
		t.Errorf("image = %s, want the --values override", img)
	}
	if msg := asMap(get(t, ws2, "", "configmaps", "default", "mychart-sub").Obj["data"])["message"]; msg != "from-set" {
		t.Errorf("--set message = %v", msg)
	}
	if b, _ := json.Marshal(ws2.Header().Repo.Options); strings.Contains(string(b), "from-set") {
		t.Errorf("--set values must not be recorded: %s", b)
	}
}

func TestKustomize(t *testing.T) {
	root := sample(t)
	r, ws := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "kustomize")}})
	get(t, ws, "apps", "deployments", "worker-dev", "worker")
	prod := get(t, ws, "apps", "deployments", "worker-prod", "worker")
	if n := asMap(prod.Obj["spec"])["replicas"]; n != float64(4) {
		t.Errorf("prod replicas = %v, want 4 from the patch", n)
	}
	if a := ann(prod); a[SourceAnnotation] != "kustomize/base/deployment.yaml" || a[LineAnnotation] != "1" || a[RenderedByAnnotation] != "kustomize kustomize/overlays/prod" {
		t.Errorf("annotations = %v", a)
	}
	if _, ok := ann(prod)["config.kubernetes.io/origin"]; ok {
		t.Error("the origin annotation should be removed")
	}
	if input(r, "kustomize/base") != nil {
		t.Error("the base should be built only through its overlays")
	}
	// --env picks one overlay
	r2, ws2 := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "kustomize")}, Env: "prod"})
	if ws2.Get(ws2.Clusters()[0], "apps", "deployments", "worker-dev", "worker") != nil {
		t.Error("--env prod should skip overlays/dev")
	}
	if c := input(r2, "kustomize/overlays/dev"); c == nil || c.State != bundle.StateExcluded {
		t.Errorf("dev overlay coverage = %+v", c)
	}
	if len(r2.Picked) == 0 {
		t.Error("--env should report what it picked")
	}
	// --overlay
	_, ws3 := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "kustomize")}, Overlays: []string{filepath.Join(root, "kustomize/overlays/dev")}})
	if ws3.Get(ws3.Clusters()[0], "apps", "deployments", "worker-prod", "worker") != nil {
		t.Error("--overlay dev should build only dev")
	}
}

func TestArgoApplication(t *testing.T) {
	root := sample(t)
	r, ws := scanWS(t, Options{Paths: []string{root}})
	d := get(t, ws, "apps", "deployments", "guestbook", "guestbook-ui")
	if a := ann(d); a[RenderedByAnnotation] != "argocd Application argocd/guestbook" || a[SourceAnnotation] != "apps/guestbook/deploy.yaml" {
		t.Errorf("annotations = %v", a)
	}
	found := false
	for _, c := range r.Inputs {
		if strings.Contains(c.Reason, "argocd/external") && strings.Contains(c.Reason, "unresolved source") {
			found = true
		}
	}
	if !found {
		t.Error("the Application pointing at another repository should be an unresolved source in coverage")
	}
	// without a matching repo URL the in-repo Application is unresolved too
	r2, ws2 := scanWS(t, Options{Paths: []string{root}, RepoURL: "https://example.com/other.git"})
	if ws2.Get(ws2.Clusters()[0], "apps", "deployments", "guestbook", "guestbook-ui") != nil {
		t.Error("guestbook should not get the Application's namespace when its source is another repository")
	}
	_ = r2
}

func TestSecretRedaction(t *testing.T) {
	root := sample(t)
	r, ws := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "manifests")}})
	raw := string(bundleText(t, r.Bundle))
	for _, v := range []string{"c3VwZXJzZWNyZXQ=", "supersecret", "plain-text-token-value", "hunter2"} {
		if strings.Contains(raw, v) {
			t.Errorf("bundle contains the secret value %q", v)
		}
	}
	s := get(t, ws, "", "secrets", "demo", "web-creds")
	if s.Obj["data"] != nil || s.Obj["stringData"] != nil {
		t.Error("Secret data must not be kept")
	}
	keys := map[string]float64{}
	for _, k := range asList(asMap(s.Obj["x-flightbay"])["keys"]) {
		keys[asStr(asMap(k)["name"])] = asMap(k)["bytes"].(float64)
	}
	if keys["password"] != 11 || keys["token"] != 22 {
		t.Errorf("secret keys = %v, want password 11 bytes and token 22", keys)
	}
	if ann(s)[SourceAnnotation] != "manifests/app.yaml" {
		t.Error("the Secret keeps its source annotation")
	}
	cm := get(t, ws, "", "configmaps", "demo", "web-config")
	if v := asMap(cm.Obj["data"])["DB_PASSWORD"]; v != "[REDACTED]" {
		t.Errorf("ConfigMap DB_PASSWORD = %v, want redacted", v)
	}
	if v := asMap(cm.Obj["data"])["LOG_LEVEL"]; v != "info" {
		t.Errorf("ConfigMap LOG_LEVEL = %v, want kept", v)
	}
}

// bundleText decompresses a bundle to its tar stream, for grepping.
func bundleText(t *testing.T, b []byte) []byte {
	t.Helper()
	rd, err := bundle.NewReader(bytes.NewReader(b), bundle.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for {
		m, err := rd.Next()
		if err != nil {
			break
		}
		out.Write(m.Data)
	}
	if err := rd.Verify(); err != nil {
		t.Fatalf("bundle does not verify: %v", err)
	}
	return out.Bytes()
}

func TestDeclaredStateChecks(t *testing.T) {
	root := sample(t)
	_, ws := scanWS(t, Options{Root: root, Paths: []string{filepath.Join(root, "manifests")}})
	g := Evaluate(ws)
	byID := map[string]posture.Check{}
	for _, c := range g.Checks {
		byID[c.ID] = c
	}
	for _, id := range []string{"k8s.node-health", "k8s.helm-releases", "k8s.version-supported", "k8s.cert-expiry"} {
		c := byID[id]
		if c.Status != posture.NA || !strings.HasPrefix(c.Reason, posture.DeclaredWhy) {
			t.Errorf("%s = %s %q, want N/A with %q", id, c.Status, c.Reason, posture.DeclaredWhy)
		}
	}
	for _, c := range g.Checks {
		if strings.Contains(c.Reason, "not captured") {
			t.Errorf("%s says %q on a repo scan", c.ID, c.Reason)
		}
	}
	// pod checks run on the workload template and report on the workload
	probes := byID["k8s.probes"]
	if probes.Status == posture.NA {
		t.Fatalf("k8s.probes should run on templates: %s", probes.Reason)
	}
	for _, it := range probes.Items {
		if it.Kind != "Deployment" || it.Name != "web" {
			t.Errorf("probe item = %+v, want Deployment web", it)
		}
	}
	// the failures carry the file and line
	var f *Failure
	for i := range g.Failures {
		if g.Failures[i].Check == "k8s.probes" {
			f = &g.Failures[i]
		}
	}
	if f == nil || f.File != "manifests/app.yaml" || f.Line != 9 {
		t.Errorf("probe failure = %+v, want manifests/app.yaml:9", f)
	}
}
