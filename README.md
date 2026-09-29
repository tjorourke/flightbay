# Solo flightbay

Flightbay collects Kubernetes objects, logs and optional proxy diagnostics in a redacted bundle. A support engineer can open it locally and investigate without access to the original cluster.

```bash
flightbay collect --context my-cluster
flightbay ui .                            # open the newest bundle in this directory
```

The workspace has object and log search, configuration checks, proxy dumps and a read-only Kubernetes API for `kubectl` and `k9s`. It supports Kubernetes, Istio ambient, agentgateway and kgateway.

![Overview of a snapshot](docs/images/overview.png)

## Install

Build from source with Go 1.27 and a C compiler. The C compiler is required by `flightbay-desk`, which embeds DuckDB. The collector is pure Go.

```bash
git clone https://github.com/tjorourke/flightbay
cd flightbay
make install
```

Add `$(go env GOPATH)/bin` to your `PATH`. `make build` writes both binaries to `./bin` instead. Release packaging is configured, but Flightbay is unreleased.

## Try the sample

```bash
flightbay ui examples/sample-bundle.tar.zst
```

The sample contains an Istio ambient kind cluster with five planted faults. Open **Findings** to inspect them, or search for `kind:pod CrashLoopBackOff`. In the terminal (Ctrl+Backtick), try `k get pods -n payments`, `k9s` or `fbq 'level:error'`.

If the browser does not open, use the printed `UI:` address, including its token. Ctrl+C closes the desk and deletes the temporary workspace.

Running `flightbay ui` without arguments opens the start page. Choose a live cluster, a bundle file or a repository to scan. See [Getting started](docs/site/content/getting-started/_index.md).

## Collect and review

Preview the collection policy and required RBAC before collecting:

```bash
flightbay collect --context mesh1 --dry-run
flightbay collect --context mesh1 --namespaces payments,istio-system --since 2h --case 01234567
flightbay collect --context mesh1 --solo-diagnostics    # include proxy and control-plane diagnostics
flightbay inspect flightbay-01234567-*.tar.zst --redactions
```

The default `standard` profile collects namespaced objects, events, Helm release summaries and up to 24 hours of current and previous container logs, capped at 20 MiB per stream. Namespace, resource and log filters reduce the scope. `flightbay rbac` generates RBAC for the selected options.

Secret values are removed, and recognised credentials in other content are redacted in memory before writing. Review the bundle for data specific to your organisation. **Review and send** lets you remove namespaces, objects, logs and diagnostic files, add redaction rules and export a new bundle. Flightbay does not upload it.

```bash
flightbay keygen --name "ACME platform team"
flightbay ui flightbay-01234567-*.tar.zst --sign flightbay-signing.key --encrypt-to age1...
```

Send the public signing key to the recipient and retain the private key. `--encrypt-to` takes the recipient's age public key.

Collection guides: [scope](docs/site/content/collect/scope.md), [logs](docs/site/content/collect/logs.md), [large clusters](docs/site/content/collect/large-clusters.md), [redaction](docs/site/content/collect/secrets-and-redaction.md), [diagnostics](docs/site/content/collect/solo-diagnostics.md), [review](docs/site/content/collect/review.md) and [signing](docs/site/content/collect/signing.md). [COLLECTS.md](docs/COLLECTS.md) is generated from the collector's tables.

## Investigate

| View | Use |
|---|---|
| [Search](docs/site/content/desk/search.md) and [Resources](docs/site/content/desk/resources.md) | Find captured objects by name, kind, labels or content; inspect YAML and related objects. ⌘K opens object search. |
| [Log explorer](docs/site/content/desk/logs.md) | Search logs, select a time range and follow trace or request IDs across pods. |
| [Findings](docs/site/content/desk/findings.md) | Inspect detected failures and the objects or logs behind them. |
| [Scorecard](docs/site/content/desk/scorecard.md) | Review configuration and security checks, with affected objects and proposed fixes. |
| [Proxy configs](docs/site/content/desk/proxy-configs.md) | Read captured Envoy, ztunnel and agentgateway configuration, and istiod debug output. |
| [Timeline](docs/site/content/desk/timeline.md) | Compare events, container exits, rollouts and error spikes by time. |
| [Compare](docs/site/content/desk/compare.md) | Inspect changes between captures or compare an object across clusters. |
| [Coverage](docs/site/content/desk/coverage.md) | Check collected, excluded, failed and verified data. |

[Flightbay Enterprise](docs/site/content/enterprise/_index.md) adds ranked Scorecard priorities, [Agent reach](docs/site/content/desk/agent-reach.md) and [Traffic](docs/site/content/desk/traffic.md). Enable these with `FLIGHTBAY_ENTERPRISE=1 flightbay ui BUNDLE`, or select features with `FLIGHTBAY_FEATURES=priorities,reach,traffic`.

The terminal helpers query the same workspace:

```bash
flightbay-desk shell
fbsearch 'kind:pod CrashLoopBackOff'
fbq 'level:error ns:payments'
fbsql "SELECT namespace, pod, count(*) FROM logs WHERE level = 'error' GROUP BY ALL"
fbpc
fbfind
fbscore
```

See [Terminal](docs/site/content/desk/terminal.md) for helper options, including `fbreach`, `fbtraffic`, `fbactivity` and `fbexplain`.

## Live mode and repository scans

With cluster access, live mode lists and watches the selected objects and follows logs. **Save as bundle** exports the current read-only mirror.

```bash
flightbay ui --live --context prod-eu --namespaces kagent,agentgateway-system,istio-system
```

A repository scan renders manifests, Helm charts and Kustomize overlays into a bundle. Checks that require live data are marked not applicable.

```bash
flightbay scan deploy/ --env prod
flightbay scan . --fail-on high --new-only --base-ref origin/main --format sarif -o flightbay.sarif
flightbay-desk drift repo-scan.tar.zst cluster.tar.zst
flightbay-desk diff before.tar.zst after.tar.zst
```

See [Live mode](docs/site/content/desk/live.md), [Scanning a repository](docs/site/content/scan/_index.md) and [CI](docs/site/content/scan/ci.md).

## MCP access

`flightbay-desk mcp` exposes read-only tools for an open workspace:

```bash
claude mcp add flightbay -- flightbay-desk mcp
```

The server connects to the local desk. An agent using a hosted model may send queried data to that provider. See [MCP server](docs/site/content/desk/mcp.md).

## Bundle and workspace

A bundle is a zstd-compressed tar stream. `bundle.json` records the collection policy. `manifest.json` lists file checksums, followed by `manifest.sig` when signed. The desk imports objects, logs and events into a temporary DuckDB workspace and serves the snapshot API locally. See [Bundle format](docs/site/content/reference/bundle-format.md) and [Security](docs/site/content/security.md).

The complete command references are [flightbay](docs/site/content/reference/cli-flightbay.md) and [flightbay-desk](docs/site/content/reference/cli-flightbay-desk.md).

## Development

```bash
make build
make test
test/e2e/e2e.sh                                 # kind cluster with planted faults
MESH_CONTEXT=kind-mesh1 test/e2e/mesh.sh          # read-only checks against an existing ambient cluster
make e2e-reach                                  # apply proposed policies and check traffic on kind
FLIGHTBAY_SCALE=10000000 go test ./internal/store -run TestScale -v -timeout 60m
docker build -t flightbay-desk .
go run ./scripts/screenshots -mesh BUNDLE        # refresh documentation screenshots
```

## Status

Unreleased. The licence and ownership of customer bundle-encryption keys remain undecided.
