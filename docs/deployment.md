# Linux deployment and operations

## 1. Prepare host directories

Keep the existing marketplace container and its extension mount. Record its image digest, directory, UID/GID, and upstreaming configuration. Back up the VSIX folder before adding a writer.

Create a **separate local** state directory. SQLite must not live on an arbitrary SMB/NFS share. The extension filesystem must support atomic hard links within the directory and directory fsync (verify your mounted share in a pilot).

Grant the manager container account write access to both directories using your existing group/ACL policy. Avoid blindly changing ownership of a shared extension tree. The default is UID/GID `10001:10001`, configurable in Compose. The marketplace only needs read access to extensions.

## 2. Configure secrets and Compose

Copy the entire `deploy/` directory to an operator-managed location such as
`/opt/private-marketplace-manager`. The included `.env.example` contains a
concrete Linux example with every Compose input. Generate secrets and the state
directory before starting:

```sh
cd /opt/private-marketplace-manager
cp .env.example .env
chmod 600 .env

install -d -m 0700 secrets
umask 077
openssl rand -hex 32 > secrets/api-token.txt
openssl rand -base64 24 > secrets/admin-password.txt

sudo install -d -o 10001 -g 10001 -m 0750 /var/lib/private-marketplace-manager
```

Edit `.env` and replace at least `PUBLIC_URL`, `EXTENSIONS_HOST_DIR`, and
`STATE_HOST_DIR`. Change `MANAGER_UID`/`MANAGER_GID` if `10001:10001` does not
have read/write access to both host directories. The secret values must remain
independent: the API token is used by `marketplace-sync`; the password is used
only for browser login. Use at least 32 characters for the token and 12 for the
password. Local Compose bind-mounted secret ownership is governed by host
permissions.

Do not commit `.env` or secrets. `PUBLIC_URL` must be the browser's exact HTTPS origin, including a non-default port if used. It must not contain a path.

```sh
docker compose --env-file .env config
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
docker compose --env-file .env logs --tail 100 manager
curl --fail http://127.0.0.1:8080/health/ready
```

Inspect the rendered `docker compose ... config` output before starting. This
catches unresolved variables and confirms the exact paths, port, image, user,
limits, and secret files Docker will use. The bind mounts use
`create_host_path: false`, so a misspelled host path fails instead of silently
creating an empty directory.

The example exposes only `127.0.0.1:8080` for a TLS reverse proxy on the same host. A complete Nginx server-block example is provided at `deploy/nginx/private-marketplace-manager.conf.example`; change its server name and internal-PKI certificate paths. For a containerized proxy, attach the manager to its private Docker network instead. Use your approved internal certificate. Configure proxy upload size/timeouts to accommodate the maximum VSIX (default 2 GiB and up to 30 minutes). Avoid exposing the raw HTTP port outside trusted local/private proxy connections.

## 3. Configuration reference

The `.env` file controls these Compose settings:

| Variable | Example | Purpose |
|---|---|---|
| `MANAGER_IMAGE` | `ghcr.io/juhunc/private-marketplace-manager:0.2.0` | Pinned manager image |
| `PUBLIC_URL` | `https://marketplace-manager.corp.example.com` | Exact browser-facing origin |
| `MANAGER_BIND_ADDRESS` / `MANAGER_HOST_PORT` | `127.0.0.1` / `8080` | Host listener used by the TLS proxy |
| `EXTENSIONS_HOST_DIR` | `/srv/vsmarketplace/extensions` | Existing Microsoft marketplace VSIX directory |
| `STATE_HOST_DIR` | `/var/lib/private-marketplace-manager` | Separate local SQLite state directory |
| `MANAGER_UID` / `MANAGER_GID` | `10001` / `10001` | Host identity with write access to both directories |
| `API_TOKEN_SECRET_FILE` | `./secrets/api-token.txt` | Generated sync-client token file |
| `ADMIN_PASSWORD_SECRET_FILE` | `./secrets/admin-password.txt` | Generated browser password file |
| `MAX_UPLOAD_BYTES` | `2147483648` | Maximum compressed VSIX bytes |
| `CPU_LIMIT` / `MEMORY_LIMIT` / `PIDS_LIMIT` | `2` / `1g` / `128` | Container resource limits |
| `LOG_MAX_SIZE` / `LOG_MAX_FILES` | `10m` / `5` | Docker JSON log rotation |

Other values in `.env.example` control the Compose project/container names and
restart policy. Relative secret paths resolve from the directory containing
`compose.yaml`.

The manager receives these application settings from Compose:

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
- To withdraw a bad version in v0.2.0, stop scheduled collection for its identifier and coordinate removal/quarantine with the Linux operator. A subsequent all-version collection would otherwise upload the version again. Per-version suppression is a roadmap feature.
- Application rollback: stop the manager and restore a compatible image/state backup. No application update should remove historical VSIXs.

The default release is a single-operator service. Treat the API token as an upload/inventory/report credential, not a public browser credential. There are no file-delete, shell-execution, or arbitrary-URL-fetch endpoints.
