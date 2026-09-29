package collect

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	dynfake "k8s.io/client-go/dynamic/fake"
	metafake "k8s.io/client-go/metadata/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
)

const canary = "FLIGHTBAY-CANARY-51c0de"

type fakeDiscovery struct {
	lists []*metav1.APIResourceList
	err   error
	minor string // server minor version, default 34
}

func (f *fakeDiscovery) ServerVersion() (*version.Info, error) {
	minor := f.minor
	if minor == "" {
		minor = "34"
	}
	return &version.Info{Major: "1", Minor: minor, GitVersion: "v1." + minor + ".1", Platform: "linux/arm64"}, nil
}

func (f *fakeDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, f.lists, f.err
}

type fakeLogs struct {
	mu      sync.Mutex // streams are read concurrently
	streams map[string]string
	queue   map[string][]string // responses returned in order before falling back to streams
	calls   []string
}

func (f *fakeLogs) Stream(_ context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	key := fmt.Sprintf("%s/%s/%s/%v", ns, pod, o.Container, o.Previous)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if !o.Timestamps {
		return nil, errors.New("timestamps must be requested")
	}
	if q := f.queue[key]; len(q) > 0 {
		f.queue[key] = q[1:]
		return io.NopCloser(strings.NewReader(q[0])), nil
	}
	s, ok := f.streams[key]
	if !ok {
		return nil, apierrors.NewBadRequest("previous terminated container not found")
	}
	return io.NopCloser(strings.NewReader(s)), nil
}

func res(name, kind string, namespaced bool, verbs ...string) metav1.APIResource {
	if len(verbs) == 0 {
		verbs = []string{"get", "list", "watch"}
	}
	return metav1.APIResource{Name: name, Kind: kind, Namespaced: namespaced, Verbs: verbs, ShortNames: nil}
}

func discovery() *fakeDiscovery {
	return &fakeDiscovery{lists: []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			res("pods", "Pod", true), res("pods/log", "Pod", true), res("secrets", "Secret", true),
			res("configmaps", "ConfigMap", true), res("namespaces", "Namespace", false), res("nodes", "Node", false),
			res("events", "Event", true), res("services", "Service", true), res("bindings", "Binding", true, "create"),
		}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{res("deployments", "Deployment", true)}},
		{GroupVersion: "gateway.networking.k8s.io/v1", APIResources: []metav1.APIResource{res("httproutes", "HTTPRoute", true)}},
	}}
}

var gvrs = map[schema.GroupVersionResource]string{
	{Version: "v1", Resource: "pods"}:                                           "PodList",
	{Version: "v1", Resource: "secrets"}:                                        "SecretList",
	{Version: "v1", Resource: "configmaps"}:                                     "ConfigMapList",
	{Version: "v1", Resource: "namespaces"}:                                     "NamespaceList",
	{Version: "v1", Resource: "nodes"}:                                          "NodeList",
	{Version: "v1", Resource: "events"}:                                         "EventList",
	{Version: "v1", Resource: "services"}:                                       "ServiceList",
	{Group: "apps", Version: "v1", Resource: "deployments"}:                     "DeploymentList",
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}: "HTTPRouteList",
}

func u(apiVersion, kind, ns, name string, extra map[string]any) *unstructured.Unstructured {
	md := map[string]any{"name": name, "labels": map[string]any{"app": strings.Split(name, "-")[0]}}
	if ns != "" {
		md["namespace"] = ns
	}
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": md}
	for k, v := range extra {
		obj[k] = v
	}
	return &unstructured.Unstructured{Object: obj}
}

func crashPod(ns, name string) *unstructured.Unstructured {
	return u("v1", "Pod", ns, name, map[string]any{
		"spec": map[string]any{"nodeName": "worker-1", "containers": []any{
			map[string]any{"name": "app", "env": []any{map[string]any{"name": "DB_PASSWORD", "value": canary}}},
		}},
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{
			map[string]any{"name": "app", "restartCount": int64(3), "ready": false,
				"state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}},
		}},
	})
}

func objects() []runtime.Object {
	return []runtime.Object{
		u("v1", "Namespace", "", "payments", nil), u("v1", "Namespace", "", "kube-system", nil),
		crashPod("payments", "checkout-1"),
		u("v1", "Pod", "kube-system", "coredns-1", map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "coredns"}}}}),
		u("v1", "Secret", "payments", "db", map[string]any{"type": "Opaque", "data": map[string]any{"password": base64.StdEncoding.EncodeToString([]byte(canary))}}),
		u("v1", "ConfigMap", "payments", "cfg", map[string]any{"data": map[string]any{"DB_PASSWORD": canary, "app.conf": "url=postgres://u:" + canary + "@db\nlevel=debug"}}),
		u("v1", "Node", "", "worker-1", nil),
		u("v1", "Event", "payments", "checkout-1.1", map[string]any{"reason": "BackOff", "message": "Back-off restarting failed container", "type": "Warning",
			"involvedObject": map[string]any{"kind": "Pod", "name": "checkout-1", "namespace": "payments"}}),
		u("v1", "Service", "payments", "checkout", nil),
		u("apps/v1", "Deployment", "payments", "checkout", nil),
		u("gateway.networking.k8s.io/v1", "HTTPRoute", "payments", "checkout-route", nil),
	}
}

func metaObjects() []runtime.Object {
	var out []runtime.Object
	for _, o := range objects() {
		uo := o.(*unstructured.Unstructured)
		out = append(out, &metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: uo.GetAPIVersion(), Kind: uo.GetKind()},
			ObjectMeta: metav1.ObjectMeta{Name: uo.GetName(), Namespace: uo.GetNamespace(), Labels: uo.GetLabels()},
		})
	}
	return out
}

type harness struct {
	fwd   PortForwarder
	dyn   *dynfake.FakeDynamicClient
	meta  *metafake.FakeMetadataClient
	logs  *fakeLogs
	disc  *fakeDiscovery
	typed TypedLister
}

func newHarness() *harness {
	scheme := runtime.NewScheme()
	metaScheme := runtime.NewScheme()
	_ = metav1.AddMetaToScheme(metaScheme)
	return &harness{
		dyn:  dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrs, objects()...),
		meta: metafake.NewSimpleMetadataClient(metaScheme, metaObjects()...),
		disc: discovery(),
		logs: &fakeLogs{streams: map[string]string{
			"payments/checkout-1/app/false": "2026-09-27T10:11:05Z Starting checkout version=2.8.1\n2026-09-27T10:11:06Z Authorization: Bearer " + canary + "\n",
			"payments/checkout-1/app/true":  "2026-09-27T10:07:12Z ERROR database connection timed out\n-----BEGIN PRIVATE KEY-----\n" + canary + "\n-----END PRIVATE KEY-----\n2026-09-27T10:08:42Z Process exiting\n",
		}},
	}
}

func (h *harness) clients() Clients {
	return Clients{Discovery: h.disc, Dynamic: h.dyn, Metadata: h.meta, Logs: h.logs, Forward: h.fwd, Typed: h.typed}
}

func run(t *testing.T, p *policy.Policy, h *harness, tweak func(*Collector)) (map[string][]byte, []string, *bundle.Reader) {
	t.Helper()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := bundle.NewWriter(&buf, bundle.WriterOptions{Compression: "fastest"})
	if err != nil {
		t.Fatal(err)
	}
	rd, _ := redact.New(nil)
	c := &Collector{Policy: p, Redactor: rd, Writer: w, Version: "test"}
	if tweak != nil {
		tweak(c)
	}
	if err := c.Begin([]string{"mesh1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CollectCluster(context.Background(), "mesh1", h.clients()); err != nil {
		t.Fatal(err)
	}
	if err := c.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := bundle.NewReader(&buf, bundle.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	var order []string
	for {
		m, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		files[m.Name] = m.Data
		order = append(order, m.Name)
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
	return files, order, r
}

func items(t *testing.T, files map[string][]byte, prefix string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for name, data := range files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		var l struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(data, &l); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out = append(out, l.Items...)
	}
	return out
}

func coverage(t *testing.T, files map[string][]byte) map[string]bundle.Coverage {
	out := map[string]bundle.Coverage{}
	for name, data := range files {
		if info, ok := bundle.ParsePath(name); ok && info.Kind == bundle.PathCoverage {
			var c bundle.Coverage
			_ = json.Unmarshal(data, &c)
			out[c.ID] = c
		}
	}
	return out
}

func listed(actions []ktesting.Action, resource string) bool {
	for _, a := range actions {
		if a.GetVerb() == "list" && a.GetResource().Resource == resource {
			return true
		}
	}
	return false
}

func standard(t *testing.T) *policy.Policy {
	p, _ := policy.Profile("standard")
	p.Namespaces = []string{"payments"}
	return p
}

func TestStandardCollectionScopedAndRedacted(t *testing.T) {
	h := newHarness()
	files, order, r := run(t, standard(t), h, nil)
	for name, data := range files {
		if bytes.Contains(data, []byte(canary)) {
			t.Fatalf("canary found in %s: %s", name, data)
		}
	}
	if order[0] != bundle.HeaderName || order[len(order)-1] != bundle.ManifestName {
		t.Fatalf("bad ordering %v", order)
	}
	pods := items(t, files, "clusters/mesh1/resources/core/v1/pods/")
	if len(pods) != 1 || pods[0]["metadata"].(map[string]any)["name"] != "checkout-1" {
		t.Fatalf("expected only payments pod, got %v", pods)
	}
	secrets := items(t, files, "clusters/mesh1/resources/core/v1/secrets/")
	if len(secrets) != 1 || secrets[0]["data"] != nil || secrets[0]["x-flightbay"].(map[string]any)["keys"] == nil {
		t.Fatalf("secret not reduced to key names: %v", secrets)
	}
	if len(items(t, files, "clusters/mesh1/resources/gateway.networking.k8s.io/v1/httproutes/")) != 1 {
		t.Fatal("httproute missing")
	}
	// Only the Namespace objects in scope are captured.
	if nss := items(t, files, "clusters/mesh1/resources/core/v1/namespaces/"); len(nss) != 1 || nss[0]["metadata"].(map[string]any)["name"] != "payments" {
		t.Fatalf("expected only the payments Namespace, got %v", nss)
	}
	for _, missing := range []string{"clusters/mesh1/resources/core/v1/nodes/", "clusters/mesh1/resources/core/v1/bindings/", "clusters/mesh1/resources/core/v1/events/"} {
		if len(items(t, files, missing)) != 0 {
			t.Fatalf("unexpected collection under %s", missing)
		}
	}
	if len(items(t, files, "clusters/mesh1/events/")) != 1 {
		t.Fatal("events not collected")
	}
	cur := files[bundle.LogPath("mesh1", "payments", "checkout-1", "app", "current", 1)]
	prev := files[bundle.LogPath("mesh1", "payments", "checkout-1", "app", "previous", 1)]
	if !bytes.Contains(cur, []byte("Starting checkout")) || !bytes.Contains(prev, []byte("database connection timed out")) || !bytes.Contains(prev, []byte("Process exiting")) {
		t.Fatalf("logs missing:\ncurrent=%s\nprevious=%s", cur, prev)
	}
	for _, c := range h.logs.calls {
		if strings.HasPrefix(c, "kube-system/") {
			t.Fatalf("logs read outside selected namespaces: %v", h.logs.calls)
		}
	}
	cov := coverage(t, files)
	if c := cov["mesh1.resource.core.v1.pods"]; c.State != bundle.StateComplete || c.Items != 1 || len(c.Chunks) != 1 {
		t.Fatalf("pods coverage %+v (all: %v)", c, cov)
	}
	if c := cov["mesh1.logs.payments.checkout-1.app.previous"]; c.State != bundle.StateComplete {
		t.Fatalf("previous log coverage %+v", c)
	}
	var disc []bundle.APIResource
	_ = json.Unmarshal(files[bundle.DiscoveryPath("mesh1", "apiresources.json")], &disc)
	for _, d := range disc {
		if d.Resource == "nodes" || d.Resource == "bindings" {
			t.Fatalf("discovery lists a resource outside the policy: %+v", d)
		}
	}
	if len(files[bundle.RedactionName]) == 0 || files[bundle.CollectionLog] == nil {
		t.Fatal("redaction report or collection log missing")
	}
	var rep redact.Report
	_ = json.Unmarshal(files[bundle.RedactionName], &rep)
	if rep.Total == 0 {
		t.Fatal("redaction report recorded nothing")
	}
	if !r.Manifest().Complete {
		t.Fatal("manifest should be complete")
	}
	var h1 bundle.Header
	_ = json.Unmarshal(files[bundle.HeaderName], &h1)
	if !strings.Contains(string(h1.Policy), `"payments"`) {
		t.Fatalf("resolved policy not recorded in header: %s", h1.Policy)
	}
}

func TestExcludedResourcesAreNeverRequested(t *testing.T) {
	h := newHarness()
	p := standard(t)
	p.ExcludeResources = []string{"configmaps"}
	p.Secrets = "omit"
	files, _, _ := run(t, p, h, nil)
	if listed(h.dyn.Actions(), "configmaps") || listed(h.dyn.Actions(), "secrets") || listed(h.meta.Actions(), "secrets") {
		t.Fatal("excluded resource was listed")
	}
	if len(items(t, files, "clusters/mesh1/resources/core/v1/configmaps/")) != 0 {
		t.Fatal("excluded data archived")
	}
}

func TestMetadataModeUsesMetadataAPI(t *testing.T) {
	h := newHarness()
	p, _ := policy.Profile("minimal")
	p.Namespaces = []string{"payments"}
	files, _, _ := run(t, p, h, nil)
	if listed(h.dyn.Actions(), "secrets") || listed(h.dyn.Actions(), "configmaps") || listed(h.dyn.Actions(), "pods") {
		t.Fatal("full objects were read in metadata mode")
	}
	if !listed(h.meta.Actions(), "secrets") {
		t.Fatal("metadata API not used")
	}
	secrets := items(t, files, "clusters/mesh1/resources/core/v1/secrets/")
	b, _ := json.Marshal(secrets)
	if len(secrets) != 1 || bytes.Contains(b, []byte("password")) || secrets[0]["kind"] != "Secret" || secrets[0]["apiVersion"] != "v1" {
		t.Fatalf("metadata-only secret wrong: %s", b)
	}
	for name := range files {
		if strings.Contains(name, "/logs/") {
			t.Fatalf("logs collected under minimal profile: %s", name)
		}
	}
}

func TestForbiddenIsRecordedNotFatal(t *testing.T) {
	h := newHarness()
	h.dyn.PrependReactor("list", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("RBAC denied"))
	})
	files, _, r := run(t, standard(t), h, nil)
	c := coverage(t, files)["mesh1.resource.core.v1.secrets"]
	if c.State != bundle.StateFailed || !strings.Contains(c.Reason, "forbidden") {
		t.Fatalf("expected failed coverage, got %+v", c)
	}
	if !bytes.Contains(files[bundle.CollectionLog], []byte("secrets")) || r.Manifest().Errors == 0 {
		t.Fatal("failure not recorded in collection log and manifest")
	}
	if len(items(t, files, "clusters/mesh1/resources/core/v1/pods/")) != 1 {
		t.Fatal("collection stopped after a failure")
	}
}

func TestChunkingAndLogTailTruncation(t *testing.T) {
	h := newHarness()
	var extra []runtime.Object
	for i := 0; i < 4; i++ {
		extra = append(extra, crashPod("payments", fmt.Sprintf("worker-%d", i)))
	}
	h.dyn = dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, append(objects(), extra...)...)
	long := ""
	for i := 0; i < 100; i++ {
		long += fmt.Sprintf("2026-09-27T10:00:%02dZ line %03d\n", i%60, i)
	}
	h.logs.streams["payments/checkout-1/app/false"] = long
	p := standard(t)
	p.Logs.MaxBytes = 300
	files, _, _ := run(t, p, h, func(c *Collector) { c.ChunkItems = 2 })
	cov := coverage(t, files)
	if c := cov["mesh1.resource.core.v1.pods"]; len(c.Chunks) != 3 || c.Items != 5 {
		t.Fatalf("expected 5 pods in 3 chunks, got %+v", c)
	}
	cur := files[bundle.LogPath("mesh1", "payments", "checkout-1", "app", "current", 1)]
	if len(cur) > 300 || !bytes.Contains(cur, []byte("line 099")) || bytes.Contains(cur, []byte("line 000")) {
		t.Fatalf("log not tail-truncated: %d bytes %s", len(cur), cur)
	}
	if c := cov["mesh1.logs.payments.checkout-1.app.current"]; c.State != bundle.StateTruncated {
		t.Fatalf("truncation not recorded: %+v", c)
	}
}

func TestClusterScopedOptInCollectsNodes(t *testing.T) {
	h := newHarness()
	p := standard(t)
	p.ClusterScoped = true
	p.Categories = append(p.Categories, policy.CatNodes)
	files, _, _ := run(t, p, h, nil)
	if len(items(t, files, "clusters/mesh1/resources/core/v1/nodes/")) != 1 {
		t.Fatal("nodes not collected after opt-in")
	}
	if ns := items(t, files, "clusters/mesh1/resources/core/v1/namespaces/"); len(ns) != 1 {
		t.Fatalf("namespace objects should be filtered to the selection: %v", ns)
	}
}

func TestDryRunReadsNoPayloads(t *testing.T) {
	h := newHarness()
	rd, _ := redact.New(nil)
	c := &Collector{Policy: standard(t), Redactor: rd}
	plan, err := c.DryRun(context.Background(), "mesh1", h.clients())
	if err != nil {
		t.Fatal(err)
	}
	if len(h.dyn.Actions()) != 0 || len(h.logs.calls) != 0 {
		t.Fatalf("dry run read data: %v", h.dyn.Actions())
	}
	names := strings.Join(plan.Resources, ",")
	if !strings.Contains(names, "pods") || strings.Contains(names, "nodes") || !strings.Contains(names, "httproutes.gateway.networking.k8s.io") {
		t.Fatalf("unexpected dry run resources %v", plan.Resources)
	}
}

func TestPartialDiscoveryIsRecorded(t *testing.T) {
	h := newHarness()
	h.disc.err = errors.New("unable to retrieve the complete list of server APIs: metrics.k8s.io/v1beta1: service unavailable")
	files, _, _ := run(t, standard(t), h, nil)
	if !bytes.Contains(files[bundle.CollectionLog], []byte("metrics.k8s.io")) {
		t.Fatal("partial discovery failure not recorded")
	}
}

func TestKubeletErrorBodyIsRetriedNotArchived(t *testing.T) {
	const kerr = "unable to retrieve container logs for containerd://1f7a26bf60d4\n"
	fast := func(c *Collector) { c.LogRetryDelay = time.Millisecond }

	// The container settles after one failed read: the real log is kept.
	h := newHarness()
	h.logs.queue = map[string][]string{"payments/checkout-1/app/true": {kerr}}
	files, _, _ := run(t, standard(t), h, fast)
	prev := files[bundle.LogPath("mesh1", "payments", "checkout-1", "app", "previous", 1)]
	if bytes.Contains(prev, []byte("unable to retrieve")) || !bytes.Contains(prev, []byte("database connection timed out")) {
		t.Fatalf("retry did not recover the log: %s", prev)
	}

	// It never settles: no log member, failed coverage with the reason.
	h = newHarness()
	h.logs.queue = map[string][]string{"payments/checkout-1/app/true": {kerr, kerr, kerr, kerr, kerr}}
	files, _, r := run(t, standard(t), h, fast)
	if _, ok := files[bundle.LogPath("mesh1", "payments", "checkout-1", "app", "previous", 1)]; ok {
		t.Fatal("kubelet error text archived as a log")
	}
	c := coverage(t, files)["mesh1.logs.payments.checkout-1.app.previous"]
	if c.State != bundle.StateFailed || !strings.Contains(c.Reason, "kubelet could not return the logs") {
		t.Fatalf("coverage %+v", c)
	}
	if r.Manifest().Errors == 0 {
		t.Fatal("failure not counted in the manifest")
	}

	// An app that legitimately prints that phrase among other lines is kept.
	h = newHarness()
	h.logs.streams["payments/checkout-1/app/false"] = "2026-09-27T10:00:00Z " + kerr + "2026-09-27T10:00:01Z next\n"
	files, _, _ = run(t, standard(t), h, fast)
	if cur := files[bundle.LogPath("mesh1", "payments", "checkout-1", "app", "current", 1)]; !bytes.Contains(cur, []byte("next")) {
		t.Fatalf("normal log dropped: %s", cur)
	}
}

func TestSlimCRDKeepsVersionAnnotationsOnly(t *testing.T) {
	out := SlimCRD(map[string]any{"metadata": map[string]any{"name": "gateways.gateway.networking.k8s.io", "annotations": map[string]any{
		"gateway.networking.k8s.io/bundle-version": "v1.5.1", "kubectl.kubernetes.io/last-applied-configuration": "{huge}"}},
		"spec": map[string]any{"versions": []any{map[string]any{"name": "v1", "schema": map[string]any{"openAPIV3Schema": map[string]any{}}}}}})
	ann := out["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann["gateway.networking.k8s.io/bundle-version"] != "v1.5.1" || ann["kubectl.kubernetes.io/last-applied-configuration"] != nil {
		t.Fatalf("annotations %v", ann)
	}
	if _, ok := out["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"]; ok {
		t.Fatal("schema kept")
	}
}
