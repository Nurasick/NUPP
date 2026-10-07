package testutil

import (
	"os"
	"testing"
	"time"
)

// TempDir is like t.TempDir, but its cleanup retries for a short while.
//
// On Windows, antivirus or the search indexer may briefly keep a file that a
// test just wrote open, and t.TempDir's cleanup then fails the test with
// "The directory is not empty". Retrying for up to ~1s removes that flake
// without hiding real leaks: a file we forgot to Close would stay locked and
// still be reported (as a log line).
func TempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nupp-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() {
		var err error
		for range 20 {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("could not remove temp dir %s: %v", dir, err)
	})
	return dir
}
