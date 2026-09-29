Private Marketplace Manager v0.8.1 lets deleted versions be uploaded again, so the version limit alone decides what the library keeps.

- Uploads: a deleted version can be uploaded again through the REST API, the webpage, or `marketplace-mcp` while it is within the extension's version limit. It is stored again and its `deletedAt`/`deletedBy` are cleared. The `restore=true` flag is no longer needed (and is ignored); the 409 `deleted` refusal is gone. Versions older than the limit are still refused with 409 `retention`.
- `marketplace-sync`: deleted versions within the limit are collected again at the next run. This also makes raising a limit and syncing again bring back versions the limit removed, as the v0.8.0 documentation described; in v0.8.0 they stayed skipped.
- Deleting now frees space until the next sync for versions within the limit. To keep versions away, lower the limit or remove the extension from the sync list. The webpage's delete confirmation says so.
- API: still version 2. `POST /api/v2/extensions/check` keeps reporting deleted keys in `deleted`, for information. The sync report no longer has a `deleted` count.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.8.1` for Linux x64/ARM64.

Upgrading from v0.8.0: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart; no migration is required. Install the v0.8.1 `marketplace-sync` so deleted versions are collected again; v0.8.0 clients keep working but still skip them. Upgrading from v0.7.0 or earlier: follow the v0.8.0 notes (upgrade the manager first, then every client, because API v1 is retired).

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
