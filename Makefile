.PHONY: build test test-integration run bench-put bench-get bench-range profile lint

build:
	go build -o bin/ ./cmd/...

test:
	go test -race ./...

# Needs the docker compose stack (SeaweedFS on :8333, Azurite on :10000).
test-integration:
	FILEFLOW_TEST_S3_ENDPOINT=http://localhost:8333 FILEFLOW_TEST_S3_ACCESS_KEY=dev FILEFLOW_TEST_S3_SECRET_KEY=devsecret \
	FILEFLOW_TEST_AZURE_CONN='DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://localhost:10000/devstoreaccount1;' \
	go test -race -count=1 ./internal/storage/...

run: build
	FILEFLOW_LOG_FORMAT=text FILEFLOW_LOCAL_ROOT=./data ./bin/fileflow

bench-put: build
	./bin/ffbench -op put -n 50 -c 8 -size 64MiB -kind agent

bench-get: build
	./bin/ffbench -op get -n 500 -c 32 -size 1MiB -keys 50 -kind human

bench-range: build
	./bin/ffbench -op get -n 200 -c 16 -size 256MiB -keys 4 -range 8MiB -kind agent

# 30s CPU profile while a benchmark runs in another terminal.
profile:
	go tool pprof -http=:7070 http://localhost:9090/debug/pprof/profile?seconds=30

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"
