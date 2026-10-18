# Install and upgrade

Flightbay is two binaries. `flightbay` is the collector, a static Go binary for macOS, Linux and Windows. `flightbay-desk` opens bundles, on macOS and Linux. Someone who only captures a cluster needs just `flightbay`.

## Homebrew on macOS

```bash
brew tap tjorourke/flightbay https://github.com/tjorourke/flightbay
brew install flightbay
```

The formula installs both binaries. If Homebrew asks you to trust this third-party formula (Homebrew 7), run `brew trust --formula tjorourke/flightbay/flightbay`, then repeat `brew install flightbay`.

To update them later:

```bash
brew update
brew upgrade flightbay
```

If source-install symlinks already occupy `/opt/homebrew/bin/flightbay` and `flightbay-desk`, run `brew link --overwrite flightbay` after installing to switch those links to Homebrew. The source-built binaries in `~/go/bin` stay where they are.

## Release archives

For Linux and Windows, or to install without Homebrew, download the matching archive from [GitHub Releases](https://github.com/tjorourke/flightbay/releases/latest):

| Archive | What it is |
|---|---|
| `flightbay_<version>_<os>_<arch>.tar.gz` | The collector for macOS and Linux, with an SBOM |
| `flightbay_<version>_windows_<arch>.zip` | The collector for Windows |
| `flightbay-desk_<version>_<os>_<arch>.tar.gz` | The desk for macOS and Linux |

macOS downloads are labelled `darwin`. Apple Silicon is `arm64`, and Intel/AMD is `amd64`. Extract the archive into a working directory and run the binary from there: `./flightbay` on macOS and Linux, `.\flightbay.exe` in PowerShell. Flightbay uses the normal kubeconfig location or `$KUBECONFIG`.

Release checksums are signed with cosign. [Which collector wrote it](site/content/collect/signing.md#which-collector-wrote-it) has the commands to check a download before you run it.

## Upgrading

With a direct installation on macOS or Linux, `flightbay upgrade` installs the latest published stable release in place, and updates a desk installed beside it:

```bash
flightbay upgrade --check    # the installed version and the available release, without installing
flightbay upgrade
```

`--collector-only` leaves the desk alone, and `--version` picks a published version. Homebrew-managed binaries are updated with `brew upgrade flightbay` instead. Restart a running desk after upgrading. Versions before 0.2.5 need a one-time download or a Homebrew installation to get the upgrade command.

## Container image

The `Dockerfile` builds an image of the desk with `kubectl`, `k9s`, `istioctl`, `stern`, `jq` and `yq` included, for a machine without those tools:

```bash
docker build -t flightbay-desk .
docker run --rm -it -p 8484:8484 -v "$PWD:/data" flightbay-desk \
  open /data/flightbay-01234567-20260927T1012Z.tar.zst \
  --listen 0.0.0.0:8484 --allow-remote --no-browser
```

Inside a container the desk has to listen beyond loopback, so it needs `--allow-remote`. The token is still required: open the `UI:` address it prints and change the host to `localhost`.

## From source

See [Contributing](../CONTRIBUTING.md#development-setup). `make install` builds both binaries and installs them into `$(go env GOPATH)/bin`.
