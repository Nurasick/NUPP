package server

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// NewHTTPServer returns an http.Server with the limits from hardening spec
// H-HTTP-1. Every value protects against a slow or malicious client:
//
//   - ReadHeaderTimeout: the request line and headers must arrive within 5 s
//     (stops "slowloris" attacks that trickle headers to hold connections);
//   - ReadTimeout: the whole request, body included, within 15 s; our routes
//     are all GET, so nothing legitimate needs longer;
//   - IdleTimeout: idle keep-alive connections are closed after 60 s;
//   - MaxHeaderBytes: headers plus the request line (so also the URL and
//     query string) are capped at 32 KiB instead of Go's default 1 MiB.
//
// There is deliberately no WriteTimeout: it would cut large downloads off
// for everyone. Responses get per-request write deadlines instead (see
// deadlines.go and catalog/files.go).
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10, // 32 KiB
	}
}

// Shutdown stops srv gracefully: no new connections, and in-flight requests
// get up to timeout to finish. Requests still running after that are cut off
// by closing their connections, and an error is returned so the process can
// exit non-zero (H-OPS-1).
func Shutdown(srv *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		closeErr := srv.Close() // force-close whatever is left
		return fmt.Errorf("graceful shutdown timed out, connections closed (close error: %v): %w", closeErr, err)
	}
	return nil
}
