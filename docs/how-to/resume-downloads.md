# How to resume or split a download with Range requests

fileflow supports single byte ranges on `GET` and `HEAD`.

## Resume an interrupted download

Use `If-Range` with the ETag you got the first time. If the object changed in the meantime, the server sends the whole new file instead of a range from a different version.

```bash
# first attempt (interrupted)
curl -D first.hdr -o file.bin localhost:8080/v1/files/agents/a1/model.bin
ETAG=$(grep -i '^etag:' first.hdr | cut -d' ' -f2 | tr -d '\r')

# resume
curl -C - -H "If-Range: $ETAG" -o file.bin localhost:8080/v1/files/agents/a1/model.bin
```

`curl -C -` works out the offset from the partial file and sends `Range: bytes=<size>-`.

- `206` means you got the rest.
- `200` means the object changed and you got the whole thing. curl overwrites the file.

## Download in parallel chunks

Find the size, then fetch ranges at the same time:

```bash
URL=localhost:8080/v1/files/agents/a1/model.bin
SIZE=$(curl -sI $URL | grep -i content-length | tr -dc 0-9)
HALF=$((SIZE / 2))
curl -s -H "Range: bytes=0-$((HALF-1))" -o part0 $URL &
curl -s -H "Range: bytes=$HALF-"        -o part1 $URL &
wait
cat part0 part1 > model.bin
```

Tools such as `aria2c -x 8` do the same thing automatically.

## Supported range forms

| Header | Returns |
|---|---|
| `bytes=0-99` | first 100 bytes |
| `bytes=100-` | from byte 100 to the end |
| `bytes=-100` | last 100 bytes |
| `bytes=0-9,20-29` (several ranges) | the whole object, `200` |
| start past the end, or malformed | `416` with `Content-Range: bytes */<size>` |

Full details are in the [HTTP API reference](../reference/http-api.md#range-requests).
