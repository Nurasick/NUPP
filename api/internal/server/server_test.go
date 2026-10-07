package server_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nurasick/NUPP/api/internal/server"
	"github.com/Nurasick/NUPP/api/internal/storage"
	"github.com/Nurasick/NUPP/api/internal/testutil"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(testutil.RunWithDB(m, &testPool)) }

// newServer builds the complete production handler (routes + middleware).
func newServer(t *testing.T) http.Handler {
	t.Helper()
	files, err := storage.NewLocal(testutil.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return server.New(server.Deps{Pool: testPool, Files: files, Logger: slog.New(slog.DiscardHandler)})
}

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// AC-26 / R-EP-1
func TestHealthz_ReportsOK(t *testing.T) {
	rec := serve(newServer(t), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
}

// R-EP-1: an unreachable database makes the health check fail with 503.
func TestHealthz_DatabaseDownIs503(t *testing.T) {
	// pgxpool.New doesn't connect until first use, so a pool pointing at a
	// closed port is easy to build; every Ping on it fails.
	deadPool, err := pgxpool.New(context.Background(), "postgres://nobody:nothing@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer deadPool.Close()
	files, _ := storage.NewLocal(testutil.TempDir(t))
	h := server.New(server.Deps{Pool: deadPool, Files: files, Logger: slog.New(slog.DiscardHandler)})

	rec := serve(h, http.MethodGet, "/healthz")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("status = %d body = %s, want 503 unavailable", rec.Code, rec.Body)
	}
}

// AC-27 / R-API-3
func TestUnknownAPIRoute_IsJSON404(t *testing.T) {
	rec := serve(newServer(t), http.MethodGet, "/api/v1/nope")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d content-type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Errorf("body = %s", rec.Body)
	}
}

// AC-27 / R-API-3
func TestWrongMethod_IsJSON405WithAllow(t *testing.T) {
	h := newServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := serve(h, method, "/api/v1/courses")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, rec.Code)
			continue
		}
		if rec.Header().Get("Allow") != "GET, HEAD" || !strings.Contains(rec.Body.String(), `"code":"method_not_allowed"`) {
			t.Errorf("%s Allow = %q body = %s", method, rec.Header().Get("Allow"), rec.Body)
		}
	}
}

// R-API-2: HEAD is answered for GET routes.
func TestHead_IsSupportedOnGetRoutes(t *testing.T) {
	if rec := serve(newServer(t), http.MethodHead, "/api/v1/courses"); rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
}

// AC-28 / R-OPS-4
func TestResponses_CarrySecurityHeaders(t *testing.T) {
	h := newServer(t)
	for _, path := range []string{"/healthz", "/api/v1/courses", "/api/v1/nope"} {
		rec := serve(h, http.MethodGet, path)
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "strict-origin-when-cross-origin",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", path, header, got, want)
			}
		}
	}
}
