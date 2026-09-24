# Administrator MCP server

`marketplace-mcp` is a native stdio MCP server for administrators and support
agents. It connects to the manager REST API over HTTPS, so it can run on
Windows, Linux, or macOS without direct access to the Linux filesystem, Docker
socket, or SQLite database.

## Install and configure

Download the `marketplace-mcp` archive for your OS and CPU from the GitHub
release, then verify it against `SHA256SUMS`. Copy
`mcp-settings.example.json` to `mcp-settings.json`:

```json
{
  "serverUrl": "https://marketplace-manager.example.internal",
  "tokenFile": "api-token.txt",
  "maxUploadBytes": 2147483648
}
```

`tokenFile` and optional `caFile` paths resolve relative to this settings file.
On Unix, the token file must have mode `0600`; on Windows, restrict its ACL to
the administrator account. Corporate `HTTPS_PROXY`/`NO_PROXY` environment
variables are honored. Set `caFile` to a PEM bundle when your internal CA is
not in the OS trust store. HTTP is rejected except loopback development with
`allowInsecureHttp: true`.

Add the executable to an MCP client using its stdio configuration format. The
portable configuration shape is:

```json
{
  "mcpServers": {
    "private-marketplace": {
      "command": "/absolute/path/marketplace-mcp",
      "args": [
        "--config",
        "/absolute/path/mcp-settings.json"
      ]
    }
  }
}
```

On Windows, use the absolute path to `marketplace-mcp.exe` and escape backslashes
in JSON. Never place the API token in MCP arguments or environment variables.

## Tools

| Tool | Effect |
|---|---|
| `manager_health` | Read liveness, readiness, API version, inventory size, and upload limit |
| `manager_analyze` | Review readiness, up to 5,000 inventory records, and recent sync failures; return findings and repair guidance |
| `manager_inventory` | Read paginated package metadata, optionally for one extension ID |
| `manager_check_packages` | Check selected records against current regular-file presence and size |
| `manager_audit_events` | Read the latest 100 audit events |
| `manager_sync_runs` | Read the latest 100 synchronization reports |
| `manager_reconcile_storage` | Rescan storage, recover pending records, inventory valid files, and mark absent records missing |
| `manager_upload_vsix` | Validate and idempotently upload one reviewed local VSIX |

The first six tools are read-only. Reconciliation changes inventory metadata but
does not delete VSIX files or resolve different-byte conflicts. Upload is
additive and retains the manager's validation, atomic publication, idempotency,
and no-overwrite guarantees. MCP clients should show the administrator the
arguments and request confirmation before either repair tool runs.

The MCP process can read only paths allowed to its OS account. The upload tool
rejects directories, symlinks, non-VSIX data, unsafe archives, and files beyond
the configured size. Tool output never contains the API token.

## Recommended incident workflow

1. Run `manager_analyze` and `manager_health`.
2. Inspect relevant package records, audit events, and sync runs.
3. Run `manager_reconcile_storage` for stale or externally changed inventory.
4. If a package is still missing, obtain and review the original VSIX, then run
   `manager_upload_vsix` with its local path.
5. For a conflict, compare files and hashes manually. The MCP server will not
   delete, quarantine, or select a winner.

The manager API token currently grants inventory, report, upload, and reconcile
access. v0.2.0 does not provide per-tool roles. Run the MCP server only for
trusted administrators, keep human approval enabled in the MCP host, and rotate
the API token when retiring a workstation.
