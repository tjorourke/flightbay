---
title: Environment variables
weight: 20
description: Every environment variable flightbay and flightbay-desk read, and the ones the test scripts use.
---

# Environment variables

## flightbay and flightbay-desk

| Variable | Read by | Default | Effect |
|---|---|---|---|
| `KUBECONFIG` | `flightbay` | `~/.kube/config` | The kubeconfig to read contexts from, as with `kubectl`. `--kubeconfig` overrides it. |
| `FLIGHTBAY_HOME` | `flightbay-desk`, `flightbay` | `~/.flightbay` | Where workspaces (`workspaces/ws-*`), `desks/` (one entry per open desk), `current.json` (the most recently opened desk) and `trusted-keys/*.pub` (public keys that bundle signatures are checked against, for the desk and `flightbay inspect`) live. The container image sets it to `/home/flightbay/.flightbay`. |
| `FLIGHTBAY_URL` | `flightbay-desk` helpers, `shell`, `mcp` | the newest open desk in `desks/` | The desk to talk to. Set in workspace shells. Used together with `FLIGHTBAY_TOKEN`; when both are set, the registry is not read and `mcp` does not offer the `workspaces` tool. |
| `FLIGHTBAY_TOKEN` | `flightbay-desk` helpers, `shell`, `mcp` | the newest open desk in `desks/` | The desk's token. Set in workspace shells. |
| `FLIGHTBAY_ENTERPRISE` | `flightbay-desk` | off | `1`, `true`, `yes` or `on` turns on every [Flightbay Enterprise](/enterprise/) feature. |
| `FLIGHTBAY_FEATURES` | `flightbay-desk` | none | Comma-separated Enterprise features to turn on: `priorities`, `reach`. `all` or `enterprise` for every one; a leading `-` turns one off. |
| `FLIGHTBAY_DEBUG_HTTP` | `flightbay-desk` | off | Any value logs every request to the snapshot Kubernetes API on standard error, with its status and time. Useful when a client such as k9s misbehaves. |

## Set for you in workspace shells

The in-browser terminal and `flightbay-desk shell` set these. You do not normally set them yourself.

| Variable | Value |
|---|---|
| `KUBECONFIG` | The workspace's kubeconfig, with one `<cluster>@snapshot` context per cluster |
| `PATH` | The workspace's `bin/` (the `fb*` helpers) first |
| `FLIGHTBAY_URL`, `FLIGHTBAY_TOKEN` | This desk |
| `PS1` | A prompt showing the case and snapshot time |

The shell drops `PROMPT_COMMAND`, `BASH_ENV`, `ENV` and exported bash functions from your environment, so hooks from your own shell setup do not break it.

## Development and test scripts

These are read by the test suite and the end-to-end scripts in `test/e2e`, not by the shipped binaries.

| Variable | Used by | Effect |
|---|---|---|
| `UPDATE_DOCS=1` | `go test ./internal/collect` | Regenerates `docs/COLLECTS.md` from the collector's tables |
| `FLIGHTBAY_SCALE` | `go test ./internal/store -run TestScale` | Number of log lines for the scale test, e.g. `10000000` |
| `FLIGHTBAY_POSTURE_BUNDLE` | `go test ./internal/posture` | A real bundle to run the Scorecard against |
| `FLIGHTBAY_REACH_BUNDLE`, `FLIGHTBAY_REACH_JSON` | `go test ./internal/reach` | A real bundle to run Agent reach against, and a file to write the report to |
| `MESH_CONTEXT`, `MESH_NAMESPACES` | `test/e2e/mesh.sh` | The existing ambient cluster for read-only proxy diagnostics tests |
| `KEEP=1` | `test/e2e/reach.sh` | Keep the kind cluster afterwards; a rerun reuses it |
| `CLUSTER` | `test/e2e/reach.sh` | kind cluster name (default `flightbay-reach`) |
| `REACH_KUBECONFIG` | `test/e2e/reach.sh` | The kubeconfig file the test creates and uses |
| `ISTIOCTL`, `ISTIO_HUB`, `ISTIO_TAG` | `test/e2e/reach.sh` | Which `istioctl` and which Istio images to install |
| `GATEWAY_API` | `test/e2e/reach.sh` | Gateway API CRD version to install |
