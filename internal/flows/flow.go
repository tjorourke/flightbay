// Package flows turns observed traffic from any source into one normalised
// record: agentgateway and Envoy access logs, CoreDNS query logs, and flow
// logs exported from a CNI or a cloud network (Cilium Hubble, Calico, AWS,
// GCP and Azure VPC flow logs, or a generic CSV / JSON Lines file).
//
// Everything here is a pure function over text, so each format is tested
// with fixture lines taken from the upstream projects. The workspace (package
// store) decides which log lines to feed in and keeps the results in DuckDB.
package flows

import (
	"net"
	"sort"
	"strconv"
	"strings"
)

// Flow is one observed connection or request, aggregated: Count is the number
// of lines, connections or flow records it stands for. Endpoints are named
// when the source names them and otherwise carried as IPs.
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

	Protocol string `json:"protocol,omitempty"` // tcp, udp, icmp, http, grpc, mcp, a2a, llm, dns
	Method   string `json:"method,omitempty"`   // HTTP method, MCP/A2A method, or DNS query type
	Path     string `json:"path,omitempty"`     // without the query string
	Tool     string `json:"tool,omitempty"`     // MCP tool name
	Route    string `json:"route,omitempty"`    // gateway route
	Via      string `json:"via,omitempty"`      // gateway or waypoint that carried it
	Verdict  string `json:"verdict,omitempty"`  // "denied" when refused; flow logs also say "allowed"
	Count    int    `json:"count"`

	// Direction is "inbound" or "outbound" when the source logs one side of a
	// connection (Envoy sidecars log both ends of an in-mesh request).
	Direction string `json:"direction,omitempty"`
	// File is the flow log file a record came from ("" for capture logs).
	File string `json:"file,omitempty"`
	// Origin says where a record came from: "logs" (derived from captured
	// logs), "bundle" (a flow file attached at collection, covered by the
	// manifest) or "reviewer" (added when the bundle was opened; not signed).
	Origin string `json:"origin,omitempty"`
}

// Sources and formats.
const (
	SrcZtunnel      = "ztunnel"
	SrcAgentgateway = "agentgateway"
	SrcEnvoy        = "envoy"
	SrcCoreDNS      = "coredns"
	FmtHubble       = "hubble"
	FmtCalico       = "calico"
	FmtAWS          = "aws-vpc"
	FmtGCP          = "gcp-vpc"
	FmtAzure        = "azure-vnet"
	FmtGeneric      = "generic"

	Denied  = "denied"
	Allowed = "allowed"
)

// Formats lists the flow log formats ParseFile understands.
var Formats = []string{FmtHubble, FmtCalico, FmtAWS, FmtGCP, FmtAzure, FmtGeneric}

// key identifies a flow for aggregation: every field except Count.
func (f Flow) key() Flow { f.Count = 0; return f }

// Aggregate merges flows that differ only in Count, keeping first-seen order.
func Aggregate(in []Flow) []Flow {
	idx := map[Flow]int{}
	var out []Flow
	for _, f := range in {
		if f.Count <= 0 {
			f.Count = 1
		}
		k := f.key()
		if i, ok := idx[k]; ok {
			out[i].Count += f.Count
			continue
		}
		idx[k] = len(out)
		out = append(out, f)
	}
	return out
}

// Sort orders flows by count, then source and destination, for stable output.
func Sort(fs []Flow) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.SrcName() != b.SrcName() {
			return a.SrcName() < b.SrcName()
		}
		return a.DstName() < b.DstName()
	})
}

// SrcName is the best label for the source: namespace/pod, identity or IP.
func (f Flow) SrcName() string {
	switch {
	case f.SrcPod != "" && f.SrcNamespace != "":
		return f.SrcNamespace + "/" + f.SrcPod
	case f.SrcPod != "":
		return f.SrcPod
	case f.SrcIdentity != "":
		return f.SrcIdentity
	case f.SrcIP != "":
		return f.SrcIP
	case f.SrcNamespace != "":
		return f.SrcNamespace + "/*"
	}
	return "?"
}

// DstName is the best label for the destination, with the port when known.
func (f Flow) DstName() string {
	n := ""
	switch {
	case f.DstHost != "":
		n = f.DstHost
	case f.DstWorkload != "" && f.DstNamespace != "":
		n = f.DstNamespace + "/" + f.DstWorkload
	case f.DstIP != "":
		n = f.DstIP
	case f.DstNamespace != "":
		n = f.DstNamespace + "/*"
	default:
		n = "?"
	}
	if f.DstPort > 0 {
		n += ":" + strconv.Itoa(f.DstPort)
	}
	return n
}

// SplitHostPort splits "1.2.3.4:80", "[::1]:80", "host:80" or a bare host.
func SplitHostPort(s string) (string, int) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return "", 0
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, _ := strconv.Atoi(p)
		return h, n
	}
	return strings.Trim(s, "[]"), 0
}

// isIP reports whether s parses as an IP address.
func isIP(s string) bool { return net.ParseIP(s) != nil }

// StripQuery removes the query string and fragment from a path or URL path.
func StripQuery(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		return p[:i]
	}
	return p
}

// ProtoName maps IANA protocol numbers and one-letter codes to names.
func ProtoName(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "6", "tcp", "t":
		return "tcp"
	case "17", "udp", "u":
		return "udp"
	case "1", "icmp", "58", "icmpv6", "icmp6":
		return "icmp"
	case "132", "sctp":
		return "sctp"
	case "", "-":
		return ""
	}
	return strings.ToLower(p)
}

// ServiceNamespace returns the namespace of a Kubernetes Service host
// (name.ns.svc.cluster.local or name.ns.svc), or "".
func ServiceNamespace(host string) string {
	parts := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(parts) >= 3 && parts[2] == "svc" {
		return parts[1]
	}
	return ""
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

// verdictOf normalises allow/deny words used by the flow log formats.
func verdictOf(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "deny", "denied", "drop", "dropped", "reject", "rejected", "d", "block", "blocked", "error":
		return Denied
	case "allow", "allowed", "accept", "accepted", "forwarded", "a", "pass", "permit", "redirected", "translated":
		return Allowed
	case "":
		return ""
	}
	return strings.ToLower(s)
}
