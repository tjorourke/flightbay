---
title: Repo compared with the cluster
weight: 20
description: Find objects in Git that are not running, objects running that are not in Git, and fields changed by hand.
---

# Repo compared with the cluster

Scan a repository, capture the cluster it deploys to, and compare the two. The report has three lists:

- **in Git, not in the cluster**: declared and never applied, or deleted since;
- **in the cluster, not in Git**: applied by hand, in the namespaces and kinds the repository declares;
- **differs**: the same object with different fields, and which fields.

```bash
flightbay scan deploy/ --env prod --bundle repo.tar.zst
flightbay collect --context prod --namespaces payments,checkout -o cluster.tar.zst
flightbay-desk drift repo.tar.zst cluster.tar.zst
```

```text
Declared (repo): repo_payments-deploy (payments-deploy@94b42904579e)
Live (cluster):  prod captured 2026-09-28 14:38 UTC

0 in Git, not in the cluster; 1 in the cluster, not in Git; 1 differ; 5 match

In the cluster, not in Git:
  ConfigMap demo/applied-by-hand

Differs:
  Deployment demo/web  (manifests/app.yaml:9)
      spec.replicas: 2 -> 3

Scope of "in the cluster, not in Git": namespaces demo; kinds /ConfigMap, /Namespace, /Secret, /Service, /ServiceAccount, apps/Deployment
```

`--json` prints the same report as JSON. The order of the two bundles does not matter: whichever is the repo scan is the declared side.

## In the desk

Open the capture with the scan as its baseline and use [Compare](/desk/compare/):

```bash
flightbay-desk open cluster.tar.zst --baseline repo.tar.zst
```

The sides are labelled "Declared (repo)" and "Live (cluster)". An object in Git and not in the cluster shows as removed, one in the cluster and not in Git as added, and one that differs as changed, with its field list. The API response (`/api/baseline`) carries the full drift report under `report.drift`.

## What is ignored

Objects are matched on group, kind, namespace and name. The cluster names of the two bundles do not have to match, and API versions may differ (`v1beta1` in Git, `v1` in the cluster).

Never compared, because every live object has them:

- `status`;
- `metadata.uid`, `resourceVersion`, `creationTimestamp`, `generation`, `managedFields`, `selfLink` and `ownerReferences`;
- the `kubectl.kubernetes.io/last-applied-configuration` annotation;
- the scan's own `flightbay.dev/*` annotations.

**Defaulted fields** are the ones the API server or a controller fills in when a manifest leaves them out: `spec.template.spec.dnsPolicy`, `schedulerName`, `terminationMessagePath`, `imagePullPolicy`, a Service's `clusterIP`, `sessionAffinity` and port `protocol`, a Deployment's `revisionHistoryLimit` and `strategy`, and so on. A defaulted field is ignored only when the repository does not set it. When the repository sets `dnsPolicy: None` and the cluster runs `ClusterFirst`, that is reported. The list is `DefaultedFields` in `internal/diff/drift.go`; add to it when a new default shows up as noise.

Resource quantities are compared by value, so `cpu: 0.5` and `cpu: 500m` match. Secrets are compared on their key names and sizes, which is all either bundle holds.

## Scope of "in the cluster, not in Git"

A cluster holds far more than any one repository: system namespaces, add-ons, controllers. So this list only includes objects:

- in a namespace the repository declares (a Namespace object, or the namespace of any declared object);
- of a kind the repository declares;
- that were not created by something else: objects with `ownerReferences`, the `default` ServiceAccount, `kube-root-ca.crt`, service-account tokens and Helm release Secrets are left out.

Cluster-scoped objects other than Namespaces are not listed, because every add-on installs ClusterRoles and CRDs.

When the capture did not collect a type or namespace completely, a declared object missing from it proves nothing. It is listed under "Not comparable" instead of "in Git, not in the cluster".

## Limits

- Lists are compared position by position. An env var inserted in the middle shows as several changed fields.
- Fields that a mutating webhook adds (sidecar injection, a policy engine's labels) show as differences unless they are in the defaulted list.
- A Helm chart rendered by the scan and the same chart installed by Helm can differ in labels such as `helm.sh/chart` when the chart version differs. That is real drift.
