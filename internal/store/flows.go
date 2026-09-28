package store

import (
	"strconv"
	"strings"
)

// Flow is one observed connection or request, normalised from any source:
// ztunnel, agentgateway, Envoy (waypoints and sidecars), CoreDNS query logs,
// or flow logs imported alongside the bundle (Cilium Hubble, Calico, AWS, GCP
// and Azure VPC flow logs, or a generic CSV/JSON). Endpoints are named when
// the source names them and otherwise carried as IPs; internal/reach resolves
// IPs to workloads from the captured pod, Service and node addresses.
type Flow struct {
	Cluster string `json:"cluster"`
	Source  string `json:"source"` // ztunnel, agentgateway, envoy, coredns, hubble, calico, aws-vpc, gcp-vpc, azure-vnet, generic

	SrcIP        string `json:"srcIp,omitempty"`
	SrcNamespace string `json:"srcNamespace,omitempty"`
	SrcPod       string `json:"srcPod,omitempty"`
	SrcIdentity  string `json:"srcIdentity,omitempty"` // SPIFFE ID or JWT subject when known

	DstIP        string `json:"dstIp,omitempty"`
	DstPort      int    `json:"dstPort,omitempty"`
	DstHost      string `json:"dstHost,omitempty"` // service host, HTTP host, SNI or DNS name
	DstNamespace string `json:"dstNamespace,omitempty"`
	DstWorkload  string `json:"dstWorkload,omitempty"` // pod name when known

	Protocol string `json:"protocol,omitempty"` // tcp, udp, http, grpc, mcp, dns
	Method   string `json:"method,omitempty"`
	Path     string `json:"path,omitempty"`
	Tool     string `json:"tool,omitempty"`  // MCP tool name
	Route    string `json:"route,omitempty"` // gateway route
	Via      string `json:"via,omitempty"`   // gateway or waypoint that carried it
	Verdict  string `json:"verdict,omitempty"`
	Count    int    `json:"count"`
}

// Flows returns every observed flow for a cluster ("" for all), from logs in
// the capture and any flow logs imported with it.
func (w *Workspace) Flows(cluster string) []Flow {
	var out []Flow
	for _, e := range w.AccessEdges(cluster) {
		host := e.DstService
		port := 0
		if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
			if p, err := strconv.Atoi(host[i+1:]); err == nil {
				port = p
				host = host[:i]
			}
		}
		f := Flow{Cluster: e.Cluster, Source: "ztunnel", SrcNamespace: e.SrcNamespace, SrcPod: e.SrcPod, SrcIdentity: e.SrcIdentity,
			DstHost: host, DstPort: port, DstNamespace: e.DstNamespace, DstWorkload: e.DstWorkload, Protocol: "tcp", Count: e.Requests}
		if len(e.Methods) > 0 {
			f.Protocol = "http"
		}
		if e.Denied > 0 {
			f.Verdict = "denied"
		}
		for _, m := range e.Methods {
			g := f
			g.Method = m
			out = append(out, g)
		}
		if len(e.Methods) == 0 {
			out = append(out, f)
		}
	}
	return out
}
