---
title: Resources
weight: 40
description: Browse captured objects by type, and open any object for its YAML, relationships, events and logs.
---

# Resources

Resources lists captured objects one type at a time, with the same columns `kubectl get` prints. Use it to browse: what Gateways are there, which pods are not ready, what does this policy look like.

The page has two tabs over the same objects. **Browse** (this page) is for the mouse: every type grouped on the left, a table, and a drawer for each object. **Keyboard** is the [k9s-style view](/desk/navigator/): `:` to switch type, `/` to filter, Enter to describe in place. The **Resources** menu entry opens whichever tab you used last.

## The type list

Down the left are the types that have captured objects, grouped by area (Workloads, Networking, Gateway API, Istio, agentgateway and kgateway, kagent, other platform APIs, and Config, access and storage), each with its object count and a dot for its collection state. Types with no captured objects are hidden; the note at the bottom says how many, and links to [Coverage](/desk/coverage/) for types that were excluded or failed.

When the same kind exists in two API groups (for example `Gateway` in Gateway API and in Istio), the group is shown next to the name.

## Filtering

| Control | Does |
|---|---|
| Namespace menu | One namespace, or all |
| Filter box (`/`) | Matches name, status and other columns; takes `label:key=value` with typeahead |
| Reset | Clears both |

## Collection state

If a type did not collect cleanly, a note above the table says so, because it changes what the list means:

| State | Means |
|---|---|
| loading | Still arriving. The rows are what has loaded so far. |
| excluded | The customer excluded this type. Its absence says nothing about the cluster. |
| failed | Collection failed, with the reason (often RBAC). |
| truncated | The list was cut short, with the reason (for example the bundle size budget). |
| removed by customer | The customer removed the whole type in review before sending. |
| some removed by customer | The customer removed some of these objects by name in review; the rest are here, so a missing object proves nothing. |

## The object drawer

Click any row to open the drawer.

| Tab | Shows |
|---|---|
| Summary | Kind, namespace, cluster, creation time, labels, node and phase. For pods, each container's readiness, state, restarts and last termination. For Secrets, a note that values were never collected. |
| YAML | The captured object, redacted, with **Copy YAML** |
| JSON | The same as JSON |
| Related | Owners, children, pods a Service selects, routes and policies pointing at it, and references to objects that were not captured (in red) |
| Events | Events for this object |
| Logs | Pods only: every captured log stream, each opening in the [Log explorer](/desk/logs/) |

Buttons in the summary:

- **Open pod logs** (pods) opens the log explorer on that pod, on the previous stream if there is one.
- **Open logs of all its pods** (Deployments, DaemonSets, StatefulSets, ReplicaSets, Jobs and CronJobs) opens the logs of every pod it owns.
- **kubectl describe** runs `kubectl describe` for this object in the terminal.
- **Compare across clusters** (bundles with more than one cluster) shows how the object differs between clusters. See [Compare](/desk/compare/).
- **Remove from the bundle** (bundles opened from a file) leaves this object out of the bundle you export; press it again (**Keep in the bundle**) to undo. See [Reviewing before you send](/collect/review/).

## Terminal shortcuts

The page header has two buttons:

- **Open in k9s** runs `k9s --readonly` on the current type and namespace, in a full-height terminal.
- **kubectl get** runs `kubectl get` for the current type in the terminal.

See [Terminal](/desk/terminal/).
