package catalog_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nurasick/NUPP/api/internal/testutil"
)

// testPool is shared by every test in this package; see testutil.RunWithDB.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(testutil.RunWithDB(m, &testPool)) }
