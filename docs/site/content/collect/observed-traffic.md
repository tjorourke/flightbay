---
title: Observed traffic
weight: 50
description: Traffic sources in captured logs and attached flow files.
---

# Observed traffic

Observed traffic records connections, requests and denials found in the capture. [Agent reach](/desk/agent-reach/) compares these records with policy permissions, and `fbflows` lists them in the terminal. A destination missing from the logs may have been used outside the capture window or through a source that was not collected.

Most of it comes from logs a normal capture already has. For anything else, you can attach flow logs from your CNI, your cloud provider or a firewall.

## Already in your logs

Nothing extra is read from the cluster for these. They are parsed from the container logs the capture already selected.

| Source | Where it comes from | Notes |
|---|---|---|
| `ztunnel` | ztunnel access logs (`access`, `http_access`) | Counted once per connection, even when both ends log it. |
| `agentgateway` | agentgateway request logs (scope `request`, text or JSON) | Route, gateway, backend endpoint, `jwt.sub`, MCP method and tool, A2A method, LLM provider. 401 and 403 count as denied. |
| `envoy` | `istio-proxy` access logs of sidecars, Istio gateways and waypoints (Istio's default text format or its JSON encoding) | The upstream cluster gives the destination. RBAC, ext_authz, 401, 403 and `BlackHoleCluster` count as denied. |
| `coredns` | CoreDNS query logs | External names only: in-cluster names, reverse lookups and search-path expansions are skipped. |

A ztunnel line:

```text
2026-09-28T11:06:10.074538Z	info	access	connection complete	src.addr=10.244.0.11:34734 src.workload="eval-agent-5898644d8f-td826" src.namespace="agents" src.identity="spiffe://cluster.local/ns/agents/sa/eval-agent" dst.addr=10.244.0.9:15008 dst.hbone_addr=10.244.0.9:8081 dst.service="artifactory.tools.svc.cluster.local" dst.workload="artifactory-84889f66f6-8fb7m" dst.namespace="tools" direction="inbound" bytes_sent=360
```

An agentgateway MCP request:

```text
2026-09-27T21:51:02.100000Z	info	request gateway=kagent/agw listener=mcp route=kagent/github-mcp endpoint=10.244.2.40:3000 src.addr=10.244.1.77:51000 http.method=POST http.host=agw.kagent.svc.cluster.local http.path=/mcp http.status=200 jwt.sub=system:serviceaccount:kagent:demo-agent protocol=mcp mcp.method.name=tools/call mcp.target=github gen_ai.tool.name=create_issue mcp.session.id=abc duration=120ms
```

An Envoy access log line from a waypoint:

```text
[2020-11-25T21:26:18.409Z] "GET /status/418 HTTP/1.1" 418 - via_upstream - "-" 0 135 4 4 "-" "curl/7.73.0-DEV" "84961386-6d84-929d-98bd-c5aee93b5c88" "httpbin:8000" "10.44.1.27:80" outbound|8000||httpbin.foo.svc.cluster.local 10.44.1.23:37652 10.0.45.184:8000 10.44.1.23:46520 - default
```

A CoreDNS query:

```text
[INFO] 10.244.1.5:41234 - 4242 "A IN api.github.com. udp 32 false 512" NOERROR qr,rd,ra 110 0.0003s
```

### CoreDNS logs are off by default

The CoreDNS `log` plugin is not enabled in most clusters. Without it there are no query lines to parse. To get them, add `log` to the Corefile (in the `coredns` ConfigMap in `kube-system`) before you capture:

```text
.:53 {
    log
    errors
    ...
}
```

### Keep the right logs in scope

If you narrow logs with `--log-pods`, `--log-containers` or `--namespaces`, keep ztunnel (in `istio-system`), your waypoints and your gateways, or there will be nothing to parse. See [Logs](/collect/logs/).

## Attaching flow logs to the bundle

`--attach-flows` embeds flow log files in the bundle. They sit under `clusters/<context>/flows/<format>/<file>` and are covered by the manifest's checksums like everything else.

```bash
flightbay collect --context mesh1 --attach-flows hubble.jsonl,vpc-flows.log
flightbay collect --context mesh1,mesh2 --attach-flows eu.log@mesh1,us.log@mesh2
```

`FILE@CONTEXT` says which cluster a file belongs to; without it the file goes with the first context. The format is detected per file, and a file that cannot be read or recognised stops the run before anything is collected. Records naming namespaces outside the selection are removed, and every line is redacted, before writing.

## Reading Hubble live

If the cluster runs Cilium with Hubble, Flightbay can read recent flows from `hubble-relay` for you:

```bash
flightbay collect --context mesh1 --hubble-flows          # the last 20000 flows
flightbay collect --context mesh1 --hubble-flows=50000
```

It port-forwards to the `hubble-relay` pod in `kube-system` (port 4245) and runs your local `hubble observe --last N -o jsonpb`, with `--namespace` for each selected namespace. It needs:

- port-forward (asking for Hubble flows allows it, unless you also pass `--no-portforward`);
- `pods` get and list, and `pods/portforward` create, in `kube-system` (`flightbay rbac --hubble-flows` prints the Role);
- the `hubble` CLI on your `PATH`. The dry run says whether it was found.

Flows naming namespaces outside the selection are removed, and HTTP paths are redacted, before writing.

## Adding flow logs when opening a bundle

The engineer can add flow logs when opening a bundle, without a new capture:

```bash
flightbay-desk open flightbay-01234567-20260927T1012Z.tar.zst --flows hubble.jsonl,vpc-flows.log@mesh2
```

These are marked as added by the reviewer. They are not part of the bundle or its checksums, and the desk says so when it loads them.

## Supported formats

The format is detected from the first bytes of each file.

| Format | Name in the desk | What to export |
|---|---|---|
| Cilium Hubble | `hubble` | `hubble observe -o json` or `-o jsonpb` |
| Calico Enterprise / Cloud flow logs | `calico` | JSON Lines |
| AWS VPC flow logs | `aws-vpc` | The default version 2 format, or a custom format with a header line |
| GCP VPC flow logs and firewall logs | `gcp-vpc` | Cloud Logging JSON export, as JSON Lines or an array |
| Azure NSG and VNet flow logs | `azure-vnet` | The JSON files Azure writes |
| Generic | `generic` | CSV or JSON Lines with the columns below |

### Hubble

```json
{"flow":{"time":"2023-03-15T09:47:41.155466295Z","verdict":"FORWARDED","IP":{"source":"10.244.1.107","destination":"10.244.2.98"},"l4":{"TCP":{"source_port":52262,"destination_port":80}},"source":{"namespace":"default","pod_name":"xwing"},"destination":{"namespace":"default","pod_name":"deathstar-7848d6c4d5-8hhsr"},"traffic_direction":"EGRESS"}}
```

### Calico

```json
{"start_time":1700000000,"end_time":1700000300,"source_ip":"10.0.1.5","source_name_aggr":"frontend-7d9f8c-*","source_namespace":"shop","dest_ip":"10.0.2.9","dest_name_aggr":"checkout-5c6b-*","dest_namespace":"shop","dest_port":8080,"dest_service_name":"checkout","proto":"tcp","action":"allow","reporter":"src","num_flows":3}
```

### AWS VPC, default format

```text
2 123456789010 eni-1235b8ca123456789 172.31.16.139 172.31.16.21 20641 22 6 20 4249 1418530010 1418530070 ACCEPT OK
```

### AWS VPC, custom format with a header

```text
version vpc-id subnet-id instance-id interface-id account-id type srcaddr dstaddr srcport dstport pkt-srcaddr pkt-dstaddr protocol bytes packets start end action tcp-flags log-status
3 vpc-12345678 subnet-012345678 i-0c50d5961bcb2d47b eni-1235b8ca123456789 123456789010 IPv4 52.213.180.42 10.0.0.62 43416 5001 52.213.180.42 10.0.0.62 6 568 8 1566848875 1566848933 ACCEPT 2 OK
```

### GCP

```json
{"jsonPayload":{"connection":{"src_ip":"10.8.0.14","src_port":41234,"dest_ip":"10.8.1.3","dest_port":8080,"protocol":6},"reporter":"SRC","src_gke_details":{"pod":{"pod_name":"web-6d4b7","pod_namespace":"shop"}},"dest_gke_details":{"pod":{"pod_name":"api-7f9c2","pod_namespace":"shop"}}},"logName":"projects/acme/logs/compute.googleapis.com%2Fvpc_flows","timestamp":"2024-05-01T10:00:06Z"}
```

When both ends of a flow report it, the destination's copy is skipped so the flow counts once.

### Azure

```json
{"records":[{"time":"2018-11-13T12:00:35.3899262Z","category":"NetworkSecurityGroupFlowEvent","properties":{"Version":2,"flows":[{"rule":"DefaultRule_AllowInternetOutBound","flows":[{"mac":"000D3AF87856","flowTuples":["1542110377,10.5.16.4,13.67.143.118,59831,443,T,O,A,B,,,,"]}]}]}}]}
```

VNet flow logs (`flowLogVersion` 4, `flowRecords`) are read as well.

### Generic CSV or JSON Lines

The generic form is the way in for any other CNI or firewall: Antrea, Weave, kube-router, a firewall export or a SIEM query. A CSV file needs a header row; a JSON Lines file needs one object per line. You need at least one source column and one destination column.

Column names are matched without case, and `_`, `-`, `.` and spaces are ignored, so `src_ip`, `srcIp` and `Src IP` are the same column.

| Field | Accepted column names |
|---|---|
| Source IP | `src_ip`, `source_ip`, `srcaddr`, `source_address`, `client_ip` |
| Destination IP | `dst_ip`, `dest_ip`, `dstaddr`, `destination_ip`, `destination_address` |
| Destination port | `dst_port`, `dest_port`, `destination_port` |
| Source namespace | `src_namespace`, `source_namespace` |
| Source pod | `src_pod`, `source_pod`, `src_workload`, `source_workload` |
| Source identity | `src_identity`, `source_identity`, `principal` |
| Destination namespace | `dst_namespace`, `dest_namespace`, `destination_namespace` |
| Destination pod | `dst_pod`, `dest_pod`, `dst_workload`, `dest_workload`, `destination_pod` |
| Destination host | `dst_host`, `dest_host`, `dest_domain`, `dst_domain`, `fqdn`, `host`, `sni`, `destination_host` |
| Protocol | `protocol`, `proto` |
| Method, path | `method`, `path` (or `url`: a full URL fills in the host too) |
| Tool, route, via | `tool`, `route`, `via` |
| Verdict | `action`, `verdict`, `decision`, `disposition` |
| Count | `count`, `packets`, `connections`, `hits` (1 when absent) |
| Cluster | `cluster` |

```text
src_ip,src_namespace,src_pod,dst_host,dst_ip,dst_port,protocol,method,path,action,count
10.244.1.7,payments,checkout-7d9f8c-x2k,api.stripe.com,,443,tcp,,,allow,12
10.244.2.3,kagent,demo-agent-6b8,github.com,140.82.121.4,443,tcp,,,deny,2
```

```json
{"srcIp":"10.1.2.3","srcNamespace":"shop","srcPod":"web-1","fqdn":"s3.eu-west-2.amazonaws.com","destPort":443,"proto":"TCP","verdict":"ALLOW","packets":40}
```

Query strings are stripped from paths.

## Looking at it in the desk

| Where | What you get |
|---|---|
| `fbflows` in the [terminal](/desk/terminal/) | Source and destination pairs, grouped, with counts and the top methods and paths. `--source hubble` for one source, `--cluster mesh1` for one cluster. |
| [Agent reach](/desk/agent-reach/) | Requests per destination, flow sources, methods, MCP tools and denials. "Not observed" requires a source covering the agent. |
| `flows` table in [SQL](/desk/sql/) | Envoy and CoreDNS flows and imported flow logs, one row per record. ztunnel and agentgateway flows are read from the logs directly, so use `fbflows` for those. |
| [Coverage](/desk/coverage/) | A `flows` entry for each attached or Hubble file, with the detected format. |
