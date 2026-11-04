// Package storage uploads recording output to local disk or S3-compatible
// object storage (AWS S3, MinIO, R2, GCS interop).
//
// Ownership: WP4.
package storage

import (
	"context"
	"io"

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
)

// NewLocal returns a Storage writing under root (atomic rename on write).
func NewLocal(root string) (Storage, error) {
	return nil, errs.NotImplemented("storage.NewLocal")
}

// NewS3 returns an S3-compatible Storage.
func NewS3(ctx context.Context, cfg *tesserav1.S3Storage) (Storage, error) {
	return nil, errs.NotImplemented("storage.NewS3")
}

// FromOptions builds a Storage from per-request options, falling back to def.
func FromOptions(ctx context.Context, opts *tesserav1.StorageOptions, def Storage) (Storage, error) {
	if opts == nil || opts.GetBackend() == nil {
		return def, nil
	}
	switch b := opts.GetBackend().(type) {
	case *tesserav1.StorageOptions_S3:
		return NewS3(ctx, b.S3)
	case *tesserav1.StorageOptions_Local:
		return NewLocal(b.Local.GetDirectory())
	}
	return nil, errs.New(errs.CodeInvalidArgument, "unknown storage backend")
}
