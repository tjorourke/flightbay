package ambient

import (
	"github.com/tjorourke/flightbay/internal/store"
	"sigs.k8s.io/yaml"
	"sort"
	"strings"
)

type ServicePortInfo struct {
	Port        int    `json:"port"`
	Name        string `json:"name"`
	AppProtocol string `json:"appProtocol,omitempty"`
}
type PublishedGRPCService struct {
	Cluster    string `json:"cluster"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	MeshDomain string `json:"meshDomain"`
	Ports      []int  `json:"ports"`
}
type RateLimitQuota struct {
	Source   Ref    `json:"source"`
	Domain   string `json:"domain"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Requests int    `json:"requests"`
	Unit     string `json:"unit"`
}

// Only explicit domains and flat constant descriptors can populate the simple
// HTTP quota chooser. Do not invent a controller domain or flatten nested keys.
func capturedRateLimitQuotas(ws *store.Workspace) []RateLimitQuota {
	out := []RateLimitQuota{}
	add := func(o *store.Object, config map[string]any) {
		domain := S(config["domain"])
		if domain == "" {
			return
		}
		for _, v := range L(config["descriptors"]) {
			d := M(v)
			limit := M(d["rate_limit"])
			if limit == nil {
				limit = M(d["rateLimit"])
			}
			key, value := S(d["key"]), S(d["value"])
			if key == "" || value == "" || len(L(d["descriptors"])) > 0 || len(limit) == 0 || strings.Contains(strings.ToLower(value), "[redacted]") {
				continue
			}
			count := limit["requests_per_unit"]
			if count == nil {
				count = limit["requestsPerUnit"]
			}
			if count == nil {
				continue
			}
			out = append(out, RateLimitQuota{Source: reference(o), Domain: domain, Key: key, Value: value, Requests: N(count), Unit: strings.ToLower(S(limit["unit"]))})
		}
	}
	for _, cluster := range ws.Clusters() {
		for _, o := range ws.List(cluster, "", "configmaps", "", nil) {
			for _, value := range M(o.Obj["data"]) {
				var config map[string]any
				if yaml.Unmarshal([]byte(S(value)), &config) == nil {
					add(o, config)
				}
			}
		}
		for _, o := range ws.List(cluster, "ratelimit.solo.io", "ratelimitconfigs", "", nil) {
			raw := M(Copy(Path(o.Obj, "spec", "raw")))
			if raw == nil {
				continue
			}
			if S(raw["domain"]) == "" {
				raw["domain"] = Path(o.Obj, "spec", "domain")
			}
			add(o, raw)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Source.Cluster+a.Domain+a.Key+a.Value+a.Source.Name < b.Source.Cluster+b.Domain+b.Key+b.Value+b.Source.Name
	})
	return out
}

func capturedPublishedGRPCServices(ws *store.Workspace) []PublishedGRPCService {
	out := []PublishedGRPCService{}
	for _, cluster := range ws.Clusters() {
		for _, entry := range ws.List(cluster, "networking.istio.io", "serviceentries", "", nil) {
			labels := entry.Labels()
			name, ns := labels["solo.io/parent-service"], labels["solo.io/parent-service-namespace"]
			if name == "" || ns == "" {
				continue
			}
			ports := []int{}
			for _, v := range L(Path(entry.Obj, "spec", "ports")) {
				p := M(v)
				if strings.HasPrefix(S(p["protocol"]), "GRPC") {
					ports = append(ports, N(p["number"]))
				}
			}
			if len(ports) == 0 {
				continue
			}
			for _, v := range L(Path(entry.Obj, "spec", "hosts")) {
				host := S(v)
				prefix := name + "." + ns + "."
				if strings.HasPrefix(host, prefix) && !strings.HasPrefix(strings.TrimPrefix(host, prefix), "svc.") {
					out = append(out, PublishedGRPCService{Cluster: cluster, Namespace: ns, Name: name, Host: host, MeshDomain: strings.TrimPrefix(host, prefix), Ports: ports})
				}
			}
		}
	}
	return out
}
