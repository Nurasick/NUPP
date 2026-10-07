package db

// This file is compiled only during `go test` (its name ends in _test.go) but
// belongs to package db, so it can expose internals to the external db_test
// package without adding a dangerous "drop everything" function to the real API.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

// MigrateDownAll rolls back every migration. Test-only (R-DB-20).
func MigrateDownAll(ctx context.Context, pool *pgxpool.Pool) error {
	return withProvider(pool, func(p *goose.Provider) error {
		if _, err := p.DownTo(ctx, 0); err != nil {
			return fmt.Errorf("roll back migrations: %w", err)
		}
		return nil
	})
}
