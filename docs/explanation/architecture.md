# Architecture

fileflow sits between clients and object storage. Clients are people using browsers and scripts, and AI agents moving large artifacts. Its job is to give both groups one HTTP API, no matter which storage holds the bytes, and to make it obvious where time goes when transfers are slow.

```
 humans ─┐                       ┌─────────────── fileflow (stateless, N replicas) ──────────────┐
         ├─ HTTPS ─▶ LB / CDN ─▶ │ obs middleware → httpapi → storage.Backend (local | s3 | azure)│ ─▶ object store
 agents ─┘                       └───────────────── :9090 /metrics /debug/pprof ─────────────────┘
```

## Three layers

**`obs` middleware** wraps every request. It sets up the trace context and starts a per-request `Timings` collector. It also sets idle-progress deadlines on the connection, counts bytes, and writes the access log line and the metrics once the request finishes. The handlers don't need to know about any of this.

**`httpapi`** turns HTTP semantics (ranges, conditional requests, checksums, error codes) into calls on a storage backend. It owns everything that is about HTTP and nothing that is about a particular store.

**`storage.Backend`** is a five-method interface: `Put`, `Get`, `Stat`, `Delete` and `List`. Each store (local disk, S3, Azure) implements it, and every implementation must pass the same conformance suite. The HTTP layer wraps each backend with `storage.Instrument`, so all backends get the same timing phases and metrics without each one adding its own.

The direction of dependencies is strict: `httpapi` depends on `storage`, never on `s3` or `azure`. Only `cmd/fileflow` knows the concrete backends, and it picks one from configuration.

## Why the gateway is stateless

A fileflow process keeps nothing between requests. Objects live in the backend, and metadata either travels with the object (S3/Azure user metadata) or sits in a sidecar file next to it (local disk). This means:

- any replica can serve any request, so scaling out is just adding replicas,
- a replica can be killed at any time after it drains,
- durability and replication are the storage provider's job, which it already does well.

The local backend is the exception. It is only stateless if its root directory is on shared or persistent storage. It exists for development, tests and single-node setups.

## Why one process, for now

The longer-term design has two services: a **metadata service** (paths, owners, versions, in Postgres) and a **data service** (blob bytes, content-addressed). Today both jobs run in one process.

Splitting early would add a network hop, a second deployment and new ways to fail. It wouldn't make anything faster, and until there is versioning the metadata service would have nothing to do. The code is already divided where the split will happen: handlers on one side, `storage.Backend` on the other. Trace propagation across services (`obs.Inject`) is already in place. When versioning arrives (roadmap phase 5), the split has a real reason to exist.

## Why a separate admin port

Metrics and pprof are on `:9090`, apart from the API on `:8080`. They are for operators, not users. pprof can expose internals and cost a lot of CPU. A separate port can be kept inside the cluster with a simple network rule, without path-based filtering at the ingress.

## What's deliberately missing

- **Authentication and authorization.** These must come before any public exposure (phase 2).
- **Caching.** The `Stat` before every `GET` is the first thing a metadata cache would remove (phase 3).
- **Versioning.** Overwrites replace objects in place. A read that overlaps an overwrite is detected and logged, but not prevented (phase 5).

See [ROADMAP.md](../../ROADMAP.md) for the order and reasoning.
