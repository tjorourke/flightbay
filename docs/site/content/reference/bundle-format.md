---
title: Bundle format
weight: 30
description: The layout of a Flightbay bundle, member by member, and the rules that let the desk open it while it is still arriving.
---

# Bundle format

A bundle is a plain tar stream compressed with zstd, optionally wrapped in age encryption. Every file in it is ordinary JSON or text, so it can be read with standard tools and without Flightbay. This page describes schema version 1.

## The container

| Layer | Detail |
|---|---|
| tar | A normal tar stream. Every chunk of data is a complete tar member. |
| zstd | Compression level from `--compression` (`best` by default). The window is 8 MiB by default and at most 128 MiB (`--window-size`), which stock `tar --zstd` and `zstd` open without extra flags. |
| age (optional) | With `--encrypt-to`, the compressed stream is encrypted to one or more X25519 recipients. The file name ends in `.age`. |

```bash
tar --zstd -tvf flightbay-01234567-20260927T1012Z.tar.zst
flightbay extract flightbay-01234567-20260927T1012Z.tar.zst.age ./out -i support.key
```

## Order

The order is part of the format, because it is what lets the desk open a bundle before it has finished reading:

1. `bundle.json` is always the first member.
2. Data follows in bounded chunks. Objects, events and logs come early; bulky diagnostics come late.
3. A coverage record follows each finished collection.
4. `collection-log.jsonl` and `redaction-report.json` come near the end.
5. `manifest.json` is always the last member.

A bundle whose last member is not a valid manifest is incomplete, and fails verification.

## Layout

```text
bundle.json
clusters/<context>/
  discovery/
    version.json                                  API server version
    apiresources.json                             discovered resource types
    crds/<resource>.<group>.json                  CRD names, scope and printer columns (schema dropped)
  resources/<group or core>/<version>/<resource>/part-000001.json
  events/part-000001.json
  logs/<namespace>/<pod>/<container>/<current|previous>/part-000001.log
  solo/
    summary.json                                  components found, versions and pods
    proxies/<namespace>/<pod>/meta.json           component, container, image, version, node
    proxies/<namespace>/<pod>/config_dump.json    and clusters.json, listeners.json, certs.json, stats.txt, metrics.prom ...
    control-plane/<namespace>/<pod>/syncz.json    and configz.json, registryz.json, endpointz.json, mesh.json, connections.json, metrics.prom
  flows/<format>/<file>                           flow logs attached with --attach-flows or read from Hubble
  coverage/<id>.json                              one per collection
collection-log.jsonl
redaction-report.json
manifest.json
```

The context name is made safe for a path. Core resources use `core` as the group, for example `resources/core/v1/pods/`.

## bundle.json

Written before collection starts.

```json
{
  "schemaVersion": 1,
  "bundleId": "b2a0ac0211211e27",
  "case": "DEMO-0001",
  "collector": "flightbay 0.1.0-dev",
  "captureStart": "2026-09-28T00:36:14.877816Z",
  "contexts": ["kind-flightbay-e2e"],
  "policy": {"profile": "standard", "namespaces": ["payments", "istio-system"], "categories": ["objects", "events", "logs", "helm"], "secrets": "keys", "...": "..."},
  "planned": ["objects", "events", "logs", "helm"],
  "unavailable": ["nodes: cluster-scoped data needs --cluster-scoped"]
}
```

| Field | Meaning |
|---|---|
| `schemaVersion` | Format version, currently 1 |
| `bundleId` | Random ID for this bundle |
| `case` | The `--case` value, if any |
| `collector` | Name and version of the collector |
| `captureStart` | When collection started, UTC |
| `contexts` | The kubeconfig contexts collected |
| `policy` | The fully resolved collection policy, the same shape as a `--config` file |
| `planned` | Categories that will be collected |
| `unavailable` | Categories asked for that cannot be collected, with the reason |

## Resource chunks

Each `part-NNNNNN.json` is a Kubernetes-style list, with an `x-flightbay` block naming the resource type:

```json
{"apiVersion": "v1", "kind": "List",
 "x-flightbay": {"group": "", "version": "v1", "resource": "pods", "kind": "Pod", "namespaced": true},
 "items": [{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "checkout-85b9d479d6-xqvht", "namespace": "payments"}, "spec": {}, "status": {}}]}
```

Objects are redacted and `managedFields` is removed. Secrets carry an `x-flightbay` block with the capture mode and key names and sizes, never values (see [Secrets and redaction](/collect/secrets-and-redaction/)).

## Log chunks

Each `part-NNNNNN.log` is the container's log as the kubelet returned it with `timestamps=true`, redacted line by line:

```text
2026-09-27T17:11:52.111661317Z GET /healthz 200 duration=0ms
2026-09-27T17:11:53.113103579Z GET /healthz 200 duration=1ms
```

Init and ephemeral container logs, when selected, are stored under their container name like any other.

## Coverage records

One per collection: a resource type, a log stream, events, a diagnostic file or a flow file.

```json
{"id": "kind-flightbay-e2e.resource.core.v1.pods", "cluster": "kind-flightbay-e2e", "type": "resource",
 "version": "v1", "resource": "pods", "kind": "Pod", "namespaced": true,
 "chunks": ["clusters/kind-flightbay-e2e/resources/core/v1/pods/part-000001.json"],
 "items": 8, "state": "complete"}
```

| Field | Meaning |
|---|---|
| `type` | `resource`, `logs`, `events`, `diagnostic` or `flows` |
| `group`, `version`, `resource`, `kind`, `namespaced` | For resources |
| `namespace`, `pod`, `container`, `stream` | For logs and diagnostics |
| `chunks` | The members holding the data |
| `items` | Objects, lines or files |
| `state` | `complete`, `failed`, `excluded` or `truncated` |
| `reason` | Why, when not simply complete (for example "kept the last 20971520 bytes (per-stream limit)" or "13 entries naming namespaces outside the selection removed") |

Coverage is what lets the desk tell an excluded type from a failed one from an empty one.

## collection-log.jsonl

One JSON object per line, for every call, including failures:

```json
{"time":"2026-09-28T00:36:15.017839Z","cluster":"kind-flightbay-e2e","operation":"list","target":"pods","result":"ok: 8 items"}
{"time":"2026-09-28T00:36:15.857514Z","cluster":"kind-flightbay-e2e","operation":"logs","target":"bad-image/app (current)","namespace":"payments","result":"failed","error":"container \"app\" in pod \"bad-image\" is waiting to start: trying and failing to pull image"}
```

## redaction-report.json

Counts of replacements per file location and rule. Never the values.

```json
{
  "total": 52,
  "locations": {
    "clusters/kind-flightbay-e2e/logs/payments/checkout-85b9d479d6-xqvht/app": {"authorization": 2},
    "clusters/kind-flightbay-e2e/resources/configmaps": {"sensitive-key": 27, "url-credentials": 2}
  },
  "note": "Counts only. Redacted values are never recorded."
}
```

## manifest.json

Always last.

```json
{
  "schemaVersion": 1,
  "bundleId": "b2a0ac0211211e27",
  "captureEnd": "2026-09-28T00:36:19.963738Z",
  "complete": true,
  "errors": 1,
  "coverage": ["kind-flightbay-e2e.resource.core.v1.pods", "..."],
  "members": {
    "bundle.json": "d2cb379cefe84faecb486bb45b9ffa39b80f5a59c10b775f7065ebee8c0ebf53",
    "...": "..."
  }
}
```

| Field | Meaning |
|---|---|
| `captureEnd` | When collection finished, UTC |
| `complete` | Whether every cluster finished collecting |
| `errors` | How many calls failed (see the collection log) |
| `coverage` | The ID of every coverage record |
| `members` | A SHA-256, in hex, of every other member |

`flightbay inspect`, `flightbay extract` and the desk check every member against `members`. There is no MD5 anywhere.

## File names

`flightbay-<case>-<YYYYMMDDTHHMMZ>.tar.zst`, with `nocase` when no case was given and `.age` appended when encrypted. While a capture runs the file is `NAME.partial`, renamed when complete.
