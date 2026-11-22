package storage

import (
	"context"
	"errors"
	"io"
	"sync"
)

// BufPool hands out fixed-size part buffers so concurrent multipart uploads
// reuse memory instead of allocating partSize bytes per part.
type BufPool struct {
	size int
	p    sync.Pool
}

func NewBufPool(size int) *BufPool {
	bp := &BufPool{size: size}
	bp.p.New = func() any { b := make([]byte, size); return &b }
	return bp
}

func (bp *BufPool) Size() int     { return bp.size }
func (bp *BufPool) Get() *[]byte  { return bp.p.Get().(*[]byte) }
func (bp *BufPool) Put(b *[]byte) { bp.p.Put(b) }

// ReadChunk fills buf from r. eof reports that r is exhausted, so a caller
// that gets eof on the first chunk can use a single-shot PUT.
func ReadChunk(r io.Reader, buf []byte) (n int, eof bool, err error) {
	n, err = io.ReadFull(r, buf)
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return n, true, nil
	case err != nil:
		return n, false, err
	}
	return n, false, nil
}

// UploadFunc uploads one part. Part numbers start at 1. data is only valid
// for the duration of the call.
type UploadFunc func(ctx context.Context, partNum int, data []byte) error

// UploadParts streams r as numbered parts to upload, keeping at most
// concurrency parts in memory/in flight. first is a chunk the caller already
// read (part 1) and is returned to pool when done. Returns total bytes.
//
// Memory per upload is bounded by concurrency * partSize, which is what makes
// multi-GB agent uploads safe on small pods.
func UploadParts(ctx context.Context, r io.Reader, first *[]byte, firstLen int,
	concurrency int, pool *BufPool, maxParts int, upload UploadFunc) (int64, error) {

	if concurrency < 1 {
		concurrency = 1
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var total int64

	dispatch := func(num int, buf *[]byte, n int) {
		wg.Add(1)
		go func() {
			defer func() { pool.Put(buf); <-sem; wg.Done() }()
			if err := upload(ctx, num, (*buf)[:n]); err != nil {
				cancel(err)
			}
		}()
	}

	sem <- struct{}{}
	dispatch(1, first, firstLen)
	total += int64(firstLen)

	var readErr error
	for num := 2; ; num++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			readErr = context.Cause(ctx)
		}
		if readErr != nil {
			break
		}
		if num > maxParts {
			<-sem
			readErr = errors.New("storage: object exceeds maximum part count; increase part size")
			cancel(readErr)
			break
		}
		buf := pool.Get()
		n, eof, err := ReadChunk(r, *buf)
		if err != nil {
			pool.Put(buf)
			<-sem
			readErr = err
			cancel(err)
			break
		}
		if n == 0 { // stream ended exactly on a part boundary
			pool.Put(buf)
			<-sem
			break
		}
		dispatch(num, buf, n)
		total += int64(n)
		if eof {
			break
		}
	}
	wg.Wait()
	if readErr != nil {
		return total, readErr
	}
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return total, err
	}
	if err := ctx.Err(); err != nil {
		return total, err
	}
	return total, nil
}
