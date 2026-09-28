Private Marketplace Manager v0.7.0 lets administrators delete old versions, or a whole extension, from disk to free space, while the inventory keeps those versions listed as deleted.

- Bulk deletion: on an extension's page, delete from its earliest version through a chosen version (semantic order, inclusive), or delete the entire extension. A confirmation first shows the versions, package files, and size that will be removed.
- API: `POST /api/v1/catalog/{id}/delete` with `{"through":"1.5.1"}` or `{"all":true}`; add `"dryRun":true` for a preview. The response lists the versions, package count, freed bytes, and any files that could not be removed yet. Each deletion is recorded in the audit log as `deleted_versions`.
- Index kept: deleted versions stay in the inventory with status `deleted` and a `deletedAt` time. The webpage shows them struck through without a download link; the catalog and `GET /api/v1/status` report them separately (`deleted`, `deletedBytes`) and no longer count them as versions, packages, platforms, or needing attention.
- Not collected again: `POST /api/v1/extensions/check` returns deleted keys in `deleted`, and `marketplace-sync` skips them without downloading, counting them in its report's `deleted` field. Uploading a deleted version returns 409 `deleted` unless the request adds `restore=true`; uploads from the webpage and `marketplace-mcp` restore on purpose.
- Safe interruption: the deletion is recorded before files are removed. If the manager stops midway, the next scan removes the remaining files holding exactly the deleted bytes; other files are never touched.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.7.0` for Linux x64/ARM64.

Upgrading from v0.6.0: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart; no migration is required. Update every scheduled `marketplace-sync` to v0.7.0 as well: older clients do not know about deleted versions, so they would download them again, have the upload refused, and report partial runs. The API token can now delete versions, so protect it as a privileged credential.

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
