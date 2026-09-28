---
title: Secrets and redaction
weight: 30
description: What Flightbay never collects, how Secrets and ConfigMaps are handled, and how credentials in everything else are replaced before anything is written.
---

# Secrets and redaction

A bundle is only useful if a security team is willing to let it leave. This page explains what never goes into a bundle, and how credentials that turn up in ordinary objects, logs and dumps are replaced before anything is written to disk.

Everything here happens in the collector's memory. Unredacted data is never written to disk, not even to a temporary file.

## Secrets never carry values

Secret values are never collected, whatever options you choose. You decide how much of the rest you want with `--secrets`:

| `--secrets` | What is kept |
|---|---|
| `keys` (default in `standard`) | Name, namespace, type, labels, annotations (without `last-applied-configuration`), owner references, and each key's name and length in bytes. |
| `metadata` (default in `minimal`) | Name, namespace, type, labels and annotations. The Secret body is never requested: Flightbay uses the Kubernetes metadata API, so the values do not even cross the network. |
| `omit` | Secrets are not read at all. |

This is what a Secret looks like in a bundle captured with `keys`:

```json
{
  "apiVersion": "v1",
  "kind": "Secret",
  "metadata": {"name": "checkout-db", "namespace": "payments"},
  "type": "Opaque",
  "x-flightbay": {
    "captureMode": "keys",
    "keys": [
      {"name": "password", "bytes": 25},
      {"name": "username", "bytes": 8}
    ],
    "valuesCollected": false
  }
}
```

Key names and sizes are enough for most checks: whether a referenced key exists, whether a TLS Secret has both halves, whether an empty key is the reason something fails.

**Helm release Secrets** are reduced to the chart name, version, app version, status and revision. Helm values are never kept. Reading them needs `--secrets keys`, because the summary is decoded from the Secret data in memory and everything else is thrown away.

## ConfigMaps

| `--configmaps` | What is kept |
|---|---|
| `scrubbed` (default in `standard`) | The data, with every value passed through the redactor. |
| `metadata` | Name, namespace, labels and annotations only. |
| `omit` | ConfigMaps are not read at all. |

## The redactor

Every object spec, ConfigMap value, environment variable, log line and diagnostic dump goes through the same set of rules before it is written:

| Rule | Replaces |
|---|---|
| `private-key` | PEM private keys. Certificates are kept, because expiry and SAN checks need them. |
| `authorization` | `Authorization: Bearer ...` and `Basic ...` header values. |
| `jwt` | JSON Web Tokens. |
| `aws-access-key` | AWS access key IDs (`AKIA...`, `ASIA...`). |
| `api-key` | `sk-...` and `sk-ant-...` style API keys. |
| `url-credentials` | `user:password@` in URLs. |
| `sensitive-key` | The value of any key or variable whose name contains `password`, `passwd`, `secret`, `token`, `api_key` or `credential`. |
| `extra-N` | Your own patterns from `--redact-extra`. |

Replaced values become `[REDACTED]` (or `[REDACTED PRIVATE KEY]`), so you can still see that a value was there. Names that point at a credential rather than hold one, such as `secretName`, `tokenPath` or `apiKeyRef`, are left alone, as are plain numbers and booleans.

### JSON inside strings

Some fields hold a whole document as a string. The best known is the `kubectl.kubernetes.io/last-applied-configuration` annotation, which holds a full copy of the object as it was applied, environment variables included. JSON files in ConfigMaps are another.

The redactor parses JSON held inside a string and applies the same rules to it, so a password inside an embedded manifest is caught just like one in the object itself. On Secrets, `last-applied-configuration` is removed entirely.

### Your own patterns

`--redact-extra FILE` adds Go regular expressions, one per line. Lines starting with `#` are ignored. Any match is replaced.

```text
# redact-extra.txt
corp-[0-9]{8}
internal\.example\.com/[a-z]+/secret
```

```bash
flightbay collect --redact-extra redact-extra.txt --dry-run
```

A pattern that does not compile stops the run before anything is read.

## Review before you send

`redaction-report.json` in the bundle counts how many replacements each rule made in each file. It never records the values. Read it with:

```bash
flightbay inspect flightbay-01234567-20260927T1012Z.tar.zst --redactions
```

```text
Redactions: 52 (values are never recorded)
  clusters/kind-flightbay-e2e/logs/payments/checkout-85b9d479d6-xqvht/app: authorization=2
  clusters/kind-flightbay-e2e/resources/configmaps: sensitive-key=27, url-credentials=2
  clusters/kind-flightbay-e2e/resources/deployments.apps: authorization=2, sensitive-key=2
  clusters/kind-flightbay-e2e/resources/secrets: last-applied=1, secret-value=7
```

A count where you did not expect one is worth a look: extract the bundle and check the file (see [Sending a bundle](/collect/sending/)). The same report is on the [Coverage](/desk/coverage/) page in the desk.

## What never leaves

| Never collected | Why |
|---|---|
| Secret values | By design, in every mode. |
| Helm values | Release Secrets are reduced to a chart summary. |
| `managedFields` | On any object. It is noise, and can reveal who changed what. |
| Envoy SDS secret sections | Removed from every config dump. Certificate expiry comes from `/certs`, which holds no keys. |
| The agentgateway and kgateway controller krt snapshot | It spans every namespace and can hold Secret contents (TLS keys, LLM provider keys), so it can honour neither a namespace limit nor "never Secret values". The controllers' logs, metrics and objects are collected instead. |
| Anything from namespaces outside the selection | Including entries inside proxy and control-plane dumps, which are removed before writing. |
| Anything a policy exclusion covers | Through any collector. A diagnostic endpoint that would expose an excluded type is skipped and the reason recorded. |

`flightbay docs collects` prints the full list, generated from the collector's own tables. A test regenerates `docs/COLLECTS.md` from the same code, so the document and the collector cannot drift apart.
