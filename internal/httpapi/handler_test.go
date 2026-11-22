package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luka/fileflow/internal/obs"
	"github.com/luka/fileflow/internal/storage"
	"github.com/luka/fileflow/internal/storage/local"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	b, err := local.New(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New(b, log, Options{MaxUploadBytes: 1 << 20})
	ts := httptest.NewServer(obs.Middleware(obs.MiddlewareConfig{Logger: log}, api.Routes()))
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if body == nil {
		req.Body = nil
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func read(r *http.Response) string { b, _ := io.ReadAll(r.Body); return string(b) }

func TestUploadDownloadFlow(t *testing.T) {
	ts := newTestServer(t)
	url := ts.URL + "/v1/files/agents/run-1/out.txt"
	data := []byte("0123456789abcdefghij")
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])

	resp := do(t, "PUT", url, data, map[string]string{"Content-Type": "text/plain", "X-Checksum-SHA256": hexsum, "X-Client-Kind": "agent"})
	if resp.StatusCode != 201 {
		t.Fatalf("put: %d %s", resp.StatusCode, read(resp))
	}
	var info storage.ObjectInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)
	if info.SHA256 != hexsum || info.Size != int64(len(data)) {
		t.Fatalf("bad info: %+v", info)
	}
	if resp.Header.Get("X-Trace-Id") == "" {
		t.Fatal("missing trace id")
	}

	resp = do(t, "GET", url, nil, nil)
	if resp.StatusCode != 200 || read(resp) != string(data) || resp.Header.Get("X-Checksum-SHA256") != hexsum {
		t.Fatalf("get: %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")

	resp = do(t, "GET", url, nil, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != 304 {
		t.Fatalf("want 304, got %d", resp.StatusCode)
	}

	cases := []struct{ rng, body, cr string }{
		{"bytes=0-3", "0123", "bytes 0-3/20"},
		{"bytes=18-", "ij", "bytes 18-19/20"},
		{"bytes=-5", "fghij", "bytes 15-19/20"},
		{"bytes=10-999", "abcdefghij", "bytes 10-19/20"},
	}
	for _, c := range cases {
		resp = do(t, "GET", url, nil, map[string]string{"Range": c.rng})
		if got := read(resp); resp.StatusCode != 206 || got != c.body || resp.Header.Get("Content-Range") != c.cr {
			t.Errorf("%s: %d %q %q", c.rng, resp.StatusCode, got, resp.Header.Get("Content-Range"))
		}
	}
	resp = do(t, "GET", url, nil, map[string]string{"Range": "bytes=50-"})
	if resp.StatusCode != 416 || resp.Header.Get("Content-Range") != "bytes */20" {
		t.Fatalf("want 416, got %d", resp.StatusCode)
	}
	// If-Range with a stale ETag must return the full object.
	resp = do(t, "GET", url, nil, map[string]string{"Range": "bytes=0-1", "If-Range": `"stale"`})
	if resp.StatusCode != 200 {
		t.Fatalf("stale If-Range: want 200, got %d", resp.StatusCode)
	}

	resp = do(t, "HEAD", url, nil, nil)
	if resp.StatusCode != 200 || resp.ContentLength != 20 {
		t.Fatalf("head: %d %d", resp.StatusCode, resp.ContentLength)
	}

	resp = do(t, "GET", ts.URL+"/v1/files?prefix=agents/", nil, nil)
	if !strings.Contains(read(resp), `"count":1`) {
		t.Fatal("list missing object")
	}

	if resp = do(t, "DELETE", url, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp = do(t, "GET", url, nil, nil); resp.StatusCode != 404 {
		t.Fatalf("after delete: %d", resp.StatusCode)
	}
}

func TestChecksumMismatchRejectsAndHides(t *testing.T) {
	ts := newTestServer(t)
	url := ts.URL + "/v1/files/bad.bin"
	resp := do(t, "PUT", url, []byte("hello"), map[string]string{"X-Checksum-SHA256": strings.Repeat("0", 64)})
	if resp.StatusCode != 422 {
		t.Fatalf("want 422, got %d", resp.StatusCode)
	}
	var e map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e["error"] != "checksum_mismatch" || e["trace_id"] == "" {
		t.Fatalf("bad error body: %v", e)
	}
	if resp = do(t, "GET", url, nil, nil); resp.StatusCode != 404 {
		t.Fatalf("rejected upload is visible: %d", resp.StatusCode)
	}
}

func TestLimitsAndValidation(t *testing.T) {
	ts := newTestServer(t)
	if resp := do(t, "PUT", ts.URL+"/v1/files/big", make([]byte, 2<<20), nil); resp.StatusCode != 413 {
		t.Fatalf("want 413, got %d", resp.StatusCode)
	}
	if resp := do(t, "PUT", ts.URL+"/v1/files/a/../b", []byte("x"), nil); resp.StatusCode == 201 {
		t.Fatal("path traversal accepted")
	}
	if resp := do(t, "GET", ts.URL+"/v1/files/.ff-meta/x.json", nil, nil); resp.StatusCode != 400 {
		t.Fatalf("reserved prefix: want 400, got %d", resp.StatusCode)
	}
}

func TestParseRange(t *testing.T) {
	bad := []string{"bytes=5-2", "bytes=100-", "bytes=-0", "bytes=a-b", "bytes=5"}
	for _, h := range bad {
		if _, err := parseRange(h, 100); err == nil {
			t.Errorf("%q should fail", h)
		}
	}
	for _, h := range []string{"", "items=0-1", "bytes=0-1,5-6"} {
		if r, err := parseRange(h, 100); r != nil || err != nil {
			t.Errorf("%q should be ignored", h)
		}
	}
}
