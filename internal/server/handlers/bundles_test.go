package handlers

// bundles_test.go — httptest coverage for BundlesHandler.
//
// Covers:
//   - Empty state: no slurm_builds rows → GET returns {bundles:[], total:0}
//   - DELETE /api/v1/bundles/builtin returns HTTP 404 (not 409 — no special case)
//   - DELETE /api/v1/bundles/<unknown-uuid> returns HTTP 404

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/pkg/api"
)

func newBundlesHandler(t *testing.T) *BundlesHandler {
	t.Helper()
	return &BundlesHandler{
		DB: openTestDB(t),
	}
}

// routeBundles wires BundlesHandler into a minimal chi router.
func routeBundles(h *BundlesHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/bundles", h.ListBundles)
	r.Delete("/api/v1/bundles/{id}", h.DeleteBundle)
	return r
}

// TestListBundles_EmptyState asserts that when slurm_builds is empty,
// GET /api/v1/bundles returns HTTP 200 with {bundles:[], total:0}.
func TestListBundles_EmptyState(t *testing.T) {
	h := newBundlesHandler(t)
	srv := routeBundles(h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bundles", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListBundlesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Bundles) != 0 {
		t.Errorf("expected 0 bundles, got %d", len(resp.Bundles))
	}
	if resp.Total != 0 {
		t.Errorf("expected total=0, got %d", resp.Total)
	}
}

// TestDeleteBundle_BuiltinReturns404 asserts that DELETE /api/v1/bundles/builtin
// returns HTTP 404 (not 409). There is no special-cased "builtin" row;
// the ID simply doesn't exist in slurm_builds.
func TestDeleteBundle_BuiltinReturns404(t *testing.T) {
	h := newBundlesHandler(t)
	srv := routeBundles(h)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/bundles/builtin", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if resp.Code != "not_found" {
		t.Errorf("expected code='not_found', got %q", resp.Code)
	}
}

// TestDeleteBundle_UnknownIDReturns404 asserts that any unrecognised UUID
// also returns HTTP 404.
func TestDeleteBundle_UnknownIDReturns404(t *testing.T) {
	h := newBundlesHandler(t)
	srv := routeBundles(h)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/bundles/00000000-0000-0000-0000-000000000000", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}
