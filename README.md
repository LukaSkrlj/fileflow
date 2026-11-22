# fileflow

A streaming file gateway in Go for humans and AI agents, with pluggable storage: local disk, any S3-compatible store (AWS, Hetzner, R2, SeaweedFS), or Azure Blob. Logs and metrics are trace-correlated, and a benchmark tool is included for finding bottlenecks.

Full documentation (tutorials, how-to guides, reference, explanation) is in **[docs/](docs/README.md)**.

See **[ROADMAP.md](ROADMAP.md)** for the phased plan (caching/CDN, versioning, compression, protocols) and the bottleneck-hunting method.

## Quick start

```bash
make test            # unit tests + local-disk conformance suite
make run             # local disk backend on :8080, admin on :9090

# in another terminal
curl -T ./somefile -H "X-Client-Kind: human" localhost:8080/v1/files/users/luka/somefile
curl -O localhost:8080/v1/files/users/luka/somefile
curl -H "Range: bytes=0-99" localhost:8080/v1/files/users/luka/somefile
make bench-put bench-get
make web             # browser UI on :4200 (needs Node 24, see docs/how-to/use-the-web-ui.md)
```

Full stack with an S3 backend (SeaweedFS) or Azure Blob (Azurite):

```bash
docker compose up --build
FILEFLOW_BACKEND=azure docker compose up --build
make test-integration    # runs the conformance suite against both emulators
```

## API

| Method | Path | Notes |
|---|---|---|
| `PUT` | `/v1/files/{key...}` | Streams the body. Optional `X-Checksum-SHA256` (verified; the object is not created on mismatch → 422). Returns 201 + JSON info. |
| `GET` | `/v1/files/{key...}` | Streams. Supports `Range`, `If-Range`, `If-None-Match`. Returns `ETag` and `X-Checksum-SHA256`. |
| `HEAD` | `/v1/files/{key...}` | Headers only. |
| `DELETE` | `/v1/files/{key...}` | 204, idempotent. |
| `GET` | `/v1/files?prefix=&limit=` | JSON listing. |
| `GET` | `/healthz`, `/readyz` | Liveness / readiness (readiness checks the backend and goes 503 while draining). |
| admin `GET` | `:9090/metrics`, `:9090/debug/pprof/` | Prometheus + Go profiling. Keep internal. |

Send `traceparent` (W3C) to join an existing trace. Every response returns `traceparent` and `X-Trace-Id`, and error bodies include `trace_id`. Send `X-Client-Kind: human|agent` to split metrics and logs by client type.

## Reading a log line

```json
{"msg":"http_request","route":"GET /v1/files/{key...}","status":206,"duration_ms":34.8,
 "ttfb_ms":0.2,"bytes_out":4194304,"client":"agent","throughput_mbps":964.8,
 "timings_ms":{"backend_stat":0.05,"backend_open":0.02,"backend_read":0.58,"client_write":34.04},
 "trace_id":"ec0013484464c0fa13df059154451537","span_id":"..."}
```

Here `client_write` takes almost the whole duration, so the server and storage are fine and the client side is the limit. ROADMAP.md has the full symptom → cause → fix table.

## Configuration (env vars)

| Variable | Default | |
|---|---|---|
| `FILEFLOW_BACKEND` | `local` | `local` \| `s3` \| `azure` |
| `FILEFLOW_LISTEN_ADDR` / `FILEFLOW_ADMIN_ADDR` | `:8080` / `:9090` | |
| `FILEFLOW_LOG_LEVEL` / `FILEFLOW_LOG_FORMAT` | `info` / `json` | `text` for local dev |
| `FILEFLOW_MAX_UPLOAD` | `100GiB` | |
| `FILEFLOW_PART_SIZE` / `FILEFLOW_PART_CONCURRENCY` | `16MiB` / `4` | Memory per upload ≈ size × concurrency. Grows automatically for huge known-size uploads. |
| `FILEFLOW_IDLE_IO_TIMEOUT` | `60s` | Drops stalled clients; never cuts healthy long transfers |
| `FILEFLOW_SHUTDOWN_TIMEOUT` / `FILEFLOW_DRAIN_DELAY` | `30s` / `5s` | |
| `FILEFLOW_H2C` | `false` | Cleartext HTTP/2 behind a TLS-terminating LB |
| `FILEFLOW_LOCAL_ROOT` / `FILEFLOW_LOCAL_FSYNC` | `./data` / `true` | |
| `S3_ENDPOINT`, `S3_REGION`, `S3_BUCKET`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `S3_PATH_STYLE`, `S3_CREATE_BUCKET` | | Empty keys → default AWS credential chain |
| `AZURE_STORAGE_CONNECTION_STRING` *or* `AZURE_STORAGE_ACCOUNT_URL`, `AZURE_STORAGE_CONTAINER`, `AZURE_CREATE_CONTAINER` | | Account URL uses DefaultAzureCredential (Workload Identity on AKS) |

Examples:
- Hetzner: `S3_ENDPOINT=https://fsn1.your-objectstorage.com S3_REGION=fsn1`
- Cloudflare R2: `S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com S3_REGION=auto`

## Layout

```
cmd/fileflow            server (API + data path in one process for now)
cmd/ffbench             load generator: percentiles, TTFB, throughput, slowest trace ids
internal/storage        Backend interface, streaming multipart engine, instrumentation
internal/storage/{local,s3,azure}
internal/storage/storagetest   conformance suite every backend must pass
internal/httpapi        handlers, Range parsing
internal/obs            traceparent, trace-aware slog, Server-Timing, Prometheus metrics, middleware
internal/config         env config
```

## Notes on dependencies

- The Azure SDK is pinned to `azblob v1.6.4` / `azcore v1.20.0` / `azidentity v1.13.1`, the last versions that build with Go 1.24. On Go ≥ 1.26 (the Dockerfile default), run `go get -u ./... && go mod tidy` to move to the current releases. The APIs used here are unchanged.
- Verified here: `go vet`, `go test -race` on all packages, the conformance suite against local disk, moto (S3) and Azurite (Azure), and end-to-end runs of server + ffbench on all three backends (including h2c). Not verified here: the Dockerfile and docker-compose (no Docker daemon in the sandbox).
