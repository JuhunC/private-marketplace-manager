Private Marketplace Manager v0.4.0 redesigns the Extension inventory page around extensions instead of individual package files, so administrators can see at a glance how many extensions and versions they hold and which ones need attention.

- Summary cards: stored extensions, distinct versions, package files, stored size, and records that need attention (missing, pending, or conflicting). The attention card turns amber and filters the list when clicked.
- Extension list: one row per extension with its latest version, version count, platforms, package files, stored size, and status. Search by part of a name or ID, sort by name, most versions, largest, or recently updated, and show only extensions that need attention.
- Extension view: every version newest first by semantic version (1.10.0 above 1.9.0), marked latest or prerelease, with a chip per platform package. Missing files are highlighted; a stored package's details include a download link.
- API: new `GET /api/v1/catalog` (search, sort, attention filter, pagination) and `GET /api/v1/catalog/{id}` (versions with their platform packages). `GET /api/v1/status` adds `extensions`, `versions`, and `attention` totals and no longer loads every package record to compute them. Existing endpoints are unchanged; catalog requests return 503 `starting` during the startup inventory scan like other inventory requests.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.4.0` for Linux x64/ARM64.

Upgrading from v0.3.0: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart. No state migration is required. The `marketplace-sync` and `marketplace-mcp` downloads are rebuilt for this version without behavior changes, so v0.3.0 clients keep working.

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
