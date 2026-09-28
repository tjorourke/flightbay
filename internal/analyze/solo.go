package analyze

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/solo"
	"github.com/tjorourke/flightbay/internal/store"
)

// snapshotTime is when the capture describes; certificate lifetimes are
// measured against it, not against the time the bundle is opened.
func snapshotTime(ws *store.Workspace) time.Time {
	if ws.IsLive() {
		return time.Now().UTC()
	}
	if m := ws.Manifest(); m != nil && !m.CaptureEnd.IsZero() {
		return m.CaptureEnd
	}
	if h := ws.Header(); h != nil && !h.CaptureStart.IsZero() {
		return h.CaptureStart
	}
	return time.Now().UTC()
}

func diagRef(d store.Diagnostic) *Ref {
	return &Ref{Cluster: d.Cluster, Resource: "pods", Kind: "Pod", Namespace: d.Namespace, Name: d.Pod}
}

// certificates flags expired certificates in any proxy, roots expiring within
// 30 days, and ztunnel certificates that are not available. Workload
// certificates are short-lived (24h by default) and rotate continuously, so a
// few hours left is normal and is not reported.
func certificates(ws *store.Workspace, cluster string) []Finding {
	ref := snapshotTime(ws)
	var out []Finding
	type agg struct {
		pods []string
		cert solo.Cert
		d    store.Diagnostic
	}
	expired, rootSoon := map[string]*agg{}, map[string]*agg{}
	for _, d := range ws.Diagnostics(cluster) {
		var certs []solo.Cert
		switch d.Component {
		case "ztunnel":
			certs = solo.Summarise("ztunnel", map[string][]byte{"config_dump.json": d.Files["config_dump.json"]}, ref).Certs
		case "envoy":
			certs = solo.EnvoyCerts(d.Files["certs.json"], ref)
		default:
			continue
		}
		for _, c := range certs {
			k := c.Identity + "|" + c.Serial
			switch {
			case c.NotAfter.IsZero():
				// state-only entry: reported by cert.unavailable below
			case c.HoursLeft <= 0:
				if expired[k] == nil {
					expired[k] = &agg{cert: c, d: d}
				}
				expired[k].pods = append(expired[k].pods, d.Namespace+"/"+d.Pod)
			case c.Root && c.DaysLeft < 30:
				if rootSoon[k] == nil {
					rootSoon[k] = &agg{cert: c, d: d}
				}
				rootSoon[k].pods = append(rootSoon[k].pods, d.Namespace+"/"+d.Pod)
			}
			if d.Component == "ztunnel" && c.State != "" && c.State != "Available" && !c.Root {
				out = append(out, Finding{Rule: "cert.unavailable", Severity: Warning, Cluster: cluster, Object: diagRef(d),
					Title:    fmt.Sprintf("ztunnel %s has no usable certificate for %s", d.Pod, c.Identity),
					Detail:   "ztunnel reported the certificate state as " + c.State + ". Workloads with this identity cannot originate or accept mTLS through this node.",
					Evidence: []string{d.Pod + " config_dump certificates[].state: " + c.State}, NextStep: "Check istiod or the CA for signing errors, and the ztunnel logs for CSR failures.",
					LogQuery: fmt.Sprintf("pod:%s level:error", d.Pod)})
			}
		}
	}
	keys := func(m map[string]*agg) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	for _, k := range keys(expired) {
		a := expired[k]
		kind := "Workload certificate"
		if a.cert.Root {
			kind = "Root certificate"
		}
		out = append(out, Finding{Rule: "cert.expired", Severity: Critical, Cluster: cluster, Object: diagRef(a.d),
			Title:    fmt.Sprintf("%s for %s had expired when the snapshot was taken", kind, a.cert.Identity),
			Detail:   fmt.Sprintf("Expired %s, seen in %s. mTLS using it fails.", a.cert.NotAfter.Format(time.RFC3339), strings.Join(a.pods, ", ")),
			Evidence: []string{fmt.Sprintf("notAfter %s, snapshot %s", a.cert.NotAfter.Format(time.RFC3339), ref.Format(time.RFC3339))},
			NextStep: "For workload certificates, check why rotation stopped (istiod reachability, CA errors). For roots, rotate the mesh CA.",
			Count:    len(a.pods)})
	}
	for _, k := range keys(rootSoon) {
		a := rootSoon[k]
		out = append(out, Finding{Rule: "cert.root-expiring", Severity: Warning, Cluster: cluster, Object: diagRef(a.d),
			Title:    fmt.Sprintf("Mesh %s expires in %d days", a.cert.Identity, a.cert.DaysLeft),
			Detail:   fmt.Sprintf("Expires %s. Seen in %s.", a.cert.NotAfter.Format(time.RFC3339), strings.Join(a.pods, ", ")),
			Evidence: []string{"notAfter " + a.cert.NotAfter.Format(time.RFC3339)}, NextStep: "Plan a root rotation before expiry.", Count: len(a.pods)})
	}
	return out
}

// xds reports proxies istiod has sent config to that have not acknowledged
// it, and NACKs in istiod's logs.
func xds(ws *store.Workspace, cluster string) []Finding {
	var out []Finding
	for _, d := range ws.Diagnostics(cluster) {
		if d.Component != "istiod" {
			continue
		}
		for _, r := range solo.Syncz(d.Files["syncz.json"]) {
			if len(r.Stale) == 0 {
				continue
			}
			pod := r.Proxy
			if i := strings.LastIndex(pod, "."); i > 0 {
				pod = pod[:i]
			}
			out = append(out, Finding{Rule: "xds.stale", Severity: Warning, Cluster: cluster, Object: diagRef(d),
				Title:    fmt.Sprintf("%s has not acknowledged the latest config from %s", r.Proxy, d.Pod),
				Detail:   "istiod sent a newer version than the proxy acknowledged for: " + strings.Join(r.Stale, ", ") + ". The proxy is running older config, or rejected the update.",
				Evidence: []string{d.Pod + " /debug/syncz: sent != acked for " + strings.Join(r.Stale, ", ")},
				NextStep: "Look for a NACK in istiod's logs for this proxy, and check the proxy's own logs.", LogQuery: fmt.Sprintf("NACK OR \"%s\"", pod)})
		}
	}
	type key struct{ pod, proxy string }
	nacks := map[key]int{}
	sample := map[key]string{}
	nackLines, _ := ws.FindLogs(`cluster:`+cluster+` pod:istiod* NACK OR "ACK ERROR"`, 50000)
	for _, rec := range nackLines {
		// Proxy IDs look like sidecar~10.244.1.5~checkout-6d8b9c-f2k7p.payments~payments.svc.cluster.local-12
		proxy := ""
		for _, f := range strings.Fields(rec.Msg) {
			if parts := strings.Split(f, "~"); len(parts) >= 3 {
				proxy = parts[2]
				break
			}
		}
		k := key{rec.Pod, proxy}
		nacks[k]++
		if sample[k] == "" {
			sample[k] = truncate(rec.Msg, 240)
		}
	}
	var ks []key
	for k := range nacks {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return nacks[ks[i]] > nacks[ks[j]] })
	for _, k := range ks {
		who := k.proxy
		if who == "" {
			who = "a proxy"
		}
		out = append(out, Finding{Rule: "xds.nack", Severity: Warning, Cluster: cluster,
			Title:    fmt.Sprintf("%s rejected config pushed by %s (%d NACKs)", who, k.pod, nacks[k]),
			Detail:   "A proxy refused an xDS update, so it keeps running the previous config. The message usually names the invalid resource.",
			Evidence: []string{sample[k]}, Count: nacks[k], LogQuery: fmt.Sprintf("pod:%s NACK", k.pod),
			NextStep: "Find the resource named in the NACK and check the policy or route that produced it."})
	}
	return out
}

// waypoints reports istio.io/use-waypoint labels that name a waypoint that was
// not captured. It only runs when Gateways are completely captured and the
// waypoint's namespace was selected, otherwise absence proves nothing.
func waypoints(ws *store.Workspace, cluster string) []Finding {
	var out []Finding
	gwState := ws.CollectionState(cluster, "gateway.networking.k8s.io", "gateways", "Gateway", true)
	if gwState.State != store.StateComplete {
		return nil
	}
	pol := ws.Policy()
	gws := map[string]bool{}
	for _, g := range ws.List(cluster, "gateway.networking.k8s.io", "gateways", "", nil) {
		gws[g.Namespace+"/"+g.Name] = true
	}
	check := func(o *store.Object, labelNS string) {
		lbl := o.Labels()
		wp := lbl["istio.io/use-waypoint"]
		if wp == "" || wp == "none" {
			return
		}
		ns := lbl["istio.io/use-waypoint-namespace"]
		if ns == "" {
			ns = labelNS
		}
		if pol != nil && !pol.NamespaceAllowed(ns) {
			return
		}
		if gws[ns+"/"+wp] {
			return
		}
		out = append(out, Finding{Rule: "mesh.waypoint-missing", Severity: Critical, Cluster: cluster, Object: ref(o),
			Title:    fmt.Sprintf("%s %s uses waypoint %s/%s, which does not exist", o.Kind, o.Name, ns, wp),
			Detail:   "Traffic that should be handled by the waypoint has nowhere to go. In ambient, a missing waypoint usually means requests to the target fail.",
			Evidence: []string{fmt.Sprintf("label istio.io/use-waypoint=%s", wp), fmt.Sprintf("no Gateway %s/%s in the complete Gateway list", ns, wp)},
			NextStep: "Create the waypoint (istioctl waypoint apply) or fix the label."})
	}
	for _, n := range ws.List(cluster, "", "namespaces", "", nil) {
		check(n, n.Name)
	}
	for _, res := range []struct{ g, r string }{{"", "services"}, {"", "pods"}, {"networking.istio.io", "serviceentries"}} {
		for _, o := range ws.List(cluster, res.g, res.r, "", nil) {
			check(o, o.Namespace)
		}
	}
	return out
}

// cni reports istio-cni DaemonSets that are not ready on every node.
func cni(ws *store.Workspace, cluster string) []Finding {
	var out []Finding
	for _, d := range ws.List(cluster, "apps", "daemonsets", "", nil) {
		if !strings.Contains(d.Name, "istio-cni") {
			continue
		}
		st := m(d.Obj["status"])
		want, ready := num(st["desiredNumberScheduled"]), num(st["numberReady"])
		if want > 0 && ready < want {
			out = append(out, Finding{Rule: "mesh.cni-not-ready", Severity: Critical, Cluster: cluster, Object: ref(d),
				Title:    fmt.Sprintf("istio-cni is ready on %d of %d nodes", ready, want),
				Detail:   "Pods scheduled on a node without a ready istio-cni agent are not redirected into ambient, or fail to start.",
				Evidence: []string{fmt.Sprintf("status.numberReady %d, desiredNumberScheduled %d", ready, want)},
				NextStep: "Find the istio-cni-node pods that are not ready and read their logs.", LogQuery: "pod:istio-cni* level:error"})
		}
	}
	return out
}

// agentgatewaySkew compares agentgateway data-plane and controller versions.
func agentgatewaySkew(ws *store.Workspace, cluster string) []Finding {
	versions := map[string]map[string][]string{} // role -> version -> pods
	add := func(role, v, pod string) {
		if v == "" {
			return
		}
		if versions[role] == nil {
			versions[role] = map[string][]string{}
		}
		versions[role][v] = append(versions[role][v], pod)
	}
	for _, o := range ws.List(cluster, "", "pods", "", nil) {
		for _, c := range l(m(o.Obj["spec"])["containers"]) {
			img := s(m(c)["image"])
			name := img
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			if i := strings.Index(name, ":"); i >= 0 {
				name = name[:i]
			}
			switch {
			case name == "agentgateway" || name == "agentgateway-enterprise":
				add("data plane", tag(img), o.Namespace+"/"+o.Name)
			case strings.HasSuffix(name, "agentgateway-controller"):
				add("controller", tag(img), o.Namespace+"/"+o.Name)
			}
		}
	}
	dp, ctl := versions["data plane"], versions["controller"]
	if len(ctl) == 0 || len(dp) == 0 {
		return nil
	}
	var out []Finding
	for cv := range ctl {
		for dv, pods := range dp {
			if dv != cv {
				sort.Strings(pods)
				out = append(out, Finding{Rule: "agentgateway.version-skew", Severity: Warning, Cluster: cluster,
					Title:    fmt.Sprintf("agentgateway proxies run %s but the controller runs %s", dv, cv),
					Detail:   fmt.Sprintf("%d proxy pod(s) on %s: %s. Proxies are normally rolled with the controller; a mismatch after an upgrade means some gateways were not restarted.", len(pods), dv, truncate(strings.Join(pods, ", "), 300)),
					Evidence: []string{"controller image tag " + cv, "proxy image tag " + dv}, Count: len(pods),
					NextStep: "Restart the gateway deployments, or check the controller's deployer logs."})
			}
		}
	}
	return out
}

// gatewayAPI reports the installed Gateway API CRD bundle version, and mixed
// versions across the CRDs, which breaks controllers in subtle ways.
func gatewayAPI(ws *store.Workspace, cluster string) []Finding {
	byVersion := map[string][]string{}
	channel := ""
	for _, c := range ws.CRDs(cluster) {
		md := m(c["metadata"])
		name := s(md["name"])
		if !strings.HasSuffix(name, ".gateway.networking.k8s.io") {
			continue
		}
		// Gateway API records its release as annotations; accept labels too.
		get := func(k string) string {
			if v := s(m(md["annotations"])[k]); v != "" {
				return v
			}
			return s(m(md["labels"])[k])
		}
		v := get("gateway.networking.k8s.io/bundle-version")
		if v == "" {
			v = "unversioned"
		}
		if ch := get("gateway.networking.k8s.io/channel"); ch != "" {
			channel = ch
		}
		byVersion[v] = append(byVersion[v], strings.TrimSuffix(name, ".gateway.networking.k8s.io"))
	}
	if len(byVersion) == 0 {
		return nil
	}
	var vs []string
	for v := range byVersion {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	if len(vs) == 1 {
		d := "Gateway API CRDs " + vs[0]
		if channel != "" {
			d += ", " + channel + " channel"
		}
		return []Finding{{Rule: "gateway.api-version", Severity: Info, Cluster: cluster, Title: d + " on " + cluster,
			Detail: "Compare this with the Gateway API version the installed agentgateway, kgateway and Istio releases require.", Evidence: []string{fmt.Sprintf("%d CRDs annotated bundle-version %s", len(byVersion[vs[0]]), vs[0])}}}
	}
	var ev []string
	for _, v := range vs {
		sort.Strings(byVersion[v])
		ev = append(ev, fmt.Sprintf("%s: %s", v, strings.Join(byVersion[v], ", ")))
	}
	return []Finding{{Rule: "gateway.api-version-mixed", Severity: Warning, Cluster: cluster,
		Title:    fmt.Sprintf("Gateway API CRDs from %d different releases on %s", len(vs), cluster),
		Detail:   "The Gateway API CRDs come from more than one release, usually after a partial upgrade or two products installing their own copy. Controllers may reject or misread fields.",
		Evidence: ev, NextStep: "Install one Gateway API release (standard or experimental) consistently."}}
}
