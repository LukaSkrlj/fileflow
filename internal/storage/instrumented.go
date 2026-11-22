package storage

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/luka/fileflow/internal/obs"
)

// Instrument wraps a backend so every call is timed into the request's
// Timings (log + Server-Timing) and the backend latency histogram.
func Instrument(b Backend) Backend { return &instrumented{b} }

type instrumented struct{ Backend }

func observe(ctx context.Context, backend, op string, start time.Time, err error) {
	d := time.Since(start)
	result := "ok"
	switch {
	case err == nil:
	case errors.Is(err, ErrNotFound):
		result = "not_found"
	case errors.Is(err, context.Canceled):
		result = "canceled"
	default:
		result = "error"
	}
	obs.BackendOps.ObserveDuration(d, backend, op, result)
	obs.TimingsFrom(ctx).Add("backend_"+op, d)
}

func (i *instrumented) Put(ctx context.Context, key string, r io.Reader, o PutOptions) (ObjectInfo, error) {
	start := time.Now()
	info, err := i.Backend.Put(ctx, key, r, o)
	observe(ctx, i.Name(), "put", start, err)
	return info, err
}

func (i *instrumented) Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, ObjectInfo, error) {
	start := time.Now()
	rc, info, err := i.Backend.Get(ctx, key, rng)
	observe(ctx, i.Name(), "open", start, err)
	return rc, info, err
}

func (i *instrumented) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	start := time.Now()
	info, err := i.Backend.Stat(ctx, key)
	observe(ctx, i.Name(), "stat", start, err)
	return info, err
}

func (i *instrumented) Delete(ctx context.Context, key string) error {
	start := time.Now()
	err := i.Backend.Delete(ctx, key)
	observe(ctx, i.Name(), "delete", start, err)
	return err
}

func (i *instrumented) List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	start := time.Now()
	out, err := i.Backend.List(ctx, prefix, limit)
	observe(ctx, i.Name(), "list", start, err)
	return out, err
}
