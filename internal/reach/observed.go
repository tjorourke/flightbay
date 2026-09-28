package reach

import (
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// ---------- observed traffic ----------
//
// Observed traffic comes from store.Flows: ztunnel and waypoint access logs,
// agentgateway request logs, CoreDNS query logs and flow logs imported with
// the capture (Hubble, Calico, VPC flow logs). Every source describes the same
// connections in its own way, so this file resolves each flow's two ends to
// the workloads and Services of the capture (or to an external host), and
// keeps what each source saw separately: counts are never added across
// sources, because two sources seeing one request is still one request.

// observed is what was seen going from one workload to one destination.
type observed struct {
	requests  int // the largest count any one source saw
	denied    int
	methods   []string
	paths     []string // up to five examples
	tools     []string // MCP tools
	routes    []string // gateway routes
	sources   []string // which flow sources saw it
	ports     []int
	ip        string // the address, for an external destination known by IP
	lookups   int    // DNS lookups of the name (external hosts)
	perSource map[string]*srcCount
	records   []store.Flow // the flow records behind it, largest first, at most maxEvidence
}

// maxEvidence caps the flow records kept per destination as evidence.
const maxEvidence = 20

// keepEvidence merges flow records, largest counts first, capped.
func keepEvidence(into, from []store.Flow) []store.Flow {
	o := &observed{records: into}
	o.keep(from...)
	return o.records
}

// keep adds a flow record to the evidence, largest counts first.
func (o *observed) keep(fs ...store.Flow) {
	o.records = append(o.records, fs...)
	sort.SliceStable(o.records, func(i, j int) bool { return o.records[i].Count > o.records[j].Count })
	if len(o.records) > maxEvidence {
		o.records = o.records[:maxEvidence]
	}
}

type srcCount struct {
	requests, denied int
	rows             map[string]int // row key -> count, for sources whose rows are separate requests
}

// flowSummary is the observed traffic of one cluster.
type flowSummary struct {
	byW     map[*workload]map[string]*observed // workload -> destination key -> traffic
	covered map[*workload][]string             // workload -> flow sources that cover it
	td      string                             // trust domain, from SPIFFE IDs
	wide    []string                           // cluster-wide sources present (hubble, VPC flow logs...)
}

// Sources that see every pod once they are present at all: a CNI or cloud
// flow log does not depend on the pod being in the mesh.
var clusterWideSources = map[string]bool{"hubble": true, "calico": true, "aws-vpc": true, "gcp-vpc": true, "azure-vnet": true}

// Sources that describe connections or requests. CoreDNS only says a name was
// looked up, which names an address but does not show a connection.
func connectionSource(src string) bool { return src != "coredns" && src != "" }

// Sources whose rows each carry a whole edge's count, so rows are maxed
// rather than summed. store.Flows now counts ztunnel per method, so none do.
var aggregateRows = map[string]bool{}

func deniedVerdict(v string) bool {
	switch strings.ToLower(v) {
	case "denied", "deny", "dropped", "drop", "reject", "rejected", "blocked", "forbidden", "policy_denied", "unauthorized", "401", "403":
		return true
	}
	return false
}

// ipIndex maps addresses in the capture to what owns them.
type ipIndex struct {
	pod  map[string]*workload
	svc  map[string]*service
	node map[string]bool
}

func (c *cluster) ips() *ipIndex {
	if c.ipx != nil {
		return c.ipx
	}
	x := &ipIndex{pod: map[string]*workload{}, svc: map[string]*service{}, node: map[string]bool{}}
	for _, w := range c.works {
		for _, p := range w.pods {
			st := m(p.Obj["status"])
			var addrs []string
			if ip := s(st["podIP"]); ip != "" {
				addrs = append(addrs, ip)
			}
			for _, pi := range l(st["podIPs"]) {
				if ip := s(m(pi)["ip"]); ip != "" {
					addrs = append(addrs, ip)
				}
			}
			if hostNetwork(w) { // the pod's address is the node's
				for _, a := range addrs {
					x.node[a] = true
				}
				continue
			}
			for _, a := range addrs {
				x.pod[a] = w
			}
			if hip := s(st["hostIP"]); hip != "" {
				x.node[hip] = true
			}
		}
	}
	for _, sv := range c.services {
		sp := m(sv.obj.Obj["spec"])
		ips := strList(l(sp["clusterIPs"]))
		if ip := s(sp["clusterIP"]); ip != "" {
			ips = append(ips, ip)
		}
		for _, ip := range ips {
			if ip != "" && ip != "None" {
				x.svc[ip] = sv
			}
		}
	}
	for _, n := range c.list("", "nodes") {
		for _, a := range l(path(n.Obj, "status", "addresses")) {
			if t := s(m(a)["type"]); t == "InternalIP" || t == "ExternalIP" {
				x.node[s(m(a)["address"])] = true
			}
		}
	}
	c.ipx = x
	return x
}

// serviceFor returns the Service that selects a workload, preferring one
// that serves the port.
func (c *cluster) serviceFor(w *workload, port int) *service {
	var first *service
	for _, sv := range c.services {
		if len(sv.backends) == 0 || has(sv.classes, "waypoint") {
			continue
		}
		for _, b := range sv.backends {
			if b != w {
				continue
			}
			if port == 0 {
				return sv
			}
			for _, p := range sv.ports {
				if p.port == port || p.target == strconv.Itoa(port) {
					return sv
				}
			}
			if first == nil {
				first = sv
			}
		}
	}
	return first
}

// resolveSrc finds the workload a flow came from: by pod name, then by pod
// IP, then by a SPIFFE identity whose service account only one workload uses.
func (c *cluster) resolveSrc(f store.Flow) *workload {
	if f.SrcPod != "" {
		ns := f.SrcNamespace
		if w := c.byPod[key(ns, f.SrcPod)]; w != nil {
			return w
		}
		for _, w := range c.works { // some sources name the workload rather than the pod
			if (ns == "" || w.ns == ns) && w.name == f.SrcPod {
				return w
			}
		}
	}
	if f.SrcIP != "" {
		if w := c.ips().pod[stripPort(f.SrcIP)]; w != nil {
			return w
		}
	}
	if ns, sa, ok := spiffeSA(f.SrcIdentity); ok {
		var hit *workload
		for _, w := range c.works {
			if w.ns == ns && w.sa == sa {
				if hit != nil {
					return nil // shared service account: cannot tell which
				}
				hit = w
			}
		}
		return hit
	}
	return nil
}

func spiffeSA(id string) (ns, sa string, ok bool) {
	if !strings.HasPrefix(id, "spiffe://") {
		return "", "", false
	}
	p := strings.Split(strings.TrimPrefix(id, "spiffe://"), "/")
	for i := 0; i+3 < len(p); i++ {
		if p[i] == "ns" && p[i+2] == "sa" {
			return p[i+1], p[i+3], true
		}
	}
	return "", "", false
}

func stripPort(a string) string {
	if h, _, err := net.SplitHostPort(a); err == nil {
		return h
	}
	return strings.Trim(a, "[]")
}

// hostOnly drops a port and a trailing dot from a host.
func hostOnly(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if hh, _, err := net.SplitHostPort(h); err == nil {
		return hh
	}
	return h
}

// dstResult is where a flow went.
type dstResult struct {
	key  string // ns/name of a Service, "ext:<host or ip>", "" when unresolved or not a destination
	ip   string
	node bool
}

func (c *cluster) resolveDst(f store.Flow) dstResult {
	x := c.ips()
	host := hostOnly(f.DstHost)
	ip := stripPort(f.DstIP)
	if net.ParseIP(host) != nil {
		if ip == "" {
			ip = host
		}
		host = ""
	}
	if host != "" {
		if k := svcKeyFromHost(host, f.DstNamespace); k != "" && c.svcByKey[k] != nil {
			return dstResult{key: k}
		}
	}
	if f.DstWorkload != "" && f.DstNamespace != "" {
		if w := c.byPod[key(f.DstNamespace, f.DstWorkload)]; w != nil {
			if sv := c.serviceFor(w, f.DstPort); sv != nil {
				return dstResult{key: key(sv.ns, sv.name)}
			}
		}
	}
	if ip != "" {
		if sv := x.svc[ip]; sv != nil {
			return dstResult{key: key(sv.ns, sv.name)}
		}
		if w := x.pod[ip]; w != nil {
			if sv := c.serviceFor(w, f.DstPort); sv != nil {
				return dstResult{key: key(sv.ns, sv.name)}
			}
			return dstResult{} // a pod with no Service in front: not a destination we model
		}
		if x.node[ip] {
			return dstResult{node: true, ip: ip}
		}
	}
	if host != "" && !clusterLocal(host) {
		return dstResult{key: "ext:" + host, ip: ip}
	}
	if ip != "" && !loopback(ip) {
		return dstResult{key: "ext:" + ip, ip: ip}
	}
	return dstResult{}
}

func clusterLocal(h string) bool {
	return strings.HasSuffix(h, ".cluster.local") || strings.HasSuffix(h, ".svc") || strings.Contains(h, ".svc.") ||
		strings.HasSuffix(h, ".in-addr.arpa") || strings.HasSuffix(h, ".ip6.arpa") || !strings.Contains(h, ".")
}

func loopback(ip string) bool {
	p := net.ParseIP(ip)
	return p == nil || p.IsLoopback() || p.IsUnspecified()
}

// gatewayService finds the agentgateway Service a flow's Via names:
// "ns/name" or "name" of the Gateway or its Service.
func (c *cluster) gatewayService(via, defNS string) *service {
	if via == "" {
		return nil
	}
	ns, name := "", via
	if i := strings.Index(via, "/"); i > 0 {
		ns, name = via[:i], via[i+1:]
	}
	for _, sv := range c.services {
		if sv.agwClass == "" {
			continue
		}
		if (sv.gatewayNm == name || sv.name == name) && (ns == "" || sv.gatewayNS == ns || sv.ns == ns) {
			return sv
		}
	}
	return nil
}

// observe aggregates the flows of one cluster.
func (c *cluster) observe(flows []store.Flow) *flowSummary {
	fs := &flowSummary{byW: map[*workload]map[string]*observed{}, covered: map[*workload][]string{}}
	wide := map[string]bool{}
	type dnsKey struct {
		w  *workload
		ip string
	}
	byAnswer := map[dnsKey]string{} // (querier, answer IP) -> name
	anyAnswer := map[string]string{}
	lookups := map[*workload]map[string]int{}
	get := func(w *workload, k string) *observed {
		if fs.byW[w] == nil {
			fs.byW[w] = map[string]*observed{}
		}
		o := fs.byW[w][k]
		if o == nil {
			o = &observed{perSource: map[string]*srcCount{}}
			fs.byW[w][k] = o
		}
		return o
	}
	record := func(w *workload, k string, f store.Flow, ip string) {
		o := get(w, k)
		// Envoy logs an in-mesh request on both sidecars; count each side on
		// its own so the edge takes the larger rather than the sum.
		srcKey := f.Source
		if f.Direction != "" {
			srcKey += ":" + f.Direction
		}
		sc := o.perSource[srcKey]
		if sc == nil {
			sc = &srcCount{rows: map[string]int{}}
			o.perSource[srcKey] = sc
		}
		n := f.Count
		if n <= 0 {
			n = 1
		}
		den := deniedVerdict(f.Verdict)
		row := strings.Join([]string{f.Method, f.Path, f.Tool, f.Route, strconv.Itoa(f.DstPort), strconv.FormatBool(den), f.SrcPod, f.SrcIP}, "\x00")
		if aggregateRows[f.Source] {
			if n > sc.requests {
				sc.requests = n
			}
			if den && n > sc.denied {
				sc.denied = n
			}
		} else {
			if n > sc.rows[row] { // the same row twice is the same traffic
				if den {
					sc.denied += n - sc.rows[row]
				}
				sc.requests += n - sc.rows[row]
				sc.rows[row] = n
			}
		}
		if f.Method != "" {
			o.methods = union(o.methods, []string{strings.ToUpper(f.Method)})
		}
		if f.Path != "" && len(o.paths) < 5 && !has(o.paths, f.Path) {
			o.paths = append(o.paths, f.Path)
		}
		if f.Tool != "" {
			o.tools = union(o.tools, []string{f.Tool})
		}
		if f.Route != "" {
			o.routes = union(o.routes, []string{f.Route})
		}
		if f.DstPort > 0 && !hasInt(o.ports, f.DstPort) {
			o.ports = append(o.ports, f.DstPort)
		}
		if ip != "" && o.ip == "" {
			o.ip = ip
		}
		o.sources = union(o.sources, []string{f.Source})
		o.keep(f)
	}
	dnsRecs := map[*workload]map[string][]store.Flow{} // querier -> name -> lookups, as evidence
	for _, f := range flows {
		if fs.td == "" && strings.HasPrefix(f.SrcIdentity, "spiffe://") {
			fs.td = strings.SplitN(strings.TrimPrefix(f.SrcIdentity, "spiffe://"), "/", 2)[0]
		}
		if clusterWideSources[f.Source] {
			wide[f.Source] = true
		}
		w := c.resolveSrc(f)
		if w == nil {
			continue
		}
		if f.Source == "coredns" {
			name := hostOnly(f.DstHost)
			if name == "" || clusterLocal(name) || svcKeyFromHost(name, w.ns) != "" && c.svcByKey[svcKeyFromHost(name, w.ns)] != nil {
				continue
			}
			if v := strings.ToLower(f.Verdict); v == "nxdomain" || v == "refused" || v == "servfail" {
				continue
			}
			if lookups[w] == nil {
				lookups[w] = map[string]int{}
			}
			n := f.Count
			if n <= 0 {
				n = 1
			}
			lookups[w][name] += n
			if dnsRecs[w] == nil {
				dnsRecs[w] = map[string][]store.Flow{}
			}
			dnsRecs[w][name] = append(dnsRecs[w][name], f)
			if ip := stripPort(f.DstIP); ip != "" && net.ParseIP(ip) != nil {
				byAnswer[dnsKey{w, ip}] = name
				anyAnswer[ip] = name
			}
			continue
		}
		if connectionSource(f.Source) {
			fs.covered[w] = union(fs.covered[w], []string{f.Source})
		}
		d := c.resolveDst(f)
		if d.key == "" {
			continue
		}
		gw := c.gatewayService(f.Via, w.ns)
		if gw == nil && f.Source == "agentgateway" && !strings.HasPrefix(d.key, "ext:") {
			if sv := c.svcByKey[d.key]; sv != nil && sv.agwClass != "" {
				gw = sv // agent -> gateway, logged by the gateway
			}
		}
		isGW := false
		if gw != nil {
			for _, b := range gw.backends {
				if b == w {
					isGW = true
				}
			}
		}
		gwKey := ""
		if gw != nil {
			gwKey = key(gw.ns, gw.name)
		}
		switch {
		case gw != nil && !isGW && d.key != gwKey:
			// The agent called the gateway, which called the backend: mark
			// both the hop to the gateway and what it reached through it.
			record(w, gwKey, f, "")
			record(w, "via:"+gwKey+"|"+d.key, f, d.ip)
		default:
			record(w, d.key, f, d.ip)
		}
	}
	// Name external addresses from DNS: the answer the querier got, any
	// answer for the address, or, when a workload looked up exactly one
	// name no flow names and called exactly one unnamed address, that pair.
	for w, dsts := range fs.byW {
		var unnamedIPs []string
		for k := range dsts {
			if strings.HasPrefix(k, "ext:") && net.ParseIP(strings.TrimPrefix(k, "ext:")) != nil {
				unnamedIPs = append(unnamedIPs, k)
			}
		}
		sort.Strings(unnamedIPs)
		rename := func(from, host string) {
			o := dsts[from]
			delete(dsts, from)
			to := "ext:" + host
			if prev := dsts[to]; prev != nil {
				mergeObserved(prev, o)
			} else {
				dsts[to] = o
			}
		}
		var left []string
		for _, k := range unnamedIPs {
			ip := strings.TrimPrefix(k, "ext:")
			if n := byAnswer[dnsKey{w, ip}]; n != "" {
				rename(k, n)
			} else if n := anyAnswer[ip]; n != "" {
				rename(k, n)
			} else {
				left = append(left, k)
			}
		}
		var unmatched []string
		for n := range lookups[w] {
			if dsts["ext:"+n] == nil {
				unmatched = append(unmatched, n)
			}
		}
		if len(left) == 1 && len(unmatched) == 1 {
			rename(left[0], unmatched[0])
		}
	}
	for w, names := range lookups {
		for n, cnt := range names {
			o := get(w, "ext:"+n)
			o.lookups += cnt
			o.sources = union(o.sources, []string{"coredns"})
			o.keep(dnsRecs[w][n]...)
		}
	}
	for _, dsts := range fs.byW {
		for _, o := range dsts {
			for _, sc := range o.perSource {
				if sc.requests > o.requests {
					o.requests = sc.requests
				}
				if sc.denied > o.denied {
					o.denied = sc.denied
				}
			}
		}
	}
	fs.wide = sortedKeys(wide)
	return fs
}

func mergeObserved(into, from *observed) {
	for src, sc := range from.perSource {
		if into.perSource[src] == nil {
			into.perSource[src] = sc
			continue
		}
		t := into.perSource[src]
		t.requests += sc.requests
		t.denied += sc.denied
	}
	into.methods = union(into.methods, from.methods)
	for _, p := range from.paths {
		if len(into.paths) < 5 && !has(into.paths, p) {
			into.paths = append(into.paths, p)
		}
	}
	into.tools = union(into.tools, from.tools)
	into.routes = union(into.routes, from.routes)
	into.sources = union(into.sources, from.sources)
	for _, p := range from.ports {
		if !hasInt(into.ports, p) {
			into.ports = append(into.ports, p)
		}
	}
	if into.ip == "" {
		into.ip = from.ip
	}
	into.lookups += from.lookups
	into.keep(from.records...)
}

func hasInt(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// coverage lists the sources that would have logged traffic from w: the
// sources that did, ztunnel when its logs were captured and w is in the
// mesh, and any cluster-wide flow log.
func (c *cluster) coverage(fs *flowSummary, w *workload, ztunnelLogs bool) []string {
	out := append([]string{}, fs.covered[w]...)
	if ztunnelLogs && w.inMesh {
		out = union(out, []string{"ztunnel"})
	}
	out = union(out, fs.wide)
	return out
}
