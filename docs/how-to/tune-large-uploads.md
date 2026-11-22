# How to tune memory and throughput for large uploads

Two settings control the upload path on the S3 and Azure backends:

| Variable | Default | Effect |
|---|---|---|
| `FILEFLOW_PART_SIZE` | `16MiB` | Size of each multipart part (S3) or block (Azure) |
| `FILEFLOW_PART_CONCURRENCY` | `4` | Parts uploaded in parallel per upload |

## Work out memory first

Each upload can use up to:

```
part_size × part_concurrency        (16 MiB × 4 = 64 MiB by default)
```

Multiply by the number of uploads you expect at once:

```
100 concurrent uploads × 64 MiB ≈ 6.4 GiB
```

If that's more than your pod's memory, lower one of the two settings or limit concurrent uploads at the load balancer.

## Uploads are slow and `backend_put` dominates

The storage write path is the limit. Try, in this order:

1. **More concurrency:** `FILEFLOW_PART_CONCURRENCY=8`. This helps most when each part's round-trip is slow (storage in another region).
2. **Bigger parts:** `FILEFLOW_PART_SIZE=32MiB` or `64MiB`. This means fewer requests and less per-request overhead.
3. **Move storage closer:** same region, Private Endpoint.

Measure each change with `make bench-put` (see [benchmark and profile](benchmark-and-profile.md)).

## Uploads are slow and `client_read` dominates

The uploader's network is the limit, not fileflow. Tuning these settings won't help.

## Very large files

You don't need to do anything for huge files with a known `Content-Length`. If the file would need more than 10,000 parts on S3 or 50,000 blocks on Azure, fileflow uses larger parts for that upload automatically.

Uploads with unknown size (chunked transfer encoding) can't be adjusted in advance. With 16 MiB parts, S3 caps them at about 156 GiB (10,000 parts) and Azure at about 781 GiB (50,000 blocks). The default `FILEFLOW_MAX_UPLOAD` of 100 GiB is below both. If you raise it, raise `FILEFLOW_PART_SIZE` too.

## Size limits

- `FILEFLOW_MAX_UPLOAD` (default `100GiB`) rejects larger uploads with `413`.
- S3 requires parts of at least 5 MiB. A smaller `FILEFLOW_PART_SIZE` is replaced with 16 MiB.
- Azure requires blocks of at least 1 MiB. A smaller value is replaced with 16 MiB.

## Local disk

The local backend writes directly to a temp file and ignores part settings. Set `FILEFLOW_LOCAL_FSYNC=false` to skip the flush before commit. Uploads get faster, but a power loss can lose recently written files. Only do this for scratch data.
