package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/policy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const maxMeshRequests = 1000

type meshClientOption struct {
	Pod       ambient.Ref `json:"pod"`
	Container string      `json:"container"`
	UID       string      `json:"uid"`
	Ambient   bool        `json:"ambient"`
}

func (s *Server) meshClientOptions(cluster string) []meshClientOption {
	out := []meshClientOption{}
	for _, pod := range s.WS.List(cluster, "", "pods", "", nil) {
		if ambient.S(ambient.Path(pod.Obj, "status", "phase")) != "Running" || ambient.Path(pod.Obj, "metadata", "deletionTimestamp") != nil || ambient.S(ambient.Path(pod.Obj, "metadata", "uid")) == "" {
			continue
		}
		ready := false
		for _, v := range ambient.L(ambient.Path(pod.Obj, "status", "conditions")) {
			c := ambient.M(v)
			ready = ready || ambient.S(c["type"]) == "Ready" && ambient.S(c["status"]) == "True"
		}
		if !ready {
			continue
		}
		ns := s.WS.Get(cluster, "", "namespaces", "", pod.Namespace)
		enrolled := ambient.S(ambient.Path(pod.Obj, "metadata", "annotations", "ambient.istio.io/redirection")) == "enabled"
		if ns != nil {
			enrolled = enrolled || ns.Labels()["istio.io/dataplane-mode"] == "ambient"
		}
		for _, v := range ambient.L(ambient.Path(pod.Obj, "spec", "containers")) {
			c := ambient.M(v)
			image := ambient.S(c["image"])
			if !strings.Contains(image, "curlimages/curl:") && !strings.Contains(image, "curlimages/curl@") {
				continue
			}
			out = append(out, meshClientOption{ambient.Ref{Cluster: cluster, Resource: "pods", Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, ambient.S(c["name"]), ambient.S(ambient.Path(pod.Obj, "metadata", "uid")), enrolled})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Pod.Namespace+"/"+out[i].Pod.Name < out[j].Pod.Namespace+"/"+out[j].Pod.Name
	})
	return out
}

type meshProbeRequest struct {
	Service   ambient.Ref `json:"service"`
	Context   string      `json:"context"`
	Client    ambient.Ref `json:"client"`
	Container string      `json:"container"`
	Names     []string    `json:"names"`
	Port      int         `json:"port"`
	Count     int         `json:"count"`
	Profile   httpProfile `json:"profile"`
	// Ephemeral asks for a temporary client Pod in this namespace, created
	// for the test and deleted when it ends. Client and Container are ignored.
	Ephemeral *struct {
		Namespace string `json:"namespace"`
	} `json:"ephemeral,omitempty"`
}
type meshRouteRule struct {
	Route    ambient.Ref        `json:"route"`
	Rule     int                `json:"rule"`
	Current  bool               `json:"current"`
	Matches  any                `json:"matches"`
	Backends []meshRouteBackend `json:"backends"`
}
type meshRouteBackend struct {
	ReadyEndpoints *int    `json:"readyEndpoints,omitempty"`
	Namespace      string  `json:"namespace"`
	Name           string  `json:"name"`
	Weight         int     `json:"weight"`
	Percent        float64 `json:"percent"`
}
type meshResponder struct {
	Pod      ambient.Ref `json:"pod"`
	UID      string      `json:"uid"`
	Version  string      `json:"version"`
	Service  string      `json:"service"`
	Evidence string      `json:"evidence"`
}
type meshProbeResult struct {
	httpProbeResult
	Index       int            `json:"index"`
	Responder   *meshResponder `json:"responder,omitempty"`
	Attribution string         `json:"attribution,omitempty"`
}
type meshDistribution struct {
	Name       string         `json:"name"`
	Responder  *meshResponder `json:"responder,omitempty"`
	Count      int            `json:"count"`
	Successful int            `json:"successful"`
	Percent    float64        `json:"percent"`
}
type meshProbeSummary struct {
	Requested    int                `json:"requested"`
	Completed    int                `json:"completed"`
	Successful   int                `json:"successful"`
	Failed       int                `json:"failed"`
	Unknown      int                `json:"unknown"`
	Unattempted  int                `json:"unattempted"`
	Distribution []meshDistribution `json:"distribution"`
	Stopped      bool               `json:"stopped"`
}

func summarizeMeshProbe(results []meshProbeResult, requested int) meshProbeSummary {
	s := meshProbeSummary{Requested: requested, Completed: len(results), Unattempted: requested - len(results), Distribution: []meshDistribution{}}
	groups := map[string]*meshDistribution{}
	perName := map[string]int{}
	for _, r := range results {
		perName[r.Name]++
		ok := r.Error == "" && r.Status >= 200 && r.Status < 300
		if ok {
			s.Successful++
		} else {
			s.Failed++
		}
		key := r.Name + "/unknown"
		if r.Responder != nil {
			key = r.Name + "/" + r.Responder.Pod.Cluster + "/" + r.Responder.UID
		} else {
			s.Unknown++
		}
		g := groups[key]
		if g == nil {
			g = &meshDistribution{Name: r.Name, Responder: r.Responder}
			groups[key] = g
		}
		g.Count++
		if ok {
			g.Successful++
		}
	}
	for _, g := range groups {
		if perName[g.Name] > 0 {
			g.Percent = 100 * float64(g.Count) / float64(perName[g.Name])
		}
		s.Distribution = append(s.Distribution, *g)
	}
	sort.Slice(s.Distribution, func(i, j int) bool {
		a, b := s.Distribution[i], s.Distribution[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Responder == nil {
			return false
		}
		if b.Responder == nil {
			return true
		}
		if a.Responder.Pod.Cluster != b.Responder.Pod.Cluster {
			return a.Responder.Pod.Cluster < b.Responder.Pod.Cluster
		}
		return a.Responder.Pod.Name < b.Responder.Pod.Name
	})
	return s
}

func meshResponseIdentity(r httpProbeResult, pods []corev1.Pod, services []corev1.Service, backends map[string]bool) *meshResponder {
	var body struct {
		PodName string `json:"podname"`
	}
	_ = json.Unmarshal([]byte(r.Body), &body)
	name := body.PodName
	evidence := "Response JSON podname matched a live Pod"
	if name == "" {
		name = r.ResponseHeaders["X-Pod-Name"]
		evidence = "Response X-Pod-Name matched a live Pod"
	}
	if name == "" {
		return nil
	}
	for _, pod := range pods {
		if pod.Name != name || pod.UID == "" {
			continue
		}
		out := &meshResponder{Pod: ambient.Ref{Kind: "Pod", Resource: "pods", Namespace: pod.Namespace, Name: pod.Name}, UID: string(pod.UID), Version: pod.Labels["version"], Evidence: evidence}
		matches := []string{}
		for _, svc := range services {
			if !backends[svc.Namespace+"/"+svc.Name] || len(svc.Spec.Selector) == 0 {
				continue
			}
			if labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(pod.Labels)) {
				matches = append(matches, svc.Namespace+"/"+svc.Name)
			}
		}
		if len(matches) == 1 {
			out.Service = matches[0]
			out.Evidence += "; backend Service selector and version label read from live objects"
		}
		return out
	}
	return nil
}

type meshResponderInventory struct {
	Cluster  string
	Pods     []corev1.Pod
	Services []corev1.Service
	Backends map[string]bool
}

func meshResponseAcrossClusters(r httpProbeResult, inventories []meshResponderInventory) (*meshResponder, string) {
	var matched *meshResponder
	for _, inventory := range inventories {
		candidate := meshResponseIdentity(r, inventory.Pods, inventory.Services, inventory.Backends)
		if candidate == nil {
			continue
		}
		if matched != nil {
			return nil, "Ambiguous responder: the returned Pod name exists in more than one verified cluster."
		}
		candidate.Pod.Cluster = inventory.Cluster
		candidate.Evidence += "; uniquely matched across verified cluster inventories read at test start"
		matched = candidate
	}
	if matched == nil {
		return nil, "Returned identity could not be matched to a live Pod in the verified cluster inventories."
	}
	return matched, matched.Evidence
}

func loadMeshResponderInventory(ctx context.Context, c ambientConnection, cluster, namespace, anchorUID string) (meshResponderInventory, error) {
	out := meshResponderInventory{Cluster: cluster, Backends: map[string]bool{}}
	if anchorUID == "" {
		return out, fmt.Errorf("captured kube-system identity is missing")
	}
	anchor, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	if string(anchor.GetUID()) != anchorUID {
		return out, fmt.Errorf("cluster identity differs from the capture")
	}
	pods, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return out, err
	}
	services, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return out, err
	}
	for _, pod := range pods.Items {
		var value corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pod.Object, &value); err != nil {
			return out, err
		}
		out.Pods = append(out.Pods, value)
	}
	for _, service := range services.Items {
		var value corev1.Service
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(service.Object, &value); err != nil {
			return out, err
		}
		out.Services = append(out.Services, value)
	}
	return out, nil
}

func (s *Server) meshRemoteResponderInventories(ctx context.Context, q meshProbeRequest) ([]meshResponderInventory, []meshRouteRule, []string) {
	out, routes, warnings := []meshResponderInventory{}, []meshRouteRule{}, []string{}
	contexts, _, _, err := KubeContexts(s.AmbientKubeconfig)
	if err != nil {
		return out, routes, []string{"Remote responder attribution unavailable: " + err.Error()}
	}
	for _, cluster := range s.WS.Clusters() {
		if ctx.Err() != nil {
			warnings = append(warnings, "Responder inventory lookup stopped before all captured clusters were read.")
			break
		}
		if cluster == q.Service.Cluster {
			continue
		}
		name := ""
		for _, context := range contexts {
			if s.ambientLiveCluster(context.Name, context.Server) == cluster {
				if name != "" {
					name = ""
					break
				}
				name = context.Name
			}
		}
		if name == "" {
			warnings = append(warnings, cluster+": no uniquely verified live connection for responder attribution.")
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		c, connectErr := s.ambientConnect(name)
		if connectErr != nil {
			cancel()
			warnings = append(warnings, cluster+": responder inventory unavailable: "+connectErr.Error())
			continue
		}
		anchor := s.WS.Get(cluster, "", "namespaces", "", "kube-system")
		uid := ""
		if anchor != nil {
			uid = ambient.S(ambient.Path(anchor.Obj, "metadata", "uid"))
		}
		inventory, loadErr := loadMeshResponderInventory(lookupCtx, c, cluster, q.Service.Namespace, uid)
		if loadErr == nil {
			ref := q.Service
			ref.Cluster = cluster
			clusterRoutes, backends, notices := meshRouteEvidence(lookupCtx, c, ref, q.Port)
			backends[q.Service.Namespace+"/"+q.Service.Name] = len(backends) == 0
			inventory.Backends = backends
			out = append(out, inventory)
			routes = append(routes, clusterRoutes...)
			for _, notice := range notices {
				warnings = append(warnings, cluster+": "+notice)
			}
		} else {
			warnings = append(warnings, cluster+": responder inventory unavailable: "+loadErr.Error())
		}
		cancel()
	}
	return out, routes, warnings
}

// Native traffic does not require Kubernetes access. Inventory reads are
// optional attribution evidence; failure never deploys a helper or blocks HTTP.
func (s *Server) nativeResponderInventories(ctx context.Context, q httpProbeRequest) ([]meshResponderInventory, []meshRouteRule, []string) {
	out, routes, warnings := []meshResponderInventory{}, []meshRouteRule{}, []string{}
	if q.Context == "" {
		return out, routes, []string{"No cluster connection selected. Native HTTP still runs; responder cluster will remain unverified."}
	}
	c, err := s.ambientConnect(q.Context)
	if err != nil {
		return out, routes, []string{"Responder inventory unavailable: " + err.Error()}
	}
	anchor := s.WS.Get(q.Service.Cluster, "", "namespaces", "", "kube-system")
	uid := ""
	if anchor != nil {
		uid = ambient.S(ambient.Path(anchor.Obj, "metadata", "uid"))
	}
	inventory, err := loadMeshResponderInventory(ctx, c, q.Service.Cluster, q.Service.Namespace, uid)
	if err != nil {
		return out, routes, []string{"Responder inventory unavailable: " + err.Error()}
	}
	routes, backends, warnings := meshRouteEvidence(ctx, c, q.Service, q.Port)
	backends[q.Service.Namespace+"/"+q.Service.Name] = len(backends) == 0
	inventory.Backends = backends
	out = append(out, inventory)
	remote, remoteRoutes, notices := s.meshRemoteResponderInventories(ctx, meshProbeRequest{Service: q.Service, Context: q.Context, Port: q.Port})
	out = append(out, remote...)
	routes = append(routes, remoteRoutes...)
	warnings = append(warnings, notices...)
	return out, routes, warnings
}

// A bounded writer prevents an exec response from retaining unbounded output.
type meshOutput struct {
	bytes.Buffer
	Max      int
	Overflow bool
}

func (b *meshOutput) Write(p []byte) (int, error) {
	if len(p) > b.Max-b.Len() {
		b.Overflow = true
		return 0, fmt.Errorf("HTTP response exceeded the output limit")
	}
	return b.Buffer.Write(p)
}

func meshCurlCommand(p httpProfile, address, marker, id string) []string {
	args := []string{"curl", "--silent", "--show-error", "--include", "--raw", "--noproxy", "*", "--max-time", strconv.Itoa(p.Timeout), "--max-filesize", "32768", "--request", p.Method, "--header", "X-Flightbay-Test-ID: " + id, "--write-out", "\n" + marker + " %{http_code} %{time_total}\n"}
	keys := []string{}
	for k := range p.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--header", k+": "+p.Headers[k])
	}
	if p.Body != "" {
		args = append(args, "--data-binary", "@-")
	}
	return append(args, "--url", address)
}

func parseMeshCurl(output, marker string, p httpProfile, result *httpProbeResult) {
	at := strings.LastIndex(output, "\n"+marker+" ")
	if at < 0 {
		result.Error = "Client returned no complete HTTP result"
		return
	}
	fields := strings.Fields(output[at+1:])
	if len(fields) >= 3 {
		result.Status, _ = strconv.Atoi(fields[1])
	}
	reader := bufio.NewReader(strings.NewReader(output[:at]))
	request := &http.Request{Method: p.Method}
	response, err := http.ReadResponse(reader, request)
	for err == nil && response.StatusCode >= 100 && response.StatusCode < 200 {
		response.Body.Close()
		response, err = http.ReadResponse(reader, request)
	}
	if err != nil {
		if result.Status != 0 {
			result.Error = "Could not parse the client HTTP response"
		}
		return
	}
	defer response.Body.Close()
	result.ResponseHeaders = map[string]string{}
	for k, values := range response.Header {
		value := strings.Join(values, ", ")
		if sensitiveHTTPHeader(k) {
			value = "[redacted]"
		}
		result.ResponseHeaders[k] = probeRedact(value, p)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32769))
	result.Truncated = len(body) > 32768
	if result.Truncated {
		body = body[:32768]
	}
	result.Body = probeRedact(string(body), p)
	if err != nil {
		result.Error = probeRedact(err.Error(), p)
	}
}

func runMeshCurl(ctx context.Context, cfg *rest.Config, client kubernetes.Interface, q meshProbeRequest, name string) httpProbeResult {
	random := make([]byte, 12)
	_, _ = rand.Read(random)
	id := hex.EncodeToString(random)
	marker := "FLIGHTBAY_" + id
	address := "http://" + net.JoinHostPort(name, strconv.Itoa(q.Port)) + q.Profile.Path
	note := "Request executed by curl in the existing client Pod using cluster DNS and the normal Service/mesh path. No Pod is created. Responder identity is read from the response and matched to live Pod metadata."
	if q.Ephemeral != nil {
		note = "Request executed by curl in the temporary flightbay-http Pod using cluster DNS and the normal Service/mesh path. The Pod is deleted when the test ends. Responder identity is read from the response and matched to live Pod metadata."
	}
	out := httpProbeResult{ID: id, Mode: "mesh", Name: name, Authority: net.JoinHostPort(name, strconv.Itoa(q.Port)), Connection: address, Started: time.Now().UTC(), Pod: &q.Client, Note: note}
	for k, v := range q.Profile.Headers {
		if strings.EqualFold(k, "Host") {
			out.Authority = v
		}
	}
	request := client.CoreV1().RESTClient().Post().Resource("pods").Namespace(q.Client.Namespace).Name(q.Client.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: q.Container, Command: meshCurlCommand(q.Profile, address, marker, id), Stdin: q.Profile.Body != "", Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", request.URL())
	stdout, stderr := &meshOutput{Max: 96 << 10}, &meshOutput{Max: 8 << 10}
	if err == nil {
		requestCtx, cancel := context.WithTimeout(ctx, time.Duration(q.Profile.Timeout+5)*time.Second)
		defer cancel()
		var stdin io.Reader
		if q.Profile.Body != "" {
			stdin = strings.NewReader(q.Profile.Body)
		}
		err = executor.StreamWithContext(requestCtx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
	}
	parseMeshCurl(stdout.String(), marker, q.Profile, &out)
	if err != nil {
		out.Error = probeRedact(strings.TrimSpace(stderr.String()+" "+err.Error()), q.Profile)
		if out.Status == 0 && strings.Contains(stdout.String(), "\n"+marker+" 000 ") {
			if transport := classifyMeshCurlTransport(stderr.String(), err); transport != "" {
				out.Error = "curl transport: " + transport
			}
		}
	}
	if out.Status == 0 && out.Error == "" {
		out.Error = "Client returned curl 000 without a classified connection refusal"
	}
	out.Truncated = out.Truncated || stdout.Overflow
	out.Finished = time.Now().UTC()
	out.DurationMS = out.Finished.Sub(out.Started).Milliseconds()
	return out
}

// An exec/RBAC error, DNS failure or timeout is never a successful denial.
// Require both curl's remote exit status and its matching diagnostic.
func classifyMeshCurlTransport(stderr string, err error) string {
	exit, ok := err.(interface{ ExitStatus() int })
	if !ok {
		return ""
	}
	message := strings.ToLower(stderr)
	if exit.ExitStatus() == 56 && strings.Contains(message, "curl: (56)") && strings.Contains(message, "connection reset") {
		return "connection-reset"
	}
	if exit.ExitStatus() == 7 && strings.Contains(message, "curl: (7)") && strings.Contains(message, "connection refused") {
		return "connection-refused"
	}
	return ""
}

func meshRouteEvidence(ctx context.Context, c ambientConnection, target ambient.Ref, port int) ([]meshRouteRule, map[string]bool, []string) {
	rules := []meshRouteRule{}
	backends := map[string]bool{}
	warnings := []string{}
	routes, err := c.client.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "httproutes"}).Namespace(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return rules, backends, []string{"Route configuration could not be read: " + err.Error()}
	}
	routeCount := 0
	for _, route := range routes.Items {
		parent := false
		for _, value := range ambient.L(ambient.Path(route.Object, "spec", "parentRefs")) {
			p := ambient.M(value)
			ns := ambient.S(p["namespace"])
			if ns == "" {
				ns = route.GetNamespace()
			}
			parent = parent || ambient.S(p["kind"]) == "Service" && ambient.S(p["group"]) == "" && ns == target.Namespace && ambient.S(p["name"]) == target.Name && (p["port"] == nil || ambient.N(p["port"]) == port)
		}
		if !parent {
			continue
		}
		routeCount++
		accepted, _ := currentDemoCondition(&route, "Accepted")
		resolved, _ := currentDemoCondition(&route, "ResolvedRefs")
		for index, value := range ambient.L(ambient.Path(route.Object, "spec", "rules")) {
			rule := ambient.M(value)
			row := meshRouteRule{Route: ambient.Ref{Cluster: target.Cluster, Group: ambient.GatewayGroup, Resource: "httproutes", Kind: "HTTPRoute", Namespace: route.GetNamespace(), Name: route.GetName()}, Rule: index, Current: accepted && resolved, Matches: rule["matches"], Backends: []meshRouteBackend{}}
			total := 0
			for _, value := range ambient.L(rule["backendRefs"]) {
				b := ambient.M(value)
				if ambient.S(b["group"]) != "" || (ambient.S(b["kind"]) != "" && ambient.S(b["kind"]) != "Service") {
					continue
				}
				ns := ambient.S(b["namespace"])
				if ns == "" {
					ns = route.GetNamespace()
				}
				weight := 1
				if b["weight"] != nil {
					weight = ambient.N(b["weight"])
				}
				row.Backends = append(row.Backends, meshRouteBackend{Namespace: ns, Name: ambient.S(b["name"]), Weight: weight})
				if ambient.S(b["group"]) == "" && (ambient.S(b["kind"]) == "" || ambient.S(b["kind"]) == "Service") {
					ready, err := meshBackendReadyEndpoints(ctx, c, ns, ambient.S(b["name"]), ambient.N(b["port"]))
					if err != nil {
						warnings = append(warnings, "Backend readiness could not be read for "+ns+"/"+ambient.S(b["name"])+": "+err.Error())
					} else {
						row.Backends[len(row.Backends)-1].ReadyEndpoints = &ready
						if ready == 0 && weight > 0 {
							warnings = append(warnings, fmt.Sprintf("Backend %s/%s has no ready endpoints on the route's port (weight %d). Requests selected for it can return HTTP 503 no healthy upstream; weights do not provide failover to another Service.", ns, ambient.S(b["name"]), weight))
						}
					}
				}
				backends[ns+"/"+ambient.S(b["name"])] = true
				total += weight
			}
			for i := range row.Backends {
				if total > 0 {
					row.Backends[i].Percent = 100 * float64(row.Backends[i].Weight) / float64(total)
				}
			}
			rules = append(rules, row)
		}
	}
	if routeCount > 1 {
		warnings = append(warnings, "Multiple Service-parent HTTPRoutes reference this Service. Check their matches and precedence; a newer weighted route does not necessarily replace the existing route.")
	}
	if len(rules) == 0 {
		// No route is the normal case (no split configured); not a notice.
	}
	return rules, backends, warnings
}

func meshBackendReadyEndpoints(ctx context.Context, c ambientConnection, ns, name string, port int) (int, error) {
	svc, err := c.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	portName, found := "", false
	for _, v := range ambient.L(ambient.Path(svc.Object, "spec", "ports")) {
		p := ambient.M(v)
		if ambient.N(p["port"]) == port {
			portName, found = ambient.S(p["name"]), true
			break
		}
	}
	if !found {
		return 0, fmt.Errorf("Service does not expose port %d", port)
	}
	slices, err := c.client.Resource(schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + name})
	if err != nil {
		return 0, err
	}
	ready := 0
	for _, slice := range slices.Items {
		portFound := false
		for _, v := range ambient.L(slice.Object["ports"]) {
			p := ambient.M(v)
			portFound = portFound || ambient.S(p["name"]) == portName && ambient.N(p["port"]) > 0
		}
		if !portFound {
			continue
		}
		for _, v := range ambient.L(slice.Object["endpoints"]) {
			e := ambient.M(v)
			if ambient.Path(e, "conditions", "ready") != false && len(ambient.L(e["addresses"])) > 0 {
				ready++
			}
		}
	}
	return ready, nil
}

func (s *Server) httpMeshProbe(w http.ResponseWriter, r *http.Request) {
	if !probePost(w, r) {
		return
	}
	var q meshProbeRequest
	if json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&q) != nil {
		jerr(w, 400, "invalid mesh test request")
		return
	}
	if q.Context == "" {
		jerr(w, 400, "no matching live connection is available; choose a real context")
		return
	}
	if err := validateHTTPProfile(&q.Profile); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	if q.Count < 1 || q.Count > maxMeshRequests || len(q.Names) < 1 || len(q.Names) > 8 || q.Count*len(q.Names) > maxMeshRequests {
		jerr(w, 400, "choose 1–1000 requests total across selected DNS names")
		return
	}
	v, err := ambient.NewView(s.WS, q.Service.Cluster, "Service", q.Service.Namespace, q.Service.Name, "")
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	allowed := append([]string{v.DNS.LocalName}, v.DNS.GlobalNames...)
	seen := map[string]bool{}
	for _, name := range q.Names {
		found := false
		for _, a := range allowed {
			found = found || name != "" && name == a
		}
		if !found || seen[name] {
			jerr(w, 400, "choose distinct captured DNS names for this Service")
			return
		}
		seen[name] = true
	}
	var option *meshClientOption
	ephemeralNS, ephemeralEnrolled := "", false
	if q.Ephemeral != nil {
		ephemeralNS = strings.TrimSpace(q.Ephemeral.Namespace)
		ns := s.WS.Get(q.Service.Cluster, "", "namespaces", "", ephemeralNS)
		if ephemeralNS == "" || ns == nil {
			jerr(w, 400, "choose a captured namespace for the temporary test Pod")
			return
		}
		ephemeralEnrolled = ns.Labels()["istio.io/dataplane-mode"] == "ambient"
	} else {
		for _, o := range s.meshClientOptions(q.Service.Cluster) {
			if o.Pod == q.Client && o.Container == q.Container {
				copy := o
				option = &copy
				break
			}
		}
		if option == nil {
			jerr(w, 400, "choose an existing captured Ready curl client Pod in the target cluster")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	p, _ := policy.Profile("minimal")
	cfg, err := collect.RestConfig(s.AmbientKubeconfig, q.Context, p, "flightbay-mesh-http-test")
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	cfg.Timeout = 20 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	svc, err := client.CoreV1().Services(q.Service.Namespace).Get(ctx, q.Service.Name, metav1.GetOptions{})
	captured := s.WS.Get(q.Service.Cluster, "", "services", q.Service.Namespace, q.Service.Name)
	if err != nil || captured == nil || string(svc.UID) == "" || string(svc.UID) != ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) {
		jerr(w, 409, "Service identity differs from the capture/context; refresh before testing")
		return
	}
	portOK := false
	for _, port := range svc.Spec.Ports {
		portOK = portOK || int(port.Port) == q.Port && port.Protocol == corev1.ProtocolTCP
	}
	if !portOK {
		jerr(w, 400, "choose a captured TCP Service port")
		return
	}
	var ephemeral map[string]string
	preWarnings := []string{}
	cleanup := func() (bool, error) { return false, nil }
	if q.Ephemeral != nil {
		removed := sweepStaleEphemeralClients(ctx, client, ephemeralNS, time.Now())
		pod, err := startEphemeralClient(ctx, client, ephemeralNS, time.Second, ephemeralReadyTimeout)
		if err != nil {
			code := 502
			if apierrors.IsForbidden(err) {
				code = 403
			}
			jerr(w, code, err.Error())
			return
		}
		done := false
		cleanup = func() (bool, error) {
			if done {
				return false, nil
			}
			done = true
			err := deleteEphemeralClient(client, ephemeralNS, pod.Name)
			return err == nil, err
		}
		// Safety net for every early return and panic below; the normal path
		// deletes synchronously and reports the result as a cleanup event.
		defer cleanup()
		q.Client = ambient.Ref{Cluster: q.Service.Cluster, Resource: "pods", Kind: "Pod", Namespace: ephemeralNS, Name: pod.Name}
		q.Container = ephemeralContainer
		option = &meshClientOption{Pod: q.Client, Container: ephemeralContainer, UID: string(pod.UID), Ambient: ephemeralEnrolled}
		ephemeral = map[string]string{"namespace": ephemeralNS, "name": pod.Name}
		preWarnings = append(preWarnings, "Temporary Pod "+ephemeralNS+"/"+pod.Name+" was created for this test and is deleted when it ends.")
		if len(removed) > 0 {
			preWarnings = append(preWarnings, "Removed "+strconv.Itoa(len(removed))+" leftover temporary test Pod(s) older than 15 minutes: "+strings.Join(removed, ", ")+".")
		}
		if !ephemeralEnrolled {
			preWarnings = append(preWarnings, "Namespace "+ephemeralNS+" is not labelled istio.io/dataplane-mode=ambient, so the temporary Pod is not in the mesh and this is not an in-mesh test.")
		}
	} else {
		source, err := client.CoreV1().Pods(q.Client.Namespace).Get(ctx, q.Client.Name, metav1.GetOptions{})
		if err != nil || string(source.UID) != option.UID || source.DeletionTimestamp != nil {
			jerr(w, 409, "client Pod identity changed; reopen the test")
			return
		}
		if !podReady(source) {
			jerr(w, 409, "client Pod is no longer Ready")
			return
		}
		containerOK := false
		for _, c := range source.Spec.Containers {
			containerOK = containerOK || c.Name == q.Container && (strings.Contains(c.Image, "curlimages/curl:") || strings.Contains(c.Image, "curlimages/curl@"))
		}
		if !containerOK {
			jerr(w, 409, "client container changed; reopen the test")
			return
		}
	}
	pods, err := client.CoreV1().Pods(q.Service.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	services, err := client.CoreV1().Services(q.Service.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	c, err := s.ambientConnect(q.Context)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	rules, backends, warnings := meshRouteEvidence(ctx, c, q.Service, q.Port)
	if svc.Labels["solo.io/service-takeover"] == "true" && len(rules) > 0 {
		warnings = append(warnings, "Local-name takeover does not replace HTTPRoute backend references. Requests to the local name still follow the matching route's selected Services; the global DNS name can have a different route. An empty local version backend can return 503 even when the global Service has healthy remote endpoints.")
	}
	backends[q.Service.Namespace+"/"+q.Service.Name] = len(backends) == 0
	inventories := []meshResponderInventory{{Cluster: q.Service.Cluster, Pods: pods.Items, Services: services.Items, Backends: backends}}
	remoteInventories, remoteRoutes, attributionWarnings := s.meshRemoteResponderInventories(ctx, q)
	inventories = append(inventories, remoteInventories...)
	rules = append(rules, remoteRoutes...)
	warnings = append(warnings, attributionWarnings...)
	verifiedClusters := []string{}
	for _, inventory := range inventories {
		verifiedClusters = append(verifiedClusters, inventory.Cluster)
	}
	completeAttribution := len(inventories) == len(s.WS.Clusters())
	warnings = append(preWarnings, warnings...)
	if !option.Ambient && q.Ephemeral == nil {
		warnings = append(warnings, "The captured client is not confirmed ambient-enrolled. Verify its mesh capture and waypoint path before interpreting this as a mesh routing test.")
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	encode := json.NewEncoder(w)
	emit := func(event any) bool {
		if encode.Encode(event) != nil {
			return false
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return true
	}
	total := q.Count * len(q.Names)
	start := map[string]any{"type": "start", "requested": total, "client": option, "routes": rules, "warnings": warnings, "attributionClusters": verifiedClusters}
	if ephemeral != nil {
		start["ephemeral"] = ephemeral
	}
	if !emit(start) {
		return
	}
	results := []meshProbeResult{}
	execConfig := rest.CopyConfig(cfg)
	execConfig.Timeout = 0
	for _, name := range q.Names {
		for i := 0; i < q.Count; i++ {
			if ctx.Err() != nil {
				break
			}
			result := runMeshCurl(ctx, execConfig, client, q, name)
			row := meshProbeResult{httpProbeResult: result, Index: len(results) + 1}
			row.Responder, row.Attribution = meshResponseAcrossClusters(result, inventories)
			if name != v.DNS.LocalName && !completeAttribution {
				row.Responder = nil
				row.Attribution = "Responder cluster is unverified: not all captured cluster inventories could be read for this global request."
			}
			results = append(results, row)
			if !emit(map[string]any{"type": "result", "result": row, "completed": len(results), "requested": total}) {
				return
			}
		}
	}
	summary := summarizeMeshProbe(results, total)
	summary.Stopped = ctx.Err() != nil
	emit(map[string]any{"type": "complete", "summary": summary})
	if ephemeral != nil {
		deleted, err := cleanup()
		event := map[string]any{"type": "cleanup", "pod": ephemeral["name"], "namespace": ephemeral["namespace"], "deleted": deleted}
		if err != nil {
			event["error"] = err.Error()
		}
		emit(event)
	}
}
