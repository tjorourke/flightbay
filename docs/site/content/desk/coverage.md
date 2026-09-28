---
title: Coverage
weight: 120
description: What the customer allowed, what was collected, what failed and what the importer verified.
---

# Coverage

Coverage is the page that stops you drawing conclusions from missing data. It shows what the customer's policy allowed, what each collection produced, which calls failed and why, and whether the bundle verified.

Check it whenever something you expected is not there. An empty result means something different when the type was excluded, when its collection failed, and when it really had nothing in it.

## What is on it

| Panel | Shows |
|---|---|
| Collections | One row per collection (a resource type, a log stream, events, a diagnostic file, an attached flow file): cluster, items, state and reason |
| Collection log | Every call that did not simply succeed: operation, target, result and error (RBAC denied, timeouts, truncation) |
| Import | Source, bytes and members received, counts of objects, log lines and events, start and finish, any import error, and deferred categories. **Verified** once every checksum matched. |
| Not collected by policy or build | Categories the policy asked for that could not be collected, with the reason |
| Redaction report | Replacements per file and rule. Counts only: values are never recorded. |
| Collection policy | The resolved policy recorded in the bundle, as JSON |

## Collection states

| State | Means |
|---|---|
| complete | Collected in full |
| truncated | Cut short, with the reason: a log stream hit its byte limit ("kept the last 20971520 bytes"), or the bundle size budget was reached |
| failed | The collection did not succeed, with the reason: forbidden by RBAC, not found, a container waiting to start |
| excluded | The policy excluded it |

The same states drive the rest of the desk: [Resources](/desk/resources/) warns when a type is not complete, the snapshot API returns 403 for an excluded type, [Findings](/desk/findings/) turns a failure into a `collection.gap` finding, and the [Scorecard](/desk/scorecard/) marks checks that need missing data as not applicable.

## From the command line

`flightbay inspect BUNDLE` prints the same coverage, collection log problems and redaction total without opening the desk. See [Sending a bundle](/collect/sending/).
