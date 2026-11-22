# fileflow documentation

fileflow is a streaming file gateway written in Go. It puts one HTTP API in front of local disk, any S3-compatible store, or Azure Blob Storage, and it records where every request spends its time.

The docs follow the [Diátaxis](https://diataxis.fr/) framework. Each page has one job. Pick the section that matches what you need right now:

| | **When you are studying** | **When you are working** |
|---|---|---|
| **Practical steps** | [Tutorials](#tutorials): learn by doing | [How-to guides](#how-to-guides): reach a specific goal |
| **Theory** | [Explanation](#explanation): understand why | [Reference](#reference): look up facts |

## Tutorials

Lessons that take you from nothing to a working result. Follow them in order the first time.

1. [Your first upload and download](tutorials/01-first-upload.md): run fileflow locally, store a file, fetch it back, and read the log line it produces.
2. [Find out why a request is slow](tutorials/02-find-a-slow-request.md): benchmark the server, take the slowest trace id, and read its timing breakdown.

## How-to guides

Recipes for a task you already have in mind.

- [Run fileflow against an S3-compatible store](how-to/use-s3-backend.md) (SeaweedFS, AWS, Hetzner, Cloudflare R2)
- [Run fileflow against Azure Blob Storage](how-to/use-azure-backend.md) (Azurite, connection string, Workload Identity)
- [Verify uploads with a SHA-256 checksum](how-to/verify-uploads.md)
- [Resume or split a download with Range requests](how-to/resume-downloads.md)
- [Trace a request across logs](how-to/trace-a-request.md)
- [Watch uploads, downloads and streaming in the browser](how-to/use-the-web-ui.md)
- [Benchmark and profile the server](how-to/benchmark-and-profile.md)
- [Run behind a load balancer or in Kubernetes](how-to/run-behind-a-load-balancer.md)
- [Tune memory and throughput for large uploads](how-to/tune-large-uploads.md)
- [Run the test suites](how-to/run-tests.md)
- [Add a new storage backend](how-to/add-a-storage-backend.md)

## Reference

Lookup material: accurate, complete, and with no opinions.

- [HTTP API](reference/http-api.md): endpoints, headers, status codes, error codes
- [Configuration](reference/configuration.md): every environment variable
- [Observability](reference/observability.md): log fields, timing phases, `Server-Timing`, Prometheus metrics
- [ffbench](reference/ffbench.md): flags and output of the load generator
- [Storage backend contract](reference/storage-backend.md): the `storage.Backend` interface, key rules, and per-backend behaviour
- [Make targets and project layout](reference/make-and-layout.md)

## Explanation

Background and reasoning behind the design.

- [Architecture](explanation/architecture.md): the moving parts and why it is one process for now
- [Streaming uploads, bounded memory and atomic commits](explanation/streaming-and-commits.md)
- [Why every request is split into timing phases](explanation/timing-split.md)
- [Timeouts, slow clients and graceful shutdown](explanation/timeouts-and-shutdown.md)
- [Choosing a storage backend](explanation/choosing-a-backend.md)

For what comes next (caching, CDN, versioning, compression, protocols), see [ROADMAP.md](../ROADMAP.md).
