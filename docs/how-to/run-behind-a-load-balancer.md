# How to run behind a load balancer or in Kubernetes

## Ports

| Port | Serve to | Contents |
|---|---|---|
| `:8080` (`FILEFLOW_LISTEN_ADDR`) | the load balancer | the file API, `/healthz`, `/readyz` |
| `:9090` (`FILEFLOW_ADMIN_ADDR`) | **inside the cluster only** | `/metrics`, `/debug/pprof/` |

Never expose the admin port publicly. pprof shows internals and can use a lot of CPU.

## Health probes

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 8080 }
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 2
```

`/readyz` returns `503` if the storage backend doesn't answer within 2 seconds, and also while the pod is draining.

## Graceful shutdown

On `SIGTERM` fileflow:

1. makes `/readyz` return `503`,
2. waits `FILEFLOW_DRAIN_DELAY` (5 s) so the load balancer notices,
3. stops accepting connections and waits up to `FILEFLOW_SHUTDOWN_TIMEOUT` (30 s) for in-flight transfers.

Set the pod's `terminationGracePeriodSeconds` higher than drain delay plus shutdown timeout, for example `45`. Make the drain delay longer than the readiness probe period multiplied by its failure threshold.

## Configure the ingress for large files

Many ingress defaults break large transfers. Change them so that:

- **request buffering is off**, so uploads stream through instead of being written to the proxy's disk first,
- **the body size limit** is at least `FILEFLOW_MAX_UPLOAD`,
- **read and send timeouts** are long, or based on idle time, not total time.

For ingress-nginx:

```yaml
nginx.ingress.kubernetes.io/proxy-body-size: "0"
nginx.ingress.kubernetes.io/proxy-request-buffering: "off"
nginx.ingress.kubernetes.io/proxy-buffering: "off"
nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
```

fileflow cuts off stalled clients itself with `FILEFLOW_IDLE_IO_TIMEOUT`, so long proxy timeouts are safe.

## Use HTTP/2 between the load balancer and pods

If the load balancer terminates TLS and can speak cleartext HTTP/2 to the backend:

```bash
FILEFLOW_H2C=true
```

HTTP/1.1 keeps working on the same port.

## High availability

- Run at least 3 replicas, spread across zones with `topologySpreadConstraints`.
- Add a PodDisruptionBudget with `minAvailable: 2`.
- Autoscale on CPU and on `fileflow_http_inflight_requests`.
- fileflow is stateless, so any replica can serve any request. Durability comes from the storage backend.

> fileflow has **no authentication yet**. Don't expose it to the internet until auth is in place (see [ROADMAP.md](../../ROADMAP.md), phase 2).
