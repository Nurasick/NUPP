package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Local stores objects as ordinary files below a root directory.
type Local struct {
	root string
}

// Local must satisfy Storage. This line fails to compile if it ever doesn't,
// which is a cheap, common Go idiom for checking interface conformance.
var _ Storage = (*Local)(nil)

// NewLocal returns a Local rooted at root, creating the directory if needed.
func NewLocal(root string) (*Local, error) {
	// 0o750: owner can read/write, group can read, everyone else nothing.
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("create storage root: %w", err)
	}
	return &Local{root: root}, nil
}

// path converts a key into a filesystem path inside root, rejecting any key
// that could point outside it (path traversal, spec R-ST-3).
func (l *Local) path(key string) (string, error) {
	// fs.ValidPath accepts only clean, relative, slash-separated paths: it
	// rejects "", leading "/", and any ".", ".." or empty element. We also
	// reject "\" and ":" ourselves, because on Windows `a\..\b` and `C:/x`
	// would otherwise be interpreted as a parent directory and a drive letter.
	// "." alone is "valid" to fs.ValidPath (it means the root) but is not an object.
	if key == "." || !fs.ValidPath(key) || strings.ContainsAny(key, `\:`) {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return filepath.Join(l.root, filepath.FromSlash(key)), nil
}

// Put writes r to key atomically.
//
// We write into a temporary file in the same directory and rename it into
// place only after every byte was written. A rename within one filesystem is
// atomic, so a concurrent reader sees either the old object or the complete
// new one, never a half-written file, and a failed upload leaves nothing.
func (l *Local) Put(_ context.Context, key string, r io.Reader) error {
	dst, err := l.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create object dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	// If anything below fails, delete the temp file. After a successful
	// rename the temp name no longer exists, so this is a harmless no-op.
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("write object: %w", err)
	}
	// Close can report write errors that were buffered, so check it.
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("move object into place: %w", err)
	}
	return nil
}

// Open returns the object stored at key.
func (l *Local) Open(_ context.Context, key string) (Object, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("open object: %w", err)
	}
	return f, nil // *os.File already implements Read, Seek and Close
}
