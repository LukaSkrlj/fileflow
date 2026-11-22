# Observability reference

## Trace context

| Item | Format |
|---|---|
| Incoming `traceparent` | W3C `00-<32 hex trace id>-<16 hex span id>-<2 hex flags>`. If it is valid, the request becomes a child span. If it is invalid or missing, a new root span is started. All-zero ids are invalid. |
| Outgoing `traceparent` | This request's span |
| `X-Trace-Id` | Trace id only |
| Error bodies | `"trace_id"` field |
| Object metadata | `trace-id` of the request that wrote the object |

## Log format

The default is one JSON object per line (`FILEFLOW_LOG_FORMAT=json`). With `text`, the same fields are written in `key=value` form, with groups as dotted names (`timings_ms.backend_stat=…`).

Fields on every line:

| Field | Description |
|---|---|
| `time`, `level`, `msg` | Standard slog fields |
| `service` | `fileflow` |
| `version` | Build version |
| `trace_id`, `span_id` | Present when the line was logged during a request |

## The `http_request` access log line

One line per request, written when the request finishes.

| Field | Type | Description |
|---|---|---|
| `method` | string | HTTP method |
| `route` | string | Matched pattern, for example `GET /v1/files/{key...}`, or `unmatched` |
| `path` | string | Raw request path |
| `status` | int | Response status |
| `duration_ms` | float | Total time including the body transfer |
| `ttfb_ms` | float | Time to the first response body byte. Absent if no body was written. |
| `bytes_in`, `bytes_out` | int | Request and response body bytes |
| `throughput_mbps` | float | `max(bytes_in, bytes_out) × 8 / 1e6 / duration` |
| `client` | string | `human`, `agent` or `unknown` (from `X-Client-Kind`) |
| `proto` | string | `HTTP/1.1`, `HTTP/2.0` |
| `remote` | string | Peer address |
| `user_agent` | string | |
| `timings_ms` | object | Phase durations, see below |
| `aborted` | bool | Present and `true` if the handler aborted the connection |

**Level:** `ERROR` for status ≥ 500, `WARN` for aborted requests, otherwise `INFO`. A 4xx response is logged at `INFO`.

### Timing phases (`timings_ms`)

| Phase | Recorded for | Measures |
|---|---|---|
| `backend_stat` | `GET`, `HEAD`, `/readyz` | Metadata lookup in storage |
| `backend_open` | `GET` | Opening the storage stream, up to the first byte being available |
| `backend_read` | `GET` | Total time blocked reading from storage while streaming |
| `client_write` | `GET` | Total time blocked writing to the client |
| `backend_put` | `PUT` | The whole storage write, including multipart parts and commit |
| `client_read` | `PUT` | Time blocked waiting for the uploader's bytes. This time is part of `backend_put`. |
| `backend_delete` | `DELETE` | Storage delete |
| `backend_list` | `GET /v1/files` | Storage list |

### Other log messages

| `msg` | Level | When |
|---|---|---|
| `fileflow started` | INFO | Startup, with addresses, backend and part settings |
| `request failed` | WARN / ERROR | A handler returned an error with an underlying cause. Has `code` and `err`. |
| `download interrupted` | WARN | Streaming stopped before `Content-Length` bytes were sent. Has `key`, `sent`, `want`, `err`. |
| `object changed during request` | WARN | ETag changed between `Stat` and `Get` |
| `handler panic` | ERROR | Unexpected panic, with stack |
| `shutdown: draining`, `shutdown complete` | INFO | `SIGTERM` / `SIGINT` handling |

## `Server-Timing` header

Holds the phases that finished before the response headers were sent, in the form `name;dur=<ms>`, joined with `, `. For a `GET` this is usually `backend_stat` and `backend_open`. Phases that happen while the body streams (`backend_read`, `client_write`) appear only in the log.

## Prometheus metrics

Served on `GET :9090/metrics`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `fileflow_http_requests_total` | counter | `route`, `method`, `code`, `client` | Requests |
| `fileflow_http_request_duration_seconds` | histogram | `route`, `method` | Full request duration |
| `fileflow_http_ttfb_seconds` | histogram | `route`, `method` | Time to the first response byte |
| `fileflow_http_bytes_total` | counter | `direction` (`in`/`out`), `client` | Body bytes |
| `fileflow_http_inflight_requests` | gauge | | Requests being served now |
| `fileflow_backend_op_duration_seconds` | histogram | `backend`, `op`, `result` | Storage calls. `op` is `put`, `open`, `stat`, `delete` or `list`. `result` is `ok`, `not_found`, `canceled` or `error`. |
| `go_goroutines` | gauge | | |
| `go_memstats_heap_inuse_bytes` | gauge | | |
| `go_gc_cycles_total` | counter | | |

Histogram buckets (seconds): `0.001 0.005 0.01 0.025 0.05 0.1 0.25 0.5 1 2.5 5 10 30 60 120 300`.

## pprof

Standard `net/http/pprof` handlers on the admin port under `/debug/pprof/`. Named profiles (`heap`, `goroutine`, `allocs`, `block`, `mutex`, `threadcreate`) are served through the index handler.
