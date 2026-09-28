---
title: Signing and approved policies
weight: 68
description: Sign bundles with your own key so Solo can prove they came from you unmodified, have your security team sign the collection policy, and check the collector binary itself.
---

# Signing and approved policies

The manifest in every bundle proves the file has not been damaged. A signature proves who wrote it. With both, Solo can tell you the bundle it opened is exactly the one you sent, and your security team can require that captures only ever run with the policy they approved.

You keep the private keys. Solo only ever gets public keys.

## Create a signing key

```bash
flightbay keygen --name "ACME platform team"
```

```text
Key id fb:2c5e7e25308477c5 for ACME platform team
  Private key: flightbay-signing.key (keep it; never send it)
  Public key:  flightbay-signing.pub (give this to Solo)
```

The key is Ed25519. The name is shown to Solo when a bundle verifies. `-o NAME` changes the file names, and it refuses to overwrite an existing key unless you pass `--force`. The private key is written with mode 0600, and Flightbay refuses to sign with a private key that other users can read.

Send `flightbay-signing.pub` to your Solo contact once. It is safe to share.

## Sign a capture

```bash
flightbay collect --context prod-eu --case 01234567 --sign flightbay-signing.key
```

The signature covers `manifest.json`, which already holds a SHA-256 of every other file, so one signature covers the whole bundle. It is stored as `manifest.sig`, the last file in the bundle. It works the same with `--encrypt-to`: the signature is inside the encryption.

If you review the capture before sending, sign the reviewed bundle instead with `flightbay review --sign`. See [Reviewing before you send](/collect/review/).

## Check a signature

```bash
flightbay inspect flightbay-01234567-20260928T1012Z.tar.zst --verify-key flightbay-signing.pub
```

```text
Verified: yes (every member matches the manifest checksum)
Signature: signed by ACME platform team (fb:2c5e7e25308477c5), unmodified, key trusted
Trusted key: flightbay-signing.pub
Manifest SHA-256: 0c1280d89712a03d7d4ec760507f759475ba858868f15220170a1b8292726636
```

With `--verify-key`, inspect fails unless the bundle is signed by one of the keys given. Without it, inspect still reports the signature, checked against any keys in `~/.flightbay/trusted-keys/`.

| Result | Means |
|---|---|
| signed by NAME (KEY ID), unmodified, key trusted | The content is exactly what the key's owner signed, and you trust that key |
| signed by NAME (KEY ID), unmodified; key not in your trusted keys | The content has not changed since it was signed, but by a key nobody here has vouched for |
| signature invalid | The manifest was changed after signing, or the signature is damaged. Treat the bundle as untrusted. |
| not signed | There is no `manifest.sig` |

A bundle whose files do not match its manifest fails verification whatever its signature says.

## On the Solo side

Put each customer's public key in `~/.flightbay/trusted-keys/` (any name ending in `.pub`), or pass `--verify-key` to `flightbay-desk open`. The desk header shows the result once the bundle has been read to the end:

| Badge | Means |
|---|---|
| ✓ Signed by NAME (KEY ID), unmodified | Signed by a trusted key and unchanged |
| Signed by NAME (KEY ID), key not trusted | Unchanged, but the key is not in your trusted keys |
| ✕ Signature invalid | Changed after signing |
| Not signed | No signature |

When the key is trusted, the name shown is the one in your trusted key file, not the one inside the bundle.

![A signed, reviewed bundle on the Solo side](/images/coverage-origin.png)

The [Coverage](/desk/coverage/) page repeats the signature under **Origin**, with the approved policy file and the collector build.

## Approved collection policies

A security team can write the collection policy once, sign it, and require every capture to use it unchanged.

```bash
# the approver, once
flightbay keygen --name "ACME security" -o approver
flightbay policy sign collection.yaml --key approver.key      # writes collection.yaml.sig

# anyone, to check it
flightbay policy verify collection.yaml --key approver.pub
```

`policy sign` refuses a file that does not load as a policy. Distribute `collection.yaml`, `collection.yaml.sig` and `approver.pub` to whoever runs captures:

```bash
flightbay collect --config collection.yaml --policy-key approver.pub --sign flightbay-signing.key
```

| Situation | Result |
|---|---|
| `--config` with a `.sig` that matches | Allowed. The bundle records the file's SHA-256 and who approved it. |
| `--config` with a `.sig` that does not match (the file was edited) | Refused, with or without `--policy-key` |
| `--policy-key` and no `.sig`, or signed by a different key | Refused |
| `--policy-key` and a flag that widens the policy, such as `--namespaces` or `--since` | Refused: edit the file and have it signed again |
| `--policy-key` and a flag that only narrows it: `--exclude-namespaces`, `--exclude-resources`, `--no-logs`, `--no-proxy-dumps`, `--no-portforward`, `--drop-field`, `--redact-extra`, `--case` | Allowed |
| No `--policy-key`, other flags | Allowed; the flags are recorded as "refined by" |

`flightbay inspect` prints the policy file line, and the desk shows it under **Origin** on Coverage:

```text
Policy file: collection.yaml (sha256 5e1a...), approved by ACME security (fb:81c0...)
```

## Which collector wrote it

Every bundle records the SHA-256 of the `flightbay` binary that wrote it, shown by `flightbay inspect` as `Collector binary SHA-256` and in the desk under **Origin**. Compare it with the checksum of the release you downloaded.

Each release publishes `checksums-flightbay.txt`, signed with [cosign](https://docs.sigstore.dev/) keyless signing by the release workflow (`checksums-flightbay.txt.sig` and `.pem`). It covers every collector archive and `binaries-flightbay.sha256`, the SHA-256 of each `flightbay` binary. Check a download before you run it:

```bash
cosign verify-blob checksums-flightbay.txt \
  --signature checksums-flightbay.txt.sig \
  --certificate checksums-flightbay.txt.pem \
  --certificate-identity-regexp '^https://github.com/tjorourke/flightbay/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums-flightbay.txt
```

To tie a bundle to a release, look up its collector hash in `binaries-flightbay.sha256`:

```bash
grep "$(flightbay inspect BUNDLE | sed -n 's/^Collector binary SHA-256: //p')" binaries-flightbay.sha256
```

The desk archives are signed the same way, each with its own `.sig`, `.pem` and `.sha256`, and the desk container image is signed with `cosign sign`. A build from source records its own hash, which matches no release.

## Key handling

- Keep private keys out of the cluster and out of the bundle directory you share.
- One key per team is easier to manage than one per person; the name on the key is what Solo sees.
- If a private key leaks, make a new one, send Solo the new `.pub` and ask them to remove the old one from their trusted keys.
