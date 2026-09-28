package importer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
	_ "github.com/tjorourke/flightbay/internal/store/duckdb"
)

// TestImportScale imports a large bundle (from the collector's scale test)
// into a DuckDB workspace on disk, as flightbay-desk open does.
//
//	FLIGHTBAY_SCALE_BUNDLE=/tmp/scale.tar.zst go test ./internal/importer -run TestImportScale -v -timeout 60m
func TestImportScale(t *testing.T) {
	path := os.Getenv("FLIGHTBAY_SCALE_BUNDLE")
	if path == "" {
		t.Skip("set FLIGHTBAY_SCALE_BUNDLE to a bundle to import")
	}
	ws, err := store.Open(filepath.Join(t.TempDir(), "ws.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prof, _ := os.Create(path + ".import.cpu.pprof")
	_ = pprof.StartCPUProfile(prof)
	var peak uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapInuse)
			}
		}
	}()
	start := time.Now()
	if err := Run(context.Background(), f, ws, Options{Source: path}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	close(stop)
	pprof.StopCPUProfile()
	prof.Close()
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	st := ws.State()
	t.Logf("imported in %s: %d objects, %d log lines (%.0f lines/s), %d events, %d members; Go heap peak %.0f MiB, live after %.0f MiB",
		elapsed.Round(time.Millisecond), st.Objects, st.LogLines, float64(st.LogLines)/elapsed.Seconds(), st.Events, st.Members, float64(peak)/(1<<20), float64(ms.HeapInuse)/(1<<20))
	s := time.Now()
	res, err := ws.SearchLogs(store.LogFilter{Query: `level:error`, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("search level:error: %d matches in %s", res.Total, time.Since(s).Round(time.Millisecond))
}
