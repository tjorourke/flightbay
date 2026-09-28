package store

import (
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/flows"
)

// Flow is one observed connection or request, normalised from any source:
// ztunnel, agentgateway, Envoy (waypoints and sidecars), CoreDNS query logs,
// or flow logs imported alongside the bundle (Cilium Hubble, Calico, AWS, GCP
// and Azure VPC flow logs, or a generic CSV/JSON). Endpoints are named when
// the source names them and otherwise carried as IPs; internal/reach resolves
// IPs to workloads from the captured pod, Service and node addresses.
//
// The type lives in internal/flows, where the parsers are; this alias keeps
// store.Flow as the name the rest of Flightbay codes against.
type Flow = flows.Flow

// flowsSchema holds flows that are not queried straight out of log_lines:
// Envoy and CoreDNS flows parsed from log lines as they are loaded, and flow
// log files attached to the bundle or added when it was opened. Rows are
// aggregated per log chunk or file, so a flow can have several rows; Flows()
// sums them.
const flowsSchema = `
CREATE TABLE IF NOT EXISTS flows (
  cluster VARCHAR, source VARCHAR,
  src_ip VARCHAR, src_namespace VARCHAR, src_pod VARCHAR, src_identity VARCHAR,
  dst_ip VARCHAR, dst_port INTEGER, dst_host VARCHAR, dst_namespace VARCHAR, dst_workload VARCHAR,
  protocol VARCHAR, method VARCHAR, path VARCHAR, tool VARCHAR, route VARCHAR, via VARCHAR, verdict VARCHAR,
  direction VARCHAR, count BIGINT, file VARCHAR, origin VARCHAR);`

const flowCols = "cluster, source, src_ip, src_namespace, src_pod, src_identity, dst_ip, dst_port, dst_host, dst_namespace, dst_workload, protocol, method, path, tool, route, via, verdict, direction, file, origin"

// Origins of a flow row.
const (
	OriginLogs     = "logs"
	OriginBundle   = "bundle"
	OriginReviewer = "reviewer"
)

// AddFlows appends flows to the flows table.
func (w *Workspace) AddFlows(fs []Flow) error {
	rows := make([][]driver.Value, 0, len(fs))
	for _, f := range fs {
		rows = append(rows, []driver.Value{f.Cluster, f.Source, f.SrcIP, f.SrcNamespace, f.SrcPod, f.SrcIdentity,
			f.DstIP, int32(f.DstPort), f.DstHost, f.DstNamespace, f.DstWorkload, f.Protocol, f.Method, f.Path, f.Tool, f.Route, f.Via, f.Verdict,
			f.Direction, int64(f.Count), f.File, f.Origin})
	}
	return w.ldb.appendRows("flows", rows)
}

// FlowFileResult is what ImportFlowFile loaded.
type FlowFileResult struct {
	File   string `json:"file"`
	Format string `json:"format"`
	Flows  int    `json:"flows"`   // aggregated rows
	Events int    `json:"records"` // records they stand for
}

// ImportFlowFile parses a flow log file (format "" to detect it) and adds its
// flows under cluster, marked with the file name and origin (OriginBundle for
// a file inside the signed bundle, OriginReviewer for one added at open).
func (w *Workspace) ImportFlowFile(cluster, name string, r io.Reader, format, origin string) (FlowFileResult, error) {
	res := FlowFileResult{File: name}
	fs, format, err := flows.ParseFile(r, format)
	res.Format = format
	if err != nil && len(fs) == 0 {
		return res, err
	}
	for i := range fs {
		if fs[i].Cluster == "" {
			fs[i].Cluster = cluster
		}
		fs[i].File, fs[i].Origin = name, origin
		res.Events += fs[i].Count
	}
	res.Flows = len(fs)
	if aerr := w.AddFlows(fs); aerr != nil {
		return res, aerr
	}
	w.mu.Lock()
	w.commit()
	w.mu.Unlock()
	return res, err
}

// AddReviewerFlows imports a flow file chosen by the reviewer when opening
// the bundle and lists it on the Coverage page, marked as not part of the
// signed bundle.
func (w *Workspace) AddReviewerFlows(cluster, name string, r io.Reader, format string) (FlowFileResult, error) {
	res, err := w.ImportFlowFile(cluster, name, r, format, OriginReviewer)
	cov := bundle.Coverage{ID: cluster + ".flows.reviewer." + bundle.SafeID(name), Cluster: cluster, Type: bundle.CoverageFlows,
		Resource: name, Kind: res.Format, Items: res.Events, State: bundle.StateComplete,
		Reason: res.Format + " flow log added by the reviewer when opening the bundle; not part of the signed bundle"}
	if err != nil {
		cov.State, cov.Reason = bundle.StateFailed, cov.Reason+": "+err.Error()
	}
	w.SetCoverage(cov)
	return res, err
}

// envoyPodLocked classifies the pod a proxy log line came from. Gateways and
// waypoints carry traffic for someone else, so their outbound lines name the
// downstream client as the source, not the pod. Callers hold w.mu.
func (w *Workspace) envoyPodLocked(cluster, ns, pod, wname string) flows.EnvoyPod {
	ep := flows.EnvoyPod{Namespace: ns, Pod: pod}
	if o := w.objects[objKey(cluster, "", "pods", ns, pod)]; o != nil {
		l := o.Labels()
		for _, k := range []string{"gateway.networking.k8s.io/gateway-name", "istio.io/gateway-name"} {
			if g := l[k]; g != "" {
				ep.Gateway, ep.Via = true, ns+"/"+g
				return ep
			}
		}
		switch l["istio"] {
		case "ingressgateway", "egressgateway", "eastwestgateway":
			ep.Gateway = true
		}
		if l["gateway.istio.io/managed"] != "" || strings.Contains(l["app"], "gateway") {
			ep.Gateway = true
		}
	} else if strings.Contains(pod, "gateway") || strings.Contains(pod, "waypoint") {
		ep.Gateway = true
	}
	if ep.Gateway {
		ep.Via = ns + "/" + wname
	}
	return ep
}

// flowsFromLines derives Envoy and CoreDNS flows from one chunk of log lines,
// aggregated. It runs as the chunk is loaded, so Flows() never re-parses log
// text: the cost is one cheap check per line, and a parse only for lines
// that look like an Envoy access log or a CoreDNS query log.
func flowsFromLines(cluster, container string, ep flows.EnvoyPod, namespaces []string, lines []string, parsed func(i int) (scope string, fields map[string]string, rest string)) []Flow {
	var out []Flow
	dns := strings.Contains(container, "coredns") || strings.HasPrefix(ep.Pod, "coredns")
	for i := range lines {
		scope, fields, rest := parsed(i)
		switch {
		case scope == "access" && fields["response_flags"] != "" && fields["src.workload"] == "",
			fields["upstream_cluster"] != "" && fields["downstream_remote_address"] != "":
			if f, ok := flows.ParseEnvoy(rest, ep); ok {
				f.Cluster, f.Origin = cluster, OriginLogs
				out = append(out, f)
			}
		case dns || strings.HasPrefix(rest, "[INFO] ") && strings.Contains(rest, `" NOERROR `):
			if f, ok := flows.ParseCoreDNS(rest, namespaces...); ok {
				f.Cluster, f.Origin = cluster, OriginLogs
				out = append(out, f)
			}
		}
	}
	return flows.Aggregate(out)
}

// namespacesLocked lists the captured namespaces of a cluster: bare
// service.namespace DNS lookups for them are search-path noise, not
// external names. Callers hold w.mu.
func (w *Workspace) namespacesLocked(cluster string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, o := range w.byType[typeKey{cluster, "", "namespaces"}] {
		add(o.Name)
	}
	for _, o := range w.byType[typeKey{cluster, "", "pods"}] {
		add(o.Namespace)
	}
	return out
}

// Flows returns every observed flow for a cluster ("" for all), from logs in
// the capture and any flow logs imported with it.
func (w *Workspace) Flows(cluster string) []Flow {
	out := w.ztunnelFlows(cluster)
	out = append(out, w.agentgatewayFlows(cluster)...)
	out = append(out, w.tableFlows(cluster, "")...)
	return out
}

// FlowsFrom returns flows for one source ("" for all).
func (w *Workspace) FlowsFrom(cluster, source string) []Flow {
	switch source {
	case "":
		return w.Flows(cluster)
	case flows.SrcZtunnel:
		return w.ztunnelFlows(cluster)
	case flows.SrcAgentgateway:
		return w.agentgatewayFlows(cluster)
	}
	return w.tableFlows(cluster, source)
}

// ztunnelFlows aggregates ztunnel access logs per source, destination and
// HTTP method. Both ends of an in-mesh connection log it, and behind a
// waypoint the destination's line names the waypoint rather than the
// caller: each side is counted on its own and the larger taken, so a
// connection counts once whether one end, both ends or only the caller's end
// was captured.
func (w *Workspace) ztunnelFlows(cluster string) []Flow {
	rows, err := w.ldb.db.Query(`
	  SELECT cluster,
	         coalesce(fields['src.namespace'], ''), coalesce(fields['src.workload'], ''), coalesce(fields['src.identity'], ''),
	         coalesce(fields['dst.service'], fields['dst.hbone_addr'], fields['dst.addr'], ''),
	         coalesce(fields['dst.namespace'], ''), coalesce(fields['dst.workload'], ''),
	         coalesce(fields['method'], ''),
	         greatest(count(*) FILTER (WHERE fields['direction'] = 'outbound'), count(*) FILTER (WHERE coalesce(fields['direction'], '') <> 'outbound')),
	         greatest(count(*) FILTER (WHERE fields['direction'] = 'outbound' AND (fields['response_code'] IN ('401', '403') OR coalesce(fields['error'], '') ILIKE '%policy%')),
	                  count(*) FILTER (WHERE coalesce(fields['direction'], '') <> 'outbound' AND (fields['response_code'] IN ('401', '403') OR coalesce(fields['error'], '') ILIKE '%policy%')))
	  FROM log_lines
	  WHERE scope IN ('access', 'http_access') AND fields['src.workload'] IS NOT NULL AND (? = '' OR cluster = ?)
	  GROUP BY ALL`, cluster, cluster)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Flow
	for rows.Next() {
		var f Flow
		var denied int
		if rows.Scan(&f.Cluster, &f.SrcNamespace, &f.SrcPod, &f.SrcIdentity, &f.DstHost, &f.DstNamespace, &f.DstWorkload, &f.Method, &f.Count, &denied) != nil {
			continue
		}
		if i := strings.LastIndex(f.DstHost, ":"); i > 0 && !strings.Contains(f.DstHost[i:], "]") {
			if p, err := strconv.Atoi(f.DstHost[i+1:]); err == nil {
				f.DstPort, f.DstHost = p, f.DstHost[:i]
			}
		}
		f.Source, f.Protocol, f.Origin = flows.SrcZtunnel, "tcp", OriginLogs
		if f.Method != "" {
			f.Protocol = "http"
		}
		if denied > 0 {
			// A mix of allowed and denied lines on one edge: report the denials
			// as their own flow so neither count is lost.
			if denied < f.Count {
				d := f
				d.Count, d.Verdict = denied, flows.Denied
				out = append(out, d)
				f.Count -= denied
			} else {
				f.Verdict = flows.Denied
			}
		}
		out = append(out, f)
	}
	return out
}

// agentgatewayFlows groups agentgateway request logs (scope "request", text
// or JSON) by the fields a flow keeps, then converts each group. Ephemeral
// source ports and query strings are dropped in SQL so the groups stay few.
func (w *Workspace) agentgatewayFlows(cluster string) []Flow {
	var sel []string
	for _, k := range flows.AgentgatewayFields {
		expr := fmt.Sprintf("coalesce(fields['%s'], '')", k)
		switch k {
		case "src.addr":
			expr = fmt.Sprintf(`regexp_replace(%s, ':[0-9]+$', '')`, expr)
		case "http.path":
			expr = fmt.Sprintf(`split_part(%s, '?', 1)`, expr)
		case "http.status":
			expr = `CASE WHEN fields['http.status'] IN ('401', '403') THEN fields['http.status'] ELSE '' END`
		}
		sel = append(sel, expr)
	}
	rows, err := w.ldb.db.Query(`SELECT cluster, `+strings.Join(sel, ", ")+`, count(*) FROM log_lines
	  WHERE scope = 'request' AND (fields['gateway'] IS NOT NULL OR fields['src.addr'] IS NOT NULL) AND (? = '' OR cluster = ?)
	  GROUP BY ALL`, cluster, cluster)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Flow
	vals := make([]string, len(flows.AgentgatewayFields))
	for rows.Next() {
		var c string
		var n int
		ptrs := []any{&c}
		for i := range vals {
			ptrs = append(ptrs, &vals[i])
		}
		ptrs = append(ptrs, &n)
		if rows.Scan(ptrs...) != nil {
			continue
		}
		m := make(map[string]string, len(vals))
		for i, k := range flows.AgentgatewayFields {
			if vals[i] != "" {
				m[k] = vals[i]
			}
		}
		if f, ok := flows.FromAgentgateway(m); ok {
			f.Cluster, f.Count, f.Origin = c, n, OriginLogs
			out = append(out, f)
		}
	}
	return flows.Aggregate(out)
}

// tableFlows sums the flows table (Envoy, CoreDNS and imported flow logs).
func (w *Workspace) tableFlows(cluster, source string) []Flow {
	rows, err := w.ldb.db.Query(`SELECT `+flowCols+`, sum(count)::BIGINT FROM flows
	  WHERE (? = '' OR cluster = ?) AND (? = '' OR source = ?) GROUP BY ALL`, cluster, cluster, source, source)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Flow
	for rows.Next() {
		var f Flow
		var port int32
		var n int64
		if rows.Scan(&f.Cluster, &f.Source, &f.SrcIP, &f.SrcNamespace, &f.SrcPod, &f.SrcIdentity, &f.DstIP, &port, &f.DstHost, &f.DstNamespace, &f.DstWorkload,
			&f.Protocol, &f.Method, &f.Path, &f.Tool, &f.Route, &f.Via, &f.Verdict, &f.Direction, &f.File, &f.Origin, &n) != nil {
			continue
		}
		f.DstPort, f.Count = int(port), int(n)
		out = append(out, f)
	}
	return out
}
