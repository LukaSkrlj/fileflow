// Package httpapi exposes the storage backend over HTTP.
//
//	PUT    /v1/files/{key...}   stream upload (Content-Length or chunked)
//	GET    /v1/files/{key...}   stream download, supports Range / If-None-Match
//	HEAD   /v1/files/{key...}   metadata only
//	DELETE /v1/files/{key...}
//	GET    /v1/files?prefix=&limit=
//	GET    /healthz, /readyz
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/luka/fileflow/internal/obs"
	"github.com/luka/fileflow/internal/storage"
)

type Options struct {
	MaxUploadBytes int64
}

type Server struct {
	backend storage.Backend
	log     *slog.Logger
	opts    Options
	ready   atomic.Bool
}

func New(b storage.Backend, log *slog.Logger, opts Options) *Server {
	s := &Server{backend: storage.Instrument(b), log: log, opts: opts}
	s.ready.Store(true)
	return s
}

// SetReady(false) makes /readyz fail so load balancers drain this instance
// before shutdown.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/files/{key...}", s.put)
	mux.HandleFunc("GET /v1/files/{key...}", s.get) // GET patterns also match HEAD
	mux.HandleFunc("DELETE /v1/files/{key...}", s.del)
	mux.HandleFunc("GET /v1/files", s.list)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.readyz)
	return mux
}

var errChecksum = errors.New("checksum mismatch")

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.PathValue("key")
	if err := storage.ValidateKey(key); err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid_key", err)
		return
	}
	if s.opts.MaxUploadBytes > 0 && r.ContentLength > s.opts.MaxUploadBytes {
		s.fail(w, r, http.StatusRequestEntityTooLarge, "too_large", nil)
		return
	}
	expected := strings.ToLower(r.Header.Get("X-Checksum-SHA256"))
	if expected != "" && !isHexSHA256(expected) {
		s.fail(w, r, http.StatusBadRequest, "invalid_checksum_header", nil)
		return
	}

	body := io.Reader(r.Body)
	if s.opts.MaxUploadBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)
	}
	hasher := sha256.New()
	// clientRead measures time blocked waiting for the client's bytes. If it
	// dominates backend_put, the uploader's network is the bottleneck, not us.
	// Time the body alone, so hashing CPU isn't blamed on the client.
	tr := &timedReader{r: body}
	src := io.TeeReader(tr, hasher)

	meta := map[string]string{
		"client-kind": obs.ClientKind(r),
	}
	if sp, ok := obs.SpanFrom(ctx); ok {
		meta["trace-id"] = sp.TraceIDString() // find the request that wrote any object
	}
	if expected != "" {
		meta["sha256"] = expected // known up front: persisted even on S3 multipart
	}
	var actual string
	info, err := s.backend.Put(ctx, key, src, storage.PutOptions{
		ContentType: r.Header.Get("Content-Type"),
		Size:        r.ContentLength,
		Metadata:    meta,
		BeforeCommit: func() (map[string]string, error) {
			actual = hex.EncodeToString(hasher.Sum(nil))
			if expected != "" && actual != expected {
				return nil, errChecksum
			}
			return map[string]string{"sha256": actual}, nil
		},
	})
	obs.TimingsFrom(ctx).Add("client_read", tr.d)
	if err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			s.fail(w, r, http.StatusRequestEntityTooLarge, "too_large", err)
		case errors.Is(err, errChecksum):
			s.fail(w, r, http.StatusUnprocessableEntity, "checksum_mismatch", err)
		case errors.Is(err, storage.ErrInvalidKey):
			s.fail(w, r, http.StatusBadRequest, "invalid_key", err)
		case ctx.Err() != nil:
			s.fail(w, r, 499, "client_closed", err) // nginx convention; client is gone anyway
		default:
			s.fail(w, r, http.StatusBadGateway, "backend_error", err)
		}
		return
	}
	info.SHA256 = actual
	w.Header().Set("ETag", quote(info.ETag))
	w.Header().Set("X-Checksum-SHA256", actual)
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.PathValue("key")
	if err := storage.ValidateKey(key); err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid_key", err)
		return
	}
	// Stat first: needed for conditional requests and range validation.
	// Its cost shows up as backend_stat; it is the first thing a metadata
	// cache (Redis, roadmap phase 3) removes from the hot path.
	info, err := s.backend.Stat(ctx, key)
	if err != nil {
		s.backendErr(w, r, err)
		return
	}
	etag := quote(info.ETag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && (inm == etag || inm == "*") {
		setObjectHeaders(w, info)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	rangeHdr := r.Header.Get("Range")
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		rangeHdr = "" // object changed since the client's partial download: send it all
	}
	rng, err := parseRange(rangeHdr, info.Size)
	if err != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(info.Size, 10))
		s.fail(w, r, http.StatusRequestedRangeNotSatisfiable, "invalid_range", err)
		return
	}

	setObjectHeaders(w, info)
	length := info.Size
	status := http.StatusOK
	if rng != nil {
		length = rng.Length
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(rng.Start, 10)+"-"+
			strconv.FormatInt(rng.End(), 10)+"/"+strconv.FormatInt(info.Size, 10))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}

	rc, ginfo, err := s.backend.Get(ctx, key, rng)
	if err != nil {
		w.Header().Del("Content-Range")
		w.Header().Del("Content-Length")
		s.backendErr(w, r, err)
		return
	}
	defer rc.Close()
	if ginfo.ETag != "" && ginfo.ETag != info.ETag {
		// Overwritten between Stat and Get. Versioning (phase 5) fixes this
		// properly by reading an immutable version id.
		s.log.WarnContext(ctx, "object changed during request", "key", key)
	}
	w.WriteHeader(status)
	n, backendRead, clientWrite, err := copyTimed(w, rc)
	t := obs.TimingsFrom(ctx)
	t.Add("backend_read", backendRead)
	t.Add("client_write", clientWrite)
	if err != nil || n != length {
		// Headers are gone; all we can do is log and drop the connection so
		// the client sees a truncated body rather than a silent short file.
		s.log.WarnContext(ctx, "download interrupted", "key", key, "sent", n, "want", length, "err", errString(err))
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := s.backend.Delete(r.Context(), key); err != nil {
		s.backendErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	items, err := s.backend.List(r.Context(), r.URL.Query().Get("prefix"), limit)
	if err != nil {
		s.backendErr(w, r, err)
		return
	}
	if items == nil {
		items = []storage.ObjectInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, err := s.backend.Stat(ctx, "readyz-probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
		s.fail(w, r, http.StatusServiceUnavailable, "backend_unavailable", err)
		return
	}
	w.Write([]byte("ready"))
}

// ---- helpers ----

func setObjectHeaders(w http.ResponseWriter, info storage.ObjectInfo) {
	h := w.Header()
	ct := info.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("ETag", quote(info.ETag))
	h.Set("Accept-Ranges", "bytes")
	if !info.ModTime.IsZero() {
		h.Set("Last-Modified", info.ModTime.UTC().Format(http.TimeFormat))
	}
	if info.SHA256 != "" {
		h.Set("X-Checksum-SHA256", info.SHA256)
	}
	// Private until auth + CDN rules exist (phases 2-3): never let a shared
	// cache store user files by accident.
	h.Set("Cache-Control", "private, no-cache")
}

func (s *Server) backendErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", nil)
	case errors.Is(err, storage.ErrInvalidKey):
		s.fail(w, r, http.StatusBadRequest, "invalid_key", err)
	case errors.Is(err, storage.ErrInvalidRange):
		s.fail(w, r, http.StatusRequestedRangeNotSatisfiable, "invalid_range", err)
	case r.Context().Err() != nil:
		s.fail(w, r, 499, "client_closed", err)
	default:
		s.fail(w, r, http.StatusBadGateway, "backend_error", err)
	}
}

// fail writes a JSON error carrying the trace id, so a user can hand you one
// string that finds every log line for their failed request.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code string, err error) {
	body := map[string]string{"error": code}
	if sp, ok := obs.SpanFrom(r.Context()); ok {
		body["trace_id"] = sp.TraceIDString()
	}
	if err != nil {
		lvl := slog.LevelWarn
		if status >= 500 {
			lvl = slog.LevelError
		}
		s.log.Log(r.Context(), lvl, "request failed", "code", code, "err", err.Error())
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func quote(etag string) string {
	if etag == "" {
		return ""
	}
	return `"` + etag + `"`
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type timedReader struct {
	r io.Reader
	d time.Duration
}

func (t *timedReader) Read(p []byte) (int, error) {
	start := time.Now()
	n, err := t.r.Read(p)
	t.d += time.Since(start)
	return n, err
}

// 256 KiB copy buffers: large enough to amortise syscalls on 10+ Gbit links,
// pooled so thousands of concurrent downloads don't churn the GC.
var copyBufs = sync.Pool{New: func() any { b := make([]byte, 256<<10); return &b }}

// copyTimed streams src to dst and splits elapsed time into time spent
// waiting on the backend vs. waiting on the client. This split is the single
// most useful signal for "where is my download slow?".
func copyTimed(dst io.Writer, src io.Reader) (n int64, readD, writeD time.Duration, err error) {
	bp := copyBufs.Get().(*[]byte)
	defer copyBufs.Put(bp)
	buf := *bp
	for {
		t0 := time.Now()
		nr, rerr := src.Read(buf)
		t1 := time.Now()
		readD += t1.Sub(t0)
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			writeD += time.Since(t1)
			n += int64(nw)
			if werr != nil {
				return n, readD, writeD, werr
			}
		}
		if rerr == io.EOF {
			return n, readD, writeD, nil
		}
		if rerr != nil {
			return n, readD, writeD, rerr
		}
	}
}
