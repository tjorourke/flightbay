package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestScale loads FLIGHTBAY_SCALE synthetic log lines (default skipped) and
// times representative searches. Phase 3 target: 10M lines, under 1s.
//
//	FLIGHTBAY_SCALE=10000000 go test ./internal/store -run TestScale -v -timeout 30m
func TestScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("FLIGHTBAY_SCALE"))
	if n == 0 {
		t.Skip("set FLIGHTBAY_SCALE to run")
	}
	dbPath := filepath.Join(t.TempDir(), "scale.duckdb")
	ws, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	formats := []string{
		`[%s] "GET /api/v1/orders/%d HTTP/1.1" 200 - via_upstream - "-" 0 1043 12 11 "-" "curl/8.0" "a4e98f02-%d" "checkout.payments.svc:8080" "10.244.1.%d:8080" outbound|8080||checkout.payments.svc.cluster.local`,
		`{"level":"info","time":"%s","msg":"reconciled gateway","gateway":"ai-gateway-%d","trace_id":"t%d","duration_ms":%d}`,
		`%s	info	xds	Pushing proxy %d version %d resources %d`,
		`I0927 %s 1 controller.go:%d] Reconciler error for pod-%d: context deadline exceeded after %dms`,
		`%s ERROR database connection timed out host=postgres.payments.svc:5432 attempt=%d of %d id=%d`,
	}
	var ms0 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	start := time.Now()
	const chunk = 5000
	pods := 400
	lines := make([]string, 0, chunk)
	for i := 0; i < n; i += chunk {
		lines = lines[:0]
		pod := (i / chunk) % pods
		for j := 0; j < chunk && i+j < n; j++ {
			k := i + j
			ts := base.Add(time.Duration(k) * 8 * time.Millisecond)
			f := formats[k%len(formats)]
			lines = append(lines, ts.Format(time.RFC3339Nano)+" "+fmt.Sprintf(f, ts.Format("15:04:05.000000"), k%9973, k, k%251))
		}
		ws.QueueLogLines("mesh1", fmt.Sprintf("ns-%d", pod%20), fmt.Sprintf("pod-%d", pod), "app", "current", append([]string(nil), lines...), fmt.Sprintf("c%d", i), nil)
	}
	ws.WaitLogs() // as the importer does before reporting complete
	load := time.Since(start)
	runtime.GC()
	var ms1 runtime.MemStats
	runtime.ReadMemStats(&ms1)
	var disk int64
	for _, p := range []string{dbPath, dbPath + ".wal"} {
		if fi, err := os.Stat(p); err == nil {
			disk += fi.Size()
		}
	}
	t.Logf("loaded %d lines in %s (%.0f lines/s); Go heap %.0f MiB; DuckDB file %.2f GiB (%.0f bytes/line)", n, load.Round(time.Millisecond), float64(n)/load.Seconds(),
		float64(int64(ms1.HeapAlloc)-int64(ms0.HeapAlloc))/(1<<20), float64(disk)/(1<<30), float64(disk)/float64(n))
	for _, q := range []string{`"connection timed out"`, `level:error`, `trace_id:t12345`, `ns:ns-3 pod:pod-63 "reconciled"`, ``, `outbound -healthz`} {
		best := time.Hour
		var total int
		for rep := 0; rep < 3; rep++ {
			s := time.Now()
			res, err := ws.SearchLogs(LogFilter{Query: q, Limit: 200})
			if err != nil {
				t.Fatal(err)
			}
			if d := time.Since(s); d < best {
				best = d
			}
			total = res.Total
		}
		t.Logf("query %-34q total=%-9d best of 3: %s", q, total, best.Round(time.Millisecond))
	}
}
