package azure

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/luka/fileflow/internal/storage/storagetest"
)

// Integration test against Azurite (docker compose up azurite):
//
//	FILEFLOW_TEST_AZURE_CONN='DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;' \
//	  go test ./internal/storage/azure/
func TestConformance(t *testing.T) {
	cs := os.Getenv("FILEFLOW_TEST_AZURE_CONN")
	if cs == "" {
		t.Skip("FILEFLOW_TEST_AZURE_CONN not set")
	}
	b, err := New(context.Background(), Config{
		ConnectionString: cs,
		Container:        "fileflow-test",
		BlockSize:        2 << 20,
		Concurrency:      3,
		CreateContainer:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, b, 7<<20)
}

func TestBlockIDsAreFixedLength(t *testing.T) {
	a, b := rand.Text(), rand.Text()
	if len(blockID(a, 1)) != len(blockID(a, 49999)) || len(blockID(a, 1)) != len(blockID(b, 1)) {
		t.Fatal("Azure requires equal-length block IDs within a blob")
	}
	if blockID(a, 1) == blockID(b, 1) {
		t.Fatal("concurrent uploads to one key must not share block IDs")
	}
}
