// Package testutil provides helpers for tests that need a real PostgreSQL.
//
// We test against a real database instead of mocks because most of the
// behaviour worth testing here (constraints, ordering, case-insensitive
// search, NULL handling) lives in SQL. A mock would only test our assumptions
// about PostgreSQL, not PostgreSQL itself.
package testutil

import (
	"context"
	"log"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Nurasick/NUPP/api/internal/db"
)

// RunWithDB starts a throwaway PostgreSQL container, applies migrations,
// stores the connection pool in *pool and runs the package's tests.
//
// Call it from TestMain so one container is shared by every test in the
// package (starting a container per test would be far too slow):
//
//	func TestMain(m *testing.M) { os.Exit(testutil.RunWithDB(m, &testPool)) }
//
// It returns the exit code instead of calling os.Exit itself so that the
// deferred cleanup (stopping the container) still runs.
func RunWithDB(m *testing.M, pool **pgxpool.Pool) int {
	ctx := context.Background()

	// These credentials belong to a disposable container that only lives for
	// the duration of this test run; they are not secrets.
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("nupp_test"),
		tcpostgres.WithUsername("nupp"),
		tcpostgres.WithPassword("nupp"),
		// Same Unicode-aware locale as docker-compose.yml (spec R-DB-0), so
		// tests see exactly the case-folding behaviour production has.
		testcontainers.WithEnv(map[string]string{
			"POSTGRES_INITDB_ARGS": "--locale-provider=builtin --builtin-locale=C.UTF-8",
		}),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("start postgres container (is Docker running?): %v", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(ctr) }()

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("postgres connection string: %v", err)
		return 1
	}
	p, err := db.Connect(ctx, url)
	if err != nil {
		log.Printf("connect: %v", err)
		return 1
	}
	defer p.Close()
	if err := db.Migrate(ctx, p); err != nil {
		log.Printf("migrate: %v", err)
		return 1
	}

	*pool = p
	return m.Run()
}

// resetSQL truncates every table in the public schema except goose's own
// bookkeeping table. Discovering tables dynamically means later plans can add
// tables without having to remember to update this helper.
const resetSQL = `
DO $$
DECLARE stmt text;
BEGIN
    SELECT 'TRUNCATE ' || string_agg(format('%I', tablename), ', ') || ' CASCADE'
      INTO stmt
      FROM pg_tables
     WHERE schemaname = 'public' AND tablename <> 'goose_db_version';
    IF stmt IS NOT NULL THEN
        EXECUTE stmt;
    END IF;
END $$;`

// Reset empties every application table so each test starts from a clean
// database. Tests sharing one database must therefore not run in parallel.
func Reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), resetSQL); err != nil {
		t.Fatalf("reset database: %v", err)
	}
}
