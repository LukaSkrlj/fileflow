# How to add a new storage backend

This guide adds a backend called `example`. Replace the name with yours, for example `gcs`.

## 1. Create the package

Create `internal/storage/example/example.go` with a type that implements `storage.Backend`:

```go
package example

type Backend struct{ /* client, bucket, pool ... */ }

func (b *Backend) Name() string { return "example" }
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, o storage.PutOptions) (storage.ObjectInfo, error)
func (b *Backend) Get(ctx context.Context, key string, rng *storage.Range) (io.ReadCloser, storage.ObjectInfo, error)
func (b *Backend) Stat(ctx context.Context, key string) (storage.ObjectInfo, error)
func (b *Backend) Delete(ctx context.Context, key string) error
func (b *Backend) List(ctx context.Context, prefix string, limit int) ([]storage.ObjectInfo, error)
```

The [storage backend contract](../reference/storage-backend.md) lists the rules each method must follow.

## 2. Implement `Put` with bounded memory

Don't read the whole body into memory. Use the shared helpers, the way `s3` and `azure` do:

```go
first := pool.Get()
n, eof, err := storage.ReadChunk(r, *first)
if eof {
    // small object: one request
}
total, err := storage.UploadParts(ctx, r, first, n, concurrency, pool, maxParts,
    func(ctx context.Context, num int, data []byte) error {
        // upload part `num`; data is only valid during this call
    })
```

Then, in this order:

1. If `opts.Size >= 0` and `total != opts.Size`, fail (short upload).
2. Call `opts.BeforeCommit()` if it is set. If it returns an error, **abort** and return the error.
3. Commit, merging any metadata `BeforeCommit` returned if your store allows that at commit time.

If anything fails, abort the multipart upload with a context that isn't cancelled (`context.WithoutCancel`), so a disconnected client doesn't leave parts behind.

## 3. Map errors

Return `storage.ErrNotFound` for missing keys and `storage.ErrInvalidRange` for ranges the store rejects. Wrap them with `%w` so `errors.Is` works. The HTTP layer turns these into `404` and `416`.

Call `storage.ValidateKey(key)` at the start of `Put`.

## 4. Add a conformance test

Create `internal/storage/example/example_test.go`:

```go
func TestConformance(t *testing.T) {
    ep := os.Getenv("FILEFLOW_TEST_EXAMPLE_ENDPOINT")
    if ep == "" {
        t.Skip("FILEFLOW_TEST_EXAMPLE_ENDPOINT not set")
    }
    b, err := New(context.Background(), Config{ /* small part size, e.g. 2 MiB */ })
    if err != nil {
        t.Fatal(err)
    }
    storagetest.Run(t, b, 7<<20) // bigSize > part size, so multipart is exercised
}
```

Run it until all subtests pass. They cover round trips, multipart with ranges, aborted commits, short uploads, commit metadata, listing and deletion, and invalid keys.

## 5. Wire it into the server

1. Add the config fields and environment variables in `internal/config/config.go`, and add `"example"` to the backend `switch` in `Load`.
2. Add a `case "example":` to `newBackend` in `cmd/fileflow/main.go`.
3. If there is an emulator, add it to `docker-compose.yml` and `make test-integration`.
4. Document the new variables in [configuration reference](../reference/configuration.md).

You don't need to add instrumentation: `httpapi.New` wraps every backend with `storage.Instrument`, so timings and metrics work automatically.
