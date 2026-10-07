package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// logLines decodes each JSON log line written to buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		lines = append(lines, m)
	}
	return lines
}

// AC-29 / R-OPS-2, R-OPS-3
func TestPanicIsRecoveredLoggedOnceAndServerKeepsServing(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	mux.HandleFunc("GET /fine", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := withMiddleware(logger, mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom?secret=1", nil))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d content-type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// The same handler keeps working after a panic.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fine", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("request after panic: status = %d", rec.Code)
	}

	var requests, panics int
	for _, l := range logLines(t, &buf) {
		switch l["msg"] {
		case "request":
			requests++
			if l["path"] == "/boom" && l["status"] != float64(500) {
				t.Errorf("panicking request logged with status %v, want 500", l["status"])
			}
		case "panic":
			panics++
			if l["method"] != "GET" || l["path"] != "/boom" || l["stack"] == nil {
				t.Errorf("panic log missing details: %v", l)
			}
		}
	}
	if requests != 2 || panics != 1 {
		t.Errorf("got %d request logs and %d panic logs, want 2 and 1", requests, panics)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("query string must not be logged: %s", buf.String())
	}
}

// readerFromRecorder is a ResponseWriter that also implements io.ReaderFrom,
// like the real one in net/http (which uses it for zero-copy sendfile).
type readerFromRecorder struct {
	*httptest.ResponseRecorder
	used bool
}

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.used = true
	return io.Copy(r.ResponseRecorder, src)
}

// AC-30 / R-OPS-7
func TestStatusRecorder_PreservesReaderFrom(t *testing.T) {
	inner := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := withMiddleware(slog.New(slog.DiscardHandler), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// io.Copy prefers the source's WriteTo, then the destination's
		// ReadFrom. Wrapping the reader in a bare struct hides strings.Reader's
		// WriteTo, so ReadFrom is used if the middleware exposes it (an
		// *os.File, which is what file downloads copy from, has no WriteTo).
		_, _ = io.Copy(w, struct{ io.Reader }{strings.NewReader("payload")})
	}))
	h.ServeHTTP(inner, httptest.NewRequest(http.MethodGet, "/", nil))

	if !inner.used {
		t.Fatal("middleware hid io.ReaderFrom from the handler")
	}
	if inner.Body.String() != "payload" {
		t.Fatalf("body = %q", inner.Body.String())
	}
}

// Review fix: a panic after the response has started must abort the
// connection (so the client sees a broken download, not a "200" with JSON
// glued onto half a file), and the request must still be logged once.
func TestPanicAfterResponseStartedAbortsAndIsLogged(t *testing.T) {
	var buf bytes.Buffer
	h := withMiddleware(slog.New(slog.NewJSONHandler(&buf, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial file"))
		panic("disk read failed")
	}))
	rec := httptest.NewRecorder()

	aborted := func() (v any) {
		defer func() { v = recover() }()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/files/x", nil))
		return nil
	}()

	if aborted != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler", aborted)
	}
	if strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("JSON error appended to a started response: %q", rec.Body.String())
	}
	var requests, panics int
	for _, l := range logLines(t, &buf) {
		switch l["msg"] {
		case "request":
			requests++
			if l["aborted"] != true {
				t.Errorf("aborted request not marked as such: %v", l)
			}
		case "panic":
			panics++
		}
	}
	if requests != 1 || panics != 1 {
		t.Errorf("got %d request logs and %d panic logs, want 1 and 1", requests, panics)
	}
}

// Review fix: if a handler set file headers and then panicked before writing,
// the 500 must not carry them (no caching an error as if it were the file).
func TestPanicBeforeWriteDropsResponseSpecificHeaders(t *testing.T) {
	h := withMiddleware(slog.New(slog.DiscardHandler), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hdr := w.Header()
		hdr.Set("ETag", `"abc"`)
		hdr.Set("Cache-Control", "public, max-age=3600")
		hdr.Set("Content-Disposition", `inline; filename="x.pdf"`)
		hdr.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, name := range []string{"ETag", "Cache-Control", "Content-Disposition", "Content-Security-Policy"} {
		if v := rec.Header().Get(name); v != "" {
			t.Errorf("500 response still carries %s: %q", name, v)
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("security headers must survive")
	}
}
