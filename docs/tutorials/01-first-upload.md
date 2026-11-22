# Tutorial: your first upload and download

In this tutorial you will run fileflow on your machine, store a file in it, download the file again, fetch part of it, and read the log line the server wrote about your request. It takes about ten minutes.

You need **Go 1.24 or newer**, `make`, and `curl`.

## 1. Build and start the server

From the repository root:

```bash
go mod tidy
make run
```

`make run` builds both binaries into `bin/` and starts the server with the local-disk backend. Files are stored under `./data`, and logs are in readable text format. You should see a line like:

```
level=INFO msg="fileflow started" addr=:8080 admin=:9090 backend=local part_size=16777216 part_concurrency=4 h2c=false
```

Leave this terminal open. Open a second terminal for the next steps.

## 2. Check that it is up

```bash
curl localhost:8080/healthz
curl localhost:8080/readyz
```

The first prints `ok`, the second prints `ready`. Readiness also checks that the storage backend answers.

## 3. Upload a file

Create a small file and upload it with `PUT`. The path after `/v1/files/` is the object's **key**:

```bash
echo "hello from my first upload" > hello.txt
curl -T hello.txt -H "X-Client-Kind: human" localhost:8080/v1/files/users/me/hello.txt
```

The server answers `201 Created` with JSON describing the object:

```json
{"key":"users/me/hello.txt","size":27,"etag":"...","sha256":"...","mod_time":"...","metadata":{...}}
```

Notice the `sha256` field. The server hashed your file while it streamed through.

## 4. See it on disk

```bash
ls data/users/me/
cat data/.ff-meta/users/me/hello.txt.json
```

The bytes are stored in a plain file. The metadata (content type, checksum, the client kind, and the trace id of the upload) lives in a sidecar JSON file.

## 5. Download it

```bash
curl localhost:8080/v1/files/users/me/hello.txt
```

You get your text back. Now look at the headers:

```bash
curl -I localhost:8080/v1/files/users/me/hello.txt
```

You will see `ETag`, `X-Checksum-SHA256`, `Accept-Ranges: bytes`, `X-Trace-Id` and `Server-Timing`, among others.

## 6. Download only part of it

Ask for the first five bytes:

```bash
curl -H "Range: bytes=0-4" localhost:8080/v1/files/users/me/hello.txt
```

The output is `hello` and the status is `206 Partial Content`. This is what lets download managers resume and split large files.

## 7. List and delete

```bash
curl "localhost:8080/v1/files?prefix=users/me/"
curl -X DELETE -i localhost:8080/v1/files/users/me/hello.txt
curl -i localhost:8080/v1/files/users/me/hello.txt
```

The listing shows your object, the delete returns `204`, and the last request returns `404` with a JSON body like `{"error":"not_found","trace_id":"..."}`.

## 8. Read the log line

Switch to the server terminal. Every request produced one `http_request` line. Find the one for your `GET`:

```
level=INFO msg=http_request method=GET route="GET /v1/files/{key...}" status=200 duration_ms=0.41 ...
  client=human timings_ms.backend_stat=0.05 timings_ms.backend_open=0.03 timings_ms.backend_read=0.01 timings_ms.client_write=0.02 trace_id=...
```

The `timings_ms` fields show how the request's time split between the storage backend and the network to you. The second tutorial builds on this.

## What you did

You started fileflow, uploaded a file, fetched all of it and part of it, listed and deleted it, and found the log line for a single request.

**Next:** [Find out why a request is slow](02-find-a-slow-request.md).
