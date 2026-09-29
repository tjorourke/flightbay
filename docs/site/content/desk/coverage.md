---
title: Coverage
weight: 120
description: Collection scope, missing data and bundle verification.
---

# Coverage

Coverage lists the collection policy, results, failed requests and bundle verification status.

Check it whenever something you expected is not there. An empty result means something different when the type was excluded, when its collection failed, and when it really had nothing in it.

![Coverage with the Origin and Removed by the customer panels](/images/coverage-origin.png)

## What is on it

| Panel | Shows |
|---|---|
| Collections | One row per collection (a resource type, a log stream, events, a diagnostic file, an attached flow file): cluster, items, state and reason |
| Collection log | Every call that did not simply succeed: operation, target, result and error (RBAC denied, timeouts, truncation) |
| Origin | The bundle's signature, the approved policy file if one was used, and the SHA-256 of the collector binary. See [Signing](/collect/signing/). |
| Removed by the customer | For a reviewed bundle: who reviewed it, the original bundle and its manifest hash, and counts of what was removed and redacted. Names are listed only if the customer chose to. See [Reviewing before you send](/collect/review/). |
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
| removed | The customer removed the whole type or stream in review, before sending |
| some removed | The customer removed some objects of the type by name in review; the rest are here |

The same states drive the rest of the desk: [Resources](/desk/resources/) warns when a type is not complete, the snapshot API returns 403 for an excluded or removed type and a warning for a partly removed one, [Findings](/desk/findings/) turns a failure into a `collection.gap` finding, and the [Scorecard](/desk/scorecard/) marks checks that need missing data as not applicable.

## From the command line

`flightbay inspect BUNDLE` prints the same coverage, collection log problems and redaction total without opening the desk. See [Sending a bundle](/collect/sending/).
