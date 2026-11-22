# How to verify uploads with a SHA-256 checksum

Send the expected hash in `X-Checksum-SHA256`. If the bytes the server receives don't match, the upload is aborted and no object is created.

## Upload with a checksum

```bash
SUM=$(shasum -a 256 bigfile.bin | cut -d' ' -f1)     # Linux: sha256sum
curl -T bigfile.bin \
  -H "X-Checksum-SHA256: $SUM" \
  localhost:8080/v1/files/agents/a1/bigfile.bin
```

| Response | Meaning |
|---|---|
| `201` | The bytes matched. The response body and `X-Checksum-SHA256` header carry the hash. |
| `422` `checksum_mismatch` | The bytes didn't match. No object was created, and any previous version of the key is unchanged. |
| `400` `invalid_checksum_header` | The header is not 64 hex characters. |

The header value is case-insensitive.

## Upload without a checksum and check afterwards

If you don't send the header, the server still hashes the stream and returns the result:

```bash
curl -sT bigfile.bin localhost:8080/v1/files/agents/a1/bigfile.bin | jq -r .sha256
```

Compare that value with your local hash.

## Verify a download

```bash
curl -sD headers.txt -o out.bin localhost:8080/v1/files/agents/a1/bigfile.bin
grep -i x-checksum-sha256 headers.txt
shasum -a 256 out.bin
```

> **On the S3 backend, send the header if you want the hash stored.** For multipart uploads to S3 (anything larger than one part, 16 MiB by default), metadata is fixed when the upload starts. A hash you send up front is stored with the object. A hash the server only computes at the end is returned in the `PUT` response but not stored, so later `GET`s won't include `X-Checksum-SHA256`. Local disk and Azure always store it. See [storage backend contract](../reference/storage-backend.md#per-backend-behaviour).
