# Timeouts, slow clients and graceful shutdown

## The standard timeouts don't fit file transfers

Go's `http.Server` has `ReadTimeout` and `WriteTimeout`. They limit the **total** time for reading a request or writing a response. For an API that returns JSON, 30 seconds is plenty. For a file gateway, every value is wrong:

- Set it low, and a healthy 50 GB download over a fast but long link gets killed half-way.
- Set it high or turn it off, and a client that opens a connection and stops reading keeps a goroutine, a buffer and a storage stream open for as long as it likes. A few thousand of those can exhaust a pod.

The question that matters isn't "has this taken too long?" but "**is it still moving?**"

## Idle-progress deadlines

fileflow leaves `ReadTimeout` and `WriteTimeout` unset and uses connection deadlines that move forward while data flows. Each successful read from the request body or write to the response pushes the deadline `FILEFLOW_IDLE_IO_TIMEOUT` (60 s) into the future. A transfer that keeps moving, however slowly, never hits it. A connection that makes no progress for 60 seconds is closed.

Setting a deadline on every read or write would cost a little on each one, so it's moved at most once per second. That's accurate enough at this timescale and costs almost nothing.

Fixed limits that don't depend on transfer size are still there: headers must arrive within 10 seconds (which protects against slow-header attacks), and idle keep-alive connections are closed after 120 seconds.

## Graceful shutdown

When Kubernetes replaces a pod, it sends `SIGTERM` and removes the pod from load balancer endpoints, but these happen **at the same time**, not in order. A server that stops accepting connections as soon as `SIGTERM` arrives refuses requests that the load balancer is still sending to it.

fileflow shuts down in three steps:

1. **Fail readiness.** `/readyz` starts returning `503`. Nothing else changes, and requests are still served.
2. **Wait** `FILEFLOW_DRAIN_DELAY` (5 s). This gives the readiness probe and load balancer time to notice and stop sending new traffic.
3. **Shut down.** Stop accepting new connections and wait up to `FILEFLOW_SHUTDOWN_TIMEOUT` (30 s) for in-flight transfers to finish.

A rolling deploy then causes no failed requests, except for transfers that take longer than the shutdown timeout. For those, the right fix is resumable transfers (Range downloads now, resumable multipart uploads later), not ever-longer grace periods.

## Why readiness checks the backend

`/readyz` does a `Stat` on a probe key with a 2-second limit. A "not found" result counts as healthy, because it shows storage answered. A replica that can't reach storage would fail every file request, so it's better for the load balancer to send traffic elsewhere. `/healthz`, used for liveness, doesn't check the backend. A storage outage shouldn't make Kubernetes restart every pod, since restarts can't fix storage.

## Client disconnects

When a client goes away mid-request, the request context is cancelled. Uploads abort their multipart upload, and the response is recorded as `499 client_closed`, the nginx convention, so it doesn't show up as a server error in dashboards. The status never reaches the client, because the client is gone. It exists for logs and metrics.
