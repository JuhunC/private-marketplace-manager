Private Marketplace Manager v0.9.0 keeps the inventory page fast with millions of packages and fixes the issues found in a review of the webpage and REST API.

- Fast at scale: the manager keeps per-extension totals up to date as packages change, so the inventory page and `GET /api/v2/status` no longer scan every package. With 1 million packages on 2 CPUs, status dropped from 2.8 s to 13 ms and catalog pages, sorts, and searches from 2–4 s to under 40 ms. Reads and the healthcheck use their own read-only database connections, so they no longer wait behind uploads, scans, or deletions. The first start after upgrading builds the totals once in the background (about 3 s per million packages).
- Uploads report deletions: when a new version pushes older ones past the version limit, the upload response lists them in `removedVersions`, and the webpage and `marketplace-mcp` say so.
- Rescan from the webpage: the storage panel adds **Rescan folder** and **Verify every file** (the same as `POST /api/v2/admin/reconcile` and `?verify=full`), so a failed startup scan can be recovered without the API.
- Per-extension limits on the webpage: a list of extensions with their own limit, with a form to add one for any ID, including extensions sync has not collected yet.
- Readable errors: the webpage shows "HTTP 502 Bad Gateway"-style messages instead of JSON parser errors when a proxy answers, and unknown API paths and unsupported methods return JSON 404/405 errors (with `Allow`).
- Activity log: readable entries with local times, filters by kind of event and actor, and paging. `GET /api/v2/audit-events` accepts `action`, `actor`, `before`, and `pageSize`.
- `pageSize`: the page-size query parameter of `GET /catalog`, `GET /extensions`, and `GET /audit-events` is now `pageSize`; `limit` still works as a deprecated alias, and responses include both.
- Login throttling behind a proxy: set `TRUSTED_PROXIES` to the reverse proxy's address so throttling counts each browser from `X-Forwarded-For` instead of treating all logins as the proxy. Invalid entries stop startup with a clear error.
- Webpage fixes: the search box keeps a usable width on phones, typed-but-unapplied limits survive page refreshes, and the "Library default" label shows the saved value.
- Healthcheck: readiness also checks that the state directory accepts writes.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.9.0` for Linux x64/ARM64.

Upgrading from v0.8.x: set `MANAGER_IMAGE` to the new tag (or use the updated Compose file) and restart; the database adds its totals table automatically. The API stays at version 2, so v0.8.x `marketplace-sync` and `marketplace-mcp` keep working; update `marketplace-mcp` to see removed versions after uploads. If the manager runs behind nginx on the host, set `TRUSTED_PROXIES` to the Docker network gateway (see the deployment guide).

Download the archive matching the tool, OS, and CPU, then verify `SHA256SUMS`. No Go, Python, or PowerShell installation is required. See the MCP guide before granting an MCP host access. Repair tools are additive but use the same API token as synchronization, so retain human approval in the MCP host.

The release supports one operator password and one API token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on the existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on actual hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.
