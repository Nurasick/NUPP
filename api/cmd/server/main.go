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

	"github.com/Nurasick/NUPP/api/internal/config"
	"github.com/Nurasick/NUPP/api/internal/db"
	"github.com/Nurasick/NUPP/api/internal/server"
	"github.com/Nurasick/NUPP/api/internal/storage"
)

// shutdownTimeout is how long in-flight requests get to finish on SIGTERM.
const shutdownTimeout = 10 * time.Second

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

	// ctx is cancelled when the process receives Ctrl+C (SIGINT) or SIGTERM
	// (what Docker sends on `docker stop`).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	files, err := storage.NewLocal(cfg.StorageDir)
	if err != nil {
		return err
	}

	// Timeouts and size limits live in server.NewHTTPServer (spec H-HTTP-1).
	srv := server.NewHTTPServer(cfg.HTTPAddr, server.New(server.Deps{Pool: pool, Files: files, Logger: logger}))

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
