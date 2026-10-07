// Package storage saves and loads uploaded files.
//
// Handlers depend only on the small Storage interface below, never on the
// local-disk implementation directly. That's the seam that lets us move files
// to MinIO, Cloudflare R2 or S3 later by writing one new type, without
// touching any handler (spec R-ST-1).
package storage

import (
	"context"
	"errors"
	"io"
)

// Sentinel errors. Callers test for them with errors.Is, which also works
// when the error has been wrapped with extra context via fmt.Errorf("%w").
var (
	ErrNotFound   = errors.New("storage: object not found")
	ErrInvalidKey = errors.New("storage: invalid key")
)

// Object is an opened stored file.
//
// It must support Seek (not just Read) because http.ServeContent needs to jump
// around the file to answer HTTP Range requests — PDF viewers download large
// PDFs in chunks that way.
type Object interface {
	io.ReadSeekCloser
}

// Storage stores objects under slash-separated keys such as
// "materials/<material_id>/<file_id>.pdf".
type Storage interface {
	// Put stores everything read from r under key, replacing any existing
	// object. Readers never see a half-written object.
	Put(ctx context.Context, key string, r io.Reader) error
	// Open returns the object at key, or an error wrapping ErrNotFound.
	// The caller must Close it.
	Open(ctx context.Context, key string) (Object, error)
}
