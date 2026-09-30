---
title: "Scan examples"
weight: 5
description: "Worked examples: plain YAML, Helm charts with values, published charts, Kustomize overlays, environments, Argo CD and Flux, then reading the result."
---

Each example below was run against a real repository or chart, and the output shown is what `flightbay scan` printed. The same commands work on your own repository. See [Scanning a repository](/scan/) for every flag.

A scan never talks to a cluster. It reads files, renders them the way your deployment tool would, and scores what comes out. You don't need kubectl access or a kubeconfig.

## Plain YAML manifests

Point it at a directory. Every `.yaml`, `.yml` and `.json` file under it is read, multi-document files and `kind: List` included. Files that aren't Kubernetes objects (no `apiVersion` and `kind`) are skipped and listed in coverage.

```bash
flightbay scan ~/code/solo/solo-demos/sovereign-ai-uk-eks --root ~/code/solo/solo-demos
```

```text
Scanned solo: 103 objects of 27 kinds from 45 inputs (0 failed) in 159ms
Declared state from solo at commit 91d7b6a3eb7c
  Agentic (agentgateway, kagent, MCP)            70  C  (12 pass, 4 warn, 9 fail, 28 not applicable)
  Kubernetes (workload and platform hygiene)     73  C  (6 pass, 9 warn, 5 fail, 9 not applicable)

[CRITICAL] LLM routes require caller authentication (agentic.llm-auth-required)
    sovereign-ai-uk-eks/yaml/24-model-routing.yaml:35  EnterpriseAgentgatewayBackend agentgateway-system/vllm-qwen: OPEN: exposed via HTTPRoute agentgateway-system/model-routing-internal ...
[HIGH] JWT validation runs in Strict mode (agentic.jwt-strict-mode)
    sovereign-ai-uk-eks/yaml/47-ui-auth-exempt.yaml:14  EnterpriseAgentgatewayPolicy agentregistry/agentregistry-ui-auth-exempt: mode: "Permissive"
```

Every problem names the file and the line of the object, so you can go straight to it.

`--root` sets the repository root for the paths in the report (and for Argo CD paths, below). Without it, Flightbay uses the top of the Git repository that contains the path.

You can pass several paths in one scan:

```bash
flightbay scan labs/agentgateway-virtual-keys labs/agentic-tool-privilege-kind labs/sovereign-ai-uk-eks
```

## A Helm chart in your repository

Any directory with a `Chart.yaml` is rendered with the Helm Go SDK, the same way `helm template --include-crds` renders it. Subcharts in `charts/` are rendered with their parent. You don't need the `helm` binary.

```bash
flightbay scan ~/code/solo/agentgateway/controller/install/helm/agentgateway --namespace agentgateway-system
```

```text
  helm controller/install/helm/agentgateway
Scanned agentgateway: 5 objects of 5 kinds from 1 inputs (0 failed) in 133ms
  Kubernetes (workload and platform hygiene)     88  B  (16 pass, 4 warn, 1 fail, 8 not applicable)

[MEDIUM] Application namespaces deny ingress by default (k8s.netpol-default-deny)
```

### With your values

Values are applied in the same order as Helm: the chart's `values.yaml`, then each `--values` file in order, then `--set`.

```bash
# a values file for every chart the scan finds
flightbay scan charts/api --values values-prod.yaml --namespace shop

# a values file for one chart only, when the repository has several
flightbay scan . --values charts/api=charts/api/values-prod.yaml

# individual values, as with helm --set
flightbay scan charts/api --values values-prod.yaml --set image.tag=1.4.2 --set replicaCount=3
```

A `--values` file is looked for in the chart directory first, then relative to where you run the command. Only the keys you `--set` are recorded in the bundle, never the values.

The release name defaults to the chart name. Set it with `--release` if your templates use `.Release.Name` in object names.

Helm's own warnings are shown indented under the chart, once each:

```text
  helm charts/mychart
  helm: skipped value for mychart.image: Not a table.
```

### Chart dependencies

Dependencies already in the chart's `charts/` directory are used. If one is missing, the scan says so in coverage and renders the rest. To fetch missing dependencies, add `--helm-fetch`. That runs the equivalent of `helm dependency update` on a temporary copy of the chart, so your repository is not changed, and it needs network access.

## A published chart

A scan reads files, so pull the chart first, then scan the directory. This is how to check a chart before you install it.

```bash
helm pull oci://ghcr.io/kagent-dev/kagent/helm/kagent --untar -d /tmp/charts
flightbay scan /tmp/charts/kagent --namespace kagent --values my-kagent-values.yaml
```

```text
Scanned kagent: 52 objects of 13 kinds from 1 inputs (0 failed) in 133ms
  Agentic (agentgateway, kagent, MCP)            87  B  (9 pass, 2 warn, 1 fail, 41 not applicable)
  Kubernetes (workload and platform hygiene)     85  B  (13 pass, 3 warn, 4 fail, 9 not applicable)

[HIGH] Images are pinned by digest (k8s.image-pinned)
    charts/grafana-mcp/templates/deployment.yaml  Deployment kagent/kagent-grafana-mcp: container grafana-mcp uses mcp/grafana:latest (no tag or :latest)
[HIGH] No wildcard RBAC rules (k8s.rbac-wildcards)
    charts/kagent-tools/templates/clusterrole.yaml  ClusterRole kagent-tools-cluster-admin-role: verbs * on * in groups [*] ...
```

For a chart in a classic Helm repository, add the repository first, then pull:

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm pull grafana/grafana --untar -d /tmp/charts
flightbay scan /tmp/charts/grafana --namespace monitoring
```

## Kustomize

Any directory with a `kustomization.yaml` is built with the Kustomize Go API. You don't need the `kustomize` or `kubectl` binary.

By default, the scan builds every kustomization that is not only used as a base by another. In a typical layout that means each overlay:

```text
k8s/
  base/            kustomization.yaml, deployment.yaml, service.yaml
  overlays/dev/    kustomization.yaml (resources: [../../base])
  overlays/prod/   kustomization.yaml (resources: [../../base])
```

```bash
# build dev and prod (both overlays)
flightbay scan k8s

# build only prod
flightbay scan k8s --overlay k8s/overlays/prod

# build two overlays and nothing else
flightbay scan k8s --overlay k8s/overlays/prod --overlay k8s/overlays/prod-eu
```

```text
  kustomize k8s/overlays/prod
Scanned repo: 1 objects of 1 kinds from 2 inputs (0 failed) in 158ms
```

Remote bases (`resources: [github.com/...]`) and the `helmCharts` generator are not built yet. The overlay is marked failed in coverage with the reason.

## Pick one environment

When a repository holds several environments, scanning everything renders the same objects more than once. `--env` picks one: it looks for `overlays/NAME`, `envs/NAME`, `environments/NAME` and `clusters/NAME` for Kustomize, and `values-NAME.yaml` in each chart. It prints what it picked.

```bash
flightbay scan . --env prod
```

```text
  --env prod: overlay kustomize/overlays/prod
  kustomize kustomize/overlays/prod
  --env prod: chart charts/mychart: values file charts/mychart/values-prod.yaml
  helm charts/mychart
Scanned repo-sample: 12 objects of 7 kinds from 10 inputs (0 failed) in 147ms
```

To compare two environments, scan each to its own bundle and open one with the other as the baseline:

```bash
flightbay scan . --env staging --bundle staging.tar.zst
flightbay scan . --env prod --bundle prod.tar.zst
flightbay ui prod.tar.zst --baseline staging.tar.zst
```

## Argo CD and Flux repositories

In a GitOps repository, the Kubernetes objects often come from Argo CD `Application` or Flux `HelmRelease`/`Kustomization` resources that point at a path. The scan follows them when the source is the repository being scanned, and renders that path with the Application's values, overlay and target namespace.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: guestbook, namespace: argocd}
spec:
  source:
    repoURL: https://github.com/example/repo-sample.git
    path: apps/guestbook
  destination:
    namespace: guestbook
```

The scan compares `repoURL` with the Git remote of the repository. If your checkout's remote differs (a fork, a mirror, SSH against HTTPS), tell it which URL this repository is:

```bash
flightbay scan . --repo-url https://github.com/example/repo-sample.git
```

Sources in other repositories are listed as unresolved, with the reason:

```text
  Application argocd/external (argocd/guestbook.yaml): unresolved source: repository https://github.com/someone/else.git is not the one being scanned (pass --repo-url if it is)
```

ApplicationSet generators and Flux `valuesFrom`/`chartRef` are not followed yet.

## Look at the result in the desk

Every scan writes a bundle unless you pass `--no-bundle`. Open it like a capture:

```bash
flightbay scan . --env prod --bundle ~/Downloads/myrepo-prod.tar.zst
flightbay ui ~/Downloads/myrepo-prod.tar.zst
```

- The banner says **Declared state from myrepo at commit abc1234**, with "(uncommitted changes)" if your working tree was dirty.
- **[Scorecard](/desk/scorecard/)** scores the rendered objects. Checks that need a running cluster (policy status, pod IPs, logs, Helm release status) are not applicable, with the reason "declared state only (repo scan)". They never count against the score.
- Click any affected object: the drawer shows a **Source** row with the file and line (the template, for Helm).
- **[Search](/desk/search/)**, **[Resources](/desk/resources/)**, **[SQL](/desk/sql/)** and the terminal (`kubectl get`, `k9s`) work on the rendered objects.
- **[Agent reach](/desk/agent-reach/)** works from the workload templates. Objects that a controller creates at runtime (the Deployment kagent makes for an Agent, Gateway Services, waypoints) are not in a repository, so they don't appear.

## Output formats

```bash
flightbay scan . --format text        # the default: a summary and every failing check
flightbay scan . --format json -o scan.json
flightbay scan . --format sarif -o scan.sarif          # GitHub code scanning
flightbay scan . --format github                        # ::error annotations in a GitHub Actions log
flightbay scan . --format codequality -o gl-code-quality.json   # GitLab
```

## Next

- [Using it in CI](/scan/ci/): fail a pull request on critical findings, and only on new ones.
- [Repo compared with the cluster](/scan/drift/): what is in Git, what is running, and what differs.
- [Fix packs](/scan/fix-packs/): the YAML to fix each failing check, next to the source files.
