package flows

import (
	"net"
	"regexp"
	"strings"
)

// The CoreDNS log plugin's default format (plugin/log, DefaultFormat):
//
//	{remote}:{port} - {>id} "{type} {class} {name} {proto} {size} {>do} {>bufsize}" {rcode} {>rflags} {rsize} {duration}
//
// written with a "[INFO] " prefix, for example
//
//	[INFO] 10.244.1.5:41234 - 4242 "A IN api.github.com. udp 32 false 512" NOERROR qr,rd,ra 110 0.0003s
var corednsRe = regexp.MustCompile(`^(?:\[\w+\]\s+)?(\S+)\s+-\s+\S+\s+"(\S+)\s+(\S+)\s+(\S+)\s+(\S+)[^"]*"\s+(\S+)`)

// ClusterDomains are suffixes CoreDNS answers from the cluster itself, or
// that come from resolv.conf search-path expansion (the pod tries each search
// domain before the bare name). None of them is an external lookup.
var ClusterDomains = []string{
	"cluster.local", "svc", "in-addr.arpa", "ip6.arpa",
	"internal",           // GCE (c.<project>.internal, google.internal) and AWS (compute.internal, ec2.internal)
	"localdomain", "lan", // host search domains leaking into pods
	// Bare service.namespace lookups (the resolver tries them before the
	// search domains) for namespaces every cluster has. The workspace adds the
	// captured namespace names.
	"default", "kube-system", "kube-public", "kube-node-lease",
}

// InClusterName reports whether a DNS name is in-cluster, a reverse lookup or
// search-path noise rather than a real external hostname.
func InClusterName(name string, extra ...string) bool {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	if n == "" || !strings.Contains(n, ".") {
		return true
	}
	if tld := n[strings.LastIndex(n, ".")+1:]; strings.Trim(tld, "0123456789") == "" {
		return true // CoreDNS loop plugin probes (random digits) and IP-like names
	}
	for _, d := range append(ClusterDomains, extra...) {
		d = strings.Trim(strings.ToLower(d), ".")
		if d != "" && (n == d || strings.HasSuffix(n, "."+d)) {
			return true
		}
	}
	return false
}

// ParseCoreDNS converts one CoreDNS query log line into a flow for an
// external name. In-cluster names and search-path noise return ok=false.
func ParseCoreDNS(line string, extraDomains ...string) (Flow, bool) {
	m := corednsRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Flow{}, false
	}
	name := strings.TrimSuffix(m[4], ".")
	if InClusterName(name, extraDomains...) {
		return Flow{}, false
	}
	ip, _ := SplitHostPort(m[1])
	if !isIP(ip) || net.ParseIP(ip).IsLoopback() || m[2] == "HINFO" {
		return Flow{}, false // CoreDNS's own loop-detection query
	}
	f := Flow{Source: SrcCoreDNS, Protocol: "dns", SrcIP: ip, DstHost: strings.ToLower(name), Method: m[2], Count: 1}
	if m[6] == "REFUSED" {
		f.Verdict = Denied
	}
	return f, true
}
