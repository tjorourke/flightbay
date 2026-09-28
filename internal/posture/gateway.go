package posture

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// ---------- gateway helpers (prefixed gw) ----------

// gwClassControllers maps GatewayClass name to its controllerName.
func gwClassControllers(e env) map[string]string {
	out := map[string]string{}
	for _, gc := range e.list("gateway.networking.k8s.io", "gatewayclasses") {
		out[gc.Name] = s(path(gc.Obj, "spec", "controllerName"))
	}
	return out
}

// gwRole classifies a Gateway API Gateway: waypoint, agentgateway, eastwest,
// remote, egress, ingress or other.
func gwRole(gw *store.Object, classes map[string]string) string {
	cls := gatewayClass(gw)
	ctrl := classes[cls]
	lc := strings.ToLower(cls)
	switch {
	case waypointClasses[cls] || strings.Contains(lc, "waypoint") || labels(gw)["istio.io/waypoint-for"] != "":
		return "waypoint"
	case strings.Contains(lc, "agentgateway") || strings.Contains(ctrl, "agentgateway"):
		return "agentgateway"
	case strings.Contains(lc, "eastwest"):
		return "eastwest"
	case lc == "istio-remote" || strings.HasSuffix(lc, "-remote"):
		return "remote"
	case strings.Contains(lc, "egress"):
		return "egress"
	case cls == "istio" || cls == "kgateway" || cls == "enterprise-kgateway" || strings.Contains(ctrl, "kgateway") || ctrl == "istio.io/gateway-controller":
		return "ingress"
	}
	return "other"
}

// gwIsKgateway reports whether a Gateway is served by kgateway.
func gwIsKgateway(gw *store.Object, classes map[string]string) bool {
	return strings.Contains(gatewayClass(gw), "kgateway") || strings.Contains(classes[gatewayClass(gw)], "kgateway")
}

// gwParents returns ns/name of the Gateway parents of a route.
func gwParents(r *store.Object) []string {
	var out []string
	for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
		pm := m(pr)
		if k := s(pm["kind"]); k != "" && k != "Gateway" {
			continue
		}
		ns := s(pm["namespace"])
		if ns == "" {
			ns = r.Namespace
		}
		out = append(out, ns+"/"+s(pm["name"]))
	}
	return out
}

// gwAttaches reports whether a route attaches to a Gateway listener.
func gwAttaches(r, gw *store.Object, listener string) bool {
	for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
		pm := m(pr)
		if k := s(pm["kind"]); k != "" && k != "Gateway" {
			continue
		}
		ns := s(pm["namespace"])
		if ns == "" {
			ns = r.Namespace
		}
		if ns != gw.Namespace || s(pm["name"]) != gw.Name {
			continue
		}
		if sec := s(pm["sectionName"]); listener == "" || sec == "" || sec == listener {
			return true
		}
	}
	return false
}

type gwBRef struct{ group, kind, name, ns string }

// gwBackendRefs returns every rules[].backendRefs of a route.
func gwBackendRefs(r *store.Object) []gwBRef {
	var out []gwBRef
	for _, rule := range l(path(r.Obj, "spec", "rules")) {
		for _, br := range l(m(rule)["backendRefs"]) {
			bm := m(br)
			kind := s(bm["kind"])
			if kind == "" {
				kind = "Service"
			}
			ns := s(bm["namespace"])
			if ns == "" {
				ns = r.Namespace
			}
			out = append(out, gwBRef{s(bm["group"]), kind, s(bm["name"]), ns})
		}
	}
	return out
}

// gwHostService resolves a mesh hostname to a Service, or a ServiceEntry
// that lists it.
func gwHostService(x *meshCtx, host, defNS string) *store.Object {
	h := strings.TrimSuffix(host, ".cluster.local")
	h = strings.TrimSuffix(h, ".svc")
	parts := strings.Split(h, ".")
	switch len(parts) {
	case 1:
		if sv := x.svcs[defNS+"/"+parts[0]]; sv != nil {
			return sv
		}
	case 2:
		if sv := x.svcs[parts[1]+"/"+parts[0]]; sv != nil {
			return sv
		}
	}
	for _, se := range x.ses {
		for _, sh := range mxStrings(path(se.Obj, "spec", "hosts")) {
			if sh == host {
				return se
			}
		}
	}
	return nil
}

// gwInternalLB reports whether a LoadBalancer Service is internal-only by
// its cloud annotations.
func gwInternalLB(sv *store.Object) bool {
	for k, v := range m(path(sv.Obj, "metadata", "annotations")) {
		vs := strings.ToLower(s(v))
		switch k {
		case "service.beta.kubernetes.io/aws-load-balancer-internal", "service.beta.kubernetes.io/azure-load-balancer-internal":
			if vs == "true" || vs == "0.0.0.0/0" {
				return true
			}
		case "service.beta.kubernetes.io/aws-load-balancer-scheme":
			if vs == "internal" {
				return true
			}
		case "networking.gke.io/load-balancer-type", "cloud.google.com/load-balancer-type":
			if vs == "internal" {
				return true
			}
		}
	}
	return false
}

// gwReferenceGrant reports whether a ReferenceGrant in toNS lets fromNS
// objects of fromKind refer to toKind/toName.
func gwReferenceGrant(e env, fromKind, fromNS, toGroup, toKind, toNS, toName string) bool {
	for _, rg := range e.ws.List(e.cluster, "gateway.networking.k8s.io", "referencegrants", toNS, nil) {
		from, to := false, false
		for _, f := range l(path(rg.Obj, "spec", "from")) {
			fm := m(f)
			if s(fm["kind"]) == fromKind && s(fm["namespace"]) == fromNS && (s(fm["group"]) == "gateway.networking.k8s.io" || s(fm["group"]) == "networking.istio.io") {
				from = true
			}
		}
		for _, t := range l(path(rg.Obj, "spec", "to")) {
			tm := m(t)
			if s(tm["kind"]) == toKind && s(tm["group"]) == toGroup && (s(tm["name"]) == "" || s(tm["name"]) == toName) {
				to = true
			}
		}
		if from && to {
			return true
		}
	}
	return false
}

// gwTrafficPolicies returns kgateway (and Enterprise kgateway) traffic
// policies that apply to a route: by targetRefs or targetSelectors on the
// route or its Gateway, or through an ExtensionRef filter.
func gwTrafficPolicies(e env, r *store.Object, gws []*store.Object) []*store.Object {
	var all []*store.Object
	all = append(all, e.list("gateway.kgateway.dev", "trafficpolicies")...)
	all = append(all, e.list("enterprisekgateway.solo.io", "enterprisekgatewaytrafficpolicies")...)
	ext := map[string]bool{}
	for _, rule := range l(path(r.Obj, "spec", "rules")) {
		for _, f := range l(m(rule)["filters"]) {
			if er := m(m(f)["extensionRef"]); er != nil {
				ext[s(er["kind"])+"/"+s(er["name"])] = true
			}
		}
	}
	hits := func(p *store.Object, kind string, o *store.Object) bool {
		if p.Namespace != o.Namespace {
			return false
		}
		for _, t := range mxTargets(p) {
			if t.kind == kind && t.name == o.Name {
				return true
			}
		}
		for _, ts := range l(path(p.Obj, "spec", "targetSelectors")) {
			tm := m(ts)
			if s(tm["kind"]) == kind && selects(map[string]any{"matchLabels": tm["matchLabels"]}, labels(o)) && len(m(tm["matchLabels"])) > 0 {
				return true
			}
		}
		return false
	}
	var out []*store.Object
	for _, p := range all {
		ok := hits(p, "HTTPRoute", r) || (p.Namespace == r.Namespace && ext[p.Kind+"/"+p.Name])
		for _, gw := range gws {
			ok = ok || hits(p, "Gateway", gw)
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// gwClassicService returns the exposed Service in front of an Istio classic
// Gateway's pods, and the namespace those pods run in.
func gwClassicService(e env, gw *store.Object) (*store.Object, string) {
	sel := mxLabelMap(path(gw.Obj, "spec", "selector"))
	if len(sel) == 0 {
		return nil, gw.Namespace
	}
	podNS := ""
	for _, p := range e.podsIn("", sel) {
		if podNS == "" || p.Namespace == gw.Namespace {
			podNS = p.Namespace
		}
		for _, sv := range e.ws.List(e.cluster, "", "services", p.Namespace, nil) {
			ss := mxLabelMap(path(sv.Obj, "spec", "selector"))
			if len(ss) > 0 && selects(m(path(sv.Obj, "spec", "selector")), labels(p)) && exposedService(sv) {
				return sv, p.Namespace
			}
		}
	}
	if podNS == "" {
		podNS = gw.Namespace
	}
	return nil, podNS
}

// ---------- the area ----------

// isGateway reports whether the cluster has Gateway API ingress, Istio
// classic gateways or kgateway.
func isGateway(e env) bool {
	cls := gwClassControllers(e)
	for _, gw := range e.list("gateway.networking.k8s.io", "gateways") {
		if gwRole(gw, cls) == "ingress" {
			return true
		}
	}
	for _, r := range e.ws.Resources(e.cluster) {
		if (r.Group == "gateway.kgateway.dev" || r.Group == "enterprisekgateway.solo.io") && len(e.list(r.Group, r.Resource)) > 0 {
			return true
		}
	}
	return len(e.list("networking.istio.io", "gateways")) > 0
}

// gatewayChecks scores Gateway API ingress, Istio classic gateways and
// kgateway. Gateways of class agentgateway are scored by the agentic area.
func gatewayChecks(e env) []Check {
	var out []Check
	add := func(sp spec, eval func(*Check)) {
		c := sp.check("gateway")
		eval(&c)
		out = append(out, c)
	}
	na := func(c *Check, why string) { c.Status, c.Reason = NA, why }
	x := newMeshCtx(e)
	detailOK, detailWhy := e.fullDetail()
	var ingress []*store.Object
	for _, gw := range x.gws {
		if x.role(gw) == "ingress" {
			ingress = append(ingress, gw)
		}
	}
	sortedObjs(ingress)
	classic := sortedObjs(e.list("networking.istio.io", "gateways"))
	var routes []*store.Object
	for _, res := range []string{"httproutes", "grpcroutes", "tlsroutes", "tcproutes"} {
		routes = append(routes, e.list("gateway.networking.k8s.io", res)...)
	}
	sortedObjs(routes)
	httpRoutes := sortedObjs(e.list("gateway.networking.k8s.io", "httproutes"))
	routesOn := func(gw *store.Object, listener string) []*store.Object {
		var rs []*store.Object
		for _, r := range routes {
			if gwAttaches(r, gw, listener) {
				rs = append(rs, r)
			}
		}
		return rs
	}
	ingressKey := map[string]*store.Object{}
	for _, gw := range ingress {
		ingressKey[gw.Namespace+"/"+gw.Name] = gw
	}
	parentsOf := func(r *store.Object) []*store.Object {
		var gs []*store.Object
		for _, k := range gwParents(r) {
			if g := ingressKey[k]; g != nil {
				gs = append(gs, g)
			}
		}
		return gs
	}

	add(spec{"gateway.listener-tls", "Encryption", "high", "Exposed gateways serve HTTPS",
		"An internet-facing listener without TLS sends credentials and tokens in the clear. Plain HTTP should only redirect to HTTPS.",
		"listeners:\n- name: https\n  port: 443\n  protocol: HTTPS\n  tls:\n    mode: Terminate\n    certificateRefs: [{kind: Secret, name: <cert>}]\n---\n# redirect route on the http listener\nfilters:\n- type: RequestRedirect\n  requestRedirect: {scheme: https, statusCode: 301}",
		meshBase + "kgateway-tls-sni-kind/"}, func(c *Check) {
		if !detailOK {
			na(c, detailWhy)
			return
		}
		for _, gw := range ingress {
			sv := e.gatewayService(gw)
			if !exposedService(sv) {
				continue
			}
			tls := false
			var warns []string
			for _, ln := range l(path(gw.Obj, "spec", "listeners")) {
				lm := m(ln)
				switch s(lm["protocol"]) {
				case "HTTPS", "TLS":
					tls = true
				case "HTTP":
					for _, r := range routesOn(gw, s(lm["name"])) {
						if r.Kind != "HTTPRoute" || gwRedirectsHTTPS(r) {
							continue
						}
						warns = append(warns, fmt.Sprintf("HTTP listener %s serves HTTPRoute %s/%s without redirecting to https", s(lm["name"]), r.Namespace, r.Name))
					}
				}
			}
			switch {
			case !tls:
				c.Items = append(c.Items, e.item(gw, Fail, "exposed through "+s(path(sv.Obj, "spec", "type"))+" Service "+sv.Name+" with no HTTPS or TLS listener"))
			case len(warns) > 0:
				c.Items = append(c.Items, e.item(gw, Warn, strings.Join(warns, "; ")))
			default:
				c.Items = append(c.Items, e.item(gw, Pass, "TLS listener; HTTP only redirects"))
			}
		}
		for _, gw := range classic {
			sv, _ := gwClassicService(e, gw)
			if sv == nil {
				continue
			}
			var warns []string
			for _, srv := range l(path(gw.Obj, "spec", "servers")) {
				sm := m(srv)
				proto := strings.ToUpper(s(path(sm, "port", "protocol")))
				redirect, _ := path(sm, "tls", "httpsRedirect").(bool)
				if proto == "HTTP" && !redirect {
					warns = append(warns, fmt.Sprintf("HTTP server on port %v without tls.httpsRedirect", path(sm, "port", "number")))
				}
				if v := s(path(sm, "tls", "minProtocolVersion")); v == "TLSV1_0" || v == "TLSV1_1" {
					warns = append(warns, fmt.Sprintf("port %v allows %s", path(sm, "port", "number"), v))
				}
			}
			if len(warns) > 0 {
				c.Items = append(c.Items, e.item(gw, Warn, strings.Join(warns, "; ")))
			} else {
				c.Items = append(c.Items, e.item(gw, Pass, "servers use TLS or redirect"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no exposed ingress Gateways")
		}
	})

	add(spec{"gateway.tls-secret-valid", "Encryption", "critical", "Listener certificates point at usable Secrets",
		"A listener whose certificate Secret is missing, of the wrong type, lacks tls.crt or tls.key, or sits in another namespace without a ReferenceGrant, is not programmed, and HTTPS on it fails.",
		"kubectl create secret tls <cert> -n <gateway-ns> --cert=tls.crt --key=tls.key\n# cross-namespace: a ReferenceGrant in the Secret's namespace\nspec:\n  from: [{group: gateway.networking.k8s.io, kind: Gateway, namespace: <gateway-ns>}]\n  to: [{group: \"\", kind: Secret, name: <cert>}]",
		meshBase + "kgateway-tls-sni-kind/"}, func(c *Check) {
		if ok, why := e.absenceOK("", "secrets", "Secret", true); !ok {
			na(c, why)
			return
		}
		rgOK, rgWhy := e.absenceOK("gateway.networking.k8s.io", "referencegrants", "ReferenceGrant", true)
		judge := func(ns, name string, strict bool) (Status, string) {
			o := e.ws.Get(e.cluster, "", "secrets", ns, name)
			if o == nil {
				return Fail, "Secret " + ns + "/" + name + " does not exist"
			}
			keys, _, known := e.secretKeys(ns, name)
			typ := e.secretType(ns, name)
			if strict && typ != "" && typ != "kubernetes.io/tls" {
				return Fail, "Secret " + ns + "/" + name + " is type " + typ + ", not kubernetes.io/tls"
			}
			if !known {
				return Pass, "Secret " + ns + "/" + name + " exists (keys not captured)"
			}
			if keys["tls.crt"] > 0 && keys["tls.key"] > 0 {
				return Pass, "Secret " + ns + "/" + name
			}
			if !strict && keys["cert"] > 0 && keys["key"] > 0 {
				return Pass, "Secret " + ns + "/" + name + " (cert/key)"
			}
			return Fail, "Secret " + ns + "/" + name + " has no non-empty tls.crt and tls.key"
		}
		for _, gw := range ingress {
			st, notes := Pass, []string{}
			any := false
			for _, ln := range l(path(gw.Obj, "spec", "listeners")) {
				for _, cr := range l(path(ln, "tls", "certificateRefs")) {
					cm := m(cr)
					if k := s(cm["kind"]); (k != "" && k != "Secret") || s(cm["group"]) != "" {
						continue
					}
					any = true
					ns := s(cm["namespace"])
					if ns == "" {
						ns = gw.Namespace
					}
					s1, d := judge(ns, s(cm["name"]), true)
					if ns != gw.Namespace && s1 == Pass {
						switch {
						case !rgOK:
							d += " (cross-namespace; ReferenceGrants not captured: " + rgWhy + ")"
						case !gwReferenceGrant(e, "Gateway", gw.Namespace, "", "Secret", ns, s(cm["name"])):
							s1, d = Fail, "Secret "+ns+"/"+s(cm["name"])+" is in another namespace with no ReferenceGrant"
						}
					}
					if statusRank[s1] < statusRank[st] {
						st = s1
					}
					notes = append(notes, d)
				}
			}
			if any {
				c.Items = append(c.Items, e.item(gw, st, strings.Join(notes, "; ")))
			}
		}
		for _, gw := range classic {
			_, podNS := gwClassicService(e, gw)
			st, notes := Pass, []string{}
			any := false
			for _, srv := range l(path(gw.Obj, "spec", "servers")) {
				cn := s(path(srv, "tls", "credentialName"))
				if cn == "" || strings.Contains(cn, "://") {
					continue
				}
				any = true
				s1, d := judge(podNS, cn, false)
				if statusRank[s1] < statusRank[st] {
					st = s1
				}
				notes = append(notes, d)
			}
			if any {
				c.Items = append(c.Items, e.item(gw, st, strings.Join(notes, "; ")))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no ingress listeners reference certificate Secrets")
		}
	})

	add(spec{"gateway.referencegrants", "Isolation", "high", "Cross-namespace references have a ReferenceGrant",
		"A route may only send to a Service in another namespace when that namespace grants it. Without the grant the backend is refused, and a broad grant hands out more than one Secret.",
		"apiVersion: gateway.networking.k8s.io/v1beta1\nkind: ReferenceGrant\nmetadata: {name: allow-<route-ns>, namespace: <backend-ns>}\nspec:\n  from: [{group: gateway.networking.k8s.io, kind: HTTPRoute, namespace: <route-ns>}]\n  to: [{group: \"\", kind: Service, name: <svc>}]",
		meshBase + "gateway-api-ambient/"}, func(c *Check) {
		ok, why := e.absenceOK("gateway.networking.k8s.io", "referencegrants", "ReferenceGrant", true)
		if !ok {
			na(c, why)
			return
		}
		agw := map[string]bool{}
		for _, gw := range x.gws {
			if x.role(gw) == "agentgateway" {
				agw[gw.Namespace+"/"+gw.Name] = true
			}
		}
		for _, r := range routes {
			ps := gwParents(r)
			allAgw := len(ps) > 0 && len(ps) == len(l(path(r.Obj, "spec", "parentRefs")))
			for _, p := range ps {
				allAgw = allAgw && agw[p]
			}
			if allAgw {
				continue
			}
			var bad, good []string
			for _, br := range gwBackendRefs(r) {
				if br.ns == r.Namespace || br.kind == "HTTPRoute" {
					continue
				}
				if gwReferenceGrant(e, r.Kind, r.Namespace, br.group, br.kind, br.ns, br.name) {
					good = append(good, br.kind+" "+br.ns+"/"+br.name)
				} else {
					bad = append(bad, br.kind+" "+br.ns+"/"+br.name)
				}
			}
			switch {
			case len(bad) > 0:
				c.Items = append(c.Items, e.item(r, Fail, "no ReferenceGrant for "+strings.Join(bad, ", ")))
			case len(good) > 0:
				c.Items = append(c.Items, e.item(r, Pass, "granted: "+strings.Join(good, ", ")))
			}
		}
		for _, rg := range sortedObjs(e.list("gateway.networking.k8s.io", "referencegrants")) {
			for _, t := range l(path(rg.Obj, "spec", "to")) {
				if tm := m(t); s(tm["kind"]) == "Secret" && s(tm["name"]) == "" {
					c.Items = append(c.Items, e.item(rg, Warn, "grants every Secret in "+rg.Namespace+": name the one certificate"))
					break
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no cross-namespace backend references and no Secret grants")
		}
	})

	add(spec{"gateway.shared-gateway-isolation", "Isolation", "medium", "Shared gateways limit who can attach routes",
		"allowedRoutes from All lets any namespace attach a route and take traffic. When routes from several namespaces share a listener with no hostnames, they compete for the same requests.",
		"listeners:\n- name: https\n  hostname: \"*.example.com\"\n  allowedRoutes:\n    namespaces:\n      from: Selector\n      selector: {matchLabels: {shared-gateway-access: \"true\"}}",
		meshBase + "gateway-api-ambient/"}, func(c *Check) {
		for _, gw := range ingress {
			var warns []string
			for _, ln := range l(path(gw.Obj, "spec", "listeners")) {
				lm := m(ln)
				name := s(lm["name"])
				if s(path(lm, "allowedRoutes", "namespaces", "from")) == "All" {
					warns = append(warns, "listener "+name+" allows routes from All namespaces")
				}
				if s(lm["hostname"]) != "" {
					continue
				}
				nss := map[string]bool{}
				var bare []string
				for _, r := range routesOn(gw, name) {
					nss[r.Namespace] = true
					if len(l(path(r.Obj, "spec", "hostnames"))) == 0 && (r.Kind == "HTTPRoute" || r.Kind == "GRPCRoute") {
						bare = append(bare, r.Namespace+"/"+r.Name)
					}
				}
				if len(nss) > 1 && len(bare) > 0 {
					warns = append(warns, fmt.Sprintf("listener %s has no hostname, routes from %d namespaces, and %s set no hostnames", name, len(nss), strings.Join(bare, ", ")))
				}
			}
			if len(warns) > 0 {
				c.Items = append(c.Items, e.item(gw, Warn, strings.Join(warns, "; ")))
			} else {
				c.Items = append(c.Items, e.item(gw, Pass, "route attachment is scoped"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no ingress Gateways")
		}
	})

	add(spec{"gateway.client-ip-policy", "Authorization", "high", "IP-based policy at the gateway sees the real client address",
		"ipBlocks matches the connection's source address. Behind a LoadBalancer with externalTrafficPolicy Cluster that is a node IP, so the rule matches the wrong thing. remoteIpBlocks needs numTrustedProxies to read X-Forwarded-For correctly.",
		"# gateway Service\nspec:\n  externalTrafficPolicy: Local\n# or via the Gateway\nspec:\n  infrastructure:\n    annotations:\n      proxy.istio.io/config: '{\"gatewayTopology\": {\"numTrustedProxies\": 1}}'",
		meshBase + "ambient-client-ip-authz-at-waypoint/"}, func(c *Check) {
		trusted := false
		for _, d := range e.ws.Diagnostics(e.cluster) {
			if d.Component == "istiod" && strings.Contains(string(d.Files["mesh.json"]), "numTrustedProxies") {
				trusted = true
			}
		}
		for _, gw := range ingress {
			var aps []*store.Object
			for _, ap := range x.aps {
				if ap.Namespace != gw.Namespace {
					continue
				}
				for _, t := range mxTargets(ap) {
					if t.kind == "Gateway" && t.name == gw.Name {
						aps = append(aps, ap)
					}
				}
			}
			gwTrusted := trusted || strings.Contains(fmt.Sprint(path(gw.Obj, "metadata", "annotations")), "numTrustedProxies") ||
				strings.Contains(fmt.Sprint(path(gw.Obj, "spec", "infrastructure")), "numTrustedProxies")
			for _, p := range e.gatewayPods(gw) {
				gwTrusted = gwTrusted || strings.Contains(fmt.Sprint(path(p.Obj, "metadata", "annotations")), "numTrustedProxies")
			}
			sv := e.gatewayService(gw)
			for _, ap := range aps {
				var ipb, remote bool
				for _, src := range mxSources(ap) {
					ipb = ipb || src["ipBlocks"] != nil || src["notIpBlocks"] != nil
					remote = remote || src["remoteIpBlocks"] != nil || src["notRemoteIpBlocks"] != nil
				}
				if !ipb && !remote {
					continue
				}
				switch {
				case ipb && sv != nil && s(path(sv.Obj, "spec", "externalTrafficPolicy")) != "Local":
					c.Items = append(c.Items, e.item(ap, Fail, "ipBlocks on gateway "+gw.Name+", but Service "+sv.Name+" has externalTrafficPolicy "+strings.TrimSpace(s(path(sv.Obj, "spec", "externalTrafficPolicy"))+" (not Local)")+": the source is a node IP"))
				case remote && !gwTrusted:
					c.Items = append(c.Items, e.item(ap, Warn, "remoteIpBlocks on gateway "+gw.Name+" with no numTrustedProxies set"))
				default:
					c.Items = append(c.Items, e.item(ap, Pass, "client address is preserved"))
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no IP-based AuthorizationPolicies on ingress Gateways")
		}
	})

	add(spec{"gateway.gateway-ha", "Resilience", "medium", "Gateway and waypoint proxies run more than one replica",
		"Ingress, waypoint and east-west proxies are on the request path. A single replica, or no PodDisruptionBudget, turns a node drain or a crash into an outage.",
		"# on the Gateway (Istio) or the GatewayParameters (kgateway)\nspec:\n  infrastructure:\n    parametersRef: {group: \"\", kind: ConfigMap, name: <gw>-options}\n# HPA and PDB\nkind: HorizontalPodAutoscaler\nspec: {minReplicas: 2}\n---\nkind: PodDisruptionBudget\nspec: {minAvailable: 1}",
		meshBase + "hbone-east-west/"}, func(c *Check) {
		if !detailOK {
			na(c, detailWhy)
			return
		}
		pdbOK, pdbWhy := e.absenceOK("policy", "poddisruptionbudgets", "PodDisruptionBudget", true)
		for _, d := range sortedObjs(e.list("apps", "deployments")) {
			lb := labels(d)
			tl := podTemplateLabels(d)
			name := lb["gateway.networking.k8s.io/gateway-name"]
			if name == "" {
				name = tl["gateway.networking.k8s.io/gateway-name"]
			}
			role := ""
			if name != "" {
				if gw := x.gws[d.Namespace+"/"+name]; gw != nil {
					role = x.role(gw)
					if strings.Contains(gatewayClass(gw), "agentgateway") {
						continue // agentgateway proxies are scored in the agentic area
					}
				}
			} else if tl["istio"] == "ingressgateway" || tl["istio"] == "eastwestgateway" {
				role = "ingress"
				if tl["istio"] == "eastwestgateway" {
					role = "eastwest"
				}
			}
			if role != "ingress" && role != "waypoint" && role != "eastwest" {
				continue
			}
			n, desc := mxReplicas(e, d)
			var warns []string
			if n < 2 {
				warns = append(warns, desc)
			}
			if pdbOK && len(mxPDBs(e, d)) == 0 {
				warns = append(warns, "no PodDisruptionBudget")
			}
			if len(warns) > 0 {
				c.Items = append(c.Items, e.item(d, Warn, role+": "+strings.Join(warns, "; ")))
			} else {
				note := desc
				if !pdbOK {
					note += "; PDBs not checked (" + pdbWhy + ")"
				}
				c.Items = append(c.Items, e.item(d, Pass, role+": "+note))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no ingress, waypoint or east-west gateway Deployments")
		}
	})

	add(spec{"gateway.upstream-tls-verified", "Encryption", "high", "TLS to backends verifies the server",
		"TLS without verification encrypts the connection but accepts any certificate, so anyone on the path can impersonate the backend.",
		"apiVersion: gateway.networking.k8s.io/v1\nkind: BackendTLSPolicy\nspec:\n  targetRefs: [{group: \"\", kind: Service, name: <svc>}]\n  validation:\n    hostname: <svc>.example.com\n    wellKnownCACertificates: System",
		meshBase + "kgateway-tls-sni-kind/"}, func(c *Check) {
		for _, p := range sortedObjs(e.list("gateway.kgateway.dev", "backendconfigpolicies")) {
			t := m(path(p.Obj, "spec", "tls"))
			if t == nil {
				continue
			}
			skip, _ := t["insecureSkipVerify"].(bool)
			mv := s(path(t, "parameters", "minVersion"))
			switch {
			case skip:
				c.Items = append(c.Items, e.item(p, Fail, "tls.insecureSkipVerify is true"))
			case mv == "1.0" || mv == "1.1":
				c.Items = append(c.Items, e.item(p, Warn, "tls.parameters.minVersion "+mv))
			default:
				c.Items = append(c.Items, e.item(p, Pass, "backend certificate verified"))
			}
		}
		for _, p := range sortedObjs(e.list("gateway.networking.k8s.io", "backendtlspolicies")) {
			v := m(path(p.Obj, "spec", "validation"))
			if len(l(v["caCertificateRefs"])) == 0 && s(v["wellKnownCACertificates"]) == "" {
				c.Items = append(c.Items, e.item(p, Fail, "validation has neither caCertificateRefs nor wellKnownCACertificates"))
			} else {
				c.Items = append(c.Items, e.item(p, Pass, "validates against a CA"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no backend TLS policies (DestinationRule TLS is scored by ambient.no-tls-disable)")
		}
	})

	add(spec{"gateway.cors-safe", "Security", "medium", "CORS does not allow any origin with credentials",
		"Allowing every origin together with credentials lets any website make logged-in requests to your API from a victim's browser and read the answers.",
		"filters:\n- type: CORS\n  cors:\n    allowOrigins: [\"https://app.example.com\"]\n    allowCredentials: true", meshBase + "kgateway-tls-sni-kind/"}, func(c *Check) {
		judge := func(o *store.Object, cors map[string]any, origins []string) {
			cred, _ := cors["allowCredentials"].(bool)
			wild := false
			for _, og := range origins {
				if og == "*" {
					wild = true
				}
			}
			switch {
			case wild && cred:
				c.Items = append(c.Items, e.item(o, Fail, "allows origin * with credentials"))
			case wild:
				c.Items = append(c.Items, e.item(o, Pass, "origin * without credentials"))
			default:
				c.Items = append(c.Items, e.item(o, Pass, "origins listed"))
			}
		}
		for _, r := range httpRoutes {
			if ps := gwParents(r); len(ps) > 0 && len(parentsOf(r)) == 0 {
				agwOnly := true
				for _, p := range ps {
					if g := x.gws[p]; g == nil || x.role(g) != "agentgateway" {
						agwOnly = false
					}
				}
				if agwOnly {
					continue
				}
			}
			for _, rule := range l(path(r.Obj, "spec", "rules")) {
				for _, f := range l(m(rule)["filters"]) {
					if cors := m(m(f)["cors"]); cors != nil {
						judge(r, cors, mxStrings(cors["allowOrigins"]))
					}
				}
			}
		}
		for _, res := range []struct{ g, r string }{{"gateway.kgateway.dev", "trafficpolicies"}, {"enterprisekgateway.solo.io", "enterprisekgatewaytrafficpolicies"}} {
			for _, p := range sortedObjs(e.list(res.g, res.r)) {
				if cors := m(path(p.Obj, "spec", "cors")); cors != nil {
					judge(p, cors, mxStrings(cors["allowOrigins"]))
				}
			}
		}
		for _, vs := range sortedObjs(e.list("networking.istio.io", "virtualservices")) {
			for _, hr := range l(path(vs.Obj, "spec", "http")) {
				cp := m(m(hr)["corsPolicy"])
				if cp == nil {
					continue
				}
				origins := mxStrings(cp["allowOrigin"])
				for _, og := range l(cp["allowOrigins"]) {
					if ex := s(m(og)["exact"]); ex != "" {
						origins = append(origins, ex)
					}
					if rx := s(m(og)["regex"]); rx == ".*" || rx == "*" {
						origins = append(origins, "*")
					}
				}
				judge(vs, cp, origins)
			}
		}
		if len(c.Items) == 0 {
			na(c, "no CORS configuration")
		}
	})

	add(spec{"gateway.timeouts-retries", "Resilience", "low", "Ingress routes set timeouts and bounded retries",
		"Without a request timeout a slow backend holds gateway connections open until clients give up. Unbounded retries multiply load on a backend that is already failing.",
		"rules:\n- timeouts: {request: 30s}\n  retry: {attempts: 2}\n# kgateway\nkind: TrafficPolicy\nspec:\n  timeouts: {request: 30s}\n  retry: {attempts: 2, perTryTimeout: 10s}",
		meshBase + "kgateway-tls-sni-kind/"}, func(c *Check) {
		for _, r := range httpRoutes {
			gws := parentsOf(r)
			if len(gws) == 0 {
				continue
			}
			var warns []string
			timeout := false
			for _, rule := range l(path(r.Obj, "spec", "rules")) {
				rm := m(rule)
				if s(path(rm, "timeouts", "request")) != "" {
					timeout = true
				}
				if a, ok := mxNum(path(rm, "retry", "attempts")); ok && a > 5 {
					warns = append(warns, fmt.Sprintf("retry attempts %g", a))
				}
			}
			for _, p := range gwTrafficPolicies(e, r, gws) {
				sp := m(p.Obj["spec"])
				if s(path(sp, "timeouts", "request")) != "" {
					timeout = true
				}
				if rt := m(sp["retry"]); rt != nil {
					if a, ok := mxNum(rt["attempts"]); ok && a > 5 {
						warns = append(warns, fmt.Sprintf("%s %s retries %g times", p.Kind, p.Name, a))
					}
					if rt["perTryTimeout"] == nil {
						warns = append(warns, p.Kind+" "+p.Name+" retries without perTryTimeout")
					}
				}
			}
			if !timeout {
				warns = append([]string{"no request timeout"}, warns...)
			}
			if len(warns) > 0 {
				c.Items = append(c.Items, e.item(r, Warn, strings.Join(warns, "; ")))
			} else {
				c.Items = append(c.Items, e.item(r, Pass, "timeout set, retries bounded"))
			}
		}
		for _, vs := range sortedObjs(e.list("networking.istio.io", "virtualservices")) {
			edge := false
			for _, g := range mxStrings(path(vs.Obj, "spec", "gateways")) {
				if g != "mesh" {
					edge = true
				}
			}
			if !edge {
				continue
			}
			var warns []string
			for _, hr := range l(path(vs.Obj, "spec", "http")) {
				hm := m(hr)
				if s(hm["timeout"]) == "" && hm["redirect"] == nil && hm["directResponse"] == nil {
					warns = append(warns, "a route has no timeout")
				}
				if rt := m(hm["retries"]); rt != nil {
					if a, ok := mxNum(rt["attempts"]); ok && a > 5 {
						warns = append(warns, fmt.Sprintf("retries.attempts %g", a))
					}
					if a, _ := mxNum(rt["attempts"]); a > 0 && s(rt["perTryTimeout"]) == "" {
						warns = append(warns, "retries without perTryTimeout")
					}
				}
			}
			if len(warns) > 0 {
				c.Items = append(c.Items, e.item(vs, Warn, strings.Join(gwUniq(warns), "; ")))
			} else {
				c.Items = append(c.Items, e.item(vs, Pass, "timeouts set, retries bounded"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no routes on ingress gateways")
		}
	})

	add(spec{"gateway.kgw-auth-fails-closed", "Authentication", "high", "kgateway authentication fails closed",
		"An external auth provider that fails open, or JWT validation that lets missing or invalid tokens through, means the gateway admits requests whenever the check cannot decide.",
		"apiVersion: gateway.kgateway.dev/v1alpha1\nkind: GatewayExtension\nspec:\n  extAuth:\n    failOpen: false\n---\nspec:\n  jwt:\n    validationMode: Strict\n    providers:\n    - name: idp\n      issuer: https://idp.example.com\n      audiences: [api]",
		meshBase + "kgateway-tls-sni-kind/"}, func(c *Check) {
		for _, ge := range sortedObjs(e.list("gateway.kgateway.dev", "gatewayextensions")) {
			sp := m(ge.Obj["spec"])
			var fails, warns []string
			if ea := m(sp["extAuth"]); ea != nil {
				if fo, _ := ea["failOpen"].(bool); fo {
					fails = append(fails, "extAuth.failOpen is true")
				}
			}
			jwt := m(sp["jwt"])
			if jwt != nil {
				switch s(jwt["validationMode"]) {
				case "AllowMissingOrFailed":
					fails = append(fails, "jwt.validationMode AllowMissingOrFailed rejects nothing")
				case "AllowMissing":
					warns = append(warns, "jwt.validationMode AllowMissing lets requests without a token through")
				}
				for _, p := range l(jwt["providers"]) {
					if len(l(m(p)["audiences"])) == 0 {
						warns = append(warns, "provider "+s(m(p)["name"])+" has no audiences")
					}
				}
			}
			if sp["extAuth"] == nil && jwt == nil {
				continue
			}
			switch {
			case len(fails) > 0:
				c.Items = append(c.Items, e.item(ge, Fail, strings.Join(append(fails, warns...), "; ")))
			case len(warns) > 0:
				c.Items = append(c.Items, e.item(ge, Warn, strings.Join(warns, "; ")))
			default:
				c.Items = append(c.Items, e.item(ge, Pass, "fails closed"))
			}
		}
		tps := e.list("gateway.kgateway.dev", "trafficpolicies")
		gwAuth := func(gw *store.Object, field string) string {
			for _, p := range tps {
				if p.Namespace != gw.Namespace || path(p.Obj, "spec", field, "extensionRef") == nil {
					continue
				}
				for _, t := range mxTargets(p) {
					if t.kind == "Gateway" && t.name == gw.Name {
						return p.Name
					}
				}
			}
			return ""
		}
		for _, p := range sortedObjs(append([]*store.Object{}, tps...)) {
			for _, field := range []string{"extAuth", "jwtAuth"} {
				if path(p.Obj, "spec", field, "disable") == nil {
					continue
				}
				for _, t := range mxTargets(p) {
					if t.kind != "HTTPRoute" {
						continue
					}
					r := e.ws.Get(e.cluster, "gateway.networking.k8s.io", "httproutes", p.Namespace, t.name)
					if r == nil {
						continue
					}
					for _, gw := range parentsOf(r) {
						if by := gwAuth(gw, field); by != "" {
							c.Items = append(c.Items, e.item(p, Warn, fmt.Sprintf("disables %s from %s on route %s", field, by, r.Name)))
						}
					}
				}
			}
		}
		if len(c.Items) == 0 {
			na(c, "no kgateway extAuth or JWT GatewayExtensions")
		}
	})

	add(spec{"gateway.kgw-public-route-protected", "Authentication", "medium", "Public kgateway routes are authenticated and rate limited",
		"A route on an internet-facing gateway with no authentication is open to everyone, and with no rate limit one client can exhaust the backend.",
		"apiVersion: gateway.kgateway.dev/v1alpha1\nkind: TrafficPolicy\nmetadata: {name: protect, namespace: <ns>}\nspec:\n  targetRefs: [{group: gateway.networking.k8s.io, kind: HTTPRoute, name: <route>}]\n  jwtAuth: {extensionRef: {name: jwt-provider}}\n  rateLimit:\n    local: {tokenBucket: {maxTokens: 100, fillInterval: 1s}}",
		meshBase + "kgateway-tls-sni-kind/"}, func(c *Check) {
		exposed := map[string]*store.Object{}
		for _, gw := range ingress {
			if gwIsKgateway(gw, x.gwClass) && exposedService(e.gatewayService(gw)) {
				exposed[gw.Namespace+"/"+gw.Name] = gw
			}
		}
		if len(exposed) == 0 {
			na(c, "no exposed kgateway Gateways")
			return
		}
		for _, r := range httpRoutes {
			var gws []*store.Object
			for _, k := range gwParents(r) {
				if g := exposed[k]; g != nil {
					gws = append(gws, g)
				}
			}
			if len(gws) == 0 {
				continue
			}
			auth, rl := "", ""
			for _, p := range gwTrafficPolicies(e, r, gws) {
				sp := m(p.Obj["spec"])
				for _, f := range []string{"extAuth", "jwtAuth", "basicAuth", "apiKeyAuth", "oauth2", "rbac", "entExtAuth", "entJWT", "entRBAC"} {
					if v := m(sp[f]); v != nil && v["disable"] == nil && auth == "" {
						auth = p.Kind + " " + p.Name + " " + f
					}
				}
				for _, f := range []string{"rateLimit", "entRateLimit"} {
					if sp[f] != nil && rl == "" {
						rl = p.Kind + " " + p.Name + " " + f
					}
				}
			}
			if auth == "" {
				for _, gw := range gws {
					ra, ap := false, false
					for _, o := range x.ras {
						for _, t := range mxTargets(o) {
							ra = ra || (t.kind == "Gateway" && t.name == gw.Name && o.Namespace == gw.Namespace)
						}
					}
					for _, o := range x.aps {
						for _, t := range mxTargets(o) {
							ap = ap || (t.kind == "Gateway" && t.name == gw.Name && o.Namespace == gw.Namespace)
						}
					}
					if ra && ap {
						auth = "Istio RequestAuthentication and AuthorizationPolicy on " + gw.Name
					}
				}
			}
			var warns []string
			if auth == "" {
				warns = append(warns, "no authentication applies")
			}
			if rl == "" {
				warns = append(warns, "no rate limit applies")
			}
			if len(warns) > 0 {
				c.Items = append(c.Items, e.item(r, Warn, strings.Join(warns, "; ")))
			} else {
				c.Items = append(c.Items, e.item(r, Pass, auth+"; "+rl))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no HTTPRoutes on exposed kgateway Gateways")
		}
	})
	return out
}

// gwRedirectsHTTPS reports whether every rule of a route redirects to https.
func gwRedirectsHTTPS(r *store.Object) bool {
	rules := l(path(r.Obj, "spec", "rules"))
	if len(rules) == 0 {
		return false
	}
	for _, rule := range rules {
		ok := false
		for _, f := range l(m(rule)["filters"]) {
			if s(path(f, "requestRedirect", "scheme")) == "https" {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func gwUniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
