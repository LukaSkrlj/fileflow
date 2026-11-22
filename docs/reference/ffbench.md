# ffbench reference

`ffbench` is a load generator for fileflow. It is built into `bin/ffbench` by `make build` and included in the Docker image at `/usr/local/bin/ffbench`.

```
ffbench [flags]
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `-url` | `http://localhost:8080` | fileflow base URL |
| `-op` | `put` | `put` or `get` |
| `-n` | `100` | Total requests |
| `-c` | `8` | Concurrent workers |
| `-size` | `4MiB` | Object size for `put`, and for the `get` setup uploads |
| `-keys` | `10` | `get` only: number of distinct objects uploaded in the setup phase and read at random |
| `-range` | *(empty)* | `get` only: read random ranges of this size instead of whole objects. Ignored if it's ≥ `-size`. |
| `-prefix` | `bench` | Key prefix |
| `-kind` | `agent` | Value sent in `X-Client-Kind` |
| `-data` | `random` | `random` (incompressible) or `text` (repeated JSON log lines, compresses well) |
| `-h2c` | `false` | Use cleartext HTTP/2. The server needs `FILEFLOW_H2C=true`. |
| `-json` | `false` | Print the summary as one JSON object |
| `-top` | `5` | Number of slowest requests to list |

Sizes accept the same units as the [configuration](configuration.md) (`MiB`, `GB`, …).

## Behaviour

- Keys are `<prefix>/<run id>/obj-<NNNNN>`. The run id comes from the start time, so runs don't collide.
- Every request gets a new root `traceparent`, so its trace id can be looked up in the server log.
- The client turns off transparent compression and uses 256 KiB socket buffers.
- `get` first uploads `-keys` objects one after another. If any setup upload fails, it exits.
- Benchmark objects are **not deleted** afterwards.

## Text output

```
GET  requests=500 ok=500 errors=0 concurrency=32 wall=412ms
throughput   1272.3 MB/s  (10178 Mbit/s)   1213.6 req/s
latency      p50=… p90=… p99=… max=…
ttfb         p50=… p90=… p99=… max=…
server avg   backend_open=…ms backend_stat=…ms
error x3: HTTP 502

slowest 5 (grep these trace ids in the server log):
  <duration> ttfb=<ttfb> trace_id=<id> key=<key>
```

- **throughput:** total bytes from successful requests divided by wall time.
- **latency:** full request time including the body.
- **ttfb:** time to the first response byte, as seen by the client.
- **server avg:** mean of each `Server-Timing` phase over successful requests.
- A request counts as an error if it failed at the transport level or returned a status ≥ 300.

## JSON output (`-json`)

```json
{
  "op": "get", "concurrency": 32, "requests": 500, "ok": 500,
  "errors": {"HTTP 502": 3},
  "wall_s": 0.41, "req_per_s": 1213.6, "mb_per_s": 1272.3,
  "latency_ms": {"p50": 21.3, "p90": 38.1, "p99": 61.0, "p100": 74.2},
  "ttfb_ms":    {"p50": 1.1,  "p90": 2.4,  "p99": 5.8,  "p100": 9.0},
  "server_timing_ms": {"backend_open": 0.04, "backend_stat": 0.05}
}
```

The JSON output doesn't include the slowest-requests list.
