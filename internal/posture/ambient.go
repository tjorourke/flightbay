package posture

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

func nsObj(name string) *store.Object {
	return &store.Object{Resource: "namespaces", Kind: "Namespace", Name: name}
}

var waypointClasses = map[string]bool{"istio-waypoint": true, "enterprise-agentgateway-waypoint": true, "agentgateway-waypoint": true}

func ambientChecks(e env) []Check {
	var out []Check
	add := func(sp spec, eval func(*Check)) {
		c := sp.check("ambient")
		eval(&c)
		out = append(out, c)
	}
	na := func(c *Check, why string) { c.Status, c.Reason = NA, why }
	nsl, nsKnown := e.nsLabels()
	noNS := "namespace labels were not captured (re-capture with this version of flightbay, which records the Namespace objects in scope)"

	// Namespaces that run workloads, excluding system ones.
	workloadNS := map[string]int{}
	for _, p := range e.list("", "pods") {
		if !isSystem(p.Namespace) {
			workloadNS[p.Namespace]++
		}
	}
	ambientNS := map[string]bool{}
	for ns, lb := range nsl {
		if lb["istio.io/dataplane-mode"] == "ambient" {
			ambientNS[ns] = true
		}
	}
	sortedNS := func(set map[string]bool) []string {
		var out []string
		for n := range set {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	aps := e.list("security.istio.io", "authorizationpolicies")
	apOK, apWhy := e.evaluable("security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true)
	root := "istio-system"

	add(spec{"ambient.namespace-enrolled", "Encryption", "high", "Namespaces with workloads are enrolled in ambient",
		"Without istio.io/dataplane-mode: ambient a namespace stays plain Kubernetes: no mTLS, no workload identity, no L4 authorization.",
		"kubectl label namespace <ns> istio.io/dataplane-mode=ambient", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		if !nsKnown {
			na(c, noNS)
			return
		}
		var names []string
		for ns := range workloadNS {
			names = append(names, ns)
		}
		sort.Strings(names)
		for _, ns := range names {
			lb, ok := nsl[ns]
			if !ok {
				continue
			}
			switch {
			case lb["istio.io/dataplane-mode"] == "ambient":
				c.Items = append(c.Items, e.item(nsObj(ns), Pass, "ambient"))
			case lb["istio-injection"] == "enabled" || lb["istio.io/rev"] != "":
				c.Items = append(c.Items, e.item(nsObj(ns), Warn, "sidecar injection, not ambient"))
			default:
				c.Items = append(c.Items, e.item(nsObj(ns), Fail, fmt.Sprintf("%d pods outside the mesh", workloadNS[ns])))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no application namespaces in the capture")
		}
	})

	add(spec{"ambient.no-opt-out", "Isolation", "medium", "No workload opts out of an ambient namespace",
		"A per-pod istio.io/dataplane-mode: none quietly gives that one workload no identity or encryption.",
		"# remove from the pod template, or document the exception\nmetadata:\n  labels:\n    istio.io/dataplane-mode: none   # delete", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		if !nsKnown {
			na(c, noNS)
			return
		}
		for _, p := range e.list("", "pods") {
			if !ambientNS[p.Namespace] {
				continue
			}
			lb := labels(p)
			// Waypoints and gateways opt out by design: they are the mesh's own proxies.
			if lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["gateway.istio.io/managed"] != "" || lb["istio.io/gateway-name"] != "" {
				continue
			}
			if lb["istio.io/dataplane-mode"] == "none" {
				c.Items = append(c.Items, e.item(p, Fail, "opted out of ambient"))
			}
		}
		if len(ambientNS) == 0 {
			na(c, "no ambient namespaces")
		} else if len(c.Items) == 0 {
			c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Pass, Detail: "no pods opt out"})
		}
	})

	add(spec{"ambient.no-sidecar-mix", "Isolation", "medium", "No sidecar injection alongside ambient",
		"Mixing sidecars into ambient namespaces splits where policy is enforced, and Sidecar resources do nothing in ambient.",
		"metadata:\n  labels:\n    istio.io/dataplane-mode: ambient\n    # remove: istio-injection: enabled", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		if !nsKnown {
			na(c, noNS)
			return
		}
		for ns := range ambientNS {
			lb := nsl[ns]
			if lb["istio-injection"] == "enabled" || lb["istio.io/rev"] != "" {
				c.Items = append(c.Items, e.item(nsObj(ns), Fail, "ambient and sidecar injection labels together"))
			}
		}
		for _, p := range e.list("", "pods") {
			if !ambientNS[p.Namespace] {
				continue
			}
			for _, ct := range l(path(p.Obj, "spec", "containers")) {
				if s(m(ct)["name"]) == "istio-proxy" && !strings.Contains(s(m(ct)["image"]), "ztunnel") && labels(p)["gateway.istio.io/managed"] == "" && labels(p)["gateway.networking.k8s.io/gateway-name"] == "" {
					c.Items = append(c.Items, e.item(p, Warn, "sidecar container in an ambient namespace"))
				}
			}
		}
		for _, sc := range e.list("networking.istio.io", "sidecars") {
			if ambientNS[sc.Namespace] {
				c.Items = append(c.Items, e.item(sc, Warn, "Sidecar resource has no effect in ambient"))
			}
		}
		if len(ambientNS) == 0 {
			na(c, "no ambient namespaces")
		} else if len(c.Items) == 0 {
			c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Pass, Detail: "no sidecars mixed in"})
		}
	})

	add(spec{"ambient.mtls-strict", "Encryption", "high", "mTLS is STRICT",
		"STRICT is the only mode that refuses plaintext from callers outside the mesh; with no PeerAuthentication the effective mode is PERMISSIVE.",
		"apiVersion: security.istio.io/v1\nkind: PeerAuthentication\nmetadata: {name: default, namespace: istio-system}\nspec:\n  mtls: {mode: STRICT}", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		ok, why := e.evaluable("security.istio.io", "peerauthentications", "PeerAuthentication", true)
		if !ok {
			na(c, why)
			return
		}
		if !nsKnown {
			na(c, noNS)
			return
		}
		if len(ambientNS) == 0 {
			na(c, "no namespaces are enrolled in ambient")
			return
		}
		mode := func(ns string) (string, string) {
			for _, pa := range e.ws.List(e.cluster, "security.istio.io", "peerauthentications", ns, nil) {
				if path(pa.Obj, "spec", "selector") == nil {
					return s(path(pa.Obj, "spec", "mtls", "mode")), pa.Namespace + "/" + pa.Name
				}
			}
			return "", ""
		}
		meshMode, meshSrc := mode(root)
		for _, ns := range sortedNS(ambientNS) {
			md, src := mode(ns)
			if md == "" {
				md, src = meshMode, meshSrc
			}
			for _, pa := range e.ws.List(e.cluster, "security.istio.io", "peerauthentications", ns, nil) {
				if strings.Contains(fmt.Sprint(path(pa.Obj, "spec", "portLevelMtls")), "PERMISSIVE") || strings.Contains(fmt.Sprint(path(pa.Obj, "spec", "portLevelMtls")), "DISABLE") {
					c.Items = append(c.Items, e.item(pa, Fail, "port-level mTLS is not STRICT"))
				}
			}
			switch md {
			case "STRICT":
				c.Items = append(c.Items, e.item(nsObj(ns), Pass, "STRICT ("+src+")"))
			case "":
				c.Items = append(c.Items, e.item(nsObj(ns), Fail, "no PeerAuthentication: effective mode PERMISSIVE"))
			default:
				c.Items = append(c.Items, e.item(nsObj(ns), Fail, md+" ("+src+")"))
			}
		}
	})

	add(spec{"ambient.default-deny", "Authorization", "high", "Each ambient namespace starts from default-deny",
		"Zero trust starts from zero: an empty ALLOW policy denies everything, then you allow by identity only what you meant.",
		"apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: default-deny, namespace: <ns>}\nspec: {}", meshBase + "zero-trust-agents-kubernetes/"}, func(c *Check) {
		if !apOK {
			na(c, apWhy)
			return
		}
		if !nsKnown {
			na(c, noNS)
			return
		}
		if len(ambientNS) == 0 {
			na(c, "no namespaces are enrolled in ambient")
			return
		}
		isDeny := func(ap *store.Object) bool {
			sp := m(ap.Obj["spec"])
			act := s(sp["action"])
			return sp["selector"] == nil && sp["targetRefs"] == nil && sp["targetRef"] == nil && len(l(sp["rules"])) == 0 && (act == "" || act == "ALLOW")
		}
		meshDeny := false
		for _, ap := range aps {
			if ap.Namespace == root && isDeny(ap) {
				meshDeny = true
			}
		}
		for _, ns := range sortedNS(ambientNS) {
			st, detail := Fail, "no default-deny policy"
			if meshDeny {
				st, detail = Pass, "mesh-wide default-deny in "+root
			}
			scoped := false
			for _, ap := range e.ws.List(e.cluster, "security.istio.io", "authorizationpolicies", ns, nil) {
				if isDeny(ap) {
					st, detail = Pass, "default-deny "+ap.Name
				} else {
					scoped = true
				}
			}
			if st == Fail && scoped {
				st, detail = Warn, "only workload-scoped policies: other workloads allow all"
			}
			c.Items = append(c.Items, e.item(nsObj(ns), st, detail))
		}
	})

	add(spec{"ambient.no-allow-all", "Authorization", "critical", "No ALLOW policy admits any source",
		"Once an ALLOW policy targets a workload everything else is denied; a rule with no source constraint turns that back into allow-all.",
		"rules:\n- from:\n  - source:\n      principals: [\"cluster.local/ns/<ns>/sa/<caller>\"]", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		if !apOK {
			na(c, apWhy)
			return
		}
		for _, ap := range aps {
			sp := m(ap.Obj["spec"])
			if a := s(sp["action"]); a != "" && a != "ALLOW" {
				continue
			}
			rules := l(sp["rules"])
			if len(rules) == 0 {
				continue // spec: {} is default-deny
			}
			bad := ""
			for _, r := range rules {
				rm := m(r)
				rs := fmt.Sprint(rm)
				switch {
				case len(rm) == 0:
					bad = "an empty rule {} matches every request"
				case rm["from"] == nil && sp["targetRefs"] == nil:
					bad = "a rule with no from: any source"
				case strings.Contains(rs, "principals:[*]") || strings.Contains(rs, "namespaces:[*]"):
					bad = "source \"*\""
				case strings.Contains(rs, "0.0.0.0/0"):
					bad = "ipBlocks 0.0.0.0/0"
				}
			}
			if bad != "" {
				c.Items = append(c.Items, e.item(ap, Fail, bad))
			} else {
				c.Items = append(c.Items, e.item(ap, Pass, "sources constrained"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no ALLOW policies with rules")
		}
	})

	add(spec{"ambient.l7-needs-waypoint", "Authorization", "high", "L7 policy is attached to a waypoint",
		"Only a waypoint parses HTTP. L7 rules on a selector-attached policy are not enforced as written by ztunnel.",
		"spec:\n  targetRefs:\n  - {kind: Gateway, group: gateway.networking.k8s.io, name: <waypoint>}\n  rules:\n  - to:\n    - operation: {methods: [GET]}", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		if !apOK {
			na(c, apWhy)
			return
		}
		for _, ap := range aps {
			sp := m(ap.Obj["spec"])
			if sp["selector"] == nil || !ambientNS[ap.Namespace] {
				continue
			}
			rs := fmt.Sprint(sp["rules"])
			if strings.Contains(rs, "methods:") || strings.Contains(rs, "paths:") || strings.Contains(rs, "hosts:") || strings.Contains(rs, "requestPrincipals") || strings.Contains(rs, "key:request.") {
				c.Items = append(c.Items, e.item(ap, Fail, "L7 fields on a selector policy: attach it to a waypoint with targetRefs"))
			} else {
				c.Items = append(c.Items, e.item(ap, Pass, "L4 only"))
			}
		}
		for _, ra := range e.list("security.istio.io", "requestauthentications") {
			if path(ra.Obj, "spec", "selector") != nil && ambientNS[ra.Namespace] {
				c.Items = append(c.Items, e.item(ra, Fail, "RequestAuthentication on a selector: attach it to a waypoint"))
			}
		}
		if len(c.Items) == 0 {
			if !nsKnown {
				na(c, noNS)
			} else {
				na(c, "no selector-attached policies in ambient namespaces")
			}
		}
	})

	add(spec{"ambient.waypoint-wiring", "Authorization", "high", "Waypoints referenced by use-waypoint exist",
		"A dangling istio.io/use-waypoint label sends traffic to a waypoint that is not there, and L7 policy becomes a silent no-op.",
		"apiVersion: gateway.networking.k8s.io/v1\nkind: Gateway\nmetadata:\n  name: <waypoint>\n  namespace: <ns>\n  labels: {istio.io/waypoint-for: service}\nspec:\n  gatewayClassName: istio-waypoint\n  listeners: [{name: mesh, port: 15008, protocol: HBONE}]",
		meshBase + "istio-ambient-crds/"}, func(c *Check) {
		gws := map[string]string{}
		for _, gw := range e.list("gateway.networking.k8s.io", "gateways") {
			gws[gw.Namespace+"/"+gw.Name] = s(path(gw.Obj, "spec", "gatewayClassName"))
		}
		chk := func(o *store.Object, ns string, lb map[string]string) {
			wp := lb["istio.io/use-waypoint"]
			if wp == "" || wp == "none" {
				return
			}
			if n := lb["istio.io/use-waypoint-namespace"]; n != "" {
				ns = n
			}
			cls, ok := gws[ns+"/"+wp]
			switch {
			case !ok:
				c.Items = append(c.Items, e.item(o, Fail, "uses waypoint "+ns+"/"+wp+", which does not exist"))
			case !waypointClasses[cls]:
				c.Items = append(c.Items, e.item(o, Warn, "waypoint "+wp+" has class "+cls))
			default:
				c.Items = append(c.Items, e.item(o, Pass, "waypoint "+wp))
			}
		}
		for ns, lb := range nsl {
			chk(nsObj(ns), ns, lb)
		}
		for _, sv := range e.list("", "services") {
			chk(sv, sv.Namespace, labels(sv))
		}
		if len(c.Items) == 0 {
			na(c, "no istio.io/use-waypoint labels")
		}
	})

	add(spec{"ambient.request-auth-enforced", "Authentication", "high", "RequestAuthentication is backed by a policy requiring a token",
		"RequestAuthentication only rejects invalid tokens; a request with no token passes unless an AuthorizationPolicy requires a principal.",
		"spec:\n  targetRefs: [{kind: Gateway, group: gateway.networking.k8s.io, name: <waypoint>}]\n  action: ALLOW\n  rules:\n  - from:\n    - source: {requestPrincipals: [\"https://issuer.example.com/*\"]}",
		meshBase + "istio-ambient-crds/"}, func(c *Check) {
		ras := e.list("security.istio.io", "requestauthentications")
		if len(ras) == 0 {
			na(c, "no RequestAuthentication")
			return
		}
		for _, ra := range ras {
			req := false
			for _, ap := range e.ws.List(e.cluster, "security.istio.io", "authorizationpolicies", ra.Namespace, nil) {
				rs := fmt.Sprint(path(ap.Obj, "spec", "rules"))
				if strings.Contains(rs, "requestPrincipals") || strings.Contains(rs, "request.auth") {
					req = true
				}
			}
			noAud := strings.Contains(fmt.Sprint(path(ra.Obj, "spec", "jwtRules")), "audiences") == false
			switch {
			case !req:
				c.Items = append(c.Items, e.item(ra, Fail, "no AuthorizationPolicy requires a request principal: missing tokens are allowed"))
			case noAud:
				c.Items = append(c.Items, e.item(ra, Warn, "jwtRules without audiences"))
			default:
				c.Items = append(c.Items, e.item(ra, Pass, "token required"))
			}
		}
	})

	add(spec{"ambient.egress-controlled", "Isolation", "high", "Outbound traffic from mesh namespaces is controlled",
		"An agent's reach includes everything its callees can fetch; outbound should go through an allowlisted egress point.",
		"apiVersion: security.istio.io/v1\nkind: AuthorizationPolicy\nmetadata: {name: egress-allowlist, namespace: istio-egress}\nspec:\n  targetRefs: [{kind: Gateway, group: gateway.networking.k8s.io, name: egress-waypoint}]\n  action: ALLOW\n  rules:\n  - to:\n    - operation: {hosts: [\"api.anthropic.com\"]}",
		meshBase + "zero-trust-agents-kubernetes/"}, func(c *Check) {
		ok, why := e.evaluable("networking.k8s.io", "networkpolicies", "NetworkPolicy", true)
		if !ok {
			na(c, why)
			return
		}
		if !nsKnown {
			na(c, noNS)
			return
		}
		if len(ambientNS) == 0 {
			na(c, "no namespaces are enrolled in ambient")
			return
		}
		egressWaypoint := false
		for _, se := range e.list("networking.istio.io", "serviceentries") {
			if labels(se)["istio.io/use-waypoint"] != "" {
				egressWaypoint = true
			}
		}
		for _, ns := range sortedNS(ambientNS) {
			np := false
			for _, p := range e.ws.List(e.cluster, "networking.k8s.io", "networkpolicies", ns, nil) {
				if strings.Contains(fmt.Sprint(path(p.Obj, "spec", "policyTypes")), "Egress") {
					np = true
				}
			}
			switch {
			case np:
				c.Items = append(c.Items, e.item(nsObj(ns), Pass, "egress NetworkPolicy"))
			case egressWaypoint:
				c.Items = append(c.Items, e.item(nsObj(ns), Warn, "an egress waypoint exists, but nothing stops traffic bypassing it"))
			default:
				c.Items = append(c.Items, e.item(nsObj(ns), Fail, "unrestricted egress"))
			}
		}
	})

	add(spec{"ambient.no-tls-disable", "Encryption", "medium", "No DestinationRule disables TLS",
		"tls.mode DISABLE on an in-mesh host removes encryption; external hosts need SIMPLE or MUTUAL origination.",
		"trafficPolicy:\n  tls: {mode: SIMPLE, sni: api.external.example.com}", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		drs := e.list("networking.istio.io", "destinationrules")
		if len(drs) == 0 {
			na(c, "no DestinationRules")
			return
		}
		for _, dr := range drs {
			if strings.Contains(fmt.Sprint(path(dr.Obj, "spec", "trafficPolicy")), "mode:DISABLE") {
				c.Items = append(c.Items, e.item(dr, Fail, "tls mode DISABLE"))
			} else {
				c.Items = append(c.Items, e.item(dr, Pass, "TLS not disabled"))
			}
		}
	})

	add(spec{"ambient.telemetry", "Observability", "medium", "Mesh access logging is configured",
		"Denials are where probing shows up; without access logs you will not see it.",
		"apiVersion: telemetry.istio.io/v1\nkind: Telemetry\nmetadata: {name: mesh-default, namespace: istio-system}\nspec:\n  accessLogging:\n  - providers: [{name: envoy}]", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		ok, why := e.evaluable("telemetry.istio.io", "telemetries", "Telemetry", true)
		if !ok {
			na(c, why)
			return
		}
		found := false
		for _, t := range e.list("telemetry.istio.io", "telemetries") {
			if len(l(path(t.Obj, "spec", "accessLogging"))) > 0 {
				found = true
				c.Items = append(c.Items, e.item(t, Pass, "accessLogging"))
			}
		}
		if !found {
			c.Items = append(c.Items, Item{Cluster: e.cluster, Status: Fail, Detail: "no Telemetry resource enables access logging"})
		}
	})

	add(spec{"ambient.extension-hygiene", "Supply chain", "low", "No EnvoyFilter, and security Wasm fails closed",
		"EnvoyFilter is a brittle back door across upgrades; a security-phase Wasm module that fails open is a bypass.",
		"spec:\n  phase: AUTHN\n  failStrategy: FAIL_CLOSE\n  sha256: <digest>", meshBase + "istio-ambient-crds/"}, func(c *Check) {
		for _, ef := range e.list("networking.istio.io", "envoyfilters") {
			c.Items = append(c.Items, e.item(ef, Warn, "EnvoyFilter"))
		}
		for _, wp := range e.list("extensions.istio.io", "wasmplugins") {
			sp := m(wp.Obj["spec"])
			if (s(sp["phase"]) == "AUTHN" || s(sp["phase"]) == "AUTHZ") && s(sp["failStrategy"]) == "FAIL_OPEN" {
				c.Items = append(c.Items, e.item(wp, Fail, "security phase fails open"))
			} else {
				c.Items = append(c.Items, e.item(wp, Pass, "ok"))
			}
		}
		if len(c.Items) == 0 {
			na(c, "no EnvoyFilters or WasmPlugins")
		}
	})
	out = append(out, ambientExtra(e)...)
	return out
}
