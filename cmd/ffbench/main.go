// Command ffbench is a load generator for fileflow. It reports latency
// percentiles, time-to-first-byte and throughput, the server's own
// Server-Timing breakdown, and the trace ids of the slowest requests so you
// can jump straight to their server logs.
//
//	ffbench -op put -n 50 -c 8 -size 64MiB -kind agent
//	ffbench -op get -n 500 -c 32 -size 1MiB -keys 20
//	ffbench -op get -n 200 -c 16 -size 256MiB -keys 4 -range 8MiB
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptrace"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luka/fileflow/internal/config"
	"github.com/luka/fileflow/internal/obs"
)

type result struct {
	op      string
	status  int
	err     error
	dur     time.Duration
	ttfb    time.Duration
	bytes   int64
	traceID string
	timing  map[string]float64 // parsed Server-Timing
	key     string
}

func main() {
	var (
		base    = flag.String("url", "http://localhost:8080", "fileflow base URL")
		op      = flag.String("op", "put", "put | get")
		n       = flag.Int("n", 100, "number of requests")
		conc    = flag.Int("c", 8, "concurrent workers")
		sizeS   = flag.String("size", "4MiB", "object size (put, and setup for get)")
		keys    = flag.Int("keys", 10, "distinct objects for get (uploaded in a setup phase)")
		rangeS  = flag.String("range", "", "for get: random ranged reads of this size, e.g. 8MiB")
		prefix  = flag.String("prefix", "bench", "key prefix")
		kind    = flag.String("kind", "agent", "X-Client-Kind header: agent | human")
		data    = flag.String("data", "random", "random (incompressible) | text (compressible)")
		h2c     = flag.Bool("h2c", false, "use cleartext HTTP/2 (server needs FILEFLOW_H2C=true)")
		jsonOut = flag.Bool("json", false, "print summary as JSON (for CI regression tracking)")
		top     = flag.Int("top", 5, "show N slowest requests with trace ids")
	)
	flag.Parse()

	size, err := config.ParseBytes(*sizeS)
	must(err)
	var rangeLen int64
	if *rangeS != "" {
		rangeLen, err = config.ParseBytes(*rangeS)
		must(err)
	}

	tr := &http.Transport{
		MaxIdleConns: *conc * 2, MaxIdleConnsPerHost: *conc * 2,
		DisableCompression: true, IdleConnTimeout: 90 * time.Second,
		WriteBufferSize: 256 << 10, ReadBufferSize: 256 << 10,
	}
	if *h2c {
		var p http.Protocols
		p.SetUnencryptedHTTP2(true)
		tr.Protocols = &p
	}
	client := &http.Client{Transport: tr}
	src := newSource(*data)
	runID := strconv.FormatInt(time.Now().Unix(), 36)

	keyFor := func(i int) string { return fmt.Sprintf("%s/%s/obj-%05d", *prefix, runID, i) }

	if *op == "get" {
		fmt.Fprintf(os.Stderr, "setup: uploading %d objects of %s...\n", *keys, *sizeS)
		for i := 0; i < *keys; i++ {
			r := doPut(client, *base, keyFor(i), size, src, *kind)
			if r.err != nil || r.status != 201 {
				fail("setup upload failed: status=%d err=%v trace=%s", r.status, r.err, r.traceID)
			}
		}
	}

	results := make([]result, *n)
	var next atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= *n {
					return
				}
				switch *op {
				case "put":
					results[i] = doPut(client, *base, keyFor(i), size, src, *kind)
				case "get":
					results[i] = doGet(client, *base, keyFor(mrand.IntN(*keys)), size, rangeLen, *kind)
				default:
					fail("unknown -op %q", *op)
				}
			}
		}()
	}
	wg.Wait()
	wall := time.Since(start)
	summarize(results, wall, *op, *conc, *top, *jsonOut)
}

func doPut(c *http.Client, base, key string, size int64, src *source, kind string) result {
	body := io.LimitReader(src.reader(), size)
	req, _ := http.NewRequest("PUT", base+"/v1/files/"+key, body)
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	return send(c, req, "put", key, kind)
}

func doGet(c *http.Client, base, key string, size, rangeLen int64, kind string) result {
	req, _ := http.NewRequest("GET", base+"/v1/files/"+key, nil)
	if rangeLen > 0 && rangeLen < size {
		start := mrand.Int64N(size - rangeLen)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+rangeLen-1))
	}
	return send(c, req, "get", key, kind)
}

func send(c *http.Client, req *http.Request, op, key, kind string) result {
	span := obs.NewRoot()
	req.Header.Set("traceparent", span.Traceparent())
	req.Header.Set("X-Client-Kind", kind)
	res := result{op: op, key: key, traceID: span.TraceIDString()}

	start := time.Now()
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { res.ttfb = time.Since(start) }}
	req = req.WithContext(httptrace.WithClientTrace(context.Background(), trace))
	resp, err := c.Do(req)
	if err != nil {
		res.err, res.dur = err, time.Since(start)
		return res
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	res.dur = time.Since(start)
	res.status, res.err = resp.StatusCode, err
	res.timing = parseServerTiming(resp.Header.Get("Server-Timing"))
	if op == "put" {
		res.bytes = req.ContentLength
	} else {
		res.bytes = n
	}
	return res
}

func summarize(rs []result, wall time.Duration, op string, conc, top int, asJSON bool) {
	var ok []result
	errs := map[string]int{}
	var bytesTotal int64
	timingSum := map[string]float64{}
	for _, r := range rs {
		switch {
		case r.err != nil:
			errs[r.err.Error()]++
		case r.status >= 300:
			errs["HTTP "+strconv.Itoa(r.status)]++
		default:
			ok = append(ok, r)
			bytesTotal += r.bytes
			for k, v := range r.timing {
				timingSum[k] += v
			}
		}
	}
	durs := make([]time.Duration, len(ok))
	ttfbs := make([]time.Duration, len(ok))
	for i, r := range ok {
		durs[i], ttfbs[i] = r.dur, r.ttfb
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	sort.Slice(ttfbs, func(i, j int) bool { return ttfbs[i] < ttfbs[j] })
	mbps := float64(bytesTotal) / 1e6 / wall.Seconds()

	avgTiming := map[string]float64{}
	for k, v := range timingSum {
		avgTiming[k] = v / float64(max(len(ok), 1))
	}

	if asJSON {
		out := map[string]any{
			"op": op, "concurrency": conc, "requests": len(rs), "ok": len(ok), "errors": errs,
			"wall_s": wall.Seconds(), "req_per_s": float64(len(ok)) / wall.Seconds(), "mb_per_s": mbps,
			"latency_ms":       pctMap(durs),
			"ttfb_ms":          pctMap(ttfbs),
			"server_timing_ms": avgTiming,
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
		return
	}

	fmt.Printf("\n%s  requests=%d ok=%d errors=%d concurrency=%d wall=%s\n",
		strings.ToUpper(op), len(rs), len(ok), len(rs)-len(ok), conc, wall.Round(time.Millisecond))
	fmt.Printf("throughput   %.1f MB/s  (%.0f Mbit/s)   %.1f req/s\n", mbps, mbps*8, float64(len(ok))/wall.Seconds())
	fmt.Printf("latency      p50=%s p90=%s p99=%s max=%s\n", pct(durs, 50), pct(durs, 90), pct(durs, 99), pct(durs, 100))
	fmt.Printf("ttfb         p50=%s p90=%s p99=%s max=%s\n", pct(ttfbs, 50), pct(ttfbs, 90), pct(ttfbs, 99), pct(ttfbs, 100))
	if len(avgTiming) > 0 {
		var ks []string
		for k := range avgTiming {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Print("server avg  ")
		for _, k := range ks {
			fmt.Printf(" %s=%.2fms", k, avgTiming[k])
		}
		fmt.Println()
	}
	for e, c := range errs {
		fmt.Printf("error x%d: %s\n", c, e)
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].dur > ok[j].dur })
	if top > 0 && len(ok) > 0 {
		fmt.Printf("\nslowest %d (grep these trace ids in the server log):\n", min(top, len(ok)))
		for _, r := range ok[:min(top, len(ok))] {
			fmt.Printf("  %-10s ttfb=%-10s trace_id=%s key=%s\n", r.dur.Round(time.Microsecond), r.ttfb.Round(time.Microsecond), r.traceID, r.key)
		}
	}
}

func pct(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	i := (len(d)*p + 99) / 100
	i = min(max(i-1, 0), len(d)-1)
	return d[i].Round(time.Microsecond)
}

func pctMap(d []time.Duration) map[string]float64 {
	out := map[string]float64{}
	for _, p := range []int{50, 90, 99, 100} {
		out["p"+strconv.Itoa(p)] = float64(pct(d, p)) / 1e6
	}
	return out
}

func parseServerTiming(h string) map[string]float64 {
	if h == "" {
		return nil
	}
	out := map[string]float64{}
	for _, part := range strings.Split(h, ",") {
		name, rest, _ := strings.Cut(strings.TrimSpace(part), ";")
		if v, ok := strings.CutPrefix(strings.TrimSpace(rest), "dur="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out[name] = f
			}
		}
	}
	return out
}

// source produces upload bodies quickly without allocating object-sized
// buffers: a 4 MiB block repeated. "random" is incompressible (realistic for
// model weights, media, archives); "text" compresses well (logs, JSON, CSV)
// and is what you want when testing compression (roadmap phase 6).
type source struct{ block []byte }

func newSource(kind string) *source {
	b := make([]byte, 4<<20)
	if kind == "text" {
		line := []byte(`{"ts":"2026-11-22T12:00:00Z","agent":"planner-7","level":"info","msg":"step completed","tokens":1842}` + "\n")
		for i := 0; i < len(b); i += len(line) {
			copy(b[i:], line)
		}
	} else {
		_, _ = rand.Read(b)
	}
	return &source{block: b}
}

func (s *source) reader() io.Reader { return &repeatReader{b: s.block} }

type repeatReader struct {
	b   []byte
	off int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	n := copy(p, r.b[r.off:])
	r.off = (r.off + n) % len(r.b)
	return n, nil
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
