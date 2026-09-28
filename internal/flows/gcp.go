package flows

import "encoding/json"

// ParseGCP converts one GCP VPC flow log (or firewall rules log) entry as
// exported from Cloud Logging: the LogEntry with jsonPayload, or the payload
// alone.
//
//	{"jsonPayload":{"connection":{"src_ip":"10.8.0.14","src_port":41234,"dest_ip":"10.8.1.3","dest_port":8080,"protocol":6},
//	  "reporter":"SRC","src_gke_details":{"pod":{"pod_name":"web-6d4","pod_namespace":"shop"}},
//	  "dest_gke_details":{"pod":{"pod_name":"api-7f9","pod_namespace":"shop"}}},
//	 "logName":"projects/p/logs/compute.googleapis.com%2Fvpc_flows"}
//
// Both ends of a flow can report it; a DEST-reported record whose source is
// also a VM or pod in the VPC is skipped so the flow counts once.
func ParseGCP(line []byte) (Flow, bool) {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return Flow{}, false
	}
	return gcpEntry(m)
}

func gcpEntry(m map[string]any) (Flow, bool) {
	p := jobj(m, "jsonPayload")
	if p == nil {
		p = m
	}
	c := jobj(p, "connection")
	if c == nil || jstr(c, "src_ip") == "" {
		return Flow{}, false
	}
	srcKnown := jobj(p, "src_instance") != nil || jobj(p, "src_gke_details") != nil
	if jstr(p, "reporter") == "DEST" && srcKnown && jobj(p, "disposition") == nil && jstr(p, "disposition") == "" {
		return Flow{}, false
	}
	f := Flow{Source: FmtGCP, Count: 1, SrcIP: jstr(c, "src_ip"), DstIP: jstr(c, "dest_ip"), DstPort: jint(c, "dest_port"),
		Protocol: ProtoName(jstr(c, "protocol"))}
	if pod := jobj(jobj(p, "src_gke_details"), "pod"); pod != nil {
		f.SrcNamespace, f.SrcPod = jstr(pod, "pod_namespace"), jstr(pod, "pod_name")
	} else if vm := jobj(p, "src_instance"); vm != nil {
		f.SrcPod = jstr(vm, "vm_name")
	}
	if pod := jobj(jobj(p, "dest_gke_details"), "pod"); pod != nil {
		f.DstNamespace, f.DstWorkload = jstr(pod, "pod_namespace"), jstr(pod, "pod_name")
	} else if vm := jobj(p, "dest_instance"); vm != nil {
		f.DstWorkload = jstr(vm, "vm_name")
	}
	if svcs, ok := jget(jobj(p, "dest_gke_details"), "service").([]any); ok && len(svcs) > 0 {
		if s, ok := svcs[0].(map[string]any); ok && jstr(s, "service_name") != "" {
			f.DstHost = jstr(s, "service_name") + "." + jstr(s, "service_namespace") + ".svc.cluster.local"
		}
	}
	if fq := jstr(p, "dest_fqdn"); fq != "" {
		f.DstHost = fq
	}
	f.Verdict = verdictOf(jstr(p, "disposition")) // firewall rules logging: ALLOWED / DENIED
	return f, true
}
