package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
	"github.com/Nurasick/NUPP/api/internal/httpx"
	"github.com/Nurasick/NUPP/api/internal/storage"
)

// maxSearchLen is the longest accepted search text, in characters (R-EP-2 table).
const maxSearchLen = 100

// likeEscaper prefixes LIKE's special characters with a backslash (PostgreSQL's
// default LIKE escape character) so user input is matched literally: searching
// for "%" finds a literal percent sign instead of matching everything (R-EP-3).
// The backslash itself must be escaped first, which NewReplacer guarantees by
// replacing in a single left-to-right pass.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Handler serves the public catalog endpoints.
type Handler struct {
	q     *catalogdb.Queries
	files storage.Storage
}

// NewHandler builds a catalog handler that reads from pool and serves file
// contents from files.
func NewHandler(pool *pgxpool.Pool, files storage.Storage) *Handler {
	return &Handler{q: catalogdb.New(pool), files: files}
}

// Register adds the catalog routes to mux.
//
// Go 1.22+ patterns can include the HTTP method and {wildcards}:
// "GET /api/v1/courses/{slug}" matches only GET (and HEAD), and the handler
// reads the wildcard with r.PathValue("slug").
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/courses", h.listCourses)
	mux.HandleFunc("GET /api/v1/courses/{slug}", h.getCourse)
	mux.HandleFunc("GET /api/v1/offerings/{id}/materials", h.listMaterials)
	mux.HandleFunc("GET /api/v1/materials/{id}", h.getMaterial)
	mux.HandleFunc("GET /api/v1/files/{id}", h.getFile) // see files.go
}

// listCourses handles GET /api/v1/courses?q=&limit=&offset= (spec §5.2).
func (h *Handler) listCourses(w http.ResponseWriter, r *http.Request) {
	page, err := httpx.ParsePage(r)
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	// A URL can carry any bytes (%FF, %00). PostgreSQL text must be valid
	// UTF-8 without NUL bytes and would reject them with an error, turning a
	// bad request into a 500. Reject them here instead.
	if !utf8.ValidString(search) || strings.ContainsRune(search, 0) {
		httpx.BadRequest(w, "q contains invalid characters")
		return
	}
	// Count characters (runes), not bytes: "қ" is one character but two bytes.
	if utf8.RuneCountInString(search) > maxSearchLen {
		httpx.BadRequest(w, fmt.Sprintf("q must be at most %d characters", maxSearchLen))
		return
	}
	pattern := likeEscaper.Replace(search)

	courses, err := h.q.SearchCourses(r.Context(), catalogdb.SearchCoursesParams{
		Query: pattern,
		// The conversions to int32 are safe because ParsePage caps limit at
		// httpx.MaxLimit and offset at httpx.MaxOffset, far below int32's max.
		RowLimit:  int32(page.Limit),
		RowOffset: int32(page.Offset),
	})
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("search courses: %w", err))
		return
	}
	total, err := h.q.CountCourses(r.Context(), pattern)
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("count courses: %w", err))
		return
	}

	items := make([]CourseSummary, 0, len(courses))
	for _, c := range courses {
		items = append(items, toCourseSummary(c))
	}
	httpx.List(w, items, httpx.Meta{Total: total, Limit: page.Limit, Offset: page.Offset})
}

// getCourse handles GET /api/v1/courses/{slug} (spec §5.3).
func (h *Handler) getCourse(w http.ResponseWriter, r *http.Request) {
	// Slugs are stored lower-case (a DB CHECK enforces it), so lower-casing
	// the input makes the lookup case-insensitive (R-EP-6).
	slug := strings.ToLower(r.PathValue("slug"))
	// Anything that isn't shaped like a slug can't exist, so answer 404
	// without asking the database (which would also reject NUL bytes with an
	// error rather than "no rows").
	if !validSlug.MatchString(slug) {
		httpx.NotFound(w, "course")
		return
	}
	course, err := h.q.GetCourseBySlug(r.Context(), slug)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.NotFound(w, "course")
		return
	}
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("get course %q: %w", slug, err))
		return
	}

	// Two queries total, no matter how many offerings the course has.
	offerings, err := h.q.ListOfferingsByCourse(r.Context(), course.ID)
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("list offerings: %w", err))
		return
	}
	assessments, err := h.q.ListAssessmentsByCourse(r.Context(), course.ID)
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("list assessments: %w", err))
		return
	}
	httpx.OK(w, buildCourseDetail(course, offerings, assessments))
}

// listMaterials handles GET /api/v1/offerings/{id}/materials (spec §5.4).
func (h *Handler) listMaterials(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseUUID(r.PathValue("id"))
	if err != nil {
		httpx.BadRequest(w, "invalid offering id")
		return
	}
	// Check the offering exists first so we can tell "unknown offering" (404)
	// apart from "offering with no materials yet" (200 with []), R-EP-11.
	if _, err := h.q.GetOffering(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		httpx.NotFound(w, "offering")
		return
	} else if err != nil {
		httpx.Internal(w, r, fmt.Errorf("get offering: %w", err))
		return
	}

	materials, err := h.q.ListVisibleMaterialsByOffering(r.Context(), id)
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("list materials: %w", err))
		return
	}
	items := make([]MaterialSummary, 0, len(materials))
	for _, m := range materials {
		items = append(items, toMaterialSummary(m))
	}
	httpx.OK(w, items)
}

// getMaterial handles GET /api/v1/materials/{id} (spec §5.5).
func (h *Handler) getMaterial(w http.ResponseWriter, r *http.Request) {
	id, err := httpx.ParseUUID(r.PathValue("id"))
	if err != nil {
		httpx.BadRequest(w, "invalid material id")
		return
	}
	// GetVisibleMaterial filters hidden materials, so a hidden one is
	// indistinguishable from one that never existed (R-API-20).
	material, err := h.q.GetVisibleMaterial(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.NotFound(w, "material")
		return
	}
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("get material: %w", err))
		return
	}
	where, err := h.q.GetMaterialContext(r.Context(), id)
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("get material context: %w", err))
		return
	}
	files, err := h.q.ListMaterialFiles(r.Context(), id)
	if err != nil {
		httpx.Internal(w, r, fmt.Errorf("list material files: %w", err))
		return
	}
	httpx.OK(w, buildMaterialDetail(material, where, files))
}
