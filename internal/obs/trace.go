// Package obs holds observability plumbing: W3C trace context propagation,
// a trace-aware slog handler, per-request phase timings and a tiny
// Prometheus-format metrics registry.
//
// The trace/span IDs use the W3C Trace Context format (traceparent header),
// the same format OpenTelemetry uses, so swapping this for the OTel SDK later
// (roadmap phase 4) keeps every ID already in your logs compatible.
package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// SpanContext identifies one unit of work within a distributed trace.
type SpanContext struct {
	TraceID  [16]byte
	SpanID   [8]byte
	ParentID [8]byte // zero for a root span
	Flags    byte    // 0x01 = sampled
}

func (s SpanContext) TraceIDString() string  { return hex.EncodeToString(s.TraceID[:]) }
func (s SpanContext) SpanIDString() string   { return hex.EncodeToString(s.SpanID[:]) }
func (s SpanContext) ParentIDString() string { return hex.EncodeToString(s.ParentID[:]) }
func (s SpanContext) HasParent() bool        { return s.ParentID != [8]byte{} }

// Traceparent renders the W3C header: 00-<trace-id>-<span-id>-<flags>.
func (s SpanContext) Traceparent() string {
	return "00-" + s.TraceIDString() + "-" + s.SpanIDString() + "-" + hex.EncodeToString([]byte{s.Flags})
}

// NewRoot starts a new trace.
func NewRoot() SpanContext {
	var s SpanContext
	_, _ = rand.Read(s.TraceID[:])
	_, _ = rand.Read(s.SpanID[:])
	s.Flags = 0x01
	return s
}

// Child returns a new span in the same trace whose parent is s.
func (s SpanContext) Child() SpanContext {
	c := SpanContext{TraceID: s.TraceID, ParentID: s.SpanID, Flags: s.Flags}
	_, _ = rand.Read(c.SpanID[:])
	return c
}

// ParseTraceparent parses a W3C traceparent header value.
func ParseTraceparent(h string) (SpanContext, bool) {
	var s SpanContext
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || parts[0] == "ff" ||
		len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return s, false
	}
	if _, err := hex.Decode(s.TraceID[:], []byte(parts[1])); err != nil {
		return s, false
	}
	if _, err := hex.Decode(s.SpanID[:], []byte(parts[2])); err != nil {
		return s, false
	}
	var f [1]byte
	if _, err := hex.Decode(f[:], []byte(parts[3])); err != nil {
		return s, false
	}
	s.Flags = f[0]
	if s.TraceID == [16]byte{} || s.SpanID == [8]byte{} {
		return SpanContext{}, false // all-zero IDs are invalid per spec
	}
	return s, true
}

type spanKey struct{}

func WithSpan(ctx context.Context, s SpanContext) context.Context {
	return context.WithValue(ctx, spanKey{}, s)
}

func SpanFrom(ctx context.Context) (SpanContext, bool) {
	s, ok := ctx.Value(spanKey{}).(SpanContext)
	return s, ok
}

// Inject adds a traceparent for a child span to outgoing request headers, so
// the next service (e.g. the future metadata service) joins the same trace.
func Inject(ctx context.Context, h http.Header) {
	if s, ok := SpanFrom(ctx); ok {
		h.Set("traceparent", s.Child().Traceparent())
	}
}
