package flows

import (
	"encoding/json"
	"net/url"
	"strings"
)

// jget reads a key from a JSON object in either naming: the proto names
// hubble prints (pod_name, destination_port) or protojson's camelCase
// (podName, destinationPort).
func jget(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	if v, ok := m[key]; ok {
		return v
	}
	if strings.Contains(key, "_") {
		parts := strings.Split(key, "_")
		for i := 1; i < len(parts); i++ {
			if parts[i] != "" {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
		}
		if v, ok := m[strings.Join(parts, "")]; ok {
			return v
		}
	}
	return nil
}

func jobj(m map[string]any, key string) map[string]any {
	o, _ := jget(m, key).(map[string]any)
	return o
}

func jstr(m map[string]any, key string) string {
	switch t := jget(m, key).(type) {
	case string:
		return t
	case float64:
		return jsonNumber(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func jint(m map[string]any, key string) int { return atoi(jstr(m, key)) }

// hubbleFlow returns the flow object of a hubble line: `-o jsonpb` wraps it
// as {"flow": {...}, "node_name": ...}; `-o json` prints the flow itself.
func hubbleFlow(m map[string]any) map[string]any {
	if f := jobj(m, "flow"); f != nil {
		return f
	}
	if jget(m, "IP") != nil || jget(m, "verdict") != nil && (jget(m, "source") != nil || jget(m, "l4") != nil) {
		return m
	}
	return nil
}

// ParseHubble converts one line of `hubble observe -o json` or `-o jsonpb`.
// Replies (is_reply) are skipped: they are the return leg of a flow already
// counted. ok is false for other lines (including hubble's own lost-events
// notices).
//
//	{"flow":{"verdict":"FORWARDED","IP":{"source":"10.244.1.107","destination":"10.244.2.98"},
//	  "l4":{"TCP":{"source_port":52262,"destination_port":80}},
//	  "source":{"namespace":"default","pod_name":"xwing"},"destination":{"namespace":"default","pod_name":"deathstar-..."},
//	  "Type":"L3_L4","traffic_direction":"EGRESS","is_reply":false}}
func ParseHubble(line []byte) (Flow, bool) {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return Flow{}, false
	}
	fl := hubbleFlow(m)
	if fl == nil {
		return Flow{}, false
	}
	if r, _ := jget(fl, "is_reply").(bool); r {
		return Flow{}, false
	}
	if r := jobj(fl, "is_reply"); r != nil { // wrapped BoolValue in some encoders
		if v, _ := r["value"].(bool); v {
			return Flow{}, false
		}
	}
	f := Flow{Source: FmtHubble, Count: 1}
	ip := jobj(fl, "IP")
	f.SrcIP, f.DstIP = jstr(ip, "source"), jstr(ip, "destination")
	src, dst := jobj(fl, "source"), jobj(fl, "destination")
	f.SrcNamespace, f.SrcPod = jstr(src, "namespace"), jstr(src, "pod_name")
	f.DstNamespace, f.DstWorkload = jstr(dst, "namespace"), jstr(dst, "pod_name")
	l4 := jobj(fl, "l4")
	switch {
	case jobj(l4, "TCP") != nil:
		f.Protocol, f.DstPort = "tcp", jint(jobj(l4, "TCP"), "destination_port")
	case jobj(l4, "UDP") != nil:
		f.Protocol, f.DstPort = "udp", jint(jobj(l4, "UDP"), "destination_port")
	case jobj(l4, "SCTP") != nil:
		f.Protocol, f.DstPort = "sctp", jint(jobj(l4, "SCTP"), "destination_port")
	case jobj(l4, "ICMPv4") != nil || jobj(l4, "ICMPv6") != nil:
		f.Protocol = "icmp"
	}
	if names, ok := jget(fl, "destination_names").([]any); ok && len(names) > 0 {
		f.DstHost, _ = names[0].(string)
	}
	if f.DstHost == "" {
		if svc := jobj(fl, "destination_service"); svc != nil && jstr(svc, "name") != "" {
			f.DstHost = jstr(svc, "name")
			if ns := jstr(svc, "namespace"); ns != "" {
				f.DstHost += "." + ns + ".svc.cluster.local"
			}
		}
	}
	if l7 := jobj(fl, "l7"); l7 != nil {
		if typ := jstr(l7, "type"); typ == "RESPONSE" {
			return Flow{}, false // the request line is counted
		}
		if h := jobj(l7, "http"); h != nil {
			f.Protocol, f.Method = "http", jstr(h, "method")
			if u, err := url.Parse(jstr(h, "url")); err == nil {
				f.Path = u.Path
				if f.DstHost == "" && u.Hostname() != "" && !isIP(u.Hostname()) {
					f.DstHost = u.Hostname()
				}
			}
			if c := jint(h, "code"); c == 401 || c == 403 {
				f.Verdict = Denied
			}
		}
		if d := jobj(l7, "dns"); d != nil {
			f.Protocol = "dns"
			q := strings.TrimSuffix(jstr(d, "query"), ".")
			if q != "" {
				f.DstHost = q
			}
			if qt, ok := jget(d, "qtypes").([]any); ok && len(qt) > 0 {
				f.Method, _ = qt[0].(string)
			}
		}
	}
	switch v := strings.ToUpper(jstr(fl, "verdict")); v {
	case "DROPPED", "ERROR":
		f.Verdict = Denied
	case "FORWARDED", "REDIRECTED", "TRANSLATED", "TRACED":
		if f.Verdict == "" {
			f.Verdict = Allowed
		}
	case "AUDIT":
		if f.Verdict == "" {
			f.Verdict = "audit"
		}
	}
	if f.SrcIP == "" && f.DstIP == "" && f.SrcPod == "" && f.DstWorkload == "" && f.DstHost == "" {
		return Flow{}, false
	}
	return f, true
}
