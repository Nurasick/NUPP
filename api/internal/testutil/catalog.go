package testutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
)

// FileContent is the body of the fixture's file. SeedCatalog only creates the
// database row; tests that download the file must write these bytes to
// storage under Catalog.File.StorageKey themselves.
var FileContent = []byte("%PDF-1.4 fixture document for tests")

// Catalog is one fully linked chain:
// CSCI 151 → 2025 fall → Midterm 1 → "Midterm 1 questions" → one PDF file.
type Catalog struct {
	Course   catalogdb.Course
	Offering catalogdb.Offering
	Midterm  catalogdb.Assessment
	Material catalogdb.Material
	File     catalogdb.MaterialFile
}

// SeedCatalog inserts the fixture chain and returns the created rows.
func SeedCatalog(t *testing.T, pool *pgxpool.Pool) Catalog {
	t.Helper()
	ctx := context.Background()
	q := catalogdb.New(pool)
	var c Catalog
	var err error

	dept := "SEDS"
	if c.Course, err = q.CreateCourse(ctx, catalogdb.CreateCourseParams{
		Code: "CSCI 151", Slug: "csci-151", Title: "Programming for Scientists and Engineers", Department: &dept,
	}); err != nil {
		t.Fatalf("seed course: %v", err)
	}
	instructor := "Prof. Example"
	if c.Offering, err = q.CreateOffering(ctx, catalogdb.CreateOfferingParams{
		CourseID: c.Course.ID, Year: 2025, Term: "fall", Instructor: &instructor,
	}); err != nil {
		t.Fatalf("seed offering: %v", err)
	}
	one := int32(1)
	if c.Midterm, err = q.CreateAssessment(ctx, catalogdb.CreateAssessmentParams{
		OfferingID: c.Offering.ID, Kind: "midterm", Number: &one,
	}); err != nil {
		t.Fatalf("seed assessment: %v", err)
	}
	if c.Material, err = q.CreateMaterial(ctx, catalogdb.CreateMaterialParams{
		OfferingID: c.Offering.ID, AssessmentID: &c.Midterm.ID,
		Title: "Midterm 1 questions", Type: "questions", Description: "Scanned exam paper",
	}); err != nil {
		t.Fatalf("seed material: %v", err)
	}
	fileID := uuid.New()
	sum := sha256.Sum256(FileContent)
	if c.File, err = q.CreateMaterialFile(ctx, catalogdb.CreateMaterialFileParams{
		ID:         fileID,
		MaterialID: c.Material.ID,
		// Key layout from spec R-ST-2: materials/<material_id>/<file_id><ext>
		StorageKey: fmt.Sprintf("materials/%s/%s.pdf", c.Material.ID, fileID),
		MimeType:   "application/pdf",
		SizeBytes:  int64(len(FileContent)),
		Sha256:     hex.EncodeToString(sum[:]),
		Position:   0,
	}); err != nil {
		t.Fatalf("seed material file: %v", err)
	}
	return c
}

// HideMaterial marks a material hidden, as moderation will in Plan 5.
func HideMaterial(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "UPDATE materials SET hidden_at = now() WHERE id = $1", id); err != nil {
		t.Fatalf("hide material: %v", err)
	}
}
