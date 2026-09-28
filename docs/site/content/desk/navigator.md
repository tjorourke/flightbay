---
title: Navigator
weight: 30
description: A keyboard-first, k9s-style browser for the snapshot, in the browser.
---

# Navigator

The Navigator is a k9s-style view of the snapshot that runs in the page. It is for people who think in `:pods` and `/filter` and would rather not reach for the mouse. Everything happens on the keyboard.

![Navigator](/images/navigator.png)

The header shows the bundle, the context (cluster), the snapshot time and the connection, which is always "offline · read-only".

## Keys

| Key | Does |
|---|---|
| ↑ ↓ or `j` `k` | Move the selection |
| `g` / `G` | First / last row |
| Enter | Describe the object (open the drawer). On a Namespace, list its pods. |
| `y` | The captured YAML |
| `d` | The object's events |
| `l` | Logs of the selected pod, or of every pod of the selected Deployment, DaemonSet, StatefulSet, ReplicaSet, Job or CronJob |
| `p` | Previous logs (the container before its last restart) |
| `s` | Open real k9s on this type and namespace in the terminal |
| `:` | Switch resource type |
| `/` | Filter the list |
| `0` | All clusters |
| `1` to `9` | That cluster |
| Esc | Clear the filter |

Keys only act when nothing else has focus, so they do not interfere with typing in a box or in the terminal.

## Switching type with `:`

Press `:` and a command line opens with the captured types listed straight away, as k9s does. Type a resource name, kind, singular name or short name (`po`, `deploy`, `svc`, `httproute`, `gw`) and press Enter. Types with captured objects are listed first, with counts; types that exist but have nothing captured say "none captured". Esc cancels.

The quick buttons under the title switch to pods, deployments, services, gateways, httproutes and events, where the capture has them.

## Filtering with `/`

The filter matches names, status and other columns, and takes `label:key=value`. It has the same typeahead as Search, narrowed to the current type and namespace. The namespace menu next to it limits the list to one namespace.

## Columns

Each type shows the columns `kubectl get` would print for it, including the printer columns of custom resources, taken from their CRD definitions in the bundle. Status values are coloured (running, pending, failed and so on).

## When to use it instead of Resources

[Resources](/desk/resources/) is the same data with a type list down the side and a mouse-friendly layout. The Navigator is faster once you know the keys. For the real thing, `k9s` works in the [terminal](/desk/terminal/) against the same snapshot.
