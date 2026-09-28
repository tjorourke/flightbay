package server

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/cli"
	"github.com/tjorourke/flightbay/internal/flows"
)

func TestFlowsEndpointAndHelper(t *testing.T) {
	s, srv := newServer(t, false)
	s.WS.AddLogLines("mesh1", "agentgateway-system", "ar-ingress-1", "agentgateway", "current", []string{
		"2026-09-27T21:50:44.900000000Z 2026-09-27T21:50:44.861295Z  info  request gateway=agentgateway-system/ar-ingress listener=http route=ar-keycloak/keycloak endpoint=10.244.1.131:8080 src.addr=10.244.1.1:29278 http.method=GET http.host=keycloak.example.com http.path=/realms/a http.status=200 protocol=http duration=45ms",
	}, "agw")
	if _, err := s.WS.AddReviewerFlows("mesh1", "vpc.log", strings.NewReader("2 123456789010 eni-1235b8ca123456789 172.31.16.139 172.31.16.21 20641 22 6 20 4249 1418530010 1418530070 ACCEPT OK\n"), ""); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Total    int            `json:"total"`
		BySource map[string]int `json:"bySource"`
		Flows    []flows.Flow   `json:"flows"`
	}
	if code := get(t, srv, "/api/flows", &res); code != 200 || res.Total != 2 || res.BySource["agentgateway"] != 1 || res.BySource["aws-vpc"] != 1 {
		t.Fatalf("%d %+v", code, res)
	}
	res.Flows = nil
	get(t, srv, "/api/flows?source=agentgateway&cluster=mesh1", &res)
	if len(res.Flows) != 1 || res.Flows[0].Via != "agentgateway-system/ar-ingress" {
		t.Fatalf("%+v", res)
	}
	var out bytes.Buffer
	if err := cli.Flows(&out, srv.URL, tok, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "keycloak.example.com:8080") || !strings.Contains(out.String(), "172.31.16.21:22") || !strings.Contains(out.String(), "2 source/destination pairs") {
		t.Fatal(out.String())
	}
}
