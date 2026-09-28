Private Marketplace Manager v0.5.0 makes the startup "Verifying the extension folder" scan fast enough for libraries of millions of VSIX files by rehashing only files that are new or changed.

- Change-based verification: the state database remembers each VSIX's size, modification time, and identity. A restart lists and stats every file but opens and hashes only new or changed ones, so scan time follows the number of files and the storage's metadata speed rather than the archive's total size.
- Measured with 2 CPUs (matching the Compose default) on macOS APFS, whose per-file stat cost is higher than Linux ext4/xfs: a restart verifies 1 million unchanged files in about 13 seconds and 3 million in about 50 seconds, with about 12 MiB of heap. Deleted files are marked missing in the same pass. Database work is split into short batches, so health checks and other requests wait at most a few hundred milliseconds during a scan.
- Upgrade: the first start after upgrading trusts files that v0.4.0 or earlier already verified when their size is unchanged, instead of rehashing the archive. In the same benchmark this one-time pass took about 34 seconds per million files.
- Uploads record their file as they publish it, so newly uploaded packages are not rehashed at the next restart.
- Full verification on demand: `POST /api/v1/admin/reconcile?verify=full`, or `manager_reconcile_storage` with `fullVerify` in `marketplace-mcp`, rehashes every file. Use it after restoring a backup or when files may have been edited outside the manager without changing their size and modification time, which routine scans do not detect.
- Admin reconciliation now reports its changes from the scan itself instead of loading every record twice, and responds with `fullVerify`. Invalid or conflicting files are logged and audited when first found or changed rather than on every restart.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.5.0` for Linux x64/ARM64.

Upgrading from v0.4.0: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart. The state database gains its file table automatically; no manual migration is required. Clients from v0.3.0 onward keep working; update `marketplace-mcp` to use `fullVerify`.

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
