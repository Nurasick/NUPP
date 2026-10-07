// Package server assembles all routes and middleware into the single
// http.Handler that cmd/server runs.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nurasick/NUPP/api/internal/catalog"
	"github.com/Nurasick/NUPP/api/internal/clientip"
	"github.com/Nurasick/NUPP/api/internal/httpx"
	"github.com/Nurasick/NUPP/api/internal/ratelimit"
	"github.com/Nurasick/NUPP/api/internal/storage"
)

// healthTimeout bounds the database ping in /healthz so a hung database makes
// the check fail quickly instead of hanging the caller (R-EP-1).
const healthTimeout = 2 * time.Second

// Deps are the server's collaborators. Passing them in (rather than creating
// them inside New) lets tests supply a test database and a temp directory.
type Deps struct {
	Pool   *pgxpool.Pool
	Files  storage.Storage
	Logger *slog.Logger

	// Protection against abuse (hardening spec). All optional: nil / zero
	// means "off", which keeps tests that don't care about limits simple.
	ClientIP *clientip.Resolver
	Limiter  *ratelimit.Limiter
	Caps     Caps
}

// New returns the fully wired HTTP handler.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(d.Pool))
	catalog.NewHandler(d.Pool, d.Files).Register(mux)

	// Fallbacks for anything under /api/ that no route above matched
	// (R-API-3). ServeMux always picks the most specific pattern, so these
	// never shadow real routes — including POST routes added in later plans.
	//   "GET /api/" → GET/HEAD to an unknown path → 404
	//   "/api/"     → any other method              → 405
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, _ *http.Request) {
		httpx.Fail(w, http.StatusNotFound, httpx.CodeNotFound, "no such endpoint")
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.Fail(w, http.StatusMethodNotAllowed, httpx.CodeMethodNotAllowed, "method not allowed")
	})

	return withMiddleware(stack{
		logger:   d.Logger,
		resolver: d.ClientIP,
		limiter:  d.Limiter,
		caps:     d.Caps,
	}, mux)
}

// healthz reports whether the service can reach its database (R-EP-1).
// Docker and uptime monitors poll it.
func healthz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			slog.ErrorContext(r.Context(), "health check failed", "err", err)
			httpx.Fail(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, "database unreachable")
			return
		}
		httpx.OK(w, map[string]string{"status": "ok"})
	}
}
