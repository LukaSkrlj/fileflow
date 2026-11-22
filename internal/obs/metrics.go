package obs

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A deliberately small Prometheus text-format registry so phase 1 has zero
// extra dependencies. Scrape /metrics with Prometheus, Grafana Agent/Alloy or
// Azure Monitor managed Prometheus. Replace with prometheus/client_golang or
// OTel metrics when the project grows (roadmap phase 4).

type Registry struct {
	mu      sync.Mutex
	metrics []writer
}

type writer interface{ write(io.Writer) }

var Default = &Registry{}

func (r *Registry) register(w writer) {
	r.mu.Lock()
	r.metrics = append(r.metrics, w)
	r.mu.Unlock()
}

// WritePrometheus writes all metrics in text exposition format.
func (r *Registry) WritePrometheus(w io.Writer) {
	r.mu.Lock()
	ms := append([]writer(nil), r.metrics...)
	r.mu.Unlock()
	for _, m := range ms {
		m.write(w)
	}
}

// ---- counters ----

type CounterVec struct {
	name, help string
	labels     []string
	m          sync.Map // joined label values -> *atomic.Uint64
}

func NewCounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{name: name, help: help, labels: labels}
	Default.register(c)
	return c
}

func (c *CounterVec) Add(v uint64, lvs ...string) {
	k := strings.Join(lvs, "\xff")
	p, ok := c.m.Load(k)
	if !ok {
		p, _ = c.m.LoadOrStore(k, new(atomic.Uint64))
	}
	p.(*atomic.Uint64).Add(v)
}

func (c *CounterVec) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
	for _, k := range sortedKeys(&c.m) {
		v, _ := c.m.Load(k)
		fmt.Fprintf(w, "%s%s %d\n", c.name, labelStr(c.labels, k, ""), v.(*atomic.Uint64).Load())
	}
}

// ---- gauge ----

type Gauge struct {
	name, help string
	v          atomic.Int64
}

func NewGauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	Default.register(g)
	return g
}

func (g *Gauge) Add(d int64) { g.v.Add(d) }
func (g *Gauge) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", g.name, g.help, g.name, g.name, g.v.Load())
}

// ---- histograms ----

type HistogramVec struct {
	name, help string
	labels     []string
	buckets    []float64
	m          sync.Map // key -> *hist
}

type hist struct {
	mu     sync.Mutex
	counts []uint64
	sum    float64
	n      uint64
}

// DurationBuckets span 1ms..5min: small metadata calls through multi-GB streams.
var DurationBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}

func NewHistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	h := &HistogramVec{name: name, help: help, labels: labels, buckets: buckets}
	Default.register(h)
	return h
}

func (h *HistogramVec) Observe(v float64, lvs ...string) {
	k := strings.Join(lvs, "\xff")
	p, ok := h.m.Load(k)
	if !ok {
		p, _ = h.m.LoadOrStore(k, &hist{counts: make([]uint64, len(h.buckets))})
	}
	hs := p.(*hist)
	hs.mu.Lock()
	for i, b := range h.buckets {
		if v <= b {
			hs.counts[i]++
		}
	}
	hs.sum += v
	hs.n++
	hs.mu.Unlock()
}

func (h *HistogramVec) ObserveDuration(d time.Duration, lvs ...string) {
	h.Observe(d.Seconds(), lvs...)
}

func (h *HistogramVec) write(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
	for _, k := range sortedKeys(&h.m) {
		p, _ := h.m.Load(k)
		hs := p.(*hist)
		hs.mu.Lock()
		for i, b := range h.buckets {
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelStr(h.labels, k, fmtFloat(b)), hs.counts[i])
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelStr(h.labels, k, "+Inf"), hs.n)
		fmt.Fprintf(w, "%s_sum%s %g\n", h.name, labelStr(h.labels, k, ""), hs.sum)
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, labelStr(h.labels, k, ""), hs.n)
		hs.mu.Unlock()
	}
}

func labelStr(names []string, key, le string) string {
	var vals []string
	if len(names) > 0 {
		vals = strings.Split(key, "\xff")
	}
	var parts []string
	for i, n := range names {
		v := ""
		if i < len(vals) {
			v = vals[i]
		}
		parts = append(parts, fmt.Sprintf(`%s="%s"`, n, escape(v)))
	}
	if le != "" {
		parts = append(parts, fmt.Sprintf(`le="%s"`, le))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func fmtFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}
	return fmt.Sprintf("%g", f)
}

func sortedKeys(m *sync.Map) []string {
	var ks []string
	m.Range(func(k, _ any) bool { ks = append(ks, k.(string)); return true })
	sort.Strings(ks)
	return ks
}

// ---- the service's metrics ----

var (
	HTTPRequests = NewCounterVec("fileflow_http_requests_total",
		"HTTP requests by route, method, status code and client kind.", "route", "method", "code", "client")
	HTTPDuration = NewHistogramVec("fileflow_http_request_duration_seconds",
		"Full request duration including body transfer.", DurationBuckets, "route", "method")
	HTTPTTFB = NewHistogramVec("fileflow_http_ttfb_seconds",
		"Time from request start to first response byte.", DurationBuckets, "route", "method")
	HTTPBytes = NewCounterVec("fileflow_http_bytes_total",
		"Body bytes transferred, direction=in|out.", "direction", "client")
	HTTPInflight = NewGauge("fileflow_http_inflight_requests", "Requests currently being served.")
	BackendOps   = NewHistogramVec("fileflow_backend_op_duration_seconds",
		"Storage backend call duration (for get: time to open the stream).", DurationBuckets, "backend", "op", "result")
)
