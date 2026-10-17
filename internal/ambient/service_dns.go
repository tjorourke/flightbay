package ambient

import (
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	"k8s.io/apimachinery/pkg/util/validation"
)

type ServiceDNS struct {
	LocalName      string   `json:"localName,omitempty"`
	DomainSource   string   `json:"domainSource"`
	DomainObserved bool     `json:"domainObserved"`
	GlobalNames    []string `json:"globalNames"`
	GlobalEvidence []Ref    `json:"globalEvidence,omitempty"`
	ExternalName   string   `json:"externalName,omitempty"`
}

func clusterDNSDomain(ws *store.Workspace, c string) (string, string, bool) {
	domains := map[string]bool{}
	for _, pod := range ws.List(c, "", "pods", "istio-system", nil) {
		if pod.Labels()["app"] != "istiod" {
			continue
		}
		for _, container := range L(Path(pod.Obj, "spec", "containers")) {
			args := L(M(container)["args"])
			for i, arg := range args {
				value := ""
				if S(arg) == "--domain" && i+1 < len(args) {
					value = S(args[i+1])
				} else if strings.HasPrefix(S(arg), "--domain=") {
					value = strings.TrimPrefix(S(arg), "--domain=")
				}
				if value != "" && len(validation.IsDNS1123Subdomain(value)) == 0 {
					domains[value] = true
				}
			}
		}
	}
	if len(domains) == 1 {
		for domain := range domains {
			return domain, "Captured istiod --domain", true
		}
	}
	if len(domains) > 1 {
		return "", "Conflicting cluster DNS domains in captured istiod Pods", false
	}
	if config := ws.Get(c, "", "configmaps", "kube-system", "coredns"); config != nil {
		for _, line := range strings.Split(S(Path(config.Obj, "data", "Corefile")), "\n") {
			line, _, _ = strings.Cut(line, "#")
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "kubernetes" {
				continue
			}
			for _, zone := range fields[1:] {
				if zone == "{" {
					break
				}
				zone = strings.TrimSuffix(zone, ".")
				if zone == "in-addr.arpa" || zone == "ip6.arpa" {
					continue
				}
				if len(validation.IsDNS1123Subdomain(zone)) == 0 {
					domains[zone] = true
				}
			}
		}
		if len(domains) == 1 {
			for domain := range domains {
				return domain, "Captured CoreDNS kubernetes zone", true
			}
		}
		if len(domains) > 1 {
			return "", "Multiple Kubernetes DNS zones captured; no single suffix assumed", false
		}
	}
	return "cluster.local", "Kubernetes default suffix; not confirmed by this capture", false
}

func serviceDNS(ws *store.Workspace, svc *store.Object) ServiceDNS {
	domain, source, observed := clusterDNSDomain(ws, svc.Cluster)
	d := ServiceDNS{DomainSource: source, DomainObserved: observed, GlobalNames: []string{}, ExternalName: S(Path(svc.Obj, "spec", "externalName"))}
	if domain != "" {
		d.LocalName = svc.Name + "." + svc.Namespace + ".svc." + domain
	}
	for _, entry := range ws.List(svc.Cluster, "networking.istio.io", "serviceentries", "", nil) {
		labels := entry.Labels()
		if labels["solo.io/parent-service"] != svc.Name || labels["solo.io/parent-service-namespace"] != svc.Namespace || labels["solo.io/service-scope"] == "" {
			continue
		}
		for _, host := range L(Path(entry.Obj, "spec", "hosts")) {
			name := S(host)
			if name != "" && !contains(d.GlobalNames, name) {
				d.GlobalNames = append(d.GlobalNames, name)
			}
		}
		d.GlobalEvidence = append(d.GlobalEvidence, reference(entry))
	}
	sort.Strings(d.GlobalNames)
	return d
}
