# Storage backend contract reference

Package `internal/storage`.

## `Backend` interface

```go
type Backend interface {
    Name() string
    Put(ctx context.Context, key string, r io.Reader, opts PutOptions) (ObjectInfo, error)
    Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error)
}
```

| Method | Rules |
|---|---|
| `Name` | Short id used as the `backend` metric label: `local`, `s3`, `azure` |
| `Put` | Must stream: bounded per-part buffers are fine, the whole body in memory is not. Must fail if `opts.Size ≥ 0` and the byte count differs. Must call `opts.BeforeCommit` after the last byte and before the object becomes visible. If it returns an error, the upload must be aborted. |
| `Get` | `rng == nil` means the whole object. Returns `ErrNotFound` or `ErrInvalidRange`. |
| `Stat` | Metadata only. Returns `ErrNotFound`. |
| `Delete` | Deleting a missing key returns `nil`. |
| `List` | Up to `limit` objects whose key starts with `prefix` |

## Types

```go
type ObjectInfo struct {
    Key         string            `json:"key"`
    Size        int64             `json:"size"`
    ContentType string            `json:"content_type,omitempty"`
    ETag        string            `json:"etag,omitempty"`
    SHA256      string            `json:"sha256,omitempty"`
    ModTime     time.Time         `json:"mod_time"`
    Metadata    map[string]string `json:"metadata,omitempty"`
}

type Range struct{ Start, Length int64 }      // [Start, Start+Length)
func (r Range) End() int64                    // inclusive last byte
func (r Range) HTTPHeader() string            // "bytes=a-b"

type PutOptions struct {
    ContentType  string
    Size         int64                                 // -1 = unknown
    Metadata     map[string]string                     // keys: [a-z0-9-]
    BeforeCommit func() (map[string]string, error)
}
```

## Errors

| Error | HTTP mapping |
|---|---|
| `ErrNotFound` | `404 not_found` |
| `ErrInvalidKey` | `400 invalid_key` |
| `ErrInvalidRange` | `416 invalid_range` |
| any other error | `502 backend_error`, or `499` if the client has gone |

## Helpers

| Function | Purpose |
|---|---|
| `ValidateKey(key) error` | Applies the [key rules](http-api.md#keys). `MaxKeyLen = 1024`. |
| `MergeMeta(a, b) map[string]string` | New map with `b` on top of `a` |
| `NewBufPool(size) *BufPool` | Pool of fixed-size part buffers |
| `ReadChunk(r, buf) (n, eof, err)` | Fills `buf`. `eof` means the stream ended, so the caller can use a single-request upload. |
| `UploadParts(ctx, r, first, firstLen, concurrency, pool, maxParts, upload) (int64, error)` | Streams `r` as numbered parts (from 1) with at most `concurrency` in flight. Cancels everything on the first error. |
| `Instrument(b) Backend` | Wraps a backend with timing and metrics. Applied automatically by `httpapi.New`. |

## Conformance suite

`storagetest.Run(t, backend, bigSize)`. Set `bigSize` larger than the backend's part size.

| Subtest | Checks |
|---|---|
| `RoundTripSmall` | Put, Stat (size and non-empty ETag), Get |
| `MultipartAndRanges` | Multipart upload and ranged reads |
| `BeforeCommitAbortHidesObject` | An error from `BeforeCommit` leaves no visible object |
| `ShortUploadRejected` | A size mismatch fails |
| `CommitMetadata` | Metadata from `BeforeCommit` is persisted where supported |
| `ListAndDelete` | Prefix listing, idempotent delete |
| `InvalidKeys` | Unsafe keys are rejected |

## Per-backend behaviour

| | local | s3 | azure |
|---|---|---|---|
| Upload mechanism | temp file in `.ff-tmp/`, renamed into place | single `PutObject` if it fits in one part, otherwise `CreateMultipartUpload` / `UploadPart` / `CompleteMultipartUpload` | single upload if it fits in one block, otherwise `StageBlock` / `CommitBlockList` |
| Part limit | n/a | 10,000 parts, min 5 MiB | 50,000 blocks, min 1 MiB |
| Part size below minimum | n/a | replaced with 16 MiB | replaced with 16 MiB |
| Auto-grow part size for large known sizes | n/a | yes | yes |
| Metadata from `BeforeCommit` stored | yes | single-part only | yes |
| ETag | SHA-256 if known, else `<size hex>-<mtime ns hex>` | provider ETag | provider ETag |
| Metadata storage | `.ff-meta/<key>.json` sidecar | object user metadata | blob metadata (`-` stored as `_`) |
| Durability | `fsync` before rename if `FILEFLOW_LOCAL_FSYNC=true` | provider | provider (choose ZRS/GZRS) |

Local layout under `FILEFLOW_LOCAL_ROOT`:

```
<key>                   object bytes
.ff-meta/<key>.json     sidecar metadata
.ff-tmp/                in-flight uploads
```
