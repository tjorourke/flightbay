---
title: Fix packs
weight: 30
description: Write the YAML for each failing check, with names filled in and mapped back to the files that declare the objects.
---

# Fix packs

`--fix-pack DIR` writes the fixes for the failing checks of a scan into a directory, ready to review and raise as a pull request.

```bash
flightbay scan . --fix-pack flightbay-fixes
flightbay scan . --new-only --base-ref origin/main --fix-pack flightbay-fixes   # only for what the change adds
```

It never edits your files.

## What it writes

| File | Content |
|---|---|
| `DIR/<check-id>.yaml` | One file per failing check, for example `k8s-netpol-default-deny.yaml`. A comment names the check, its severity, why it matters and its guide. Then one YAML document per affected object, headed by the object and the file and line that declare it. |
| `DIR/README.md` | Each check with a table of the affected objects, their source files and the detail, and a list of the proposals. |
| `DIR/proposals/...` | New objects for manifests in plain YAML, in the same layout as the repository. |

Names are filled in where Flightbay knows them: `<ns>` and `<namespace>` become the object's namespace, and `<name>`, `<agent>`, `<svc>` and similar become its name. The Agent reach checks use the fix Agent reach has already written for that agent, with its labels, ports and Services in place. Placeholders Flightbay cannot fill, such as an issuer URL, stay in angle brackets.

When a fix is a command rather than YAML (label a namespace, restart a rollout), it is written as comments, so every file is still valid YAML.

```yaml
# Application namespaces deny ingress by default (k8s.netpol-default-deny, medium)
# Without a default-deny NetworkPolicy every pod accepts connections from every other pod in the cluster.
# Guide: https://masterthemesh.com/solo/zero-trust-agents-kubernetes/
---
# Namespace demo (declared in manifests/app.yaml:1)
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: default-deny-ingress, namespace: demo}
spec:
  podSelector: {}
  policyTypes: [Ingress]
```

## Proposals next to the source

When the fix is a new object (a NetworkPolicy, AuthorizationPolicy, PeerAuthentication or PodDisruptionBudget) and the object it protects is declared in a plain manifest, the fix is also written under `proposals/`, in the directory of that manifest:

```text
flightbay-fixes/
  README.md
  k8s-netpol-default-deny.yaml
  k8s-probes.yaml
  proposals/
    manifests/
      flightbay-k8s-netpol-default-deny-demo.yaml
```

Copying `proposals/` over the repository root puts each proposal next to the manifests it belongs with. For objects from a Helm chart or a Kustomize overlay the fix belongs in the chart's values or an overlay patch, so no proposal is written; the README points at the template or base file instead.

## Raising the pull request

The fix pack stops at the directory. Opening the pull request is left to CI, so a person always reviews it. Both CI examples have a commented-out job that does it:

- [`examples/ci/github-actions.yml`](https://github.com/tjorourke/flightbay/blob/main/examples/ci/github-actions.yml): writes the pack, copies `proposals/` into a branch, and runs `gh pr create` with the README as the description;
- [`examples/ci/gitlab-ci.yml`](https://github.com/tjorourke/flightbay/blob/main/examples/ci/gitlab-ci.yml): the same with a push option that opens a merge request.

See [Using it in CI](/scan/ci/) for the rest of the workflow.

## Limits

- Fixes for existing objects (add a probe, set resource requests, pin an image) are snippets to merge into the object by hand. The pack does not patch manifests, values or overlays.
- A default-deny NetworkPolicy blocks traffic until the allow rules exist. Read the guide for each check before applying its fix.
