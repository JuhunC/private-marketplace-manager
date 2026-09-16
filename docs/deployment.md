# Linux deployment and operations

## 1. Prepare host directories

Keep the existing marketplace container and its extension mount. Record its image digest, directory, UID/GID, and upstreaming configuration. Back up the VSIX folder before adding a writer.

Create a **separate local** state directory. SQLite must not live on an arbitrary SMB/NFS share. The extension filesystem must support atomic hard links within the directory and directory fsync (verify your mounted share in a pilot).

Grant the manager container account write access to both directories using your existing group/ACL policy. Avoid blindly changing ownership of a shared extension tree. The default is UID/GID `10001:10001`, configurable in Compose. The marketplace only needs read access to extensions.

## 2. Configure secrets and Compose

Copy `deploy/compose.yaml` and `.env.example` to an operator-managed deployment directory. Rename `.env.example` to `.env` and replace all placeholders. Create `secrets/api-token.txt` and `secrets/admin-password.txt` using independent random values. Use at least 32 characters for the token and 12 for the password. Restrict secret files while ensuring the container's UID can read them; local Compose bind-mounted secret ownership is governed by host permissions.

Do not commit `.env` or secrets. `PUBLIC_URL` must be the browser's exact HTTPS origin, including a non-default port if used. It must not contain a path.

```sh
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose logs --tail 100 manager
```

The example exposes only `127.0.0.1:8080` for a TLS reverse proxy on the same host. For a containerized proxy, attach the manager to its private Docker network instead. Use your approved internal certificate. Configure proxy upload size/timeouts to accommodate the maximum VSIX (default 2 GiB and up to 30 minutes). Avoid exposing the raw HTTP port outside trusted local/private proxy connections.

## 3. Configuration reference

| Variable | Default | Purpose |
|---|---|---|
| `PUBLIC_URL` | `http://localhost:8080` | Exact UI origin; production uses HTTPS |
| `LISTEN_ADDR` | `:8080` | Listen address; built-in container healthcheck assumes port 8080 |
| `EXTENSIONS_DIR` | `/data/extensions` | Existing VSIX directory |
| `STATE_DIR` | `/data/state` | Persistent local SQLite state and instance lock |
| `API_TOKEN_FILE` | required in Compose | Token file, 32+ characters |
| `ADMIN_PASSWORD_FILE` | required in Compose | Operator password file, 12+ characters |
| `MAX_UPLOAD_BYTES` | `2147483648` | Maximum compressed VSIX bytes |

`API_TOKEN` and `ADMIN_PASSWORD` environment alternatives exist for local development; file secrets take precedence. Set only a single manager replica. Archive validation limits are 100,000 entries, 4 GiB total decompressed bytes, 2 MiB per manifest, and bounded compression ratios. Unsupported packages fail explicitly.

The Docker daemon's registry proxy is configured separately from the client computer's marketplace/API proxy. Test complete image pulls through the proxy; registry blob hosts/redirects may need approval. Public GHCR images can be pulled anonymously after package visibility is public.

## 4. Pilot

Upload a known-good VSIX through the webpage. Confirm the stored hash, final file, and status. Then sync one identifier with old/new and multiple platform records and check the actual Microsoft marketplace. The file scanner refresh delay, historical-version selection, signature behavior, extension dependencies, and runtime internet requirements must be verified in your environment.

The receiver does not restart the existing marketplace. Its dashboard deliberately reports visibility as unverified. Confirm it manually through the marketplace and VS Code.

## 5. Operations and recovery

- `/health/live` checks process liveness. `/health/ready` checks database and writable storage. The container uses readiness as its healthcheck.
- Sync reports record success, no changes, empty lists, and partial failures. Logs go to stdout/stderr; audit events and reports live in SQLite. Monitor filesystem free space externally; no automatic pruning occurs.
- Inbound uploads are bounded to four active requests. A fifth receives 429 with `Retry-After`. Do not set aggressive client concurrency.
- Startup scans and hashes all VSIXs, so large historical archives increase startup time. Adjust startup probe grace periods for the measured inventory.
- Startup repairs published-but-pending records and marks absent files missing. Invalid/conflicting existing packages appear in startup logs/audit. Existing directory files are never deleted except abandoned `.upload-*.part` staging files from an interrupted manager.
- Avoid external writers while running. Restart the manager after manual directory changes to reconcile inventory. An out-of-band same-size edit can evade the lightweight batch inventory check until reconciliation; startup rehashes the files.
- If storage fills up, free space without deleting required history, then rerun synchronization. Existing confirmed packages are retained.
- Back up the extension folder and state directory while the manager is stopped. Restore together, then start to reconcile. Keep settings and secrets in your organization's approved backup/secret system.
- Rotate token/password by replacing secret files and restarting; sessions end. Use a new token when retiring a client machine.
- To withdraw a bad version in v0.1.0, stop scheduled collection for its identifier and coordinate removal/quarantine with the Linux operator. A subsequent all-version collection would otherwise upload the version again. Per-version suppression is a roadmap feature.
- Application rollback: stop the manager and restore a compatible image/state backup. No application update should remove historical VSIXs.

The default release is a single-operator service. Treat the API token as an upload/inventory/report credential, not a public browser credential. There are no file-delete, shell-execution, or arbitrary-URL-fetch endpoints.
