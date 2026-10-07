package catalog_test

// Tests for the material and file endpoints. Shared helpers (newMux, get,
// decode, assertError) live in handler_test.go.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Nurasick/NUPP/api/internal/catalog"
	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
	"github.com/Nurasick/NUPP/api/internal/httpx"
	"github.com/Nurasick/NUPP/api/internal/storage"
	"github.com/Nurasick/NUPP/api/internal/testutil"
)

const unknownID = "6f1c2a43-8a8e-4b7c-9d0e-2f3a4b5c6d7e"

func putFixtureFile(t *testing.T, files storage.Storage, fx testutil.Catalog) {
	t.Helper()
	if err := files.Put(context.Background(), fx.File.StorageKey, bytes.NewReader(testutil.FileContent)); err != nil {
		t.Fatalf("store fixture file: %v", err)
	}
}

// AC-16 / R-API-18, R-API-19
func TestCatalog_RejectsMalformedAndUnknownIDs(t *testing.T) {
	testutil.Reset(t, testPool)
	mux, _ := newMux(t)
	cases := []struct {
		id     string
		status int
		code   string
	}{
		{"not-a-uuid", http.StatusBadRequest, httpx.CodeBadRequest},
		{strings.ToUpper(unknownID), http.StatusBadRequest, httpx.CodeBadRequest}, // non-canonical
		{unknownID, http.StatusNotFound, httpx.CodeNotFound},
	}
	for _, pattern := range []string{"/api/v1/offerings/%s/materials", "/api/v1/materials/%s", "/api/v1/files/%s"} {
		for _, c := range cases {
			path := fmt.Sprintf(pattern, c.id)
			t.Run(path, func(t *testing.T) { assertError(t, get(t, mux, path), c.status, c.code) })
		}
	}
}

func TestListMaterials_ReturnsMaterialsOfOffering(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, _ := newMux(t)

	rec := get(t, mux, "/api/v1/offerings/"+fx.Offering.ID.String()+"/materials")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	env := decode[[]catalog.MaterialSummary](t, rec)
	if len(env.Data) != 1 || env.Data[0].ID != fx.Material.ID || env.Data[0].Title != "Midterm 1 questions" {
		t.Fatalf("data = %+v", env.Data)
	}
	if env.Data[0].AssessmentID == nil || *env.Data[0].AssessmentID != fx.Midterm.ID {
		t.Errorf("assessment_id = %v", env.Data[0].AssessmentID)
	}
	if env.Meta != nil {
		t.Errorf("material list is not paginated, meta must be absent: %+v", env.Meta)
	}
}

// AC-17
func TestListMaterials_OfferingWithoutVisibleMaterialsReturnsEmptyArray(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	testutil.HideMaterial(t, testPool, fx.Material.ID) // its only material
	mux, _ := newMux(t)

	rec := get(t, mux, "/api/v1/offerings/"+fx.Offering.ID.String()+"/materials")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("status = %d body = %s, want 200 with data []", rec.Code, rec.Body)
	}
}

// AC-18 / R-EP-15, R-EP-16
func TestGetMaterial_IncludesContextAndFiles(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, _ := newMux(t)

	rec := get(t, mux, "/api/v1/materials/"+fx.Material.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	d := decode[catalog.MaterialDetail](t, rec).Data
	if d.Title != "Midterm 1 questions" || d.Type != "questions" || d.OfferingID != fx.Offering.ID {
		t.Errorf("summary = %+v", d.MaterialSummary)
	}
	if d.Course.Slug != "csci-151" || d.Course.Code != "CSCI 151" {
		t.Errorf("course = %+v", d.Course)
	}
	if d.Offering.Year != 2025 || d.Offering.Term != "fall" {
		t.Errorf("offering = %+v", d.Offering)
	}
	if d.Assessment == nil || d.Assessment.Kind != "midterm" || *d.Assessment.Number != 1 {
		t.Errorf("assessment = %+v", d.Assessment)
	}
	if len(d.Files) != 1 || d.Files[0].URL != "/api/v1/files/"+fx.File.ID.String() || d.Files[0].MimeType != "application/pdf" {
		t.Errorf("files = %+v", d.Files)
	}
}

// R-EP-15: a general material has "assessment": null, and no files is [].
func TestGetMaterial_GeneralMaterialHasNullAssessment(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	m, err := catalogdb.New(testPool).CreateMaterial(context.Background(), catalogdb.CreateMaterialParams{
		OfferingID: fx.Offering.ID, Title: "Lecture notes", Type: "notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	mux, _ := newMux(t)

	body := get(t, mux, "/api/v1/materials/"+m.ID.String()).Body.String()
	if !strings.Contains(body, `"assessment":null`) || !strings.Contains(body, `"files":[]`) {
		t.Fatalf("body = %s", body)
	}
}

// AC-19 / R-API-20
func TestHiddenMaterial_IsInvisibleEverywhere(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, files := newMux(t)
	putFixtureFile(t, files, fx)
	testutil.HideMaterial(t, testPool, fx.Material.ID)

	list := decode[[]catalog.MaterialSummary](t, get(t, mux, "/api/v1/offerings/"+fx.Offering.ID.String()+"/materials"))
	if len(list.Data) != 0 {
		t.Errorf("hidden material listed: %+v", list.Data)
	}
	assertError(t, get(t, mux, "/api/v1/materials/"+fx.Material.ID.String()), http.StatusNotFound, httpx.CodeNotFound)
	assertError(t, get(t, mux, "/api/v1/files/"+fx.File.ID.String()), http.StatusNotFound, httpx.CodeNotFound)
}

// AC-20 / R-EP-18
func TestGetFile_StreamsContentWithSafeHeaders(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, files := newMux(t)
	putFixtureFile(t, files, fx)

	rec := get(t, mux, "/api/v1/files/"+fx.File.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(rec.Body.Bytes(), testutil.FileContent) {
		t.Errorf("body mismatch")
	}
	for header, want := range map[string]string{
		"Content-Type":            "application/pdf",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; sandbox",
		"Content-Disposition":     `inline; filename="csci-151-midterm-1-questions-p1.pdf"`,
		"Etag":                    `"` + fx.File.Sha256 + `"`,
		"Cache-Control":           "public, max-age=3600, s-maxage=86400",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// R-EP-18: only PDFs and images are shown inline; other types download.
func TestGetFile_OfficeDocumentsAreAttachments(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	fileID := uuid.New()
	key := fmt.Sprintf("materials/%s/%s.docx", fx.Material.ID, fileID)
	if _, err := catalogdb.New(testPool).CreateMaterialFile(context.Background(), catalogdb.CreateMaterialFileParams{
		ID: fileID, MaterialID: fx.Material.ID, StorageKey: key,
		MimeType:  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		SizeBytes: 4, Sha256: strings.Repeat("ab", 32), Position: 1,
	}); err != nil {
		t.Fatal(err)
	}
	mux, files := newMux(t)
	if err := files.Put(context.Background(), key, strings.NewReader("docx")); err != nil {
		t.Fatal(err)
	}

	rec := get(t, mux, "/api/v1/files/"+fileID.String())
	want := `attachment; filename="csci-151-midterm-1-questions-p2.docx"`
	if got := rec.Header().Get("Content-Disposition"); got != want {
		t.Fatalf("Content-Disposition = %q, want %q", got, want)
	}
}

// AC-21: a client that already has the file gets 304 and no body.
func TestGetFile_MatchingETagReturns304(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, files := newMux(t)
	putFixtureFile(t, files, fx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+fx.File.ID.String(), nil)
	req.Header.Set("If-None-Match", `"`+fx.File.Sha256+`"`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body length = %d; want 304 and empty", rec.Code, rec.Body.Len())
	}
}

// AC-22 / R-EP-19
func TestGetFile_SupportsRangeRequests(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, files := newMux(t)
	putFixtureFile(t, files, fx)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+fx.File.ID.String(), nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "%PDF" {
		t.Fatalf("status = %d body = %q, want 206 %%PDF", rec.Code, rec.Body.String())
	}
}

// AC-23 / R-EP-20
func TestGetFile_RowWithoutStoredObjectIs404(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool) // nothing written to storage
	mux, _ := newMux(t)
	assertError(t, get(t, mux, "/api/v1/files/"+fx.File.ID.String()), http.StatusNotFound, httpx.CodeNotFound)
}

// deadlineRecorder records write deadlines (the real ResponseWriter supports
// them; httptest's recorder doesn't).
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}

// HAC-12 / H-HTTP-3: downloads get 30 s plus time for the file at 32 KiB/s.
func TestGetFile_SetsSizeBasedWriteDeadline(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	// Pretend the file is 3.2 MiB so the size part is clearly visible: 100 s.
	if _, err := testPool.Exec(context.Background(),
		"UPDATE material_files SET size_bytes = $1 WHERE id = $2", 100*32*1024, fx.File.ID); err != nil {
		t.Fatal(err)
	}
	mux, files := newMux(t)
	putFixtureFile(t, files, fx)

	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	start := time.Now()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/files/"+fx.File.ID.String(), nil))

	if len(rec.deadlines) != 1 {
		t.Fatalf("deadlines = %v, want exactly one", rec.deadlines)
	}
	if d := rec.deadlines[0].Sub(start); d < 129*time.Second || d > 131*time.Second {
		t.Errorf("write deadline = now+%v, want ~130s (30s + 100s)", d)
	}
}
