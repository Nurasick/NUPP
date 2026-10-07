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
	"time"

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

// MaterialSummary is a material in an offering's list.
type MaterialSummary struct {
	ID           uuid.UUID  `json:"id"`
	OfferingID   uuid.UUID  `json:"offering_id"`
	AssessmentID *uuid.UUID `json:"assessment_id"` // null = general material
	Title        string     `json:"title"`
	Type         string     `json:"type"`
	Description  string     `json:"description"`
	PublishedAt  time.Time  `json:"published_at"` // encodes as RFC 3339
}

// CourseRef identifies the course a material belongs to.
type CourseRef struct {
	ID    uuid.UUID `json:"id"`
	Code  string    `json:"code"`
	Slug  string    `json:"slug"`
	Title string    `json:"title"`
}

// OfferingRef identifies the offering a material belongs to.
type OfferingRef struct {
	ID         uuid.UUID `json:"id"`
	Year       int32     `json:"year"`
	Term       string    `json:"term"`
	Instructor *string   `json:"instructor"`
}

// FileView is one downloadable file (page) of a material.
type FileView struct {
	ID        uuid.UUID `json:"id"`
	MimeType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	Position  int32     `json:"position"`
	URL       string    `json:"url"`
}

// MaterialDetail is the material page: the material, where it belongs
// (course, offering, assessment) and its files (R-EP-15).
type MaterialDetail struct {
	MaterialSummary
	Course     CourseRef       `json:"course"`
	Offering   OfferingRef     `json:"offering"`
	Assessment *AssessmentView `json:"assessment"` // nil → null for general materials
	Files      []FileView      `json:"files"`
}

func toMaterialSummary(m catalogdb.Material) MaterialSummary {
	return MaterialSummary{
		ID: m.ID, OfferingID: m.OfferingID, AssessmentID: m.AssessmentID,
		Title: m.Title, Type: m.Type, Description: m.Description, PublishedAt: m.PublishedAt,
	}
}

// fileURL is the public download path of a material file (R-EP-16).
func fileURL(id uuid.UUID) string { return "/api/v1/files/" + id.String() }

func buildMaterialDetail(m catalogdb.Material, where catalogdb.GetMaterialContextRow, files []catalogdb.MaterialFile) MaterialDetail {
	// The assessment columns come from a LEFT JOIN: all nil when the
	// material is general, all set otherwise.
	var assessment *AssessmentView
	if where.AssessmentID != nil {
		assessment = &AssessmentView{
			ID: *where.AssessmentID, Kind: *where.AssessmentKind,
			Number: where.AssessmentNumber, Label: where.AssessmentLabel,
		}
	}

	views := make([]FileView, 0, len(files))
	for _, f := range files {
		views = append(views, FileView{
			ID: f.ID, MimeType: f.MimeType, SizeBytes: f.SizeBytes, Position: f.Position, URL: fileURL(f.ID),
		})
	}

	return MaterialDetail{
		MaterialSummary: toMaterialSummary(m),
		Course:          CourseRef{ID: where.CourseID, Code: where.CourseCode, Slug: where.CourseSlug, Title: where.CourseTitle},
		Offering: OfferingRef{
			ID: where.OfferingID, Year: where.OfferingYear, Term: where.OfferingTerm, Instructor: where.OfferingInstructor,
		},
		Assessment: assessment,
		Files:      views,
	}
}
