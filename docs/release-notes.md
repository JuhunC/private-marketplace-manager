Private Marketplace Manager v0.6.0 shows local disk space in the webpage, so administrators can see how much room is left for the extension library before syncs and uploads fail.

- Storage panel: below the summary cards, the Extension inventory page shows free space and total size of the disk holding the extension folder, the percentage used, and a bar splitting it into the VSIX library, other files, and free space. When the state database lives on a different disk, its free space is shown too.
- Low-space warning: the panel turns amber when less than a tenth of the extension disk is free or less than twice the upload limit remains, because each upload briefly stages a second copy of the VSIX. The state disk is flagged below a tenth or 1 GiB free.
- API: `GET /api/v1/status` adds `storage` with `total`, `used`, `free`, and `low` for the extension folder disk and, when separate, the state disk. `used` excludes blocks the filesystem reserves, as `df` does.
- `marketplace-mcp`: `manager_health` includes disk space, and `manager_analyze` reports a `low_disk_space` warning.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.6.0` for Linux x64/ARM64.

Upgrading from v0.5.0: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart. No migration is required, and clients from v0.3.0 onward keep working; update `marketplace-mcp` for the disk-space finding. Upgrading from v0.4.0 or earlier also brings the v0.5.0 change-based startup verification; see the v0.5.0 notes. Keep external disk alerting in place; the webpage shows space only while someone is looking.

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
