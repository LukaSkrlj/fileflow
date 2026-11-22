// Package storagetest is a conformance suite every storage.Backend must pass.
// Run it against real services by setting the env vars documented in each
// backend's _test.go (MinIO / Azurite from docker-compose work fine).
package storagetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/luka/fileflow/internal/storage"
)

// Run executes the suite. bigSize should exceed the backend's part size so
// the multipart path is exercised.
func Run(t *testing.T, b storage.Backend, bigSize int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	prefix := fmt.Sprintf("conformance/%d/", time.Now().UnixNano())

	t.Run("RoundTripSmall", func(t *testing.T) {
		key := prefix + "small.txt"
		data := []byte("hello fileflow")
		mustPut(t, ctx, b, key, data, storage.PutOptions{ContentType: "text/plain", Size: int64(len(data))})
		info, err := b.Stat(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size != int64(len(data)) || info.ETag == "" {
			t.Fatalf("bad stat: %+v", info)
		}
		if got := mustGet(t, ctx, b, key, nil); !bytes.Equal(got, data) {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("MultipartAndRanges", func(t *testing.T) {
		key := prefix + "big.bin"
		data := randBytes(bigSize)
		// Size -1 simulates a chunked upload with unknown length.
		mustPut(t, ctx, b, key, data, storage.PutOptions{Size: -1})
		if got := mustGet(t, ctx, b, key, nil); !bytes.Equal(got, data) {
			t.Fatalf("full read mismatch (len %d vs %d)", len(got), len(data))
		}
		for _, r := range []storage.Range{
			{Start: 0, Length: 1},
			{Start: int64(bigSize/2 - 7), Length: 1000},
			{Start: int64(bigSize - 10), Length: 10},
		} {
			got := mustGet(t, ctx, b, key, &r)
			if !bytes.Equal(got, data[r.Start:r.Start+r.Length]) {
				t.Fatalf("range %+v mismatch", r)
			}
		}
		_, info, err := b.Get(ctx, key, &storage.Range{Start: 0, Length: 1})
		if err != nil || info.Size != int64(bigSize) {
			t.Fatalf("ranged Get must report full object size, got %d err %v", info.Size, err)
		}
	})

	t.Run("BeforeCommitAbortHidesObject", func(t *testing.T) {
		key := prefix + "rejected.bin"
		_, err := b.Put(ctx, key, bytes.NewReader(randBytes(bigSize)), storage.PutOptions{
			Size:         int64(bigSize),
			BeforeCommit: func() (map[string]string, error) { return nil, errors.New("checksum mismatch") },
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if _, err := b.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("aborted object is visible: %v", err)
		}
	})

	t.Run("ShortUploadRejected", func(t *testing.T) {
		key := prefix + "short.bin"
		_, err := b.Put(ctx, key, bytes.NewReader([]byte("abc")), storage.PutOptions{Size: 10})
		if err == nil {
			t.Fatal("expected error for short body")
		}
		if _, err := b.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("short object is visible: %v", err)
		}
	})

	t.Run("CommitMetadata", func(t *testing.T) {
		key := prefix + "meta.txt"
		_, err := b.Put(ctx, key, bytes.NewReader([]byte("x")), storage.PutOptions{
			Size:         1,
			Metadata:     map[string]string{"client-kind": "agent"},
			BeforeCommit: func() (map[string]string, error) { return map[string]string{"sha256": "abc"}, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		info, err := b.Stat(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if info.Metadata["client-kind"] != "agent" || info.SHA256 != "abc" {
			t.Fatalf("metadata not persisted: %+v", info.Metadata)
		}
	})

	t.Run("ListAndDelete", func(t *testing.T) {
		lp := prefix + "list/"
		for i := 0; i < 3; i++ {
			mustPut(t, ctx, b, fmt.Sprintf("%sf%d", lp, i), []byte{byte(i)}, storage.PutOptions{Size: 1})
		}
		items, err := b.List(ctx, lp, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 {
			t.Fatalf("want 3 items, got %d", len(items))
		}
		if items, _ := b.List(ctx, lp, 2); len(items) != 2 {
			t.Fatalf("limit not applied: %d", len(items))
		}
		if err := b.Delete(ctx, lp+"f0"); err != nil {
			t.Fatal(err)
		}
		if err := b.Delete(ctx, lp+"f0"); err != nil {
			t.Fatalf("second delete should be a no-op: %v", err)
		}
		if _, _, err := b.Get(ctx, lp+"f0", nil); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("InvalidKeys", func(t *testing.T) {
		for _, k := range []string{"", "../etc/passwd", "a//b", "/abs", "a/./b", "trail/"} {
			if _, err := b.Stat(ctx, k); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("key %q: want ErrInvalidKey, got %v", k, err)
			}
		}
	})
}

func mustPut(t *testing.T, ctx context.Context, b storage.Backend, key string, data []byte, o storage.PutOptions) {
	t.Helper()
	if _, err := b.Put(ctx, key, bytes.NewReader(data), o); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func mustGet(t *testing.T, ctx context.Context, b storage.Backend, key string, r *storage.Range) []byte {
	t.Helper()
	rc, _, err := b.Get(ctx, key, r)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
