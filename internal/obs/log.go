package obs

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// traceHandler decorates every record logged with a context that carries a
// span with trace_id / span_id, so `grep trace_id=...` (or a KQL query in
// Azure Log Analytics) returns every line of one request across services.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if s, ok := SpanFrom(ctx); ok {
		r.AddAttrs(slog.String("trace_id", s.TraceIDString()), slog.String("span_id", s.SpanIDString()))
		if s.HasParent() {
			r.AddAttrs(slog.String("parent_span_id", s.ParentIDString()))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(a)}
}
func (h traceHandler) WithGroup(n string) slog.Handler { return traceHandler{h.Handler.WithGroup(n)} }

// NewLogger builds the process logger. format is "json" (default, for
// production log shipping) or "text" (human-friendly local dev).
func NewLogger(w io.Writer, level, format, service, version string) *slog.Logger {
	var lv slog.Level
	_ = lv.UnmarshalText([]byte(strings.ToUpper(level)))
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(traceHandler{h}).With("service", service, "version", version)
}
