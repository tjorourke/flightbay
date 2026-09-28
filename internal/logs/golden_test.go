package logs

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// parseCorpus covers every format Parse sniffs, and near misses for each.
func parseCorpus() []string {
	c := []string{
		`{"level":"info","msg":"reconciled","gateway":"gw","status":502}`, `{"severity":"ERROR","message":"boom","trace_id":"t1"}`, `{not json`,
		`2026-09-27T10:00:00.000000Z	info	xds	Pushing proxy x version 3 resources=4`, `2026-09-27T10:00:00Z warning ads push failed err="deadline"`,
		`2026-09-27 10:00:00 info not iso`, `2026x09-27T10:00:00Z info xds nope`,
		`[2026-09-27T10:00:00.000Z] "GET /api/v1/orders HTTP/1.1" 503 UF 0 91 12 - "-" "curl"`, `[bracket but not envoy]`,
		`I0927 10:00:00.000000 1 controller.go:42] Reconciler error for pod-1: timeout after 12ms`, `E0927 10:00:00.1 7 x.go:1] failed key=v`, `Ixxxx not klog`,
		`level=error msg="db down" host=pg attempt=3`, `ts=1 msg=ok level=debug scope=cache`, `msg=only`, `level=info this is not logfmt because words`,
		`ERROR database connection timed out host=postgres:5432`, `java.lang.NullPointerException at Foo`, `Traceback (most recent call last):`,
		`panic: runtime error`, `error: something`, `fatal: bad`, `WARNING disk nearly full`, `warning: soft limit`, `WARNINGS are not WARN`,
		`DEBUG cache hit key=orders/1`, `TRACE span=abc`, `plain text line`, `key=value only`, `url=http://x?a=b&c=d`, `ERRORS plural`, ``, `   `,
	}
	for i := 0; i < 300; i++ {
		c = append(c, fmt.Sprintf(`2026-09-27T00:00:%02d.000000000Z GET /v%d status=%d duration=%dms user=u%d`, i%60, i, 200+i%400, i, i%7))
	}
	return c
}

func TestParseGolden(t *testing.T) {
	var got []Parsed
	for _, l := range parseCorpus() {
		got = append(got, Parse(l))
	}
	b, _ := json.MarshalIndent(got, "", " ")
	const file = "testdata/parse_golden.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(file, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Fatal("Parse output changed; if intended, run UPDATE_GOLDEN=1 go test ./internal/logs")
	}
}

func BenchmarkParse(b *testing.B) {
	lines := parseCorpus()
	for i := 0; i < b.N; i++ {
		Parse(lines[i%len(lines)])
	}
}
