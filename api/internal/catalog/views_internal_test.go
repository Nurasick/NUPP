package catalog

// An "internal" test (package catalog, not catalog_test) can reach unexported
// functions like buildCourseDetail. We use it for pure logic that is easier
// to check directly than through HTTP.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
)

// R-EP-7: each assessment appears only under its own offering.
func TestBuildCourseDetail_GroupsAssessmentsUnderTheirOffering(t *testing.T) {
	course := catalogdb.Course{ID: uuid.New(), Code: "CSCI 151", Slug: "csci-151", Title: "Programming"}
	fall := catalogdb.Offering{ID: uuid.New(), CourseID: course.ID, Year: 2025, Term: "fall"}
	spring := catalogdb.Offering{ID: uuid.New(), CourseID: course.ID, Year: 2025, Term: "spring"}
	quiz := catalogdb.Assessment{ID: uuid.New(), OfferingID: fall.ID, Kind: "quiz"}

	got := buildCourseDetail(course, []catalogdb.Offering{fall, spring}, []catalogdb.Assessment{quiz})

	if len(got.Offerings) != 2 {
		t.Fatalf("offerings = %d, want 2", len(got.Offerings))
	}
	if len(got.Offerings[0].Assessments) != 1 || got.Offerings[0].Assessments[0].ID != quiz.ID {
		t.Errorf("fall assessments = %+v", got.Offerings[0].Assessments)
	}
	if len(got.Offerings[1].Assessments) != 0 {
		t.Errorf("spring assessments = %+v, want none", got.Offerings[1].Assessments)
	}
}

// AC-14 / R-API-8: empty collections are [] in JSON, never null.
func TestBuildCourseDetail_EmptyCollectionsSerializeAsArrays(t *testing.T) {
	course := catalogdb.Course{ID: uuid.New()}
	offering := catalogdb.Offering{ID: uuid.New(), CourseID: course.ID, Year: 2025, Term: "fall"}

	raw, _ := json.Marshal(buildCourseDetail(course, []catalogdb.Offering{offering}, nil))
	if !strings.Contains(string(raw), `"assessments":[]`) {
		t.Errorf("offering without assessments must serialize [] not null: %s", raw)
	}
	raw, _ = json.Marshal(buildCourseDetail(course, nil, nil))
	if !strings.Contains(string(raw), `"offerings":[]`) {
		t.Errorf("course without offerings must serialize [] not null: %s", raw)
	}
}

// R-EP-18: download filenames are readable, ASCII-only, built from server data.
func TestDownloadFilename(t *testing.T) {
	cases := []struct {
		slug, title string
		position    int32
		mime, want  string
	}{
		{"csci-151", "Midterm 1 questions", 0, "application/pdf", "csci-151-midterm-1-questions-p1.pdf"},
		{"csci-151", "Scan", 4, "image/jpeg", "csci-151-scan-p5.jpg"},
		{"kaz-101", "Қазақ тілі", 0, "image/png", "kaz-101-p1.png"}, // no ASCII title slug
		{"csci-151", "Notes", 0, "application/x-unknown", "csci-151-notes-p1.bin"},
	}
	for _, c := range cases {
		if got := downloadFilename(c.slug, c.title, c.position, c.mime); got != c.want {
			t.Errorf("downloadFilename(%q, %q, %d, %q) = %q, want %q", c.slug, c.title, c.position, c.mime, got, c.want)
		}
	}
}
