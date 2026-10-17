package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/policy"
	"golang.org/x/net/http/httpguts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type httpProfile struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Timeout int               `json:"timeout"`
}
type httpProbeRequest struct {
	Service        ambient.Ref  `json:"service"`
	Context        string       `json:"context"`
	Mode           string       `json:"mode"`
	URL            string       `json:"url"`
	Names          []string     `json:"names"`
	Port           int          `json:"port"`
	GatewayService *ambient.Ref `json:"gatewayService,omitempty"`
	GatewayPort    int          `json:"gatewayPort,omitempty"`
	Profile        httpProfile  `json:"profile"`
}
type httpProbeResult struct {
	ID              string            `json:"id"`
	Mode            string            `json:"mode"`
	Name            string            `json:"name"`
	Authority       string            `json:"authority"`
	Connection      string            `json:"connection"`
	Status          int               `json:"status"`
	DurationMS      int64             `json:"durationMs"`
	Started         time.Time         `json:"started"`
	Finished        time.Time         `json:"finished"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	Body            string            `json:"body"`
	Truncated       bool              `json:"truncated"`
	Error           string            `json:"error,omitempty"`
	Pod             *ambient.Ref      `json:"pod,omitempty"`
	Note            string            `json:"note"`
}

var httpProfilesMu sync.Mutex

func validateHTTPProfile(p *httpProfile) error {
	p.Method = strings.ToUpper(p.Method)
	if p.Method == "" {
		p.Method = "GET"
	}
	allowedMethod := false
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		allowedMethod = allowedMethod || method == p.Method
	}
	if !allowedMethod {
		return fmt.Errorf("choose a supported HTTP method")
	}
	if p.Path == "" {
		p.Path = "/"
	}
	u, err := url.ParseRequestURI(p.Path)
	if err != nil || u.IsAbs() || !strings.HasPrefix(p.Path, "/") || strings.HasPrefix(p.Path, "//") || len(p.Path) > 4096 {
		return fmt.Errorf("use a relative request path beginning with /")
	}
	if len(p.Body) > 32768 || len(p.Headers) > 64 || len(p.Name) > 80 {
		return fmt.Errorf("profile is too large (80-character name, 64 headers and 32 KiB body maximum)")
	}
	if p.Timeout == 0 {
		p.Timeout = 10
	}
	if p.Timeout < 1 || p.Timeout > 30 {
		return fmt.Errorf("timeout must be 1–30 seconds")
	}
	seenHeaders := map[string]bool{}
	for k, v := range p.Headers {
		if seenHeaders[strings.ToLower(k)] {
			return fmt.Errorf("duplicate header name %q", k)
		}
		seenHeaders[strings.ToLower(k)] = true
		if !httpguts.ValidHeaderFieldName(k) || !httpguts.ValidHeaderFieldValue(v) || len(v) > 8192 {
			return fmt.Errorf("invalid HTTP header %q", k)
		}
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection", "upgrade", "proxy-connection", "x-flightbay-test-id":
			return fmt.Errorf("%s is managed by the HTTP client", k)
		}
	}
	return nil
}
func probePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		jerr(w, 405, "POST only")
		return false
	}
	if !sameOrigin(r) {
		jerr(w, 403, "cross-origin request refused")
		return false
	}
	return true
}
func (s *Server) httpProfiles(w http.ResponseWriter, r *http.Request) {
	httpProfilesMu.Lock()
	defer httpProfilesMu.Unlock()
	dir, err := s.ambientDir()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	dir = filepath.Join(dir, "http-profiles")
	if r.Method == "POST" {
		if !probePost(w, r) {
			return
		}
		var p httpProfile
		if json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&p) != nil {
			jerr(w, 400, "invalid profile")
			return
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			jerr(w, 400, "name the profile")
			return
		}
		if err := validateHTTPProfile(&p); err != nil {
			jerr(w, 400, err.Error())
			return
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		sum := sha256.Sum256([]byte(p.Name))
		path := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
		data, _ := json.Marshal(p)
		f, err := os.CreateTemp(dir, "profile-")
		if err != nil {
			jerr(w, 500, err.Error())
			return
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		_, err = f.Write(data)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			jerr(w, 500, err.Error())
			return
		}
	} else if r.Method != "GET" {
		jerr(w, 405, "GET or POST only")
		return
	}
	profiles := []httpProfile{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, file := range files {
		data, e := os.ReadFile(file)
		if e != nil {
			continue
		}
		var p httpProfile
		if json.Unmarshal(data, &p) == nil {
			profiles = append(profiles, p)
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	writeJSON(w, 200, profiles)
}

func sensitiveHTTPHeader(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "authorization") || strings.Contains(n, "cookie") || strings.Contains(n, "token") || strings.Contains(n, "secret") || strings.Contains(n, "key")
}
func probeRedact(value string, p httpProfile) string {
	for name, secret := range p.Headers {
		if sensitiveHTTPHeader(name) && secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func runHTTPProbe(ctx context.Context, p httpProfile, address, authority, mode string) httpProbeResult {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	result := httpProbeResult{ID: hex.EncodeToString(buf), Mode: mode, Authority: authority, Started: time.Now().UTC(), ResponseHeaders: map[string]string{}}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, p.Method, address, strings.NewReader(p.Body))
	if err != nil {
		result.Error = "invalid request address"
		return result
	}
	req.Host = authority
	for name, value := range p.Headers {
		if strings.EqualFold(name, "Host") {
			req.Host = value
		} else {
			req.Header.Set(name, value)
		}
	}
	result.Authority = req.Host
	req.Header.Set("X-Flightbay-Test-ID", result.ID)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		result.Error = probeRedact(err.Error(), p)
	} else {
		defer response.Body.Close()
		result.Status = response.StatusCode
		for name, values := range response.Header {
			value := strings.Join(values, ", ")
			if sensitiveHTTPHeader(name) {
				value = "[redacted]"
			}
			result.ResponseHeaders[name] = probeRedact(value, p)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 32769))
		result.Truncated = len(body) > 32768
		if result.Truncated {
			body = body[:32768]
		}
		result.Body = probeRedact(string(body), p)
		if readErr != nil {
			result.Error = probeRedact(readErr.Error(), p)
		}
	}
	result.Finished = time.Now().UTC()
	result.DurationMS = result.Finished.Sub(result.Started).Milliseconds()
	return result
}

func (s *Server) probeForward(ctx context.Context, contextName string, ref ambient.Ref, port int) (string, *ambient.Ref, func(), error) {
	if contextName == "" {
		return "", nil, nil, fmt.Errorf("select a real kubeconfig context")
	}
	p, _ := policy.Profile("minimal")
	cfg, err := collect.RestConfig(s.AmbientKubeconfig, contextName, p, "flightbay-http-test")
	if err != nil {
		return "", nil, nil, err
	}
	cfg.Timeout = 20 * time.Second
	guard := &collect.ReadOnlyGuard{AllowPortForward: true}
	guard.Wrap(cfg)
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", nil, nil, err
	}
	captured := s.WS.Get(ref.Cluster, "", "services", ref.Namespace, ref.Name)
	if captured == nil {
		return "", nil, nil, fmt.Errorf("Service was not captured")
	}
	svc, err := client.CoreV1().Services(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return "", nil, nil, err
	}
	if string(svc.UID) != ambient.S(ambient.Path(captured.Obj, "metadata", "uid")) || svc.UID == "" {
		return "", nil, nil, fmt.Errorf("Service UID does not match this capture/context; refresh inventory")
	}
	if len(svc.Spec.Selector) == 0 {
		return "", nil, nil, fmt.Errorf("Service has no Pod selector")
	}
	labels := []string{}
	for k, v := range svc.Spec.Selector {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	pods, err := client.CoreV1().Pods(ref.Namespace).List(ctx, metav1.ListOptions{LabelSelector: strings.Join(labels, ",")})
	if err != nil {
		return "", nil, nil, err
	}
	for _, pod := range pods.Items {
		ready := false
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
		}
		if !ready || pod.DeletionTimestamp != nil {
			continue
		}
		remote := 0
		for _, sp := range svc.Spec.Ports {
			if int(sp.Port) != port || sp.Protocol != "TCP" {
				continue
			}
			remote = sp.TargetPort.IntValue()
			if sp.TargetPort.StrVal != "" {
				for _, ct := range pod.Spec.Containers {
					for _, cp := range ct.Ports {
						if cp.Name == sp.TargetPort.StrVal {
							remote = int(cp.ContainerPort)
						}
					}
				}
			} else if remote == 0 {
				remote = int(sp.Port)
			}
		}
		if remote < 1 {
			return "", nil, nil, fmt.Errorf("choose a captured TCP Service port with a resolvable targetPort")
		}
		clients, err := collect.NewClients(cfg)
		if err != nil {
			return "", nil, nil, err
		}
		session, err := clients.Forward.Open(ctx, ref.Namespace, pod.Name, []int{remote})
		if err != nil {
			return "", nil, nil, err
		}
		local, ok := session.(interface{ LocalPort(int) int })
		if !ok {
			session.Close()
			return "", nil, nil, fmt.Errorf("port-forward address unavailable")
		}
		podRef := &ambient.Ref{Cluster: ref.Cluster, Kind: "Pod", Resource: "pods", Namespace: ref.Namespace, Name: pod.Name}
		return "http://127.0.0.1:" + strconv.Itoa(local.LocalPort(remote)), podRef, session.Close, nil
	}
	return "", nil, nil, fmt.Errorf("no Ready existing Pod matches this Service")
}

func (s *Server) httpProbeOptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := ambient.NewView(s.WS, q.Get("cluster"), "Service", q.Get("namespace"), q.Get("name"), "")
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	gateways := []map[string]any{}
	for _, gw := range s.WS.List(q.Get("cluster"), ambient.GatewayGroup, "gateways", "", nil) {
		class := ambient.S(ambient.Path(gw.Obj, "spec", "gatewayClassName"))
		if strings.Contains(class, "waypoint") || class == "istio-eastwest" || class == "istio-remote" {
			continue
		}
		for _, svc := range s.WS.List(gw.Cluster, "", "services", gw.Namespace, nil) {
			if svc.Labels()["gateway.networking.k8s.io/gateway-name"] != gw.Name {
				continue
			}
			for _, listener := range ambient.L(ambient.Path(gw.Obj, "spec", "listeners")) {
				l := ambient.M(listener)
				if ambient.S(l["protocol"]) != "HTTP" {
					continue
				}
				ref := ambient.Ref{Cluster: svc.Cluster, Resource: "services", Kind: "Service", Namespace: svc.Namespace, Name: svc.Name}
				gateways = append(gateways, map[string]any{"service": ref, "port": ambient.N(l["port"]), "label": gw.Namespace + "/" + gw.Name + " · HTTP " + strconv.Itoa(ambient.N(l["port"]))})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"dns": v.DNS, "gateways": gateways})
}

func (s *Server) httpProbe(w http.ResponseWriter, r *http.Request) {
	if !probePost(w, r) {
		return
	}
	var req httpProbeRequest
	if json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&req) != nil {
		jerr(w, 400, "invalid HTTP test request")
		return
	}
	if err := validateHTTPProfile(&req.Profile); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	v, err := ambient.NewView(s.WS, req.Service.Cluster, "Service", req.Service.Namespace, req.Service.Name, "")
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	base := ""
	var pod *ambient.Ref
	if req.Mode == "public" {
		u, e := url.Parse(req.URL)
		if e != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
			jerr(w, 400, "enter an http:// or https:// origin without credentials; put path and query in the profile")
			return
		}
		req.Names = []string{u.Host}
		base = u.Scheme + "://" + u.Host
	} else if req.Mode == "service" || req.Mode == "gateway" {
		allowed := append([]string{v.DNS.LocalName}, v.DNS.GlobalNames...)
		if len(req.Names) == 0 || len(req.Names) > 8 {
			jerr(w, 400, "choose 1–8 captured DNS names")
			return
		}
		for _, name := range req.Names {
			found := false
			for _, candidate := range allowed {
				if name == candidate && name != "" {
					found = true
				}
			}
			if !found {
				jerr(w, 400, "a selected DNS name is not in this capture")
				return
			}
		}
		ref, port := req.Service, req.Port
		if req.Mode == "gateway" {
			if req.GatewayService == nil || req.GatewayService.Cluster != req.Service.Cluster {
				jerr(w, 400, "choose a captured HTTP ingress gateway in this cluster")
				return
			}
			ref, port = *req.GatewayService, req.GatewayPort
			svc := s.WS.Get(ref.Cluster, "", "services", ref.Namespace, ref.Name)
			if svc == nil || svc.Labels()["gateway.networking.k8s.io/gateway-name"] == "" {
				jerr(w, 400, "not a captured gateway Service")
				return
			}
			gw := s.WS.Get(ref.Cluster, ambient.GatewayGroup, "gateways", ref.Namespace, svc.Labels()["gateway.networking.k8s.io/gateway-name"])
			httpPort := false
			if gw != nil {
				for _, value := range ambient.L(ambient.Path(gw.Obj, "spec", "listeners")) {
					l := ambient.M(value)
					if ambient.S(l["protocol"]) == "HTTP" && ambient.N(l["port"]) == port {
						httpPort = true
					}
				}
			}
			if !httpPort {
				jerr(w, 400, "choose an HTTP listener, not HBONE or TLS transport")
				return
			}
		}
		var closeForward func()
		base, pod, closeForward, err = s.probeForward(ctx, req.Context, ref, port)
		if err != nil {
			jerr(w, 400, err.Error())
			return
		}
		defer closeForward()
	} else {
		jerr(w, 400, "choose public, service or gateway connection mode")
		return
	}
	results := []httpProbeResult{}
	for _, name := range req.Names {
		authority := name
		if req.Mode == "service" {
			authority = net.JoinHostPort(name, strconv.Itoa(req.Port))
		}
		result := runHTTPProbe(ctx, req.Profile, base+req.Profile.Path, authority, req.Mode)
		result.Name = name
		result.Connection = base
		result.Pod = pod
		result.Note = "Direct request from the Flightbay host; no in-cluster caller identity is assumed."
		if req.Mode == "service" {
			result.Note = "Direct backend via port-forward to an existing Pod. DNS resolution and the normal Service/waypoint/east-west path are bypassed; DNS names are HTTP authorities only."
		}
		if req.Mode == "gateway" {
			result.Note = "Gateway route test via port-forward. DNS resolution is bypassed. Only an existing matching gateway route to the selected global Service can exercise east-west routing; an HTTP success alone does not prove that path."
			if !s.probeRouteCaptured(*req.GatewayService, req.Service, name) {
				result.Note += " No captured HTTPRoute on this gateway references the selected Service or this hostname; the response may be from another backend."
			}
		}
		results = append(results, result)
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func (s *Server) probeRouteCaptured(gatewayService, target ambient.Ref, hostname string) bool {
	svc := s.WS.Get(gatewayService.Cluster, "", "services", gatewayService.Namespace, gatewayService.Name)
	if svc == nil {
		return false
	}
	gatewayName := svc.Labels()["gateway.networking.k8s.io/gateway-name"]
	for _, route := range s.WS.List(target.Cluster, ambient.GatewayGroup, "httproutes", "", nil) {
		parent := false
		for _, value := range ambient.L(ambient.Path(route.Obj, "spec", "parentRefs")) {
			p := ambient.M(value)
			ns := ambient.S(p["namespace"])
			if ns == "" {
				ns = route.Namespace
			}
			kind := ambient.S(p["kind"])
			if (kind == "" || kind == "Gateway") && ns == gatewayService.Namespace && ambient.S(p["name"]) == gatewayName {
				parent = true
			}
		}
		if !parent {
			continue
		}
		hosts := ambient.L(ambient.Path(route.Obj, "spec", "hostnames"))
		matchedHost := len(hosts) == 0
		for _, value := range hosts {
			h := ambient.S(value)
			if h == hostname || (strings.HasPrefix(h, "*.") && strings.HasSuffix(hostname, h[1:])) {
				matchedHost = true
			}
		}
		if !matchedHost {
			continue
		}
		for _, rule := range ambient.L(ambient.Path(route.Obj, "spec", "rules")) {
			for _, value := range ambient.L(ambient.M(rule)["backendRefs"]) {
				b := ambient.M(value)
				kind := ambient.S(b["kind"])
				ns := ambient.S(b["namespace"])
				if ns == "" {
					ns = route.Namespace
				}
				if (kind == "" || kind == "Service") && ambient.S(b["group"]) == "" && ns == target.Namespace && ambient.S(b["name"]) == target.Name {
					return true
				}
				if kind == "Hostname" && ambient.S(b["name"]) == hostname {
					return true
				}
			}
		}
	}
	return false
}
