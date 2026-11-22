# Streaming uploads, bounded memory and atomic commits

AI agents upload multi-gigabyte files: model weights, datasets, archives. Gateway pods are small. These two facts shape the whole upload path.

## The problem with the obvious approaches

**Buffer the whole body, then upload.** This is simple, but memory grows with file size. One 20 GB upload kills a 2 GB pod.

**Pipe the body straight to the store in one request.** Memory stays constant, but S3 needs a known length for a single `PutObject` and limits it to 5 GB. A failure near the end means starting from zero, and you can't check the bytes before they become visible.

**Use the SDK's upload manager.** This works, but the manager decides when to commit. fileflow needs to decide *after* the last byte, once it knows whether the checksum matches.

## What fileflow does

The body is read in fixed-size **parts** (16 MiB by default). Each part goes to the store as one multipart part (S3) or staged block (Azure). At most `part_concurrency` parts (default 4) are in memory or in flight at any time. A new part isn't read from the client until one of the slots is free.

So memory per upload is at most `part_size × part_concurrency`, whatever the file size: 64 MiB for a 10 MB file or a 100 GB one. Part buffers come from a pool, so concurrent uploads reuse memory instead of allocating new buffers all the time.

This also applies **backpressure**. If storage is slower than the client, the slots stay full, fileflow stops reading, and TCP slows the client down. Nothing piles up in memory.

If the first chunk holds the whole body, fileflow sends it as one ordinary request and skips the multipart overhead. Small files cost one round trip.

## Commit or abort happens last

The upload engine (`storage.UploadParts`) only moves bytes. The decision about the result is made at the end:

1. Every part is uploaded. Any part failure cancels the others.
2. If the client sent `Content-Length`, the byte count must match. A client that disconnects early can't leave a truncated object behind.
3. The `BeforeCommit` hook runs. The HTTP layer has been hashing the stream as it passed through. Here it compares the hash with `X-Checksum-SHA256`, if the client sent one.
4. Only then does the backend **commit** (`CompleteMultipartUpload`, `CommitBlockList`, or a rename on local disk).

If anything fails, the backend **aborts** instead. On S3 that is `AbortMultipartUpload`, which runs with a context that isn't tied to the request, so it still happens after the client has gone. Otherwise orphaned parts would sit in the bucket and be billed.

The result is that **readers see either the previous object or the complete new one, never a partial or corrupt one.** The conformance suite checks this for every backend (`BeforeCommitAbortHidesObject`, `ShortUploadRejected`).

## Why the hash is always computed

The server hashes the whole stream, even when the client sends no checksum. SHA-256 costs CPU, but it gives:

- an integrity value to return to the client, which it can check against its own file,
- a content identity, the basis for the content-addressed storage and deduplication planned in phase 5,
- independence from provider checksums. Newer AWS SDKs add CRC checksums that some S3-compatible stores reject, so fileflow turns those off and relies on its own hash.

If profiling shows `sha256` is the limit, the roadmap suggests skipping hashing for trusted direct uploads, not removing it everywhere.

## Limits that come from the stores

S3 allows at most 10,000 parts, and Azure 50,000 blocks. With 16 MiB parts that is about 156 GiB on S3. When the client sends `Content-Length`, fileflow picks a larger part size for that one upload if needed. With chunked uploads the size isn't known ahead of time, so the configured part size sets the ceiling.

## Downloads

Downloads are simpler: fileflow opens a stream from the store and copies it to the client through a pooled 256 KiB buffer. It times reads and writes separately, which is where the `backend_read` / `client_write` split comes from. If the copy fails half-way, the headers (including `Content-Length`) have already been sent. fileflow then **drops the connection** instead of ending the response normally, so the client sees a broken transfer rather than a short file it might take for a complete one.
