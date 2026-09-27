# Flightbay by Solo

Flightbay takes an offline, redacted snapshot of one or more Kubernetes clusters and lets Solo support investigate it as if it were live: `kubectl`, `k9s` and `stern` against the snapshot, a log explorer, findings that know about Istio ambient, agentgateway and kgateway, and the Envoy, ztunnel and agentgateway config dumps.

There are two binaries:

- **`flightbay`**, the collector the customer runs. It's a single static Go binary with no runtime dependencies. The customer chooses what it reads, it redacts in memory, and it writes one `tar.zst` bundle.
- **`flightbay-desk`**, where support opens a bundle. You can start browsing and searching before the bundle has finished loading.

## Collecting (customer side)

```bash
flightbay collect --context mesh1 --namespaces payments,istio-system --case 01234567 --dry-run   # what it would read, and the RBAC it needs
flightbay collect --context mesh1 --namespaces payments,istio-system --case 01234567              # writes flightbay-01234567-<time>.tar.zst
flightbay inspect flightbay-01234567-*.tar.zst --contents --redactions                             # review before sending
```

Useful options:

- `--profile minimal|standard|custom`, `--config collection.yaml`, `--interactive`: start from a profile, a saved policy, or prompts. `--save-config` writes the resolved policy for repeat runs.
- `--collect objects,logs,events,...`, `--exclude-resources configmaps,secrets`, `--namespaces` / `--exclude-namespaces` (globs), `--label-selector`, `--cluster-scoped`.
- `--since 2h`, `--max-log-bytes 10MiB`, `--log-streams current,previous`, `--no-logs`.
- `--solo-diagnostics`: adds proxy config dumps, control-plane debug output and metrics from ztunnel, Envoy, agentgateway, istiod and gateway controllers. These need port-forward, and this flag is what allows it. `--no-portforward` refuses port-forward outright.
- `--encrypt-to age1...`: only Solo support's identity can open the bundle.
- `flightbay rbac --namespaces payments` prints least-privilege RBAC for the same policy.
- `flightbay upload BUNDLE --server URL --case 01234567` uploads to a hosted desk and resumes after interruptions.

[docs/COLLECTS.md](docs/COLLECTS.md) lists exactly what is collected and what never is. It's generated from the collector's own tables (`flightbay docs collects`), and a test fails if the two drift apart.

Secret values are never collected. Output from proxies and the control plane is filtered to the selected namespaces before anything is written. A category that can't honour an exclusion is skipped, with the reason recorded, instead of collecting a second copy through another endpoint.

## Investigating (support side)

```bash
flightbay-desk open flightbay-01234567-20260927T1012Z.tar.zst      # UI in the browser, while it loads
flightbay-desk open bundle.tar.zst.age -i ~/.flightbay/support.key
flightbay collect -o - | flightbay-desk open -                       # straight from the collector
flightbay-desk open after.tar.zst --baseline before.tar.zst          # compare two bundles from the same customer
```

Once it's open:

- **kubectl, k9s and stern.** `export KUBECONFIG=<printed path>` or `flightbay-desk shell`. Each cluster gets a `<context>@snapshot` context. Writes, `exec` and `port-forward` are refused with the snapshot time. Types that are still loading return a retryable error instead of a misleading empty list.
- **Helpers.** `fbq 'level:error ns:payments "timed out"'` searches logs, `fbfind` lists findings, and `fbpc ztunnel-abc12` shows a captured proxy or istiod summary (`--file config_dump.json` gives the raw file).
- **UI.** Overview, resources with the same columns as kubectl, the log explorer (facets, histogram, trace pivots, lines arriving while you read), timeline, findings, proxy configs, compare (across clusters or against a baseline bundle) and coverage. Coverage shows what the customer excluded, what failed and what was truncated.
- **Diff.** `flightbay-desk diff before.tar.zst after.tar.zst` shows objects added, removed and changed, image changes, and findings that appeared or went away. An object only counts as removed when both bundles covered it.
- **AI agents.** `flightbay-desk mcp` serves the workspace over MCP (stdio) with read-only tools and a `triage` prompt. For example, `claude mcp add flightbay -- flightbay-desk mcp`. The desk also serves MCP over HTTP at `/mcp`. Snapshot data reaches a model only if the agent you connect uses one.

Findings are signals, not verdicts. Rules that depend on something being absent, such as a missing backend or a missing waypoint, wait until the relevant data is completely loaded, and report nothing for excluded data.

## Hosted desk

`flightbay-desk serve` runs a shared desk for many bundles behind an SSO proxy. [deploy/hosted/flightbay-hosted.yaml](deploy/hosted/flightbay-hosted.yaml) sets it up with an oauth2-proxy sidecar:

- **Identity** comes only from the proxy's headers. The desk listens on the pod's loopback address, so the sidecar is the only way in.
- **Access is per case.** `--support-group` members can open every case, and `--acl` grants named users or groups specific cases. Other users get a 404, not a hint that the case exists.
- **Uploads resume and open while they arrive.** The UI and `flightbay upload` send offset-checked chunks, and the importer reads the growing file, so the workspace is searchable before the upload finishes. The case is fixed at upload time and checked against `bundle.json`.
- **Storage.** Bundles are stored as uploaded (encrypted if the customer used `--encrypt-to`). Workspaces live in memory, are rebuilt on demand and are evicted when idle.
- **Audit and retention.** Uploads, opens, downloads, deletions and denied requests go to `audit.jsonl`. `--retention 30d` deletes bundles after that long.
- **No terminal.** A hosted shell needs a sandboxed pod per session, so engineers download the bundle and use `flightbay-desk open` for kubectl and k9s.

## Building and testing

```bash
go test ./...                                 # unit tests
test/e2e/e2e.sh                               # kind cluster with seeded faults: collector, desk, kubectl, k9s, progressive loading
MESH_CONTEXT=kind-mesh1 test/e2e/mesh.sh      # Solo diagnostics against an existing ambient cluster (read-only)
FLIGHTBAY_SCALE=10000000 go test ./internal/store -run TestScale -v -timeout 60m
docker build -t flightbay-desk .              # desk image with kubectl, k9s, istioctl, stern, jq, yq
```

Releases use goreleaser (`.goreleaser.yaml`). It builds static binaries for Linux, macOS and Windows, then produces checksums, SBOMs, keyless cosign signatures, the `ghcr.io/…/flightbay-desk` image, Homebrew casks and the `kubectl flightbay` krew plugin.

## Status

Built against the plan in `flightbay-plan.md`. Still open:
- the licence;
- the owner of the Solo support age key and how it's rotated;
- the hosted cloud and region;
- a disk-backed log store for bundles beyond about 20 million lines.
