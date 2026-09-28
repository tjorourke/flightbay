---
title: "Resources: Keyboard (Navigator)"
weight: 30
description: A keyboard-first, k9s-style view of the snapshot or live mirror in the browser, with describe, YAML, events and logs in place.
---

The Navigator is the **Keyboard** tab of [Resources](/desk/resources/), next to **Browse**. It works like k9s, in the page. You pick a resource type with `:`, filter with `/`, move with the arrow keys or `j`/`k`, and press Enter to describe the selected object. Describe, YAML, events and logs open in place of the table, and `Esc` takes you back, as in k9s.

![Navigator](/images/navigator.png)

## Navigator or Resources?

Both list the same objects, with the same columns `kubectl get` prints.

| | Navigator | [Resources](/desk/resources/) |
|---|---|---|
| Best for | Moving fast on the keyboard, the way you would in k9s | Browsing with the mouse, seeing every type at once |
| Choosing a type | `:pods`, `:deploy kube-system`, `:httproute` | A grouped list of every captured type |
| Opening an object | In place: describe, YAML, events, logs | A side drawer with tabs (summary, YAML, JSON, related, events, logs) |
| Describe | The real `kubectl describe` output | A summary panel |

`o` opens the selected object in the Resources drawer when you want the related objects or the JSON.

## The header

The header shows the bundle (or "live mirror"), the context (cluster), the snapshot time (or "Now" in [live mode](/desk/live/)) and the connection: "offline · read-only" for a bundle, "● live · read-only" for a live cluster. Everything is read-only in both cases.

## Keys in the list

| Key | Does |
|---|---|
| ↑ ↓ or `j` `k` | Move the selection |
| `PgUp` `PgDn` | Move 15 rows |
| `g` / `G` | First / last row |
| `Enter` or `d` | Describe the object. On a namespace, open its pods instead |
| `y` | YAML |
| `e` | Events for the object |
| `l` / `p` | Logs / previous (crashed container) logs, for pods and workloads |
| `o` | Open in the side drawer |
| `s` | Open this type in k9s, in the terminal |
| `Ctrl-D` | Run `kubectl describe` for it in the terminal |
| `:` | Command line (below) |
| `/` | Filter (below) |
| `0`-`9` | Switch cluster (`0` is all clusters) |
| `?` | Show every key |
| `Esc` | Clear the filter |

With the mouse: click a row to describe it. The `d`, `y` and `l` buttons at the end of each row open that view directly, and `Esc` (or the Back button) returns to the list with the row still selected.

## Views: describe, YAML, events, logs

The view replaces the table. The title shows where you are, for example `Pod(kagent) › logs kagent/kagent-controller-cdc96c97c-9bb2d`.

| Key | Does |
|---|---|
| `Esc` or `q` | Back to the list |
| ↑ ↓ `j` `k`, `PgUp` `PgDn`, `Space` | Scroll |
| `g` / `G` | Top / bottom |
| `d` `y` `e` `l` `p` | Switch to describe, YAML, events, logs, previous logs |
| `w` | Wrap long lines |
| `c` | Copy the text |
| `o` | Open the object in the side drawer |
| `L` | Open the logs in the [log explorer](/desk/logs/), with search and time zoom |

- **Describe** is the real `kubectl describe` output, events included, produced by kubectl's own code against the desk's read-only API. It works for built-in types and for custom resources.
- **YAML** is the captured object, redacted as it was collected.
- **Events** lists the events for the object, newest first.
- **Logs** shows the newest 1,000 lines of the pod (every container), or of every pod of a Deployment, StatefulSet, DaemonSet or Job, oldest at the top. Errors are red and warnings amber. For more, press `L`.

## Commands (`:`)

Press `:` and type. Suggestions appear as you type: the captured types (with how many objects each has) and the commands. `Tab` completes, `Enter` runs, `Esc` cancels.

| Command | Does |
|---|---|
| `:pods` | Switch type. Any plural, kind, singular or short name works: `po`, `deploy`, `svc`, `httproute`, `agentgatewaypolicies` |
| `:pods kube-system` | A type in one namespace |
| `:pods all` | A type in all namespaces (`-A` works too) |
| `:ns` | List namespaces. `Enter` on one opens its pods |
| `:ns kagent` | Stay on the current type, in namespace kagent |
| `:ctx` | List the clusters |
| `:ctx 2` or `:ctx mesh2` | Switch cluster |
| `:help` | Show every key |
| `:q` | Leave the Navigator |

## Filters (`/`)

Press `/` to type in the filter box, then `Enter` to go back to the list with the filter applied. `Esc` clears it.

| Filter | Keeps |
|---|---|
| `/checkout` | Rows whose name, status or content contains `checkout` |
| `/!Running` | Rows that do not contain `Running` |
| `/-l app=web` | Objects with that label (comma-separate several); `label:app=web` works too |

The filter box completes label keys and values from the capture as you type.

## Live clusters

In [live mode](/desk/live/), the Navigator reads the live mirror: the table, describe, events and logs show the cluster as it is now. It is still read-only; nothing you do in the Navigator changes the cluster.
