---
title: RBAC
weight: 95
description: Explore what a ServiceAccount can do in the captured snapshot, and the reverse (who can verb a resource).
---

# RBAC explorer

The RBAC page (and the **Service account RBAC** button in any ServiceAccount drawer) shows exactly what a ServiceAccount is allowed to do, based only on the captured objects: RoleBindings, ClusterRoleBindings, Roles, ClusterRoles, and the ServiceAccount itself.

It is read-only review text. Nothing is applied.

## What it shows

- The ServiceAccount identity and whether it was found in the capture.
- Coverage note if the capture is partial for RBAC.
- Workloads (Deployments etc.) that use this SA as their serviceAccountName, and how many pods.
- Permissions granted in this namespace and cluster-wide.
  - Each row: API group, resource (and subresource), verb, resourceNames if limited, the binding that granted it, and any red flags (wildcard, create on secrets, impersonate, bind, escalate, wildcard on non-list/get/watch, etc.).
- The bindings that mention the SA (direct or via group).
- A "Who can" form that answers the reverse question: given a verb and resource (e.g. get pods) in a namespace, which subjects (users, groups, serviceaccounts) can do it, via what bindings.

## How it decides

It walks the captured bindings, resolves Roles/ClusterRoles (including aggregation), expands groups, and computes the effective rules for the SA. It does not reach outside the snapshot.

For the reverse, it walks every binding+role in the namespace (and cluster roles), and collects subjects that grant the requested verb/resource.

See the [MCP tools](/desk/mcp/) `rbac_explain_serviceaccount` and `rbac_who_can` for the same answers to agents.

The suggestion box at the bottom is generated review text only; you still have to apply changes yourself on the real cluster.
