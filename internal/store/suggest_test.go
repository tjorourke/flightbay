package store

import (
	"fmt"
	"strings"
	"testing"
)

func sugg(s []Suggestion) string {
	var out []string
	for _, x := range s {
		out = append(out, fmt.Sprintf("%s=%d", x.Value, x.Count))
	}
	return strings.Join(out, ",")
}

func TestSuggestObjects(t *testing.T) {
	ws := objWS(t)
	for _, c := range []struct{ field, prefix, q, want string }{
		{"kind", "", "", "pod=3,httproute=1,service=1"},
		{"kind", "po", "", "pod=3"},
		{"k", "svc", "", ""}, // short names resolve on search, not here
		{"ns", "", "", "payments=4,istio-system=1"},
		{"ns", "", "kind:pod", "payments=2,istio-system=1"},
		{"label", "", "", "app=4"},
		{"label", "a", "kind:svc", "app=1"},
		{"label", "app=", "", "checkout=2,cart=1,istiod=1"},
		{"label", "app=c", "kind:pod", "app=cart=1,app=checkout=1"},
		{"name", "check", "", "checkout=1,checkout-7d9f=1"},
		{"cluster", "", "", "mesh1=5"},
		{"namespace", "ist", "", "istio-system=1"},
	} {
		got, err := ws.SuggestObjects(c.field, c.prefix, c.q, "", 0)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		want := c.want
		if c.field == "label" && strings.Contains(c.prefix, "=") && !strings.HasPrefix(want, "app=") {
			parts := strings.Split(want, ",")
			for i := range parts {
				parts[i] = "app=" + parts[i]
			}
			want = strings.Join(parts, ",")
		}
		if sugg(got) != want {
			t.Errorf("%+v: got %s", c, sugg(got))
		}
	}
	keys, _ := ws.SuggestObjects("", "", "", "", 0)
	if len(keys) < 5 || keys[0].Example == "" {
		t.Fatalf("keys: %+v", keys)
	}
	lk, _ := ws.SuggestObjects("label", "ap", "", "", 0)
	if len(lk) != 1 || !lk[0].More || lk[0].Hint != "3 values" {
		t.Fatalf("label keys should continue into values: %+v", lk)
	}
}

func TestSuggestLogs(t *testing.T) {
	ws := New()
	ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "current", []string{
		`2026-09-27T10:07:12Z {"level":"error","msg":"timed out","upstream":"db"}`,
		`2026-09-27T10:07:13Z {"level":"info","msg":"ok","upstream":"cache"}`,
	}, "a")
	ws.AddLogLines("mesh1", "istio-system", "istiod-1", "discovery", "previous", []string{`2026-09-27T10:07:14Z warn push failed`}, "b")
	for _, c := range []struct{ field, prefix, q, want string }{
		{"ns", "", "", "payments=2,istio-system=1"},
		{"ns", "p", "", "payments=2"},
		{"level", "", "ns:payments", "error=1,info=1"},
		{"stream", "", "", "current=2,previous=1"},
		{"upstream", "", "", "cache=1,db=1"},
		{"upstream", "d", "level:error \"free text is ignored\"", "db=1"},
		{"since", "1", "", "15m=0,1h=0"},
	} {
		got, err := ws.SuggestLogs(c.field, c.prefix, c.q, "", 0)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if sugg(got) != c.want {
			t.Errorf("%+v: got %s", c, sugg(got))
		}
	}
	keys, err := ws.SuggestLogs("", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, k := range keys {
		found = found || k.Value == "upstream"
	}
	if !found || keys[0].Value != "level" {
		t.Fatalf("keys should include structured fields: %s", sugg(keys))
	}
}
