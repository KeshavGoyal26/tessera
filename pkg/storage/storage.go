// Package storage uploads recording output to local disk or S3-compatible
// object storage (AWS S3, MinIO, R2, GCS interop).
//
// Ownership: WP4.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// Storage stores objects by key ("<prefix>/<recording>/<track>/seg_00001.m4s").
type Storage interface {
	// Put stores r (size bytes, -1 if unknown) and returns its location
	// (file path or s3://bucket/key). Implementations must be safe for
	// concurrent use and must overwrite existing keys (playlists).
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (location string, err error)
	Delete(ctx context.Context, key string) error
	// URL returns a public/HTTP URL for key when configured, else "".
	URL(key string) string
	// Check verifies connectivity/permissions (readiness).
	Check(ctx context.Context) error
}

// Content types for HLS objects.
const (
	ContentTypePlaylist = "application/vnd.apple.mpegurl"
	ContentTypeSegment  = "video/iso.segment"
	ContentTypeInit     = "video/mp4"
	ContentTypeJSON     = "application/json"
)

// cleanKey rejects keys that would escape the storage root.
func cleanKey(key string) (string, error) {
	k := strings.TrimLeft(filepath.ToSlash(filepath.Clean("/"+key)), "/")
	if k == "" || strings.Contains(key, "..") {
		return "", errs.New(errs.CodeInvalidArgument, "invalid storage key %q", key)
	}
	return k, nil
}

// LocalOption customises NewLocal.
type LocalOption func(*local)

// WithBaseURL sets the public URL prefix returned by URL().
func WithBaseURL(u string) LocalOption {
	return func(l *local) { l.baseURL = strings.TrimRight(u, "/") }
}

type local struct {
	root    string
	baseURL string
}

// NewLocal returns a Storage writing under root (atomic rename on write).
func NewLocal(root string, opts ...LocalOption) (Storage, error) {
	if root == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "local storage root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	l := &local{root: abs}
	for _, o := range opts {
		o(l)
	}
	return l, nil
}

func (l *local) Put(ctx context.Context, key string, r io.Reader, _ int64, _ string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(l.root, filepath.FromSlash(k))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_, err = io.Copy(f, &ctxReader{ctx, r})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func (l *local) Delete(_ context.Context, key string) error {
	k, err := cleanKey(key)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(l.root, filepath.FromSlash(k)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *local) URL(key string) string {
	if l.baseURL == "" {
		return ""
	}
	return l.baseURL + "/" + strings.TrimLeft(key, "/")
}

func (l *local) Check(context.Context) error {
	f, err := os.CreateTemp(l.root, ".check-*")
	if err != nil {
		return err
	}
	_ = f.Close()
	return os.Remove(f.Name())
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// S3Config configures an S3-compatible Storage.
type S3Config struct {
	Bucket, Region, Endpoint, AccessKey, Secret, Prefix string
	ForcePathStyle                                      bool
	// PublicBaseURL is returned by URL() (e.g. a CDN); default derives from endpoint.
	PublicBaseURL string
	// PartSize for multipart uploads (default 16 MiB, minimum 5 MiB).
	PartSize uint64
	// Attempts per Put including the first (default 4) and base backoff (default 500 ms).
	Attempts int
	Backoff  time.Duration
}

type s3store struct {
	cfg S3Config
	cl  *minio.Client
}

// NewS3 returns an S3-compatible Storage.
func NewS3(ctx context.Context, p *tesserav1.S3Storage) (Storage, error) {
	return NewS3FromConfig(ctx, S3Config{
		Bucket: p.GetBucket(), Region: p.GetRegion(), Endpoint: p.GetEndpoint(), AccessKey: p.GetAccessKey(),
		Secret: p.GetSecret(), Prefix: p.GetPrefix(), ForcePathStyle: p.GetForcePathStyle(),
	})
}

// NewS3FromConfig creates the Storage from explicit settings.
func NewS3FromConfig(_ context.Context, cfg S3Config) (Storage, error) {
	if cfg.Bucket == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "s3 bucket is required")
	}
	ep := cfg.Endpoint
	secure := true
	if ep == "" {
		ep = "s3.amazonaws.com"
	} else if strings.Contains(ep, "://") {
		u, err := url.Parse(ep)
		if err != nil {
			return nil, errs.New(errs.CodeInvalidArgument, "bad s3 endpoint: %v", err)
		}
		ep, secure = u.Host, u.Scheme == "https"
	}
	lookup := minio.BucketLookupAuto
	if cfg.ForcePathStyle {
		lookup = minio.BucketLookupPath
	}
	cl, err := minio.New(ep, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.Secret, ""),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, err
	}
	if cfg.PartSize == 0 {
		cfg.PartSize = 16 << 20
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = 4
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = 500 * time.Millisecond
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	if cfg.PublicBaseURL == "" && cfg.Endpoint != "" {
		scheme := "https"
		if !secure {
			scheme = "http"
		}
		cfg.PublicBaseURL = scheme + "://" + ep + "/" + cfg.Bucket
	}
	return &s3store{cfg: cfg, cl: cl}, nil
}

func (s *s3store) objectKey(key string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	if s.cfg.Prefix != "" {
		k = s.cfg.Prefix + "/" + k
	}
	return k, nil
}

// Retry runs fn up to attempts times with exponential backoff.
func Retry(ctx context.Context, attempts int, base time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if ctx.Err() != nil || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(base << i):
		}
	}
	return fmt.Errorf("after %d attempts: %w", attempts, err)
}

func retryable(err error) bool {
	var perm permanent
	if errors.As(err, &perm) {
		return false
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		switch er.StatusCode {
		case 400, 401, 403, 404, 405:
			return false
		}
	}
	return true
}

type permanent struct{ error }

func (s *s3store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	k, err := s.objectKey(key)
	if err != nil {
		return "", err
	}
	// Retrying needs a replayable body.
	rs, ok := r.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(&ctxReader{ctx, r})
		if err != nil {
			return "", err
		}
		rs, size = bytes.NewReader(b), int64(len(b))
	}
	err = Retry(ctx, s.cfg.Attempts, s.cfg.Backoff, func() error {
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return permanent{err}
		}
		_, err := s.cl.PutObject(ctx, s.cfg.Bucket, k, rs, size, minio.PutObjectOptions{
			ContentType: contentType, PartSize: s.cfg.PartSize, SendContentMd5: false,
		})
		return err
	})
	if err != nil {
		return "", err
	}
	return "s3://" + s.cfg.Bucket + "/" + k, nil
}

func (s *s3store) Delete(ctx context.Context, key string) error {
	k, err := s.objectKey(key)
	if err != nil {
		return err
	}
	return s.cl.RemoveObject(ctx, s.cfg.Bucket, k, minio.RemoveObjectOptions{})
}

func (s *s3store) URL(key string) string {
	k, err := s.objectKey(key)
	if err != nil || s.cfg.PublicBaseURL == "" {
		return ""
	}
	return strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/" + k
}

func (s *s3store) Check(ctx context.Context) error {
	ok, err := s.cl.BucketExists(ctx, s.cfg.Bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bucket %q does not exist", s.cfg.Bucket)
	}
	return nil
}

// FromOptions builds a Storage from per-request options, falling back to def.
// Local directories are not handled here: the caller resolves them below its
// configured root, so a request can never name an arbitrary path.
func FromOptions(ctx context.Context, opts *tesserav1.StorageOptions, def Storage) (Storage, error) {
	if opts == nil || opts.GetBackend() == nil {
		return def, nil
	}
	switch b := opts.GetBackend().(type) {
	case *tesserav1.StorageOptions_S3:
		return NewS3(ctx, b.S3)
	case *tesserav1.StorageOptions_Local:
		return nil, errs.New(errs.CodeInvalidArgument, "local storage must be resolved below the recorder root")
	}
	return nil, errs.New(errs.CodeInvalidArgument, "unknown storage backend")
}
