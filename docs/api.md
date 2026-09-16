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

`201` means newly stored; `200` means the identical package already exists. Both are synchronous durable acknowledgements. No `202`/background-upload polling is required in v0.1.0. If the connection fails after publication, retry the same bytes/key. The manager verifies the existing file and returns its receipt. An idempotency key cannot be reused for another identity/hash.

## Endpoints

| Method and path | Purpose |
|---|---|
| `GET /status` | API version, package count, total bytes, upload limit |
| `GET /extensions?limit=100&offset=0&id=publisher.extension` | Inventory, exact ID filter, max page size 500 |
| `POST /extensions/check` | Lookup up to 1,000 package keys; only stored regular files of the expected size are returned |
| `POST /extensions` | Raw-byte VSIX upload |
| `GET /packages/download?key=publisher.extension@1.2.3@linux-arm64` | Download original package bytes |
| `GET /uploads/by-key/{key}` | Stored receipt for a successful idempotency key |
| `GET /audit-events` | Latest 100 audit events |
| `POST /sync-runs` | Save/update a JSON report with a run `id`; max 4 MiB |
| `GET /sync-runs` | Latest 100 client-reported runs |
| `POST /login` | Browser operator password login; matching Origin required |
| `GET /me` | Session CSRF token and server version |
| `POST /logout` | End browser session; matching Origin and CSRF header required |

Batch lookup example:

```json
{"keys":["publisher.extension@1.2.3@universal","publisher.extension@1.2.3@linux-arm64"]}
```

Response: `{"packages":{"<key>":{...package metadata...}}}`. Absent, missing, or conflicting packages are not returned. Lightweight lookup checks presence/type/size, not the entire file hash. Restart reconciliation rehashes existing files. Avoid out-of-band changes.

Health endpoints `/health/live` and `/health/ready` are outside `/api/v1` and require no credentials. No endpoint accepts an arbitrary destination path, downloads arbitrary remote URLs, executes commands, or deletes VSIX files.

## Errors and retry policy

Errors contain `code`, `error`, and `requestId`:

| HTTP | Meaning | Action |
|---|---|---|
| 400 | Invalid request/identifier/key | Fix input |
| 401/403 | Authentication or browser origin/CSRF failure | Fix credential/origin; do not retry blindly |
| 404 | Package/receipt unavailable | Reconcile or retry the original upload |
| 409 | Different bytes at same identity, reused key, or publication conflict | Investigate; never overwrite automatically |
| 413 | Upload/body exceeds limit | Review size limits |
| 422 | Invalid ZIP/manifests, identity, or checksum | Inspect original package |
| 429 | Upload/login concurrency limit | Honor Retry-After |
| 500/503 | State/service failure | Retry with bounded backoff |
| 507 | Staging or durability operation failed | Check disk, filesystem support, and permissions |

A non-success response can occur after final file publication (for example, a database failure). Retrying identical content is safe. Per-file publication is atomic; an entire historical collection is not a single transaction. Independent successes remain available after another package fails.

Sync reports are client-reported collection summaries, not independent attestations of source completeness. SHA-256 does not verify publisher authenticity. No automated marketplace visibility status is claimed.
