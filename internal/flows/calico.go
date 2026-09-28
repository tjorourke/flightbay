package flows

import (
	"encoding/json"
	"strings"
)

// ParseCalico converts one Calico Enterprise / Calico Cloud flow log record
// (JSON, as written to /var/log/calico/flowlogs/flows.log or exported from
// the tigera_secure_ee_flows index).
//
// Calico aggregates by prefix, so names can end in "*" (source_name_aggr)
// and the full name may be "-". Both ends report a flow (reporter src and
// dst); an allowed flow between two workload endpoints is counted from the
// source side only so it is not doubled, while a denial is kept from
// whichever side denied it.
//
//	{"start_time":1700000000,"end_time":1700000300,"source_ip":"10.0.1.5","source_name":"-",
//	 "source_name_aggr":"frontend-7d9f8c-*","source_namespace":"shop","source_type":"wep",
//	 "dest_ip":"10.0.2.9","dest_name_aggr":"checkout-5c6b-*","dest_namespace":"shop","dest_type":"wep",
//	 "dest_port":8080,"proto":"tcp","action":"allow","reporter":"src","num_flows":3}
func ParseCalico(line []byte) (Flow, bool) {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return Flow{}, false
	}
	if m["source_namespace"] == nil && m["dest_namespace"] == nil && m["source_name_aggr"] == nil {
		return Flow{}, false
	}
	action, reporter := strings.ToLower(jstr(m, "action")), strings.ToLower(jstr(m, "reporter"))
	if reporter == "dst" && action == "allow" && jstr(m, "source_type") == "wep" {
		return Flow{}, false
	}
	name := func(prefix string) string {
		n := dash(jstr(m, prefix+"_name"))
		if n == "" {
			n = dash(jstr(m, prefix+"_name_aggr"))
		}
		return n
	}
	f := Flow{Source: FmtCalico, Count: 1,
		SrcIP: dash(jstr(m, "source_ip")), SrcNamespace: dash(jstr(m, "source_namespace")), SrcPod: name("source"),
		DstIP: dash(jstr(m, "dest_ip")), DstNamespace: dash(jstr(m, "dest_namespace")), DstWorkload: name("dest"),
		DstPort: jint(m, "dest_port"), Protocol: ProtoName(jstr(m, "proto")), Verdict: verdictOf(action)}
	if f.SrcNamespace == "" && jstr(m, "source_type") == "net" {
		f.SrcPod = "" // a network set or the internet: keep the IP only
	}
	if svc := dash(jstr(m, "dest_service_name")); svc != "" {
		f.DstHost = svc
		if ns := dash(jstr(m, "dest_service_namespace")); ns != "" {
			f.DstHost += "." + ns + ".svc.cluster.local"
		}
		if p := jint(m, "dest_service_port_num"); p > 0 {
			f.DstPort = p
		}
	}
	switch d := jget(m, "dest_domains").(type) {
	case []any:
		if len(d) > 0 {
			f.DstHost, _ = d[0].(string)
		}
	case string:
		if d != "" && d != "-" {
			f.DstHost = strings.Split(d, ",")[0]
		}
	}
	if n := jint(m, "num_flows"); n > 0 {
		f.Count = n
	}
	return f, true
}
