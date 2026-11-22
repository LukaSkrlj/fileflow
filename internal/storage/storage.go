// Package storage defines the backend-neutral blob storage contract used by
// the HTTP layer. Every backend (local disk, S3-compatible, Azure Blob) must
// satisfy Backend and pass the conformance suite in storagetest.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	// ErrNotFound is returned when the key does not exist.
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalidKey is returned for keys that are empty, too long or unsafe.
	ErrInvalidKey = errors.New("storage: invalid key")
	// ErrInvalidRange is returned when a byte range cannot be satisfied.
	ErrInvalidRange = errors.New("storage: invalid range")
)

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key         string            `json:"key"`
	Size        int64             `json:"size"`
	ContentType string            `json:"content_type,omitempty"`
	ETag        string            `json:"etag,omitempty"`
	SHA256      string            `json:"sha256,omitempty"`
	ModTime     time.Time         `json:"mod_time"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Range is an absolute, already-validated byte range: [Start, Start+Length).
type Range struct {
	Start  int64
	Length int64
}

// End returns the inclusive last byte offset, as used by HTTP Range headers.
func (r Range) End() int64 { return r.Start + r.Length - 1 }

// HTTPHeader renders the range as an HTTP Range header value (bytes=a-b).
func (r Range) HTTPHeader() string { return fmt.Sprintf("bytes=%d-%d", r.Start, r.End()) }

// PutOptions controls an upload.
type PutOptions struct {
	ContentType string
	// Size is the expected size in bytes, or -1 when unknown (chunked upload).
	Size int64
	// Metadata is stored with the object. Keys must match [a-z0-9-] to be
	// portable: S3 lower-cases names and Azure only allows identifiers.
	Metadata map[string]string
	// BeforeCommit, when set, is called after the whole body has been read
	// and before the object becomes visible to readers. Returning an error
	// aborts the upload, so a failed checksum never produces a visible
	// object. Returned metadata is merged in when the backend can still set
	// metadata at commit time (local, Azure, S3 single-part).
	BeforeCommit func() (map[string]string, error)
}

// Backend is the minimal blob store contract.
type Backend interface {
	// Name identifies the backend in logs and metrics ("local", "s3", "azure").
	Name() string
	// Put streams r into key. Implementations must not buffer the whole body
	// in memory; bounded per-part buffers are fine.
	Put(ctx context.Context, key string, r io.Reader, opts PutOptions) (ObjectInfo, error)
	// Get opens key for reading. rng == nil means the whole object.
	Get(ctx context.Context, key string, rng *Range) (io.ReadCloser, ObjectInfo, error)
	// Stat returns metadata without reading content.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns up to limit objects whose key starts with prefix.
	List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error)
}

// MaxKeyLen matches the S3 limit (1024 bytes) so keys are portable.
const MaxKeyLen = 1024

// ValidateKey rejects keys that are not portable across backends or could
// escape a filesystem root.
func ValidateKey(key string) error {
	if key == "" || len(key) > MaxKeyLen {
		return ErrInvalidKey
	}
	if strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return ErrInvalidKey
	}
	if strings.ContainsAny(key, "\\\x00") {
		return ErrInvalidKey
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ErrInvalidKey
		}
		// Reserved for the local backend's sidecar and temp dirs. Case-
		// insensitive: on macOS/Windows ".FF-TMP" opens the real .ff-tmp.
		if strings.HasPrefix(strings.ToLower(seg), ".ff-") {
			return ErrInvalidKey
		}
	}
	return nil
}

// MergeMeta returns a new map with b applied on top of a.
func MergeMeta(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
