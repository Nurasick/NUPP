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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Connect opens a connection pool and verifies the database is reachable.
//
// pgxpool keeps several connections open and hands them out to concurrent
// requests, which is much cheaper than connecting per request.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
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
