package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/flows"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/reach"
	"github.com/tjorourke/flightbay/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Running a suite. The seam is two small interfaces, so the loop that turns a
// suite into results can be tested without a cluster:
//
//	suiteRunner  sends one case and says what came back
//	suiteDecider names who decided it, best effort, and never blocks a result

type suiteRunSpec struct {
	Context string
	Cluster string
	Service ambient.Ref
	Source  ambient.SuiteSource
	Suite   *ambient.Suite
	Authz   *ambient.AuthzInput
}

type suiteSendRequest struct {
	Case    ambient.Case
	Source  ambient.SuiteSource
	Samples int
	// OnSample, when set, is called once for each request of a multi-sample
	// case as it finishes, never concurrently.
	OnSample func(ambient.Sample)
}

type suiteRunner interface {
	Send(ctx context.Context, req suiteSendRequest) (ambient.Observation, error)
	Close()
}

type suiteDecider interface {
	Decide(ctx context.Context, suite *ambient.Suite, c ambient.Case, obs *ambient.Observation, sentAt time.Time)
}

// skipCase is returned by a runner for a case it cannot send from here.
type skipCase string

func (e skipCase) Error() string { return string(e) }

type suiteHTTPError struct {
	Code int
	Msg  string
}

func (e suiteHTTPError) Error() string { return e.Msg }

// newSuiteRunner builds the runner and decider for a run. Tests replace it.
var newSuiteRunner = func(s *Server, ctx context.Context, spec suiteRunSpec) (suiteRunner, suiteDecider, error) {
	r, err := newMeshSuiteRunner(s, ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	return r, newMeshDecider(s, spec), nil
}

// runSuite sends the cases in ids, in order, and evaluates each against its
// expectation. A runner failure becomes an error result for that case, and a
// stopped context skips what is left.
func runSuite(ctx context.Context, suite *ambient.Suite, ids []string, runner suiteRunner, decider suiteDecider, now func() time.Time) *ambient.SuiteRun {
	return runSuiteWith(ctx, suite, ids, runner, decider, now, runOptions{})
}

const (
	msgNotSent     = "The run stopped before this case was sent."
	msgInterrupted = "The run stopped before this case finished."
)

// stoppedResult is a result runSuite records for a case the stop prevented.
func stoppedResult(r ambient.CaseResult) bool {
	return r.Status == ambient.StatusSkipped && (r.Message == msgNotSent || r.Message == msgInterrupted)
}

// runOptions are the extras of a run: progress events and the route rules
// that say statically which rule a request matches.
type runOptions struct {
	// Emit receives the ndjson events of a streamed run, in order.
	Emit func(event any)
	// Route holds the rules of the HTTPRoute in front of the suite's Service.
	Route []ambient.RouteRuleSpec
	// SampleEvery is how often a sample event is sent at most (by time);
	// zero means 250 ms. Ten new samples always send one.
	SampleEvery time.Duration
}

type runEventStart struct {
	Type  string   `json:"type"`
	Total int      `json:"total"`
	Cases []string `json:"cases"`
}

type runEventCase struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type runEventSample struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Completed int            `json:"completed"`
	Requested int            `json:"requested"`
	Backends  map[string]int `json:"backends"`
	Statuses  map[string]int `json:"statuses"`
	P50Ms     int64          `json:"p50Ms"`
	P95Ms     int64          `json:"p95Ms"`
}

type runEventCaseDone struct {
	Type   string             `json:"type"`
	ID     string             `json:"id"`
	Result ambient.CaseResult `json:"result"`
}

func pct(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	return sorted[i-1]
}

// sampleTally builds the cumulative sample events of one weights case.
type sampleTally struct {
	ev        runEventSample
	durations []int64
	lastN     int
	lastAt    time.Time
	every     time.Duration
	emit      func(any)
}

func newSampleTally(id string, requested int, every time.Duration, emit func(any)) *sampleTally {
	if every <= 0 {
		every = 250 * time.Millisecond
	}
	return &sampleTally{ev: runEventSample{Type: "sample", ID: id, Requested: requested, Backends: map[string]int{}, Statuses: map[string]int{}}, lastAt: time.Now(), every: every, emit: emit}
}

func (t *sampleTally) add(s ambient.Sample) {
	t.ev.Completed++
	if s.Backend != "" {
		t.ev.Backends[s.Backend]++
	}
	t.ev.Statuses[strconv.Itoa(s.Status)]++
	i := sort.Search(len(t.durations), func(i int) bool { return t.durations[i] >= s.DurationMs })
	t.durations = append(t.durations, 0)
	copy(t.durations[i+1:], t.durations[i:])
	t.durations[i] = s.DurationMs
	if t.ev.Completed-t.lastN >= 10 || time.Since(t.lastAt) >= t.every || t.ev.Completed == t.ev.Requested {
		t.flush()
	}
}

func (t *sampleTally) flush() {
	if t.ev.Completed == t.lastN {
		return
	}
	t.lastN, t.lastAt = t.ev.Completed, time.Now()
	ev := t.ev
	ev.Backends, ev.Statuses = map[string]int{}, map[string]int{}
	for k, v := range t.ev.Backends {
		ev.Backends[k] = v
	}
	for k, v := range t.ev.Statuses {
		ev.Statuses[k] = v
	}
	ev.P50Ms, ev.P95Ms = pct(t.durations, 50), pct(t.durations, 95)
	t.emit(ev)
}

// runSuiteWith is runSuite with progress events and static rule matching. A
// stopped context stops sending: the case in flight is kept if it finished,
// and every case after it is recorded as not sent.
func runSuiteWith(ctx context.Context, suite *ambient.Suite, ids []string, runner suiteRunner, decider suiteDecider, now func() time.Time, opt runOptions) *ambient.SuiteRun {
	emit := opt.Emit
	if emit == nil {
		emit = func(any) {}
	}
	byID := map[string]ambient.Case{}
	for _, c := range suite.Cases {
		byID[c.ID] = c
	}
	run := &ambient.SuiteRun{At: now(), Source: suite.Source, Results: []ambient.CaseResult{}}
	toRun := []string{}
	for _, id := range ids {
		if _, ok := byID[id]; ok {
			toRun = append(toRun, id)
		}
	}
	emit(runEventStart{Type: "start", Total: len(toRun), Cases: toRun})
	for _, id := range toRun {
		c := byID[id]
		if ctx.Err() != nil {
			run.Results = append(run.Results, ambient.CaseResult{ID: id, Status: ambient.StatusSkipped, Message: msgNotSent, At: now()})
			continue
		}
		emit(runEventCase{Type: "case-start", ID: id})
		sentAt := now()
		samples := 0
		req := suiteSendRequest{Case: c, Source: suite.Source}
		var tally *sampleTally
		if len(c.Expect.Weights) > 0 {
			samples = c.Expect.Samples
			if samples == 0 {
				samples = ambient.DefaultSamples
			}
			if opt.Emit != nil {
				tally = newSampleTally(id, samples, opt.SampleEvery, emit)
				req.OnSample = tally.add
			}
		}
		req.Samples = samples
		obs, err := runner.Send(ctx, req)
		if tally != nil {
			tally.flush()
		}
		var skip skipCase
		var res ambient.CaseResult
		switch {
		case ctx.Err() != nil && (len(obs.Samples) < samples || (samples == 0 && obs.Status == 0 && obs.Transport == "")):
			// Cut short by the stop: nothing to judge.
			res = ambient.CaseResult{ID: id, Status: ambient.StatusSkipped, Message: msgInterrupted, Connection: obs.Connection, At: now()}
		case errors.As(err, &skip):
			res = ambient.CaseResult{ID: id, Status: ambient.StatusSkipped, Message: string(skip), Connection: obs.Connection, At: now()}
		default:
			if err != nil {
				obs.Error = err.Error()
			}
			if obs.Rule == 0 && len(opt.Route) > 0 && (c.Expect.Rule > 0 || c.Expect.NotRoute) && !c.Request.Direct && (c.Request.Host == "" || c.Request.Host == suite.Service.Name) {
				obs.Rule = ambient.MatchRouteRule(opt.Route, c.Request)
			}
			if decider != nil && obs.Error == "" && len(c.Expect.Weights) == 0 {
				safeDecide(ctx, decider, suite, c, &obs, sentAt)
			}
			res = ambient.Evaluate(c, obs, now())
		}
		run.Results = append(run.Results, res)
		if !stoppedResult(res) {
			emit(runEventCaseDone{Type: "case-done", ID: id, Result: res})
		}
	}
	run.Summary = ambient.Summarize(run.Results, run.At)
	return run
}

// safeDecide keeps a failure in the best-effort step from losing the result.
func safeDecide(ctx context.Context, d suiteDecider, suite *ambient.Suite, c ambient.Case, obs *ambient.Observation, sentAt time.Time) {
	defer func() { _ = recover() }()
	d.Decide(ctx, suite, c, obs, sentAt)
}

// ---- real runner -----------------------------------------------------------

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

type suiteClient struct {
	Pod       ambient.Ref
	Container string
	Ambient   bool
	Temporary bool
}

type meshSuiteRunner struct {
	s         *Server
	spec      suiteRunSpec
	cfg       *rest.Config
	client    kubernetes.Interface
	svc       *corev1.Service
	localName string
	suffix    string
	pods      []corev1.Pod
	services  []corev1.Service
	backends  map[string]bool
	options   []meshClientOption

	mu        sync.Mutex
	ephemeral map[string]*suiteClient
}

func newMeshSuiteRunner(s *Server, ctx context.Context, spec suiteRunSpec) (*meshSuiteRunner, error) {
	p, _ := policy.Profile("minimal")
	cfg, err := collect.RestConfig(s.AmbientKubeconfig, spec.Context, p, "flightbay-suite-run")
	if err != nil {
		return nil, suiteHTTPError{400, err.Error()}
	}
	cfg.Timeout = 20 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, suiteHTTPError{400, err.Error()}
	}
	ns, name := spec.Service.Namespace, spec.Service.Name
	svc, err := client.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	captured := s.WS.Get(spec.Cluster, "", "services", ns, name)
	if err != nil || captured == nil || string(svc.UID) == "" || string(svc.UID) != ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) {
		return nil, suiteHTTPError{409, "Service identity differs from the capture/context; refresh before testing"}
	}
	v, err := ambient.NewView(s.WS, spec.Cluster, "Service", ns, name, "")
	if err != nil || v.DNS == nil || v.DNS.LocalName == "" {
		return nil, suiteHTTPError{400, "the Service has no captured DNS name to test"}
	}
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, suiteHTTPError{400, err.Error()}
	}
	services, err := client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, suiteHTTPError{400, err.Error()}
	}
	r := &meshSuiteRunner{s: s, spec: spec, cfg: cfg, client: client, svc: svc, localName: v.DNS.LocalName, suffix: strings.TrimPrefix(v.DNS.LocalName, name),
		pods: pods.Items, services: services.Items, backends: suiteBackends(spec.Suite, ns), ephemeral: map[string]*suiteClient{}}
	r.options = s.meshClientOptions(spec.Cluster)
	return r, nil
}

// suiteBackends is the set of Services the suite expects to answer, as
// "namespace/name", which is how responder attribution picks a Service.
func suiteBackends(suite *ambient.Suite, ns string) map[string]bool {
	out := map[string]bool{}
	if suite == nil {
		return out
	}
	for _, c := range suite.Cases {
		for _, b := range c.Expect.Backends {
			out[ns+"/"+b] = true
		}
		if c.Expect.Backend != "" {
			out[ns+"/"+c.Expect.Backend] = true
		}
		for b := range c.Expect.Weights {
			out[ns+"/"+b] = true
		}
	}
	return out
}

func (r *meshSuiteRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ns, c := range r.ephemeral {
		_ = deleteEphemeralClient(r.client, ns, c.Pod.Name)
	}
	r.ephemeral = map[string]*suiteClient{}
}

func podSA(s *Server, cluster string, ref ambient.Ref) string {
	if o := s.WS.Get(cluster, "", "pods", ref.Namespace, ref.Name); o != nil {
		if sa := ambient.S(ambient.Path(o.Obj, "spec", "serviceAccountName")); sa != "" {
			return sa
		}
	}
	return "default"
}

// liveClient checks the Pod still is the one that was captured.
func (r *meshSuiteRunner) liveClient(ctx context.Context, o meshClientOption) (*suiteClient, error) {
	pod, err := r.client.CoreV1().Pods(o.Pod.Namespace).Get(ctx, o.Pod.Name, metav1.GetOptions{})
	if err != nil || string(pod.UID) != o.UID || pod.DeletionTimestamp != nil || !podReady(pod) {
		return nil, skipCase(fmt.Sprintf("The client Pod %s/%s is gone or not Ready.", o.Pod.Namespace, o.Pod.Name))
	}
	return &suiteClient{Pod: o.Pod, Container: o.Container, Ambient: o.Ambient}, nil
}

func (r *meshSuiteRunner) ephemeralIn(ctx context.Context, ns string) (*suiteClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.ephemeral[ns]; c != nil {
		return c, nil
	}
	nso := r.s.WS.Get(r.spec.Cluster, "", "namespaces", "", ns)
	if ns == "" || nso == nil {
		return nil, skipCase("The namespace for the temporary Pod is not in the capture.")
	}
	sweepStaleEphemeralClients(ctx, r.client, ns, time.Now())
	pod, err := startEphemeralClient(ctx, r.client, ns, time.Second, ephemeralReadyTimeout)
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, skipCase("Creating a temporary Pod in " + ns + " is forbidden: " + err.Error())
		}
		return nil, err
	}
	c := &suiteClient{Pod: ambient.Ref{Cluster: r.spec.Cluster, Resource: "pods", Kind: "Pod", Namespace: ns, Name: pod.Name}, Container: ephemeralContainer,
		Ambient: nso.Labels()["istio.io/dataplane-mode"] == "ambient", Temporary: true}
	r.ephemeral[ns] = c
	return c, nil
}

// clientFor picks the Pod a case is sent from: the suite's source, or one
// with the identity the case asks for.
func (r *meshSuiteRunner) clientFor(ctx context.Context, c ambient.Case, src ambient.SuiteSource) (*suiteClient, error) {
	if c.From != nil {
		sa := c.From.ServiceAccount
		if sa == "" {
			sa = "default"
		}
		for _, o := range r.options {
			if o.Pod.Namespace == c.From.Namespace && podSA(r.s, r.spec.Cluster, o.Pod) == sa {
				return r.liveClient(ctx, o)
			}
		}
		if sa == "default" {
			return r.ephemeralIn(ctx, c.From.Namespace)
		}
		return nil, skipCase(fmt.Sprintf("No client Pod runs as %s/%s. Add a curl client under that account, or edit the case.", c.From.Namespace, sa))
	}
	switch src.Kind {
	case ambient.SourceEphemeral:
		return r.ephemeralIn(ctx, defaultStr(src.Ref.Namespace, r.spec.Service.Namespace))
	case ambient.SourceWorkload:
		for _, o := range r.options {
			if o.Pod.Namespace == src.Ref.Namespace && o.Pod.Name == src.Ref.Name {
				return r.liveClient(ctx, o)
			}
		}
		return nil, skipCase(fmt.Sprintf("%s/%s is not a Ready curl client Pod in the capture.", src.Ref.Namespace, src.Ref.Name))
	}
	return nil, skipCase("This run has no source to send from.")
}

func defaultStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// directTarget picks a ready Pod behind the Service for a request that skips
// the Service and the waypoint: its IP and the port it listens on.
func directTarget(svc *corev1.Service, pods []corev1.Pod, port int) (ip string, targetPort int, pod string, err error) {
	if len(svc.Spec.Selector) == 0 {
		return "", 0, "", errors.New("the Service has no selector, so there are no Pods to send to")
	}
	sel := labels.SelectorFromSet(svc.Spec.Selector)
	var ready []corev1.Pod
	for _, p := range pods {
		if sel.Matches(labels.Set(p.Labels)) && p.DeletionTimestamp == nil && p.Status.PodIP != "" && podReady(&p) {
			ready = append(ready, p)
		}
	}
	if len(ready) == 0 {
		return "", 0, "", errors.New("no ready Pod behind the Service")
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Name < ready[j].Name })
	chosen := ready[0]
	targetPort = port
	for _, sp := range svc.Spec.Ports {
		if int(sp.Port) != port {
			continue
		}
		switch {
		case sp.TargetPort.IntValue() > 0:
			targetPort = sp.TargetPort.IntValue()
		case sp.TargetPort.StrVal != "":
			for _, c := range chosen.Spec.Containers {
				for _, cp := range c.Ports {
					if cp.Name == sp.TargetPort.StrVal {
						targetPort = int(cp.ContainerPort)
					}
				}
			}
		}
	}
	return chosen.Status.PodIP, targetPort, chosen.Name, nil
}

func (r *meshSuiteRunner) serviceNamed(name string) *corev1.Service {
	for i := range r.services {
		if r.services[i].Name == name {
			return &r.services[i]
		}
	}
	return nil
}

func hasTCPPort(svc *corev1.Service, port int) bool {
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == port && (p.Protocol == corev1.ProtocolTCP || p.Protocol == "") {
			return true
		}
	}
	return false
}

func (r *meshSuiteRunner) backendOf(res httpProbeResult) (backend, pod string) {
	resp := meshResponseIdentity(res, r.pods, r.services, r.backends)
	if resp == nil {
		return "", ""
	}
	if _, name, ok := strings.Cut(resp.Service, "/"); ok {
		backend = name
	}
	return backend, resp.Pod.Name
}

func (r *meshSuiteRunner) Send(ctx context.Context, req suiteSendRequest) (ambient.Observation, error) {
	c := req.Case
	obs := ambient.Observation{}
	cl, err := r.clientFor(ctx, c, req.Source)
	if err != nil {
		return obs, err
	}
	obs.Client = cl.Pod.Namespace + "/" + cl.Pod.Name
	port := c.Request.Port
	if port == 0 && len(r.svc.Spec.Ports) > 0 {
		port = int(r.svc.Spec.Ports[0].Port)
	}
	var target *corev1.Service = r.svc
	address, via := "", ""
	switch {
	case c.Request.Direct:
		live, lerr := r.client.CoreV1().Pods(r.svc.Namespace).List(ctx, metav1.ListOptions{})
		if lerr != nil {
			return obs, lerr
		}
		ip, tp, pod, derr := directTarget(r.svc, live.Items, port)
		if derr != nil {
			return obs, skipCase(derr.Error())
		}
		address = ip
		port = tp
		via = fmt.Sprintf("straight to Pod %s (%s), skipping the Service and the waypoint", pod, net.JoinHostPort(ip, strconv.Itoa(tp)))
	default:
		host := c.Request.Host
		if host != "" {
			if !dnsLabel.MatchString(host) {
				return obs, skipCase(fmt.Sprintf("%q is not a Service name.", host))
			}
			target = r.serviceNamed(host)
			if target == nil {
				return obs, skipCase(fmt.Sprintf("Service %s does not exist in %s.", host, r.svc.Namespace))
			}
			address = host + r.suffix
		} else {
			address = r.localName
		}
		if !hasTCPPort(target, port) {
			return obs, skipCase(fmt.Sprintf("Service %s does not expose TCP port %d.", target.Name, port))
		}
		via = fmt.Sprintf("the Service %s, the normal mesh path", net.JoinHostPort(address, strconv.Itoa(port)))
	}
	where := "In-mesh curl"
	if !cl.Ambient {
		where = "Curl (client not in the mesh)"
	}
	from := cl.Pod.Namespace + "/" + cl.Pod.Name
	if cl.Temporary {
		from = "temporary Pod " + from
	}
	obs.Connection = fmt.Sprintf("%s from %s via %s", where, from, via)

	profile := httpProfile{Method: c.Request.Method, Path: c.Request.Path, Headers: c.Request.Headers, Body: c.Request.Body, Timeout: 10}
	if err := validateHTTPProfile(&profile); err != nil {
		return obs, skipCase("The request is not valid: " + err.Error())
	}
	n := req.Samples
	if n < 1 {
		n = 1
	}
	if n > maxMeshRequests {
		n = maxMeshRequests
	}
	q := meshProbeRequest{Service: r.spec.Service, Context: r.spec.Context, Client: cl.Pod, Container: cl.Container, Port: port, Profile: profile}
	if cl.Temporary {
		q.Ephemeral = &struct {
			Namespace string `json:"namespace"`
		}{cl.Pod.Namespace}
	}
	var results []httpProbeResult
	if n == 1 {
		results = []httpProbeResult{runMeshCurl(ctx, r.cfg, r.client, q, address)}
	} else {
		results = sampleConcurrently(ctx, n, 8, func() httpProbeResult {
			return runMeshCurl(ctx, r.cfg, r.client, q, address)
		}, func(res httpProbeResult) ambient.Sample {
			b, _ := r.backendOf(res)
			return ambient.Sample{Status: res.Status, Backend: b, DurationMs: res.DurationMS}
		}, req.OnSample)
	}
	if n > 1 {
		for _, res := range results {
			b, _ := r.backendOf(res)
			obs.Samples = append(obs.Samples, ambient.Sample{Status: res.Status, Backend: b, DurationMs: res.DurationMS})
		}
		if len(obs.Samples) == 0 && len(results) > 0 {
			obs.Error = results[0].Error
		}
		// Count a run where every request failed as a transport error.
		failed := 0
		for _, res := range results {
			if res.Status == 0 {
				failed++
			}
		}
		if failed == len(results) && len(results) > 0 {
			obs.Samples = nil
			obs.Error = firstErr(results)
		}
		return obs, nil
	}
	res := results[0]
	obs.Status, obs.Error, obs.DurationMs = res.Status, res.Error, res.DurationMS
	if res.Status == 0 && (res.Error == "curl transport: connection-reset" || res.Error == "curl transport: connection-refused") {
		obs.Transport = strings.TrimPrefix(res.Error, "curl transport: ")
		obs.Error = ""
	}
	obs.Backend, obs.Pod = r.backendOf(res)
	return obs, nil
}

// sampleConcurrently sends n requests, at most width at a time, and returns
// the replies in the order they finished. Each reply is reported through
// onSample as it arrives. Once ctx is done no new request starts, and a
// request cut short by the stop is dropped rather than counted as a failure.
func sampleConcurrently(ctx context.Context, n, width int, send func() httpProbeResult, sample func(httpProbeResult) ambient.Sample, onSample func(ambient.Sample)) []httpProbeResult {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make([]httpProbeResult, 0, n)
	)
	sem := make(chan struct{}, width)
launch:
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			break launch
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := send()
			if res.Status == 0 && ctx.Err() != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			results = append(results, res)
			if onSample != nil {
				onSample(sample(res))
			}
		}()
	}
	wg.Wait()
	return results
}

func firstErr(rs []httpProbeResult) string {
	for _, r := range rs {
		if r.Error != "" {
			return r.Error
		}
	}
	return "No request completed."
}

// ---- decided by ------------------------------------------------------------

type suiteLogLine struct {
	ID        int64
	At        time.Time
	Namespace string
	Pod       string
	Raw       string
}

// matchWaypointLine finds the waypoint access-log line for a request: same
// method, path and status, from a waypoint Pod, closest in time to sentAt.
func matchWaypointLine(lines []suiteLogLine, c ambient.Case, status int, sentAt time.Time, wpName string, window time.Duration) (ambient.RBACDetail, *suiteLogLine, bool) {
	path := flows.StripQuery(c.Request.Path)
	var best *suiteLogLine
	var bestFields map[string]string
	var bestGap time.Duration
	for i := range lines {
		l := &lines[i]
		if wpName != "" && !strings.HasPrefix(l.Pod, wpName+"-") {
			continue
		}
		gap := l.At.Sub(sentAt)
		if gap < 0 {
			gap = -gap
		}
		if gap > window {
			continue
		}
		f, ok := flows.EnvoyFields(l.Raw)
		if !ok || f["method"] != c.Request.Method || flows.StripQuery(f["path"]) != path || f["response_code"] != strconv.Itoa(status) {
			continue
		}
		if best == nil || gap < bestGap {
			best, bestFields, bestGap = l, f, gap
		}
	}
	if best == nil {
		return ambient.RBACDetail{}, nil, false
	}
	return ambient.ParseRBACDetail(bestFields), best, true
}

// decisionFromExplain names the policy a reach.Explain step blames for the
// status the request got. ok is false when the explanation disagrees with
// what happened or names no policy.
func decisionFromExplain(x reach.Explanation, status int) (name string, ref *ambient.Ref, kind string, ok bool) {
	want := ""
	switch {
	case status == 403:
		want = reach.Denied
	case status >= 200 && status < 400:
		want = reach.Allowed
	default:
		return "", nil, "", false
	}
	if x.Verdict != want {
		return "", nil, "", false
	}
	for _, st := range x.Steps {
		if st.Verdict != want || len(st.Policies) == 0 {
			continue
		}
		p := st.Policies[0]
		r := ambient.Ref{Cluster: p.Cluster, Group: p.Group, Resource: p.Resource, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name}
		return p.Name, &r, "policy", true
	}
	return "", nil, "", false
}

type meshDecider struct {
	s     *Server
	spec  suiteRunSpec
	wpNS  string
	wpApp string
	// svc is the captured Service; policies are every AuthorizationPolicy
	// that applies to it, and wpPrincipal the identity of its waypoint.
	svc         *store.Object
	policies    []ambient.AppliedPolicy
	wpPrincipal string
	port        int
}

// decideLogWait is how long Decide waits for the waypoint's access log line
// to arrive. Envoy flushes its access log about once a second and the live
// collector stores lines about once a second, so the line for a request is
// usually not there yet when the reply comes back.
var decideLogWait = 4 * time.Second

func newMeshDecider(s *Server, spec suiteRunSpec) *meshDecider {
	d := &meshDecider{s: s, spec: spec}
	if s.WS == nil {
		return d
	}
	if svc := s.WS.Get(spec.Cluster, "", "services", spec.Service.Namespace, spec.Service.Name); svc != nil {
		d.svc = svc
		d.wpNS, d.wpApp, _ = ambient.Waypoint(s.WS, svc)
		d.policies = ambient.ServiceAuthzPolicies(s.WS, svc)
		d.wpPrincipal = ambient.WaypointPrincipal(s.WS, svc)
		if ps := ambient.L(ambient.Path(svc.Obj, "spec", "ports")); len(ps) > 0 {
			d.port = ambient.N(ambient.M(ps[0])["port"])
		}
	}
	return d
}

// waypointLines reads the waypoint's log records in [from, to].
func (s *Server) waypointLines(cluster, wpNS string, from, to time.Time, limit int) []suiteLogLine {
	if s.WS == nil {
		return nil
	}
	res, err := s.WS.SearchLogs(store.LogFilter{Cluster: cluster, Namespace: wpNS, From: from, To: to, Limit: limit})
	if err != nil || res == nil {
		return nil
	}
	lines := make([]suiteLogLine, 0, len(res.Records))
	for _, rec := range res.Records {
		if rec.Source == nil {
			continue
		}
		lines = append(lines, suiteLogLine{ID: rec.ID, At: rec.TS, Namespace: rec.Namespace, Pod: rec.Pod, Raw: rec.Raw})
	}
	return lines
}

// waypointLogAvailable reports whether the workspace holds access log lines
// from the waypoint in the ten minutes before at: when it does not, access
// logging is off or the waypoint's logs are not collected.
func (s *Server) waypointLogAvailable(cluster, wpNS, wpApp string, at time.Time) bool {
	if wpApp == "" {
		return false
	}
	for _, l := range s.waypointLines(cluster, wpNS, at.Add(-10*time.Minute), at.Add(time.Minute), 500) {
		if !strings.HasPrefix(l.Pod, wpApp+"-") {
			continue
		}
		if _, ok := flows.EnvoyFields(l.Raw); ok {
			return true
		}
	}
	return false
}

// findWaypointLine looks for the request's access log line, waiting up to
// decideLogWait for it when the waypoint's log is being collected at all.
func (d *meshDecider) findWaypointLine(ctx context.Context, c ambient.Case, status int, sentAt time.Time) (ambient.RBACDetail, *suiteLogLine, bool, bool) {
	deadline := time.Now().Add(decideLogWait)
	available := false
	for first := true; ; first = false {
		lines := d.s.waypointLines(d.spec.Cluster, d.wpNS, sentAt.Add(-5*time.Second), sentAt.Add(5*time.Second), 500)
		if det, line, ok := matchWaypointLine(lines, c, status, sentAt, d.wpApp, 5*time.Second); ok {
			return det, line, true, true
		}
		if first {
			available = d.s.waypointLogAvailable(d.spec.Cluster, d.wpNS, d.wpApp, sentAt)
		}
		if !available || time.Now().After(deadline) {
			return ambient.RBACDetail{}, nil, false, available
		}
		select {
		case <-ctx.Done():
			return ambient.RBACDetail{}, nil, false, available
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// predictionAgrees reports whether a prediction explains the status: the
// same status, or "allowed" for a reply that got past authorization (an
// application 404 is still a request the policies let through).
func predictionAgrees(p ambient.PolicyPrediction, status int) bool {
	if !p.Known {
		return false
	}
	if p.Status == status {
		return true
	}
	return p.Status == 200 && status >= 100 && status != 403 && status < 500
}

// predict evaluates every policy on the Service for the case as sent.
func (d *meshDecider) predict(c ambient.Case, client string) ambient.PolicyPrediction {
	caller := ambient.CallerFor(d.s.WS, d.spec.Cluster, client, c.From)
	return ambient.PredictRequest(d.policies, c.Request, d.port, caller, d.wpApp != "", d.wpPrincipal)
}

func (d *meshDecider) Decide(ctx context.Context, suite *ambient.Suite, c ambient.Case, obs *ambient.Observation, sentAt time.Time) {
	if c.Request.Direct {
		// Direct traffic never reaches the waypoint: ztunnel is the only thing
		// on the path, and it has no L7 policy.
		if obs.Status >= 200 && obs.Status < 400 {
			obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "ztunnel (no L7)", "ztunnel", "path"
		} else {
			obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "ztunnel", "ztunnel", "path"
		}
		return
	}
	// 1. The waypoint's own access log, within five seconds of the request.
	logAvailable := false
	if d.wpApp != "" && d.s.WS != nil {
		det, line, ok, avail := d.findWaypointLine(ctx, c, obs.Status, sentAt)
		logAvailable = avail
		if ok {
			obs.Log = &ambient.LogRef{Cluster: d.spec.Cluster, Namespace: line.Namespace, Pod: line.Pod, At: line.At, ID: line.ID, Line: line.Raw}
			if obs.Backend == "" {
				obs.Backend = det.Backend
			}
			switch {
			case det.Policy != "":
				ref := ambient.Ref{Cluster: d.spec.Cluster, Group: ambient.SecurityGroup, Resource: "authorizationpolicies", Kind: ambient.KindAuthorizationPolicy, Namespace: det.Namespace, Name: det.Policy}
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow, obs.DecidedByRef = det.Policy, "policy", "waypoint-log", &ref
				return
			case det.NoMatch:
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "no ALLOW matched", "none", "waypoint-log"
				return
			}
		}
	}
	// 2. Every AuthorizationPolicy on the Service, its waypoint, its
	// namespace and the root namespace, evaluated the way Istio does.
	var pred ambient.PolicyPrediction
	if d.svc != nil {
		pred = d.predict(c, obs.Client)
		if predictionAgrees(pred, obs.Status) {
			obs.DecidedBy, obs.DecidedByHow = pred.DecidedBy, "predicted"
			obs.DecidedByKind = "none"
			if pred.By != nil {
				obs.DecidedByKind, obs.DecidedByRef = "policy", pred.By
			}
			return
		}
	}
	// 3. reach.Explain, when the sender is a captured workload.
	if obs.Client != "" && d.s.WS != nil {
		if x, err := reach.Explain(d.s.WS, d.spec.Cluster, obs.Client, d.spec.Service.Namespace+"/"+d.spec.Service.Name); err == nil {
			if name, ref, kind, ok := decisionFromExplain(x, obs.Status); ok {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow, obs.DecidedByRef = name, kind, "explain", ref
				return
			}
		}
	}
	// 4. The suite's own policy input (a draft that is not captured yet).
	if d.spec.Authz != nil {
		if by, status, ok := ambient.PredictDecision(*d.spec.Authz, c); ok && status == obs.Status {
			if by != nil {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow, obs.DecidedByRef = by.Name, "policy", "predicted", by
			} else if status == 403 {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "no ALLOW matched", "none", "predicted"
			} else {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "no policy refused it", "none", "predicted"
			}
			return
		}
	}
	// Nothing could say: leave DecidedBy empty and say why.
	obs.DecisionNote = decisionNote(d.wpApp != "", logAvailable, pred, obs.Status)
}

func decisionNote(hasWaypoint, logAvailable bool, pred ambient.PolicyPrediction, status int) string {
	var s string
	switch {
	case !hasWaypoint:
		s = "The Service has no waypoint, so there is no waypoint log line"
	case !logAvailable:
		s = "Waypoint access logging is off or its logs are not collected, so there is no log line"
	default:
		s = "No waypoint log line matched"
	}
	s += fmt.Sprintf(" and no policy predicts %d", status)
	switch {
	case !pred.Known && pred.Verdict != "":
		s += ". " + pred.Verdict
	case pred.Known:
		s += fmt.Sprintf(" (the policies predict %d)", pred.Status)
	}
	return s
}
