package flows

import "strings"

// awsDefaultFields is the default (version 2) VPC flow log format.
var awsDefaultFields = []string{"version", "account-id", "interface-id", "srcaddr", "dstaddr", "srcport", "dstport",
	"protocol", "packets", "bytes", "start", "end", "action", "log-status"}

// AWSParser reads AWS VPC flow log records. Records in the default format
// need no header; a custom format is recognised from a header line naming the
// fields (as written to S3: "version account-id ... srcaddr dstaddr ...", or
// the format string itself: "${version} ${srcaddr} ...").
type AWSParser struct{ fields []string }

// awsHeader returns the field names when line is a header.
func awsHeader(line string) []string {
	var out []string
	for _, t := range strings.Fields(line) {
		t = strings.TrimSuffix(strings.TrimPrefix(t, "${"), "}")
		out = append(out, strings.ReplaceAll(t, "_", "-"))
	}
	has := map[string]bool{}
	for _, t := range out {
		has[t] = true
	}
	if has["srcaddr"] && has["dstaddr"] || has["pkt-srcaddr"] && has["pkt-dstaddr"] {
		return out
	}
	return nil
}

// Line parses one record. The header line itself, NODATA and SKIPDATA
// records return ok=false.
//
//	2 123456789010 eni-1235b8ca123456789 172.31.16.139 172.31.16.21 20641 22 6 20 4249 1418530010 1418530070 ACCEPT OK
func (p *AWSParser) Line(line string) (Flow, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Flow{}, false
	}
	if h := awsHeader(line); h != nil {
		p.fields = h
		return Flow{}, false
	}
	fields := p.fields
	if fields == nil {
		fields = awsDefaultFields
	}
	vals := strings.Fields(line)
	if len(vals) != len(fields) {
		return Flow{}, false
	}
	r := map[string]string{}
	for i, k := range fields {
		r[k] = dash(vals[i])
	}
	if st := r["log-status"]; st == "NODATA" || st == "SKIPDATA" {
		return Flow{}, false
	}
	f := Flow{Source: FmtAWS, Count: 1, SrcIP: first(r["pkt-srcaddr"], r["srcaddr"]), DstIP: first(r["pkt-dstaddr"], r["dstaddr"]),
		DstPort: atoi(r["dstport"]), Protocol: ProtoName(r["protocol"]), Verdict: verdictOf(r["action"])}
	if svc := r["pkt-dst-aws-service"]; svc != "" {
		f.DstHost = strings.ToLower(svc) + ".amazonaws.com"
	}
	if !isIP(f.SrcIP) || !isIP(f.DstIP) {
		return Flow{}, false
	}
	return f, true
}
