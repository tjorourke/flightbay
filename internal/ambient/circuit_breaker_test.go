package ambient

import "testing"

func TestCircuitBreakerGenerationAndScopedEdit(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"circuit-breaker"}, CircuitBreaker: &CircuitBreakerRequest{MaxConnections: 12, MaxPendingRequests: 13, MaxRequests: 14, MaxRetries: 2, MaxRequestsPerConnection: 0, ConnectTimeout: "7s"}}
	plan, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Objects) != 1 || N(Path(plan.Objects[0], "spec", "trafficPolicy", "connectionPool", "tcp", "maxConnections")) != 12 {
		t.Fatalf("bad circuit breaker: %+v", plan)
	}
	existing := object("DestinationRule", "existing", map[string]any{"host": "front", "trafficPolicy": map[string]any{"tls": map[string]any{"mode": "ISTIO_MUTUAL"}, "outlierDetection": map[string]any{"consecutive5xxErrors": 9}, "connectionPool": map[string]any{"tcp": map[string]any{"tcpKeepalive": map[string]any{"time": "40s"}}, "http": map[string]any{"idleTimeout": "60s"}}}})
	put(ws, "networking.istio.io", "destinationrules", "DestinationRule", existing)
	entries := editableConfigurations(ws, ws.Get("c", "", "services", "app", "front"))
	for _, entry := range entries {
		if entry.Ref.Name == "existing" {
			req.Edit = entry.Request.Edit
		}
	}
	if req.Edit == nil {
		t.Fatal("existing connection pool cannot be edited")
	}
	plan, err = Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Objects) != 0 || len(plan.Changes) != 1 {
		t.Fatalf("edit should patch the original rule: %+v", plan)
	}
	var policy map[string]any
	for _, f := range plan.Changes[0].Fields {
		if f.Path == "/spec/trafficPolicy" {
			policy = M(f.After)
		}
	}
	if S(Path(policy, "tls", "mode")) != "ISTIO_MUTUAL" || N(Path(policy, "outlierDetection", "consecutive5xxErrors")) != 9 || S(Path(policy, "connectionPool", "tcp", "tcpKeepalive", "time")) != "40s" || S(Path(policy, "connectionPool", "http", "idleTimeout")) != "60s" {
		t.Fatalf("edit discarded unrelated policy: %+v", policy)
	}
	req.Edit = nil
	req.CircuitBreaker.MaxConnections = -1
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("negative circuit breaker accepted")
	}
	req.CircuitBreaker.MaxConnections = 12
	req.CircuitBreaker.ConnectTimeout = "1ns"
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("sub-millisecond connect timeout accepted")
	}
}
