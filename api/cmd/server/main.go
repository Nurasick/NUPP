// Command server runs the NUPP HTTP API.
//
// Startup order (spec R-OPS-1): config → database → migrations → storage →
// listen. Any failure before listening exits with status 1, so a broken
// deployment is noticed immediately instead of serving errors.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/Nurasick/NUPP/api/internal/clientip"
	"github.com/Nurasick/NUPP/api/internal/config"
	"github.com/Nurasick/NUPP/api/internal/db"
	"github.com/Nurasick/NUPP/api/internal/ratelimit"
	"github.com/Nurasick/NUPP/api/internal/server"
	"github.com/Nurasick/NUPP/api/internal/storage"
)

const (
	// shutdownTimeout is how long in-flight requests get to finish on SIGTERM.
	shutdownTimeout = 10 * time.Second

	// Rate limiter memory bounds (hardening spec H-RL-4).
	rateLimitMaxKeys   = 100_000
	rateLimitIdleAfter = 60 * time.Second
)

func main() {
	// JSON logs are easy for machines (log collectors, grep + jq) to parse.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger) // packages calling slog.Error etc. use it too
	if err := run(logger); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// run contains the real main logic. Returning errors (instead of calling
// os.Exit deep inside) lets deferred cleanups like pool.Close() run.
func run(logger *slog.Logger) error {
	// Optional api/.env for local development. Load never overrides
	// variables that are already set, so real environment variables win.
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	for _, w := range cfg.Warnings() {
		logger.Warn(w)
	}

	// ctx is cancelled when the process receives Ctrl+C (SIGINT) or SIGTERM
	// (what Docker sends on `docker stop`).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Migrations may legitimately run longer than the 5 s query limit of
	// the serving pool, so they get their own short-lived, unlimited pool
	// (hardening spec H-DB-1).
	if err := migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := db.ConnectServing(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	files, err := storage.NewLocal(cfg.StorageDir)
	if err != nil {
		return err
	}

	// Abuse protection (hardening spec §4–§6).
	var limiter *ratelimit.Limiter
	if cfg.RateLimit.Enabled {
		limiter = ratelimit.New(ratelimit.Config{
			API:       ratelimit.Limit{Rate: cfg.RateLimit.APIRate, Burst: cfg.RateLimit.APIBurst},
			Files:     ratelimit.Limit{Rate: cfg.RateLimit.FilesRate, Burst: cfg.RateLimit.FilesBurst},
			MaxKeys:   rateLimitMaxKeys,
			IdleAfter: rateLimitIdleAfter,
			Logger:    logger,
		})
		go limiter.Run(ctx) // periodic cleanup; stops when ctx is cancelled
	}

	// Timeouts and size limits live in server.NewHTTPServer (spec H-HTTP-1).
	srv := server.NewHTTPServer(cfg.HTTPAddr, server.New(server.Deps{
		Pool:     pool,
		Files:    files,
		Logger:   logger,
		ClientIP: clientip.NewResolver(cfg.TrustedProxies),
		Limiter:  limiter,
		Caps: server.Caps{
			API:            cfg.MaxInflightAPI,
			Files:          cfg.MaxInflightFiles,
			FilesPerClient: cfg.MaxInflightFilesPerClient,
		},
	}))

	// ListenAndServe blocks, so it runs in its own goroutine; its result
	// comes back over a channel. Buffer size 1 means the goroutine can
	// always send and exit, even if nobody is receiving any more.
	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		errCh <- srv.ListenAndServe()
	}()

	// Wait for whichever happens first: the server fails, or a signal.
	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown (R-OPS-6, H-OPS-1): stop accepting new connections
	// and wait for in-flight requests. If they don't finish in time, they are
	// cut off and we exit 1 so the supervisor sees an unclean stop.
	logger.Info("shutting down")
	if err := server.Shutdown(srv, shutdownTimeout); err != nil {
		return err
	}
	logger.Info("stopped cleanly")
	return nil
}

// migrate applies migrations using a temporary pool without query limits.
func migrate(ctx context.Context, url string) error {
	pool, err := db.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(ctx, pool)
}
