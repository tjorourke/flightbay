package logs

import (
	"testing"
	"time"
)

func TestSplitTimestamp(t *testing.T) {
	ts, rest := SplitTimestamp("2026-09-27T10:07:12.123456789Z ERROR boom")
	if ts.IsZero() || ts.Nanosecond() != 123456789 || rest != "ERROR boom" {
		t.Fatalf("got %v %q", ts, rest)
	}
	ts, rest = SplitTimestamp("no timestamp here")
	if !ts.IsZero() || rest != "no timestamp here" {
		t.Fatalf("got %v %q", ts, rest)
	}
}

func TestParseFormats(t *testing.T) {
	cases := []struct {
		name, in, level, scope, msg string
		fields                      map[string]string
	}{
		{"json", `{"level":"error","msg":"upstream failed","scope":"proxy","trace_id":"a4e98f02","status":503}`, "error", "proxy", "upstream failed", map[string]string{"trace_id": "a4e98f02", "status": "503"}},
		{"json severity", `{"severity":"WARNING","message":"slow"}`, "warn", "", "slow", nil},
		{"istio text", "2026-09-27T10:00:00.000000Z\tinfo\tads\tADS: push completed version=2026-09-27", "info", "ads", "ADS: push completed version=2026-09-27", map[string]string{"version": "2026-09-27"}},
		{"ztunnel", "2026-09-27T10:00:00.000Z  warn  xds::client  connection failed destination=10.42.2.18:5432", "warn", "xds::client", "connection failed destination=10.42.2.18:5432", map[string]string{"destination": "10.42.2.18:5432"}},
		{"klog", "E0927 10:00:00.000000       1 controller.go:123] failed to sync gateway", "error", "controller.go:123", "failed to sync gateway", nil},
		{"logfmt", `level=warn msg="retrying request" attempt=2 request_id=req-72b9`, "warn", "", "retrying request", map[string]string{"attempt": "2", "request_id": "req-72b9"}},
		{"envoy access", `[2026-09-27T10:11:13.884Z] "GET /orders HTTP/1.1" 503 UF 0 91 3 - "-" "curl/8.0" "req-1" "checkout:8080" "10.42.2.18:8080"`, "error", "access", `GET /orders HTTP/1.1 503`, map[string]string{"method": "GET", "path": "/orders", "status": "503", "response_flags": "UF"}},
		{"raw error", "ERROR database connection timed out host=postgres:5432", "error", "", "ERROR database connection timed out host=postgres:5432", map[string]string{"host": "postgres:5432"}},
		{"raw plain", "Starting checkout", "info", "", "Starting checkout", nil},
		{"panic", "panic: runtime error: index out of range", "error", "", "panic: runtime error: index out of range", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Parse(c.in)
			if p.Level != c.level || p.Scope != c.scope || p.Msg != c.msg {
				t.Fatalf("got level=%q scope=%q msg=%q", p.Level, p.Scope, p.Msg)
			}
			for k, v := range c.fields {
				if p.Fields[k] != v {
					t.Fatalf("field %s = %q, want %q (all %v)", k, p.Fields[k], v, p.Fields)
				}
			}
		})
	}
}

func rec(level, ns, pod, msg string, fields map[string]string) *Record {
	return &Record{TS: time.Date(2026, 9, 27, 10, 8, 0, 0, time.UTC), Cluster: "mesh1", Namespace: ns, Pod: pod, Container: "app", Stream: "current", Level: level, Msg: msg, Raw: msg, Fields: fields}
}

func TestQuery(t *testing.T) {
	r := rec("error", "payments", "checkout-6d8b9c-f2k7p", "Database connection timed out host=postgres", map[string]string{"trace_id": "a4e98f02"})
	cases := []struct {
		q    string
		want bool
	}{
		{"", true},
		{"level:error", true},
		{"level:warn", false},
		{"ns:payments level:error", true},
		{"namespace:istio-system", false},
		{"pod:checkout*", true},
		{"pod:istiod*", false},
		{"trace_id:a4e98f02", true},
		{"trace_id:zzz", false},
		{"request_id:x", false},
		{`"connection timed out"`, true},
		{`"timed connection"`, false},
		{"database", true},
		{"DATABASE timed", true},
		{"-level:error", false},
		{"-healthz", true},
		{"cluster:mesh1 stream:current container:app", true},
		{"stream:previous", false},
		{"level:error OR level:warn", true},
		{"level:info OR level:warn", false},
	}
	for _, c := range cases {
		q, err := ParseQuery(c.q)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if got := q.Match(r); got != c.want {
			t.Errorf("%q: got %v want %v", c.q, got, c.want)
		}
	}
}

func TestQuerySince(t *testing.T) {
	q, err := ParseQuery("since:5m level:error")
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 9, 27, 10, 12, 0, 0, time.UTC)
	q.Reference = end
	r := rec("error", "payments", "p", "x", nil)
	if !q.Match(r) {
		t.Fatal("10:08 is within 5m of 10:12")
	}
	q2, _ := ParseQuery("since:2m")
	q2.Reference = end
	if q2.Match(r) {
		t.Fatal("10:08 is outside 2m of 10:12")
	}
	if _, err := ParseQuery("since:soon"); err == nil {
		t.Fatal("expected bad duration error")
	}
	if _, err := ParseQuery(`"unterminated`); err == nil {
		t.Fatal("expected unterminated quote error")
	}
}

func TestSignature(t *testing.T) {
	a := Signature("connection refused destination=10.42.2.18:5432 after 1203ms id=77f3a")
	b := Signature("connection refused destination=10.42.1.9:5432 after 88ms id=0b91c")
	if a != b {
		t.Fatalf("signatures differ:\n%s\n%s", a, b)
	}
}
