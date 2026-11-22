// Package local implements storage.Backend on a local (or mounted) filesystem.
// It is meant for development, tests and single-node deployments on a
// persistent volume (e.g. an Azure Disk or Hetzner Volume).
//
// Layout under Root:
//
//	<key>                 object bytes
//	.ff-meta/<key>.json   sidecar metadata (content type, sha256, user meta)
//	.ff-tmp/              in-flight uploads, renamed into place on commit
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/luka/fileflow/internal/storage"
)

const (
	metaDir = ".ff-meta"
	tmpDir  = ".ff-tmp"
)

// Backend stores objects as files under Root.
type Backend struct {
	root  string
	fsync bool
}

// New creates the root directories if needed. When fsync is true every
// upload is flushed to stable storage before it becomes visible.
func New(root string, fsync bool) (*Backend, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{abs, filepath.Join(abs, metaDir), filepath.Join(abs, tmpDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("local: create %s: %w", d, err)
		}
	}
	return &Backend{root: abs, fsync: fsync}, nil
}

func (b *Backend) Name() string { return "local" }

type sidecar struct {
	ContentType string            `json:"content_type,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Size and MtimeNs identify the data file this sidecar was written for
	// (rename keeps mtime). Stat ignores a sidecar that doesn't match, so a
	// racing upload or a crash between the two renames can't pair one
	// upload's bytes with another's checksum. Zero in pre-existing sidecars.
	Size    int64 `json:"size,omitempty"`
	MtimeNs int64 `json:"mtime_ns,omitempty"`
}

func (b *Backend) dataPath(key string) string {
	return filepath.Join(b.root, filepath.FromSlash(key))
}

func (b *Backend) metaPath(key string) string {
	return filepath.Join(b.root, metaDir, filepath.FromSlash(key)+".json")
}

func (b *Backend) Put(ctx context.Context, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	tmp, err := os.CreateTemp(filepath.Join(b.root, tmpDir), "up-*")
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	buf := make([]byte, 1<<20)
	n, err := io.CopyBuffer(tmp, ctxReader{ctx, r}, buf)
	if err != nil {
		return storage.ObjectInfo{}, fmt.Errorf("local: write: %w", err)
	}
	if opts.Size >= 0 && n != opts.Size {
		return storage.ObjectInfo{}, fmt.Errorf("local: short upload: got %d of %d bytes", n, opts.Size)
	}
	if b.fsync {
		if err := tmp.Sync(); err != nil {
			return storage.ObjectInfo{}, err
		}
	}
	if err := tmp.Close(); err != nil {
		return storage.ObjectInfo{}, err
	}

	meta := opts.Metadata
	if opts.BeforeCommit != nil {
		extra, err := opts.BeforeCommit()
		if err != nil {
			return storage.ObjectInfo{}, err
		}
		meta = storage.MergeMeta(meta, extra)
	}

	// Metadata first, then data: a crash in between leaves a stale sidecar,
	// never data without its checksum.
	fi, err := os.Stat(tmpName)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	sc, _ := json.Marshal(sidecar{ContentType: opts.ContentType, Metadata: meta,
		Size: fi.Size(), MtimeNs: fi.ModTime().UnixNano()})
	if err := writeFileAtomic(b.metaPath(key), sc, filepath.Join(b.root, tmpDir)); err != nil {
		return storage.ObjectInfo{}, err
	}
	dst := b.dataPath(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return storage.ObjectInfo{}, err
	}
	committed = true
	return b.Stat(ctx, key)
}

func (b *Backend) Get(ctx context.Context, key string, rng *storage.Range) (io.ReadCloser, storage.ObjectInfo, error) {
	info, err := b.Stat(ctx, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	f, err := os.Open(b.dataPath(key))
	if err != nil {
		return nil, storage.ObjectInfo{}, mapErr(err)
	}
	if rng == nil {
		return f, info, nil
	}
	if rng.Start < 0 || rng.Length <= 0 || rng.Start+rng.Length > info.Size {
		f.Close()
		return nil, storage.ObjectInfo{}, storage.ErrInvalidRange
	}
	return sectionReadCloser{io.NewSectionReader(f, rng.Start, rng.Length), f}, info, nil
}

func (b *Backend) Stat(_ context.Context, key string) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	fi, err := os.Stat(b.dataPath(key))
	if err != nil {
		return storage.ObjectInfo{}, mapErr(err)
	}
	if fi.IsDir() {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	info := storage.ObjectInfo{Key: key, Size: fi.Size(), ModTime: fi.ModTime().UTC()}
	if raw, err := os.ReadFile(b.metaPath(key)); err == nil {
		var sc sidecar
		if json.Unmarshal(raw, &sc) == nil &&
			(sc.MtimeNs == 0 || (sc.Size == fi.Size() && sc.MtimeNs == fi.ModTime().UnixNano())) {
			info.ContentType = sc.ContentType
			info.Metadata = sc.Metadata
			info.SHA256 = sc.Metadata["sha256"]
		}
	}
	if info.SHA256 != "" {
		info.ETag = info.SHA256
	} else {
		info.ETag = fmt.Sprintf("%x-%x", fi.Size(), fi.ModTime().UnixNano())
	}
	return info, nil
}

func (b *Backend) Delete(_ context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	if err := os.Remove(b.dataPath(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(b.metaPath(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (b *Backend) List(ctx context.Context, prefix string, limit int) ([]storage.ObjectInfo, error) {
	// Reject "../" etc.: the walk below starts at root+prefix and would
	// otherwise traverse directories outside root.
	if prefix != "" && storage.ValidateKey(prefix+"x") != nil {
		return nil, storage.ErrInvalidKey
	}
	if limit <= 0 {
		limit = 1000
	}
	// Start walking at the deepest directory fully contained in prefix.
	start := b.root
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		start = filepath.Join(b.root, filepath.FromSlash(prefix[:i]))
	}
	var keys []string
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".ff-") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(b.root, p)
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipDir) {
		return nil, err
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]storage.ObjectInfo, 0, len(keys))
	for _, k := range keys {
		if info, err := b.Stat(ctx, k); err == nil {
			out = append(out, info)
		}
	}
	return out, nil
}

func writeFileAtomic(dst string, data []byte, tmpRoot string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(tmpRoot, "meta-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), dst)
}

func mapErr(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return storage.ErrNotFound
	}
	return err
}

type sectionReadCloser struct {
	io.Reader
	c io.Closer
}

func (s sectionReadCloser) Close() error { return s.c.Close() }

// ctxReader stops a long copy promptly when the request is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

var _ storage.Backend = (*Backend)(nil)
