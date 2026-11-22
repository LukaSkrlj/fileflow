package obs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTraceparentRoundTrip(t *testing.T) {
	root := NewRoot()
	got, ok := ParseTraceparent(root.Traceparent())
	if !ok || got.TraceID != root.TraceID || got.SpanID != root.SpanID {
		t.Fatalf("round trip failed: %v %+v", ok, got)
	}
	child := root.Child()
	if child.TraceID != root.TraceID || child.ParentID != root.SpanID || child.SpanID == root.SpanID {
		t.Fatal("child must keep trace id and point at parent")
	}
	for _, bad := range []string{"", "00-xyz", "00-00000000000000000000000000000000-0000000000000000-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"} {
		if _, ok := ParseTraceparent(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestMiddlewareJoinsIncomingTraceAndLogs(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "info", "json", "test", "dev")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /x/{id}", func(w http.ResponseWriter, r *http.Request) {
		stop := Track(r.Context(), "work")
		time.Sleep(2 * time.Millisecond)
		stop()
		log.InfoContext(r.Context(), "inside handler")
		w.Write([]byte("ok"))
	})
	h := Middleware(MiddlewareConfig{Logger: log}, mux)

	const incoming = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequest("GET", "/x/1", nil)
	req.Header.Set("traceparent", incoming)
	req.Header.Set("X-Client-Kind", "agent")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Trace-Id"); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id not propagated: %q", got)
	}
	if !strings.HasPrefix(rr.Header().Get("Server-Timing"), "work;dur=") {
		t.Fatalf("missing Server-Timing: %q", rr.Header().Get("Server-Timing"))
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d: %s", len(lines), buf.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		if m["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || m["parent_span_id"] != "00f067aa0ba902b7" {
			t.Fatalf("log line missing trace fields: %s", l)
		}
	}
	var access map[string]any
	_ = json.Unmarshal([]byte(lines[1]), &access)
	if access["route"] != "GET /x/{id}" || access["client"] != "agent" || access["bytes_out"].(float64) != 2 {
		t.Fatalf("bad access log: %s", lines[1])
	}
}

func TestMetricsExposition(t *testing.T) {
	HTTPRequests.Add(1, "GET /t", "GET", "200", "human")
	BackendOps.Observe(0.003, "local", "stat", "ok")
	var buf bytes.Buffer
	Default.WritePrometheus(&buf)
	out := buf.String()
	for _, want := range []string{
		`fileflow_http_requests_total{route="GET /t",method="GET",code="200",client="human"} 1`,
		`fileflow_backend_op_duration_seconds_bucket{backend="local",op="stat",result="ok",le="0.005"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}
