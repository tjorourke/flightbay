---
title: Getting started
weight: 10
description: Install Flightbay, open the sample bundle, take your first capture and send it to Solo.
---

# Getting started

Install the binaries and open the sample bundle before collecting from your own cluster.

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

## The start page

Run `flightbay ui` with no arguments and your browser opens on a start page that asks what you want to open:

```bash
flightbay ui
```

![The start page](/images/start.png)

It has three cards. Click one and it opens its form in place.

- **Live stream a cluster.** Pick a context from your kubeconfig (`$KUBECONFIG` or `~/.kube/config`, or type the path of another file). The current context is preselected. Narrow it to some namespaces (comma separated, globs allowed) or exclude some. Cluster-scoped objects and Solo diagnostics are on by default: when you pick a context the page asks the cluster whether its credentials can list cluster-scoped objects and port-forward to pods, and turns an option off, with the reason, when they cannot. They sit under **Options** if you want to turn one off yourself. **Preview** reads API discovery only and shows what will be listed and watched and the RBAC it needs. **Start live** opens the desk on a read-only mirror of the cluster. See [Live mode](/desk/live/).
- **Open a file.** Lists the `flightbay-*.tar.zst` and `.tar.zst.age` files in the current directory and in `~/Downloads`, newest first, with the case, clusters, capture time and size. Each has **Open**, to investigate it, and **Review before sending**, which opens it in review mode as `flightbay review` does. You can also drop a bundle on the page (it is copied into the session's workspace), or paste a path. An encrypted bundle needs the identity file that opens it. See [Reviewing before you send](/collect/review/).
- **Open a repo.** A local directory, or a Git URL, which is cloned with `git clone --depth 1` into the workspace using your own git credentials. The scan options are the ones `flightbay scan` takes: values files, `--set` pairs, an overlay, an environment, a namespace and a release name. **Scan and open** shows the scan's progress, then opens the declared-state workspace. A URL that cannot be cloned, a directory with no manifests, or a chart that does not render is reported on the page. See [Scanning a repository](/scan/).

The footer shows the command line for what you have chosen, for example `flightbay ui --live --context kind-mesh1 --namespaces kagent`, so you can skip the start page next time.

When you pick something, the start page hands over to the desk on the same address and with the same session, so the tab carries on without a new sign-in. Flags given to `flightbay ui`, such as `--listen`, `--no-browser`, `--workdir`, `--keep` and `-i`, carry over to the desk it opens.

`flightbay ui` with a bundle, `flightbay ui .` for the newest bundle in this directory, and `flightbay ui --live ...` skip the start page, as before.

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

After reviewing the plan, run the capture. The default `standard` profile collects objects, events, the last 24 hours of logs and Helm release summaries from the namespaces you can read:

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

To decide exactly what leaves, open the capture in the UI first. On the Review and send page you can take out namespaces, types, objects and log lines, then export a signed copy:

```bash
flightbay keygen --name "Your team"
flightbay ui flightbay-01234567-*.tar.zst --sign flightbay-signing.key
```

See [Reviewing before you send](/collect/review/) and [Signing](/collect/signing/).

Flightbay does not upload anything. Send the file the way you normally share files with Solo support. [Sending a bundle](/collect/sending/) covers encryption, verification and the file format.

## Examples

- [Collect examples](/collect/examples/): capture commands for one namespace, the agentic stack, Istio ambient, several clusters, sharing less and encrypting.
- [Scan examples](/scan/examples/): check a repository, a Helm chart or a Kustomize overlay before it is deployed.
