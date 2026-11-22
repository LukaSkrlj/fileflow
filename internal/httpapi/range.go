package httpapi

import (
	"strconv"
	"strings"

	"github.com/luka/fileflow/internal/storage"
)

// parseRange resolves a single HTTP byte range against an object size.
// It returns (nil, nil) when the whole object should be served: no header,
// a non-bytes unit, or a multi-range request (RFC 9110 lets servers ignore
// those; download managers fall back to single ranges).
func parseRange(h string, size int64) (*storage.Range, error) {
	if h == "" {
		return nil, nil
	}
	spec, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return nil, nil
	}
	startS, endS, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return nil, storage.ErrInvalidRange
	}
	if size <= 0 {
		return nil, storage.ErrInvalidRange
	}
	if startS == "" { // suffix: last N bytes
		n, err := strconv.ParseInt(endS, 10, 64)
		if err != nil || n <= 0 {
			return nil, storage.ErrInvalidRange
		}
		n = min(n, size)
		return &storage.Range{Start: size - n, Length: n}, nil
	}
	start, err := strconv.ParseInt(startS, 10, 64)
	if err != nil || start < 0 || start >= size {
		return nil, storage.ErrInvalidRange
	}
	end := size - 1
	if endS != "" {
		end, err = strconv.ParseInt(endS, 10, 64)
		if err != nil || end < start {
			return nil, storage.ErrInvalidRange
		}
		end = min(end, size-1)
	}
	return &storage.Range{Start: start, Length: end - start + 1}, nil
}
