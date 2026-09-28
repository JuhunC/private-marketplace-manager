Private Marketplace Manager v0.3.0 starts serving immediately and moves the startup inventory scan to the background, so a large VSIX archive no longer leaves the container unhealthy after a restart.

- Linux manager: the listener opens before the extension directory is scanned. The scan that hashes every existing VSIX runs in the background and stops cleanly on shutdown.
- Limited API during the scan: inventory, lookup, upload, download, upload-receipt, and reconcile requests return `503` with code `starting` and `Retry-After` until it finishes. Status, login, audit events, sync reports, and health endpoints stay available.
- Health: `/health/ready` returns `{"status":"starting"}` during the scan and `{"status":"ready"}` after it, so the container healthcheck passes as soon as the manager serves requests. A failed scan now keeps the manager running with readiness `503` (container unhealthy) instead of exiting; fix the cause, then run `POST /api/v1/admin/reconcile` or restart.
- Progress: `GET /api/v1/status` reports the scan as `inventory` (`state`, `scanned`, `total`, timestamps, and any error). The webpage shows the same progress and opens the inventory when the scan completes.
- `marketplace-sync` waits for a manager that is still scanning instead of failing batches, and stops with an error if the scan failed. The `marketplace-mcp` `manager_analyze` tool reports a running or failed scan.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.3.0` for Linux x64/ARM64.

Upgrading from v0.2.0: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart. No state migration is required; the first start rescans the existing directory in the background. Automation that waits for full readiness should check for `"status":"ready"`, not only HTTP 200. The startup log line `manager ready` is now `manager listening`, followed by `inventory scan started` and `inventory scan finished`. Older `marketplace-sync` builds keep working but can report partial runs if they sync during a long scan, so update scheduled clients to the v0.3.0 archives.

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
