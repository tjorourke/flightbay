package redact

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const canary = "FLIGHTBAY-CANARY-7f3a9c"

func mustNew(t *testing.T, extra ...string) *Redactor {
	t.Helper()
	r, err := New(extra)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestStringScrubbers(t *testing.T) {
	r := mustNew(t)
	cases := []struct {
		name, in, mustNotContain, mustContain string
	}{
		{"jwt", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJlLXZhbHVl done", "eyJhbGciOiJIUzI1NiJ9", "done"},
		{"bearer", `Authorization: Bearer abc.def-ghi`, "abc.def-ghi", "Authorization: Bearer [REDACTED]"},
		{"basic", `authorization=Basic dXNlcjpwYXNz`, "dXNlcjpwYXNz", "[REDACTED]"},
		{"aws", "key AKIAIOSFODNN7EXAMPLE used", "AKIAIOSFODNN7EXAMPLE", "[REDACTED]"},
		{"openai", "OPENAI sk-proj1234567890abcdefXYZ", "sk-proj1234567890abcdefXYZ", "[REDACTED]"},
		{"anthropic", "sk-ant-api03-abcdefghijklmnopqrstuv", "abcdefghijklmnop", "[REDACTED]"},
		{"url", "dial postgres://admin:hunter2@db:5432/app", "hunter2", "postgres://[REDACTED]@db:5432/app"},
		{"kv", "db_password=hunter2 user=bob", "hunter2", "user=bob"},
		{"json kv", `{"apiKey":"s3cr3t","name":"x"}`, "s3cr3t", `"name":"x"`},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----", "MIIabc", "[REDACTED PRIVATE KEY]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.String("test", c.in)
			if strings.Contains(got, c.mustNotContain) {
				t.Fatalf("secret survived: %q", got)
			}
			if !strings.Contains(got, c.mustContain) {
				t.Fatalf("expected %q in %q", c.mustContain, got)
			}
		})
	}
}

func TestCertificatesAreKept(t *testing.T) {
	r := mustNew(t)
	cert := "-----BEGIN CERTIFICATE-----\nMIIBcert\n-----END CERTIFICATE-----"
	if got := r.String("x", cert); got != cert {
		t.Fatalf("certificate was modified: %q", got)
	}
}

func TestBenignTextUnchanged(t *testing.T) {
	r := mustNew(t)
	in := "GET /healthz 200 duration=2ms upstream=checkout.payments.svc"
	if got := r.String("x", in); got != in {
		t.Fatalf("benign text changed: %q", got)
	}
}

func TestExtraPatterns(t *testing.T) {
	r := mustNew(t, `CUST-[0-9]{6}`)
	if got := r.String("x", "customer CUST-123456 ok"); strings.Contains(got, "CUST-123456") {
		t.Fatalf("extra pattern not applied: %q", got)
	}
	if _, err := New([]string{"("}); err == nil {
		t.Fatal("expected invalid pattern error")
	}
}

func TestLineRedactorHandlesMultiLinePrivateKey(t *testing.T) {
	r := mustNew(t)
	lr := r.Lines("logs/a")
	lines := []string{"before", "-----BEGIN PRIVATE KEY-----", "MIIsecretline1", "MIIsecretline2", "-----END PRIVATE KEY-----", "after"}
	var out []string
	for _, l := range lines {
		out = append(out, lr.Line(l))
	}
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "MIIsecret") {
		t.Fatalf("multi-line key survived: %q", joined)
	}
	if out[0] != "before" || out[5] != "after" {
		t.Fatalf("surrounding lines changed: %q", out)
	}
}

func TestSecretKeysMode(t *testing.T) {
	r := mustNew(t)
	secret := map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]any{
			"name": "db", "namespace": "payments",
			"managedFields": []any{map[string]any{"manager": "x"}},
			"annotations": map[string]any{
				"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"password":"` + canary + `"}}`,
				"team": "payments",
			},
		},
		"data":       map[string]any{"password": base64.StdEncoding.EncodeToString([]byte(canary)), "user": "Ym9i"},
		"stringData": map[string]any{"token": canary},
	}
	out := r.Secret("secrets", secret, SecretKeys, false)
	b, _ := json.Marshal(out)
	if bytes.Contains(b, []byte(canary)) || bytes.Contains(b, []byte(base64.StdEncoding.EncodeToString([]byte(canary)))) {
		t.Fatalf("secret value survived: %s", b)
	}
	if _, ok := out["data"]; ok {
		t.Fatal("data should be removed")
	}
	md := out["metadata"].(map[string]any)
	if _, ok := md["managedFields"]; ok {
		t.Fatal("managedFields should be stripped")
	}
	if md["annotations"].(map[string]any)["team"] != "payments" {
		t.Fatal("benign annotation lost")
	}
	keys := out["x-flightbay"].(map[string]any)["keys"].([]any)
	if len(keys) != 3 {
		t.Fatalf("expected 3 key entries, got %v", keys)
	}
	first := keys[0].(map[string]any)
	if first["name"] != "password" || first["bytes"] != len(canary) {
		t.Fatalf("unexpected key entry %v", first)
	}
}

func TestSecretMetadataModeHasNoKeyNames(t *testing.T) {
	r := mustNew(t)
	out := r.Secret("s", map[string]any{"kind": "Secret", "metadata": map[string]any{"name": "a"}, "data": map[string]any{"password": "eA=="}}, SecretMetadata, false)
	b, _ := json.Marshal(out)
	if bytes.Contains(b, []byte("password")) {
		t.Fatalf("key name present in metadata mode: %s", b)
	}
}

func helmRelease(t *testing.T) string {
	t.Helper()
	rel := map[string]any{
		"name": "checkout", "version": 7,
		"info":   map[string]any{"status": "deployed"},
		"chart":  map[string]any{"metadata": map[string]any{"name": "checkout", "version": "2.8.1", "appVersion": "2.8.1"}},
		"config": map[string]any{"dbPassword": canary},
	}
	raw, _ := json.Marshal(rel)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(raw)
	_ = w.Close()
	helmValue := base64.StdEncoding.EncodeToString(gz.Bytes())
	return base64.StdEncoding.EncodeToString([]byte(helmValue))
}

func TestHelmReleaseReducedToSummary(t *testing.T) {
	r := mustNew(t)
	secret := map[string]any{
		"kind": "Secret", "type": "helm.sh/release.v1",
		"metadata": map[string]any{"name": "sh.helm.release.v1.checkout.v7", "namespace": "payments"},
		"data":     map[string]any{"release": helmRelease(t)},
	}
	out := r.Secret("helm", secret, SecretKeys, true)
	b, _ := json.Marshal(out)
	if bytes.Contains(b, []byte(canary)) {
		t.Fatalf("helm values survived: %s", b)
	}
	h := out["x-flightbay"].(map[string]any)["helm"].(map[string]any)
	if h["chart"] != "checkout" || h["chartVersion"] != "2.8.1" || h["status"] != "deployed" || h["revision"] != float64(7) {
		t.Fatalf("unexpected helm summary %v", h)
	}
}

func TestObjectScrubsEnvAndSensitiveKeys(t *testing.T) {
	r := mustNew(t)
	pod := map[string]any{
		"kind":     "Pod",
		"metadata": map[string]any{"name": "p", "managedFields": []any{"x"}},
		"spec": map[string]any{"containers": []any{map[string]any{
			"name": "app",
			"env": []any{
				map[string]any{"name": "DB_PASSWORD", "value": canary},
				map[string]any{"name": "LOG_LEVEL", "value": "debug"},
				map[string]any{"name": "DSN", "value": "postgres://u:" + canary + "@db/x"},
			},
			"args": []any{"--token=" + canary},
		}}},
	}
	out := r.Object("pods", pod)
	b, _ := json.Marshal(out)
	if bytes.Contains(b, []byte(canary)) {
		t.Fatalf("canary survived: %s", b)
	}
	if !bytes.Contains(b, []byte(`"debug"`)) {
		t.Fatalf("benign env value lost: %s", b)
	}
	if bytes.Contains(b, []byte("managedFields")) {
		t.Fatal("managedFields not stripped")
	}
}

func TestReportCountsWithoutValues(t *testing.T) {
	r := mustNew(t)
	r.String("clusters/a/logs/x", "Authorization: Bearer "+canary)
	r.String("clusters/a/logs/x", "password="+canary)
	rep := r.Report()
	b, _ := json.Marshal(rep)
	if bytes.Contains(b, []byte(canary)) {
		t.Fatal("report leaked a value")
	}
	if rep.Total != 2 || rep.Locations["clusters/a/logs/x"]["authorization"] != 1 || rep.Locations["clusters/a/logs/x"]["sensitive-key"] != 1 {
		t.Fatalf("unexpected report %+v", rep)
	}
}

func TestReferencesAndCountsStayReadable(t *testing.T) {
	r := mustNew(t)
	vol := map[string]any{"kind": "Pod", "spec": map[string]any{"volumes": []any{
		map[string]any{"name": "creds", "secret": map[string]any{"secretName": "checkout-db"}},
	}}}
	b, _ := json.Marshal(r.Object("pods", vol))
	if !bytes.Contains(b, []byte("checkout-db")) {
		t.Fatalf("secretName reference was redacted: %s", b)
	}
	line := "request completed input_tokens=512 output_tokens=40"
	if got := r.String("x", line); got != line {
		t.Fatalf("token counts redacted: %q", got)
	}
}

func TestHelmSummaryOnlyWhenRequested(t *testing.T) {
	r := mustNew(t)
	secret := map[string]any{"kind": "Secret", "type": "helm.sh/release.v1", "metadata": map[string]any{"name": "h"}, "data": map[string]any{"release": helmRelease(t)}}
	out := r.Secret("helm", secret, SecretKeys, false)
	if _, ok := out["x-flightbay"].(map[string]any)["helm"]; ok {
		t.Fatal("helm decoded without being requested")
	}
}

func TestSecretsInsideEmbeddedJSONAreRedacted(t *testing.T) {
	r, _ := New(nil)
	const canary = "EMBEDDED-CANARY-77"
	manifest := `{"apiVersion":"apps/v1","kind":"Deployment","spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"DB_PASSWORD","value":"` + canary + `"}]}]}}}}`
	obj := map[string]any{"metadata": map[string]any{"name": "checkout", "annotations": map[string]any{LastApplied: manifest}},
		"data": map[string]any{"settings.json": `{"db":{"user":"app","password":"` + canary + `"}}`, "plain": "not json {"}}
	out := r.Object("x", obj)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), canary) {
		t.Fatalf("secret inside embedded JSON survived: %s", b)
	}
	ann := out["metadata"].(map[string]any)["annotations"].(map[string]any)[LastApplied].(string)
	if !strings.Contains(ann, `"DB_PASSWORD"`) || !strings.Contains(ann, `"kind":"Deployment"`) {
		t.Fatalf("embedded manifest mangled: %s", ann)
	}
	if out["data"].(map[string]any)["plain"] != "not json {" {
		t.Fatal("non-JSON string changed")
	}
}

func TestDropFields(t *testing.T) {
	r, _ := New(nil)
	if err := r.DropFields([]string{`metadata.annotations["corp.example/*"]`, `spec.containers[*].env`, `data.license`}); err != nil {
		t.Fatal(err)
	}
	obj := map[string]any{
		"kind": "Pod",
		"metadata": map[string]any{"name": "a", "annotations": map[string]any{
			"corp.example/owner": "jane", "corp.example/cost-centre": "42", "keep.me/x": "y",
			LastApplied: `{"metadata":{"annotations":{"corp.example/owner":"jane"}},"spec":{"containers":[{"name":"c","env":[{"name":"A","value":"b"}]}]}}`,
		}},
		"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "env": []any{map[string]any{"name": "A", "value": "b"}}}}},
		"data": map[string]any{"license": "XYZ", "other": "ok"},
	}
	out := r.Object("clusters/x/resources/pods", obj)
	ann := out["metadata"].(map[string]any)["annotations"].(map[string]any)
	if _, ok := ann["corp.example/owner"]; ok {
		t.Fatal("annotation glob")
	}
	if ann["keep.me/x"] != "y" {
		t.Fatal("other annotations kept")
	}
	if strings.Contains(ann[LastApplied].(string), "corp.example") || strings.Contains(ann[LastApplied].(string), `"env"`) {
		t.Fatalf("rules apply inside last-applied-configuration: %s", ann[LastApplied])
	}
	c := out["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if _, ok := c["env"]; ok || c["name"] != "c" {
		t.Fatal("list step")
	}
	if d := out["data"].(map[string]any); d["license"] != nil || d["other"] != "ok" {
		t.Fatal("plain path")
	}
	rep := r.Report()
	got := 0
	for rule, n := range rep.Locations["clusters/x/resources/pods"] {
		if strings.HasPrefix(rule, "drop-field-") {
			got += n
		}
	}
	if got != 6 { // two annotations, env, license, and annotation and env inside last-applied
		t.Fatalf("counted %d removals: %v", got, rep.Locations)
	}
	for _, bad := range []string{"", "metadata.name", "kind", "spec..x", `metadata.annotations["x`, "spec.containers[*]"} {
		if _, err := ParseFieldRule(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// The literal prefilters must never change what is redacted: compare with
// every rule's expression run on every string.
func TestPrefilterMatchesFullScan(t *testing.T) {
	corpus := []string{
		"plain line with nothing in it",
		"Authorization: Bearer abc.def.ghi", "authorization=basic dXNlcjpwYXNz", "AUTHORIZATION : Bearer zzz",
		`{"Authorization":"Bearer abcdefgh"}`, "x-api-key=0123456789abcdef", "API_KEY: s3cr3tvalue", "apikey=abc",
		"password=hunter2", "Passwd: hunter2", "PASSWORD=\"quoted value\"", "db_secret=abc; token=xyz", "my-credential:abc",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl", "id AKIAABCDEFGHIJKLMNOP here", "ASIAABCDEFGHIJKLMNOP",
		"sk-ant-abcdefghijklmnopqrstuv", "key sk-abcdefghijklmnopqrstuvwxyz", "postgres://user:pa55@db:5432/x", "https://a:b@host/",
		"-----BEGIN RSA PRIVATE KEY-----MIIB-----END RSA PRIVATE KEY-----", "token_count=12", "secret=true", "apiVersion: v1",
		"tokens=[REDACTED]", "Authorization: Bearer [REDACTED]", "mailto:someone@example.com", "http://example.com/@user",
	}
	fast, _ := New([]string{`corp-[0-9]{4}`})
	slow, _ := New([]string{`corp-[0-9]{4}`})
	for i := range slow.rules {
		slow.rules[i].need = nil
	}
	for i := 0; i < 2000; i++ {
		corpus = append(corpus, fmt.Sprintf("%d GET /v1/orders/%d status=200 duration_ms=%d user=u%d corp-%04d", i, i*7, i%900, i%13, i))
	}
	for _, s := range corpus {
		if a, b := fast.String("x", s), slow.String("x", s); a != b {
			t.Errorf("prefilter changed the result for %q: %q vs %q", s, a, b)
		}
	}
	if a, b := fast.Report(), slow.Report(); a.Total != b.Total || fmt.Sprint(a.Locations) != fmt.Sprint(b.Locations) {
		t.Errorf("counts differ: %+v vs %+v", a, b)
	}
}

func BenchmarkLogLine(b *testing.B) {
	r, _ := New(nil)
	l := r.Lines("x")
	line := `2026-09-27T00:00:01.000000000Z [00:00:01.000] "GET /api/v1/orders/42 HTTP/1.1" 200 - via_upstream - "-" 0 1043 12 11 "-" "curl/8.0" "a4e98f02-42" "checkout.payments.svc:8080"`
	for i := 0; i < b.N; i++ {
		l.Line(line)
	}
}
