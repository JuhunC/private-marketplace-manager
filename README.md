# Private Marketplace Manager

A Linux REST API and web interface for safely writing VSIX packages into an existing Microsoft Private Marketplace extension directory, plus a native Windows/Linux/macOS client that mirrors **every discoverable version and published platform variant** for an editable list of extension IDs.

```text
Public VS Code Marketplace → marketplace-sync → internal HTTPS API → existing VSIX folder
                            Windows/Linux/macOS                      ↓
                                                           Microsoft Private Marketplace
```

No GitHub Actions, SSH, SFTP, or Docker installation is needed on the sync computer. Release automation in this repository only builds/tests/publishes the software; it never collects extensions on your behalf.

## Downloads

- [Latest release and sync executables](https://github.com/JuhunC/private-marketplace-manager/releases/latest)
- Manager container: `ghcr.io/juhunc/private-marketplace-manager:0.1.0`
- [Deployment guide](docs/deployment.md) · [Sync guide](docs/sync.md) · [REST API](docs/api.md)

Native client archives are published for Windows, Linux, and macOS on x64 (`amd64`) and ARM64. Verify the release's `SHA256SUMS` before running. Binaries contain their runtime; Go, Python, and PowerShell are not required. The Linux server image supports x64/ARM64. Native CI covers Linux x64, Windows x64, and macOS ARM64; the other architectures are cross-built and need a pilot on your actual machine. macOS binaries are not Apple-notarized; follow your organization's executable approval process. Minimum supported OS versions follow the release's Go toolchain (currently Go 1.27).

## Manager: deploy beside your existing marketplace

An operator needs to provision the container once with write access to the existing extension folder. Subsequent transfers only need API access.

```sh
docker pull ghcr.io/juhunc/private-marketplace-manager:0.1.0
```

Use the [ready-to-use Compose deployment](deploy/README.md), including a fully annotated [`.env.example`](deploy/.env.example). Set the existing extension directory, a separate persistent state directory, and the exact internal HTTPS origin. The setup generates a random API token and separate operator password; example credentials are deliberately not committed. The container's default UID/GID is `10001:10001`. Bind it behind your internal TLS reverse proxy.

The web interface includes inventory, version/platform metadata, manual multi-file uploads, sync reports, activity logs, and an embedded guide. It uses a password-protected operator session; the client uses the separate bearer token. No external CDN assets are loaded.

## Sync: edit a list and run

Create `extensions.txt`:

```text
# Use your own list; these are examples.
ms-vscode.hexeditor
```

Copy `examples/sync-settings.example.json` to `sync-settings.json` and set the internal API URL. Put the API credential in `api-token.txt`, accessible only to the execution account (Unix: `chmod 600 api-token.txt`; Windows: restrict file ACLs).

```sh
marketplace-sync discover --list extensions.txt --config sync-settings.json
marketplace-sync sync --list extensions.txt --config sync-settings.json
```

On Windows use `marketplace-sync.exe` (or `.\marketplace-sync.exe` from its directory). On Linux/macOS use `./marketplace-sync` if it is not on PATH. Relative file paths in settings resolve from the settings file. Command-line paths resolve from the working directory.

Every run reads the current list. New IDs get a full historical backfill. Existing IDs are checked for missing versions. Removing an ID stops future collection and **does not delete** any stored packages. Stable, prerelease, universal, and every published OS/CPU variant are included, independent of the client's host OS. The tool never installs or executes extensions.

The server inventory is the checkpoint. Moving to another computer needs only the list/settings and a newly provisioned token; no old cache is required. Use Task Scheduler, systemd/cron, or launchd for recurring execution. [Examples](docs/sync.md#scheduling).

## Storage guarantees and boundaries

- Stream to a temporary `.part` file, validate ZIP/manifests and hash, flush, then atomically publish a final VSIX name. No partial `.vsix` files are exposed.
- Identity is extension ID + exact version + target platform. Identical retries are safe; changed bytes for the same identity return `409` without replacement.
- SQLite records durable inventory and upload receipts; startup reconciles journal/file state and inventories pre-existing valid VSIXs as unmanaged.
- Existing files and previous versions are never automatically deleted. One manager owns the state/extension directory; do not use other writers concurrently.
- Manager storage confirmation does not prove the marketplace exposes that historical version to VS Code. Test multi-version/platform behavior against your deployed Microsoft container.
- “All versions” means all records/assets still exposed by the public source. Removed, hidden, or no-longer-downloadable releases cannot be reconstructed; discovery/download failures are reported. The gallery endpoint is an upstream implementation detail and may change.
- SHA-256 and manifest validation establish transfer consistency, not publisher authenticity. Publisher-signature verification, malware scanning, granular multi-user roles, automatic marketplace visibility checks, and version pruning are outside v0.1.0. One operator password and one automation token are supported; restart to rotate secrets.
- Uploads are synchronous: `201` means stored, `200` means identical content already stored. Failed transfers can be retried safely. See the API guide for limits and error codes.

## Build and test

Use Go 1.27.1 or the version specified in `go.mod`:

```sh
go test -race ./...
go vet ./...
go build ./cmd/manager
go build ./cmd/marketplace-sync
./scripts/release.sh v0.1.0
```

The manager uses pure-Go SQLite. The sync client has no SQLite dependency. Container builds are multi-stage and run as a non-root user. `IMPLEMENTATION_PLAN.md` is the design roadmap; the behavior described here and in `docs/` is the delivered v0.1.0 contract.
