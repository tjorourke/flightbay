package flows

import (
	"encoding/json"
	"strconv"
	"strings"
)

// EnvoyPod describes the pod whose log a line came from.
type EnvoyPod struct {
	Namespace, Pod string
	// Gateway is true for ingress/egress gateways and waypoints: their
	// outbound lines are on behalf of the downstream client, not the pod.
	Gateway bool
	Via     string // namespace/name of the gateway or waypoint, when Gateway
}

// envoyTextFields is the order of Istio's default text access log
// (pilot/pkg/model/telemetry_logging.go, EnvoyTextLogFormat):
//
//	[%START_TIME%] "%REQ(:METHOD)% %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)% %PROTOCOL%"
//	%RESPONSE_CODE% %RESPONSE_FLAGS% %RESPONSE_CODE_DETAILS% %CONNECTION_TERMINATION_DETAILS%
//	"%UPSTREAM_TRANSPORT_FAILURE_REASON%" %BYTES_RECEIVED% %BYTES_SENT% %DURATION%
//	%RESP(X-ENVOY-UPSTREAM-SERVICE-TIME)% "%REQ(X-FORWARDED-FOR)%" "%REQ(USER-AGENT)%"
//	"%REQ(X-REQUEST-ID)%" "%REQ(:AUTHORITY)%" "%UPSTREAM_HOST%" %UPSTREAM_CLUSTER_RAW%
//	%UPSTREAM_LOCAL_ADDRESS% %DOWNSTREAM_LOCAL_ADDRESS% %DOWNSTREAM_REMOTE_ADDRESS%
//	%REQUESTED_SERVER_NAME% %ROUTE_NAME%
var envoyTextFields = []string{"start_time", "request", "response_code", "response_flags", "response_code_details",
	"connection_termination_details", "upstream_transport_failure_reason", "bytes_received", "bytes_sent", "duration",
	"upstream_service_time", "x_forwarded_for", "user_agent", "request_id", "authority", "upstream_host", "upstream_cluster",
	"upstream_local_address", "downstream_local_address", "downstream_remote_address", "requested_server_name", "route_name"}

// tokens splits an access log line on spaces, keeping "quoted" and
// [bracketed] runs whole (without the delimiters).
func tokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		switch s[i] {
		case ' ', '\t':
			i++
			continue
		case '"', '[':
			end := byte('"')
			if s[i] == '[' {
				end = ']'
			}
			j := i + 1
			for j < len(s) && s[j] != end {
				if s[j] == '\\' && end == '"' {
					j++
				}
				j++
			}
			if j > len(s) {
				j = len(s)
			}
			out = append(out, s[i+1:j])
			i = j + 1
		default:
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out
}

// EnvoyFields parses an Envoy access log line in Istio's default text format
// or its JSON encoding into the JSON key names. ok is false for other lines.
func EnvoyFields(line string) (map[string]string, bool) {
	s := strings.TrimSpace(line)
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) != nil {
			return nil, false
		}
		if _, ok := m["upstream_cluster"]; !ok {
			if _, ok := m["downstream_remote_address"]; !ok {
				return nil, false
			}
		}
		out := map[string]string{}
		for k, v := range m {
			switch t := v.(type) {
			case string:
				out[k] = t
			case float64:
				out[k] = strconv.FormatFloat(t, 'f', -1, 64)
			}
		}
		return out, true
	}
	if !strings.HasPrefix(s, "[") {
		return nil, false
	}
	t := tokens(s)
	if len(t) < len(envoyTextFields) {
		return nil, false
	}
	out := map[string]string{}
	for i, k := range envoyTextFields {
		out[k] = t[i]
	}
	req := strings.Fields(out["request"])
	if len(req) != 3 {
		return nil, false
	}
	out["method"], out["path"], out["protocol"] = req[0], req[1], req[2]
	delete(out, "request")
	return out, true
}

func dash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// ParseEnvoy converts an Envoy access log line from a sidecar, gateway or
// waypoint into a flow.
//
//   - outbound|8081||artifactory.tools.svc.cluster.local: the destination
//     host and port; the source is the pod itself (sidecar) or the downstream
//     client (gateway, waypoint).
//   - inbound|8080||: the pod is the destination; the downstream remote
//     address is the caller.
//   - inbound-vip|8080|http|reviews.default.svc.cluster.local (waypoints).
//   - PassthroughCluster / BlackHoleCluster: traffic to hosts outside the
//     registry; BlackHole means it was refused (REGISTRY_ONLY).
func ParseEnvoy(line string, pod EnvoyPod) (Flow, bool) {
	m, ok := EnvoyFields(line)
	if !ok {
		return Flow{}, false
	}
	f := Flow{Source: SrcEnvoy, Count: 1, Route: dash(m["route_name"])}
	method, path := dash(m["method"]), dash(m["path"])
	switch {
	case method != "":
		f.Protocol, f.Method, f.Path = "http", method, StripQuery(path)
		if method == "POST" && strings.Contains(strings.ToLower(m["user_agent"]), "grpc") {
			f.Protocol = "grpc"
		}
	default:
		f.Protocol = "tcp"
	}
	remote, _ := SplitHostPort(dash(m["downstream_remote_address"]))
	upHost, upPort := SplitHostPort(dash(m["upstream_host"]))
	authority, _ := SplitHostPort(dash(m["authority"]))
	sni := dash(m["requested_server_name"])
	cluster := dash(m["upstream_cluster"])
	parts := strings.Split(cluster, "|")
	dir := ""
	switch {
	case len(parts) == 4 && parts[0] == "outbound":
		dir = "outbound"
		f.DstHost, f.DstPort = parts[3], atoi(parts[1])
	case len(parts) == 4 && parts[0] == "inbound-vip":
		dir = "inbound"
		f.DstHost, f.DstPort = parts[3], atoi(parts[1])
	case len(parts) >= 2 && parts[0] == "inbound":
		dir = "inbound"
		f.DstPort = atoi(parts[1])
		f.DstNamespace, f.DstWorkload = pod.Namespace, pod.Pod
		if len(parts) == 4 && parts[3] != "" {
			f.DstHost = parts[3]
		}
	case cluster == "PassthroughCluster" || cluster == "InboundPassthroughCluster" || strings.HasPrefix(cluster, "InboundPassthroughCluster"):
		if cluster == "PassthroughCluster" {
			dir = "outbound"
		} else {
			dir = "inbound"
			f.DstNamespace, f.DstWorkload = pod.Namespace, pod.Pod
		}
		f.DstIP, f.DstPort = upHost, upPort
		f.DstHost = first(authority, sni)
	case cluster == "BlackHoleCluster":
		dir = "outbound"
		f.Verdict = Denied
		f.DstHost = first(authority, sni)
		_, f.DstPort = SplitHostPort(dash(m["downstream_local_address"]))
	case cluster == "connect_originate" || cluster == "main_internal" || cluster == "encap" || strings.HasPrefix(cluster, "connect_originate"):
		return Flow{}, false // the HBONE tunnel leg of a waypoint; the inbound-vip line carries the request
	default:
		// Gateways built on Envoy without Istio naming (kgateway, Gloo):
		// use what the line says about the upstream.
		dir = "outbound"
		f.DstHost = first(authority, sni)
		f.DstIP, f.DstPort = upHost, upPort
		if f.DstHost == "" && f.DstIP == "" {
			return Flow{}, false
		}
	}
	if f.DstIP == "" && upHost != "" && dir == "outbound" {
		f.DstIP = upHost
	}
	if f.DstNamespace == "" {
		f.DstNamespace = ServiceNamespace(f.DstHost)
	}
	f.Direction = dir
	switch {
	case dir == "outbound" && !pod.Gateway:
		f.SrcNamespace, f.SrcPod, f.SrcIP = pod.Namespace, pod.Pod, remote
	default:
		f.SrcIP = remote
		if xff := dash(m["x_forwarded_for"]); xff != "" && pod.Gateway {
			if c := strings.TrimSpace(strings.Split(xff, ",")[0]); isIP(c) {
				f.SrcIP = c
			}
		}
	}
	if pod.Gateway {
		f.Via = pod.Via
	}
	flags := m["response_flags"]
	details := strings.ToLower(m["response_code_details"] + " " + m["connection_termination_details"])
	if strings.Contains(flags, "RBAC") || strings.Contains(flags, "UAEX") || strings.Contains(details, "rbac_access_denied") ||
		strings.Contains(details, "ext_authz_denied") || m["response_code"] == "401" || m["response_code"] == "403" {
		f.Verdict = Denied
	}
	return f, true
}
