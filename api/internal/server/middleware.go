package server

// Middleware = a function that takes an http.Handler and returns a new
// http.Handler wrapping it, so code can run before and after every request.
// They nest like onion layers; see withMiddleware for the order.

import (
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/Nurasick/NUPP/api/internal/httpx"
)

// withMiddleware wraps h in all middleware, outermost first:
//
//	logRequests → recoverPanics → securityHeaders → h
//
// logRequests is outermost so that even a request whose handler panicked is
// logged exactly once, with the 500 that recoverPanics wrote (R-OPS-2).
func withMiddleware(logger *slog.Logger, h http.Handler) http.Handler {
	return logRequests(logger, recoverPanics(logger, securityHeaders(h)))
}

// statusRecorder wraps a ResponseWriter to remember the status code and
// whether the response has started, which net/http doesn't expose.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	started bool // true once the status line has been sent to the client
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.started { // only the first call takes effect, as in net/http
		s.status = code
		s.started = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write marks the response as started: the first Write implicitly sends a
// 200 status line if WriteHeader wasn't called.
func (s *statusRecorder) Write(b []byte) (int, error) {
	s.started = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the original writer (for
// Flush, deadlines, …) through our wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// ReadFrom passes io.ReaderFrom through (R-OPS-7).
//
// Embedding only promotes the methods of the http.ResponseWriter interface,
// so without this method our wrapper would hide the real writer's ReadFrom.
// net/http implements ReadFrom with sendfile(2), copying a file straight from
// disk to the network socket; hiding it would make every file download copy
// through a user-space buffer instead.
func (s *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	s.started = true
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(s.ResponseWriter, src)
}

// logRequests writes one structured log line per request.
//
// The line is written from a defer so that it also appears when the request
// ends in a panic that propagates (an aborted response, see recoverPanics);
// the panic is then re-raised unchanged (R-OPS-2).
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Default 200: a handler that writes a body without calling
		// WriteHeader implicitly sends 200.
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			v := recover()
			logger.InfoContext(r.Context(), "request",
				"method", r.Method,
				// Path only: query strings may contain search text or, in
				// later plans, tokens that don't belong in logs.
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"aborted", v != nil,
			)
			if v != nil {
				panic(v)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

// responseSpecificHeaders are set by handlers for one particular successful
// response (see catalog/files.go) and must not leak onto an error response:
// a cacheable 500 with a file's ETag would poison caches.
var responseSpecificHeaders = []string{"ETag", "Cache-Control", "Content-Disposition", "Content-Security-Policy"}

// recoverPanics turns a panic in a handler into a 500 response instead of a
// dropped connection, and logs it with a stack trace (R-OPS-3).
//
// If the handler had already started sending its response (e.g. half of a
// file), a clean 500 is impossible: the status line is gone and appending
// JSON would corrupt the body. Then we abort the connection instead, so the
// client sees a failed download rather than a "successful" broken file.
func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// http.ErrAbortHandler is net/http's deliberate "abort this
			// response" signal, not a bug; pass it on untouched.
			if v == http.ErrAbortHandler {
				panic(v)
			}
			logger.ErrorContext(r.Context(), "panic",
				"value", v,
				"method", r.Method,
				"path", r.URL.Path,
				"response_started", rec.started,
				"stack", string(debug.Stack()),
			)
			if rec.started {
				panic(http.ErrAbortHandler)
			}
			for _, name := range responseSpecificHeaders {
				w.Header().Del(name)
			}
			httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
		}()
		next.ServeHTTP(rec, r)
	})
}

// securityHeaders sets headers that harden every response (R-OPS-4):
//   - nosniff: browsers must trust our Content-Type instead of guessing
//   - X-Frame-Options DENY: no other site can embed our pages in a frame
//     (prevents clickjacking)
//   - Referrer-Policy: other sites see only our origin, not full URLs
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
