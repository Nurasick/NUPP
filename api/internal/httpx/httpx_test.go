package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nurasick/NUPP/api/internal/httpx"
)

// AC-8, AC-9 / spec §4.4
func TestParsePage(t *testing.T) {
	tests := []struct {
		query   string
		want    httpx.Page
		wantErr bool
	}{
		{"", httpx.Page{Limit: 20, Offset: 0}, false},
		{"limit=5&offset=10", httpx.Page{Limit: 5, Offset: 10}, false},
		{"limit=1000", httpx.Page{Limit: 100, Offset: 0}, false},       // clamped
		{"limit=&offset=", httpx.Page{Limit: 20, Offset: 0}, false},    // empty = default
		{"limit=7&limit=9", httpx.Page{Limit: 7, Offset: 0}, false},    // first value wins
		{"offset=100000", httpx.Page{Limit: 20, Offset: 100000}, false}, // upper bound inclusive
		{"limit=0", httpx.Page{}, true},
		{"limit=-1", httpx.Page{}, true},
		{"limit=abc", httpx.Page{}, true},
		{"limit=99999999999999999999", httpx.Page{}, true}, // overflows int
		{"limit=1.5", httpx.Page{}, true},
		{"offset=-1", httpx.Page{}, true},
		{"offset=100001", httpx.Page{}, true},
		{"offset=99999999999", httpx.Page{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x?"+tt.query, nil)
			got, err := httpx.ParsePage(r)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// R-API-4, R-API-5, R-API-7
func TestFail_WritesErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Fail(rec, http.StatusNotFound, httpx.CodeNotFound, "course not found")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	want := `{"data":null,"error":{"code":"not_found","message":"course not found"}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s, want %s", rec.Body.String(), want)
	}
}

// R-API-6: meta only on paginated lists.
func TestOK_HasNoMeta(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.OK(rec, map[string]string{"status": "ok"})
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := body["meta"]; has {
		t.Fatalf("OK response must not include meta: %s", rec.Body.String())
	}
}

func TestList_IncludesMeta(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.List(rec, []string{}, httpx.Meta{Total: 0, Limit: 20, Offset: 0})
	want := `{"data":[],"error":null,"meta":{"total":0,"limit":20,"offset":0}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s, want %s", rec.Body.String(), want)
	}
}

// AC-16 / R-API-18: only the canonical lower-case form is accepted.
func TestParseUUID(t *testing.T) {
	valid := "6f1c2a43-8a8e-4b7c-9d0e-2f3a4b5c6d7e"
	if id, err := httpx.ParseUUID(valid); err != nil || id.String() != valid {
		t.Errorf("ParseUUID(%q) = %v, %v", valid, id, err)
	}
	for _, bad := range []string{
		"not-a-uuid",
		"",
		"6F1C2A43-8A8E-4B7C-9D0E-2F3A4B5C6D7E", // upper case
		"6f1c2a438a8e4b7c9d0e2f3a4b5c6d7e",     // no hyphens
		"{6f1c2a43-8a8e-4b7c-9d0e-2f3a4b5c6d7e}",
		"urn:uuid:6f1c2a43-8a8e-4b7c-9d0e-2f3a4b5c6d7e",
	} {
		if _, err := httpx.ParseUUID(bad); err == nil {
			t.Errorf("ParseUUID(%q) accepted a non-canonical id", bad)
		}
	}
}
