# fileflow roadmap

A Go file gateway for humans and AI agents. It needs high availability and fast transfers, and it has to work with any S3-compatible store (AWS, Hetzner, Cloudflare R2, SeaweedFS) as well as Azure Blob. Each phase below ships something that works on its own, and you measure before you optimize.

```
 humans ─┐                       ┌─────────────── fileflow (stateless, N replicas) ──────────────┐
         ├─ HTTPS ─▶ LB / CDN ─▶ │ obs middleware → httpapi → storage.Backend (local | s3 | azure)│ ─▶ object store
 agents ─┘                       └───────────────── :9090 /metrics /debug/pprof ─────────────────┘
```

---

## Phase 1 — Streaming core plus observability ✅ (this repo)

**What's built**
- A single `storage.Backend` interface with three implementations: local disk, S3-compatible and Azure Blob. A shared conformance suite (`storagetest`) runs against every backend. It passes against local disk, moto (S3) and Azurite (Azure).
- Streaming upload and download. Memory per upload is capped at `part_size × part_concurrency` (16 MiB × 4 by default), no matter how big the file is. Our own multipart code (S3 `UploadPart`, Azure `StageBlock`) decides at the end whether to commit or abort, so an upload that fails or has a bad checksum never becomes visible.
- End-to-end SHA-256. The client can send `X-Checksum-SHA256` for the server to check. Either way the server computes it and returns it.
- HTTP semantics: `Range` (including suffix ranges), `If-Range`, `If-None-Match`/`304`, `HEAD`, `416` responses, and JSON errors that carry the `trace_id`.
- Observability:
  - W3C `traceparent` propagation.
  - Every log line includes `trace_id`/`span_id`.
  - The access log splits time into `backend_stat`, `backend_open`, `backend_read`, `client_write`, `client_read` and `backend_put`.
  - The phases known before the body starts also go out in a `Server-Timing` header.
  - Prometheus metrics and pprof are on a separate admin port.
- Transport:
  - Idle-progress deadlines: a stalled client is cut off, but a 50 GB transfer is never killed.
  - Optional h2c (HTTP/2 without TLS) for traffic behind a load balancer.
  - Graceful drain on SIGTERM (readyz → 503, wait, then shutdown).
- `ffbench`, a load generator that reports p50/p90/p99 latency, TTFB and throughput, plus the trace ids of the slowest requests.

**Why one process for now.** The task allows simulating two services in one. Splitting early adds a network hop, a second deployment and failure modes without making anything faster. The code is already divided at the seam where the split will happen (handlers → `storage.Backend`). Phase 5 introduces the metadata service once versioning gives it a real job.

---

## How to find the bottleneck: where to start and when to stop

**Start** by writing down targets. Without them you never know when you're done:

| Target (example — agree real ones) | Value |
|---|---|
| Small human file (≤ 5 MB) TTFB p99 | < 150 ms in-region |
| Agent single-stream throughput | ≥ 1 Gbit/s; aggregate ≥ NIC × 0.7 |
| Availability | 99.9 % monthly |
| Memory per pod at 100 concurrent uploads | < 1 GiB |

**Then measure every layer separately, from the outside in:**

1. **Network ceiling.** Run `iperf3` client→pod and pod→storage endpoint. This sets the upper bound: you can't beat it, only get close to it.
2. **Client view.** Run `ffbench` from where the real users and agents are, not from your laptop next to the server.
3. **Server split.** Look up the slowest trace ids from ffbench in the logs. In `timings_ms`, the biggest number names the guilty layer.
4. **CPU and memory.** Run `make profile` (pprof) while a benchmark runs. Check the `go_goroutines` and heap metrics.
5. **Storage limits.** Providers throttle by request rate and throughput, per account, prefix or blob. Look out for 503 SlowDown and 429 ServerBusy responses in `backend_error` logs.

**Reading the split (symptom → cause → fix):**

| Symptom in logs / ffbench | Bottleneck | Fix (phase) |
|---|---|---|
| `backend_stat` + `backend_open` dominate small GETs | Round-trips to object store | Redis metadata cache, presigned redirect (P3) |
| `backend_read` dominates big GETs | Single-stream storage throughput | Parallel ranged reads from backend, cache hot objects (P3) |
| `client_write` dominates | Client network / distance | CDN / Front Door, HTTP/3 for lossy links, compression (P3, P6, P7) |
| `client_read` ≫ `backend_put` on uploads | Uploader's uplink | Client-side parallel/resumable multipart (P7) |
| `backend_put` dominates uploads | Storage write path | Bigger parts, more part concurrency, same-region storage |
| Server TTFB small but client TTFB large | Queueing, LB, TLS handshakes, client CPU | Connection reuse, keep-alive, h2, check LB |
| CPU pegged in pprof (sha256, TLS, gzip) | Gateway CPU | More replicas / bigger nodes; skip hashing for trusted direct uploads |

**Example from the sandbox** (emulators, so only the method carries over, not the numbers). 2 MiB ranged GETs on the S3 backend had p50 ≈ 84 ms. Of that, `backend_stat` ≈ 30 ms and `backend_open` ≈ 46 ms. Nearly all the latency is two round-trips to storage, which is exactly what Phase 3a removes. On local disk the same test ran at 2.5 GB/s, and there the slowest request's time was almost all `client_write`, meaning the benchmark client was the limit.

**Stop** when you meet the targets, or when a layer reaches about 70–80% of its measured ceiling (iperf3 for the network, the provider's documented limits for storage). Past that point you're paying in complexity for a few percent.

Make it a habit: keep `ffbench -json` output in CI for every release so regressions show up as a diff.

---

## Phase 2 — Deploy on Kubernetes (AKS) with basic HA

Do this early: bottlenecks found on a laptop are often not real ones.

- Run a Deployment with ≥ 3 replicas, using `topologySpreadConstraints` across availability zones, a PodDisruptionBudget (`minAvailable: 2`), and readiness on `/readyz` / liveness on `/healthz`. Set `terminationGracePeriodSeconds` higher than `FILEFLOW_SHUTDOWN_TIMEOUT`.
- Use **Workload Identity** to reach Azure Blob (`AZURE_STORAGE_ACCOUNT_URL`, no keys) and a **ZRS/GZRS** storage account for durability across zones and regions. Use a Private Endpoint so gateway↔storage traffic stays on the backbone.
- Ingress: Azure Application Gateway for Containers, or NGINX/Envoy. Raise body size and timeouts for large uploads, and turn off request buffering. Many ingress defaults buffer the whole body or cap it at a few MB.
- HPA on CPU plus inflight requests (`fileflow_http_inflight_requests`). Prometheus scraping goes to Azure Monitor managed Prometheus, and logs go to Log Analytics. Example KQL: `ContainerLogV2 | where LogMessage.trace_id == "..."`.
- Auth comes before anything public. Start with API keys or JWT (Entra ID for humans, client credentials for agents), plus per-prefix authorization (`users/<id>/`, `agents/<id>/`, `shared/`).

## Phase 3 — Caching and taking the gateway out of the hot path

In order of value per effort:

a. **Redis metadata cache.** Cache `Stat` results (size, etag, sha256, content type) for 30–300 s, invalidated on PUT and DELETE. This removes `backend_stat` from every GET. Don't put large blobs in Redis: it is memory-bound and single-threaded per shard, and multi-MB values cause latency spikes for everyone. At most, cache small objects (< 512 KB) that are read very often.

b. **Presigned / SAS redirects for large downloads** (the biggest win for agent files). The gateway authorizes the request, then returns `307` to a short-lived S3 presigned URL or Azure user-delegation SAS. Bytes then flow straight from the object store, and gateway CPU and bandwidth stop mattering. Make it a policy: redirect above N MB, or always for agents. The same pattern works for uploads (presigned multipart parts).

c. **CDN.** On Azure this means **Azure Front Door Standard/Premium**. Azure CDN from Microsoft (classic) is being retired, so don't start new work on it. A CDN pays off for content that is shared (many readers) and immutable. Private per-user files with short TTLs get little benefit and risk leaking if cache keys are wrong. Wait for Phase 5 versioning, so URLs are immutable (`/v1/files/<key>?v=<version>`) and can carry `Cache-Control: public, max-age=31536000, immutable`. Use signed URLs or tokens at the edge for private content.

d. **Node-local SSD cache** for hot large objects (e.g. a model many agents pull at once). Use an LRU on a local NVMe disk keyed by etag, with single-flight so 50 concurrent misses trigger only one backend fetch.

e. **Parallel ranged backend reads.** For a single large GET, fetch N ranges from storage concurrently and stream them in order. This helps when one storage stream is slower than the client link.

## Phase 4 — OpenTelemetry

- Replace the internals of `internal/obs` with the OTel SDK. Trace ids already use the W3C format, so existing log correlation keeps working. Export to Azure Monitor (Application Insights) or Grafana Tempo/Jaeger.
- Add spans around backend calls, with child spans per multipart part. A 10 GB upload with one slow part shows up immediately.
- Add exemplars on histograms (metric → example trace), and RED dashboards per route and client kind (human vs agent).

## Phase 5 — Versioning and the two-service split

- **Metadata service** (Postgres): `files(id, path, owner, current_version)`, `versions(id, file_id, blob_sha256, size, created_by, trace_id, created_at)`.
- **Data service** (this gateway): stores blobs **content-addressed** (`blobs/sha256/ab/cd/<sha>`). Identical uploads deduplicate for free, which matters with agents that re-upload the same artifacts. Versions are immutable, which also fixes the overwrite-between-Stat-and-Get race that is logged today.
- Services talk over gRPC or HTTP with `traceparent` propagated (`obs.Inject` already exists).
- Garbage collection: unreferenced blobs past their retention period get deleted. Add lifecycle rules for aborted multipart uploads.
- Native bucket versioning (S3/Azure) is the quick alternative. It is simpler, but it's provider-specific and harder to query.

## Phase 6 — Compression

Measure with `ffbench -data text` (compressible) against `-data random` (incompressible).

- **Transport compression.** Use `Accept-Encoding: zstd, gzip` for compressible content types (JSON, CSV, logs, text, source). `klauspost/compress` provides fast zstd and gzip in Go. Skip it for already-compressed types (zip, mp4, jpeg, parquet with codecs, most model weights) and for Range requests.
- **Storage compression.** Store zstd-compressed and decompress on read. This saves storage and egress cost but breaks plain byte ranges. Use the *seekable zstd* format (independent frames plus an index) to keep Range support.
- Decision rule: compress when the ratio is above about 1.3 **and** the link is slower than the compressor. On a 10 Gbit link inside a datacenter, zstd level 1–3 can be the bottleneck. Toward a remote agent on 100 Mbit, it is almost always a win. Let ffbench numbers decide per content type.

## Phase 7 — Network and protocol work

In order. The early items are cheap and usually enough.

1. **Connection reuse and tuning.** Use keep-alive and large idle pools (done for the S3 client), BBR congestion control on nodes, and larger socket buffers for high bandwidth × latency links.
2. **HTTP/2** (h2c is done behind the LB; full h2 with TLS at the edge). It multiplexes many small human requests.
3. **Client-side parallel and resumable transfers.** Ranged parallel downloads (like aria2) and resumable multipart uploads (expose S3-style multipart, or the `tus` protocol). For multi-GB agent files on imperfect networks, resuming matters more than any protocol.
4. **HTTP/3 / QUIC** (`quic-go`) at the edge, for long-distance or lossy links: it avoids head-of-line blocking and connection migration helps. Inside an Azure region it rarely beats tuned TCP. Front Door can terminate near users, so you may get most of the benefit without running QUIC yourself.
5. **gRPC streaming** between internal services (metadata ↔ data) once they're split.
6. **Placement.** Run agents in the same region or VNet as storage and use Private Endpoints. Often the "faster protocol" turns out to be "fewer kilometres".

---

## Backend notes

- **AWS SDK.** `feature/s3/manager` was deprecated in Feb 2026 in favour of `feature/s3/transfermanager`. We use our own multipart (`storage.UploadParts`), so we're not affected and keep control of commit/abort.
- **S3-compatible providers.** Newer AWS SDKs add CRC checksums by default, and some providers reject them. The client sets `RequestChecksumCalculation=WhenRequired`, and integrity comes from our SHA-256.
- **Hetzner Object Storage** is S3-compatible and cheap per TB. Ingress, internal traffic and API calls are free; check current limits per bucket and object.
- **Azure metadata** names must be C# identifiers, so `client-kind` is stored as `client_kind` and translated back. This was caught by the conformance suite.
- **MinIO** community edition stopped publishing images in Oct 2025 and is in maintenance mode. Use SeaweedFS, Garage or RustFS for self-hosted S3, or moto for tests.
