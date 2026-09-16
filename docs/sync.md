# Cross-platform synchronization

## Installation and configuration

Download the appropriate release archive and verify `SHA256SUMS`. Extract it, then create `extensions.txt` and `sync-settings.json`. No interpreter or compiler is required. Select `amd64` for x64 and `arm64` for ARM64; the executable's OS/CPU never limits the extension packages mirrored.

The list accepts UTF-8 with optional BOM, LF/CRLF, blank lines, and comment lines beginning with `#`. IDs are case-insensitively deduplicated. Invalid syntax fails before transfer. A snapshot is taken at run start; edits apply next time. Empty lists generate an explicit no-op report.

```json
{
  "serverUrl": "https://marketplace-manager.example.internal",
  "tokenFile": "api-token.txt",
  "workDir": "work",
  "concurrency": 2,
  "maxDownloadBytes": 2147483648,
  "retries": 4,
  "caFile": "corporate-ca.pem"
}
```

Omit `caFile` when normal OS certificate trust is sufficient. If supplied, it appends PEM certificates to the system trust pool. Relative settings paths resolve from the config file. Command-line `--list`, `--config`, and `--token-file` paths resolve from the current directory. Restrict token files to the execution account; Unix requires mode 600 or stricter. On Windows configure ACLs explicitly. Never place the token value in an argument.

HTTPS is required for the API. `allowInsecureHttp: true` only permits loopback HTTP for local development. The client refuses API redirects to protect credentials. Source assets are restricted to Microsoft's marketplace/gallery host families; unsupported new hosts cause an explicit error rather than an arbitrary fetch.

`HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` follow Go HTTP proxy behavior. Add the internal API host to NO_PROXY if it must bypass the proxy. Test under the actual scheduler account. Secrets are read from files, so an encrypted store may provision a temporary restricted file; machine-bound encrypted credentials should be reprovisioned on a replacement computer.

## Commands

```sh
marketplace-sync version
marketplace-sync discover --list extensions.txt --config sync-settings.json
marketplace-sync sync --list extensions.txt --config sync-settings.json
```

`discover` contacts the public marketplace only and writes a report including all discovered package records. It does not upload anything and needs no API credential. `sync` compares server inventory, downloads missing original VSIX files, validates identity/channel/hash, and uploads them. Version records are queued oldest-first, with bounded parallelism; completion order may vary. No latest-only, platform, engine-version, or stable-only filter is applied.

Reports are written to `work/report-<run-id>.json`; a compact result is also printed to stdout. Diagnostics go to stderr. Only completed reports are sent to the server. If a process is killed before the report is written, the next run still resumes from server inventory. A run lock prevents two processes using the same work directory; the receiver handles duplicate submissions across different machines.

Missing IDs, incomplete metadata, disappearing source versions, validation failures, and failed transfers produce a nonzero exit code while preserving independent successes. Files are retried with bounded backoff for transient errors. Partial individual downloads are discarded and safely retried. Source versions no longer listed by the marketplace cannot be discovered automatically. There is no claim that this recovers unavailable historical artifacts.

## Scheduling

Set timezone intentionally. The examples use local host time. Install only one scheduler per active sync client. Full backfills may take hours; avoid interrupting them at every recurring trigger. The local lock prevents overlap, and reruns recover stored work. The manager's upload API must be reachable while the computer is awake and connected.

### Windows Task Scheduler

Create a task under a dedicated execution account with network access. Action:

```text
Program: C:\MarketplaceSync\marketplace-sync.exe
Arguments: sync --list C:\MarketplaceSync\extensions.txt --config C:\MarketplaceSync\sync-settings.json
Start in: C:\MarketplaceSync
```

Use a daily trigger, run after a missed start, and “Do not start a new instance.” Set any time limit to accommodate the backfill. Configure the token file ACL and certificate/proxy settings for this account. Export the task XML for your operational backup.

### Linux systemd

`/etc/systemd/system/marketplace-sync.service`:

```ini
[Unit]
Description=Mirror all configured VS Code extension versions
Wants=network-online.target
After=network-online.target
[Service]
Type=oneshot
User=marketplace-sync
WorkingDirectory=/opt/marketplace-sync
ExecStart=/opt/marketplace-sync/marketplace-sync sync --list /opt/marketplace-sync/extensions.txt --config /opt/marketplace-sync/sync-settings.json
TimeoutStartSec=infinity
```

`/etc/systemd/system/marketplace-sync.timer`:

```ini
[Unit]
Description=Daily extension mirror
[Timer]
OnCalendar=*-*-* 02:17:00
Persistent=true
RandomizedDelaySec=300
[Install]
WantedBy=timers.target
```

After operator review: `systemctl daemon-reload` and `systemctl enable --now marketplace-sync.timer`. Add needed proxy environment through an operator-owned service drop-in. Alternatively use cron with absolute paths and log redirection; ordinary cron does not replay missed runs.

### macOS launchd

Save a plist for the appropriate account, replacing paths:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>internal.marketplace-sync</string>
<key>ProgramArguments</key><array>
<string>/opt/marketplace-sync/marketplace-sync</string><string>sync</string>
<string>--list</string><string>/opt/marketplace-sync/extensions.txt</string>
<string>--config</string><string>/opt/marketplace-sync/sync-settings.json</string>
</array>
<key>StartCalendarInterval</key><dict><key>Hour</key><integer>2</integer><key>Minute</key><integer>17</integer></dict>
<key>StandardOutPath</key><string>/opt/marketplace-sync/output.log</string>
<key>StandardErrorPath</key><string>/opt/marketplace-sync/error.log</string>
</dict></plist>
```

Use a LaunchAgent for a logged-in user or an operator-installed LaunchDaemon for unattended operation. Ensure the account can access the work/credential files. Confirm sleep and network behavior on the actual host.

## Replacing the computer

1. Stop/disable the old schedule when possible.
2. Install the release for the new OS/CPU. Copy the active list and non-secret settings; adjust paths.
3. Provision a new token and CA/proxy settings. Retire the old token through receiver secret rotation.
4. Run discovery and sync. The server inventory skips confirmed packages; copying the local cache is optional.
5. Install the new schedule. Do not derive the active list from the archive: it also contains previously removed IDs and manual uploads.

Keep the list/settings backed up separately. Adding an identifier starts its complete backfill, removing one only stops future downloads, and re-adding one resumes missing history. Dependencies/extension-pack members are reported in metadata but are not silently added to your list.
