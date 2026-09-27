package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/logs"
)

var sample = []string{
	`2026-09-27T10:00:00Z ERROR database connection timed out host=postgres.payments.svc trace_id=a4e98f02`,
	`2026-09-27T10:00:01Z {"level":"info","msg":"connecting to database","trace_id":"a4e98f02","user":"Alice"}`,
	`2026-09-27T10:00:02Z {"level":"warn","msg":"slow query 100% of 5_000 rows","db":"orders"}`,
	`2026-09-27T10:00:03Z 2026-09-27T10:00:03.1Z	info	xds	Pushing proxy checkout-1.payments`,
	`2026-09-27T10:00:04Z [2026-09-27T10:00:04.000Z] "GET /healthz HTTP/1.1" 200 - via_upstream - "-" 0 2 1 1 "-" "kube-probe"`,
	`2026-09-27T10:00:05Z plain line without structure`,
}

func loaded(t *testing.T) (*Workspace, []*logs.Record) {
	ws := New()
	ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "current", sample[:3], "c1")
	ws.AddLogLines("mesh1", "istio-system", "istiod-1", "discovery", "current", sample[3:], "c2")
	var all []*logs.Record
	for _, s := range [][]string{{"payments", "checkout-1", "app"}, {"istio-system", "istiod-1", "discovery"}} {
		all = append(all, ws.LogStream("mesh1", s[0], s[1], s[2], "current")...)
	}
	if len(all) != len(sample) {
		t.Fatalf("loaded %d of %d lines", len(all), len(sample))
	}
	return ws, all
}

// The query language means the same thing in Go and in SQL.
func TestQueryParityGoAndSQL(t *testing.T) {
	ws, all := loaded(t)
	for _, q := range []string{
		`database`, `"connection timed out"`, `level:error`, `level:warning`, `trace_id:a4e98f02`, `-trace_id:a4e98f02`,
		`user:alice`, `ns:payments`, `pod:istio*`, `database OR healthz`, `-healthz`, `"100%"`, `5_000`, `since:2s`,
		`db:ord*`, `ns:payments -level:error`, `scope:xds`, `DATABASE`,
	} {
		pq, err := logs.ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		pq.Reference = all[len(all)-1].TS
		var want []int64
		for _, r := range all {
			if pq.Match(r) {
				want = append(want, r.ID)
			}
		}
		res, err := ws.SearchLogs(LogFilter{Query: q, Limit: 100})
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		var got []int64
		for _, r := range res.Records {
			got = append(got, r.ID)
		}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		if res.Total != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q: SQL %v (total %d), Go %v", q, got, res.Total, want)
		}
	}
}

func TestSearchFacetsHistogramPaging(t *testing.T) {
	ws, _ := loaded(t)
	res, err := ws.SearchLogs(LogFilter{Query: "", Namespace: "payments", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || len(res.Records) != 2 || res.Records[0].Msg != "slow query 100% of 5_000 rows" {
		t.Fatalf("page: total=%d %+v", res.Total, res.Records)
	}
	if res.Facets["namespace"]["istio-system"] != 3 || res.Facets["level"]["error"] != 1 {
		t.Fatalf("facets must ignore facet filters: %v", res.Facets)
	}
	n := 0
	for _, b := range res.Histogram {
		n += b.Count
	}
	if n != 3 || res.Scanned != 6 {
		t.Fatalf("histogram %d scanned %d", n, res.Scanned)
	}
	if r := res.Records[0]; r.Fields == nil || r.Pod != "checkout-1" || r.Node != "" || r.TS.IsZero() {
		t.Fatalf("record fields lost: %+v", r)
	}
}

func TestStreamsContextAndFollowFrom(t *testing.T) {
	ws, all := loaded(t)
	if got := ws.LogStreamFrom("mesh1", "payments", "checkout-1", "app", "current", 2); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("from: %+v", got)
	}
	ctx := ws.LogContext(all[1].ID, 1)
	if len(ctx) != 3 || ctx[1].ID != all[1].ID {
		t.Fatalf("context: %+v", ctx)
	}
	if !ws.HasStream("mesh1", "payments", "checkout-1", "app", "current") || ws.HasStream("mesh1", "payments", "checkout-1", "app", "previous") {
		t.Fatal("HasStream")
	}
	if s := ws.Streams("mesh1", "payments", "checkout-1"); len(s) != 1 || s[0] != "app/current" {
		t.Fatalf("streams %v", s)
	}
}

func TestSQLIsReadOnlyAndSandboxed(t *testing.T) {
	ws, _ := loaded(t)
	ws.AddObjects("mesh1", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true},
		[]map[string]any{{"kind": "Pod", "metadata": map[string]any{"name": "checkout-1", "namespace": "payments", "labels": map[string]any{"app": "checkout"}}, "spec": map[string]any{"nodeName": "w1"}}}, "o")
	ws.AddEvents("mesh1", []map[string]any{{"reason": "BackOff", "type": "Warning", "message": "Back-off", "involvedObject": map[string]any{"kind": "Pod", "name": "checkout-1"}, "metadata": map[string]any{"namespace": "payments"}}})
	r, err := ws.SQL(`SELECT level, count(*) AS n FROM logs GROUP BY level ORDER BY n DESC, level`, 0)
	if err != nil || len(r.Rows) == 0 || r.Columns[1] != "n" {
		t.Fatalf("select: %v %+v", err, r)
	}
	r, err = ws.SQL(`SELECT name, labels['app'] AS app, object->>'$.spec.nodeName' AS node FROM objects`, 0)
	if err != nil || len(r.Rows) != 1 || r.Rows[0][1] != "checkout" || r.Rows[0][2] != "w1" {
		t.Fatalf("objects: %v %+v", err, r)
	}
	if r, err = ws.SQL(`FROM events SELECT reason, object_name`, 0); err != nil || r.Rows[0][0] != "BackOff" {
		t.Fatalf("events: %v %+v", err, r)
	}
	for _, q := range []string{`DELETE FROM log_lines`, `SELECT 1; DROP TABLE log_lines`, `ATTACH 'x.db'`, `COPY log_lines TO 'out.csv'`, `INSTALL httpfs`} {
		if _, err := ws.SQL(q, 0); !errors.Is(err, ErrNotReadOnly) {
			t.Errorf("%q accepted: %v", q, err)
		}
	}
	if _, err := ws.SQL(`SELECT * FROM read_csv('/etc/hosts')`, 0); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("file access allowed: %v", err)
	}
	// A writing CTE is rolled back even though it starts with WITH.
	_, _ = ws.SQL(`WITH x AS (DELETE FROM log_lines RETURNING 1) SELECT count(*) FROM x`, 0)
	if res, _ := ws.SearchLogs(LogFilter{}); res.Total != 6 {
		t.Fatalf("a query changed the data: %d lines left", res.Total)
	}
	r, _ = ws.SQL(`SELECT * FROM range(5000)`, 10)
	if !r.Truncated || len(r.Rows) != 10 {
		t.Fatalf("limit: %+v", r)
	}
}
