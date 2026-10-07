package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nurasick/NUPP/api/internal/db"
	"github.com/Nurasick/NUPP/api/internal/testutil"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(testutil.RunWithDB(m, &testPool)) }

// exec runs a statement and fails the test on error.
func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// mustFail runs a statement that a constraint should reject.
func mustFail(t *testing.T, why, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), sql, args...); err == nil {
		t.Errorf("expected rejection: %s", why)
	}
}

// insertID runs an INSERT ... RETURNING id and returns the id.
func insertID(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

// seedOffering creates a course with one offering and returns their ids.
func seedOffering(t *testing.T) (courseID, offeringID string) {
	t.Helper()
	courseID = insertID(t, `INSERT INTO courses (code, slug, title) VALUES ('CSCI 151', 'csci-151', 'Programming') RETURNING id`)
	offeringID = insertID(t, `INSERT INTO offerings (course_id, year, term) VALUES ($1, 2025, 'fall') RETURNING id`, courseID)
	return courseID, offeringID
}

var catalogTables = []string{"courses", "offerings", "assessments", "materials", "material_files"}

func assertTablesExist(t *testing.T, want bool) {
	t.Helper()
	for _, table := range catalogTables {
		var exists bool
		err := testPool.QueryRow(context.Background(),
			"SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists)
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if exists != want {
			t.Errorf("table %s exists = %v, want %v", table, exists, want)
		}
	}
}

// AC-1
func TestMigrate_CreatesCatalogTables(t *testing.T) {
	assertTablesExist(t, true)
}

// AC-1: running migrations again is a no-op.
func TestMigrate_IsIdempotent(t *testing.T) {
	if err := db.Migrate(context.Background(), testPool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

// AC-1 / R-DB-20: every Down section works, and Up works again afterwards.
func TestMigrate_DownThenUpAgain(t *testing.T) {
	ctx := context.Background()
	if err := db.MigrateDownAll(ctx, testPool); err != nil {
		t.Fatalf("down: %v", err)
	}
	assertTablesExist(t, false)
	if err := db.Migrate(ctx, testPool); err != nil {
		t.Fatalf("up again: %v", err)
	}
	assertTablesExist(t, true)
}

// AC-2 / R-DB-1
func TestCourses_RejectDuplicateCodeIgnoringCase(t *testing.T) {
	testutil.Reset(t, testPool)
	exec(t, `INSERT INTO courses (code, slug, title) VALUES ('CSCI 151', 'csci-151', 'Programming')`)
	mustFail(t, "case-insensitive duplicate code",
		`INSERT INTO courses (code, slug, title) VALUES ('csci 151', 'csci-151-b', 'Duplicate')`)
	mustFail(t, "slug with upper case",
		`INSERT INTO courses (code, slug, title) VALUES ('MATH 161', 'Math-161', 'Calculus')`)
}

// AC-3 / R-DB-6, R-DB-7, R-DB-10
func TestOfferingsAndAssessments_TreatMissingValuesAsEqual(t *testing.T) {
	testutil.Reset(t, testPool)
	courseID, offeringID := seedOffering(t) // 2025 fall, no instructor

	mustFail(t, "second 2025 fall offering without instructor",
		`INSERT INTO offerings (course_id, year, term) VALUES ($1, 2025, 'fall')`, courseID)
	mustFail(t, "empty-string instructor",
		`INSERT INTO offerings (course_id, year, term, instructor) VALUES ($1, 2024, 'fall', '')`, courseID)

	exec(t, `INSERT INTO assessments (offering_id, kind) VALUES ($1, 'final')`, offeringID)
	mustFail(t, "second final without number or label",
		`INSERT INTO assessments (offering_id, kind) VALUES ($1, 'final')`, offeringID)
	mustFail(t, "empty-string label",
		`INSERT INTO assessments (offering_id, kind, label) VALUES ($1, 'quiz', '')`, offeringID)
}

// AC-4 / R-DB-12
func TestMaterials_AssessmentMustBelongToSameOffering(t *testing.T) {
	testutil.Reset(t, testPool)
	courseID, offeringID := seedOffering(t)
	otherOffering := insertID(t, `INSERT INTO offerings (course_id, year, term) VALUES ($1, 2024, 'spring') RETURNING id`, courseID)
	foreignQuiz := insertID(t, `INSERT INTO assessments (offering_id, kind, number) VALUES ($1, 'quiz', 1) RETURNING id`, otherOffering)

	mustFail(t, "assessment from a different offering",
		`INSERT INTO materials (offering_id, assessment_id, title, type) VALUES ($1, $2, 'X', 'notes')`,
		offeringID, foreignQuiz)
}

// AC-4 / R-DB-12: deleting the assessment keeps the material as a general one.
func TestMaterials_DeletingAssessmentKeepsMaterial(t *testing.T) {
	testutil.Reset(t, testPool)
	_, offeringID := seedOffering(t)
	midterm := insertID(t, `INSERT INTO assessments (offering_id, kind, number) VALUES ($1, 'midterm', 1) RETURNING id`, offeringID)
	material := insertID(t,
		`INSERT INTO materials (offering_id, assessment_id, title, type) VALUES ($1, $2, 'Midterm 1', 'questions') RETURNING id`,
		offeringID, midterm)

	exec(t, `DELETE FROM assessments WHERE id = $1`, midterm)

	var assessmentID, gotOffering *string
	err := testPool.QueryRow(context.Background(),
		`SELECT assessment_id::text, offering_id::text FROM materials WHERE id = $1`, material).
		Scan(&assessmentID, &gotOffering)
	if err != nil {
		t.Fatalf("material should still exist: %v", err)
	}
	if assessmentID != nil {
		t.Errorf("assessment_id = %v, want NULL", *assessmentID)
	}
	// ON DELETE SET NULL (assessment_id) must not also null offering_id.
	if gotOffering == nil || *gotOffering != offeringID {
		t.Errorf("offering_id = %v, want %s", gotOffering, offeringID)
	}
}

// AC-5 / R-DB-16, R-DB-17, R-DB-18
func TestMaterialFiles_RejectUnsafeValues(t *testing.T) {
	testutil.Reset(t, testPool)
	_, offeringID := seedOffering(t)
	material := insertID(t,
		`INSERT INTO materials (offering_id, title, type) VALUES ($1, 'Notes', 'notes') RETURNING id`, offeringID)
	const insert = `INSERT INTO material_files (material_id, storage_key, mime_type, size_bytes, sha256, position)
	                VALUES ($1, $2, $3, 10, $4, 0)`
	goodSHA := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	mustFail(t, "text/html mime type", insert, material, "materials/x/1.html", "text/html", goodSHA)
	mustFail(t, "svg mime type", insert, material, "materials/x/1.svg", "image/svg+xml", goodSHA)
	mustFail(t, "key outside materials/", insert, material, "pending/x/1.pdf", "application/pdf", goodSHA)
	mustFail(t, "upper-case sha256", insert, material, "materials/x/1.pdf", "application/pdf",
		"0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef")
	exec(t, insert, material, "materials/x/1.pdf", "application/pdf", goodSHA)
}
