// Package azure implements storage.Backend on Azure Blob Storage (block blobs).
//
// Large uploads are staged as blocks (StageBlock) and made visible atomically
// with CommitBlockList, which mirrors S3 multipart and lets us attach the
// final SHA-256 as metadata at commit time. Uncommitted blocks from aborted
// uploads are garbage-collected by Azure after 7 days.
package azure

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/luka/fileflow/internal/storage"
)

const maxBlocks = 50000

// Config for Azure Blob Storage. Use ConnectionString for Azurite/dev, or
// AccountURL (https://<account>.blob.core.windows.net) with workload/managed
// identity in AKS via DefaultAzureCredential.
type Config struct {
	ConnectionString string
	AccountURL       string
	Container        string
	BlockSize        int64
	Concurrency      int
	CreateContainer  bool
}

type Backend struct {
	cc   *container.Client
	cfg  Config
	pool *storage.BufPool
}

func New(ctx context.Context, c Config) (*Backend, error) {
	if c.Container == "" {
		return nil, errors.New("azure: container is required")
	}
	if c.BlockSize < 1<<20 {
		c.BlockSize = 16 << 20
	}
	if c.Concurrency < 1 {
		c.Concurrency = 4
	}
	var (
		client *azblob.Client
		err    error
	)
	switch {
	case c.ConnectionString != "":
		client, err = azblob.NewClientFromConnectionString(c.ConnectionString, nil)
	case c.AccountURL != "":
		cred, cerr := azidentity.NewDefaultAzureCredential(nil)
		if cerr != nil {
			return nil, fmt.Errorf("azure: credential: %w", cerr)
		}
		client, err = azblob.NewClient(c.AccountURL, cred, nil)
	default:
		return nil, errors.New("azure: set AZURE_STORAGE_CONNECTION_STRING or AZURE_STORAGE_ACCOUNT_URL")
	}
	if err != nil {
		return nil, fmt.Errorf("azure: client: %w", err)
	}
	cc := client.ServiceClient().NewContainerClient(c.Container)
	if c.CreateContainer {
		if _, err := cc.Create(ctx, nil); err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
			return nil, fmt.Errorf("azure: create container: %w", err)
		}
	}
	return &Backend{cc: cc, cfg: c, pool: storage.NewBufPool(int(c.BlockSize))}, nil
}

func (b *Backend) Name() string { return "azure" }

func (b *Backend) Put(ctx context.Context, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	bb := b.cc.NewBlockBlobClient(key)
	pool := b.pool
	if opts.Size > 0 && opts.Size/b.cfg.BlockSize >= maxBlocks {
		bs := (opts.Size/(maxBlocks-1) + (1 << 20)) &^ ((1 << 20) - 1)
		pool = storage.NewBufPool(int(bs))
	}
	ct := contentType(opts.ContentType)
	headers := &blob.HTTPHeaders{BlobContentType: &ct}

	first := pool.Get()
	n, eof, err := storage.ReadChunk(r, *first)
	if err != nil {
		pool.Put(first)
		return storage.ObjectInfo{}, err
	}
	if eof {
		defer pool.Put(first)
		if opts.Size >= 0 && int64(n) != opts.Size {
			return storage.ObjectInfo{}, fmt.Errorf("azure: short upload: got %d of %d bytes", n, opts.Size)
		}
		meta, err := commitMeta(opts)
		if err != nil {
			return storage.ObjectInfo{}, err
		}
		out, err := bb.Upload(ctx, streaming.NopCloser(bytes.NewReader((*first)[:n])),
			&blockblob.UploadOptions{HTTPHeaders: headers, Metadata: toAzMeta(meta)})
		if err != nil {
			return storage.ObjectInfo{}, mapErr(err)
		}
		return info(key, int64(n), ct, out.ETag, out.LastModified, meta), nil
	}

	uploadID := rand.Text() // unique per upload: concurrent same-key uploads must not share block IDs
	var mu sync.Mutex
	ids := map[int]string{}
	total, err := storage.UploadParts(ctx, r, first, n, b.cfg.Concurrency, pool, maxBlocks,
		func(ctx context.Context, num int, data []byte) error {
			id := blockID(uploadID, num)
			if _, err := bb.StageBlock(ctx, id, streaming.NopCloser(bytes.NewReader(data)), nil); err != nil {
				return fmt.Errorf("stage block %d: %w", num, err)
			}
			mu.Lock()
			ids[num] = id
			mu.Unlock()
			return nil
		})
	if err != nil {
		return storage.ObjectInfo{}, mapErr(err)
	}
	if opts.Size >= 0 && total != opts.Size {
		return storage.ObjectInfo{}, fmt.Errorf("azure: short upload: got %d of %d bytes", total, opts.Size)
	}
	meta, err := commitMeta(opts)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	list := make([]string, len(ids))
	for i := range list {
		list[i] = ids[i+1]
	}
	out, err := bb.CommitBlockList(ctx, list, &blockblob.CommitBlockListOptions{
		HTTPHeaders: headers, Metadata: toAzMeta(meta),
	})
	if err != nil {
		return storage.ObjectInfo{}, mapErr(err)
	}
	return info(key, total, ct, out.ETag, out.LastModified, meta), nil
}

func (b *Backend) Get(ctx context.Context, key string, rng *storage.Range) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	o := &blob.DownloadStreamOptions{}
	if rng != nil {
		o.Range = blob.HTTPRange{Offset: rng.Start, Count: rng.Length}
	}
	resp, err := b.cc.NewBlobClient(key).DownloadStream(ctx, o)
	if err != nil {
		return nil, storage.ObjectInfo{}, mapErr(err)
	}
	size := deref(resp.ContentLength)
	if total, ok := totalFromContentRange(deref(resp.ContentRange)); ok {
		size = total
	}
	meta := fromAzMeta(resp.Metadata)
	oi := info(key, size, deref(resp.ContentType), resp.ETag, resp.LastModified, meta)
	// RetryReader transparently resumes the stream from the last byte read
	// if the connection to Azure drops mid-download.
	body := resp.NewRetryReader(ctx, &blob.RetryReaderOptions{MaxRetries: 3})
	return body, oi, nil
}

func (b *Backend) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	p, err := b.cc.NewBlobClient(key).GetProperties(ctx, nil)
	if err != nil {
		return storage.ObjectInfo{}, mapErr(err)
	}
	return info(key, deref(p.ContentLength), deref(p.ContentType), p.ETag, p.LastModified, fromAzMeta(p.Metadata)), nil
}

func (b *Backend) Delete(ctx context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	_, err := b.cc.NewBlobClient(key).Delete(ctx, nil)
	if err = mapErr(err); errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return err
}

func (b *Backend) List(ctx context.Context, prefix string, limit int) ([]storage.ObjectInfo, error) {
	if limit <= 0 {
		limit = 1000
	}
	max := int32(min(limit, 5000))
	pager := b.cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Prefix: &prefix, MaxResults: &max, Include: container.ListBlobsInclude{Metadata: true},
	})
	var out []storage.ObjectInfo
	for pager.More() && len(out) < limit {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		for _, it := range page.Segment.BlobItems {
			if it.Name == nil || it.Properties == nil {
				continue
			}
			p := it.Properties
			out = append(out, info(*it.Name, deref(p.ContentLength), deref(p.ContentType), p.ETag, p.LastModified, fromAzMeta(it.Metadata)))
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func commitMeta(opts storage.PutOptions) (map[string]string, error) {
	meta := opts.Metadata
	if opts.BeforeCommit != nil {
		extra, err := opts.BeforeCommit()
		if err != nil {
			return nil, err
		}
		meta = storage.MergeMeta(meta, extra)
	}
	return meta, nil
}

// blockID must be base64 and the same length for every block of a blob.
// Azure keeps one set of uncommitted blocks per blob, so the per-upload id
// stops two concurrent uploads to one key from overwriting each other's blocks.
func blockID(uploadID string, n int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("ff-%s-%08d", uploadID, n)))
}

func info(key string, size int64, ct string, etag *azcore.ETag, mod *time.Time, meta map[string]string) storage.ObjectInfo {
	oi := storage.ObjectInfo{Key: key, Size: size, ContentType: ct, Metadata: meta, SHA256: meta["sha256"]}
	if etag != nil {
		oi.ETag = strings.Trim(string(*etag), `"`)
	}
	if mod != nil {
		oi.ModTime = mod.UTC()
	}
	return oi
}

// toAzMeta: see fromAzMeta.
func toAzMeta(m map[string]string) map[string]*string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]*string, len(m))
	for k, v := range m {
		v := v
		out[strings.ReplaceAll(k, "-", "_")] = &v
	}
	return out
}

// Azure metadata names must be C# identifiers, so our portable
// lower-case-with-hyphens keys ("client-kind") are stored with underscores.
// fromAzMeta reverses that and lower-cases keys, because the SDK returns them
// header-canonicalised ("Client_kind").
func fromAzMeta(m map[string]*string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v != nil {
			out[strings.ReplaceAll(strings.ToLower(k), "_", "-")] = *v
		}
	}
	return out
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return storage.ErrNotFound
	}
	if bloberror.HasCode(err, bloberror.InvalidRange) {
		return storage.ErrInvalidRange
	}
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		switch re.StatusCode {
		case http.StatusNotFound:
			return storage.ErrNotFound
		case http.StatusRequestedRangeNotSatisfiable:
			return storage.ErrInvalidRange
		}
	}
	return err
}

func contentType(ct string) string {
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

func totalFromContentRange(cr string) (int64, bool) {
	i := strings.LastIndexByte(cr, '/')
	if i < 0 || cr[i+1:] == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(cr[i+1:], 10, 64)
	return n, err == nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

var _ storage.Backend = (*Backend)(nil)
