package server

import (
	"context"
	"net/http"
	"time"

	"github.com/Nurasick/NUPP/api/internal/httpx"
)

// Time limits for JSON requests (hardening spec H-DB-3, H-HTTP-2).
const (
	apiRequestTimeout = 10 * time.Second // total time for the handler's work (DB queries)
	apiWriteTimeout   = 15 * time.Second // time to send the response to the client
)

// deadlines bounds how long an api-class request may take (spec §7–§8).
//
// Two different clocks:
//   - a context deadline: queries started with r.Context() are cancelled
//     when it passes, so a stuck query can't hold a connection forever;
//   - a write deadline on the connection: a client that reads the response
//     too slowly is cut off instead of tying up the server.
//
// File downloads set their own, size-based write deadline in their handler
// (catalog/files.go), and /healthz has its own short timeout, so both are
// skipped here.
func deadlines(writeTimeout time.Duration, next http.Handler) http.Handler {
	if writeTimeout <= 0 {
		writeTimeout = apiWriteTimeout
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if classify(r) != classAPI {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
		defer cancel()

		httpx.SetWriteDeadline(w, writeTimeout)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
