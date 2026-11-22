package storage

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func runParts(t *testing.T, data []byte, partSize, conc int, fail int) (map[int][]byte, int64, error, int32) {
	t.Helper()
	pool := NewBufPool(partSize)
	r := bytes.NewReader(data)
	first := pool.Get()
	n, _, err := ReadChunk(r, *first)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	got := map[int][]byte{}
	var inflight, peak int32
	total, err := UploadParts(context.Background(), r, first, n, conc, pool, 10000,
		func(ctx context.Context, num int, d []byte) error {
			cur := atomic.AddInt32(&inflight, 1)
			defer atomic.AddInt32(&inflight, -1)
			for {
				p := atomic.LoadInt32(&peak)
				if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
					break
				}
			}
			if num == fail {
				return errors.New("boom")
			}
			mu.Lock()
			got[num] = append([]byte(nil), d...)
			mu.Unlock()
			return nil
		})
	return got, total, err, peak
}

func TestUploadPartsReassembles(t *testing.T) {
	for _, size := range []int{100, 1000, 1024, 1025, 4096} { // includes exact boundaries
		data := bytes.Repeat([]byte("0123456789abcdef"), size/16+1)[:size]
		got, total, err, peak := runParts(t, data, 256, 3, -1)
		if err != nil {
			t.Fatal(err)
		}
		if total != int64(size) {
			t.Fatalf("size %d: total %d", size, total)
		}
		var buf bytes.Buffer
		for i := 1; i <= len(got); i++ {
			buf.Write(got[i])
		}
		if !bytes.Equal(buf.Bytes(), data) {
			t.Fatalf("size %d: reassembly mismatch", size)
		}
		if peak > 3 {
			t.Fatalf("concurrency limit exceeded: %d", peak)
		}
	}
}

func TestUploadPartsPropagatesError(t *testing.T) {
	_, _, err, _ := runParts(t, make([]byte, 5000), 256, 2, 4)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("want boom, got %v", err)
	}
}

func TestValidateKey(t *testing.T) {
	ok := []string{"a", "agents/run-42/model.safetensors", "x/y.z"}
	bad := []string{"", "/a", "a/", "a//b", "..", "a/../b", "a\\b", ".ff-meta/x", ".FF-TMP/up-1", "a/.Ff-meta/x", "a\x00b"}
	for _, k := range ok {
		if ValidateKey(k) != nil {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range bad {
		if ValidateKey(k) == nil {
			t.Errorf("%q should be invalid", k)
		}
	}
}
