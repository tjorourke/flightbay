---
title: Getting started
weight: 10
description: Install Flightbay, open the sample bundle and take your first capture.
---

# Getting started

This page takes you from nothing to an open workspace in a few minutes: install the two binaries, open the sample bundle that ships in the repository, then capture a cluster of your own.

## Install

### Homebrew on macOS

```bash
brew tap tjorourke/flightbay https://github.com/tjorourke/flightbay
brew install flightbay
```

If Homebrew reports that the formula is untrusted, run `brew trust --formula tjorourke/flightbay/flightbay`, then repeat the install. Homebrew 7 requires this for third-party formulae.

The formula installs the collector and the desk. To upgrade both:

```bash
brew update
brew upgrade flightbay
```

### From source

You need Go 1.27, and a C compiler for `flightbay-desk`, because it embeds DuckDB through cgo. The collector (`flightbay`) is pure Go and builds without one.

```bash
git clone https://github.com/tjorourke/flightbay && cd flightbay
make install      # installs flightbay and flightbay-desk into $(go env GOPATH)/bin
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`. `make build` puts both binaries in `./bin` instead.

If you only need to capture a cluster, `flightbay` on its own is enough. `flightbay ui` looks for `flightbay-desk` next to itself and then on `PATH`.

### Release binaries

Download published binaries from [GitHub Releases](https://github.com/tjorourke/flightbay/releases/latest):

| Artefact | What it is |
|---|---|
| `flightbay_<version>_<os>_<arch>.tar.gz` | The collector for Linux and macOS on amd64 and arm64, with checksums and an SBOM |
| `flightbay_<version>_windows_<arch>.zip` | The Windows collector |
| Homebrew formula `flightbay` | The collector and desk, from this repository's tap |
| `flightbay-desk_<version>_<os>_<arch>.tar.gz` | The desk for macOS and Linux, built natively per platform because of cgo |
| `ghcr.io/tjorourke/flightbay-desk` | A container image of the desk (see below) |

For direct macOS and Linux installations from version 0.2.5, upgrade in place:

```bash
flightbay upgrade --check
flightbay upgrade
```

The command verifies release SHA-256 checksums, preserves installation symlinks and updates a desk installed beside the collector. Use `--collector-only` to leave the desk alone, or `--version v0.2.5` to choose a published stable version. Homebrew installations use `brew upgrade flightbay`; Windows installations download and replace the binaries manually. Restart a running desk after an upgrade.

An earlier release needs a one-time download or a Homebrew install to get the upgrade command. A push to `main` alone does not publish binaries; a new version tag runs the release workflow and updates the Homebrew formula.

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

- **Live stream clusters.** Tick one or more contexts from the default kubeconfig (`$KUBECONFIG` or `~/.kube/config`). You can also choose multiple self-contained kubeconfig files, or enter their local paths one per line and load their contexts. The current context is preselected. Filter the list and use **Select visible** or **Clear selection**. Choose namespace scope and options; **Preview** reads discovery and shows each cluster's plan and RBAC. **Start live** opens one shared read-only workspace with independent cluster connection status. See [Live mode](/desk/live/).
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

When the plan looks right, run it for real. The default `standard` profile collects objects, events, the last 24 hours of logs and Helm release summaries from every namespace you can read:

```bash
flightbay collect --context my-cluster --case 01234567
```

It writes `flightbay-01234567-<time>.tar.zst` in the current directory and prints the command to review it. Some common variations:

```bash
flightbay collect --context my-cluster --namespaces payments,istio-system --since 2h
flightbay collect --context my-cluster --diagnostics           # add proxy dumps, istiod debug output and metrics
flightbay collect --context mesh1,mesh2                         # two clusters in one bundle
```

[Collecting a snapshot](/collect/) explains every option.

## Review and share the bundle

Before you send anything, look at what is in it:

```bash
flightbay inspect flightbay-01234567-*.tar.zst --redactions
```

That verifies every checksum, lists what was collected and what failed, and shows how many values the redactor replaced in each file. It never shows the values themselves.

To restrict who can read the file, encrypt it to an age recipient you trust:

```bash
flightbay collect --context my-cluster --case 01234567 --encrypt-to age1...
```

To decide exactly what leaves, open the capture in the UI first. On the Review and send page you can take out namespaces, types, objects and log lines, then export a signed copy:

```bash
flightbay keygen --name "Your team"
flightbay ui flightbay-01234567-*.tar.zst --sign flightbay-signing.key
```

See [Reviewing before you send](/collect/review/) and [Signing](/collect/signing/).

Flightbay does not upload anything. Share the file through your chosen secure channel. [Sending a bundle](/collect/sending/) covers encryption, verification and the file format.

## Where next

- [Collect examples](/collect/examples/): capture commands for one namespace, the agentic stack, Istio ambient, several clusters, sharing less and encrypting.
- [Scan examples](/scan/examples/): check a repository, a Helm chart or a Kustomize overlay before it is deployed.
