// Package db owns the PostgreSQL connection pool and the schema migrations.
//
// Migrations are plain .sql files in ./migrations. The //go:embed directive
// below compiles them into the binary, so a deployed server always carries
// exactly the schema version its code expects — there is no separate
// "remember to run migrations" step.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Limits for the serving pool (hardening spec H-DB-1, H-DB-2).
const (
	statementTimeout       = "5s"  // longest any single query may run
	idleInTransactionLimit = "10s" // longest a transaction may sit idle
)

// Connect opens a connection pool with no query limits and verifies the
// database is reachable. Use it for migrations and one-off tools such as
// cmd/seed; the HTTP server serves requests from ConnectServing instead.
//
// pgxpool keeps several connections open and hands them out to concurrent
// requests, which is much cheaper than connecting per request.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	return open(ctx, cfg)
}

// ConnectServing opens the pool that serves HTTP requests. Every connection
// in it gets PostgreSQL-side limits, so a slow or stuck query is cancelled by
// the database itself after 5 s, whatever the Go code does (H-DB-1):
//
//   - statement_timeout: any single statement is cancelled after 5 s
//     (it then fails with SQLSTATE 57014, query_canceled);
//   - idle_in_transaction_session_timeout: a connection that opened a
//     transaction and then went quiet is closed after 10 s, so it can't
//     hold locks forever.
func ConnectServing(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	// RuntimeParams are sent when each connection starts, like running
	// "SET statement_timeout = '5s'" on every new connection.
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = statementTimeout
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = idleInTransactionLimit
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute // close connections nobody has used for a while
	cfg.MaxConnLifetime = time.Hour       // recycle connections now and then (frees server memory)
	return open(ctx, cfg)
}

func open(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	// pgxpool.New connects lazily; Ping forces a real round-trip so a wrong
	// URL or a stopped database fails here, at startup, not on first request.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies every migration that hasn't been applied yet.
// Running it on an up-to-date database does nothing.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return withProvider(pool, func(p *goose.Provider) error {
		if _, err := p.Up(ctx); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		return nil
	})
}

// withProvider builds a goose migration provider for the pool, runs fn with
// it and releases its resources afterwards.
func withProvider(pool *pgxpool.Pool, fn func(*goose.Provider) error) error {
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	// goose speaks database/sql, while the rest of the app uses pgx's native
	// pool. OpenDBFromPool adapts one to the other without opening a second,
	// independent set of connections. Closing the adapter does not close the pool.
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()

	provider, err := newProvider(sqlDB, migrations)
	if err != nil {
		return err
	}
	return fn(provider)
}

func newProvider(sqlDB *sql.DB, migrations fs.FS) (*goose.Provider, error) {
	// The session locker takes a PostgreSQL advisory lock while migrating, so
	// two processes starting at once (e.g. the server and `cmd/seed`) cannot
	// apply the same migration twice; the second simply waits (R-DB-19).
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations,
		goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("create migration provider: %w", err)
	}
	return provider, nil
}
