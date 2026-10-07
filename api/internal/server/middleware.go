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

// statusRecorder wraps a ResponseWriter to remember the status code, which
// net/http doesn't expose after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
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
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(s.ResponseWriter, src)
}

// logRequests writes one structured log line per request.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Default 200: a handler that writes a body without calling
		// WriteHeader implicitly sends 200.
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.InfoContext(r.Context(), "request",
			"method", r.Method,
			// Path only: query strings may contain search text or, in later
			// plans, tokens that don't belong in logs.
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// recoverPanics turns a panic in a handler into a 500 response instead of a
// dropped connection, and logs it with a stack trace (R-OPS-3).
//
// (net/http would also recover and keep the server alive, but it would log
// plain text to stderr and send the client no response at all.)
func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// http.ErrAbortHandler is net/http's deliberate "abort this
			// response" signal, not a bug; let net/http handle it.
			if v == http.ErrAbortHandler {
				panic(v)
			}
			logger.ErrorContext(r.Context(), "panic",
				"value", v,
				"method", r.Method,
				"path", r.URL.Path,
				"stack", string(debug.Stack()),
			)
			httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternal, "something went wrong")
		}()
		next.ServeHTTP(w, r)
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
