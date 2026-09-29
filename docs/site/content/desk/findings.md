---
title: Findings
weight: 70
description: Inspect rule-based findings and their supporting objects and logs.
---

# Findings

Findings identify conditions in the snapshot such as crash loops, rejected routes, stale proxy configuration and expiring certificates. Open a finding for its evidence, log query and suggested next step. Confirm the cause using the linked objects and logs.

![Findings](/images/findings.png)

Filter by severity with the menu at the top. Clicking a bar in **Findings by rule** on the [Overview](/desk/overview/) opens this page filtered to that rule; the rule shows as a chip in the panel header, and clicking the chip clears it.

## The rules

| Rule | Severity | Fires when |
|---|---|---|
| `workload.crashloop` | critical | A container is in CrashLoopBackOff, or had just exited or restarted again when the snapshot was taken |
| `workload.image-pull` | warning | A container is in ImagePullBackOff or ErrImagePull |
| `workload.restarts` | warning | A container has restarted five or more times (and was not OOMKilled) |
| `workload.oomkilled` | warning | A container's last instance was OOMKilled |
| `workload.unschedulable` | warning | A pod is Pending with PodScheduled=False |
| `gateway.route-condition` | warning | A route has a condition set to False (for example Accepted or ResolvedRefs) |
| `gateway.unresolved-backend` | warning | A route references a Service that does not exist in the capture |
| `gateway.not-programmed` | warning | A Gateway has a condition set to False |
| `policy.not-accepted` | warning | A policy (for example an agentgateway or kgateway policy) is not Accepted |
| `gateway.api-version` | info | Which Gateway API release the CRDs come from |
| `gateway.api-version-mixed` | warning | Gateway API CRDs come from more than one release |
| `mesh.ambient-no-ztunnel` | critical | A namespace is labelled for ambient but no ztunnel pod was captured |
| `mesh.waypoint-missing` | critical | A Service or namespace uses a waypoint that does not exist |
| `mesh.cni-not-ready` | critical | istio-cni is not ready on every node it should run on |
| `mesh.version-skew` | warning | istiod and ztunnel run different versions |
| `agentgateway.version-skew` | warning | agentgateway proxies run a different version from their controller |
| `xds.stale` | warning | istiod's sync status shows a proxy has not acknowledged the latest config |
| `xds.nack` | warning | A proxy rejected config pushed by istiod |
| `cert.expired` | critical | A workload or root certificate had expired when the snapshot was taken |
| `cert.root-expiring` | warning | The mesh root certificate expires within 30 days |
| `cert.unavailable` | warning | ztunnel has no usable certificate for an identity |
| `logs.error-signature` | info | The same error (numbers, addresses and IDs removed) appears three or more times in one container |
| `collection.gap` | info | A collection failed, so the data is missing rather than absent |

The `xds.*` and `cert.*` rules need [Solo diagnostics](/collect/solo-diagnostics/) in the capture.

## Missing data

A rule that depends on something being absent (a Service a route points at, a waypoint a namespace uses) only runs once that data has fully loaded, and never fires on a type the customer chose not to collect. Until the bundle is verified, findings are marked **provisional**, and the page says why.

A failed collection becomes a `collection.gap` finding, which says the data is missing and that this is not proof of absence.

## Using the page

The severity menu narrows to critical, warning or info. Click a finding to open it:

- the rule, severity and cluster;
- the detail, in a sentence or two;
- the **evidence**: the exact fields and values behind it;
- a **next step**;
- **Inspect object** to open the object in the drawer;
- **Open the logs** to run the finding's log query in the [Log explorer](/desk/logs/).

The top findings also appear on the [Overview](/desk/overview/) under "Priority findings".

## From the terminal and agents

```bash
fbfind
```

`fbfind` lists every finding with its log query, ready to paste into `fbq`. The MCP `list_findings` tool returns them as JSON, optionally filtered by cluster and severity.

For best-practice problems (open MCP endpoints, missing mTLS, privileged pods) rather than things that are broken, see the [Scorecard](/desk/scorecard/).
