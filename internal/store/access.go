package store

// AccessEdge is traffic seen in mesh access logs between a source pod and a
// destination Service, aggregated over the capture.
type AccessEdge struct {
	Cluster      string   `json:"cluster"`
	SrcNamespace string   `json:"srcNamespace"`
	SrcPod       string   `json:"srcPod"`
	SrcIdentity  string   `json:"srcIdentity"`
	DstService   string   `json:"dstService"` // host, e.g. name.ns.svc.cluster.local, or an external host
	DstNamespace string   `json:"dstNamespace"`
	DstWorkload  string   `json:"dstWorkload"`
	Methods      []string `json:"methods"`
	Paths        []string `json:"paths"` // up to five examples
	Requests     int      `json:"requests"`
	Denied       int      `json:"denied"` // 401/403 responses or policy rejections
}

// AccessEdges aggregates ztunnel (and waypoint) access log lines. ztunnel
// writes one JSON line per connection (scope "access") or per HTTP request
// (scope "http_access", Solo builds) with src.* and dst.* fields.
func (w *Workspace) AccessEdges(cluster string) []AccessEdge {
	rows, err := w.ldb.db.Query(`
	  SELECT cluster,
	         coalesce(fields['src.namespace'], ''), coalesce(fields['src.workload'], ''), coalesce(fields['src.identity'], ''),
	         coalesce(fields['dst.service'], fields['dst.hbone_addr'], fields['dst.addr'], ''),
	         coalesce(fields['dst.namespace'], ''), coalesce(fields['dst.workload'], ''),
	         list_sort(list_distinct(list(fields['method']) FILTER (WHERE fields['method'] IS NOT NULL))),
	         list_slice(list_distinct(list(fields['path']) FILTER (WHERE fields['path'] IS NOT NULL)), 1, 5),
	         count(*),
	         count(*) FILTER (WHERE fields['response_code'] IN ('401', '403') OR coalesce(fields['error'], '') ILIKE '%policy%')
	  FROM log_lines
	  WHERE scope IN ('access', 'http_access') AND fields['src.workload'] IS NOT NULL AND (? = '' OR cluster = ?)
	  GROUP BY ALL`, cluster, cluster)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []AccessEdge
	for rows.Next() {
		var e AccessEdge
		var methods, paths []any
		if rows.Scan(&e.Cluster, &e.SrcNamespace, &e.SrcPod, &e.SrcIdentity, &e.DstService, &e.DstNamespace, &e.DstWorkload, &methods, &paths, &e.Requests, &e.Denied) != nil {
			continue
		}
		for _, x := range methods {
			if v, ok := x.(string); ok {
				e.Methods = append(e.Methods, v)
			}
		}
		for _, x := range paths {
			if v, ok := x.(string); ok {
				e.Paths = append(e.Paths, v)
			}
		}
		out = append(out, e)
	}
	return out
}
