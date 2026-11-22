// Package s3 implements storage.Backend for any S3-compatible object store:
// AWS S3, MinIO, Hetzner Object Storage, Cloudflare R2, Wasabi, Ceph RGW...
//
// Uploads use our own streaming multipart implementation (see
// storage.UploadParts) rather than the SDK transfer manager so we control
// part size, concurrency, memory and the commit/abort decision.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/luka/fileflow/internal/storage"
)

const (
	minPartSize = 5 << 20 // S3 minimum for every part except the last
	maxParts    = 10000   // S3 hard limit
)

// Config for an S3-compatible backend.
type Config struct {
	Endpoint     string // empty for AWS; e.g. http://minio:9000, https://fsn1.your-objectstorage.com
	Region       string // "us-east-1" works for MinIO; Hetzner uses fsn1/nbg1/hel1
	Bucket       string
	AccessKey    string // optional; falls back to the default AWS credential chain
	SecretKey    string
	UsePathStyle bool // true for MinIO and most self-hosted stores
	PartSize     int64
	Concurrency  int
	CreateBucket bool // dev convenience
}

type Backend struct {
	client *s3.Client
	bucket string
	cfg    Config
	pool   *storage.BufPool
}

func New(ctx context.Context, c Config) (*Backend, error) {
	if c.Bucket == "" {
		return nil, errors.New("s3: bucket is required")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.PartSize < minPartSize {
		c.PartSize = 16 << 20
	}
	if c.Concurrency < 1 {
		c.Concurrency = 4
	}

	// The default SDK transport keeps few idle connections per host; for a
	// storage gateway connection reuse is one of the biggest latency wins.
	httpClient := awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
		t.MaxIdleConns = 512
		t.MaxIdleConnsPerHost = 256
		t.IdleConnTimeout = 90 * time.Second
	})

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(c.Region),
		config.WithHTTPClient(httpClient),
	}
	if c.AccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, "")))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: load config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.UsePathStyle
		// Newer SDKs add CRC checksums to every request by default; several
		// S3-compatible providers reject or mishandle them. We verify
		// integrity ourselves with SHA-256 end to end.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})

	b := &Backend{client: client, bucket: c.Bucket, cfg: c, pool: storage.NewBufPool(int(c.PartSize))}
	if c.CreateBucket {
		if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &c.Bucket}); err != nil {
			if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &c.Bucket}); err != nil {
				var owned *types.BucketAlreadyOwnedByYou
				if !errors.As(err, &owned) {
					return nil, fmt.Errorf("s3: create bucket: %w", err)
				}
			}
		}
	}
	return b, nil
}

func (b *Backend) Name() string { return "s3" }

func (b *Backend) Put(ctx context.Context, key string, r io.Reader, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	pool := b.pool
	// Grow parts for huge known-size uploads so we stay under 10k parts.
	if opts.Size > 0 && opts.Size/b.cfg.PartSize >= maxParts {
		ps := (opts.Size/(maxParts-1) + (1 << 20)) &^ ((1 << 20) - 1)
		pool = storage.NewBufPool(int(ps))
	}

	first := pool.Get()
	n, eof, err := storage.ReadChunk(r, *first)
	if err != nil {
		pool.Put(first)
		return storage.ObjectInfo{}, err
	}
	if eof {
		defer pool.Put(first)
		return b.putSingle(ctx, key, (*first)[:n], opts)
	}

	ct := contentType(opts.ContentType)
	created, err := b.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: &b.bucket, Key: &key, ContentType: &ct, Metadata: opts.Metadata,
	})
	if err != nil {
		pool.Put(first)
		return storage.ObjectInfo{}, mapErr(err)
	}
	uploadID := created.UploadId

	var mu sync.Mutex
	var parts []types.CompletedPart
	total, err := storage.UploadParts(ctx, r, first, n, b.cfg.Concurrency, pool, maxParts,
		func(ctx context.Context, num int, data []byte) error {
			out, err := b.client.UploadPart(ctx, &s3.UploadPartInput{
				Bucket: &b.bucket, Key: &key, UploadId: uploadID,
				PartNumber:    aws.Int32(int32(num)),
				Body:          bytes.NewReader(data),
				ContentLength: aws.Int64(int64(len(data))),
			})
			if err != nil {
				return fmt.Errorf("upload part %d: %w", num, err)
			}
			mu.Lock()
			parts = append(parts, types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(int32(num))})
			mu.Unlock()
			return nil
		})
	if err == nil && opts.Size >= 0 && total != opts.Size {
		err = fmt.Errorf("s3: short upload: got %d of %d bytes", total, opts.Size)
	}
	if err == nil && opts.BeforeCommit != nil {
		// Metadata is fixed at CreateMultipartUpload, so late metadata is
		// dropped here; the checksum verdict still decides commit vs abort.
		_, err = opts.BeforeCommit()
	}
	if err != nil {
		// Abort even if the request context is gone, or the parts linger
		// (and are billed) until a lifecycle rule cleans them up.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = b.client.AbortMultipartUpload(actx, &s3.AbortMultipartUploadInput{
			Bucket: &b.bucket, Key: &key, UploadId: uploadID,
		})
		return storage.ObjectInfo{}, mapErr(err)
	}

	sort.Slice(parts, func(i, j int) bool { return *parts[i].PartNumber < *parts[j].PartNumber })
	done, err := b.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &b.bucket, Key: &key, UploadId: uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return storage.ObjectInfo{}, mapErr(err)
	}
	return storage.ObjectInfo{
		Key: key, Size: total, ContentType: ct, ETag: trimETag(done.ETag),
		SHA256: opts.Metadata["sha256"], ModTime: time.Now().UTC(), Metadata: opts.Metadata,
	}, nil
}

func (b *Backend) putSingle(ctx context.Context, key string, data []byte, opts storage.PutOptions) (storage.ObjectInfo, error) {
	if opts.Size >= 0 && int64(len(data)) != opts.Size {
		return storage.ObjectInfo{}, fmt.Errorf("s3: short upload: got %d of %d bytes", len(data), opts.Size)
	}
	meta := opts.Metadata
	if opts.BeforeCommit != nil {
		extra, err := opts.BeforeCommit()
		if err != nil {
			return storage.ObjectInfo{}, err
		}
		meta = storage.MergeMeta(meta, extra)
	}
	ct := contentType(opts.ContentType)
	out, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &b.bucket, Key: &key, Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))), ContentType: &ct, Metadata: meta,
	})
	if err != nil {
		return storage.ObjectInfo{}, mapErr(err)
	}
	return storage.ObjectInfo{
		Key: key, Size: int64(len(data)), ContentType: ct, ETag: trimETag(out.ETag),
		SHA256: meta["sha256"], ModTime: time.Now().UTC(), Metadata: meta,
	}, nil
}

func (b *Backend) Get(ctx context.Context, key string, rng *storage.Range) (io.ReadCloser, storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	in := &s3.GetObjectInput{Bucket: &b.bucket, Key: &key}
	if rng != nil {
		in.Range = aws.String(rng.HTTPHeader())
	}
	out, err := b.client.GetObject(ctx, in)
	if err != nil {
		return nil, storage.ObjectInfo{}, mapErr(err)
	}
	size := aws.ToInt64(out.ContentLength)
	if total, ok := totalFromContentRange(aws.ToString(out.ContentRange)); ok {
		size = total
	}
	info := storage.ObjectInfo{
		Key: key, Size: size, ContentType: aws.ToString(out.ContentType),
		ETag: trimETag(out.ETag), ModTime: aws.ToTime(out.LastModified),
		Metadata: out.Metadata, SHA256: out.Metadata["sha256"],
	}
	return out.Body, info, nil
}

func (b *Backend) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	if err := storage.ValidateKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &b.bucket, Key: &key})
	if err != nil {
		return storage.ObjectInfo{}, mapErr(err)
	}
	return storage.ObjectInfo{
		Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		ETag: trimETag(out.ETag), ModTime: aws.ToTime(out.LastModified),
		Metadata: out.Metadata, SHA256: out.Metadata["sha256"],
	}, nil
}

func (b *Backend) Delete(ctx context.Context, key string) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b.bucket, Key: &key})
	if err = mapErr(err); errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return err
}

func (b *Backend) List(ctx context.Context, prefix string, limit int) ([]storage.ObjectInfo, error) {
	if limit <= 0 {
		limit = 1000
	}
	var out []storage.ObjectInfo
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: &b.bucket, Prefix: aws.String(prefix), MaxKeys: aws.Int32(int32(min(limit, 1000))),
	})
	for p.HasMorePages() && len(out) < limit {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		for _, o := range page.Contents {
			out = append(out, storage.ObjectInfo{
				Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size),
				ETag: trimETag(o.ETag), ModTime: aws.ToTime(o.LastModified),
			})
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return storage.ErrNotFound
	}
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		switch re.HTTPStatusCode() {
		case http.StatusNotFound:
			return storage.ErrNotFound
		case http.StatusRequestedRangeNotSatisfiable:
			return storage.ErrInvalidRange
		}
	}
	return err
}

func trimETag(s *string) string { return strings.Trim(aws.ToString(s), `"`) }

func contentType(ct string) string {
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

// totalFromContentRange parses "bytes 0-99/12345".
func totalFromContentRange(cr string) (int64, bool) {
	i := strings.LastIndexByte(cr, '/')
	if i < 0 || cr[i+1:] == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(cr[i+1:], 10, 64)
	return n, err == nil
}

var _ storage.Backend = (*Backend)(nil)
