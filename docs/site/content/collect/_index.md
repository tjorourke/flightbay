---
title: Collecting a snapshot
weight: 20
description: Preview a collection policy and capture the selected cluster data.
---

# Collecting a snapshot

For copy-and-paste commands for common cases, see [Collect examples](/collect/examples/).

`flightbay collect` (alias `capture`) reads one or more clusters and writes a single bundle file. It only reads what the collection policy selects, it redacts in memory before anything is written, and it never collects Secret values.

Share the bundle for investigation without granting cluster access. Open it in the desk, check it with `flightbay inspect`, or unpack it with standard tools.

## Dry run first

Always start with `--dry-run`. It reads API discovery and nothing else: no objects, no logs, no port-forwards.

```bash
flightbay collect --context mesh1 --namespaces payments,istio-system --dry-run
```

It prints, per cluster:

- the categories that will be collected (objects, logs, events and so on);
- the namespaces in scope;
- anything that will **not** be collected and why, for example "helm: release summaries are read from Secret data, which needs secrets=keys";
- the log window, streams and byte limit;
- every resource type that will be listed;
- whether diagnostics will port-forward to Solo components;
- attached or Hubble flow sources;
- the RBAC the run needs.

Add `--json` for the plan as JSON. When the plan is right, drop `--dry-run` and run the same command.

## How the policy is built

The policy is resolved in this order, and each step refines the last:

1. A **profile**: `minimal`, `standard` (the default) or `custom`.
2. A **config file**, if you pass `--config`. It names its own profile.
3. **Flags**, which change individual settings.

Exclusions always win. If you exclude a resource type, no collector reads it, including the diagnostics: an endpoint that would expose an excluded type is skipped and the reason is recorded in the bundle.

`--interactive` asks the main questions (profile, namespaces, logs, Secrets, ConfigMaps, excluded types, cluster-scoped objects), shows the plan and asks for confirmation before reading anything. `--save-config FILE` writes the resolved policy so you can run the same capture again with `--config FILE`.

## What is in a standard capture

| Collected | Detail |
|---|---|
| Objects | Every namespaced resource type you can list, in every namespace you can read, with full spec and status. `managedFields` is dropped. |
| Events | Core and `events.k8s.io/v1` events in the selected namespaces. |
| Logs | Current and previous container logs from the last 24 hours, up to 20 MiB per stream, newest lines kept. |
| Helm | Release summaries (chart, version, app version, status, revision) read from Helm's release Secrets. Values are never kept. |
| Secrets | Name, type, labels, annotations, key names and byte lengths. Never values. |
| ConfigMaps | Data kept, with credentials replaced by the redactor. |

Not collected unless you ask: cluster-scoped objects such as nodes and CRDs (`--cluster-scoped`), and proxy dumps, control-plane debug output and metrics (`--solo-diagnostics`).

## Useful commands

```bash
flightbay collect --context mesh1 --dry-run                     # what it would read, and the RBAC it needs
flightbay collect --context mesh1                               # the standard profile
flightbay collect --context mesh1 --namespaces payments,istio-system --since 2h --case 01234567
flightbay collect --context mesh1 --solo-diagnostics            # add proxy dumps, istiod debug output and metrics
flightbay collect --context mesh1,mesh2                         # several clusters in one bundle
flightbay collect --interactive                                 # answer questions, preview, confirm
flightbay inspect flightbay-*.tar.zst --redactions              # check it before you send it
flightbay docs collects                                         # the full list of what is and is not collected
```

## The rest of this section

| Page | Covers |
|---|---|
| [Scope](/collect/scope/) | Profiles, contexts, namespaces, resource types, label selectors, config files |
| [Logs](/collect/logs/) | Time window, byte limits, streams, pod and container filters |
| [Secrets and redaction](/collect/secrets-and-redaction/) | What never leaves, and how credentials are replaced |
| [Solo diagnostics](/collect/solo-diagnostics/) | Envoy, ztunnel, agentgateway and istiod admin endpoints |
| [Observed traffic](/collect/observed-traffic/) | Access logs and flow logs for who called whom |
| [RBAC](/collect/rbac/) | Least-privilege permissions for your policy |
| [Reviewing before you send](/collect/review/) | Remove namespaces, types, objects, logs and files in the UI, redact more, export a signed bundle |
| [Signing and approved policies](/collect/signing/) | Sign bundles with your key, have your security team sign the policy, check the collector build |
| [Sending a bundle](/collect/sending/) | Encryption, verification, streaming and the file format |

The command-line reference for every flag is in [flightbay CLI](/reference/cli-flightbay/).
