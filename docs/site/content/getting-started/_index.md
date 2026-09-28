---
title: Getting started
weight: 10
description: Install Flightbay, open the sample bundle, take your first capture and send it to Solo.
---

# Getting started

This page takes you from nothing to an open workspace in a few minutes: install the two binaries, open the sample bundle that ships in the repository, then capture a cluster of your own.

## Install

### From source

You need Go 1.27, and a C compiler for `flightbay-desk`, because it embeds DuckDB through cgo. The collector (`flightbay`) is pure Go and builds without one.

```bash
git clone https://github.com/tjorourke/flightbay && cd flightbay
make install      # installs flightbay and flightbay-desk into $(go env GOPATH)/bin
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`. `make build` puts both binaries in `./bin` instead.

If you only need to capture a cluster, `flightbay` on its own is enough. `flightbay ui` looks for `flightbay-desk` next to itself and then on `PATH`.

### Release binaries

Release builds are set up (`.goreleaser.yaml` and `.github/workflows/release.yml`) but nothing has been published yet. When a release is cut it will include:

| Artefact | What it is |
|---|---|
| `flightbay_<version>_<os>_<arch>.tar.gz` | The collector for Linux, macOS and Windows on amd64 and arm64, with a signed checksum file and an SBOM |
| Homebrew cask `flightbay` | The collector, from the `tjorourke/homebrew-tap` tap |
| krew plugin `flightbay` | The collector as `kubectl flightbay` |
| `flightbay-desk` | Built natively per platform, because of cgo |
| `ghcr.io/tjorourke/flightbay-desk` | A container image of the desk (see below) |

Until then, build from source.

### Container image

The `Dockerfile` builds an image of the desk with `kubectl`, `k9s`, `istioctl`, `stern`, `jq` and `yq` included, which is handy on a machine without those tools:

```bash
docker build -t flightbay-desk .
docker run --rm -it -p 8484:8484 -v "$PWD:/data" flightbay-desk \
  open /data/flightbay-01234567-20260927T1012Z.tar.zst \
  --listen 0.0.0.0:8484 --allow-remote --no-browser
```

Inside a container the desk has to listen beyond loopback, so it needs `--allow-remote`. The token is still required: open the `UI:` address it prints and change the host to `localhost`.

## Try the sample bundle

`examples/sample-bundle.tar.zst` is a capture of a small kind cluster running Istio ambient, a waypoint and a shop app with faults planted in it: a crash loop, an image that will not pull, a pod that cannot be scheduled, a route to a missing Service and a Service pointing at a waypoint that does not exist.

```bash
flightbay ui examples/sample-bundle.tar.zst
```

Your browser opens on the workspace. If it does not, open the `UI:` address the command prints, token included. Press Ctrl+C in the terminal to close it; the workspace is deleted when you do.

Things to try:

- **Findings.** The five planted faults, each with its evidence and a next step. See [Findings](/desk/findings/).
- **Search.** `checkout`, `kind:pod CrashLoopBackOff`, `label:app=checkout`, `"registry.invalid"`. See [Search](/desk/search/).
- **Log explorer.** Click the **Errors** example, drag across the chart to zoom into a spike, then click a line and follow its `trace_id` across pods. See [Log explorer](/desk/logs/).
- **Proxy configs.** The waypoint's listeners, clusters, routes and certificates, istiod's view of which proxies are in sync, and ztunnel's workloads. See [Proxy configs](/desk/proxy-configs/).
- **k9s.** Press **Open in k9s** on Resources, or **k9s** in the terminal bar.
- **Terminal.** Open it at the bottom (Ctrl+Backtick) and run `k get pods -n payments`, `k logs`, `k9s`, `fbsearch` or `fbq`. See [Terminal](/desk/terminal/).

The button at the top right switches between light and dark.

## Your first capture

Start with a dry run. It reads API discovery only, and prints what would be collected and the RBAC it needs:

```bash
flightbay collect --context my-cluster --dry-run
```

When the plan looks right, run it for real. The default `standard` profile collects objects, events, the last 24 hours of logs and Helm release summaries from every namespace you can read:

```bash
flightbay collect --context my-cluster --case 01234567
```

It writes `flightbay-01234567-<time>.tar.zst` in the current directory and prints the command to review it. Some common variations:

```bash
flightbay collect --context my-cluster --namespaces payments,istio-system --since 2h
flightbay collect --context my-cluster --solo-diagnostics      # add proxy dumps, istiod debug output and metrics
flightbay collect --context mesh1,mesh2                         # two clusters in one bundle
```

[Collecting a snapshot](/collect/) explains every option.

## Check it and send it to Solo

Before you send anything, look at what is in it:

```bash
flightbay inspect flightbay-01234567-*.tar.zst --redactions
```

That verifies every checksum, lists what was collected and what failed, and shows how many values the redactor replaced in each file. It never shows the values themselves.

To make sure only Solo can read the file, encrypt it to the age recipient your Solo contact gives you:

```bash
flightbay collect --context my-cluster --case 01234567 --encrypt-to age1...
```

Flightbay does not upload anything. Send the file the way you normally share files with Solo support. [Sending a bundle](/collect/sending/) covers encryption, verification and the file format.
