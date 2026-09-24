Private Marketplace Manager v0.2.0 adds a native administrator MCP server and authenticated storage reconciliation.

- `marketplace-mcp`: eight tools for health, bounded issue analysis, inventory checks, audit/sync history, safe storage reconciliation, and validated VSIX restoration.
- MCP protocol: official Go SDK with current protocol support and backwards negotiation; stdio transport keeps the API credential local to the administrator workstation.
- Linux manager: new authenticated `POST /api/v1/admin/reconcile` endpoint, serialized against uploads and audited. It never deletes VSIX files or resolves content conflicts.
- Native downloads: separate `marketplace-sync` and `marketplace-mcp` archives for Windows, Linux, and macOS on x64 and ARM64.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.2.0` for Linux x64/ARM64.

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
