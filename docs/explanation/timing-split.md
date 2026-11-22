# Why every request is split into timing phases

"The download is slow" is the most common complaint a file service gets, and one of the hardest to act on. The cause could be the client's network, the gateway's CPU, the storage provider, the load balancer, or the distance between any of them. A single latency number can't tell these apart.

fileflow divides each request's time by **who it was waiting for**.

## The phases

For a download:

```
|-- backend_stat --|-- backend_open --|== streaming: backend_read + client_write ==|
                                       ^ first byte
```

- **`backend_stat`**: asking storage for size, ETag and checksum. fileflow needs these before it can answer range and conditional requests.
- **`backend_open`**: opening the content stream, up to when storage is ready to send.
- **`backend_read`**: during streaming, the total time spent waiting for storage to deliver the next bytes.
- **`client_write`**: during streaming, the total time spent waiting for the client's connection to accept bytes.

For an upload, **`backend_put`** covers the whole storage write, and **`client_read`** is the part of it spent waiting for the uploader's bytes.

The streaming loop measures the read and write sides separately. When one side is slow, the time shows up on that side.

## Reading it

Once you have the phases, you usually only need the biggest number:

- `client_write` dominates → the server and storage are fine. The client's network or distance is the limit. A faster server won't help, but a CDN or a closer region might.
- `backend_read` dominates → one storage stream is slower than the client link. Parallel ranged reads or caching help.
- `backend_stat` + `backend_open` dominate small requests → round trips to storage. A metadata cache or presigned redirects remove them.
- `client_read` ≫ the rest of `backend_put` → the uploader's uplink.

The full symptom → cause → fix table is in [ROADMAP.md](../../ROADMAP.md).

## Why three outputs

The same phases go to three places, because each answers a different question:

- **The access log line** (`timings_ms`) has the full breakdown for one request. Combined with the trace id, a user's complaint ("this request was slow, here's the id") leads to an answer.
- **The `Server-Timing` header** gives clients the server-side view without needing access to the logs. It is visible in browser devtools and `curl -v`, and ffbench averages it. It only holds the phases that finished before headers were sent. The streaming phases happen after the headers are gone.
- **Backend metrics** (`fileflow_backend_op_duration_seconds`) show trends across all requests: is storage getting slower this week, and for which operation?

## Why trace ids are everywhere

A breakdown only helps if you can find it. fileflow uses W3C `traceparent`, the format OpenTelemetry uses, and puts the trace id in response headers, in error bodies, on every log line, and in the metadata of every object it writes. ffbench sends a fresh trace id with each request and prints the slowest ones. Going from "p99 is bad" to "this request, this phase" takes one `grep`.

Using the standard format now means that when the internals move to the OpenTelemetry SDK (roadmap phase 4), the ids already in logs and stored objects stay valid.

## Why a homemade metrics registry

`internal/obs` has a small Prometheus text-format writer instead of `prometheus/client_golang`. Phase 1 has few dependencies on purpose, and the service needs only a handful of counters, one gauge and a few histograms. The output is standard, so any Prometheus-compatible scraper works. When OpenTelemetry arrives, the registry gets replaced and the metric names can stay.

## Labels are kept bounded

`X-Client-Kind` is folded into `human`, `agent` or `unknown` before it becomes a label, and routes use the matched pattern (`GET /v1/files/{key...}`), not the raw path. With unbounded labels, one client sending random values could create millions of time series and take down the metrics system.
