package catalog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nurasick/NUPP/api/internal/catalog"
	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
	"github.com/Nurasick/NUPP/api/internal/httpx"
	"github.com/Nurasick/NUPP/api/internal/storage"
	"github.com/Nurasick/NUPP/api/internal/testutil"
)

// envelope mirrors httpx.Envelope with a concrete Data type, so tests can
// decode straight into the view structs. (Go generics: T is chosen per call.)
type envelope[T any] struct {
	Data  T            `json:"data"`
	Error *httpx.Error `json:"error"`
	Meta  *httpx.Meta  `json:"meta"`
}

// newMux returns a router with only the catalog routes, plus its storage.
func newMux(t *testing.T) (*http.ServeMux, *storage.Local) {
	t.Helper()
	files, err := storage.NewLocal(testutil.TempDir(t))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	mux := http.NewServeMux()
	catalog.NewHandler(testPool, files).Register(mux)
	return mux, files
}

// get performs a GET against h without any network: httptest.NewRecorder
// captures the response in memory.
func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) envelope[T] {
	t.Helper()
	var env envelope[T]
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return env
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d (body %s)", rec.Code, status, rec.Body)
		return
	}
	if env := decode[any](t, rec); env.Error == nil || env.Error.Code != code {
		t.Errorf("error = %+v, want code %q", env.Error, code)
	}
}

// ---- GET /api/v1/courses -------------------------------------------------

// AC-6
func TestListCourses_SearchesByCodeAndTitle(t *testing.T) {
	testutil.Reset(t, testPool)
	testutil.SeedCatalog(t, testPool)
	mux, _ := newMux(t)

	for _, q := range []string{"csci", "Programming"} {
		rec := get(t, mux, "/api/v1/courses?q="+url.QueryEscape(q))
		if rec.Code != http.StatusOK {
			t.Fatalf("q=%s status = %d", q, rec.Code)
		}
		env := decode[[]catalog.CourseSummary](t, rec)
		if len(env.Data) != 1 || env.Data[0].Slug != "csci-151" {
			t.Errorf("q=%s data = %+v", q, env.Data)
		}
		if env.Meta == nil || env.Meta.Total != 1 || env.Meta.Limit != 20 || env.Meta.Offset != 0 {
			t.Errorf("q=%s meta = %+v", q, env.Meta)
		}
	}
}

// AC-7 / R-EP-2: Unicode case-insensitive match.
func TestListCourses_MatchesKazakhCaseInsensitively(t *testing.T) {
	testutil.Reset(t, testPool)
	if _, err := catalogdb.New(testPool).CreateCourse(context.Background(), catalogdb.CreateCourseParams{
		Code: "KAZ 101", Slug: "kaz-101", Title: "Қазақ тілі",
	}); err != nil {
		t.Fatal(err)
	}
	mux, _ := newMux(t)

	env := decode[[]catalog.CourseSummary](t, get(t, mux, "/api/v1/courses?q="+url.QueryEscape("қазақ")))
	if len(env.Data) != 1 || env.Data[0].Code != "KAZ 101" {
		t.Fatalf("q=қазақ data = %+v, want KAZ 101", env.Data)
	}
}

// AC-7 / R-EP-3: LIKE wildcards are literal characters.
func TestListCourses_TreatsWildcardsLiterally(t *testing.T) {
	testutil.Reset(t, testPool)
	testutil.SeedCatalog(t, testPool)
	mux, _ := newMux(t)

	for _, q := range []string{"%", "_", `\`, "CSCI_151"} {
		rec := get(t, mux, "/api/v1/courses?q="+url.QueryEscape(q))
		if rec.Code != http.StatusOK {
			t.Fatalf("q=%q status = %d body=%s", q, rec.Code, rec.Body)
		}
		if env := decode[[]catalog.CourseSummary](t, rec); len(env.Data) != 0 {
			t.Errorf("q=%q matched %d courses, want 0", q, len(env.Data))
		}
	}
}

// AC-8
func TestListCourses_RejectsBadInput(t *testing.T) {
	testutil.Reset(t, testPool)
	mux, _ := newMux(t)
	for _, qs := range []string{
		"limit=abc", "limit=-1", "limit=0", "limit=99999999999999999999",
		"offset=-1", "offset=100001",
		"q=" + strings.Repeat("a", 101),
		"q=%FF",   // invalid UTF-8: PostgreSQL would reject it (SQLSTATE 22021)
		"q=a%00b", // NUL byte: not allowed in PostgreSQL text
	} {
		t.Run(qs[:min(len(qs), 20)], func(t *testing.T) {
			assertError(t, get(t, mux, "/api/v1/courses?"+qs), http.StatusBadRequest, httpx.CodeBadRequest)
		})
	}
}

// The 100-character limit counts characters, not bytes: 100 Cyrillic letters
// are 200 bytes in UTF-8 and must still be accepted.
func TestListCourses_QueryLimitCountsCharacters(t *testing.T) {
	testutil.Reset(t, testPool)
	mux, _ := newMux(t)
	q := url.QueryEscape(strings.Repeat("қ", 100))
	if rec := get(t, mux, "/api/v1/courses?q="+q); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// AC-9
func TestListCourses_ClampsLimit(t *testing.T) {
	testutil.Reset(t, testPool)
	mux, _ := newMux(t)
	env := decode[[]catalog.CourseSummary](t, get(t, mux, "/api/v1/courses?limit=1000"))
	if env.Meta == nil || env.Meta.Limit != 100 {
		t.Fatalf("meta = %+v, want limit 100", env.Meta)
	}
}

// AC-10
func TestListCourses_EmptyDatabaseReturnsEmptyArray(t *testing.T) {
	testutil.Reset(t, testPool)
	mux, _ := newMux(t)
	rec := get(t, mux, "/api/v1/courses")
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("want data [], got %s", rec.Body.String())
	}
}

// ---- GET /api/v1/courses/{slug} -----------------------------------------

// AC-11
func TestGetCourse_ReturnsNestedOfferings(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, _ := newMux(t)

	rec := get(t, mux, "/api/v1/courses/CSCI-151") // slug lookup is case-insensitive
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	env := decode[catalog.CourseDetail](t, rec)
	if env.Data.ID != fx.Course.ID || len(env.Data.Offerings) != 1 {
		t.Fatalf("data = %+v", env.Data)
	}
	got := env.Data.Offerings[0]
	if got.Year != 2025 || got.Term != "fall" || got.Instructor == nil || *got.Instructor != "Prof. Example" {
		t.Errorf("offering = %+v", got)
	}
	if len(got.Assessments) != 1 || got.Assessments[0].Kind != "midterm" || *got.Assessments[0].Number != 1 {
		t.Errorf("assessments = %+v", got.Assessments)
	}
	if env.Meta != nil {
		t.Errorf("detail responses must not carry meta: %+v", env.Meta)
	}
}

// AC-11
func TestGetCourse_UnknownSlugIs404(t *testing.T) {
	testutil.Reset(t, testPool)
	mux, _ := newMux(t)
	for _, slug := range []string{"nope-999", "a%00b", "%FF", "has%20space"} {
		t.Run(slug, func(t *testing.T) {
			assertError(t, get(t, mux, "/api/v1/courses/"+slug), http.StatusNotFound, httpx.CodeNotFound)
		})
	}
}

// HAC-10: a request whose time ran out gets 503 timeout, not 500.
func TestHandlers_TimeoutIs503(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	mux, _ := newMux(t)
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for _, path := range []string{
		"/api/v1/courses",
		"/api/v1/courses/csci-151",
		"/api/v1/offerings/" + fx.Offering.ID.String() + "/materials",
		"/api/v1/materials/" + fx.Material.ID.String(),
		"/api/v1/files/" + fx.File.ID.String(),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(expired))
		assertError(t, rec, http.StatusServiceUnavailable, httpx.CodeTimeout)
	}
}
