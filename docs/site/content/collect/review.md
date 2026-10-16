---
title: Reviewing before you send
weight: 65
description: Open your capture in the UI, remove data you do not want to share, redact more, preview the export, and sign the reviewed bundle with a ledger of what changed.
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
| `--sign KEY` | Sign what you export with your private key, so recipients can check it came from you unmodified. See [Signing](/collect/signing/). Without it the export is unsigned. |
| `--encrypt-to age1...` | Encrypt what you export to the chosen recipient |
| `--reviewer "Jane Smith, platform"` | Who reviewed it, recorded in the ledger (also editable on the Redact tab) |
| `--review` | Open on the Review and send page instead of the Overview |
| `--review-out FILE` | Where the export goes |
| `-i FILE` | An age identity, if the capture itself is encrypted |
| `--verify-key FILE` | Check the capture's own signature against this key |

## What to send

Everything is selected to start with. The **What to send** panel at the top has one tickbox per type of content:

| Tickbox | Covers |
|---|---|
| Objects | Every resource type |
| Logs | Every container log stream |
| Events | Every Kubernetes event |
| Proxy and control-plane dumps | Every diagnostics file |
| Flow logs | Every attached flow log file |

Untick one to leave that content out, or use **Deselect all** and tick back only what you want to send. **Select all** puts everything back, including objects and lines you removed one at a time. A tickbox shows as partly ticked when you have picked some of it in the tabs below, with a count such as "12 of 19 container log streams selected".

## Choosing in detail

Each tab lists rows with a **Send** tickbox, all ticked to start with, and **Select all** and **Deselect all** buttons. With a filter typed, the buttons act on the rows the filter shows, so "Filter namespaces: team-" then **Deselect all** leaves out every namespace starting `team-`.

| Tab | Rows |
|---|---|
| Namespaces | A namespace. Unticking it leaves out every object, event, log stream and proxy dump in it, and the entries that name it inside other namespaces' proxy and control-plane dumps |
| Resource types | A type, for example all ConfigMaps |
| Log streams | A whole container log stream |
| Diagnostics and flows | A proxy or control-plane dump file, an attached flow log file, and every event |
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

## Preview the export

The panel at the top updates as you choose. It shows what is kept and what is removed for objects, events, log lines, diagnostic files and flow files, plus how many values were redacted, fields removed and names replaced. It runs the real export into nowhere, so the numbers are exactly what the export will do.

## Signing the export

The **Sign the export** panel chooses the key the export is signed with. A signature lets recipients check the bundle came from you and was not changed since.

- **Choose a key** from the list: Flightbay finds signing keys in `~/.flightbay/keys`, in the directory you started from, and next to a key you used before.
- **Use a key file** anywhere else by typing its path.
- **Create a signing key…** opens a dialog. Type who signs (for example "ACME platform team") and choose **Create and use this key**. The private key is saved in `~/.flightbay/keys`, readable only by you, exports are signed with it straight away, and the next dialog shows the public key with **Copy public key**. **Show the public key** brings it back later. Share the public key (the `.pub` file) with recipients; never share the `.key`.
- **Don't sign** turns signing off for this export.

`flightbay ui BUNDLE --sign KEY` (or `--live --sign KEY`) starts with that key chosen. The page can change it at any time.

## Exporting from a live cluster

In [live mode](/desk/live/), **Export and sign…** in the live banner (or **Review and send** in the menu) opens this page on the running cluster. The first visit freezes the mirror into a bundle in the workspace, so what you choose applies to a fixed copy and nothing moves while you decide. **Freeze again now** takes a fresh copy, keeping your choices.

The export is written to `--save-dir` (the directory you started from by default) as `flightbay-live-CONTEXT-TIME-reviewed.tar.zst`, signed with the key you chose. Nothing is ever written to the cluster.

## Export

**Export bundle** writes `NAME-reviewed.tar.zst` next to the original (`.age` on the end when encrypted). The page shows its size, SHA-256, whether it is signed and encrypted, and the commands to check it:

```bash
flightbay inspect flightbay-01234567-20260928T1012Z-reviewed.tar.zst
flightbay ui flightbay-01234567-20260928T1012Z-reviewed.tar.zst
```

Opening the reviewed bundle yourself is the surest way to see exactly what the recipient will receive.

The export reads the original from start to finish and checks every file against its manifest. If the original is damaged, the export fails; a damaged capture is never signed again.

## The ledger

Every reviewed bundle carries `review-ledger.json`. It records:

- who reviewed it and when;
- the original bundle's ID, its manifest SHA-256 and whether its signature checked out;
- how many namespaces, types, objects, events, log streams, log lines, diagnostic files and flow files were removed;
- how many values were redacted, fields removed and names replaced;
- the names of removed resource types.

It never records your redaction patterns or field paths, which could describe what they hide.

Names of removed namespaces, objects and streams are left out by default, and so are the collection records inside removed namespaces, which would name them. Turn on **List removed namespaces, objects and streams by name** on the Redact tab (or `--disclose-names` with a plan) if you want the recipient to know what to ask about.

Resource type names are always listed. Flightbay uses them to mark checks that need a removed type as not applicable, rather than failing them for data you chose not to share.

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
