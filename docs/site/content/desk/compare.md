---
title: Compare
weight: 100
description: See what changed between two captures of the same cluster, or how one object differs between the clusters in a bundle.
---

# Compare

"What changed since it last worked?" is often the fastest way to a root cause. Compare answers it: objects added, removed or changed between two captures, image upgrades, and findings that appeared or went away.

It also compares one object across the clusters in a multi-cluster bundle, for a route or policy that should be identical everywhere.

## Two captures of the same cluster

### Picking an earlier capture in the UI

Open **Compare**. If no baseline is loaded, the page lists candidate bundles found next to the open bundle, in the current directory and in `~/Downloads`, with their case, clusters, capture time and size. Bundles from the same cluster are marked. Press **Compare** on one, or paste a path into the box below the list.

The earlier bundle is read and verified in full on your machine, in the background; the page refreshes itself when it is ready. Nothing leaves your computer. Encrypted candidates are listed, and can be loaded if the desk was opened with the right `-i` identity.

Once a baseline is loaded, **Compare with a different capture** lets you switch.

### From the command line

```bash
flightbay ui after.tar.zst --baseline before.tar.zst      # open with the baseline loaded
flightbay-desk diff before.tar.zst after.tar.zst           # a text report, no UI
flightbay-desk diff before.tar.zst after.tar.zst --yaml    # include before and after YAML of changed objects
flightbay-desk diff before.tar.zst after.tar.zst --json    # as JSON
```

`-i FILE` opens encrypted bundles, for both.

### What you get

| Section | Shows |
|---|---|
| Summary | Counts of objects added, removed and changed, image changes, and new and resolved findings |
| Image changes | Per workload and container: the image before and after |
| New since the earlier bundle | Findings that are new |
| No longer present | Findings that have gone |
| Objects | Every added, removed or changed object, with the fields that changed. Filter by kind. Click a changed object for a side-by-side diff of its YAML; an added or removed one shows its YAML. |
| Not comparable | Types or namespaces one of the bundles did not capture completely |

Fields that change between captures of an unchanged object are removed before comparing: `uid`, `resourceVersion`, timestamps, `managedFields`, the last-applied annotation, condition transition times and leader-election heartbeats. What is left is real change.

### Absence is judged against coverage

An object only counts as **removed** if both captures covered its type and namespace. If the later capture excluded the type, or its collection failed, the object is listed as not comparable instead. The same rule keeps a narrower second capture from looking like a mass deletion.

### Different clusters

You can compare bundles from different clusters; the page warns that most objects will then show as added or removed.

## One object across clusters

When a bundle holds two or more clusters, the lower half of the page finds an object by name (with the same typeahead as Search) and shows its captured YAML side by side for the first two clusters that have it, with changed lines highlighted and the same metadata noise removed. If the object is missing in a cluster, the page says which.

You can also start this from any object's drawer with **Compare across clusters**.

## For agents

The MCP `compare_baseline` tool returns the same report when the desk was opened with a baseline. See [MCP server](/desk/mcp/).
