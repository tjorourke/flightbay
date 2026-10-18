package reach

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/store"
)

// Traffic is the ambient traffic map: every connection ztunnel logged
// between workloads and Services, whether it went over HBONE (mutual TLS)
// or in plaintext, which waypoint carried it, and what the captured policy
// says about it. The verdicts come from the same evaluator as Agent reach,
// so the map and the "why was this denied?" explainer agree with it.
type Traffic struct {
	Nodes      []TNode   `json:"nodes"`
	Edges      []TEdge   `json:"edges"`
	Findings   []Finding `json:"findings"`
	Totals     TTotals   `json:"totals"`
	Namespaces []string  `json:"namespaces"`
	Notes      []string  `json:"notes"`
	Clusters   []string  `json:"clusters"`
	Captured   bool      `json:"captured"` // some ztunnel access log was captured
	First      time.Time `json:"first"`
	Last       time.Time `json:"last"`
}

// TTotals summarise the map.
type TTotals struct {
	Edges       int `json:"edges"`
	Connections int `json:"connections"`
	Plaintext   int `json:"plaintext"` // edges with plaintext inside the mesh
	Unpoliced   int `json:"unpoliced"` // edges no policy looks at
	Denied      int `json:"denied"`    // edges with refused connections
	Mismatch    int `json:"mismatch"`  // edges where the logs and the policy disagree
}

// TNode is a workload, a Service or something outside the capture.
type TNode struct {
	ID        string `json:"id"`
	Cluster   string `json:"cluster"`
	Kind      string `json:"kind"` // workload, service, waypoint, external
	Label     string `json:"label"`
	Namespace string `json:"namespace,omitempty"`
	InMesh    bool   `json:"inMesh"`
	Waypoint  string `json:"waypoint,omitempty"` // a Service's waypoint
	AgentType string `json:"agentType,omitempty"`
	Ref       *Ref   `json:"ref,omitempty"`
	// Gone: a workload ztunnel logged by name that is no longer in the
	// capture (deleted since it made the connection).
	Gone bool `json:"gone,omitempty"`
}

// TEdge is traffic from one workload to one Service (or workload, when it
// was addressed directly).
type TEdge struct {
	Runbook          []ambient.RunbookCommand `json:"runbook,omitempty"`
	ExpectedWaypoint string                   `json:"expectedWaypoint,omitempty"`
	Bypass           int                      `json:"bypass,omitempty"`
	ID               string                   `json:"id"`
	Cluster          string                   `json:"cluster"`
	From             string                   `json:"from"`
	To               string                   `json:"to"`
	SrcIdentity      string                   `json:"srcIdentity,omitempty"`
	DstIdentity      string                   `json:"dstIdentity,omitempty"`
	Connections      int                      `json:"connections"` // connections, or log lines when ztunnel logs no connection lines for it
	Requests         int                      `json:"requests"`    // HTTP requests (some ztunnel builds log one line each)
	Denied           int                      `json:"denied"`
	HBONE            int                      `json:"hbone"`
	Plaintext        int                      `json:"plaintext"`
	BytesSent        int64                    `json:"bytesSent,omitempty"`
	BytesRecv        int64                    `json:"bytesRecv,omitempty"`
	Methods          []string                 `json:"methods,omitempty"`
	Paths            []string                 `json:"paths,omitempty"`
	Via              string                   `json:"via,omitempty"` // waypoint
	Verdict          string                   `json:"verdict"`       // what policy says: allowed, denied, unknown
	Layer            string                   `json:"layer,omitempty"`
	Reasons          []string                 `json:"reasons,omitempty"`
	Conditions       []string                 `json:"conditions,omitempty"`
	Policed          bool                     `json:"policed"` // some NetworkPolicy or AuthorizationPolicy looks at it
	TLS              string                   `json:"tls"`     // mtls, plaintext, mixed
	Flags            []string                 `json:"flags"`   // plaintext, plaintext-in, unpoliced, denied, mismatch
	Risk             string                   `json:"risk"`    // red, amber, green, grey
	Why              string                   `json:"why"`
	First            time.Time                `json:"first"`
	Last             time.Time                `json:"last"`
}

const trafficGuide = "https://masterthemesh.com/solo/istio-ambient-authz-kind/"

// EvaluateTraffic builds the ambient traffic map.
func EvaluateTraffic(ws *store.Workspace) Traffic {
	t := Traffic{Nodes: []TNode{}, Edges: []TEdge{}, Findings: []Finding{}, Namespaces: []string{}, Notes: []string{}, Clusters: ws.Clusters()}
	nodes := map[string]*TNode{}
	nss := map[string]bool{}
	for _, cl := range ws.Clusters() {
		c := newCluster(ws, cl)
		e := &engine{c: c, cache: map[string]hop{}, internet: map[*workload]string{}}
		agents := map[*workload]string{}
		for _, w := range c.findAgents() {
			agents[w] = w.agentType
		}
		edges := map[string]*TEdge{}
		skippedLegs := 0
		for _, ae := range ws.AccessEdges(cl) {
			t.Captured = true
			src := c.resolveSrc(store.Flow{SrcNamespace: ae.SrcNamespace, SrcPod: ae.SrcPod, SrcIdentity: ae.SrcIdentity})
			if src != nil && src.gateway && isWaypoint(src) {
				skippedLegs++ // the waypoint's own leg to the destination; the caller's edge carries it
				continue
			}
			var srcID string
			if src != nil {
				srcID = t.workloadNode(nodes, c, src, agents)
			} else {
				srcID = t.externalNode(nodes, cl, ae.SrcNamespace, ae.SrcPod)
			}
			sv, dstW := c.trafficDst(ae)
			var dstID, via string
			switch {
			case sv != nil:
				dstID = t.serviceNode(nodes, c, sv)
				if dstW != nil && (isWaypoint(dstW) || (dstW.ns == first(sv.waypointNS, sv.ns) && dstW.labels["gateway.networking.k8s.io/gateway-name"] == sv.waypoint && sv.waypoint != "")) && !has(sv.classes, "waypoint") {
					via = dstW.ns + "/" + dstW.name
				}
			case dstW != nil:
				dstID = t.workloadNode(nodes, c, dstW, agents)
			default:
				dstID = t.externalNode(nodes, cl, ae.DstNamespace, first(ae.DstService, ae.DstWorkload, "unknown"))
			}
			k := srcID + "->" + dstID
			ed := edges[k]
			if ed == nil {
				ed = &TEdge{ID: k, Cluster: cl, From: srcID, To: dstID, Flags: []string{}}
				edges[k] = ed
				ed.evaluate(c, e, src, sv, dstW)
				if sv != nil {
					ed.Runbook = ambient.Runbook(ws, sv.obj)
					if sv.waypoint != "" {
						ed.ExpectedWaypoint = first(sv.waypointNS, sv.ns) + "/" + sv.waypoint
					}
				} else if dstW != nil {
					ed.Runbook = ambient.Runbook(ws, dstW.obj)
				} else if src != nil {
					ed.Runbook = ambient.Runbook(ws, src.obj)
				} else {
					ed.Runbook = ambient.Runbook(ws, &store.Object{Cluster: cl, Namespace: ae.DstNamespace})
				}
			}
			ed.Via = first(ed.Via, via)
			// A configured binding is not evidence of traversal. A captured
			// application destination, rather than the waypoint, proves a direct leg.
			if ed.ExpectedWaypoint != "" && via == "" && dstW != nil && !isWaypoint(dstW) {
				count := ae.Connections
				if count == 0 {
					count = ae.Requests
				}
				ed.Bypass += count
			}
			ed.SrcIdentity = first(ed.SrcIdentity, ae.SrcIdentity)
			ed.DstIdentity = first(ed.DstIdentity, ae.DstIdentity)
			if ae.Connections > 0 {
				ed.Connections += ae.Connections
			} else {
				ed.Connections += ae.Requests
			}
			ed.Requests += ae.HTTPRequests
			ed.Denied += ae.Denied
			ed.HBONE += ae.HBONE
			ed.Plaintext += ae.Plaintext
			ed.BytesSent += ae.BytesSent
			ed.BytesRecv += ae.BytesRecv
			ed.Methods = union(ed.Methods, ae.Methods)
			if len(ed.Paths) < 5 {
				ed.Paths = union(ed.Paths, ae.Paths)
			}
			if ed.First.IsZero() || (!ae.First.IsZero() && ae.First.Before(ed.First)) {
				ed.First = ae.First
			}
			if ae.Last.After(ed.Last) {
				ed.Last = ae.Last
			}
			if src != nil {
				nss[src.ns] = true
			}
			if sv != nil {
				nss[sv.ns] = true
			}
		}
		if skippedLegs > 0 {
			t.Notes = append(t.Notes, fmt.Sprintf("connections from a waypoint to its destination (%d) are folded into the caller's edge, drawn \"via\" the waypoint", skippedLegs))
		}
		for _, ed := range edges {
			ed.classify(nodes)
			t.Edges = append(t.Edges, *ed)
		}
		t.Notes = append(t.Notes, c.notes...)
	}
	for _, n := range nodes {
		t.Nodes = append(t.Nodes, *n)
	}
	sort.Slice(t.Nodes, func(i, j int) bool { return t.Nodes[i].ID < t.Nodes[j].ID })
	rank := map[string]int{"red": 0, "amber": 1, "green": 2, "grey": 3}
	sort.Slice(t.Edges, func(i, j int) bool {
		a, b := t.Edges[i], t.Edges[j]
		if rank[a.Risk] != rank[b.Risk] {
			return rank[a.Risk] < rank[b.Risk]
		}
		if a.Connections != b.Connections {
			return a.Connections > b.Connections
		}
		return a.ID < b.ID
	})
	for n := range nss {
		t.Namespaces = append(t.Namespaces, n)
	}
	sort.Strings(t.Namespaces)
	for _, ed := range t.Edges {
		t.Totals.Edges++
		t.Totals.Connections += ed.Connections
		if has(ed.Flags, "plaintext") {
			t.Totals.Plaintext++
		}
		if has(ed.Flags, "unpoliced") {
			t.Totals.Unpoliced++
		}
		if ed.Denied > 0 {
			t.Totals.Denied++
		}
		if has(ed.Flags, "mismatch") {
			t.Totals.Mismatch++
		}
		if !ed.First.IsZero() && (t.First.IsZero() || ed.First.Before(t.First)) {
			t.First = ed.First
		}
		if ed.Last.After(t.Last) {
			t.Last = ed.Last
		}
	}
	if !t.Captured {
		t.Notes = append(t.Notes, "no ztunnel access logs in this capture: either the cluster does not run ambient, or ztunnel's logs were not collected")
	}
	t.Findings = trafficFindings(t, nodes)
	return t
}

func isWaypoint(w *workload) bool {
	return w.gateway && (strings.Contains(w.name, "waypoint") || w.labels["gateway.istio.io/managed"] == "istio.io-mesh-controller" ||
		w.labels["istio.io/gateway-name"] != "" && strings.Contains(w.labels["istio.io/gateway-name"], "waypoint"))
}

// trafficDst resolves a logged destination to a Service and the pod behind
// it, when the capture has them.
func (c *cluster) trafficDst(ae store.AccessEdge) (*service, *workload) {
	var dstW *workload
	if ae.DstWorkload != "" {
		dstW = c.byPod[key(ae.DstNamespace, ae.DstWorkload)]
	}
	host := ae.DstService
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if parts := strings.Split(host, "."); len(parts) >= 2 {
		if sv := c.svcByKey[key(parts[1], parts[0])]; sv != nil {
			return sv, dstW
		}
	}
	if dstW != nil {
		if sv := c.serviceFor(dstW, 0); sv != nil && !isWaypoint(dstW) {
			return sv, dstW
		}
	}
	return nil, dstW
}

func (t *Traffic) workloadNode(nodes map[string]*TNode, c *cluster, w *workload, agents map[*workload]string) string {
	id := "w:" + w.id
	if nodes[id] == nil {
		kind := "workload"
		if isWaypoint(w) {
			kind = "waypoint"
		}
		nodes[id] = &TNode{ID: id, Cluster: c.name, Kind: kind, Label: w.ns + "/" + w.name, Namespace: w.ns, InMesh: w.inMesh, AgentType: agents[w], Ref: refOf(w.obj)}
	}
	return id
}

func (t *Traffic) serviceNode(nodes map[string]*TNode, c *cluster, sv *service) string {
	id := "svc:" + sv.id
	if nodes[id] == nil {
		inMesh := len(sv.backends) > 0
		for _, b := range sv.backends {
			inMesh = inMesh && b.inMesh
		}
		if len(sv.backends) == 0 && c.waypointFor(sv) != nil {
			inMesh = true // its waypoint answers for it
		}
		nodes[id] = &TNode{ID: id, Cluster: c.name, Kind: "service", Label: sv.ns + "/" + sv.name, Namespace: sv.ns, InMesh: inMesh, Waypoint: sv.waypoint, Ref: refOf(sv.obj)}
	}
	return id
}

func (t *Traffic) externalNode(nodes map[string]*TNode, cl, ns, name string) string {
	label := name
	if ns != "" && !strings.Contains(name, ".") {
		label = ns + "/" + name
	}
	id := "x:" + cl + "/" + label
	if nodes[id] == nil {
		// A namespaced name that is not a host is an in-cluster workload the
		// capture no longer has.
		nodes[id] = &TNode{ID: id, Cluster: cl, Kind: "external", Label: label, Namespace: ns, Gone: ns != "" && !strings.Contains(name, ".")}
	}
	return id
}

// evaluate asks the reach evaluator what policy says about src -> sv.
func (ed *TEdge) evaluate(c *cluster, e *engine, src *workload, sv *service, dstW *workload) {
	switch {
	case src == nil:
		ed.Verdict, ed.Reasons = Unknown, []string{"the source is not a workload in the capture"}
		return
	case sv == nil:
		ed.Verdict, ed.Reasons = Unknown, []string{"addressed a pod or host directly, not through a captured Service"}
		if dstW != nil {
			ed.Policed = len(c.policiesSelecting(dstW.ns, dstW.labels, false)) > 0
			if dstW.inMesh && c.apKnown {
				zt, _ := c.authzFor(dstW, nil)
				ed.Policed = ed.Policed || len(zt) > 0
			}
		}
		return
	}
	h := e.hop(src, sv)
	ed.Verdict, ed.Layer, ed.Reasons, ed.Conditions = h.verdict, h.layer, h.reasons, h.conds
	for _, st := range h.steps {
		if st.Verdict != "none" && len(st.Policies) > 0 && !strings.HasPrefix(st.Point, "mTLS") {
			ed.Policed = true
		}
	}
	if len(c.policiesSelecting(src.ns, src.labels, true)) > 0 {
		ed.Policed = true
	}
}

// classify sets the TLS state, the flags and the colour of an edge.
func (ed *TEdge) classify(nodes map[string]*TNode) {
	switch {
	case ed.Plaintext > 0 && ed.HBONE > 0:
		ed.TLS = "mixed"
	case ed.Plaintext > 0:
		ed.TLS = "plaintext"
	default:
		ed.TLS = "mtls"
	}
	src, dst := nodes[ed.From], nodes[ed.To]
	srcMesh, dstMesh := src != nil && src.InMesh, dst != nil && dst.InMesh
	var why []string
	if ed.Bypass > 0 {
		ed.Flags = append(ed.Flags, "waypoint-bypass")
		why = append(why, "logged direct workload traffic bypasses configured waypoint "+ed.ExpectedWaypoint)
	}
	if ed.Plaintext > 0 && srcMesh && dstMesh {
		ed.Flags = append(ed.Flags, "plaintext")
		why = append(why, fmt.Sprintf("%d connections in plaintext between two mesh workloads", ed.Plaintext))
	} else if ed.Plaintext > 0 && dstMesh && !srcMesh {
		ed.Flags = append(ed.Flags, "plaintext-in")
		why = append(why, "plaintext from a workload outside the mesh, with no identity for policy to check")
	}
	if !ed.Policed && ed.Verdict != Denied {
		ed.Flags = append(ed.Flags, "unpoliced")
		why = append(why, "no NetworkPolicy or AuthorizationPolicy looks at this traffic")
	}
	if ed.Denied > 0 {
		ed.Flags = append(ed.Flags, "denied")
		why = append(why, fmt.Sprintf("%d refused", ed.Denied))
	}
	// The logs and the policy disagree: refused although policy allows it
	// (L7 rules, a JWT or something the snapshot cannot see), or allowed
	// through although policy denies it (a policy added after the traffic,
	// or one the evaluator reads differently).
	if (ed.Denied > 0 && ed.Denied == ed.Connections && ed.Verdict == Allowed && ed.Layer != "L7") || (ed.Verdict == Denied && ed.Connections > ed.Denied) {
		ed.Flags = append(ed.Flags, "mismatch")
		why = append(why, "the access logs and the captured policy disagree")
	}
	switch {
	case has(ed.Flags, "plaintext"):
		ed.Risk = "red"
	case has(ed.Flags, "plaintext-in") || has(ed.Flags, "unpoliced") || has(ed.Flags, "mismatch") || has(ed.Flags, "waypoint-bypass"):
		ed.Risk = "amber"
	case ed.Verdict == Unknown:
		ed.Risk = "grey"
	default:
		ed.Risk = "green"
	}
	if len(why) == 0 {
		if ed.TLS == "mtls" {
			why = append(why, "mutual TLS, and policy looks at it")
		} else {
			why = append(why, "destination outside the mesh")
		}
	}
	ed.Why = strings.Join(why, "; ")
}

func trafficFindings(t Traffic, nodes map[string]*TNode) []Finding {
	label := func(id string) string {
		if n := nodes[id]; n != nil {
			return n.Label
		}
		return id
	}
	var out []Finding
	collect := func(flag string) []FindingItem {
		var items []FindingItem
		for _, e := range t.Edges {
			if has(e.Flags, flag) {
				items = append(items, FindingItem{Target: label(e.From) + " → " + label(e.To), Access: fmt.Sprintf("%d connections", e.Connections), Note: e.Why})
			}
		}
		return items
	}
	if items := collect("plaintext"); len(items) > 0 {
		out = append(out, Finding{Severity: "critical", Title: "Plaintext between workloads that are both in the mesh",
			Detail: "ztunnel logged these connections without HBONE, so they were not encrypted or authenticated even though both ends are enrolled. Usually a workload opted out (istio.io/dataplane-mode: none), a sidecar and ambient mix, or a port excluded from capture.",
			Items:  items, Advice: []string{"Check both pods' ambient.istio.io/redirection annotation and the namespaces' istio.io/dataplane-mode label.", "Set PeerAuthentication to STRICT once every caller is enrolled."},
			Source: "https://masterthemesh.com/solo/istio-ambient-mtls-kind/", Docs: "https://docs.solo.io/gloo-mesh/latest/istio/ambient/"})
	}
	if items := collect("plaintext-in"); len(items) > 0 {
		out = append(out, Finding{Severity: "high", Title: "Plaintext into the mesh from workloads outside it",
			Detail: "These callers are not enrolled, so they arrive without an identity and PERMISSIVE mode lets them in. Policy cannot tell them apart.",
			Items:  items, Advice: []string{"Enrol the callers in ambient, then set PeerAuthentication STRICT on the destination namespace."},
			Source: "https://masterthemesh.com/solo/istio-ambient-mtls-kind/"})
	}
	if items := collect("unpoliced"); len(items) > 0 {
		out = append(out, Finding{Severity: "high", Title: "Traffic that no policy looks at",
			Detail: "No NetworkPolicy or AuthorizationPolicy applies to these connections, so anything that can reach the source can use them. Start with a namespace default-deny and allow what the map shows.",
			Items:  items, Fix: defaultDenyYAML(t), Source: trafficGuide, Docs: "https://istio.io/latest/docs/ambient/usage/l4-policy/"})
	}
	if items := collect("mismatch"); len(items) > 0 {
		out = append(out, Finding{Severity: "medium", Title: "The access logs and the captured policy disagree",
			Detail: "Refused although policy allows it, or allowed although policy denies it. Open the edge and use Why? to see each check.",
			Items:  items, Source: trafficGuide})
	}
	return out
}

func defaultDenyYAML(t Traffic) string {
	seen := map[string]bool{}
	var b strings.Builder
	for _, e := range t.Edges {
		if !has(e.Flags, "unpoliced") {
			continue
		}
		ns := ""
		if i := strings.Index(e.To, ":"); i >= 0 {
			parts := strings.Split(e.To[i+1:], "/")
			if len(parts) >= 3 {
				ns = parts[1]
			}
		}
		if ns == "" || seen[ns] || ns == "kube-system" {
			continue
		}
		seen[ns] = true
		fmt.Fprintf(&b, "# Deny everything in %s that no ALLOW policy admits. Add ALLOW policies for the edges\n# the map shows before applying, or the traffic above stops.\napiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata:\n  name: default-deny\n  namespace: %s\nspec: {}\n---\n", ns, ns)
	}
	return strings.TrimSuffix(b.String(), "---\n")
}
