---
title: Scanning a repository
weight: 25
description: Build a bundle from the manifests, Helm charts and Kustomize overlays in a Git repository, and score it before anything is deployed.
---

# Scanning a repository

For worked examples with Helm charts, Kustomize overlays, environments and Argo CD, see [Scan examples](/scan/examples/).

`flightbay scan` builds a bundle from a code repository instead of a live cluster. It renders what the repository declares (plain manifests, Helm charts, Kustomize overlays, and the Argo CD and Flux objects that point at them) and writes the same kind of bundle `flightbay collect` writes. The desk, the [Scorecard](/desk/scorecard/), [Agent reach](/desk/agent-reach/), [Search](/desk/search/), [SQL](/desk/sql/) and [Compare](/desk/compare/) all work on it without changes.

Use it to check a change before it reaches a cluster, to gate pull requests in CI, and to compare what Git says with what is running.

```bash
flightbay scan .                                   # render, score, write flightbay-scan-<repo>-<time>.tar.zst
flightbay-desk open flightbay-scan-*.tar.zst       # browse it like a capture
```

## What it reads

The scan walks the paths you give it (default `.`). It skips `.git`, `node_modules`, `vendor` and hidden directories, and in a Git repository it reads only files Git does not ignore.

| Input | How it is found | How it is rendered |
|---|---|---|
| Plain manifests | `.yaml`, `.yml` and `.json` files | Every document with `apiVersion` and `kind`. Multi-document files and `kind: List` are expanded. Files that are not Kubernetes objects (CI configs, values files) are skipped and listed in coverage. |
| Helm charts | A directory with `Chart.yaml` | The Helm Go SDK, as `helm template --include-crds` does, with no cluster. `values.yaml`, then `--values` files, then `--set`. Subcharts vendored in `charts/` are rendered with their parent. Test hooks are left out. |
| Kustomize overlays | A directory with `kustomization.yaml` | Built with the Kustomize Go API. By default every kustomization that is not only used as a base by another one is built. |
| Argo CD `Application` and `ApplicationSet` | Found in any rendered output | When `spec.source.repoURL` is this repository and the path exists, the path is rendered as a chart, an overlay or a directory, with the Application's value files, inline values, parameters and destination namespace. |
| Flux `HelmRelease` and `Kustomization` | Found in any rendered output | When the source is a `GitRepository` whose URL is this repository, the chart or path is rendered with its values and `targetNamespace`. |

A GitOps object whose source is somewhere else (another repository, a Helm repository, an OCI chart, or an ApplicationSet template that needs generator parameters) is recorded in coverage as an unresolved source. Nothing is fetched.

The repository is matched on its `origin` remote, in any of the usual forms (`https://`, `git@host:`, `ssh://`). Pass `--repo-url` when the remote is different from the URL the GitOps objects use, for example a mirror.

## Choosing values, overlays and environments

| Flag | Effect |
|---|---|
| `--values FILE` | A values file for every chart. It is looked for in the chart directory first, then relative to the working directory. Repeatable. |
| `--values CHARTDIR=FILE` | A values file for one chart only. |
| `--set key=value` | A Helm value, as `helm --set`, for every chart. The values are not recorded in the bundle, only the keys. |
| `--release NAME` | The Helm release name. The default is the chart name. |
| `--namespace NS` | The namespace for namespaced objects that have none. The default is the chart's `flightbay.dev/namespace` annotation in `Chart.yaml`, then `default`. |
| `--overlay DIR` | Build this kustomization. Repeatable. With it, no other overlay is built. |
| `--env NAME` | Pick an environment: `overlays/NAME`, `envs/NAME`, `environments/NAME`, `clusters/NAME` and `values-NAME.yaml` in each chart. Overlays for other environments are skipped. It prints what it picked. |
| `--root DIR` | The repository root that paths in the bundle and in Argo CD Applications are relative to. The default is the Git top level. |
| `--context-name NAME` | The name of the synthetic cluster in the bundle. The default is `repo:<name>`. |
| `--helm-fetch` | Run `helm dependency update` for charts whose dependencies are not vendored. This uses the network. The chart is copied to a temporary directory first, so your repository is not changed. |

Without `--env` or `--overlay`, a repository with `overlays/dev` and `overlays/prod` renders both. When the same object comes out of more than one input, the scan keeps the first and lists the duplicates in coverage, so pick one environment for a clean result.

```bash
flightbay scan deploy/ --env prod
flightbay scan charts/api --values charts/api=values-prod.yaml --set image.tag=1.4.2 --namespace api
flightbay scan . --overlay k8s/overlays/prod --overlay k8s/overlays/prod-eu
```

## What is in the bundle

| Part | Content |
|---|---|
| Header | `source: repo`, the repository name, remote (without credentials), commit, branch, whether there were uncommitted changes, and the render options. |
| Cluster | One synthetic cluster, `repo:<name>`. |
| Resources | Grouped by group, version and resource, exactly as a capture groups them, with discovery written from the kinds found. CRDs in the repository are used for their plural name and scope. |
| Coverage | One entry per input: rendered, skipped with the reason, or failed with the error. Missing chart dependencies and unresolved GitOps sources are noted here. |
| Source of each object | The annotation `flightbay.dev/source` holds the file the object came from, relative to the repository root: the manifest, the template inside the chart, or the file inside the base for an overlay. `flightbay.dev/source-line` holds the line where known, and `flightbay.dev/rendered-by` says how it was rendered. The desk shows them in the object drawer. |

Secrets and ConfigMaps are redacted by the same code the collector uses. A Secret keeps its name, type, labels and annotations, and its key names with their byte lengths. Values in `data` and `stringData` never reach the bundle. `flightbay inspect` works on a scan bundle as it does on a capture.

## Checks on declared state

A repository has no status, no pods, no logs and no events. The desk says "Declared state from repo X at commit abc1234" in place of "Captured", and the checks that need a running cluster say so instead of failing:

- policy `Accepted` and `Attached` conditions, pod IPs and ready replicas, HPA status, Helm release status, node health, the cluster version and certificate expiry are not applicable, with the reason `declared state only (repo scan)`;
- the checks that look at pods run on the pod template of each Deployment, StatefulSet, DaemonSet, Job and CronJob, and report on the workload;
- Agent reach builds its workloads from the same templates, and has no observed traffic;
- the findings that read pod status, logs and proxy dumps do not run, and one information finding says why.

A missing object is judged against what the scanned paths declare. If your NetworkPolicies live in a platform repository, scan both paths together, or expect the default-deny checks to fail. Secrets are the exception: they are usually created outside Git, so a missing Secret is never counted against you.

## Limits

- Helm charts are rendered with the default Kubernetes capabilities of the Helm SDK. Templates that call `lookup` get nothing back, as with `helm template`.
- Kustomize remote bases and the `helmCharts` generator are not built. The overlay is marked failed in coverage with the reason.
- ApplicationSet generators are not expanded. An ApplicationSet whose template has no parameters is followed like an Application.
- Flux `valuesFrom` (values from ConfigMaps and Secrets in the cluster) and `chartRef` sources are not read.
- Objects a controller creates at runtime are not in the repository: the Service in front of a Gateway, the Deployment kagent makes for an Agent, the waypoint pods. Checks that depend on them are not applicable on a scan.

## Next

- [Using it in CI](/scan/ci/): `--fail-on`, SARIF, GitHub and GitLab.
- [Repo compared with the cluster](/scan/drift/): what Git declares and what is running.
- [Fix packs](/scan/fix-packs/): YAML for each failing check, mapped back to the source files.
