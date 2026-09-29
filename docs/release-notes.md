Private Marketplace Manager v0.8.0 adds version limits, so the library keeps only each extension's newest versions on disk, and renews the REST API as version 2.

**Upgrade order: the manager first, then every `marketplace-sync` and `marketplace-mcp`.** API v1 is retired. Older clients stop at startup with "unsupported manager API version 2" until updated, instead of downloading versions the manager would refuse; the new clients report that an older manager "does not serve API v2".

- Version limits: in the storage panel, keep the latest N versions of each extension (0 keeps every version, the default). Any extension can set its own limit or keep every version on its page. Versions are ordered by version number, stable and prerelease counted together, and each version includes all its platform packages.
- Preview, then delete: before a limit applies, a confirmation shows how many versions, package files, and bytes it removes across how many extensions. Older versions are deleted from disk and kept in the inventory as deleted, labelled as removed by the version limit.
- Kept within the limit: an upload that adds a newer version deletes the oldest beyond the limit; a scan that finds VSIX files added by hand trims their extensions; uploads of older versions, including restores, are refused with 409 `retention`. Raise a limit and sync again to bring versions back.
- API v2: every endpoint moves from `/api/v1` to `/api/v2` and `GET /status` reports `"apiVersion": 2` with the library `limit`. New endpoints `GET /limit`, `PUT /limit`, and `PUT /catalog/{id}/limit` read and set limits, with `dryRun` previews. `POST /extensions/check` returns each extension's limit in `limits`; catalog entries report `keep` and `keepSource`; deleted packages report `deletedBy` (`operator` or `limit`). `GET /api/v1/status` answers with version 2 so old clients stop cleanly; other v1 paths return 410 `api_version`. See "Changes from API v1" in the API guide.
- `marketplace-sync`: downloads only the newest versions within each extension's limit and reports the rest as `beyondLimit`; a limit lowered during a run is counted the same way rather than as a failure.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.8.0` for Linux x64/ARM64.

Upgrading from v0.7.0: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart; the state database gains its limits table automatically and no limit applies until you set one. Then install the v0.8.0 `marketplace-sync` and `marketplace-mcp` archives. Update scripts and integrations that call `/api/v1` to `/api/v2`.

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
