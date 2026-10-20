package ambient

import (
	"strings"
	"testing"
)

func TestRateLimitChooserUsesExplicitCapturedQuotas(t *testing.T) {
	ws := fixture(t)
	config := object("ConfigMap", "quota-config", nil)
	config["data"] = map[string]any{"config.yaml": "domain: customer\ndescriptors:\n- key: api\n  value: reviews\n  rate_limit: {requests_per_unit: 50, unit: minute}\n- key: client\n  descriptors:\n  - key: api\n    value: nested\n    rate_limit: {requests_per_unit: 5, unit: second}\n"}
	put(ws, "", "configmaps", "ConfigMap", config)
	quotas := capturedRateLimitQuotas(ws)
	if len(quotas) != 1 || quotas[0].Domain != "customer" || quotas[0].Value != "reviews" || quotas[0].Requests != 50 {
		t.Fatalf("invented or flattened quotas: %+v", quotas)
	}
}
func TestGuidedRetryDurationsAndPercentageOnlyDetection(t *testing.T) {
	ws := enterpriseResilienceFixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"retries"}, RetryAttempts: 2, Timeout: "1m", BackendTimeout: "250ms", Resilience: &ResilienceRequest{Scope: "service"}}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.YAML, "timeout: 60s") || !strings.Contains(p.YAML, "per_try_timeout: 0.25s") {
		t.Fatal("Envoy durations were not canonicalised")
	}
	req.Tasks = []string{"endpoint-failover"}
	req.Resilience.FailurePercentage = 50
	req.Resilience.DisableConsecutive = true
	p, err = Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.YAML, "consecutive5xxErrors: 0") {
		t.Fatal("percentage-only detection still enables consecutive ejection")
	}
}
