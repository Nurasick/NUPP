package catalog_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
	"github.com/Nurasick/NUPP/api/internal/testutil"
)

func ptr[T any](v T) *T { return &v }

func TestSearchCourses_MatchesCodeOrTitleCaseInsensitively(t *testing.T) {
	testutil.Reset(t, testPool)
	testutil.SeedCatalog(t, testPool)
	q := catalogdb.New(testPool)
	ctx := context.Background()

	for _, query := range []string{"csci", "PROGRAMMING", "151"} {
		got, err := q.SearchCourses(ctx, catalogdb.SearchCoursesParams{Query: query, RowLimit: 10})
		if err != nil {
			t.Fatalf("SearchCourses(%q): %v", query, err)
		}
		if len(got) != 1 || got[0].Slug != "csci-151" {
			t.Errorf("SearchCourses(%q) = %+v, want csci-151", query, got)
		}
	}
	if n, err := q.CountCourses(ctx, ""); err != nil || n != 1 {
		t.Fatalf(`CountCourses("") = %d, %v; want 1`, n, err)
	}
}

// AC-12 / R-EP-8
func TestListOfferingsByCourse_NewestFirst(t *testing.T) {
	testutil.Reset(t, testPool)
	c := testutil.SeedCatalog(t, testPool) // already has 2025 fall
	q := catalogdb.New(testPool)
	ctx := context.Background()
	for _, o := range []catalogdb.CreateOfferingParams{
		{CourseID: c.Course.ID, Year: 2024, Term: "fall"},
		{CourseID: c.Course.ID, Year: 2025, Term: "spring"},
		{CourseID: c.Course.ID, Year: 2025, Term: "summer"},
	} {
		if _, err := q.CreateOffering(ctx, o); err != nil {
			t.Fatalf("create offering: %v", err)
		}
	}

	got, err := q.ListOfferingsByCourse(ctx, c.Course.ID)
	if err != nil {
		t.Fatalf("ListOfferingsByCourse: %v", err)
	}
	want := []string{"2025 fall", "2025 summer", "2025 spring", "2024 fall"}
	assertOrder(t, len(got), want, func(i int) string { return fmt.Sprintf("%d %s", got[i].Year, got[i].Term) })
}

// AC-13 / R-EP-9
func TestListAssessmentsByCourse_InExamOrder(t *testing.T) {
	testutil.Reset(t, testPool)
	c := testutil.SeedCatalog(t, testPool) // already has midterm 1
	q := catalogdb.New(testPool)
	ctx := context.Background()
	for _, a := range []catalogdb.CreateAssessmentParams{
		{OfferingID: c.Offering.ID, Kind: "final"},
		{OfferingID: c.Offering.ID, Kind: "quiz", Number: ptr(int32(2))},
		{OfferingID: c.Offering.ID, Kind: "quiz", Number: ptr(int32(1))},
	} {
		if _, err := q.CreateAssessment(ctx, a); err != nil {
			t.Fatalf("create assessment: %v", err)
		}
	}

	got, err := q.ListAssessmentsByCourse(ctx, c.Course.ID)
	if err != nil {
		t.Fatalf("ListAssessmentsByCourse: %v", err)
	}
	want := []string{"quiz 1", "quiz 2", "midterm 1", "final -"}
	assertOrder(t, len(got), want, func(i int) string {
		n := "-"
		if got[i].Number != nil {
			n = fmt.Sprint(*got[i].Number)
		}
		return got[i].Kind + " " + n
	})
}

// AC-15 / R-EP-14, and AC-19 for the list.
func TestListVisibleMaterialsByOffering_GeneralFirstThenAssessmentOrder(t *testing.T) {
	testutil.Reset(t, testPool)
	c := testutil.SeedCatalog(t, testPool) // "Midterm 1 questions" on midterm 1
	q := catalogdb.New(testPool)
	ctx := context.Background()
	quiz, err := q.CreateAssessment(ctx, catalogdb.CreateAssessmentParams{OfferingID: c.Offering.ID, Kind: "quiz", Number: ptr(int32(1))})
	if err != nil {
		t.Fatal(err)
	}
	create := func(title, typ string, assessment *uuid.UUID) catalogdb.Material {
		m, err := q.CreateMaterial(ctx, catalogdb.CreateMaterialParams{
			OfferingID: c.Offering.ID, AssessmentID: assessment, Title: title, Type: typ,
		})
		if err != nil {
			t.Fatalf("create material %q: %v", title, err)
		}
		return m
	}
	create("Midterm 1 solutions", "solutions", &c.Midterm.ID)
	create("Quiz 1", "questions", &quiz.ID)
	create("Lecture notes", "notes", nil)
	hidden := create("Taken down", "notes", nil)
	testutil.HideMaterial(t, testPool, hidden.ID)

	got, err := q.ListVisibleMaterialsByOffering(ctx, c.Offering.ID)
	if err != nil {
		t.Fatalf("ListVisibleMaterialsByOffering: %v", err)
	}
	want := []string{"Lecture notes", "Quiz 1", "Midterm 1 questions", "Midterm 1 solutions"}
	assertOrder(t, len(got), want, func(i int) string { return got[i].Title })
}

// assertOrder compares a result's labels, in order, against want.
func assertOrder(t *testing.T, n int, want []string, label func(int) string) {
	t.Helper()
	if n != len(want) {
		t.Fatalf("got %d rows, want %d (%v)", n, len(want), want)
	}
	for i := range want {
		if got := label(i); got != want[i] {
			t.Errorf("row %d = %q, want %q", i, got, want[i])
		}
	}
}
