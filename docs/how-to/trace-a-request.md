# How to trace a request across logs

Every response includes a trace id. Use it to find every log line for that request.

## Get the trace id

From any response:

```bash
curl -si localhost:8080/v1/files/users/me/missing.txt | grep -i -e x-trace-id -e traceparent
```

From an error body:

```json
{"error":"not_found","trace_id":"ec0013484464c0fa13df059154451537"}
```

If a user reports a failed request, ask them for this one string.

From a stored object: the trace id of the request that wrote it is saved in the object's metadata as `trace-id`. On local disk, look in `data/.ff-meta/<key>.json`.

## Join an existing trace

If your client or upstream service already has a W3C trace context, send it:

```bash
curl -H "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" \
  localhost:8080/v1/files/users/me/hello.txt
```

fileflow creates a child span in the same trace, and its logs use your trace id. A missing or invalid header starts a new trace.

## Find the log lines

JSON logs (the default):

```bash
grep '"trace_id":"ec0013484464c0fa13df059154451537"' fileflow.log
# or
jq 'select(.trace_id=="ec0013484464c0fa13df059154451537")' fileflow.log
```

Azure Log Analytics (container insights):

```kusto
ContainerLogV2
| where LogMessage.trace_id == "ec0013484464c0fa13df059154451537"
```

You will find the `http_request` access log line and any `request failed` or `download interrupted` lines that were logged during the request.

## Split by client type

Send `X-Client-Kind: human` or `X-Client-Kind: agent`. The value shows up as `client` in logs and as a label on the request and byte metrics. Any other value is recorded as `unknown`.

See [observability reference](../reference/observability.md) for every log field.
