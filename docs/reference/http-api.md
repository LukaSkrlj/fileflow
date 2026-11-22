# HTTP API reference

Base URL: `http://<host>:8080` (`FILEFLOW_LISTEN_ADDR`).

## Endpoints

| Method | Path | Success | Description |
|---|---|---|---|
| `PUT` | `/v1/files/{key...}` | `201` | Upload (streamed) |
| `GET` | `/v1/files/{key...}` | `200`, `206`, `304` | Download (streamed) |
| `HEAD` | `/v1/files/{key...}` | `200`, `206`, `304` | Same headers as `GET`, no body |
| `DELETE` | `/v1/files/{key...}` | `204` | Delete. Idempotent: deleting a missing key also returns `204`. |
| `GET` | `/v1/files?prefix=&limit=` | `200` | List objects |
| `GET` | `/healthz` | `200` `ok` | Liveness. Always OK while the process runs. |
| `GET` | `/readyz` | `200` `ready` | Readiness. `503` while draining or if the backend doesn't answer within 2 s. |

Admin server (`FILEFLOW_ADMIN_ADDR`, default `:9090`):

| Method | Path | Description |
|---|---|---|
| `GET` | `/metrics` | Prometheus text format. See [observability](observability.md#prometheus-metrics). |
| `GET` | `/debug/pprof/` | Go pprof index, plus `cmdline`, `profile`, `symbol`, `trace` |

## Keys

`{key...}` is everything after `/v1/files/`, slashes included. A key is valid if all of these are true:

- 1 to 1024 bytes long
- doesn't start or end with `/`
- contains no `\` and no NUL byte
- contains no empty, `.` or `..` segments
- contains no segment starting with `.ff-` (reserved)

Invalid keys return `400 invalid_key`.

## `PUT /v1/files/{key...}`

**Request headers**

| Header | Required | Description |
|---|---|---|
| `Content-Length` | no | Known size. Without it, chunked transfer encoding is used. |
| `Content-Type` | no | Stored and returned on `GET`. Defaults to `application/octet-stream`. |
| `X-Checksum-SHA256` | no | 64 hex characters, case-insensitive. If set, the upload is rejected when the body's hash differs. |
| `X-Client-Kind` | no | `human` or `agent`. Stored as object metadata and used as a log and metrics label. |
| `traceparent` | no | W3C trace context to join |

**Response:** `201 Created`, headers `ETag` and `X-Checksum-SHA256`, JSON body:

```json
{
  "key": "users/luka/report.pdf",
  "size": 1048576,
  "content_type": "application/pdf",
  "etag": "…",
  "sha256": "…",
  "mod_time": "2026-11-22T13:15:00Z",
  "metadata": {"client-kind": "human", "trace-id": "…", "sha256": "…"}
}
```

Fields marked `omitempty` in the source (`content_type`, `etag`, `sha256`, `metadata`) may be absent.

An existing object with the same key is replaced. Readers never see a partial or failed upload.

## `GET` / `HEAD /v1/files/{key...}`

**Request headers**

| Header | Description |
|---|---|
| `Range` | A single range in the form `bytes=a-b`, `bytes=a-` or `bytes=-n`. See [Range requests](#range-requests). |
| `If-Range` | ETag. If it doesn't match the current ETag, `Range` is ignored and the whole object is sent. |
| `If-None-Match` | ETag or `*`. If it matches, the response is `304 Not Modified` with no body. |

**Response headers**

| Header | Value |
|---|---|
| `Content-Type` | Stored type, or `application/octet-stream` |
| `Content-Length` | Bytes in this response |
| `Content-Range` | `bytes a-b/size` on `206`, `bytes */size` on `416` |
| `ETag` | Quoted ETag |
| `Accept-Ranges` | `bytes` |
| `Last-Modified` | HTTP date, if known |
| `X-Checksum-SHA256` | Stored SHA-256, if the backend has one |
| `Cache-Control` | Always `private, no-cache` |

If the connection fails part-way through, the server drops it, so the client sees a truncated response instead of a short file that looks complete.

### Range requests

| `Range` value | Result |
|---|---|
| absent | `200`, whole object |
| `bytes=0-99` | `206`, bytes 0–99 |
| `bytes=100-` | `206`, byte 100 to the end |
| `bytes=-100` | `206`, last 100 bytes (or the whole object if it's smaller) |
| `bytes=0-99999999` past the end | `206`, end clipped to the last byte |
| multiple ranges (`,`) | `200`, whole object |
| unit other than `bytes` | `200`, whole object |
| start ≥ size, end < start, malformed, or object size 0 | `416 invalid_range` |

## `GET /v1/files`

| Query | Default | Description |
|---|---|---|
| `prefix` | `""` | Only keys starting with this string |
| `limit` | `1000` | 1–1000. Values outside the range become 1000. |

```json
{"items": [ {ObjectInfo}, … ], "count": 2}
```

There is no pagination yet.

## Common response headers

Every response from the main port has:

| Header | Description |
|---|---|
| `traceparent` | W3C trace context for this request's span |
| `X-Trace-Id` | 32-hex trace id |
| `Server-Timing` | Phases measured before the response headers were sent, for example `backend_stat;dur=0.05, backend_open;dur=0.02` |

## Errors

Error responses are JSON:

```json
{"error": "<code>", "trace_id": "<32 hex>"}
```

| Status | `error` | Cause |
|---|---|---|
| `400` | `invalid_key` | Key fails the [key rules](#keys) |
| `400` | `invalid_checksum_header` | `X-Checksum-SHA256` isn't 64 hex characters |
| `404` | `not_found` | No object with this key |
| `413` | `too_large` | Body is larger than `FILEFLOW_MAX_UPLOAD` |
| `416` | `invalid_range` | Range can't be satisfied |
| `422` | `checksum_mismatch` | Body hash differs from `X-Checksum-SHA256`. No object was created. |
| `499` | `client_closed` | Client disconnected during the request (nginx convention) |
| `502` | `backend_error` | Storage backend returned an error |
| `503` | `backend_unavailable` | `/readyz` only: backend probe failed |
| `503` | *(plain text `shutting down`)* | `/readyz` only: draining |
