package collect

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
)

// TestCollectScale runs the real collector against a synthetic cluster that
// behaves like the API server as client-go sees it: requests pass a token
// bucket at the policy's QPS and burst, each costs a round trip, list pages
// arrive as JSON and are decoded, and log streams are generated line by line.
//
//	FLIGHTBAY_COLLECT_SCALE="pods=2000 lines=2000 configmaps=20000 events=20000 crds=150 rtt=15ms" \
//	  go test ./internal/collect -run TestCollectScale -v -timeout 60m
//
// It writes the bundle to $FLIGHTBAY_SCALE_BUNDLE (default: a temp file) so
// the importer's scale test can load it, and a CPU profile next to it.
func TestCollectScale(t *testing.T) {
	spec := os.Getenv("FLIGHTBAY_COLLECT_SCALE")
	if spec == "" {
		t.Skip("set FLIGHTBAY_COLLECT_SCALE to run")
	}
	cfg := map[string]string{"pods": "1000", "lines": "1000", "configmaps": "10000", "events": "10000", "crds": "100", "rtt": "15ms", "compression": ""}
	for _, kv := range strings.Fields(spec) {
		k, v, _ := strings.Cut(kv, "=")
		cfg[k] = v
	}
	n := func(k string) int { v, _ := strconv.Atoi(cfg[k]); return v }
	rtt, _ := time.ParseDuration(cfg["rtt"])
	out := os.Getenv("FLIGHTBAY_SCALE_BUNDLE")
	if out == "" {
		out = filepath.Join(t.TempDir(), "scale.tar.zst")
	}
	p, _ := policy.Profile("standard")
	p.ClusterScoped = true
	if c := cfg["compression"]; c != "" {
		p.Compression = c
	}
	sc := newScaleCluster(n("pods"), n("lines"), n("configmaps"), n("events"), n("crds"), rtt, p.QPS, p.Burst)
	sc.logLimiter = flowcontrol.NewTokenBucketRateLimiter(max(p.Logs.QPS, 1), max(p.Logs.Concurrency, 1))
	if v := cfg["concurrency"]; v != "" {
		p.Concurrency, _ = strconv.Atoi(v)
	}
	if v := cfg["logconcurrency"]; v != "" {
		p.Logs.Concurrency, _ = strconv.Atoi(v)
	}

	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	prof, _ := os.Create(out + ".cpu.pprof")
	_ = pprof.StartCPUProfile(prof)
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	start := time.Now()
	w, err := bundle.NewWriter(f, bundle.WriterOptions{Compression: p.Compression})
	if err != nil {
		t.Fatal(err)
	}
	rd, _ := redact.New(nil)
	col := &Collector{Policy: p, Redactor: rd, Writer: w, Version: "scale"}
	if err := col.Begin([]string{"scale"}); err != nil {
		t.Fatal(err)
	}
	var peak atomic.Int64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if int64(m.HeapInuse) > peak.Load() {
					peak.Store(int64(m.HeapInuse))
				}
			}
		}
	}()
	if err := col.CollectCluster(context.Background(), "scale", sc.clients()); err != nil {
		t.Fatal(err)
	}
	if err := col.Finish(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	elapsed := time.Since(start)
	pprof.StopCPUProfile()
	prof.Close()
	f.Close()
	runtime.ReadMemStats(&ms1)
	st, _ := os.Stat(out)
	lines := int64(n("pods")) * 2 * int64(n("lines"))
	t.Logf("collected in %s: %d API requests (max %d in flight), %d objects, %d log lines (%.0f lines/s), %d events",
		elapsed.Round(time.Millisecond), sc.requests.Load(), sc.maxInFlight.Load(), sc.objects.Load(), lines, float64(lines)/elapsed.Seconds(), n("events"))
	t.Logf("bundle %.1f MiB (compression %s) from %.1f MiB of JSON and log text; peak Go heap in use %.0f MiB; allocated %.1f GiB",
		float64(st.Size())/(1<<20), p.Compression, float64(sc.bytes.Load())/(1<<20), float64(peak.Load())/(1<<20), float64(ms1.TotalAlloc-ms0.TotalAlloc)/(1<<30))
	t.Logf("bundle: %s  profile: %s.cpu.pprof", out, out)
}

// scaleCluster serves discovery, lists and logs.
type scaleCluster struct {
	pods, lines, configmaps, events, crds int
	rtt                                   time.Duration
	limiter, logLimiter                   flowcontrol.RateLimiter
	requests, inFlight, maxInFlight       atomic.Int64
	objects, bytes                        atomic.Int64
	unpaged                               atomic.Int64 // lists sent without a limit
}

func newScaleCluster(pods, lines, configmaps, events, crds int, rtt time.Duration, qps float32, burst int) *scaleCluster {
	return &scaleCluster{pods: pods, lines: lines, configmaps: configmaps, events: events, crds: crds, rtt: rtt,
		limiter: flowcontrol.NewTokenBucketRateLimiter(qps, burst)}
}

// call accounts for one API request: the client-side rate limiter, then a round trip.
func (s *scaleCluster) call() func() { return s.callWith(s.limiter) }

// callWith accounts for one request through a given client-side limiter.
func (s *scaleCluster) callWith(l flowcontrol.RateLimiter) func() {
	l.Accept()
	s.requests.Add(1)
	n := s.inFlight.Add(1)
	for {
		m := s.maxInFlight.Load()
		if n <= m || s.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(s.rtt)
	return func() { s.inFlight.Add(-1) }
}

func (s *scaleCluster) clients() Clients {
	return Clients{Discovery: s, Dynamic: &scaleDyn{s: s}, Logs: s}
}

func (s *scaleCluster) ServerVersion() (*version.Info, error) {
	defer s.call()()
	return &version.Info{GitVersion: "v1.34.1"}, nil
}

func (s *scaleCluster) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	defer s.call()()
	rl := func(name, kind string, namespaced bool) metav1.APIResource {
		return metav1.APIResource{Name: name, Kind: kind, Namespaced: namespaced, Verbs: []string{"get", "list", "watch"}}
	}
	lists := []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{rl("pods", "Pod", true), rl("pods/log", "Pod", true), rl("configmaps", "ConfigMap", true),
			rl("services", "Service", true), rl("secrets", "Secret", true), rl("namespaces", "Namespace", false), rl("nodes", "Node", false), rl("events", "Event", true)}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{rl("deployments", "Deployment", true), rl("replicasets", "ReplicaSet", true)}},
	}
	for i := 0; i < s.crds; i++ {
		lists = append(lists, &metav1.APIResourceList{GroupVersion: fmt.Sprintf("g%d.example.com/v1", i), APIResources: []metav1.APIResource{rl(fmt.Sprintf("things%d", i), fmt.Sprintf("Thing%d", i), true)}})
	}
	return nil, lists, nil
}

// Stream generates a container log with a realistic mix of formats.
func (s *scaleCluster) Stream(_ context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	done := s.callWith(s.logLimiter)
	r, w := io.Pipe()
	go func() {
		defer done()
		bw := make([]byte, 0, 64<<10)
		base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
		for k := 0; k < s.lines; k++ {
			ts := base.Add(time.Duration(k) * 40 * time.Millisecond).Format("2006-01-02T15:04:05.000000000Z")
			var line string
			switch k % 6 {
			case 0:
				line = fmt.Sprintf(`%s [%s] "GET /api/v1/orders/%d HTTP/1.1" 200 - via_upstream - "-" 0 1043 12 11 "-" "curl/8.0" "a4e98f02-%d" "checkout.%s.svc:8080" "10.244.1.%d:8080" outbound|8080||checkout.%s.svc.cluster.local`, ts, ts[11:23], k%9973, k, ns, k%251, ns)
			case 1:
				line = fmt.Sprintf(`%s {"level":"info","time":"%s","msg":"reconciled gateway","gateway":"ai-gateway-%d","trace_id":"t%d","duration_ms":%d}`, ts, ts, k%97, k, k%500)
			case 2:
				line = fmt.Sprintf("%s info\txds\tPushing proxy %s version %d resources %d", ts, pod, k, k%300)
			case 3:
				line = fmt.Sprintf(`%s I0927 %s 1 controller.go:%d] Reconciler error for pod-%d: context deadline exceeded after %dms`, ts, ts[11:23], k%900, k, k%5000)
			case 4:
				line = fmt.Sprintf("%s ERROR database connection timed out host=postgres.%s.svc:5432 attempt=%d of 5", ts, ns, k%5)
			case 5:
				if k%600 == 5 {
					line = ts + " sending request Authorization: Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.c2lnbmF0dXJlLXZhbHVl"
				} else {
					line = fmt.Sprintf("%s DEBUG cache hit key=orders/%d size=%d", ts, k%1000, k%4096)
				}
			}
			bw = append(bw, line...)
			bw = append(bw, '\n')
			if len(bw) > 60<<10 {
				s.bytes.Add(int64(len(bw)))
				if _, err := w.Write(bw); err != nil {
					return
				}
				bw = bw[:0]
			}
		}
		s.bytes.Add(int64(len(bw)))
		_, _ = w.Write(bw)
		_ = w.Close()
	}()
	return r, nil
}

type scaleDyn struct {
	dynamic.Interface
	s *scaleCluster
}

func (d *scaleDyn) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &scaleRes{s: d.s, gvr: gvr}
}

type scaleRes struct {
	dynamic.NamespaceableResourceInterface
	s   *scaleCluster
	gvr schema.GroupVersionResource
	ns  string
}

func (r *scaleRes) Namespace(ns string) dynamic.ResourceInterface {
	return &scaleRes{s: r.s, gvr: r.gvr, ns: ns}
}

func (r *scaleRes) count() int {
	switch r.gvr.Resource {
	case "pods":
		return r.s.pods
	case "configmaps":
		return r.s.configmaps
	case "events":
		return r.s.events
	case "services", "deployments", "replicasets":
		return r.s.pods / 4
	case "secrets":
		return r.s.pods / 2
	case "namespaces":
		return 40
	case "nodes":
		return 30
	}
	return 0 // custom resource types: installed but empty, as most are
}

// List returns one page of JSON-decoded objects, as the dynamic client does.
func (r *scaleRes) List(_ context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	defer r.s.call()()
	total := r.count()
	from, _ := strconv.Atoi(opts.Continue)
	limit := int(opts.Limit)
	if limit <= 0 {
		r.s.unpaged.Add(1)
		limit = total
	}
	to := min(total, from+limit)
	var sb strings.Builder
	sb.WriteString(`{"apiVersion":"v1","kind":"List","metadata":{`)
	if to < total {
		fmt.Fprintf(&sb, `"continue":"%d"`, to)
	}
	sb.WriteString(`},"items":[`)
	for i := from; i < to; i++ {
		if i > from {
			sb.WriteByte(',')
		}
		sb.WriteString(scaleObject(r.gvr.Resource, i))
	}
	sb.WriteString("]}")
	r.s.bytes.Add(int64(sb.Len()))
	r.s.objects.Add(int64(to - from))
	var l unstructured.UnstructuredList
	if err := l.UnmarshalJSON([]byte(sb.String())); err != nil {
		return nil, err
	}
	return &l, nil
}

func scaleObject(resource string, i int) string {
	ns := fmt.Sprintf("team-%d", i%40)
	switch resource {
	case "pods":
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"app-%[1]d-7d9f8c6b5-x%[1]d","namespace":"%[2]s","uid":"0f4c2a1e-%[1]d","resourceVersion":"%[1]d","creationTimestamp":"2026-09-20T10:00:00Z","labels":{"app":"app-%[3]d","pod-template-hash":"7d9f8c6b5","app.kubernetes.io/part-of":"shop","version":"v2"},"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-09-20T10:00:00Z","prometheus.io/scrape":"true","istio.io/rev":"default"},"ownerReferences":[{"apiVersion":"apps/v1","kind":"ReplicaSet","name":"app-%[3]d-7d9f8c6b5","uid":"rs-%[3]d","controller":true}],"managedFields":[{"manager":"kube-controller-manager","operation":"Update","apiVersion":"v1","time":"2026-09-20T10:00:00Z","fieldsType":"FieldsV1","fieldsV1":{"f:metadata":{"f:labels":{".":{},"f:app":{}}}}}]},"spec":{"nodeName":"node-%[4]d","serviceAccountName":"app-%[3]d","containers":[{"name":"app","image":"registry.example.com/shop/app:2.8.%[5]d","ports":[{"containerPort":8080,"protocol":"TCP"}],"env":[{"name":"DB_HOST","value":"postgres.%[2]s.svc"},{"name":"DB_PASSWORD","valueFrom":{"secretKeyRef":{"name":"db","key":"password"}}},{"name":"LOG_LEVEL","value":"info"}],"resources":{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"memory":"256Mi"}},"readinessProbe":{"httpGet":{"path":"/healthz","port":8080}}},{"name":"istio-proxy","image":"docker.io/istio/proxyv2:1.29.2","args":["proxy","sidecar","--domain","$(POD_NAMESPACE).svc.cluster.local"]}]},"status":{"phase":"Running","podIP":"10.244.%[4]d.%[5]d","hostIP":"172.18.0.%[4]d","startTime":"2026-09-20T10:00:05Z","conditions":[{"type":"Ready","status":"True","lastTransitionTime":"2026-09-20T10:00:30Z"},{"type":"ContainersReady","status":"True","lastTransitionTime":"2026-09-20T10:00:30Z"}],"containerStatuses":[{"name":"app","ready":true,"restartCount":%[6]d,"image":"registry.example.com/shop/app:2.8.%[5]d","imageID":"registry.example.com/shop/app@sha256:4b1c","containerID":"containerd://%[1]d","state":{"running":{"startedAt":"2026-09-20T10:00:10Z"}}},{"name":"istio-proxy","ready":true,"restartCount":0,"image":"docker.io/istio/proxyv2:1.29.2","state":{"running":{"startedAt":"2026-09-20T10:00:08Z"}}}]}}`,
			i, ns, i/4, i%30, i%250, i%7/6)
	case "configmaps":
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"cfg-%[1]d","namespace":"%[2]s","uid":"cm-%[1]d","resourceVersion":"%[1]d","labels":{"app":"app-%[3]d"}},"data":{"application.yaml":"server:\n  port: 8080\nlogging:\n  level: info\nfeatures:\n  checkout: true\n  recommendations: %[4]t\n","endpoint":"https://api-%[1]d.internal.example.com/v1","timeout":"30s","retries":"3"}}`, i, ns, i/8, i%2 == 0)
	case "events":
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Event","metadata":{"name":"app-%[1]d.17f3a","namespace":"%[2]s","uid":"ev-%[1]d","resourceVersion":"%[1]d"},"involvedObject":{"kind":"Pod","namespace":"%[2]s","name":"app-%[1]d-7d9f8c6b5-x%[1]d","apiVersion":"v1"},"reason":"BackOff","message":"Back-off restarting failed container app in pod app-%[1]d","source":{"component":"kubelet","host":"node-%[3]d"},"firstTimestamp":"2026-09-27T09:00:00Z","lastTimestamp":"2026-09-27T10:00:00Z","count":%[4]d,"type":"Warning"}`, i, ns, i%30, i%40+1)
	case "secrets":
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"sec-%[1]d","namespace":"%[2]s","uid":"s-%[1]d","resourceVersion":"%[1]d"},"type":"Opaque","data":{"password":"c2VjcmV0LXZhbHVlLTEyMw==","username":"YWRtaW4="}}`, i, ns)
	case "namespaces":
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"team-%d","uid":"ns-%d","labels":{"istio.io/dataplane-mode":"ambient"}},"status":{"phase":"Active"}}`, i, i)
	case "nodes":
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-%d","uid":"n-%d","labels":{"kubernetes.io/os":"linux","node.kubernetes.io/instance-type":"m6i.2xlarge"}},"status":{"nodeInfo":{"kubeletVersion":"v1.34.1","containerRuntimeVersion":"containerd://2.1.0"},"allocatable":{"cpu":"8","memory":"32Gi","pods":"110"}}}`, i, i)
	default:
		kind := map[string]string{"services": "Service", "deployments": "Deployment", "replicasets": "ReplicaSet"}[resource]
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"%[3]s","metadata":{"name":"app-%[1]d","namespace":"%[2]s","uid":"x-%[1]d","resourceVersion":"%[1]d","labels":{"app":"app-%[1]d"}},"spec":{"selector":{"app":"app-%[1]d"},"replicas":4,"template":{"metadata":{"labels":{"app":"app-%[1]d"}},"spec":{"containers":[{"name":"app","image":"registry.example.com/shop/app:2.8.1"}]}}},"status":{"replicas":4,"readyReplicas":4}}`, i, ns, kind)
	}
}

// Get serves CRDs with a schema of realistic size (large CRDs run to hundreds of KB).
func (r *scaleRes) Get(_ context.Context, name string, _ metav1.GetOptions, _ ...string) (*unstructured.Unstructured, error) {
	defer r.s.call()()
	group := name[strings.Index(name, ".")+1:]
	props := strings.Repeat(`"field%d":{"type":"string","description":"A field of this custom resource, described at length as generated CRDs do."},`, 1)
	var sb strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&sb, props, i)
	}
	body := fmt.Sprintf(`{"apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition","metadata":{"name":%q},"spec":{"group":%q,"scope":"Namespaced","names":{"plural":%q,"kind":"Thing"},"versions":[{"name":"v1","served":true,"storage":true,"schema":{"openAPIV3Schema":{"type":"object","properties":{%s"x":{"type":"string"}}}},"additionalPrinterColumns":[{"name":"Ready","type":"string","jsonPath":".status.ready"}]}]}}`,
		name, group, name[:strings.Index(name, ".")], sb.String())
	r.s.bytes.Add(int64(len(body)))
	var u unstructured.Unstructured
	return &u, u.UnmarshalJSON([]byte(body))
}

// Every list is paginated: an unbounded list of events or pods in a large
// cluster is a single huge response the API server has to build.
func TestListsArePaginated(t *testing.T) {
	p, _ := policy.Profile("standard")
	p.ClusterScoped = true
	sc := newScaleCluster(1200, 3, 1200, 1200, 3, 0, 1000, 1000)
	sc.logLimiter = flowcontrol.NewFakeAlwaysRateLimiter()
	sc.limiter = flowcontrol.NewFakeAlwaysRateLimiter()
	w, _ := bundle.NewWriter(io.Discard, bundle.WriterOptions{Compression: "fastest"})
	rd, _ := redact.New(nil)
	col := &Collector{Policy: p, Redactor: rd, Writer: w, Version: "t"}
	if err := col.Begin([]string{"c"}); err != nil {
		t.Fatal(err)
	}
	if err := col.CollectCluster(context.Background(), "c", sc.clients()); err != nil {
		t.Fatal(err)
	}
	if err := col.Finish(); err != nil {
		t.Fatal(err)
	}
	if n := sc.unpaged.Load(); n != 0 {
		t.Fatalf("%d lists were sent without a page limit", n)
	}
	if sc.maxInFlight.Load() > int64(max(p.Concurrency, p.Logs.Concurrency)) {
		t.Fatalf("%d requests in flight, more than the concurrency limits allow", sc.maxInFlight.Load())
	}
}
