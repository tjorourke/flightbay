---
title: Sending a bundle
weight: 70
description: Review and sign a bundle, encrypt it for a trusted recipient, verify it before you share it, and unpack or stream it with standard tools.
---

# Sending a bundle

A capture produces one file. This page covers making sure only the right person can read it, checking it has not been changed, and what is inside.

Flightbay does not upload anything. You share the file through a channel you trust.

## The usual order

1. Capture without encryption, so you can read it: `flightbay collect --context prod-eu --case 01234567`.
2. Open it with `flightbay ui BUNDLE --sign flightbay-signing.key --encrypt-to age1...`, take out anything you do not want to send, and press **Export bundle** on the Review and send page. See [Reviewing before you send](/collect/review/).
3. Check what you are sending. Only the recipient can open an encrypted export, so to browse it yourself, export once without `--encrypt-to`, open it with `flightbay ui NAME-reviewed.tar.zst`, then export again with encryption.
4. Send the reviewed file.

If you do not need to review, `flightbay collect --sign KEY --encrypt-to age1...` does it in one step. Signing is covered in [Signing and approved policies](/collect/signing/).

## Encrypting to a recipient

`--encrypt-to` wraps the whole bundle in [age](https://age-encryption.org) encryption. Only the holder of the matching identity file can read it. The file name gets `.age` on the end.

```bash
flightbay collect --context mesh1 --case 01234567 --encrypt-to age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p
# writes flightbay-01234567-20260927T1012Z.tar.zst.age
```

| Form | Meaning |
|---|---|
| `--encrypt-to age1...` | One recipient |
| `--encrypt-to age1...,age1...` | Several recipients; any one of them can open it |
| `--encrypt-to @recipients.txt` | Recipients from a file, one per line |

There is no built-in shared recipient key. Get the recipient from whoever will open the encrypted bundle.

### Creating a recipient key pair

```bash
flightbay-desk keygen -o ~/.flightbay/support.key
```

It writes the private identity to the file (mode 0600) and prints the public recipient. Share the `age1...` recipient with the sender and keep the private file secure. It refuses to overwrite an existing key.

### Opening an encrypted bundle

Every command that reads a bundle takes `-i` (or `--identity`):

```bash
flightbay ui flightbay-01234567-20260927T1012Z.tar.zst.age -i ~/.flightbay/support.key
flightbay inspect flightbay-01234567-20260927T1012Z.tar.zst.age -i ~/.flightbay/support.key
flightbay extract flightbay-01234567-20260927T1012Z.tar.zst.age ./case -i ~/.flightbay/support.key
flightbay-desk diff before.tar.zst.age after.tar.zst.age -i ~/.flightbay/support.key
```

## Verifying

Every bundle ends with `manifest.json`, which holds a SHA-256 for every other file. `flightbay inspect` reads the whole bundle and checks each one:

```bash
flightbay inspect flightbay-01234567-20260927T1012Z.tar.zst
```

```text
Bundle: b2a0ac0211211e27
Case: DEMO-0001
Collector: flightbay 0.1.0-dev
Captured: 2026-09-28T00:36:14Z
Clusters: kind-flightbay-e2e
Collected categories: objects, logs, events, helm, proxies, control-plane, metrics
Finished: 2026-09-28T00:36:19Z (complete: true, collection errors: 1)
Verified: yes (every member matches the manifest checksum)
Secret values: never collected
```

It then lists every collection with its item count and state, the collection log's problems, members by type and the redaction total.

| Flag | Adds |
|---|---|
| `--contents` | Every file in the bundle |
| `--redactions` | Redaction counts per file and rule |
| `--policy` | The collection policy recorded in the bundle |
| `--verify-key FILE` | Fail unless the bundle is signed by this public key. See [Signing](/collect/signing/). |
| `-i FILE` | An age identity, for encrypted bundles |

`inspect` also prints the signature (`Signature: signed by ACME platform team (fb:2c5e7e25308477c5), unmodified`), the manifest's own SHA-256, the collector binary's SHA-256, the approved policy file if one was used, and, for a reviewed bundle, who reviewed it and what was removed.

The desk verifies as it imports too. The header bar says **Verified** once every checksum has matched, and findings are marked provisional until then. It then shows the signature next to it.

## Unpacking

`flightbay extract` verifies and unpacks into a directory:

```bash
flightbay extract flightbay-01234567-20260927T1012Z.tar.zst ./case-01234567
```

An unencrypted bundle is also a normal zstd-compressed tar, so standard tools work:

```bash
tar --zstd -tvf flightbay-01234567-20260927T1012Z.tar.zst
tar --zstd -xf flightbay-01234567-20260927T1012Z.tar.zst -C ./case-01234567
```

The zstd window is 8 MiB by default, so stock `tar` and `zstd` open it without extra flags. `--window-size` raises it (up to 128 MiB, still readable by stock tools) for better compression on large captures.

## Streaming

`-o -` writes the bundle to standard output instead of a file. Pipe it straight into the desk to investigate a cluster you have access to without keeping a file:

```bash
flightbay collect --context mesh1 -o - | flightbay-desk open -
```

Or send it somewhere else:

```bash
flightbay collect --context mesh1 --encrypt-to age1... -o - | ssh jump 'cat > case.tar.zst.age'
```

`-o DIR` writes into a directory, and `-o FILE` to a specific file name. While a capture runs, the file is written as `NAME.partial` and renamed when it is complete. An interrupted capture leaves the `.partial` file, which fails verification.

## File names

```text
flightbay-<case>-<UTC time>.tar.zst        e.g. flightbay-01234567-20260927T1012Z.tar.zst
flightbay-<case>-<UTC time>.tar.zst.age    with --encrypt-to
flightbay-<case>-<UTC time>-reviewed.tar.zst   exported from Review and send
```

Without `--case` the case part is `nocase`. Given a directory, `flightbay ui` and `flightbay-desk open` open the newest `flightbay-*.tar.zst` or `flightbay-*.tar.zst.age` in it (`flightbay ui .` for the current directory). With no argument they open the start page, which lists the bundles here and in `~/Downloads`.

## What is inside

The layout is described in full in [Bundle format](/reference/bundle-format/). In short:

- `bundle.json` comes first: bundle ID, case, collector version, capture start, contexts and the resolved collection policy.
- Objects, logs, events and diagnostics follow in bounded chunks under `clusters/<context>/`. Logs are written early and bulky diagnostics late, so the desk can open before the file has finished reading.
- Every collection is followed by a coverage record saying whether it completed, failed, was excluded or was truncated.
- `collection-log.jsonl` records every call, including failures.
- `redaction-report.json` counts replacements.
- `manifest.json` comes next: capture end, completion state and a SHA-256 for every member.
- `manifest.sig`, only in a signed bundle, is the very last file: your signature over the manifest.
- `review-ledger.json`, only in a reviewed bundle, records what you removed.
