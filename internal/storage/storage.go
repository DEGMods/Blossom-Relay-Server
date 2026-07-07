// Package storage is the blob backend abstraction. Blobs are content-addressed by
// their lowercase hex SHA-256. Objects are keyed "<sha256>.<ext>" to match the
// existing deg-blossom-storage bucket. The interface is backend-agnostic so the
// same node runs against Cloudflare R2 today and local disk / MinIO / Garage later.
package storage

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = errors.New("blob not found")

// StatInfo is lightweight blob metadata.
type StatInfo struct {
	Size        int64
	ContentType string
	Key         string // the resolved object key, e.g. "<sha256>.zip"
}

// Storage is a content-addressed blob store.
type Storage interface {
	// Load returns a streaming reader for the blob. ext may be empty (a bare-hash
	// request), in which case the implementation resolves the actual object.
	// Returns ErrNotFound if absent. The caller MUST Close the reader.
	Load(ctx context.Context, sha256, ext string) (io.ReadSeekCloser, error)

	// Store writes the blob under "<sha256>.<ext>" with the given content type.
	// r is streamed (never fully buffered); size is the exact length in bytes.
	Store(ctx context.Context, sha256, ext, contentType string, r io.Reader, size int64) error

	// Delete removes the blob. Absent blobs are not an error.
	Delete(ctx context.Context, sha256, ext string) error

	// Has reports whether the blob exists.
	Has(ctx context.Context, sha256, ext string) (bool, error)

	// Stat returns blob metadata (size, content type). Returns ErrNotFound if absent.
	Stat(ctx context.Context, sha256, ext string) (StatInfo, error)
}
