package local

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/luka/fileflow/internal/storage"
	"github.com/luka/fileflow/internal/storage/storagetest"
)

// Data replaced without its sidecar (racing upload, crash between renames)
// must not be served with the old checksum or ETag.
func TestStaleSidecarIgnored(t *testing.T) {
	ctx := context.Background()
	b, err := New(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("upload A")
	if _, err := b.Put(ctx, "k", bytes.NewReader(data), storage.PutOptions{
		Size: int64(len(data)), Metadata: map[string]string{"sha256": "aaa"},
	}); err != nil {
		t.Fatal(err)
	}
	if info, _ := b.Stat(ctx, "k"); info.SHA256 != "aaa" || info.ETag != "aaa" {
		t.Fatalf("fresh sidecar not used: %+v", info)
	}
	if err := os.WriteFile(b.dataPath("k"), []byte("upload B!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, _ := b.Stat(ctx, "k"); info.SHA256 != "" || info.ETag == "aaa" {
		t.Fatalf("stale sidecar used for new bytes: %+v", info)
	}
}

func TestListRejectsPrefixOutsideRoot(t *testing.T) {
	b, err := New(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../", "../../x", "a/../../"} {
		if _, err := b.List(context.Background(), p, 10); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("prefix %q: want ErrInvalidKey, got %v", p, err)
		}
	}
	if _, err := b.List(context.Background(), "users/lu", 10); err != nil {
		t.Errorf("normal partial prefix rejected: %v", err)
	}
}

func TestConformance(t *testing.T) {
	b, err := New(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, b, 3<<20)
}
