# REST API v1

Base URL: `https://<internal-manager>/api/v1`. The [OpenAPI specification](../internal/server/web/openapi.json) is also served at `/openapi.json`. Tokens are supplied via the `Authorization: Bearer ...` header. Browser operator sessions use an HTTP-only cookie and CSRF token; API clients should use bearer authentication.

## Upload one VSIX

`POST /extensions` accepts **raw original VSIX bytes**, content type `application/octet-stream` (not multipart). Optional query fields: `id`, `version`, `platform`. Optional headers: `X-Content-SHA256`, `Idempotency-Key` (max 256 characters), `X-Package-Source` (provenance only; never fetched by the server).

For curl, place the authorization header in an access-restricted curl configuration file rather than on the command line:

```text
# protected-auth.curl — restrict access, do not commit
header = "Authorization: Bearer <provisioned token>"
```

```sh
curl --fail-with-body --config protected-auth.curl \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @extension.vsix \
  'https://manager.example.internal/api/v1/extensions'
```

Successful response:

```json
{
  "duplicate": false,
  "package": {
    "id": "publisher.extension",
    "version": "1.2.3",
    "platform": "linux-arm64",
    "prerelease": false,
    "sha256": "<server-computed SHA-256>",
    "size": 12345,
    "filename": "publisher.extension-1.2.3-linux-arm64.vsix",
    "managed": true,
    "status": "stored",
    "storedAt": "2026-09-16T00:00:00Z"
  }
}
```

`201` means newly stored; `200` means the identical package already exists. Both are synchronous durable acknowledgements. No `202`/background-upload polling is required. If the connection fails after publication, retry the same bytes/key. The manager verifies the existing file and returns its receipt. An idempotency key cannot be reused for another identity/hash.

## Endpoints

| Method and path | Purpose |
|---|---|
| `GET /status` | API version; stored `extensions`, `versions`, `packages`, and `bytes`; `attention` (records not stored); upload limit; startup scan progress (`inventory`); disk space (`storage`) |
| `GET /catalog?q=python&sort=versions&attention=true&limit=100&offset=0` | One entry per extension: latest version, version/platform/package counts, stored bytes, last update, and status counts. `q` matches part of an ID or display name; `sort` is `name`, `versions`, `size`, or `updated`; `attention=true` keeps extensions with missing, pending, or conflicting records |
| `POST /catalog/{id}/delete` | Delete versions from disk: `{"through":"1.5.1"}` removes the earliest version through 1.5.1 (semantic order, inclusive) and `{"all":true}` every version. Add `"dryRun":true` for a preview. The response lists `versions`, `packages`, freed `bytes`, and `filesRemaining` |
| `GET /catalog/{id}` | One extension's summary plus its versions, newest first by semantic version, each with its platform packages |
| `GET /extensions?limit=100&offset=0&id=publisher.extension` | Inventory, exact ID filter, max page size 500 |
| `POST /extensions/check` | Lookup up to 1,000 package keys; only stored regular files of the expected size are returned |
| `POST /extensions` | Raw-byte VSIX upload |
| `GET /packages/download?key=publisher.extension@1.2.3@linux-arm64` | Download original package bytes |
| `GET /uploads/by-key/{key}` | Stored receipt for a successful idempotency key |
| `GET /audit-events` | Latest 100 audit events |
| `POST /sync-runs` | Save/update a JSON report with a run `id`; max 4 MiB |
| `GET /sync-runs` | Latest 100 client-reported runs |
| `POST /admin/reconcile?verify=full` | Rescan storage, recover pending records, inventory valid external files, and mark absent records missing. Without `verify=full`, files whose size and modification time are unchanged are trusted |
| `POST /login` | Browser operator password login; matching Origin required |
| `GET /me` | Session CSRF token and server version |
| `POST /logout` | End browser session; matching Origin and CSRF header required |

Batch lookup example:

```json
{"keys":["publisher.extension@1.2.3@universal","publisher.extension@1.2.3@linux-arm64"]}
```

Response: `{"packages":{"<key>":{...package metadata...}}}`. Absent, missing, or conflicting packages are not returned. Lightweight lookup checks presence/type/size, not the entire file hash. Restart reconciliation rehashes new or changed files; `POST /admin/reconcile?verify=full` rehashes every file. Avoid out-of-band changes.

Health endpoints `/health/live` and `/health/ready` are outside `/api/v1` and require no credentials. No endpoint accepts an arbitrary destination path, downloads arbitrary remote URLs, or executes commands. Only `POST /catalog/{id}/delete` removes VSIX files.

### Deleted versions

Deleting versions removes their files from the extension folder but keeps their records with status `deleted` and a `deletedAt` time, so the webpage and API still list them. They are not counted as versions, packages, platforms, or needing attention; `GET /status` reports them as `deleted` and `deletedBytes`. `POST /extensions/check` returns requested keys that were deleted in `deleted`, and marketplace-sync skips them without downloading. Uploading a deleted version returns 409 `deleted` unless the request adds `restore=true`, which the webpage and `marketplace-mcp` upload do to bring a version back. If the manager stops before a deletion's files are gone, the next scan removes files holding exactly the deleted bytes. The action is recorded in the audit log as `deleted_versions`.

`POST /admin/reconcile` uses the same bearer credential and serializes against active uploads. It never chooses between conflicting bytes, and the only VSIX files it deletes are leftovers of versions an operator already deleted. It may remove abandoned `.upload-*.part` staging files, updates inventory statuses, and records an audit event. The response includes the number of changed records, counts by status, and `fullVerify`. Routine scans (startup and reconcile without `verify=full`) remember each file's size and modification time and rehash only new or changed files; `verify=full` rehashes every file, which takes time proportional to the archive's size.

`GET /status` also reports disk space as `storage`: `extensions` describes the filesystem holding the extension folder, and `state` the state database's filesystem when it is a different one. Each has `total`, `used`, and `free` bytes (`free` is what the manager can use; `used` excludes blocks the filesystem reserves, as `df` does) and `low`. The extension disk is `low` when under a tenth is free or less than twice the upload limit remains, because an upload stages a full copy before publishing; the state disk is `low` under a tenth or 1 GiB free.

### Startup inventory scan

After every start the manager scans and hashes the VSIX directory in the background, so it answers requests at once. `GET /status` reports the scan as `inventory`:

```json
{"state":"scanning","scanned":1200,"total":5000,"startedAt":"2026-09-28T06:00:00Z"}
```

`state` becomes `ready` when the scan finishes, or `failed` with an `error`. Until it is `ready`, `GET /catalog`, `GET /catalog/{id}`, `GET /extensions`, `POST /extensions`, `POST /extensions/check`, `GET /packages/download`, and `GET /uploads/by-key/{key}` answer 503: code `starting` with `Retry-After` during the scan, `scan_failed` after a failure. `POST /admin/reconcile` answers 503 `starting` during the scan; after a failure it retries the scan, and success lifts the restriction. Status, login, audit events, and sync reports stay available. `/health/ready` returns 200 `{"status":"starting"}` during the scan, `{"status":"ready"}` after it, and 503 if it failed.

## Errors and retry policy

Errors contain `code`, `error`, and `requestId`:

| HTTP | Meaning | Action |
|---|---|---|
| 400 | Invalid request/identifier/key | Fix input |
| 401/403 | Authentication or browser origin/CSRF failure | Fix credential/origin; do not retry blindly |
| 404 | Package/receipt unavailable | Reconcile or retry the original upload |
| 409 | Different bytes at same identity, reused key, publication conflict, or `deleted` version | Investigate; never overwrite automatically. Add `restore=true` to bring back a deleted version |
| 413 | Upload/body exceeds limit | Review size limits |
| 422 | Invalid ZIP/manifests, identity, or checksum | Inspect original package |
| 429 | Upload/login concurrency limit | Honor Retry-After |
| 500/503 | State/service failure | Retry with bounded backoff |
| 503 `starting` / `scan_failed` | Startup inventory scan still running, or failed | Honor Retry-After and watch `GET /status`; after a failure, check logs, then reconcile |
| 507 | Staging or durability operation failed | Check disk, filesystem support, and permissions |

A non-success response can occur after final file publication (for example, a database failure). Retrying identical content is safe. Per-file publication is atomic; an entire historical collection is not a single transaction. Independent successes remain available after another package fails.

Sync reports are client-reported collection summaries, not independent attestations of source completeness. SHA-256 does not verify publisher authenticity. No automated marketplace visibility status is claimed.
