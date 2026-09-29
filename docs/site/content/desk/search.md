---
title: Search
weight: 20
description: Search captured objects by name, namespace, labels or content.
---

# Search

Search matches object names, namespaces, labels and content in spec and status. For example, search for a hostname to find routes that reference it, or an image name to find its pods.

![Searching every object](/images/search.png)

With an empty box the page lists every kind that has captured objects, grouped by area (Workloads, Networking, Gateway API, Istio, agentgateway and kgateway, kagent, Solo, Config, access and storage). Click one to list it.

## Syntax

Terms are separated by spaces and all of them must match.

| Term | Matches | Example |
|---|---|---|
| word | Name, labels or anything in the object's JSON, case-insensitive | `checkout` |
| `"phrase"` | The exact text, spaces included | `"shop.example.com"` |
| `kind:` (also `type:`, `k:`) | Resource type. Takes kinds, plural names, short names and `resource.group`. | `kind:pod`, `kind:po`, `kind:deploy`, `kind:httproute`, `kind:deployments.apps` |
| `ns:` (also `namespace:`) | Namespace, exactly, or as a prefix with `*` | `ns:payments`, `ns:team-*` |
| `name:` | Name, exactly, or as a prefix with `*` | `name:checkout*` |
| `label:key=value` (also `l:`) | A label with that value | `label:app=checkout` |
| `label:key` | Objects that have the label, any value | `label:istio.io/rev` |
| `cluster:` | Cluster in the bundle | `cluster:mesh1` |
| `-term` | Excludes anything the term matches | `-kind:event`, `-ns:kube-system` |

Any other `key:value` is searched as plain text, colon included. To find an image, search for the image itself: `nginx:1.27` or `"registry.invalid"`.

## Examples

```text
checkout                                  name, label or content
kind:pod ns:payments CrashLoopBackOff     pods in payments stuck in a crash loop
kind:httproute "shop.example.com"         which routes use a hostname
label:app=checkout -kind:event            everything labelled app=checkout, without events
checkout-db -kind:secret                  everything that refers to the checkout-db Secret
name:istio* ns:istio-system               names starting istio in istio-system
```

## Results

Results are ranked: exact name match first, then names that contain the word, then label matches, then matches anywhere in the object. The **Why it matched** column shows the matching labels, or a snippet of the object around the match.

Chips above the table count results per kind; click one to narrow to it. **Show more** loads more rows. **Search the logs for this instead** takes the free-text part of your query to the [Log explorer](/desk/logs/).

Click a row to open the object drawer: summary, YAML, JSON, related objects, events and, for pods, log streams. See [Resources](/desk/resources/).

An empty result says whether the bundle is still loading. Types the customer excluded, or that failed to collect, cannot be searched; [Coverage](/desk/coverage/) says which.

## Typeahead

Search boxes complete as you type, from what is in this capture.

![Label keys suggested from the capture](/images/search-typeahead.png)

| You type | You get |
|---|---|
| a few letters | Matching keys (`kind`, `ns`, `label`, `name`, `cluster`), and from three letters, object names that match |
| `kind:` | The kinds that have objects, with counts and their resource names |
| `ns:` | Namespaces, with object counts |
| `label:` | Label keys, with how many values each has; choosing one continues with `key=` and its values |
| `name:` | Names that start with, then contain, what you typed |
| `cluster:` | Clusters in the bundle |

Suggestions are narrowed by the other structured terms in the box, so `kind:pod label:` lists only labels that pods carry. Free text does not narrow suggestions, because scanning every object on each keystroke would be too slow.

| Key | Does |
|---|---|
| ↓ in an empty box, or Ctrl+Space | Open suggestions |
| ↑ ↓ | Move through them |
| Enter or Tab | Insert the highlighted one |
| Esc | Close |

The same typeahead works in the log explorer, the Navigator and Resources filters, the Timeline, the Scorecard and Agent reach, each with its own keys.

## From the terminal and agents

`fbsearch` runs the same query in the [terminal](/desk/terminal/):

```bash
fbsearch 'kind:pod ns:payments CrashLoopBackOff'
fbsearch '"shop.example.com"' --limit 100
```

The MCP `search_objects` tool takes the same syntax. ⌘K uses the same search to jump to an object from anywhere.
