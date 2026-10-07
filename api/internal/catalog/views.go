package catalog

// Views are the JSON shapes the API returns. They are deliberately separate
// from the sqlc models in ./catalogdb:
//   - the database can change (new columns, renames) without silently
//     changing the public API, and
//   - internal fields (e.g. hidden_at, storage_key) can never leak by accident.
//
// The `json:"..."` struct tags set the field names in the JSON output.
// Pointer fields (*string, *int32) encode as null when nil (R-API-9).

import (
	"github.com/google/uuid"

	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
)

// CourseSummary is a course in search results.
type CourseSummary struct {
	ID         uuid.UUID `json:"id"`
	Code       string    `json:"code"`
	Slug       string    `json:"slug"`
	Title      string    `json:"title"`
	Department *string   `json:"department"`
}

// AssessmentView is one midterm, quiz, final, etc.
type AssessmentView struct {
	ID     uuid.UUID `json:"id"`
	Kind   string    `json:"kind"`
	Number *int32    `json:"number"`
	Label  *string   `json:"label"`
}

// OfferingView is one run of a course (year + term) with its assessments.
type OfferingView struct {
	ID          uuid.UUID        `json:"id"`
	Year        int32            `json:"year"`
	Term        string           `json:"term"`
	Instructor  *string          `json:"instructor"`
	Assessments []AssessmentView `json:"assessments"`
}

// CourseDetail is the course page.
//
// Embedding CourseSummary (a field with a type but no name) "promotes" its
// fields: encoding/json writes id, code, slug… at the top level of the same
// object, next to "offerings", instead of nesting them.
type CourseDetail struct {
	CourseSummary
	Offerings []OfferingView `json:"offerings"`
}

func toCourseSummary(c catalogdb.Course) CourseSummary {
	return CourseSummary{ID: c.ID, Code: c.Code, Slug: c.Slug, Title: c.Title, Department: c.Department}
}

func toAssessmentView(a catalogdb.Assessment) AssessmentView {
	return AssessmentView{ID: a.ID, Kind: a.Kind, Number: a.Number, Label: a.Label}
}

// buildCourseDetail nests each assessment under its offering.
//
// Both inputs arrive already sorted from SQL; we keep that order. Slices are
// created with make(…, 0, n) rather than left nil, because a nil slice
// encodes as JSON null and the API promises [] (R-API-8).
func buildCourseDetail(c catalogdb.Course, offerings []catalogdb.Offering, assessments []catalogdb.Assessment) CourseDetail {
	// Group assessments by offering id in one pass: O(n) instead of scanning
	// all assessments once per offering.
	byOffering := make(map[uuid.UUID][]AssessmentView, len(offerings))
	for _, a := range assessments {
		byOffering[a.OfferingID] = append(byOffering[a.OfferingID], toAssessmentView(a))
	}

	views := make([]OfferingView, 0, len(offerings))
	for _, o := range offerings {
		nested := byOffering[o.ID]
		if nested == nil {
			nested = []AssessmentView{}
		}
		views = append(views, OfferingView{
			ID: o.ID, Year: o.Year, Term: o.Term, Instructor: o.Instructor, Assessments: nested,
		})
	}
	return CourseDetail{CourseSummary: toCourseSummary(c), Offerings: views}
}
