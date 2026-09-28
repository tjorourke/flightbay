package apiserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

const token = "test-token"

var (
	podsRes   = bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true, ShortNames: []string{"po"}}
	svcRes    = bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true, ShortNames: []string{"svc"}}
	deployRes = bundle.APIResource{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true, ShortNames: []string{"deploy"}}
	routeRes  = bundle.APIResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true}
)

func podObj(ns, name, app string, containers ...string) map[string]any {
	var cs, st []any
	for _, c := range containers {
		cs = append(cs, map[string]any{"name": c, "image": "example/" + c + ":1"})
		st = append(st, map[string]any{"name": c, "ready": true, "restartCount": float64(2), "state": map[string]any{"running": map[string]any{}}})
	}
	return map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": "uid-" + name, "labels": map[string]any{"app": app}, "creationTimestamp": "2026-09-27T09:56:00Z"},
		"spec":     map[string]any{"nodeName": "worker-1", "containers": cs},
		"status":   map[string]any{"phase": "Running", "podIP": "10.42.0.9", "containerStatuses": st}}
}

func setup(t *testing.T, complete bool) (*store.Workspace, *httptest.Server) {
	t.Helper()
	ws := store.New()
	p, _ := policy.Profile("standard")
	p.ExcludeResources = []string{"configmaps"}
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"mesh1"}, Policy: b, CaptureStart: time.Date(2026, 9, 27, 10, 12, 0, 0, time.UTC)})
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseLoading })
	ws.SetVersion("mesh1", json.RawMessage(`{"major":"1","minor":"34","gitVersion":"v1.34.1"}`))
	ws.RegisterResources("mesh1", []bundle.APIResource{podsRes, svcRes, deployRes, routeRes, {Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespaced: true}})
	ws.AddObjects("mesh1", podsRes, []map[string]any{
		podObj("payments", "checkout-1", "checkout", "app"),
		podObj("payments", "sidecar-1", "other", "app", "proxy"),
		podObj("istio-system", "istiod-1", "istiod", "discovery"),
	}, "c1")
	ws.AddObjects("mesh1", deployRes, []map[string]any{{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "checkout", "namespace": "payments", "creationTimestamp": "2026-09-27T09:00:00Z"},
		"spec": map[string]any{"replicas": float64(2)}, "status": map[string]any{"readyReplicas": float64(1), "updatedReplicas": float64(2), "availableReplicas": float64(1)}}}, "c2")
	ws.AddEvents("mesh1", []map[string]any{{"apiVersion": "v1", "kind": "Event", "metadata": map[string]any{"name": "checkout-1.a", "namespace": "payments"},
		"involvedObject": map[string]any{"kind": "Pod", "name": "checkout-1", "namespace": "payments", "uid": "uid-checkout-1"}, "reason": "BackOff", "type": "Warning", "message": "Back-off restarting failed container"}})
	ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "current", []string{"2026-09-27T10:11:05Z Starting", "2026-09-27T10:11:06Z Ready"}, "l1")
	ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "previous", []string{"2026-09-27T10:07:12Z ERROR timed out", "2026-09-27T10:08:42Z exiting"}, "l2")
	for _, r := range []string{"pods", "services"} {
		ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.core.v1." + r, Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: r, State: bundle.StateComplete})
	}
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.events", Cluster: "mesh1", Type: bundle.CoverageEvents, Resource: "events", Version: "v1", State: bundle.StateComplete})
	if complete {
		ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.apps.v1.deployments", Cluster: "mesh1", Type: bundle.CoverageResource, Group: "apps", Version: "v1", Resource: "deployments", State: bundle.StateComplete})
		ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete; s.Verified = true })
	}
	srv := httptest.NewServer((&Server{WS: ws, Token: token}).Handler())
	t.Cleanup(srv.Close)
	return ws, srv
}

func cfg(srv *httptest.Server) *rest.Config {
	return &rest.Config{Host: srv.URL + "/clusters/mesh1", BearerToken: token}
}

func TestDiscoveryVersionAndAuth(t *testing.T) {
	_, srv := setup(t, true)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	v, err := cs.Discovery().ServerVersion()
	if err != nil || v.GitVersion != "v1.34.1" {
		t.Fatalf("version: %v %v", v, err)
	}
	_, lists, err := cs.Discovery().ServerGroupsAndResources()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, l := range lists {
		for _, r := range l.APIResources {
			found[l.GroupVersion+"/"+r.Name] = true
			// Access reviews are the one "create": they answer questions and change nothing.
			if strings.Contains(strings.Join(r.Verbs, ","), "create") && l.GroupVersion != "authorization.k8s.io/v1" {
				t.Fatalf("write verb advertised: %v", r)
			}
		}
	}
	for _, want := range []string{"v1/pods", "v1/pods/log", "apps/v1/deployments", "gateway.networking.k8s.io/v1/httproutes", "v1/events", "v1/namespaces"} {
		if !found[want] {
			t.Fatalf("discovery missing %s (have %v)", want, found)
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/clusters/mesh1/api/v1/pods", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token accepted: %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Flightbay-Snapshot") == "" {
		t.Fatal("snapshot header missing")
	}
}

func TestListGetSelectors(t *testing.T) {
	_, srv := setup(t, true)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	ctx := context.Background()
	all, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil || len(all.Items) != 3 || all.ResourceVersion == "" {
		t.Fatalf("list all: %d %v", len(all.Items), err)
	}
	ns, _ := cs.CoreV1().Pods("payments").List(ctx, metav1.ListOptions{LabelSelector: "app=checkout"})
	if len(ns.Items) != 1 || ns.Items[0].Name != "checkout-1" {
		t.Fatalf("label selector: %v", ns.Items)
	}
	fs, _ := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "metadata.namespace=istio-system"})
	if len(fs.Items) != 1 {
		t.Fatalf("field selector: %d", len(fs.Items))
	}
	page, _ := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 2})
	if len(page.Items) != 2 || page.Continue == "" {
		t.Fatalf("paging: %d %q", len(page.Items), page.Continue)
	}
	rest2, _ := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 2, Continue: page.Continue})
	if len(rest2.Items) != 1 {
		t.Fatalf("second page: %d", len(rest2.Items))
	}
	p, err := cs.CoreV1().Pods("payments").Get(ctx, "checkout-1", metav1.GetOptions{})
	if err != nil || p.Spec.NodeName != "worker-1" {
		t.Fatalf("get: %v", err)
	}
	if _, err := cs.CoreV1().Pods("payments").Get(ctx, "nope", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	d, err := cs.AppsV1().Deployments("payments").Get(ctx, "checkout", metav1.GetOptions{})
	if err != nil || *d.Spec.Replicas != 2 {
		t.Fatalf("deployment get: %v", err)
	}
	ev, _ := cs.CoreV1().Events("payments").List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=checkout-1,involvedObject.kind=Pod"})
	if len(ev.Items) != 1 || ev.Items[0].Reason != "BackOff" {
		t.Fatalf("events for describe: %v", ev.Items)
	}
	nss, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil || len(nss.Items) != 2 {
		t.Fatalf("synthesised namespaces: %v %v", nss, err)
	}
}

func TestTableOutput(t *testing.T) {
	_, srv := setup(t, true)
	req, _ := http.NewRequest("GET", srv.URL+"/clusters/mesh1/api/v1/namespaces/payments/pods", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io,application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tbl metav1.Table
	_ = json.NewDecoder(resp.Body).Decode(&tbl)
	if tbl.Kind != "Table" || len(tbl.Rows) != 2 {
		t.Fatalf("table: %+v", tbl)
	}
	var cols []string
	for _, c := range tbl.ColumnDefinitions {
		cols = append(cols, c.Name)
	}
	if strings.Join(cols, ",") != "Name,Ready,Status,Restarts,Age,IP,Node" {
		t.Fatalf("columns %v", cols)
	}
	row := tbl.Rows[0].Cells
	if row[0] != "checkout-1" || row[1] != "1/1" || row[2] != "Running" || row[3] != float64(2) || row[4] != "16m" {
		t.Fatalf("row %v", row)
	}
	if len(tbl.Rows[0].Object.Raw) == 0 {
		t.Fatal("row object missing")
	}
}

func TestPodLogs(t *testing.T) {
	_, srv := setup(t, true)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	read := func(o *corev1.PodLogOptions, pod string) (string, error) {
		b, err := cs.CoreV1().Pods("payments").GetLogs(pod, o).DoRaw(context.Background())
		return string(b), err
	}
	out, err := read(&corev1.PodLogOptions{}, "checkout-1")
	if err != nil || out != "Starting\nReady\n" {
		t.Fatalf("current logs %q %v", out, err)
	}
	out, _ = read(&corev1.PodLogOptions{Previous: true, Timestamps: true}, "checkout-1")
	if !strings.HasPrefix(out, "2026-09-27T10:07:12Z ERROR timed out\n") {
		t.Fatalf("previous logs %q", out)
	}
	tail := int64(1)
	out, _ = read(&corev1.PodLogOptions{TailLines: &tail}, "checkout-1")
	if out != "Ready\n" {
		t.Fatalf("tail %q", out)
	}
	since := metav1.NewTime(time.Date(2026, 9, 27, 10, 11, 6, 0, time.UTC))
	out, _ = read(&corev1.PodLogOptions{SinceTime: &since}, "checkout-1")
	if out != "Ready\n" {
		t.Fatalf("sinceTime %q", out)
	}
	// kubectl logs uses Stream, which decodes the Status body (DoRaw does not).
	if _, err := cs.CoreV1().Pods("payments").GetLogs("sidecar-1", &corev1.PodLogOptions{}).Stream(context.Background()); err == nil || !strings.Contains(err.Error(), "container name must be specified") {
		t.Fatalf("expected container required error, got %v", err)
	}
}

func TestReadOnly(t *testing.T) {
	_, srv := setup(t, true)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	_, err := cs.CoreV1().Pods("payments").Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, metav1.CreateOptions{})
	if !apierrors.IsMethodNotSupported(err) || !strings.Contains(err.Error(), "read-only snapshot taken 2026-09-27T10:12:00Z") {
		t.Fatalf("expected read-only error, got %v", err)
	}
	err = cs.CoreV1().Pods("payments").Delete(context.Background(), "checkout-1", metav1.DeleteOptions{})
	if !apierrors.IsMethodNotSupported(err) {
		t.Fatalf("delete not rejected: %v", err)
	}
}

func TestLoadingExcludedAndTruncatedStates(t *testing.T) {
	ws, srv := setup(t, false)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	lreq, _ := http.NewRequest("GET", srv.URL+"/clusters/mesh1/apis/apps/v1/namespaces/payments/deployments", nil)
	lreq.Header.Set("Authorization", "Bearer "+token)
	lresp, err := http.DefaultClient.Do(lreq)
	if err != nil {
		t.Fatal(err)
	}
	lbody, _ := io.ReadAll(lresp.Body)
	if lresp.StatusCode != http.StatusServiceUnavailable || lresp.Header.Get("Retry-After") == "" || !strings.Contains(string(lbody), "still loading") {
		t.Fatalf("expected retryable loading response, got %d %s", lresp.StatusCode, lbody)
	}
	dyn := dynamic.NewForConfigOrDie(cfg(srv))
	if _, err := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("payments").List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("complete collection should list during import: %v", err)
	}
	if _, err := cs.CoreV1().Pods("payments").Get(context.Background(), "checkout-1", metav1.GetOptions{}); err != nil {
		t.Fatalf("loaded object should be readable while loading: %v", err)
	}
	_, err = cs.CoreV1().ConfigMaps("payments").List(context.Background(), metav1.ListOptions{})
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("expected excluded, got %v", err)
	}
	ws.SetCoverage(bundle.Coverage{ID: "t", Cluster: "mesh1", Type: bundle.CoverageResource, Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", State: bundle.StateTruncated, Reason: "budget"})
	req, _ := http.NewRequest("GET", srv.URL+"/clusters/mesh1/apis/gateway.networking.k8s.io/v1/httproutes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Warning"), "truncated") {
		t.Fatalf("truncated list should warn: %d %q", resp.StatusCode, resp.Header.Get("Warning"))
	}
}

func TestWatchDeliversImportedObjects(t *testing.T) {
	ws, srv := setup(t, false)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := cs.CoreV1().Pods("payments").Watch(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	seen := map[string]bool{}
	for len(seen) < 2 {
		ev := <-w.ResultChan()
		seen[ev.Object.(*corev1.Pod).Name] = true
	}
	ws.AddObjects("mesh1", podsRes, []map[string]any{podObj("payments", "late-1", "late", "app")}, "c9")
	select {
	case ev := <-w.ResultChan():
		if ev.Object.(*corev1.Pod).Name != "late-1" || ev.Type != "ADDED" {
			t.Fatalf("unexpected event %v", ev)
		}
	case <-ctx.Done():
		t.Fatal("imported object not delivered to watch")
	}
	// A live workspace replaces and removes objects: the watch reports both.
	updated := podObj("payments", "late-1", "late", "app")
	updated["status"].(map[string]any)["phase"] = "Failed"
	ws.UpsertObject("mesh1", podsRes, updated)
	ws.DeleteObject("mesh1", "", "pods", "payments", "checkout-1")
	want := []struct{ typ, name, phase string }{{"MODIFIED", "late-1", "Failed"}, {"DELETED", "checkout-1", ""}}
	var lastRV string
	for _, wnt := range want {
		select {
		case ev := <-w.ResultChan():
			p := ev.Object.(*corev1.Pod)
			if string(ev.Type) != wnt.typ || p.Name != wnt.name || (wnt.phase != "" && string(p.Status.Phase) != wnt.phase) {
				t.Fatalf("want %s %s, got %s %s (%s)", wnt.typ, wnt.name, ev.Type, p.Name, p.Status.Phase)
			}
			if p.ResourceVersion == "" || p.ResourceVersion == lastRV {
				t.Fatalf("%s without a new resourceVersion: %q", ev.Type, p.ResourceVersion)
			}
			lastRV = p.ResourceVersion
		case <-ctx.Done():
			t.Fatalf("%s not delivered to watch", wnt.typ)
		}
	}
}

// A watch resumed from a list's resourceVersion (kubectl get -w, reflectors)
// must not replay the list, but must report later changes to listed objects.
func TestWatchFromListVersionReportsChanges(t *testing.T) {
	ws, srv := setup(t, true)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l, err := cs.CoreV1().Pods("payments").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := cs.CoreV1().Pods("payments").Watch(ctx, metav1.ListOptions{ResourceVersion: l.ResourceVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	go func() {
		time.Sleep(100 * time.Millisecond)
		ws.DeleteObject("mesh1", "", "pods", "payments", "sidecar-1")
	}()
	select {
	case ev := <-w.ResultChan():
		if ev.Type != "DELETED" || ev.Object.(*corev1.Pod).Name != "sidecar-1" {
			t.Fatalf("expected DELETED sidecar-1 first (no replay of the list), got %s %s", ev.Type, ev.Object.(*corev1.Pod).Name)
		}
	case <-ctx.Done():
		t.Fatal("delete not delivered")
	}
}

func TestFollowLogsDeliversNewLines(t *testing.T) {
	ws, srv := setup(t, false)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cs.CoreV1().Pods("payments").GetLogs("checkout-1", &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	buf := make([]byte, 64)
	n, _ := io.ReadAtLeast(stream, buf, len("Starting\nReady\n"))
	if !strings.HasPrefix(string(buf[:n]), "Starting\nReady\n") {
		t.Fatalf("initial lines %q", buf[:n])
	}
	ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "current", []string{"2026-09-27T10:11:09Z Later"}, "l3")
	n, _ = io.ReadAtLeast(stream, buf, len("Later\n"))
	if string(buf[:n]) != "Later\n" {
		t.Fatalf("followed line %q", buf[:n])
	}
}

// A live workspace trims its oldest lines, so a stream's line count and its
// sequence numbers drift apart. A follower started after a trim must resume
// from the newest sequence number, not replay lines it already sent.
func TestFollowLogsAfterTrim(t *testing.T) {
	ws, srv := setup(t, false)
	ws.TrimLogs(3) // drops "Starting": current keeps "Ready" (seq 1), previous keeps both lines
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cs.CoreV1().Pods("payments").GetLogs("checkout-1", &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	buf := make([]byte, 64)
	n, _ := io.ReadAtLeast(stream, buf, len("Ready\n"))
	if string(buf[:n]) != "Ready\n" {
		t.Fatalf("initial lines %q", buf[:n])
	}
	ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "current", []string{"2026-09-27T10:11:10Z Newest"}, "l4")
	n, _ = io.ReadAtLeast(stream, buf, len("Newest\n"))
	if string(buf[:n]) != "Newest\n" {
		t.Fatalf("after a trim the follower sent %q", buf[:n])
	}
}

func TestKubeconfig(t *testing.T) {
	b, err := Kubeconfig("http://127.0.0.1:1234", token, []string{"mesh1", "mesh2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := clientcmd.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if c.CurrentContext != "mesh1@snapshot" || c.Clusters["mesh2@snapshot"].Server != "http://127.0.0.1:1234/clusters/mesh2" || c.AuthInfos["flightbay"].Token != token {
		t.Fatalf("bad kubeconfig %+v", c)
	}
}

// Regression: kubectl and k9s load credentials through clientcmd, which only
// sends a bearer token over TLS. Serve the snapshot over TLS with a session CA
// and check a kubeconfig round trip actually authenticates.
func TestKubeconfigOverTLSAuthenticates(t *testing.T) {
	ws, _ := setup(t, true)
	tlsCfg, caPEM, err := NewSessionTLS()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer((&Server{WS: ws, Token: token}).Handler())
	srv.TLS = tlsCfg
	srv.StartTLS()
	defer srv.Close()
	kc, err := Kubeconfig(srv.URL, token, []string{"mesh1"}, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(rc)
	pods, err := cs.CoreV1().Pods("payments").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 2 {
		t.Fatalf("kubeconfig over TLS did not authenticate: %v", err)
	}
}

func TestAccessReviewsAllowReadsDenyWrites(t *testing.T) {
	_, srv := setup(t, true)
	cs := kubernetes.NewForConfigOrDie(cfg(srv))
	ctx := context.Background()
	check := func(verb, sub string) authv1.SubjectAccessReviewStatus {
		rev, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{Verb: verb, Resource: "pods", Namespace: "payments", Subresource: sub}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return rev.Status
	}
	if !check("list", "").Allowed || !check("watch", "").Allowed || !check("get", "log").Allowed {
		t.Fatal("reads must be allowed")
	}
	if st := check("delete", ""); st.Allowed || !strings.Contains(st.Reason, "read-only snapshot") {
		t.Fatalf("delete allowed or unexplained: %+v", st)
	}
	if check("create", "exec").Allowed || check("get", "portforward").Allowed {
		t.Fatal("exec/portforward must be denied")
	}
	rr, err := cs.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, &authv1.SelfSubjectRulesReview{Spec: authv1.SelfSubjectRulesReviewSpec{Namespace: "payments"}}, metav1.CreateOptions{})
	if err != nil || len(rr.Status.ResourceRules) == 0 {
		t.Fatalf("rules review: %v %+v", err, rr)
	}
	_, lists, err := cs.Discovery().ServerGroupsAndResources()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lists {
		for _, r := range l.APIResources {
			found = found || (l.GroupVersion == "authorization.k8s.io/v1" && r.Name == "selfsubjectaccessreviews")
		}
	}
	if !found {
		t.Fatal("access reviews not advertised")
	}
}

func TestCRDServedAndPrinterColumnsUsed(t *testing.T) {
	ws, srv := setup(t, true)
	ws.AddCRD("mesh1", map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": "httproutes.gateway.networking.k8s.io"},
		"spec": map[string]any{"group": "gateway.networking.k8s.io", "scope": "Namespaced", "names": map[string]any{"plural": "httproutes", "kind": "HTTPRoute"},
			"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true, "additionalPrinterColumns": []any{
				map[string]any{"name": "Hostnames", "type": "string", "jsonPath": ".spec.hostnames"},
				map[string]any{"name": "Age", "type": "date", "jsonPath": ".metadata.creationTimestamp"}}}}}})
	ws.AddObjects("mesh1", routeRes, []map[string]any{{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "checkout-route", "namespace": "payments", "creationTimestamp": "2026-09-27T10:00:00Z"},
		"spec":     map[string]any{"hostnames": []any{"shop.example.com"}}}}, "r1")
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.gateway.networking.k8s.io.v1.httproutes", Cluster: "mesh1", Type: bundle.CoverageResource,
		Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", State: bundle.StateComplete})

	req, _ := http.NewRequest("GET", srv.URL+"/clusters/mesh1/apis/gateway.networking.k8s.io/v1/namespaces/payments/httproutes", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tbl metav1.Table
	_ = json.NewDecoder(resp.Body).Decode(&tbl)
	resp.Body.Close()
	if len(tbl.ColumnDefinitions) != 3 || tbl.ColumnDefinitions[1].Name != "Hostnames" || len(tbl.Rows) != 1 {
		t.Fatalf("printer columns not used: %+v", tbl)
	}
	if got := tbl.Rows[0].Cells[1]; got != `["shop.example.com"]` {
		t.Fatalf("hostnames cell %v", got)
	}
	if got := tbl.Rows[0].Cells[2]; got != "12m" {
		t.Fatalf("age measured at snapshot time, got %v", got)
	}
	dyn := dynamic.NewForConfigOrDie(cfg(srv))
	l, err := dyn.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(l.Items) != 1 || l.Items[0].GetName() != "httproutes.gateway.networking.k8s.io" {
		t.Fatalf("crd list: %v %v", err, l)
	}
}

func TestWatchListSendsInitialEventsEndBookmark(t *testing.T) {
	_, srv := setup(t, true)
	req, _ := http.NewRequest("GET", srv.URL+"/clusters/mesh1/api/v1/namespaces/payments/pods?watch=true&sendInitialEvents=true&allowWatchBookmarks=true&resourceVersionMatch=NotOlderThan&timeoutSeconds=2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var added int
	for {
		var ev struct {
			Type   string         `json:"type"`
			Object map[string]any `json:"object"`
		}
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("stream ended before the bookmark (added=%d): %v", added, err)
		}
		md, _ := ev.Object["metadata"].(map[string]any)
		if ev.Type == "ADDED" {
			added++
			if md["resourceVersion"] == nil || md["resourceVersion"] == "" {
				t.Fatalf("object without resourceVersion: %v", md)
			}
			continue
		}
		ann, _ := md["annotations"].(map[string]any)
		if ev.Type != "BOOKMARK" || ann["k8s.io/initial-events-end"] != "true" {
			t.Fatalf("unexpected event %s %v", ev.Type, ev.Object)
		}
		break
	}
	if added != 2 {
		t.Fatalf("expected the 2 payments pods before the bookmark, got %d", added)
	}
}
