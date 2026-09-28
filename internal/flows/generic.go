package flows

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"
)

// genericColumns maps accepted column names (lower case, with _ - . and
// spaces removed, so src_ip, srcIp and "Src IP" are the same) to Flow fields.
// This is the escape hatch for any CNI or firewall: Antrea, Weave,
// kube-router, a firewall export or a SIEM query can all be written as CSV or
// JSON Lines with these columns.
var genericColumns = map[string]string{
	"srcip": "src_ip", "sourceip": "src_ip", "srcaddr": "src_ip", "sourceaddress": "src_ip", "clientip": "src_ip",
	"dstip": "dst_ip", "destip": "dst_ip", "dstaddr": "dst_ip", "destinationip": "dst_ip", "destinationaddress": "dst_ip",
	"dstport": "dst_port", "destport": "dst_port", "destinationport": "dst_port",
	"srcnamespace": "src_namespace", "sourcenamespace": "src_namespace",
	"srcpod": "src_pod", "sourcepod": "src_pod", "srcworkload": "src_pod", "sourceworkload": "src_pod",
	"srcidentity": "src_identity", "sourceidentity": "src_identity", "principal": "src_identity",
	"dstnamespace": "dst_namespace", "destnamespace": "dst_namespace", "destinationnamespace": "dst_namespace",
	"dstpod": "dst_workload", "destpod": "dst_workload", "dstworkload": "dst_workload", "destworkload": "dst_workload", "destinationpod": "dst_workload",
	"dsthost": "dst_host", "desthost": "dst_host", "destdomain": "dst_host", "dstdomain": "dst_host", "fqdn": "dst_host", "host": "dst_host", "sni": "dst_host", "destinationhost": "dst_host",
	"protocol": "protocol", "proto": "protocol",
	"method": "method", "path": "path", "url": "path", "tool": "tool", "route": "route", "via": "via",
	"action": "verdict", "verdict": "verdict", "decision": "verdict", "disposition": "verdict",
	"count": "count", "packets": "count", "connections": "count", "hits": "count",
	"cluster": "cluster",
}

func normCol(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(s)
}

// genericFlow builds a flow from column name → value.
func genericFlow(row map[string]string) (Flow, bool) {
	v := map[string]string{}
	for k, x := range row {
		if f, ok := genericColumns[normCol(k)]; ok && strings.TrimSpace(x) != "" && v[f] == "" {
			v[f] = dash(strings.TrimSpace(x))
		}
	}
	f := Flow{Source: FmtGeneric, Cluster: v["cluster"], SrcIP: v["src_ip"], SrcNamespace: v["src_namespace"], SrcPod: v["src_pod"], SrcIdentity: v["src_identity"],
		DstIP: v["dst_ip"], DstPort: atoi(v["dst_port"]), DstHost: v["dst_host"], DstNamespace: v["dst_namespace"], DstWorkload: v["dst_workload"],
		Protocol: ProtoName(v["protocol"]), Method: v["method"], Path: StripQuery(v["path"]), Tool: v["tool"], Route: v["route"], Via: v["via"],
		Verdict: verdictOf(v["verdict"]), Count: atoi(v["count"])}
	if f.Count <= 0 {
		f.Count = 1
	}
	if strings.Contains(f.Path, "://") { // a full URL in a url column
		rest := f.Path[strings.Index(f.Path, "://")+3:]
		if i := strings.Index(rest, "/"); i >= 0 {
			if f.DstHost == "" {
				f.DstHost, _ = SplitHostPort(rest[:i])
			}
			f.Path = rest[i:]
		} else {
			f.Path = "/"
		}
	}
	src := f.SrcIP != "" || f.SrcPod != "" || f.SrcNamespace != "" || f.SrcIdentity != ""
	dst := f.DstIP != "" || f.DstHost != "" || f.DstWorkload != "" || f.DstNamespace != ""
	return f, src && dst
}

// ParseGenericJSON converts one JSON Lines record with generic column names.
//
//	{"src_namespace":"payments","src_pod":"checkout-7d9","dst_host":"api.stripe.com","dst_port":443,"protocol":"tcp","action":"allow","count":12}
func ParseGenericJSON(line []byte) (Flow, bool) {
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return Flow{}, false
	}
	row := map[string]string{}
	for k, x := range m {
		switch t := x.(type) {
		case string:
			row[k] = t
		case float64:
			row[k] = jsonNumber(t)
		case bool:
			if t {
				row[k] = "true"
			}
		}
	}
	return genericFlow(row)
}

// ParseGenericCSV reads a CSV (or TSV) file whose header row names generic
// columns.
//
//	src_ip,src_namespace,src_pod,dst_host,dst_port,protocol,action,count
//	10.244.1.7,payments,checkout-7d9,api.stripe.com,443,tcp,allow,12
func ParseGenericCSV(r io.Reader, comma rune) ([]Flow, error) {
	cr := csv.NewReader(r)
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.Comment = '#'
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	var out []Flow
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		row := map[string]string{}
		for i, h := range head {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		if f, ok := genericFlow(row); ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// genericHeader reports whether a CSV header row names at least a source and
// a destination column.
func genericHeader(line string, comma rune) bool {
	src, dst := false, false
	for _, h := range strings.Split(line, string(comma)) {
		switch genericColumns[normCol(strings.Trim(h, `"`))] {
		case "src_ip", "src_pod", "src_namespace", "src_identity":
			src = true
		case "dst_ip", "dst_host", "dst_workload", "dst_namespace":
			dst = true
		}
	}
	return src && dst
}
