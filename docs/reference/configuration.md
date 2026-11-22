# Configuration reference

fileflow reads all settings from environment variables at startup. If any value fails to parse, the process prints every error and exits.

**Value formats**

- *size:* a plain byte count (`5000000`) or a number with a unit. `KiB`, `MiB`, `GiB` and `TiB` are binary (1024). `KB`, `MB`, `GB` and `TB` are decimal (1000). Decimals are allowed: `1.5GiB`.
- *duration:* Go duration syntax: `500ms`, `30s`, `2m`.
- *bool:* `true`/`false`, `1`/`0`, `t`/`f`.

## Server

| Variable | Type | Default | Description |
|---|---|---|---|
| `FILEFLOW_BACKEND` | string | `local` | `local`, `s3` or `azure` |
| `FILEFLOW_LISTEN_ADDR` | addr | `:8080` | Public API listener |
| `FILEFLOW_ADMIN_ADDR` | addr | `:9090` | Metrics and pprof listener. Keep it internal. |
| `FILEFLOW_LOG_LEVEL` | string | `info` | `debug`, `info`, `warn`, `error` |
| `FILEFLOW_LOG_FORMAT` | string | `json` | `json` or `text` |
| `FILEFLOW_MAX_UPLOAD` | size | `100GiB` | Larger uploads get `413`. `0` disables the limit. |
| `FILEFLOW_PART_SIZE` | size | `16MiB` | Multipart part size (S3) and block size (Azure) |
| `FILEFLOW_PART_CONCURRENCY` | int | `4` | Parts in flight per upload. Memory per upload ≈ part size × this value. |
| `FILEFLOW_IDLE_IO_TIMEOUT` | duration | `60s` | Closes a connection after this long without read or write progress. `0` disables it. |
| `FILEFLOW_SHUTDOWN_TIMEOUT` | duration | `30s` | Maximum wait for in-flight requests after draining |
| `FILEFLOW_DRAIN_DELAY` | duration | `5s` | Time `/readyz` returns `503` before shutdown starts |
| `FILEFLOW_H2C` | bool | `false` | Also accept cleartext HTTP/2 on the API port |

Fixed server limits (not configurable): read-header timeout 10 s, keep-alive idle timeout 120 s, maximum header size 64 KiB.

## Local backend

| Variable | Type | Default | Description |
|---|---|---|---|
| `FILEFLOW_LOCAL_ROOT` | path | `./data` | Root directory. Created if it doesn't exist. |
| `FILEFLOW_LOCAL_FSYNC` | bool | `true` | Flush each upload to stable storage before it becomes visible |

## S3 backend

| Variable | Type | Default | Description |
|---|---|---|---|
| `S3_ENDPOINT` | URL | *(empty)* | Custom endpoint. Leave it empty for AWS. |
| `S3_REGION` | string | `us-east-1` | Region. Use `auto` for R2. |
| `S3_BUCKET` | string | *(empty)* | Bucket name |
| `S3_ACCESS_KEY` | string | *(empty)* | Static credentials. If both keys are empty, the default AWS credential chain is used. |
| `S3_SECRET_KEY` | string | *(empty)* | |
| `S3_PATH_STYLE` | bool | `false` | Use path-style URLs. Most self-hosted stores need this. |
| `S3_CREATE_BUCKET` | bool | `false` | Create the bucket at startup |

## Azure backend

Set **either** a connection string **or** an account URL.

| Variable | Type | Default | Description |
|---|---|---|---|
| `AZURE_STORAGE_CONNECTION_STRING` | string | *(empty)* | Full connection string, including the key |
| `AZURE_STORAGE_ACCOUNT_URL` | URL | *(empty)* | `https://<account>.blob.core.windows.net`. Authenticates with `DefaultAzureCredential`. |
| `AZURE_STORAGE_CONTAINER` | string | *(empty)* | Container name |
| `AZURE_CREATE_CONTAINER` | bool | `false` | Create the container at startup |

## Test-only variables

Only the test suites read these. See [run the tests](../how-to/run-tests.md).

| Variable | Default |
|---|---|
| `FILEFLOW_TEST_S3_ENDPOINT` | *(unset: S3 conformance test is skipped)* |
| `FILEFLOW_TEST_S3_REGION` | `us-east-1` |
| `FILEFLOW_TEST_S3_BUCKET` | `fileflow-test` |
| `FILEFLOW_TEST_S3_ACCESS_KEY` / `FILEFLOW_TEST_S3_SECRET_KEY` | `minioadmin` / `minioadmin` |
| `FILEFLOW_TEST_AZURE_CONN` | *(unset: Azure conformance test is skipped)* |

## Build-time

| Setting | Description |
|---|---|
| `-ldflags "-X main.version=<v>"` | Version string in logs. The Dockerfile sets it from the `VERSION` build arg. Defaults to `dev`. |
| Docker build arg `GO_VERSION` | Go toolchain image, default `1.26` |
