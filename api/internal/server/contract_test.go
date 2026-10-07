package server_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/Nurasick/NUPP/api/internal/catalog/catalogdb"
	"github.com/Nurasick/NUPP/api/internal/testutil"
)

// specPath is relative to this package's directory, where `go test` runs.
const specPath = "../../openapi/openapi.yaml"

// loadSpec parses and validates openapi.yaml and builds a router that maps a
// request to the operation describing it.
func loadSpec(t *testing.T) routers.Router {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("openapi.yaml is not a valid OpenAPI document: %v", err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return router
}

// AC-31 / R-CON-2: send real requests through the complete server (real
// database, real middleware) and check every JSON response, success and
// error alike, against openapi.yaml. If the code and the spec drift apart,
// this test fails, so the frontend's generated client can trust the spec.
func TestAPIConformsToOpenAPISpec(t *testing.T) {
	testutil.Reset(t, testPool)
	fx := testutil.SeedCatalog(t, testPool)
	general, err := catalogdb.New(testPool).CreateMaterial(context.Background(), catalogdb.CreateMaterialParams{
		OfferingID: fx.Offering.ID, Title: "Lecture notes", Type: "notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newServer(t)
	router := loadSpec(t)

	const unknown = "6f1c2a43-8a8e-4b7c-9d0e-2f3a4b5c6d7e"
	offering, material := fx.Offering.ID.String(), fx.Material.ID.String()
	paths := []string{
		"/healthz",
		// courses
		"/api/v1/courses",
		"/api/v1/courses?q=zzz&limit=5&offset=0",
		"/api/v1/courses?limit=abc",
		"/api/v1/courses?offset=-1",
		"/api/v1/courses/csci-151",
		"/api/v1/courses/unknown",
		// materials
		"/api/v1/offerings/" + offering + "/materials",
		"/api/v1/offerings/" + unknown + "/materials",
		"/api/v1/offerings/not-a-uuid/materials",
		"/api/v1/materials/" + material,
		"/api/v1/materials/" + general.ID.String(), // assessment: null, files: []
		"/api/v1/materials/" + unknown,
		"/api/v1/materials/not-a-uuid",
		// files: only the JSON error responses; success is binary (R-API-13)
		"/api/v1/files/" + unknown,
		"/api/v1/files/not-a-uuid",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			route, params, err := router.FindRoute(req)
			if err != nil {
				t.Fatalf("path is not described by the spec: %v", err)
			}
			input := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{
					Request: req, PathParams: params, Route: route,
				},
				Status: rec.Code,
				Header: rec.Header(),
				Body:   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
				// Without this, statuses missing from the spec would fall back
				// to a "default" response instead of failing.
				Options: &openapi3filter.Options{IncludeResponseStatus: true},
			}
			if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
				t.Errorf("status %d response violates the spec: %v\nbody: %s", rec.Code, err, rec.Body)
			}
		})
	}
}
