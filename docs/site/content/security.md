---
title: Security
weight: 50
description: What leaves the cluster in a bundle, what never does, how bundles are verified and encrypted, and how the desk keeps a snapshot on your machine.
---

# Security

This page is for the security team that has to approve sending a bundle, and for the engineer who has to keep it safe once it arrives. It covers what the collector reads, what it never reads, how the file is protected, and what the desk does with it.

## The collector

- **You choose before anything is read.** The collection policy is resolved from a profile, a config file, flags or interactive prompts before the first API call. `flightbay collect --dry-run` shows the result and the RBAC it needs, and reads API discovery only.
- **It only reads.** The collector lists and gets objects, reads logs and events, and, only when you allow it, port-forwards to Solo components to read their admin endpoints. It never creates, changes or deletes anything in the cluster.
- **Least privilege is generated for you.** `flightbay rbac` prints Roles and bindings for exactly your policy. See [RBAC](/collect/rbac/).
- **Redaction happens in memory.** Objects, ConfigMap data, environment variables, log lines and diagnostic output are redacted before they are written. Unredacted data is never written to disk.
- **It writes locally.** The bundle is written to a local file, or to standard output with `-o -`. Flightbay never uploads anything.
- **It is one static binary** with no dependencies: pure Go, no cgo. Release checksums are signed with cosign, and each bundle records the binary's own SHA-256.

## What leaves the cluster

Only what the policy selects, redacted:

| Data | With the `standard` profile |
|---|---|
| Objects | Namespaced objects in the selected namespaces, full spec and status, without `managedFields` |
| Events | In the selected namespaces |
| Logs | Current and previous container logs, last 24 hours, 20 MiB per stream |
| Helm | Chart name, version, app version, status and revision per release |
| Secrets | Name, type, labels, annotations, key names and byte lengths |
| ConfigMaps | Data, redacted |
| Diagnostics | Only with `--solo-diagnostics`: proxy config dumps, istiod debug output and metrics, filtered to the selected namespaces |
| Flow logs | Only files you attach, or Hubble flows you ask for, filtered and redacted |

[Scope](/collect/scope/) explains how to narrow each of these, down to the `minimal` profile of object metadata and events.

## What never leaves

| Never collected | |
|---|---|
| Secret values | In any mode. With `--secrets metadata` the Secret body is never even requested. |
| Helm values | Release Secrets are reduced to a chart summary. |
| `managedFields` | On any object. |
| Envoy SDS secret sections | Removed from every config dump. |
| Controller krt snapshots | The agentgateway and kgateway admin snapshot spans every namespace and can hold Secret contents, so it is never read. |
| Other namespaces | Nothing outside the selection, including entries inside mesh-wide proxy and control-plane dumps, which are removed before writing. |
| Excluded types | Through any collector, including diagnostics that would reveal them. |

Credentials that turn up elsewhere (tokens in env vars, keys in ConfigMaps, `Authorization` headers in logs, passwords in URLs, JSON embedded in annotations) are replaced by the redactor. See [Secrets and redaction](/collect/secrets-and-redaction/), and `flightbay docs collects` for the full list generated from the collector's code.

## Review before sending

```bash
flightbay review BUNDLE --sign KEY       # choose what to send in the UI, export signed
flightbay inspect BUNDLE --redactions    # verify, list contents, count replacements
flightbay inspect BUNDLE --policy        # the policy recorded in the bundle
flightbay extract BUNDLE ./review        # unpack and read any file
```

The redaction report records how many values each rule replaced in each file, never the values.

## Integrity

`manifest.json`, the last file in every bundle, holds a SHA-256 of every other file, plus the capture end time and whether collection completed. `flightbay inspect`, `flightbay extract` and the desk check every file against it. A bundle that fails verification is reported as such; the desk shows what it could read, marked as unverified, and never as verified.

The manifest proves the bundle has not been damaged or altered since it was written. A signature proves who wrote it: `flightbay collect --sign KEY` (or `flightbay review --sign KEY`) signs the manifest with the customer's Ed25519 key, and `flightbay inspect --verify-key` and the desk check it against the customer's public key. Solo only ever holds public keys. See [Signing and approved policies](/collect/signing/).

## Approved policies

A security team can sign the collection policy file (`flightbay policy sign`). With `--policy-key`, the collector refuses a policy that is unsigned, signed by someone else, or edited since, and refuses command-line flags that would widen it. Every bundle records the policy file's SHA-256 and who approved it.

## Reviewing a capture

`flightbay review` opens a capture on the customer's machine, lets them remove namespaces, types, objects, log streams, lines and files, and redact more, and exports a new signed bundle with a ledger of what was removed. By default the ledger holds counts only, and nothing inside a removed namespace, including its name, reaches the export. See [Reviewing before you send](/collect/review/).

## Which build wrote it

Each bundle records the SHA-256 of the collector binary. Release checksums are signed with cosign keyless signing and include the binaries' own hashes, so a bundle can be matched to the release that wrote it.

## Encryption

`--encrypt-to` wraps the whole bundle in [age](https://age-encryption.org) encryption to one or more X25519 recipients. Only the holder of a matching identity can read it; without it, not even the bundle header is readable. `flightbay-desk keygen` creates an identity and prints the recipient to hand out. See [Sending a bundle](/collect/sending/).

Solo does not yet publish a shared support key, so the recipient comes from your Solo contact.

## The desk

The desk keeps the snapshot on the engineer's machine:

- **Local only.** The UI and API listen on `127.0.0.1` by default. A non-loopback address is refused unless you pass `--allow-remote` (meant for containers), and then the desk warns that anyone who can reach the port and has the token can read the snapshot.
- **A token on every request.** Each desk makes a random token. The browser gets it once in the URL, which sets an HttpOnly, SameSite=Strict cookie named for the desk's port, so several desks can be open in one browser at once. The API, the terminal websocket and the MCP endpoint all require the token or the cookie. Responses are sent with `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` and `Referrer-Policy: no-referrer`.
- **A read-only snapshot API.** `kubectl`, `k9s` and `stern` talk to an API that serves the bundle over TLS on `127.0.0.1`, with a certificate made for the session. Every write returns "this is a read-only snapshot", and excluded types return 403.
- **Read-only SQL.** Queries must be a single read-only statement and run in a transaction that is rolled back.
- **Temporary workspaces.** The DuckDB workspace and kubeconfig live in a directory under `~/.flightbay/workspaces` that is deleted when the desk closes, unless you pass `--keep` or your own `--workdir`. Directories left by a desk that was killed are removed after seven days.
- **AI agents.** The MCP server only talks to the local desk, and its tools are read-only. Snapshot data reaches a model only if the agent you connect uses a hosted one.

## Flightbay Enterprise

The Enterprise feature switches decide what the desk shows. They are not a security boundary. See [Flightbay Enterprise](/enterprise/).

## Still to come

Planned and not built yet: a hosted service for uploading bundles to Solo, which will check each bundle against the public key registered for that customer.
