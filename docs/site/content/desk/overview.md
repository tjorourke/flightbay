---
title: Overview
weight: 10
description: The first page of a workspace, with the size of the capture, the findings to start from, each cluster and where the bundle came from.
---

# Overview

The Overview is the landing page. It answers the questions you have in the first minute of a case: how big is this capture, what looks wrong, which clusters are in it, and what did the customer allow.

![Overview of a snapshot](/images/overview.png)

## What is on it

| Panel | Shows |
|---|---|
| Header | The case number (or "Snapshot"), the contexts and when it was captured, with buttons to **Coverage & redactions** and **Investigate findings** |
| Clusters | How many, the Kubernetes version, nodes and namespaces captured |
| Objects | Total objects, pods and running pods |
| Log lines | How many, and whether every stream has loaded |
| Findings | Critical and warning findings, and how many informational ones |
| Findings by rule | The five rules with the most findings, coloured by their worst severity. Click a bar to open [Findings](/desk/findings/) filtered to that rule. |
| Log volume | Log lines across the capture window, with the error lines drawn as a dashed red line on top, and the totals of each |
| Restarts by namespace | Container restarts summed from pod status, for the five namespaces with the most. Click a bar to open the [Log explorer](/desk/logs/) on that namespace. |
| Start here | The highest-priority findings. Marked provisional until the import is verified. Click one for its evidence. |
| Not collected in this bundle | Categories the policy asked for but could not collect, with the reason (for example, diagnostics without port-forward) |
| Clusters | One card per cluster: version, pods, objects, namespaces, finding count and any collection gaps or truncations. Click a name to browse its resources. |
| Bundle provenance | Collector version, capture start and end, profile, namespaces, excluded resources, Secret and ConfigMap handling, log window, and the number of redactions. **Verified** once every checksum has matched. |

## When to use it

Start here on every case. The provenance panel tells you what the customer chose not to send, which matters as much as what they did: a missing log stream or an excluded resource type is a gap in the evidence, not proof that something was absent.

From here:

- **Investigate findings** goes to [Findings](/desk/findings/).
- **Coverage & redactions**, or the redaction count, goes to [Coverage](/desk/coverage/).
- A cluster name selects that cluster and opens [Resources](/desk/resources/).
