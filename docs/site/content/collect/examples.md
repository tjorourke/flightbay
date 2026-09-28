---
title: "Collect examples"
weight: 5
description: "Copy-and-paste capture commands for common cases: one namespace, the agentic stack, Istio ambient, several clusters, sharing less, encrypting, and adding flow logs."
---

Each recipe is a complete command. Run it with `--dry-run` first to see exactly what it will read and the RBAC it needs, then without to capture. A dry run reads discovery only, no objects or logs.

```bash
flightbay collect --context my-cluster --namespaces payments --dry-run   # preview
flightbay collect --context my-cluster --namespaces payments             # capture
```

Every capture writes one file, `flightbay-<cluster>-<time>.tar.zst`, in the current directory (or where `-o` says). Check it with `flightbay inspect FILE`, then open it with `flightbay ui FILE`.

## One application namespace

The smallest useful capture: objects, logs and events for one namespace.

```bash
flightbay collect --context prod-eu --namespaces payments
```

Several namespaces, with globs:

```bash
flightbay collect --context prod-eu --namespaces 'payments,checkout-*'
```

## The agentic stack (agentgateway, kagent, MCP servers)

Capture the namespaces your agents, gateways and MCP servers run in, plus the Solo diagnostics (agentgateway config dumps and metrics). This is what [Agent reach](/desk/agent-reach/) and the agentic [Scorecard](/desk/scorecard/) checks need.

```bash
flightbay collect --context prod-eu \
  --namespaces 'agentgateway-system,kagent,mcp-*,agents-*' \
  --solo-diagnostics
```

To see what each agent actually called, the ztunnel access logs must be in the capture: include `istio-system` (where ztunnel runs) in `--namespaces`. See [Observed traffic](/collect/observed-traffic/).

## Istio ambient

The control plane, ztunnel and istio-cni, your application namespaces, and the proxy and istiod debug dumps:

```bash
flightbay collect --context prod-eu \
  --namespaces 'istio-system,payments,checkout' \
  --solo-diagnostics
```

Add `--cluster-scoped` if the problem might involve nodes, cluster-wide RBAC or CRDs. It also lets the Kubernetes checks for ClusterRoles and node health run instead of showing "not applicable".

```bash
flightbay collect --context prod-eu --namespaces 'istio-system,payments' --solo-diagnostics --cluster-scoped
```

## Everything except the system namespaces

```bash
flightbay collect --context prod-eu --exclude-namespaces 'kube-*,local-path-storage'
```

## Several clusters in one bundle

For multicluster problems (peering, east-west gateways, the same object in two clusters), give each context. The bundle holds all of them, and [Compare](/desk/compare/) can show one object across clusters.

```bash
flightbay collect --context mesh-eu,mesh-us --namespaces 'istio-system,payments' --solo-diagnostics
```

## Share less

Every option narrows what is read. Excluded data is never requested from the API server, never written to the bundle, and checks that need it show "not applicable" instead of failing. See [Secrets and redaction](/collect/secrets-and-redaction/).

```bash
# no Secret key names, and no ConfigMaps at all
flightbay collect --context prod-eu --namespaces payments --secrets metadata --configmaps omit

# never read these resource types
flightbay collect --context prod-eu --namespaces payments --exclude-resources configmaps,secrets

# only these resource types (an allowlist; exclusions still win)
flightbay collect --context prod-eu --namespaces payments --include-resources 'pods,deployments.apps,*.gateway.networking.k8s.io'

# remove your own annotations from every object before writing
flightbay collect --context prod-eu --namespaces payments --drop-field 'metadata.annotations["corp.example/*"]'

# only objects with a label
flightbay collect --context prod-eu --namespaces kagent --label-selector app.kubernetes.io/part-of=kagent

# metadata only: names, labels and annotations, no specs, no logs
flightbay collect --context prod-eu --profile minimal
```

Secret values are never collected in any mode.

## Less log data

```bash
# the last two hours, at most 5 MiB per container stream, current and previous only
flightbay collect --context prod-eu --namespaces payments \
  --since 2h --max-log-bytes 5242880 --log-streams current,previous

# only some pods and containers
flightbay collect --context prod-eu --namespaces payments --log-pods 'checkout-*' --log-containers app

# no logs at all
flightbay collect --context prod-eu --namespaces payments --no-logs
```

`--max-log-bytes` takes a number of bytes. See [Logs](/collect/logs/).

## Encrypt it for Solo support

Encrypt the bundle to a public key, so only the holder of the private key can open it. Whoever receives it creates the key pair with `flightbay-desk keygen` and sends you the public part.

```bash
flightbay collect --context prod-eu --namespaces payments --encrypt-to age1qyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqs3l9yat
```

The file ends in `.tar.zst.age`. It opens with `flightbay ui FILE -i key.txt`. See [Sending a bundle](/collect/sending/).

## Sign it

Create a key once, send Solo the `.pub`, and sign every capture with the private key:

```bash
flightbay keygen --name "ACME platform team"
flightbay collect --context prod-eu --namespaces payments --sign flightbay-signing.key
```

See [Signing and approved policies](/collect/signing/).

## Only run the policy your security team approved

```bash
flightbay policy sign collection.yaml --key approver.key       # the approver, once
flightbay collect --config collection.yaml --policy-key approver.pub --sign flightbay-signing.key
```

An edited or unsigned policy, or a flag that widens it, is refused.

## Review it, then send the reviewed copy

Capture without encryption, pick what to leave out in the UI, and export a signed, encrypted copy:

```bash
flightbay collect --context prod-eu --namespaces payments,istio-system
flightbay review flightbay-nocase-*.tar.zst --sign flightbay-signing.key --encrypt-to age1...
```

The same review from a saved plan, without the UI:

```bash
flightbay review flightbay-nocase-*.tar.zst --plan review-plan.yaml --sign flightbay-signing.key
```

See [Reviewing before you send](/collect/review/).

## Add flow logs

Attach flow logs from your CNI or cloud so the desk can show what actually talked to what, even without Istio. The files go inside the bundle and are covered by its checksums.

```bash
# Cilium Hubble export, and AWS VPC flow logs
flightbay collect --context prod-eu --namespaces payments --attach-flows hubble.jsonl,vpc-flows.log

# read the last 20,000 flows straight from hubble-relay (needs the hubble CLI)
flightbay collect --context prod-eu --namespaces payments --hubble-flows
```

Formats, and the generic CSV any other tool can produce, are in [Observed traffic](/collect/observed-traffic/).

## Stream it straight into the desk

When you are collecting from a cluster you can reach yourself, skip the file and open the snapshot while it arrives:

```bash
flightbay collect --context kind-dev --namespaces payments -o - -q | flightbay-desk open -
```

## Choose interactively

Prompts for the categories and scope, shows the plan, then asks before collecting:

```bash
flightbay collect --context prod-eu --interactive
```

## Save the choices in a file

For a capture you repeat, or one your security team approves once, put the options in a YAML file. Flags still refine it, and exclusions in the file always win.

```bash
flightbay collect --context prod-eu --config flightbay-policy.yaml
```

See [Scope](/collect/scope/) for the file format.

## Scan a repository instead

If there is no cluster to hand, or you want to check changes before they are deployed, scan the manifests, Helm charts and Kustomize overlays in a repository. See [Scan examples](/scan/examples/).

## Watch a cluster live instead

If you have access to the cluster yourself and want to watch it change rather than capture it once, mirror it into the desk. It uses the same scope and redaction flags, only reads (`get`, `list`, `watch` and `pods/log`), and can save the mirror as a normal bundle at any moment.

```bash
flightbay rbac --live --namespaces payments          # the RBAC it needs: collect's, plus watch
flightbay ui --live --context prod-eu --namespaces payments --exclude-resources configmaps
```

See [Live mode](/desk/live/).
