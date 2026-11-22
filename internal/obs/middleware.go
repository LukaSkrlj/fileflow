package obs

import (
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"time"
)

// MiddlewareConfig tunes the HTTP observability/transport wrapper.
type MiddlewareConfig struct {
	Logger *slog.Logger
	// IdleIOTimeout cuts a connection that makes no read/write progress for
	// this long. Unlike http.Server.ReadTimeout/WriteTimeout it never kills a
	// healthy multi-GB transfer, only a stalled one.
	IdleIOTimeout time.Duration
}

// Middleware adds trace context, idle-progress deadlines, access logs,
// Server-Timing and metrics around next.
func Middleware(cfg MiddlewareConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		HTTPInflight.Add(1)
		defer HTTPInflight.Add(-1)

		var span SpanContext
		if parent, ok := ParseTraceparent(r.Header.Get("traceparent")); ok {
			span = parent.Child()
		} else {
			span = NewRoot()
		}
		ctx := WithSpan(r.Context(), span)
		ctx, timings := WithTimings(ctx)

		rc := http.NewResponseController(w)
		dl := &deadlines{rc: rc, idle: cfg.IdleIOTimeout}
		// No read deadline without a body: nothing would extend it, and when
		// it fired net/http's background read would cancel the request
		// context mid-download (killing S3/Azure streams).
		if r.Body != http.NoBody {
			dl.extendRead(true)
		}
		dl.extendWrite(true)

		rec := &recorder{ResponseWriter: w, start: start, timings: timings, dl: dl}
		rec.Header().Set("traceparent", span.Traceparent())
		rec.Header().Set("X-Trace-Id", span.TraceIDString())

		body := &countingBody{ReadCloser: r.Body, dl: dl}
		r2 := r.WithContext(ctx)
		r2.Body = body

		// Handlers abort mid-stream with panic(http.ErrAbortHandler); recover
		// so the access log is still written, then re-panic to drop the conn.
		aborted := false
		func() {
			defer func() {
				if rv := recover(); rv != nil {
					aborted = true
					if rv != http.ErrAbortHandler {
						cfg.Logger.ErrorContext(ctx, "handler panic", "panic", rv, "stack", string(debug.Stack()))
						if !rec.wroteHeader {
							rec.WriteHeader(http.StatusInternalServerError)
						}
					}
				}
			}()
			next.ServeHTTP(rec, r2)
		}()

		dur := time.Since(start)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		route := r2.Pattern
		if route == "" {
			route = "unmatched"
		}
		client := ClientKind(r)
		in, out := body.n.Load(), rec.bytes

		HTTPRequests.Add(1, route, r.Method, strconv.Itoa(status), client)
		HTTPDuration.ObserveDuration(dur, route, r.Method)
		if !rec.firstByte.IsZero() {
			HTTPTTFB.ObserveDuration(rec.firstByte.Sub(start), route, r.Method)
		}
		HTTPBytes.Add(uint64(in), "in", client)
		HTTPBytes.Add(uint64(out), "out", client)

		attrs := []any{
			"method", r.Method, "route", route, "path", r.URL.Path, "status", status,
			"duration_ms", round2(ms(dur)), "bytes_in", in, "bytes_out", out,
			"client", client, "proto", r.Proto, "remote", r.RemoteAddr,
			"user_agent", r.UserAgent(), timings.Attr(),
		}
		if !rec.firstByte.IsZero() {
			attrs = append(attrs, "ttfb_ms", round2(ms(rec.firstByte.Sub(start))))
		}
		if moved := max(in, out); moved > 0 && dur > 0 {
			attrs = append(attrs, "throughput_mbps", round2(float64(moved)*8/1e6/dur.Seconds()))
		}
		// Client errors (404, 416...) are normal traffic, not server problems.
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if aborted {
			level = slog.LevelWarn
		}
		if aborted {
			attrs = append(attrs, "aborted", true)
		}
		cfg.Logger.Log(ctx, level, "http_request", attrs...)
		if aborted {
			panic(http.ErrAbortHandler)
		}
	})
}

// ClientKind buckets the X-Client-Kind header into a bounded set, so humans
// and agents can be compared without exploding metric cardinality.
func ClientKind(r *http.Request) string {
	switch r.Header.Get("X-Client-Kind") {
	case "agent":
		return "agent"
	case "human":
		return "human"
	default:
		return "unknown"
	}
}

type deadlines struct {
	rc           *http.ResponseController
	idle         time.Duration
	lastR, lastW time.Time
}

// extend* push the deadline forward at most once per second to keep the
// per-Read overhead negligible.
func (d *deadlines) extendRead(force bool) {
	if d.idle <= 0 {
		return
	}
	now := time.Now()
	if force || now.Sub(d.lastR) > time.Second {
		d.lastR = now
		_ = d.rc.SetReadDeadline(now.Add(d.idle))
	}
}

func (d *deadlines) extendWrite(force bool) {
	if d.idle <= 0 {
		return
	}
	now := time.Now()
	if force || now.Sub(d.lastW) > time.Second {
		d.lastW = now
		_ = d.rc.SetWriteDeadline(now.Add(d.idle))
	}
}

type countingBody struct {
	io.ReadCloser
	n  atomic.Int64
	dl *deadlines
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	if n > 0 {
		b.dl.extendRead(false)
	}
	if err == io.EOF && b.dl.idle > 0 {
		// Body done: a deadline left running would cancel the request while
		// the handler is still committing or writing the response.
		_ = b.dl.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

type recorder struct {
	http.ResponseWriter
	start       time.Time
	status      int
	bytes       int64
	firstByte   time.Time
	timings     *Timings
	dl          *deadlines
	wroteHeader bool
}

func (r *recorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = code
	if st := r.timings.ServerTiming(); st != "" {
		r.Header().Set("Server-Timing", st)
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if r.firstByte.IsZero() && len(p) > 0 {
		r.firstByte = time.Now()
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	r.dl.extendWrite(false)
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer (Flush,
// deadlines, EnableFullDuplex).
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
