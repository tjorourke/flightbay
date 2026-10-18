# Commands

| Command | |
|---|---|
| `flightbay collect` | Capture a cluster (alias `capture`) |
| `flightbay scan [PATH...]` | Build a bundle from a code repository and score it; `--fail-on` makes it a CI gate |
| `flightbay ui [bundle \| dir]` | Open the UI on a capture, or the newest in a directory; no arguments opens the start page (live, a file or a repo) |
| `flightbay inspect BUNDLE` | Verify a bundle and show what's in it; `--verify-key` checks the signature |
| `flightbay review BUNDLE --plan FILE` | Remove objects, logs and namespaces from a bundle without the UI and export a signed copy (in the UI it is the Review and send page) |
| `flightbay keygen` | Create an Ed25519 signing key pair |
| `flightbay policy sign / verify` | Approve a collection policy file, and check an approval |
| `flightbay extract BUNDLE DIR` | Verify and unpack it (it's also a normal `tar.zst`: `tar --zstd -xf`) |
| `flightbay rbac` | Least-privilege RBAC for your options |
| `flightbay upgrade` | Install the latest release in place; `--check` only shows it |
| `flightbay docs collects` | What is and isn't collected |
| `flightbay-desk open` | What `flightbay ui` runs; `--keep` keeps the workspace, `-i key` opens encrypted bundles |
| `flightbay-desk start` | The start page that `flightbay ui` with no arguments opens |
| `flightbay-desk search / query / findings / proxy-config / sql / posture` | The terminal helpers (`fbsearch`, `fbq`, `fbfind`, `fbpc`, `fbsql`, `fbscore`), usable anywhere a workspace is open |
| `flightbay-desk diff A B` | Compare two captures |
| `flightbay-desk drift REPO CLUSTER` | Compare a repo scan with a capture |
| `flightbay-desk mcp` | MCP server for agents |
| `flightbay-desk features` | Flightbay Enterprise features and whether they are on |
| `flightbay-desk keygen` | Create an age key pair for `--encrypt-to` |

Every flag is in the generated references: [flightbay](site/content/reference/cli-flightbay.md) and [flightbay-desk](site/content/reference/cli-flightbay-desk.md).

## How it works

- **The bundle** is a plain `tar` stream compressed with zstd. `bundle.json` comes first, with the capture policy; `manifest.json` comes last, with a SHA-256 for every file, followed only by `manifest.sig` when the bundle is signed. Logs are written early and bulky diagnostics late, so the UI can open before the file has finished reading.
- **The collector** is a single static Go binary with no dependencies. It redacts in memory and never writes unredacted data to disk.
- **The desk** (`flightbay-desk`) stores logs, objects and events in DuckDB in a temporary workspace. It keeps about 90 bytes per log line on disk: 10 million lines is 0.8 GB and loads in about 35 seconds. Searching 10 million lines takes 20 to 250 ms for structured queries and under a second for free text. It also serves a read-only Kubernetes API, which is what `kubectl` and `k9s` talk to.

[Bundle format](site/content/reference/bundle-format.md) describes the file member by member.
