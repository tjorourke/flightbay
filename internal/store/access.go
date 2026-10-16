package store

import "time"

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

	DstIdentity string `json:"dstIdentity,omitempty"`
	// HBONE counts connections carried over HBONE (mutual TLS between
	// ztunnels, port 15008); Plaintext counts the rest. Both are per side, the
	// larger taken, like Requests.
	HBONE     int       `json:"hbone"`
	Plaintext int       `json:"plaintext"`
	BytesSent int64     `json:"bytesSent,omitempty"` // by the source
	BytesRecv int64     `json:"bytesRecv,omitempty"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
	// Connections counts connection lines (scope access) and HTTPRequests
	// request lines (scope http_access in some ztunnel builds); Requests is both.
	Connections  int `json:"connections"`
	HTTPRequests int `json:"httpRequests"`
}

// AccessEdges aggregates ztunnel (and waypoint) access log lines. ztunnel
// writes one JSON line per connection (scope "access") or per HTTP request
// (scope "http_access" in some builds) with src.* and dst.* fields.
func (w *Workspace) AccessEdges(cluster string) []AccessEdge {
	rows, err := w.ldb.db.Query(`
	  SELECT cluster,
	         coalesce(fields['src.namespace'], ''), coalesce(fields['src.workload'], ''), coalesce(fields['src.identity'], ''),
	         coalesce(fields['dst.service'], fields['dst.hbone_addr'], fields['dst.addr'], ''),
	         coalesce(fields['dst.namespace'], ''), coalesce(fields['dst.workload'], ''),
	         list_sort(list_distinct(list(fields['method']) FILTER (WHERE fields['method'] IS NOT NULL))),
	         list_slice(list_distinct(list(split_part(fields['path'], '?', 1)) FILTER (WHERE fields['path'] IS NOT NULL)), 1, 5),
	         -- Both ends of an in-mesh connection log it, and behind a waypoint the
	         -- destination's line names the waypoint rather than the caller. Count
	         -- each side on its own and take the larger, so a connection counts
	         -- once whether one end, both ends or only the caller's end was captured.
	         greatest(count(*) FILTER (WHERE fields['direction'] = 'outbound'), count(*) FILTER (WHERE coalesce(fields['direction'], '') <> 'outbound')),
	         greatest(count(*) FILTER (WHERE fields['direction'] = 'outbound' AND (fields['response_code'] IN ('401', '403') OR coalesce(fields['error'], '') ILIKE '%policy%')),
	                  count(*) FILTER (WHERE coalesce(fields['direction'], '') <> 'outbound' AND (fields['response_code'] IN ('401', '403') OR coalesce(fields['error'], '') ILIKE '%policy%'))),
	         coalesce(any_value(fields['dst.identity']) FILTER (WHERE fields['dst.identity'] IS NOT NULL), ''),
	         -- HBONE: ztunnel names the tunnel (dst.hbone_addr) or the connection went to 15008.
	         greatest(count(*) FILTER (WHERE fields['direction'] = 'outbound' AND (fields['dst.hbone_addr'] IS NOT NULL OR fields['dst.addr'] LIKE '%:15008')),
	                  count(*) FILTER (WHERE coalesce(fields['direction'], '') <> 'outbound' AND (fields['dst.hbone_addr'] IS NOT NULL OR fields['dst.addr'] LIKE '%:15008'))),
	         greatest(count(*) FILTER (WHERE fields['direction'] = 'outbound' AND fields['dst.hbone_addr'] IS NULL AND coalesce(fields['dst.addr'], '') NOT LIKE '%:15008'),
	                  count(*) FILTER (WHERE coalesce(fields['direction'], '') <> 'outbound' AND fields['dst.hbone_addr'] IS NULL AND coalesce(fields['dst.addr'], '') NOT LIKE '%:15008')),
	         -- Bytes from the source's side when it logged, else the destination's (sent and received swap).
	         CASE WHEN count(*) FILTER (WHERE fields['direction'] = 'outbound') > 0
	              THEN coalesce(sum(try_cast(fields['bytes_sent'] AS BIGINT)) FILTER (WHERE fields['direction'] = 'outbound'), 0)
	              ELSE coalesce(sum(try_cast(fields['bytes_recv'] AS BIGINT)), 0) END::BIGINT,
	         CASE WHEN count(*) FILTER (WHERE fields['direction'] = 'outbound') > 0
	              THEN coalesce(sum(try_cast(fields['bytes_recv'] AS BIGINT)) FILTER (WHERE fields['direction'] = 'outbound'), 0)
	              ELSE coalesce(sum(try_cast(fields['bytes_sent'] AS BIGINT)), 0) END::BIGINT,
	         coalesce(min(ts) FILTER (WHERE ts > 0), 0), coalesce(max(ts), 0),
	         -- Some ztunnel builds log a line per connection (access) and one per
	         -- HTTP request (http_access); count them apart.
	         greatest(count(*) FILTER (WHERE scope = 'access' AND fields['direction'] = 'outbound'), count(*) FILTER (WHERE scope = 'access' AND coalesce(fields['direction'], '') <> 'outbound')),
	         greatest(count(*) FILTER (WHERE scope = 'http_access' AND fields['direction'] = 'outbound'), count(*) FILTER (WHERE scope = 'http_access' AND coalesce(fields['direction'], '') <> 'outbound'))
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
		// The lists are NULL on connection-level lines (upstream ztunnel
		// "access" lines carry no method or path), and NULL does not scan
		// into a []any: scan into any, or every L4 edge is dropped.
		var methodsV, pathsV any
		var first, last int64
		if rows.Scan(&e.Cluster, &e.SrcNamespace, &e.SrcPod, &e.SrcIdentity, &e.DstService, &e.DstNamespace, &e.DstWorkload, &methodsV, &pathsV, &e.Requests, &e.Denied,
			&e.DstIdentity, &e.HBONE, &e.Plaintext, &e.BytesSent, &e.BytesRecv, &first, &last, &e.Connections, &e.HTTPRequests) != nil {
			continue
		}
		if first > 0 {
			e.First = time.Unix(0, first).UTC()
		}
		if last > 0 {
			e.Last = time.Unix(0, last).UTC()
		}
		methods, _ := methodsV.([]any)
		paths, _ := pathsV.([]any)
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
