package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Nurasick/NUPP/api/internal/storage"
)

func newLocal(t *testing.T) *storage.Local {
	t.Helper()
	// t.TempDir is deleted automatically when the test ends.
	s, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return s
}

func readAll(t *testing.T, s storage.Storage, key string) []byte {
	t.Helper()
	obj, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open(%q): %v", key, err)
	}
	defer obj.Close()
	got, err := io.ReadAll(obj)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return got
}

// AC-25 / R-ST-4
func TestLocal_PutThenOpenRoundTrips(t *testing.T) {
	s := newLocal(t)
	want := []byte("hello file")
	if err := s.Put(context.Background(), "materials/abc/0.pdf", bytes.NewReader(want)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := readAll(t, s, "materials/abc/0.pdf"); !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// AC-25 / R-ST-4
func TestLocal_PutReplacesExistingObject(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	if err := s.Put(ctx, "k/a.txt", bytes.NewReader([]byte("old"))); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := s.Put(ctx, "k/a.txt", bytes.NewReader([]byte("new"))); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if got := readAll(t, s, "k/a.txt"); string(got) != "new" {
		t.Fatalf("got %q, want new", got)
	}
}

// R-ST-4: a failed write must not leave a partial object behind.
func TestLocal_FailedPutLeavesNoObject(t *testing.T) {
	s := newLocal(t)
	broken := io.MultiReader(bytes.NewReader([]byte("partial")), failingReader{})
	if err := s.Put(context.Background(), "k/broken.pdf", broken); err == nil {
		t.Fatal("expected Put to fail when the reader fails")
	}
	if _, err := s.Open(context.Background(), "k/broken.pdf"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Open after failed Put: err = %v, want ErrNotFound", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

// AC-25 / R-ST-5
func TestLocal_OpenMissingReturnsErrNotFound(t *testing.T) {
	_, err := newLocal(t).Open(context.Background(), "materials/missing.pdf")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// AC-24 / R-ST-3
func TestLocal_RejectsKeysThatEscapeTheRoot(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	for _, key := range []string{"", ".", "../evil", "/abs/path", "a/../../b", `a\..\b`, "C:/windows", "a//b", "a/./b", "a/"} {
		if err := s.Put(ctx, key, bytes.NewReader([]byte("x"))); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Put(%q) err = %v, want ErrInvalidKey", key, err)
		}
		if _, err := s.Open(ctx, key); !errors.Is(err, storage.ErrInvalidKey) {
			t.Errorf("Open(%q) err = %v, want ErrInvalidKey", key, err)
		}
	}
}
