package s3

import (
	"context"
	"os"
	"testing"

	"github.com/luka/fileflow/internal/storage/storagetest"
)

// Integration test. With docker compose up minio:
//
//	FILEFLOW_TEST_S3_ENDPOINT=http://localhost:9000 go test ./internal/storage/s3/
func TestConformance(t *testing.T) {
	ep := os.Getenv("FILEFLOW_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("FILEFLOW_TEST_S3_ENDPOINT not set")
	}
	b, err := New(context.Background(), Config{
		Endpoint:     ep,
		Region:       envOr("FILEFLOW_TEST_S3_REGION", "us-east-1"),
		Bucket:       envOr("FILEFLOW_TEST_S3_BUCKET", "fileflow-test"),
		AccessKey:    envOr("FILEFLOW_TEST_S3_ACCESS_KEY", "minioadmin"),
		SecretKey:    envOr("FILEFLOW_TEST_S3_SECRET_KEY", "minioadmin"),
		UsePathStyle: true,
		PartSize:     5 << 20,
		Concurrency:  3,
		CreateBucket: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, b, 12<<20) // > part size: exercises multipart
}

func TestTotalFromContentRange(t *testing.T) {
	cases := map[string]int64{"bytes 0-9/100": 100, "bytes 5-5/6": 6}
	for in, want := range cases {
		if got, ok := totalFromContentRange(in); !ok || got != want {
			t.Errorf("%q: got %d,%v", in, got, ok)
		}
	}
	if _, ok := totalFromContentRange("bytes */*"); ok {
		t.Error("unknown total must not parse")
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
