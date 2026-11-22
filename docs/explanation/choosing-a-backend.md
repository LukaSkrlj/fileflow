# Choosing a storage backend

All three backends offer the same API and pass the same conformance suite, so switching is a configuration change. The differences are in operations, cost and durability.

## Local disk

Objects are plain files, with metadata in sidecar JSON files. Uploads go to a temp file and are renamed into place, so readers never see a partial file. With `FILEFLOW_LOCAL_FSYNC=true`, the data is flushed to disk before the rename.

Good for development, tests and single-node deployments on a persistent volume. It's the fastest backend because there's no network between the gateway and the bytes.

The problem is that the gateway is no longer stateless. Running several replicas needs a shared filesystem, which brings its own consistency and performance issues. Durability is whatever the disk provides.

## S3-compatible

One backend covers AWS S3, Hetzner Object Storage, Cloudflare R2, SeaweedFS, Garage, RustFS and others. The S3 API is the common standard for object storage.

Things to know:

- **Providers vary.** Newer AWS SDKs add CRC checksums by default, and some compatible stores reject them. fileflow turns that off and relies on its own SHA-256.
- **Metadata is fixed when a multipart upload starts.** A checksum computed during the upload can't be added afterwards. That's why sending `X-Checksum-SHA256` up front matters most on S3.
- **Cost differs widely.** Hetzner is cheap per TB and doesn't charge for ingress or API calls. R2 has no egress fees. AWS charges for requests and egress but has the most regions and integrations.
- **MinIO** community edition stopped publishing images in October 2025 and is in maintenance mode. That's why the compose stack uses SeaweedFS.

## Azure Blob

The natural choice when running on AKS. It supports Workload Identity, so there are no keys to manage, and storage accounts can be zone- or geo-redundant (ZRS, GZRS). A Private Endpoint keeps traffic off the public internet.

Azure is stricter about metadata names (they must be valid C# identifiers), which the backend handles by translating `-` to `_`. It also allows 50,000 blocks per blob instead of S3's 10,000 parts, so the ceiling for streamed uploads of unknown size is higher.

## A rule of thumb

- Building or testing: **local**.
- Running on Azure: **azure**, with Workload Identity and ZRS.
- Running anywhere else, or wanting a cheaper self-hosted or European store: **s3**.

Whatever you choose, keep the gateway and the storage in the same region. The timing split often shows that the "slow storage" is really the distance to it.
