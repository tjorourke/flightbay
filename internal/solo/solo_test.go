package solo

import (
	"strings"
	"testing"
	"time"
)

var ref = time.Date(2026, 9, 27, 18, 0, 0, 0, time.UTC)

func TestZtunnelSummaryAndCerts(t *testing.T) {
	dump := `{"workloads":[{"name":"checkout-1","namespace":"payments","workloadIps":["10.0.0.9"],"serviceAccount":"checkout","node":"w1","protocol":"HBONE"}],
	 "services":[{"namespace":"payments","hostname":"checkout.payments.svc.cluster.local","vips":["c/10.96.0.1"],"endpoints":{"a":{},"b":{}}}],
	 "policies":[{"name":"allow","namespace":"payments","action":"Allow","scope":"Namespace"}],
	 "certificates":[{"identity":"spiffe://c/ns/payments/sa/checkout","state":"Available",
	   "certChain":[{"expirationTime":"2026-09-28T16:00:00Z","serialNumber":"1"}],
	   "rootCerts":[{"expirationTime":"2026-10-10T00:00:00Z","serialNumber":"9"}]},
	  {"identity":"spiffe://c/ns/payments/sa/old","state":"Available","certChain":[{"expirationTime":"2026-09-27T12:00:00Z","serialNumber":"2"}]}]}`
	s := Summarise("ztunnel", map[string][]byte{"config_dump.json": []byte(dump)}, ref)
	if s.Counts["workloads"] != 1 || s.Counts["services"] != 1 || s.Counts["policies"] != 1 {
		t.Fatalf("counts %v", s.Counts)
	}
	if len(s.Tables) != 3 || s.Tables[1].Rows[0][3] != "2" {
		t.Fatalf("tables %+v", s.Tables)
	}
	if len(s.Certs) != 3 || s.Certs[0].HoursLeft >= 0 || !s.Certs[2].Root || s.Certs[2].DaysLeft != 12 {
		t.Fatalf("certs %+v", s.Certs)
	}
}

func TestEnvoySummaryAndCerts(t *testing.T) {
	dump := `{"configs":[
	 {"@type":"type.googleapis.com/envoy.admin.v3.ListenersConfigDump","dynamic_listeners":[{"active_state":{"listener":{"name":"0.0.0.0_8080","address":{"socket_address":{"address":"0.0.0.0","port_value":8080}},"filter_chains":[{},{}]}}}]},
	 {"@type":"type.googleapis.com/envoy.admin.v3.ClustersConfigDump","dynamic_active_clusters":[{"cluster":{"name":"outbound|8080||checkout.payments.svc.cluster.local","type":"EDS"}}]},
	 {"@type":"type.googleapis.com/envoy.admin.v3.EndpointsConfigDump","dynamic_endpoint_configs":[{"endpoint_config":{"cluster_name":"outbound|8080||checkout.payments.svc.cluster.local","endpoints":[{"lb_endpoints":[{},{},{}]}]}}]},
	 {"@type":"type.googleapis.com/envoy.admin.v3.RoutesConfigDump","dynamic_route_configs":[{"route_config":{"name":"8080","virtual_hosts":[{"name":"checkout","domains":["checkout","checkout.payments"]}]}}]}]}`
	certs := `{"certificates":[{"cert_chain":[{"serial_number":"a","expiration_time":"2026-09-28T10:00:00Z","subject_alt_names":[{"uri":"spiffe://c/ns/payments/sa/wp"}]}],
	  "ca_cert":[{"serial_number":"r","expiration_time":"2026-10-01T00:00:00Z"}]}]}`
	s := Summarise("envoy", map[string][]byte{"config_dump.json": []byte(dump), "certs.json": []byte(certs), "server_info.json": []byte(`{"version":"x/1.30","state":"LIVE"}`)}, ref)
	if s.Counts["listeners"] != 1 || s.Counts["clusters"] != 1 || s.Counts["routes"] != 1 {
		t.Fatalf("counts %v", s.Counts)
	}
	if s.Tables[1].Rows[0][2] != "3" || s.Tables[0].Rows[0][1] != "0.0.0.0:8080" {
		t.Fatalf("tables %+v", s.Tables)
	}
	if len(s.Certs) != 2 || s.Certs[0].Identity != "spiffe://c/ns/payments/sa/wp" || !s.Certs[1].Root {
		t.Fatalf("certs %+v", s.Certs)
	}
	if len(s.Notes) == 0 || !strings.Contains(s.Notes[0], "LIVE") {
		t.Fatalf("notes %v", s.Notes)
	}
}

func TestSynczStale(t *testing.T) {
	rows := Syncz([]byte(`[{"proxy":"b.ns","proxy_type":"sidecar","resources":{"type.googleapis.com/envoy.config.cluster.v3.Cluster":{"sent":"v2","acked":"v1"},"type.googleapis.com/envoy.config.listener.v3.Listener":{"sent":"v2","acked":"v2"}}},
	  {"proxy":"a.ns","proxy_type":"ztunnel","resources":{"type.googleapis.com/istio.workload.Address":{"sent":"v1","acked":"v1"}}}]`))
	if len(rows) != 2 || rows[0].Proxy != "a.ns" || len(rows[0].Stale) != 0 || len(rows[1].Stale) != 1 || rows[1].Stale[0] != "envoy.config.cluster.v3.Cluster" {
		t.Fatalf("%+v", rows)
	}
}

func TestAgentgatewaySummary(t *testing.T) {
	dump := `{"binds":[{"address":"[::]:80","listeners":{"ns/gw.http":{"protocol":"HTTP","routes":{"r1":{"name":"route","namespace":"ns","matches":[{"path":{"pathPrefix":"/v1"}}],"backends":[{"backend":"ns/llm"}]}}}}}],
	 "policies":[{"name":{"kind":"AgentgatewayPolicy","namespace":"ns","name":"p"},"target":{"gateway":{"gatewayName":"gw"}},"policy":{"traffic":{"jwtAuth":{}}}}],
	 "backends":[{"backend":{"ai":{"name":"llm","namespace":"ns","target":{}}}}]}`
	s := Summarise("agentgateway", map[string][]byte{"config_dump.json": []byte(dump)}, ref)
	if s.Counts["routes"] != 1 || s.Tables[0].Rows[0][5] != "ns/llm" || s.Tables[1].Rows[0][3] != "traffic.jwtAuth" || s.Tables[2].Rows[0][1] != "ns/llm" {
		t.Fatalf("%+v %+v", s.Counts, s.Tables)
	}
}
