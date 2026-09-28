package posture

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	collectpolicy "github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

// meshWS is an empty standard-profile workspace for cluster "c" with an
// ambient namespace "app" and istio-system.
func meshWS(t *testing.T) *store.Workspace {
	t.Helper()
	ws := store.New()
	p, _ := collectpolicy.Profile("standard")
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	put(ws, res("", "namespaces", "Namespace", false),
		obj("Namespace", "", "app", map[string]any{"istio.io/dataplane-mode": "ambient"}, nil),
		obj("Namespace", "", "istio-system", nil, nil),
		obj("Namespace", "", "edge", nil, nil))
	put(ws, res("", "pods", "Pod", true), obj("Pod", "istio-system", "ztunnel-a", map[string]any{"app": "ztunnel"}, nil))
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete; s.Verified = true })
	return ws
}

// put marks a type completely collected and adds objects to it.
func put(ws *store.Workspace, r bundle.APIResource, objs ...map[string]any) {
	ws.SetCoverage(bundle.Coverage{ID: "c." + r.Resource + "." + r.Group, Cluster: "c", Type: bundle.CoverageResource, Group: r.Group, Version: r.Version, Resource: r.Resource, State: bundle.StateComplete})
	if len(objs) > 0 {
		ws.AddObjects("c", r, objs, r.Resource+r.Group+objs[0]["metadata"].(map[string]any)["name"].(string))
	}
}

func with(o map[string]any, key string, v any) map[string]any {
	switch key {
	case "annotations":
		o["metadata"].(map[string]any)["annotations"] = v
	default:
		o[key] = v
	}
	return o
}

var (
	rPods     = res("", "pods", "Pod", true)
	rSvc      = res("", "services", "Service", true)
	rSecrets  = res("", "secrets", "Secret", true)
	rDeploy   = res("apps", "deployments", "Deployment", true)
	rPDB      = res("policy", "poddisruptionbudgets", "PodDisruptionBudget", true)
	rGW       = res("gateway.networking.k8s.io", "gateways", "Gateway", true)
	mx_rRoute = res("gateway.networking.k8s.io", "httproutes", "HTTPRoute", true)
	rRG       = res("gateway.networking.k8s.io", "referencegrants", "ReferenceGrant", true)
	rAP       = res("security.istio.io", "authorizationpolicies", "AuthorizationPolicy", true)
	rPA       = res("security.istio.io", "peerauthentications", "PeerAuthentication", true)
	rGE       = res("gateway.kgateway.dev", "gatewayextensions", "GatewayExtension", true)
	rDR       = res("networking.istio.io", "destinationrules", "DestinationRule", true)
)

func statuses(c Check) map[string]Status {
	out := map[string]Status{}
	for _, it := range c.Items {
		out[it.Name] = it.Status
	}
	return out
}

func mustStatus(t *testing.T, r Report, id string, want Status) Check {
	t.Helper()
	c := find(r, id)
	if c.ID == "" {
		t.Fatalf("%s did not run", id)
	}
	if c.Status != want {
		t.Fatalf("%s: got %s, want %s (%s) %+v", id, c.Status, want, c.Reason, c.Items)
	}
	return c
}

func TestWorkloadsCaptured(t *testing.T) {
	ws := meshWS(t)
	running := map[string]any{"phase": "Running"}
	put(ws, rPods,
		with(with(obj("Pod", "app", "good", nil, map[string]any{}), "status", running), "annotations", map[string]any{"ambient.istio.io/redirection": "enabled"}),
		with(obj("Pod", "app", "pending", nil, map[string]any{}), "status", map[string]any{"phase": "Pending"}),
		with(obj("Pod", "app", "wp-1", map[string]any{"gateway.networking.k8s.io/gateway-name": "wp"}, map[string]any{}), "status", running))
	c := mustStatus(t, Evaluate(ws), "ambient.workloads-captured", Pass)
	if got := statuses(c); len(got) != 1 || got["good"] != Pass {
		t.Fatalf("only the running app pod should be judged: %+v", got)
	}
	put(ws, rPods,
		with(obj("Pod", "app", "missed", nil, map[string]any{}), "status", running),
		with(with(obj("Pod", "app", "hostnet", nil, map[string]any{"hostNetwork": true}), "status", running), "annotations", map[string]any{"ambient.istio.io/redirection": "enabled"}))
	got := statuses(mustStatus(t, Evaluate(ws), "ambient.workloads-captured", Fail))
	if got["missed"] != Fail || got["hostnet"] != Fail || got["good"] != Pass {
		t.Fatalf("%+v", got)
	}
}

func TestWorkloadsCapturedNAWithoutAmbient(t *testing.T) {
	ws := store.New()
	p, _ := collectpolicy.Profile("standard")
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	put(ws, res("", "namespaces", "Namespace", false), obj("Namespace", "", "plain", nil, nil))
	put(ws, rPods, obj("Pod", "istio-system", "ztunnel-a", map[string]any{"app": "ztunnel"}, nil),
		with(obj("Pod", "plain", "x", nil, map[string]any{}), "status", map[string]any{"phase": "Running"}))
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete })
	r := Evaluate(ws)
	mustStatus(t, r, "ambient.workloads-captured", NA)
	mustStatus(t, r, "ambient.egress-default-deny", NA)
	mustStatus(t, r, "mesh.shared-root", NA)
	mustStatus(t, r, "mesh.control-plane-metrics-clean", NA)
}

func TestPolicyEnforced(t *testing.T) {
	ws := meshWS(t)
	put(ws, rSvc,
		obj("Service", "app", "plain", nil, map[string]any{"selector": map[string]any{"app": "plain"}}),
		obj("Service", "app", "fronted", map[string]any{"istio.io/use-waypoint": "wp"}, map[string]any{"selector": map[string]any{"app": "fronted"}}))
	put(ws, rGW, obj("Gateway", "app", "wp", nil, map[string]any{"gatewayClassName": "istio-waypoint"}))
	svcRef := func(name string) []any {
		return []any{map[string]any{"group": "", "kind": "Service", "name": name}}
	}
	put(ws, rAP, obj("AuthorizationPolicy", "app", "on-fronted", nil, map[string]any{"targetRefs": svcRef("fronted")}))
	mustStatus(t, Evaluate(ws), "ambient.policy-enforced", Pass)

	put(ws, rAP,
		obj("AuthorizationPolicy", "app", "on-plain", nil, map[string]any{"targetRefs": svcRef("plain")}),
		with(obj("AuthorizationPolicy", "app", "rejected", nil, map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "x"}}}), "status",
			map[string]any{"conditions": []any{map[string]any{"type": "ZtunnelAccepted", "status": "False", "reason": "UnsupportedValue", "message": "L7 rule"}}}),
		obj("AuthorizationPolicy", "app", "on-missing-gw", nil, map[string]any{"targetRefs": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "nope"}}}))
	got := statuses(mustStatus(t, Evaluate(ws), "ambient.policy-enforced", Fail))
	if got["on-plain"] != Fail || got["rejected"] != Fail || got["on-missing-gw"] != Fail || got["on-fronted"] != Pass {
		t.Fatalf("%+v", got)
	}
}

func TestIstiodHA(t *testing.T) {
	ws := meshWS(t)
	put(ws, rPDB)
	dep := func(n int) map[string]any {
		return obj("Deployment", "istio-system", "istiod", map[string]any{"app": "istiod"}, map[string]any{"replicas": float64(n),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "istiod"}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "istiod"}}, "spec": map[string]any{}}})
	}
	put(ws, rDeploy, dep(1))
	mustStatus(t, Evaluate(ws), "mesh.istiod-ha", Warn)
	put(ws, rPDB, obj("PodDisruptionBudget", "istio-system", "istiod", nil, map[string]any{"minAvailable": float64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": "istiod"}}}))
	c := mustStatus(t, Evaluate(ws), "mesh.istiod-ha", Fail)
	if !strings.Contains(c.Items[0].Detail, "drains") {
		t.Fatalf("%+v", c.Items)
	}
	put(ws, rDeploy, dep(2))
	mustStatus(t, Evaluate(ws), "mesh.istiod-ha", Pass)
}

func TestPluggedInCA(t *testing.T) {
	keys := func(names ...string) map[string]any {
		var ks []any
		for _, n := range names {
			ks = append(ks, map[string]any{"name": n, "bytes": float64(100)})
		}
		return map[string]any{"captureMode": "keys", "keys": ks}
	}
	ws := meshWS(t)
	put(ws, rSecrets, with(obj("Secret", "istio-system", "istio-ca-secret", nil, nil), "x-flightbay", keys("ca-cert.pem", "ca-key.pem")))
	mustStatus(t, Evaluate(ws), "mesh.plugged-in-ca", Warn)
	put(ws, rGW, obj("Gateway", "istio-eastwest", "peer-b", nil, map[string]any{"gatewayClassName": "istio-remote"}))
	mustStatus(t, Evaluate(ws), "mesh.plugged-in-ca", Fail)

	ws = meshWS(t)
	put(ws, rSecrets, with(obj("Secret", "istio-system", "cacerts", nil, nil), "x-flightbay", keys("ca-cert.pem", "ca-key.pem", "root-cert.pem", "cert-chain.pem")))
	mustStatus(t, Evaluate(ws), "mesh.plugged-in-ca", Pass)

	// Secrets excluded from the capture: n/a, never a failure.
	ws = meshWS(t)
	ws.RegisterResources("c", []bundle.APIResource{rSecrets})
	c := mustStatus(t, Evaluate(ws), "mesh.plugged-in-ca", NA)
	if !strings.Contains(c.Reason, "not captured") {
		t.Fatalf("%+v", c)
	}
}

func TestMeshPortsNotPublic(t *testing.T) {
	ws := meshWS(t)
	put(ws, rPods, obj("Pod", "istio-system", "istiod-1", map[string]any{"app": "istiod", "istio": "pilot"}, nil))
	svc := func(typ string, ports ...float64) map[string]any {
		var ps []any
		for _, p := range ports {
			ps = append(ps, map[string]any{"port": p})
		}
		return obj("Service", "istio-system", "istiod", nil, map[string]any{"type": typ, "selector": map[string]any{"app": "istiod"}, "ports": ps})
	}
	put(ws, rSvc, svc("ClusterIP", 15010, 15012))
	mustStatus(t, Evaluate(ws), "mesh.mesh-ports-not-public", NA)
	put(ws, rSvc, svc("LoadBalancer", 15012, 15017))
	mustStatus(t, Evaluate(ws), "mesh.mesh-ports-not-public", Pass)
	put(ws, rSvc, svc("LoadBalancer", 15010, 15012))
	c := mustStatus(t, Evaluate(ws), "mesh.mesh-ports-not-public", Fail)
	if !strings.Contains(c.Items[0].Detail, "15010") {
		t.Fatalf("%+v", c.Items)
	}
}

// ingressWS has an exposed istio-class Gateway edge/web.
func ingressWS(t *testing.T, listeners ...any) *store.Workspace {
	ws := meshWS(t)
	put(ws, rGW, obj("Gateway", "edge", "web", nil, map[string]any{"gatewayClassName": "istio", "listeners": listeners}))
	put(ws, rSvc, obj("Service", "edge", "web-istio", map[string]any{"gateway.networking.k8s.io/gateway-name": "web"}, map[string]any{"type": "LoadBalancer"}))
	return ws
}

func TestListenerTLS(t *testing.T) {
	httpL := map[string]any{"name": "http", "port": float64(80), "protocol": "HTTP"}
	httpsL := map[string]any{"name": "https", "port": float64(443), "protocol": "HTTPS", "tls": map[string]any{"certificateRefs": []any{map[string]any{"name": "cert"}}}}
	ws := ingressWS(t, httpL)
	if !isGateway(env{ws: ws, cluster: "c"}) {
		t.Fatal("istio-class Gateway should make the gateway area present")
	}
	mustStatus(t, Evaluate(ws), "gateway.listener-tls", Fail)

	ws = ingressWS(t, httpL, httpsL)
	put(ws, mx_rRoute, obj("HTTPRoute", "edge", "redirect", nil, map[string]any{"parentRefs": []any{map[string]any{"name": "web", "sectionName": "http"}},
		"rules": []any{map[string]any{"filters": []any{map[string]any{"type": "RequestRedirect", "requestRedirect": map[string]any{"scheme": "https"}}}}}}))
	mustStatus(t, Evaluate(ws), "gateway.listener-tls", Pass)

	put(ws, mx_rRoute, obj("HTTPRoute", "edge", "plain", nil, map[string]any{"parentRefs": []any{map[string]any{"name": "web"}},
		"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "app"}}}}}))
	mustStatus(t, Evaluate(ws), "gateway.listener-tls", Warn)
}

func TestTLSSecretValid(t *testing.T) {
	httpsL := func(ns, name string) map[string]any {
		ref := map[string]any{"name": name}
		if ns != "" {
			ref["namespace"] = ns
		}
		return map[string]any{"name": "https", "port": float64(443), "protocol": "HTTPS", "tls": map[string]any{"certificateRefs": []any{ref}}}
	}
	tlsKeys := map[string]any{"keys": []any{map[string]any{"name": "tls.crt", "bytes": float64(1200)}, map[string]any{"name": "tls.key", "bytes": float64(1600)}}}

	ws := ingressWS(t, httpsL("", "missing"))
	put(ws, rSecrets)
	put(ws, rRG)
	mustStatus(t, Evaluate(ws), "gateway.tls-secret-valid", Fail)

	ws = ingressWS(t, httpsL("", "opaque"))
	put(ws, rRG)
	put(ws, rSecrets, with(with(obj("Secret", "edge", "opaque", nil, nil), "type", "Opaque"), "x-flightbay", tlsKeys))
	mustStatus(t, Evaluate(ws), "gateway.tls-secret-valid", Fail)

	ws = ingressWS(t, httpsL("", "cert"))
	put(ws, rRG)
	put(ws, rSecrets, with(with(obj("Secret", "edge", "cert", nil, nil), "type", "kubernetes.io/tls"), "x-flightbay", tlsKeys))
	mustStatus(t, Evaluate(ws), "gateway.tls-secret-valid", Pass)

	ws = ingressWS(t, httpsL("certs", "cert"))
	put(ws, rRG)
	put(ws, rSecrets, with(with(obj("Secret", "certs", "cert", nil, nil), "type", "kubernetes.io/tls"), "x-flightbay", tlsKeys))
	mustStatus(t, Evaluate(ws), "gateway.tls-secret-valid", Fail)
	put(ws, rRG, obj("ReferenceGrant", "certs", "edge-certs", nil, map[string]any{
		"from": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "edge"}},
		"to":   []any{map[string]any{"group": "", "kind": "Secret", "name": "cert"}}}))
	mustStatus(t, Evaluate(ws), "gateway.tls-secret-valid", Pass)

	ws = ingressWS(t, httpsL("", "cert"))
	ws.RegisterResources("c", []bundle.APIResource{rSecrets})
	mustStatus(t, Evaluate(ws), "gateway.tls-secret-valid", NA)
}

func TestReferenceGrants(t *testing.T) {
	ws := ingressWS(t, map[string]any{"name": "http", "port": float64(80), "protocol": "HTTP"})
	put(ws, rRG)
	mustStatus(t, Evaluate(ws), "gateway.referencegrants", NA)
	put(ws, mx_rRoute, obj("HTTPRoute", "edge", "shop", nil, map[string]any{"parentRefs": []any{map[string]any{"name": "web"}},
		"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "cart", "namespace": "shop", "port": float64(80)}}}}}))
	mustStatus(t, Evaluate(ws), "gateway.referencegrants", Fail)
	put(ws, rRG, obj("ReferenceGrant", "shop", "from-edge", nil, map[string]any{
		"from": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "namespace": "edge"}},
		"to":   []any{map[string]any{"group": "", "kind": "Service"}}}))
	mustStatus(t, Evaluate(ws), "gateway.referencegrants", Pass)
	put(ws, rRG, obj("ReferenceGrant", "shop", "all-secrets", nil, map[string]any{
		"from": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "edge"}},
		"to":   []any{map[string]any{"group": "", "kind": "Secret"}}}))
	got := statuses(mustStatus(t, Evaluate(ws), "gateway.referencegrants", Warn))
	if got["shop"] != Pass || got["all-secrets"] != Warn {
		t.Fatalf("%+v", got)
	}
}

func TestGatewayHA(t *testing.T) {
	ws := ingressWS(t, map[string]any{"name": "http", "port": float64(80), "protocol": "HTTP"})
	put(ws, rGW, obj("Gateway", "ai", "agw", nil, map[string]any{"gatewayClassName": "agentgateway"}))
	put(ws, rPDB)
	dep := func(ns, gw string, n int) map[string]any {
		lb := map[string]any{"gateway.networking.k8s.io/gateway-name": gw}
		return obj("Deployment", ns, gw, lb, map[string]any{"replicas": float64(n), "selector": map[string]any{"matchLabels": lb},
			"template": map[string]any{"metadata": map[string]any{"labels": lb}, "spec": map[string]any{}}})
	}
	put(ws, rDeploy, dep("edge", "web", 1), dep("ai", "agw", 1))
	c := mustStatus(t, Evaluate(ws), "gateway.gateway-ha", Warn)
	if got := statuses(c); len(got) != 1 || got["web"] != Warn {
		t.Fatalf("agentgateway proxies belong to the agentic area: %+v", got)
	}
	put(ws, rDeploy, dep("edge", "web", 2))
	put(ws, rPDB, obj("PodDisruptionBudget", "edge", "web", nil, map[string]any{"minAvailable": float64(1),
		"selector": map[string]any{"matchLabels": map[string]any{"gateway.networking.k8s.io/gateway-name": "web"}}}))
	mustStatus(t, Evaluate(ws), "gateway.gateway-ha", Pass)
}

func TestKgatewayAuthFailsClosed(t *testing.T) {
	ws := meshWS(t)
	put(ws, rGE, obj("GatewayExtension", "kgw", "jwt", nil, map[string]any{"jwt": map[string]any{"validationMode": "Strict",
		"providers": []any{map[string]any{"name": "idp", "issuer": "https://idp", "audiences": []any{"api"}}}}}))
	if !isGateway(env{ws: ws, cluster: "c"}) {
		t.Fatal("kgateway objects should make the gateway area present")
	}
	mustStatus(t, Evaluate(ws), "gateway.kgw-auth-fails-closed", Pass)
	put(ws, rGE, obj("GatewayExtension", "kgw", "loose", nil, map[string]any{"jwt": map[string]any{"validationMode": "AllowMissing",
		"providers": []any{map[string]any{"name": "idp", "issuer": "https://idp", "audiences": []any{"api"}}}}}))
	mustStatus(t, Evaluate(ws), "gateway.kgw-auth-fails-closed", Warn)
	put(ws, rGE, obj("GatewayExtension", "kgw", "ext", nil, map[string]any{"extAuth": map[string]any{"failOpen": true}}))
	got := statuses(mustStatus(t, Evaluate(ws), "gateway.kgw-auth-fails-closed", Fail))
	if got["ext"] != Fail || got["loose"] != Warn || got["jwt"] != Pass {
		t.Fatalf("%+v", got)
	}
}

func TestEgressDefaultDeny(t *testing.T) {
	ws := meshWS(t)
	put(ws, rAP)
	mustStatus(t, Evaluate(ws), "ambient.egress-default-deny", NA)
	put(ws, rGW, obj("Gateway", "istio-egress", "egress", nil, map[string]any{"gatewayClassName": "solo-ztunnel-egress"}))
	mustStatus(t, Evaluate(ws), "ambient.egress-default-deny", Fail)
	put(ws, rAP, obj("AuthorizationPolicy", "istio-egress", "deny-all", nil, map[string]any{
		"targetRefs": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "egress"}}}))
	mustStatus(t, Evaluate(ws), "ambient.egress-default-deny", Pass)
	if isGateway(env{ws: ws, cluster: "c"}) {
		t.Fatal("an egress gateway is not ingress")
	}
}

func TestMTLSStrictPortAndSelectorDisable(t *testing.T) {
	ws := meshWS(t)
	put(ws, rPA, obj("PeerAuthentication", "istio-system", "default", nil, map[string]any{"mtls": map[string]any{"mode": "STRICT"}}))
	mustStatus(t, Evaluate(ws), "ambient.mtls-strict", Pass)
	put(ws, rPA,
		obj("PeerAuthentication", "app", "metrics-port", nil, map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "a"}},
			"portLevelMtls": map[string]any{"9090": map[string]any{"mode": "DISABLE"}}}),
		obj("PeerAuthentication", "app", "legacy", nil, map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "b"}},
			"mtls": map[string]any{"mode": "DISABLE"}}))
	got := statuses(mustStatus(t, Evaluate(ws), "ambient.mtls-strict", Fail))
	if got["metrics-port"] != Fail || got["legacy"] != Fail || got["app"] != Pass {
		t.Fatalf("%+v", got)
	}
}

func TestNoTLSDisableSkipVerify(t *testing.T) {
	ws := meshWS(t)
	put(ws, rDR, obj("DestinationRule", "app", "ok", nil, map[string]any{"host": "a", "trafficPolicy": map[string]any{"tls": map[string]any{"mode": "SIMPLE"}}}),
		obj("DestinationRule", "app", "skip", nil, map[string]any{"host": "b", "trafficPolicy": map[string]any{"tls": map[string]any{"mode": "SIMPLE", "insecureSkipVerify": true}}}))
	got := statuses(mustStatus(t, Evaluate(ws), "ambient.no-tls-disable", Fail))
	if got["ok"] != Pass || got["skip"] != Fail {
		t.Fatalf("%+v", got)
	}
}

func TestPrincipalTrustDomain(t *testing.T) {
	ws := meshWS(t)
	rules := func(ps ...any) []any {
		return []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": ps}}}}}
	}
	wpRef := []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "wp"}}
	put(ws, rAP, obj("AuthorizationPolicy", "app", "p", nil, map[string]any{"targetRefs": wpRef, "rules": rules("cluster.local/ns/app/sa/x")}))
	mustStatus(t, Evaluate(ws), "ambient.principal-trust-domain", NA) // trust domain unknown
	ws.AddDiagnostic("c", "control-plane", "istio-system", "istiod-1", "meta.json", []byte(`{"component":"istiod"}`))
	ws.AddDiagnostic("c", "control-plane", "istio-system", "istiod-1", "mesh.json", []byte(`{"trustDomain":"prod.example"}`))
	mustStatus(t, Evaluate(ws), "ambient.principal-trust-domain", Pass) // a waypoint maps cluster.local to the local domain
	put(ws, rAP,
		obj("AuthorizationPolicy", "app", "q", nil, map[string]any{"targetRefs": wpRef, "rules": rules("staging.example/ns/app/sa/x", "*")}),
		obj("AuthorizationPolicy", "app", "l4", nil, map[string]any{"rules": rules("cluster.local/ns/app/sa/x")}),
		obj("AuthorizationPolicy", "app", "l4-ok", nil, map[string]any{"rules": rules("prod.example/ns/app/sa/x", "*/ns/app/sa/y")}))
	got := statuses(mustStatus(t, Evaluate(ws), "ambient.principal-trust-domain", Fail))
	if got["q"] != Fail || got["p"] != Pass || got["l4"] != Fail || got["l4-ok"] != Pass {
		t.Fatalf("%+v", got)
	}
}

func TestRevisionResolves(t *testing.T) {
	ws := meshWS(t)
	put(ws, res("", "namespaces", "Namespace", false), obj("Namespace", "", "legacy", map[string]any{"istio.io/rev": "canary"}, nil))
	put(ws, rPods, obj("Pod", "istio-system", "istiod-canary-1", map[string]any{"app": "istiod", "istio.io/rev": "stable"}, nil))
	c := mustStatus(t, Evaluate(ws), "mesh.revision-resolves", NA) // webhooks not captured, may be a tag
	if !strings.Contains(c.Reason, "webhooks") {
		t.Fatalf("%+v", c)
	}
	put(ws, res("admissionregistration.k8s.io", "mutatingwebhookconfigurations", "MutatingWebhookConfiguration", false))
	mustStatus(t, Evaluate(ws), "mesh.revision-resolves", Fail)
	put(ws, rPods, obj("Pod", "istio-system", "istiod-canary-2", map[string]any{"app": "istiod", "istio.io/rev": "canary"}, nil))
	mustStatus(t, Evaluate(ws), "mesh.revision-resolves", Pass)
}

func TestWaypointAndAgentgatewayAreNotIngress(t *testing.T) {
	ws := meshWS(t)
	put(ws, rGW, obj("Gateway", "app", "wp", nil, map[string]any{"gatewayClassName": "istio-waypoint"}),
		obj("Gateway", "ai", "agw", nil, map[string]any{"gatewayClassName": "enterprise-agentgateway"}),
		obj("Gateway", "istio-eastwest", "ew", nil, map[string]any{"gatewayClassName": "istio-eastwest"}))
	if isGateway(env{ws: ws, cluster: "c"}) {
		t.Fatal("waypoint, agentgateway and east-west gateways must not make the ingress area present")
	}
}

func TestSharedRootAcrossClusters(t *testing.T) {
	build := func(rootB, interB string) *store.Workspace {
		ws := store.New()
		p, _ := collectpolicy.Profile("standard")
		b, _ := json.Marshal(p)
		ws.SetHeader(&bundle.Header{Contexts: []string{"a", "b"}, Policy: b})
		dump := func(root, inter string) []byte {
			return []byte(`{"certificates":[{"identity":"spiffe://cluster.local/ns/app/sa/x","state":"Available",` +
				`"certChain":[{"serialNumber":"leaf","expirationTime":"2030-01-01T00:00:00Z"},{"serialNumber":"` + inter + `","expirationTime":"2030-01-01T00:00:00Z"},{"serialNumber":"` + root + `","expirationTime":"2030-01-01T00:00:00Z"}],` +
				`"rootCerts":[{"serialNumber":"` + root + `","expirationTime":"2030-01-01T00:00:00Z"}]}]}`)
		}
		for cl, ids := range map[string][2]string{"a": {"root-1", "inter-a"}, "b": {rootB, interB}} {
			ws.AddObjects(cl, rPods, []map[string]any{obj("Pod", "istio-system", "ztunnel-"+cl, map[string]any{"app": "ztunnel"}, nil)}, "p"+cl)
			ws.AddObjects(cl, rGW, []map[string]any{obj("Gateway", "istio-eastwest", "peer", nil, map[string]any{"gatewayClassName": "istio-remote"})}, "g"+cl)
			ws.AddDiagnostic(cl, "proxies", "istio-system", "ztunnel-"+cl, "meta.json", []byte(`{"component":"ztunnel"}`))
			ws.AddDiagnostic(cl, "proxies", "istio-system", "ztunnel-"+cl, "config_dump.json", dump(ids[0], ids[1]))
		}
		ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete })
		return ws
	}
	c := mustStatus(t, Evaluate(build("root-1", "inter-b")), "mesh.shared-root", Pass)
	if len(c.Items) != 2 {
		t.Fatalf("one item per cluster, evaluated once: %+v", c.Items)
	}
	mustStatus(t, Evaluate(build("root-1", "inter-a")), "mesh.shared-root", Warn)
	mustStatus(t, Evaluate(build("root-2", "inter-b")), "mesh.shared-root", Fail)
}

func TestControlPlaneMetrics(t *testing.T) {
	ws := meshWS(t)
	ws.AddDiagnostic("c", "control-plane", "istio-system", "istiod-1", "meta.json", []byte(`{"component":"istiod"}`))
	ws.AddDiagnostic("c", "control-plane", "istio-system", "istiod-1", "metrics.prom", []byte("# HELP x\npilot_eds_no_instances 0\npilot_conflict_inbound_listener 0\n"))
	mustStatus(t, Evaluate(ws), "mesh.control-plane-metrics-clean", Pass)
	ws.AddDiagnostic("c", "control-plane", "istio-system", "istiod-1", "metrics.prom", []byte("pilot_eds_no_instances 0\npilot_conflict_outbound_listener_tcp_over_current_tcp{a=\"b\"} 3\n"))
	c := mustStatus(t, Evaluate(ws), "mesh.control-plane-metrics-clean", Warn)
	if !strings.Contains(c.Items[0].Detail, "tcp_over_current_tcp 3") {
		t.Fatalf("%+v", c.Items)
	}
}
