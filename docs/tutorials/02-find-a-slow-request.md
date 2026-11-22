# Tutorial: find out why a request is slow

In this tutorial you will put load on fileflow with the bundled `ffbench` tool, pick the slowest request it reports, and use its trace id to find out which layer made it slow.

Before you start, finish [Your first upload and download](01-first-upload.md). The server must be running with `make run`.

## 1. Run a download benchmark

In a second terminal:

```bash
make bench-get
```

This uploads 50 objects of 1 MiB each, then runs 500 `GET`s with 32 concurrent workers. When it finishes it prints a summary:

```
GET  requests=500 ok=500 errors=0 concurrency=32 wall=412ms
throughput   1272.3 MB/s  (10178 Mbit/s)   1213.6 req/s
latency      p50=21.3ms p90=38.1ms p99=61.0ms max=74.2ms
ttfb         p50=1.1ms p90=2.4ms p99=5.8ms max=9.0ms
server avg   backend_open=0.04ms backend_stat=0.05ms

slowest 5 (grep these trace ids in the server log):
  74.2ms     ttfb=9.0ms      trace_id=4bf92f3577b34da6a3ce929d0e0e4736 key=bench/...
  ...
```

Your numbers will be different. That is expected.

## 2. Copy the slowest trace id

Take the `trace_id` from the first line under "slowest 5". ffbench made up this id and sent it in a `traceparent` header, and the server used it for its log line.

## 3. Find the server's log line for it

The server prints logs to its terminal. To search them, stop the server (Ctrl-C) and start it again with the output saved to a file:

```bash
make run 2>&1 | tee server.log
```

Run `make bench-get` again, copy a new slowest trace id, and search for it:

```bash
grep 4bf92f3577b34da6a3ce929d0e0e4736 server.log
```

You get exactly one `http_request` line.

## 4. Read the timing breakdown

Look at the `timings_ms` fields in that line:

```
timings_ms.backend_stat=0.06 timings_ms.backend_open=0.03 timings_ms.backend_read=0.41 timings_ms.client_write=72.9
```

Find the biggest number. Here it is `client_write`: the server spent almost all of the 74 ms waiting for the client to accept bytes. Storage was fast. The benchmark client, which runs 32 workers on the same machine, was the limit.

If `backend_stat` and `backend_open` were the biggest numbers instead, the time would be going to round-trips to the object store.

## 5. Try an upload benchmark

```bash
make bench-put
```

This uploads fifty 64 MiB objects. Look up the slowest trace id again. For uploads, compare `client_read` (time waiting for the uploader's bytes) with `backend_put` (time spent in the storage write).

## What you did

You generated load, found the slowest request by its trace id, and named the layer responsible by reading the biggest number in `timings_ms`. This is the same method you will use on a real deployment.

**Next steps:**

- [Benchmark and profile the server](../how-to/benchmark-and-profile.md) covers more benchmark shapes and CPU profiling.
- [Why every request is split into timing phases](../explanation/timing-split.md) explains what each phase means.
- The symptom → cause → fix table in [ROADMAP.md](../../ROADMAP.md#how-to-find-the-bottleneck-where-to-start-and-when-to-stop) maps each dominant phase to a fix.
