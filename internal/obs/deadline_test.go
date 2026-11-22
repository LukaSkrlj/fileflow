package obs

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A healthy download longer than IdleIOTimeout must keep its request
// context: S3/Azure body streams die when it is cancelled.
func TestLongDownloadKeepsContext(t *testing.T) {
	ctxErr := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		for i := 0; i < 12; i++ { // ~3s, twice the idle timeout
			w.Write([]byte("x"))
			rc.Flush()
			time.Sleep(250 * time.Millisecond)
		}
		ctxErr <- r.Context().Err()
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(Middleware(MiddlewareConfig{Logger: log, IdleIOTimeout: 1500 * time.Millisecond}, h))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, err := io.ReadAll(resp.Body); err != nil || len(b) != 12 {
		t.Fatalf("download cut short: %d bytes, err %v", len(b), err)
	}
	if err := <-ctxErr; err != nil {
		t.Fatalf("request context cancelled during a healthy download: %v", err)
	}
}
