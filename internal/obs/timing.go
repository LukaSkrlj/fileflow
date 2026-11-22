package obs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Timings records named phases of one request (stat, open, transfer, ...).
// They are emitted three ways: in the access log, as a Server-Timing response
// header (visible in browser devtools and curl -v), and as backend metrics.
// This breakdown is the first tool for finding where time goes.
type Timings struct {
	mu     sync.Mutex
	phases []phase
}

type phase struct {
	name string
	dur  time.Duration
}

type timingsKey struct{}

func WithTimings(ctx context.Context) (context.Context, *Timings) {
	t := &Timings{}
	return context.WithValue(ctx, timingsKey{}, t), t
}

func TimingsFrom(ctx context.Context) *Timings {
	t, _ := ctx.Value(timingsKey{}).(*Timings)
	return t
}

func (t *Timings) Add(name string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.phases = append(t.phases, phase{name, d})
	t.mu.Unlock()
}

// Track starts a phase; call the returned func to end it.
//
//	defer obs.Track(ctx, "stat")()
func Track(ctx context.Context, name string) func() {
	start := time.Now()
	t := TimingsFrom(ctx)
	return func() { t.Add(name, time.Since(start)) }
}

// ServerTiming renders the Server-Timing header value.
func (t *Timings) ServerTiming() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := make([]string, 0, len(t.phases))
	for _, p := range t.phases {
		parts = append(parts, fmt.Sprintf("%s;dur=%.2f", p.name, ms(p.dur)))
	}
	return strings.Join(parts, ", ")
}

// Attr renders the phases as a log group: "timings_ms":{"stat":1.2,...}.
func (t *Timings) Attr() slog.Attr {
	if t == nil {
		return slog.Attr{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	attrs := make([]any, 0, len(t.phases))
	for _, p := range t.phases {
		attrs = append(attrs, slog.Float64(p.name, round2(ms(p.dur))))
	}
	return slog.Group("timings_ms", attrs...)
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func round2(f float64) float64   { return float64(int64(f*100+0.5)) / 100 }
