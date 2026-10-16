---
title: RBAC
weight: 60
description: Generate least-privilege RBAC for your collection policy, so the collector runs with only the access it needs.
---

# RBAC

The collector only needs read access, and only to what your policy selects. `flightbay rbac` prints the Roles, ClusterRoles and bindings for exactly the options you give it, so a security team can create a dedicated identity for Flightbay instead of lending it an admin kubeconfig.

It takes the same policy flags as `flightbay collect` (profile, namespaces, resources, logs, diagnostics, `--config` and so on).

## Generate it

```bash
flightbay rbac --namespaces payments,istio-system > flightbay-rbac.yaml
kubectl apply -f flightbay-rbac.yaml
```

The subject is a group called `flightbay:readers`. Change it to the user, group or service account that will run the collector.

The dry run prints the same RBAC at the end of its plan:

```bash
flightbay collect --namespaces payments,istio-system --dry-run
```

## Wildcards and exclusions

Without cluster access, `flightbay rbac` does not know which resource types exist, so it grants `get, list` on `*` in the scoped namespaces. Kubernetes RBAC cannot express "everything except Secrets", so when your policy excludes a type the output starts with a warning: the wildcard grants more than the policy collects, and the collector still enforces every exclusion itself.

For an explicit list, let it read API discovery:

```bash
flightbay rbac --discover --context mesh1 --exclude-resources secrets,configmaps
```

`--discover` lists every resource type the policy allows by name, and leaves out the excluded ones. A narrower alternative is `--resources`, which lists only the types you name.

## What each option needs

| Option | Permissions |
|---|---|
| Objects (every profile) | `get, list` on the selected resource types in the selected namespaces; `get` on the Namespace objects in scope |
| Custom resources | `get` on their `customresourcedefinitions` (names and printer columns are kept; the schema is dropped), by name when `--discover` or `--resources` is used |
| `--namespaces a,b` | A Role and RoleBinding per namespace instead of a ClusterRole, plus a ClusterRole to `get` those Namespace objects by name. Globs fall back to a ClusterRole. |
| `--cluster-scoped` | `get, list` on cluster-scoped types (a ClusterRole) |
| Events | `get, list` on `events` in core and `events.k8s.io` |
| Logs | `get, list` on `pods` and `pods/log` |
| Nodes | `get, list` on `nodes` (needs `--cluster-scoped`) |
| Helm | Read on `secrets`, because release summaries are decoded from Secret data (values are never kept) |
| `--diagnostics` or `--portforward` | `get, list` on `pods` and `create` on `pods/portforward` in the selected namespaces |
| `--hubble-flows` | A Role in `kube-system`: `get, list` on `pods` and `create` on `pods/portforward`, for `hubble-relay` |
| `--secrets metadata` | Secret metadata through the metadata API. RBAC still says `get, list` on `secrets`, but the body is never requested. |
| `--secrets omit` or `--exclude-resources secrets` | Nothing on Secrets with `--discover`; the wildcard version warns instead |

## Running as a service account

```bash
kubectl create serviceaccount flightbay -n default
# change the subject in flightbay-rbac.yaml to:
#   - kind: ServiceAccount
#     name: flightbay
#     namespace: default
kubectl apply -f flightbay-rbac.yaml
kubectl create token flightbay -n default --duration 1h
```

Put the token in a kubeconfig of its own and pass it with `--kubeconfig`.

## When RBAC denies something

The collector does not stop at the first denied call. Each failure is recorded in `collection-log.jsonl` and in the coverage record for that type, as `failed: forbidden: ...`. `flightbay inspect` lists them, and the desk treats those types as not collected, never as empty. See [Troubleshooting](/troubleshooting/).
