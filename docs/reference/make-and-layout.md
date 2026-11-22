# Make targets and project layout reference

## Make targets

| Target | Runs |
|---|---|
| `make build` | `go build -o bin/ ./cmd/...` → `bin/fileflow`, `bin/ffbench` |
| `make test` | `go test -race ./...` |
| `make test-integration` | Conformance suite in `./internal/storage/...` against SeaweedFS (`:8333`) and Azurite (`:10000`) |
| `make run` | Build, then start the server with `FILEFLOW_LOG_FORMAT=text FILEFLOW_LOCAL_ROOT=./data` |
| `make web` | `npm install && npm start` in `web/`: browser UI on `:4200`, proxied to `:8080` |
| `make bench-put` | `ffbench -op put -n 50 -c 8 -size 64MiB -kind agent` |
| `make bench-get` | `ffbench -op get -n 500 -c 32 -size 1MiB -keys 50 -kind human` |
| `make bench-range` | `ffbench -op get -n 200 -c 16 -size 256MiB -keys 4 -range 8MiB -kind agent` |
| `make profile` | 30 s CPU profile from `:9090`, opens pprof UI on `:7070` |
| `make lint` | `go vet ./...` and a `gofmt -l` check |

## docker compose

| Command | Starts |
|---|---|
| `docker compose up --build` | fileflow (S3 backend) + SeaweedFS |
| `FILEFLOW_BACKEND=azure docker compose up --build` | fileflow (Azure backend) + Azurite |
| `docker compose --profile observability up` | + Prometheus on `:9091` (scrapes `fileflow:9090` every 5 s) |
| `docker compose --profile cache up` | + Redis on `:6379` (512 MB, LRU; not used by fileflow yet) |

| Service | Ports |
|---|---|
| `fileflow` | `8080`, `9090` |
| `seaweedfs` | `8333` |
| `azurite` | `10000` |
| `prometheus` | `9091` → `9090` |
| `redis` | `6379` |

## Docker image

- Multi-stage build: `golang:${GO_VERSION}-alpine` builds static binaries (`CGO_ENABLED=0`, `-trimpath`), and the runtime is `gcr.io/distroless/static-debian12:nonroot` (uid 65532, no shell).
- Contents: `/usr/local/bin/fileflow` (entrypoint) and `/usr/local/bin/ffbench`.
- Exposes `8080` and `9090`.

## Source layout

```
cmd/fileflow/            server entrypoint: config, backend selection, servers, shutdown
cmd/ffbench/             load generator
internal/config/         environment variable loading, size parsing
internal/httpapi/        HTTP handlers (handler.go), Range parsing (range.go)
internal/obs/            trace context, logger, timings, metrics registry, middleware
internal/storage/        Backend interface, multipart engine, instrumentation
internal/storage/local/  local disk backend
internal/storage/s3/     S3-compatible backend (aws-sdk-go-v2)
internal/storage/azure/  Azure Blob backend (azblob)
internal/storage/storagetest/  conformance suite
deploy/prometheus.yml    Prometheus scrape config for compose
web/                     Angular 22 browser UI (uploads, streamed downloads, media playback)
```
