// Package httpx holds the pieces every HTTP handler shares: the JSON response
// envelope, error codes, pagination parsing and id parsing.
//
// Keeping them in one place guarantees every endpoint answers in exactly the
// same shape (spec §4), which is what lets the frontend use one generic
// "unwrap the envelope" function.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// Pagination limits (spec §4.4).
const (
	DefaultLimit = 20
	MaxLimit     = 100
	MaxOffset    = 100_000
)

// Error codes returned in error.code (spec §4.3). Clients branch on these,
// never on the human-readable message.
const (
	CodeBadRequest       = "bad_request"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeInternal         = "internal"
	CodeUnavailable      = "unavailable"
)

// Envelope is the shape of every JSON response body.
//
// Data and Error have no `omitempty`, so they are always present (as null when
// unused); Meta does, so it only appears on paginated lists (R-API-4..6).
type Envelope struct {
	Data  any    `json:"data"`
	Error *Error `json:"error"`
	Meta  *Meta  `json:"meta,omitempty"`
}

// Error describes why a request failed.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Meta describes one page of a paginated list.
type Meta struct {
	Total  int64 `json:"total"`  // matches across all pages
	Limit  int   `json:"limit"`  // page size actually applied
	Offset int   `json:"offset"` // index of the first item on this page
}

func writeJSON(w http.ResponseWriter, status int, body Envelope) {
	w.Header().Set("Content-Type", "application/json")
	// Headers must be set before WriteHeader; after it they are ignored.
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so we can't report this to the
		// client. Usually it means the client disconnected; log and move on.
		slog.Error("write json response", "err", err)
	}
}

// OK writes a 200 response carrying data.
func OK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Envelope{Data: data})
}

// List writes a 200 response carrying one page of data plus its Meta.
func List(w http.ResponseWriter, data any, meta Meta) {
	writeJSON(w, http.StatusOK, Envelope{Data: data, Meta: &meta})
}

// Fail writes an error response. message must be safe to show to end users.
func Fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, Envelope{Error: &Error{Code: code, Message: message}})
}

// BadRequest writes a 400.
func BadRequest(w http.ResponseWriter, message string) {
	Fail(w, http.StatusBadRequest, CodeBadRequest, message)
}

// NotFound writes a 404 such as "course not found".
func NotFound(w http.ResponseWriter, what string) {
	Fail(w, http.StatusNotFound, CodeNotFound, what+" not found")
}

// Internal logs err with request details and writes a generic 500.
//
// The real error goes to the log only: database errors can contain table
// names, SQL or connection details that must not reach users (R-API-11).
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "internal error", "err", err, "method", r.Method, "path", r.URL.Path)
	Fail(w, http.StatusInternalServerError, CodeInternal, "something went wrong")
}

// Page is a validated limit/offset pair.
type Page struct {
	Limit  int
	Offset int
}

// ParsePage reads ?limit= and ?offset= (spec §4.4).
//
// URL.Query().Get returns the first value of a repeated parameter and "" for
// a missing or empty one; both cases are part of the spec (R-API-16).
// The bounds also guarantee the values fit in the int32 the SQL layer uses.
func ParsePage(r *http.Request) (Page, error) {
	page := Page{Limit: DefaultLimit}
	query := r.URL.Query()

	if raw := query.Get("limit"); raw != "" {
		// Atoi fails on non-numbers, decimals and numbers too big for int.
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Page{}, errors.New("limit must be a positive integer")
		}
		page.Limit = min(n, MaxLimit) // clamp instead of rejecting large limits
	}
	if raw := query.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > MaxOffset {
			return Page{}, errors.New("offset must be an integer between 0 and 100000")
		}
		page.Offset = n
	}
	return page, nil
}

// ParseUUID parses an id from the URL path.
//
// uuid.Parse is lenient: it also accepts upper case, missing hyphens, braces
// and "urn:uuid:" prefixes. We additionally require the input to equal the
// canonical form so each resource has exactly ONE URL (R-API-18). Otherwise
// a cache purge of /files/abc… would miss a cached copy at /files/ABC….
func ParseUUID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id.String() != raw {
		return uuid.Nil, errors.New("invalid id")
	}
	return id, nil
}
