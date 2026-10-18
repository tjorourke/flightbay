---
title: Scope
weight: 10
description: Choose which clusters, namespaces and resource types flightbay reads, with profiles, flags and config files.
---

# Scope

Scope decides what `flightbay collect` is allowed to read. Get it right before the capture, because nothing outside the scope is read at all: it is not collected and filtered later, it is never requested.

## Profiles

A profile is the starting point. Flags and config files refine it.

| Profile | Categories | Object detail | ConfigMaps | Secrets | Logs |
|---|---|---|---|---|---|
| `minimal` | objects, events | metadata | metadata | metadata | not collected |
| `standard` (default) | objects, events, logs, helm | full | scrubbed | keys | current and previous, last 24h, up to 20 MiB per stream |
| `custom` | none until you choose | full | scrubbed | keys | not collected |

`minimal` is for a first look when a security team wants the least possible: names, labels, owners and events, but no spec or status. `custom` is for building a policy from nothing with `--collect`.

```bash
flightbay collect --profile minimal
flightbay collect --profile custom --collect objects,events --namespaces payments
```

## Categories

`--collect` replaces the profile's category list.

| Category | What is read |
|---|---|
| `objects` | A `list` of each selected resource type, in bounded chunks. For custom resources, the CRD definition too (names and printer columns; only HTTPRoute retains its schema for routing capability checks). |
| `events` | Core and `events.k8s.io/v1` events. |
| `logs` | `pods/log` for the selected containers. See [Logs](/collect/logs/). |
| `nodes` | Node objects: conditions, capacity, versions, taints. Needs `--cluster-scoped`. |
| `helm` | Release summaries from Helm's release Secrets. Needs `--secrets keys`. |
| `proxies`, `control-plane`, `metrics` | Admin endpoints of supported components, through port-forward. See [Proxy and control-plane diagnostics](/collect/diagnostics/). |

A category whose dependency is missing is not silently added; the dry run lists it under "Not collected" with the reason.

## Contexts

`--context` takes one or more kubeconfig contexts. Without it, the current context is used. Several contexts go into one bundle, and the desk shows each as its own cluster.

```bash
flightbay collect --context mesh1,mesh2 --case 01234567
flightbay collect --kubeconfig ~/.kube/customer.yaml --context prod
```

`--kubeconfig` defaults to `$KUBECONFIG`, then `~/.kube/config`.

## Namespaces

`--namespaces` limits the capture to these namespaces; the default is every namespace you can read. `--exclude-namespaces` removes namespaces, and always wins over an include. Both accept globs.

```bash
flightbay collect --namespaces 'team-*',istio-system
flightbay collect --exclude-namespaces 'sandbox-*',scratch
```

| Pattern | Matches |
|---|---|
| `payments` | exactly `payments` |
| `team-*` | `team-a`, `team-payments` |
| `ns-?` | `ns-1`, `ns-a` |
| `[ab]-prod` | `a-prod`, `b-prod` |

The Namespace objects in scope are always read, because their labels decide ambient enrolment and waypoint use. Only the namespaces in scope are read.

When you limit namespaces, the proxy and control-plane dumps are filtered as well. ztunnel, agentgateway and istiod describe the whole mesh, so any entry naming another namespace is removed before it is written, and the bundle records how many were removed.

## Cluster-scoped objects

Cluster-scoped types (nodes, namespaces outside the selection, CRDs, ClusterRoles, ClusterRoleBindings and so on) need `--cluster-scoped`. Without it only namespaced types are read.

```bash
flightbay collect --cluster-scoped --collect objects,events,logs,helm,nodes
```

Several Scorecard checks (RBAC wildcards, cluster-admin bindings, node versions) need cluster-scoped data. Without it they are marked not applicable rather than failing.

## Resource types

| Flag | Effect |
|---|---|
| `--exclude-resources configmaps,secrets` | Never read these types. Exclusions win over everything. |
| `--include-resources pods,deployments.apps,httproutes.gateway.networking.k8s.io` | Allowlist: only read these types. `--resources` is the same flag under its older name. |
| `--secrets omit` | Skip Secrets entirely (same effect as excluding them). |
| `--configmaps omit` | Skip ConfigMaps entirely. |

Types are named as `resource` for core types and `resource.group` for the rest. The plural name, the full name and the kind all match, and globs work: `*.istio.io`.

With an allowlist, nothing outside it is read, and `--exclude-resources` still takes types out of it: `--include-resources 'pods,*.istio.io' --exclude-resources sidecars.networking.istio.io` reads pods and every Istio type except Sidecars. The dry run lists the resulting types, and `flightbay rbac` grants exactly those instead of a wildcard. In a config file the allowlist is `resources:`.

## Label selector

`--label-selector` passes a standard Kubernetes label selector to every list of a namespaced type:

```bash
flightbay collect --label-selector 'app in (checkout,payments)'
```

Use it with care. Objects without the label (a Service, a NetworkPolicy, an AuthorizationPolicy) are not collected either, and checks and reach analysis that rely on them will see less.

## Object detail

`--object-detail metadata` keeps only `apiVersion`, `kind` and `metadata` for every object. `full` (the default in `standard`) keeps spec and status. With `metadata`, the Secret body is not requested at all.

## Rate and size

| Flag | Default | Effect |
|---|---|---|
| `--qps` | 50 | List and get requests per second (the same as kubectl) |
| `--burst` | 100 | List and get request burst |
| `--concurrency` | 4 | List and get requests in flight at once; each still passes `--qps` |
| `--log-concurrency` | 8 | Log streams read at once |
| `--log-qps` | 50 | Log requests per second, limited separately |
| `--max-bundle-bytes` | none | Stop collecting once the compressed bundle reaches this size |
| `--compression` | `best` | `best`, `better`, `default` or `fastest` |

How these protect the API server, and what to change on a very large or busy cluster, is in [Large clusters](/collect/large-clusters/).

## Config files

A config file is the same policy in YAML. Unknown fields are rejected. Flags refine it, and exclusions always win.

```yaml
# collection.yaml
profile: standard
case: "01234567"
namespaces: [payments, istio-system]
excludeNamespaces: ["sandbox-*"]
excludeResources: [configmaps]
clusterScoped: false
logs:
  since: 2h
  maxBytes: 10485760
  streams: [current, previous]
secrets: keys
configMaps: scrubbed
```

```bash
flightbay collect --config collection.yaml --dry-run
flightbay collect --config collection.yaml
```

Pass either `--profile` or a `profile:` field, not both. The easiest way to get a correct file is to build the capture with flags and add `--save-config collection.yaml`.

## Interactive mode

```bash
flightbay collect --interactive
```

Flightbay asks for the profile, namespaces to include and exclude, whether to collect logs and over what window, how to treat Secrets and ConfigMaps, resource types never to read, and whether to include cluster-scoped objects. It then prints the plan and asks "Collect now?". Nothing is read until you say yes. `-y` skips the final question.
