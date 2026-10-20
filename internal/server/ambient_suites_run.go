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
	byID := map[string]ambient.Case{}
	for _, c := range suite.Cases {
		byID[c.ID] = c
	}
	run := &ambient.SuiteRun{At: now(), Source: suite.Source, Results: []ambient.CaseResult{}}
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			continue
		}
		if ctx.Err() != nil {
			run.Results = append(run.Results, ambient.CaseResult{ID: id, Status: ambient.StatusSkipped, Message: "The run stopped before this case was sent.", DecidedBy: "unknown", DecidedByKind: "unknown", At: now()})
			continue
		}
		sentAt := now()
		samples := 0
		if len(c.Expect.Weights) > 0 {
			samples = c.Expect.Samples
			if samples == 0 {
				samples = ambient.DefaultSamples
			}
		}
		obs, err := runner.Send(ctx, suiteSendRequest{Case: c, Source: suite.Source, Samples: samples})
		var skip skipCase
		switch {
		case errors.As(err, &skip):
			run.Results = append(run.Results, ambient.CaseResult{ID: id, Status: ambient.StatusSkipped, Message: string(skip), DecidedBy: "unknown", DecidedByKind: "unknown", Connection: obs.Connection, At: now()})
			continue
		case err != nil:
			obs.Error = err.Error()
		}
		if decider != nil && obs.Error == "" && len(c.Expect.Weights) == 0 {
			safeDecide(ctx, decider, suite, c, &obs, sentAt)
		}
		run.Results = append(run.Results, ambient.Evaluate(c, obs, now()))
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

	profile := httpProfile{Method: c.Request.Method, Path: c.Request.Path, Headers: c.Request.Headers, Timeout: 10}
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
	results := make([]httpProbeResult, n)
	if n == 1 {
		results[0] = runMeshCurl(ctx, r.cfg, r.client, q, address)
	} else {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i := 0; i < n; i++ {
			if ctx.Err() != nil {
				results = results[:i]
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] = runMeshCurl(ctx, r.cfg, r.client, q, address)
			}(i)
		}
		wg.Wait()
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
	obs.Status, obs.Error = res.Status, res.Error
	obs.Backend, obs.Pod = r.backendOf(res)
	if res.Status != 0 {
		obs.Error = ""
	}
	return obs, nil
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
}

func newMeshDecider(s *Server, spec suiteRunSpec) *meshDecider {
	d := &meshDecider{s: s, spec: spec}
	if svc := s.WS.Get(spec.Cluster, "", "services", spec.Service.Namespace, spec.Service.Name); svc != nil {
		d.wpNS, d.wpApp, _ = ambient.Waypoint(s.WS, svc)
	}
	return d
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
	if d.wpApp != "" && d.s.WS != nil {
		if res, err := d.s.WS.SearchLogs(store.LogFilter{Cluster: d.spec.Cluster, Namespace: d.wpNS, From: sentAt.Add(-5 * time.Second), To: sentAt.Add(5 * time.Second), Limit: 500}); err == nil && res != nil {
			lines := make([]suiteLogLine, 0, len(res.Records))
			for _, rec := range res.Records {
				if rec.Source == nil {
					continue
				}
				lines = append(lines, suiteLogLine{ID: rec.ID, At: rec.TS, Namespace: rec.Namespace, Pod: rec.Pod, Raw: rec.Raw})
			}
			if det, line, ok := matchWaypointLine(lines, c, obs.Status, sentAt, d.wpApp, 5*time.Second); ok {
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
	}
	// 2. reach.Explain, when the sender is a captured workload.
	if obs.Client != "" && d.s.WS != nil {
		if x, err := reach.Explain(d.s.WS, d.spec.Cluster, obs.Client, d.spec.Service.Namespace+"/"+d.spec.Service.Name); err == nil {
			if name, ref, kind, ok := decisionFromExplain(x, obs.Status); ok {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow, obs.DecidedByRef = name, kind, "explain", ref
				return
			}
		}
	}
	// 3. Flightbay's own evaluator, only when it agrees with what happened.
	if d.spec.Authz != nil {
		if by, status, ok := ambient.PredictDecision(*d.spec.Authz, c); ok && status == obs.Status {
			if by != nil {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow, obs.DecidedByRef = by.Name, "policy", "predicted", by
			} else if status == 403 {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "no ALLOW matched", "none", "predicted"
			} else {
				obs.DecidedBy, obs.DecidedByKind, obs.DecidedByHow = "no policy refused it", "none", "predicted"
			}
		}
	}
	// Otherwise left empty: Evaluate records "unknown".
}
