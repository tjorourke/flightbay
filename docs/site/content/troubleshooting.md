---
title: Troubleshooting
weight: 70
description: Common problems when collecting a snapshot or opening one, and what to do about each.
---

# Troubleshooting

## Collecting

### RBAC denied some types

The collector does not stop at the first forbidden call. Each one is recorded, and the capture carries on with what it can read. `flightbay inspect` shows them as `failed: forbidden: ...` in the coverage list and under "Collection log problems".

Fix it by granting the missing access, or by leaving those types out on purpose:

```bash
flightbay rbac --discover --context mesh1 > flightbay-rbac.yaml     # least privilege for your policy
flightbay collect --exclude-resources clusterroles.rbac.authorization.k8s.io,clusterrolebindings.rbac.authorization.k8s.io
```

In the desk, a type that failed is never shown as empty: [Resources](/desk/resources/) says the collection failed, Findings adds a `collection.gap` finding, and Scorecard checks that need it are not applicable. See [RBAC](/collect/rbac/).

### Port-forward is blocked

Diagnostics and Hubble flows need `pods/portforward` in the namespaces they read from. When port-forward fails (RBAC, an admission policy, a proxy in the way), each affected file is recorded as failed with the error, and the rest of the capture carries on.

- Check the RBAC: `flightbay rbac --solo-diagnostics --namespaces ...` includes `pods/portforward`.
- If port-forward is not allowed in your environment, capture without diagnostics: `--no-portforward`. The dry run then lists them under "Not collected" with the reason.
- Port-forward tries WebSockets first and falls back to SPDY. A corporate proxy that blocks both will stop it.

### "helm: release summaries are read from Secret data"

Helm summaries are decoded from Helm's release Secrets in memory. They need `--secrets keys` and Secrets not excluded. With `--secrets metadata` or `omit`, the dry run says Helm will not be collected, and why.

### "nodes: cluster-scoped data needs --cluster-scoped"

Nodes, CRDs, ClusterRoles and other cluster-scoped types need `--cluster-scoped`.

### `--max-log-bytes 10MiB` is rejected

`--max-log-bytes` and `--max-bundle-bytes` take a plain number of bytes: `--max-log-bytes 10485760`.

### Hubble flows: "hubble CLI not found"

`--hubble-flows` runs your local `hubble` binary through a port-forward to `hubble-relay`. Install the Hubble CLI and make sure it is on `PATH`; the dry run says whether it was found. It also needs `pods` get and list and `pods/portforward` create in `kube-system`.

### No CoreDNS lines in observed traffic

The CoreDNS `log` plugin is off by default. Add `log` to the Corefile before capturing. See [Observed traffic](/collect/observed-traffic/).

### The capture was interrupted

Ctrl+C, or a lost connection, leaves `NAME.partial`. It has no manifest and fails verification. Run the capture again. If one cluster of several fails, the others are still collected and that cluster is marked incomplete in the collection log.

## Opening a bundle

### "flightbay-desk is not installed next to flightbay or on PATH"

`flightbay ui` hands over to `flightbay-desk`. Install both (`make install`), or put `flightbay-desk` next to `flightbay`.

### "bundle is encrypted with age; provide an identity file"

The bundle ends in `.age`. Pass the identity that matches the recipient it was encrypted to:

```bash
flightbay ui flightbay-01234567-20260927T1012Z.tar.zst.age -i ~/.flightbay/support.key
```

"decrypt bundle: ..." means the identity does not match any recipient. Ask for the bundle to be encrypted to your recipient (`flightbay-desk keygen` prints it).

### "bundle failed verification" or "bundle is incomplete"

| Message | Likely cause |
|---|---|
| `bundle is incomplete: manifest.json missing (truncated upload or interrupted collection)` | The file was cut short in transfer, or the capture was interrupted |
| `bundle failed verification: ...` | A file's SHA-256 does not match the manifest: the file was damaged or changed |
| `first member must be bundle.json` | Not a Flightbay bundle, or it was repacked |
| `unsupported bundle schema N` | The bundle was written by a newer collector than this desk understands |

Ask for the file again, and compare its size with the original. The desk still opens what it could read, clearly marked as unverified, and findings stay provisional.

### Building flightbay-desk fails with a cgo or DuckDB error

`flightbay-desk` embeds DuckDB, so it needs cgo and a C compiler: Xcode command line tools on macOS (`xcode-select --install`), `build-essential` or equivalent on Linux. Build it with `CGO_ENABLED=1` (the Makefile does). `CGO_ENABLED=0` works only for the collector, `flightbay`. If a local toolchain is a problem, build the [container image](/getting-started/).

### "cannot create the workspace database"

The workspace directory could not be created or written. Check the space and permissions under `~/.flightbay/workspaces` (or `FLIGHTBAY_HOME`, or your `--workdir`). A workspace needs about 90 bytes per log line.

### Port 8484 is in use

If the default `127.0.0.1:8484` is taken, the desk picks a free port and prints it. For a fixed port, pass `--listen 127.0.0.1:9000`.

### "--listen must be a loopback address in local mode"

The desk only listens on loopback unless you pass `--allow-remote`, which is meant for containers. The token is still required.

### The browser says "not signed in" or "invalid token"

Open the exact `UI:` address the desk printed, token included. Each desk makes a new token, so an old tab from a previous desk will not work.

### Helpers say "no open workspace found"

`fbq`, `fbsql`, `flightbay-desk shell` and `flightbay-desk mcp` find the desk through `~/.flightbay/current.json`, which is written when a desk opens. Open a bundle first, and make sure `FLIGHTBAY_HOME` is the same in both terminals. Inside the desk's own terminal, `FLIGHTBAY_URL` and `FLIGHTBAY_TOKEN` are set for you.

### k9s shows odd state or writes to your k9s config

k9s keeps per-context state in its config directory, and the snapshot contexts (`<cluster>@snapshot`) end up there next to your real ones. To keep them apart, point k9s at a throwaway directory:

```bash
K9S_CONFIG_DIR=$(mktemp -d) k9s --readonly
```

Buttons in the UI already run k9s with `--readonly`. If k9s misbehaves against the snapshot, run the desk with `FLIGHTBAY_DEBUG_HTTP=1` to see every request it makes and the status it gets.

### kubectl says "MethodNotAllowed: this is a read-only snapshot"

That is expected: the snapshot cannot be changed. `Forbidden: ... was excluded by the customer's collection policy` means that type was not collected on purpose.

## Reading the results

### A Scorecard check says "not applicable" or "was not captured"

A check is not applicable when the data it needs was not captured (excluded by the policy, a failed collection, or cluster-scoped data without `--cluster-scoped`), or when there is nothing for it to check. The reason is on the check, for example "clusterrolebindings.rbac.authorization.k8s.io was not captured (excluded)". Not applicable checks do not lower the score. To evaluate them, capture again with the data included.

A whole area can be "not scored" when the capture has nothing it applies to, such as no Gateway API ingress.

### Proxy configs is empty

Diagnostics were not selected. Capture with `--solo-diagnostics`. Diagnostics are written last in a bundle, so while it is still loading they may simply not have arrived.

### Agent reach says "the network is flat"

No NetworkPolicy or AuthorizationPolicy exists in the capture, so every agent can reach every Service and the internet. If policies do exist, check [Coverage](/desk/coverage/): when `networkpolicies` or `authorizationpolicies` were not captured, verdicts are unknown rather than allowed, and the page says so. CNI-specific policies (Cilium, Calico, AdminNetworkPolicy) are not evaluated yet.

### "Used" and "Never used" show a dash

No flow source covers that agent. ztunnel logs cover agents in the ambient mesh; otherwise attach flow logs. See [Observed traffic](/collect/observed-traffic/).

### Agent reach is missing from the sidebar

It is a [Flightbay Enterprise](/enterprise/) feature. Start the desk with `FLIGHTBAY_ENTERPRISE=1` or `FLIGHTBAY_FEATURES=reach`.
