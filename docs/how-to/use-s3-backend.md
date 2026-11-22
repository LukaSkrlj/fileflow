# How to run fileflow against an S3-compatible store

Set `FILEFLOW_BACKEND=s3` and point the `S3_*` variables at your store. The full list of variables is in the [configuration reference](../reference/configuration.md#s3-backend).

## Local SeaweedFS (docker compose)

```bash
docker compose up --build
```

This starts fileflow with the S3 backend, plus SeaweedFS on `:8333`. Compose sets the endpoint, the dev credentials `dev` / `devsecret`, path-style addressing, and automatic bucket creation.

## AWS S3

```bash
export FILEFLOW_BACKEND=s3
export S3_REGION=eu-central-1
export S3_BUCKET=my-fileflow-bucket
# Leave S3_ACCESS_KEY / S3_SECRET_KEY empty to use the default AWS credential
# chain (env vars, shared config, IRSA, instance role).
./bin/fileflow
```

Leave `S3_ENDPOINT` empty for AWS.

## Hetzner Object Storage

```bash
export FILEFLOW_BACKEND=s3
export S3_ENDPOINT=https://fsn1.your-objectstorage.com
export S3_REGION=fsn1
export S3_BUCKET=my-bucket
export S3_ACCESS_KEY=...
export S3_SECRET_KEY=...
./bin/fileflow
```

## Cloudflare R2

```bash
export FILEFLOW_BACKEND=s3
export S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
export S3_REGION=auto
export S3_BUCKET=my-bucket
export S3_ACCESS_KEY=...
export S3_SECRET_KEY=...
./bin/fileflow
```

## Other self-hosted stores (SeaweedFS, Garage, RustFS)

Most self-hosted stores need path-style URLs:

```bash
export S3_PATH_STYLE=true
```

Set `S3_CREATE_BUCKET=true` if you want fileflow to create the bucket at startup. Leave it off in production and create the bucket yourself.

## Check that it works

```bash
curl localhost:8080/readyz     # "ready" means the bucket is reachable
curl -T ./somefile localhost:8080/v1/files/test/somefile
```

If `/readyz` returns `503` with `backend_unavailable`, the server log line has the underlying SDK error.

## Troubleshooting

- **Provider rejects requests with a checksum error.** fileflow already sets the AWS SDK to send CRC checksums only when required, because some S3-compatible providers reject them. If you see this anyway, check that the provider supports `UploadPart` and `CompleteMultipartUpload`.
- **`503 SlowDown` in `backend_error` logs.** The provider is throttling you. See [benchmark and profile](benchmark-and-profile.md).
- **Abandoned multipart parts in the bucket.** fileflow aborts failed uploads, but a crashed process cannot. Add a bucket lifecycle rule that deletes incomplete multipart uploads after a day or so.
