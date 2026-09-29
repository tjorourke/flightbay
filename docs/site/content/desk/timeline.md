---
title: Timeline
weight: 60
description: Kubernetes events, container exits, rollouts and bursts of error logs on one UTC time axis.
---

# Timeline

The Timeline orders Kubernetes events, container exits, rollouts and error spikes by timestamp. Use it to compare changes with the start of a failure.

![Timeline](/images/timeline.png)

## What is on it

| Kind | Comes from |
|---|---|
| Events | Kubernetes events, with reason, object and message |
| Container exits | Terminations recorded in pod status: reason, exit code and time |
| Rollouts | New ReplicaSets being created |
| Error spikes | Runs of error log lines per namespace |

A steady run of errors shows as one entry covering its time range. A minute with at least three times the run's median rate is called out on its own as a spike, marked critical.

Entries are grouped by day and coloured by severity (critical, warning, info).

## Filtering

| Control | Does |
|---|---|
| Kind chips | Show or hide events, container exits, rollouts and error spikes, with counts |
| Severity menu | One severity |
| Namespace menu | One namespace; a namespace tag on any entry does the same |
| Filter box | Free text and `key:value` terms, with typeahead |

Filter keys:

| Key | Matches | Example |
|---|---|---|
| `ns:` | Namespace | `ns:istio-system` |
| `kind:` | `event`, `termination`, `rollout` or `error-spike` | `kind:termination` |
| `severity:` | `critical`, `warning` or `info` | `severity:warning` |
| `reason:` | Event or exit reason | `reason:BackOff` |
| `object:` | Kind of the object involved | `object:Pod` |
| `cluster:` | Cluster in the bundle | `cluster:mesh1` |

Values match as a case-insensitive prefix, and a leading `-` excludes.

## Zooming

The density strip above the list shows where entries cluster. Drag across it, or click a column, to zoom into that window; **Reset zoom** goes back. **Newest first** switches the order.

## From an entry

- **Inspect** opens the object in the drawer.
- **Logs** opens the [Log explorer](/desk/logs/) on the entry's own query (for error spikes, the matching errors over the spike's range).
- **Logs around** opens the log explorer on the namespace for two minutes either side of the entry.

The list shows 300 entries at a time; **Show more** loads the next 300.
