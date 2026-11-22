# How to run the test suites

## Unit tests and local-disk conformance

```bash
make test
```

This runs `go test -race ./...`. The S3 and Azure conformance tests are skipped because their environment variables aren't set.

## Integration tests against S3 and Azure emulators

Start the emulators, then run the suite:

```bash
docker compose up -d seaweedfs azurite
make test-integration
```

`make test-integration` points the tests at SeaweedFS on `:8333` and Azurite on `:10000`, and runs `./internal/storage/...` with `-count=1` so nothing is cached.

## Against a real S3 bucket or Azure account

Set the variables yourself:

```bash
# S3
FILEFLOW_TEST_S3_ENDPOINT=https://fsn1.your-objectstorage.com \
FILEFLOW_TEST_S3_REGION=fsn1 \
FILEFLOW_TEST_S3_BUCKET=fileflow-test \
FILEFLOW_TEST_S3_ACCESS_KEY=... \
FILEFLOW_TEST_S3_SECRET_KEY=... \
go test -race -count=1 ./internal/storage/s3/

# Azure
FILEFLOW_TEST_AZURE_CONN='DefaultEndpointsProtocol=https;AccountName=...;AccountKey=...' \
go test -race -count=1 ./internal/storage/azure/
```

The S3 test always uses path-style addressing and creates its bucket. The Azure test uses the container `fileflow-test` and creates it.

## Lint

```bash
make lint      # go vet + gofmt check
```
