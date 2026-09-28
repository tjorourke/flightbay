---
title: Reviewing before you send
weight: 65
description: Open your capture in the UI, take out what you do not want to send, redact more, check what Solo will receive, and export a signed bundle with a ledger of what you removed.
---

# Reviewing before you send

The collection policy decides what Flightbay reads. Review is the second look, after capture and before anything leaves your organisation. You open the bundle in the UI on your own machine, see everything in it, take out what you do not want to send, and export a new bundle. The original is never changed.

![Review and send](/images/review.png)

## Open the capture

Capture without `--encrypt-to` (you need to be able to read the bundle to review it), then open it as usual:

```bash
flightbay ui flightbay-01234567-20260928T1012Z.tar.zst --sign flightbay-signing.key
```

Any bundle opened from a file has a **Review and send** page in the sidebar, and a **Remove from the bundle** button on every object. Every other page works as normal, so you can search, read logs and open objects to decide what should go, then remove it where you found it. Nothing is removed until you choose to.

The page is not there when the desk was fed a stream (`flightbay collect -o - | flightbay-desk open -`) or is mirroring a live cluster, because there is no file to export from. A live mirror can be saved as a bundle first.

| Option on `flightbay ui` | Does |
|---|---|
| `--sign KEY` | Sign what you export with your private key, so Solo can check it came from you unmodified. See [Signing](/collect/signing/). Without it the export is unsigned. |
| `--encrypt-to age1...` | Encrypt what you export to Solo's recipient |
| `--reviewer "Jane Smith, platform"` | Who reviewed it, recorded in the ledger (also editable on the Redact tab) |
| `--review` | Open on the Review and send page instead of the Overview |
| `--review-out FILE` | Where the export goes |
| `-i FILE` | An age identity, if the capture itself is encrypted |
| `--verify-key FILE` | Check the capture's own signature against this key |

## What you can take out

| Tab | Removes |
|---|---|
| Namespaces | Every object, event, log stream and proxy dump in the namespace, and the entries that name it inside other namespaces' proxy and control-plane dumps |
| Resource types | Every object of the type, for example all ConfigMaps |
| Log streams | Whole container log streams |
| Diagnostics and flows | Single proxy or control-plane dump files, attached flow log files, or every event |
| Picked items | Objects and log lines you removed one at a time |
| Redact | Text patterns, log lines to drop, and object fields to remove |

To remove one object, open it anywhere in the desk and choose **Remove from the bundle**. To remove log lines, find them in the [Log explorer](/desk/logs/): a line's detail has **Remove this line from the bundle**, and **Remove matching lines** takes out every line the current search matches (up to 20,000 at a time; use a pattern on the Redact tab for more).

When you remove a namespace, **Also replace the removed namespaces' names wherever they still appear** is on by default. It replaces the name in what is left, for example `db.secret-project.svc` in another namespace's logs becomes `db.[REMOVED NAMESPACE].svc`.

### Redact tab

| Field | Takes |
|---|---|
| Replace text | Go regular expressions, one per line. Every match in objects, logs, events and dumps becomes `[REDACTED]`. |
| Drop log lines | Regular expressions, one per line. A matching log line is removed whole. |
| Remove fields from objects | Paths, one per line, applied to every object |

Field paths use dots between keys, `["..."]` for keys that contain dots or slashes, `*` and `?` as globs, and `[*]` for every item in a list:

```text
metadata.annotations["corp.example/*"]
spec.template.spec.containers[*].env
data.license
```

A path cannot remove an object's `kind`, `apiVersion`, name or namespace; remove the object instead. The rules also apply inside `kubectl.kubernetes.io/last-applied-configuration`, which holds a full copy of the object.

## What Solo will receive

The panel at the top updates as you choose. It shows what is kept and what is removed for objects, events, log lines, diagnostic files and flow files, plus how many values were redacted, fields removed and names replaced. It runs the real export into nowhere, so the numbers are exactly what the export will do.

## Export

**Export bundle** writes `NAME-reviewed.tar.zst` next to the original (`.age` on the end when encrypted). The page shows its size, SHA-256, whether it is signed and encrypted, and the commands to check it:

```bash
flightbay inspect flightbay-01234567-20260928T1012Z-reviewed.tar.zst
flightbay ui flightbay-01234567-20260928T1012Z-reviewed.tar.zst
```

Opening the reviewed bundle yourself is the surest way to see exactly what Solo will see.

The export reads the original from start to finish and checks every file against its manifest. If the original is damaged, the export fails; a damaged capture is never signed again.

## The ledger

Every reviewed bundle carries `review-ledger.json`. It records:

- who reviewed it and when;
- the original bundle's ID, its manifest SHA-256 and whether its signature checked out;
- how many namespaces, types, objects, events, log streams, log lines, diagnostic files and flow files were removed;
- how many values were redacted, fields removed and names replaced;
- the names of removed resource types.

It never records your redaction patterns or field paths, which could describe what they hide.

Names of removed namespaces, objects and streams are left out by default, and so are the collection records inside removed namespaces, which would name them. Turn on **List removed namespaces, objects and streams by name** on the Redact tab (or `--disclose-names` with a plan) if you would rather Solo knew what to ask about.

Resource type names are always listed. Solo's desk uses them to mark the checks that need a removed type as not applicable, rather than failing them for data you chose not to send.

## Repeatable reviews with a plan

**Download plan** saves your choices as YAML. Apply the same plan to later captures without opening the UI:

```bash
flightbay review flightbay-01234567-20260929T0900Z.tar.zst --plan review-plan.yaml --sign flightbay-signing.key --encrypt-to age1...
```

```yaml
reviewer: Jane Smith, platform
scrubNames: true
remove:
  namespaces:
    - namespace: secret-project
  types:
    - resource: configmaps
  objects:
    - {cluster: prod-eu, resource: pods, namespace: shop, name: debug-*}
  logStreams:
    - {namespace: shop, container: sidecar}
  diagnostics:
    - {namespace: shop, file: stats.prom}
dropLogLinesMatching:
  - 'customer_id='
redact:
  - 'ACME-[0-9]{6}'
dropFields:
  - 'metadata.annotations["corp.example/*"]'
```

Every field in a removal takes `*` and `?` globs, and an empty field matches anything. Picked log lines are stored as the chunk and position of each line with a hash of its text, so a plan made against one capture never removes the wrong line from another; lines that do not match are left alone.

A reviewed bundle can be reviewed again. The new ledger keeps the earlier one under `previous`.

## Command reference

See [`flightbay ui`](/reference/cli-flightbay/#flightbay-ui) and the options of [`flightbay-desk open`](/reference/cli-flightbay-desk/#flightbay-desk-open), which `flightbay ui` passes through. [`flightbay review`](/reference/cli-flightbay/#flightbay-review) is the plan-file form without the UI.
