package handlers

// schema_drift_test.go — Sprint 42 Day 3
//
// Tests for the SCHEMA-DRIFT-BANNER detection logic.
//
// Coverage:
//   - Hash computation is deterministic across repeated calls
//   - computeSchemaDrift returns status=ok when in-sync (the common case)
//   - The drift detector can distinguish in-sync from divergent schema sets
//   - SchemaDriftHandler.Handle returns 200 with the expected response shape
//   - ComputeSchemaDriftForStartup returns (false, nil) in a normal binary

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSchemaDrift_HashDeterministic verifies that computeAggregateHash returns
// the same value across repeated calls with the same input.
func TestSchemaDrift_HashDeterministic(t *testing.T) {
	files := []string{"Alpha.json", "Beta.json", "Gamma.json"}
	contents := map[string][]byte{
		"Alpha.json": []byte(`{"type":"object"}`),
		"Beta.json":  []byte(`{"type":"string"}`),
		"Gamma.json": []byte(`{"type":"integer"}`),
	}

	h1 := computeAggregateHash(files, contents)
	h2 := computeAggregateHash(files, contents)
	if h1 != h2 {
		t.Errorf("hash is not deterministic: %x != %x", h1, h2)
	}
}

// TestSchemaDrift_HashChangesOnContentChange verifies that modifying a file's
// content produces a different aggregate hash.
func TestSchemaDrift_HashChangesOnContentChange(t *testing.T) {
	files := []string{"Alpha.json"}
	v1 := map[string][]byte{"Alpha.json": []byte(`{"type":"object"}`)}
	v2 := map[string][]byte{"Alpha.json": []byte(`{"type":"string"}`)}

	h1 := computeAggregateHash(files, v1)
	h2 := computeAggregateHash(files, v2)
	if h1 == h2 {
		t.Error("different contents produced the same hash — collision")
	}
}

// TestSchemaDrift_InSync verifies that computeSchemaDrift reports status=ok
// when the embedded schemas and the in-process generated schemas agree.
// In a normal binary where the embedded FS was compiled from the same code
// that generates schemas, they should always match.
func TestSchemaDrift_InSync(t *testing.T) {
	result, err := computeSchemaDrift()
	if err != nil {
		t.Fatalf("computeSchemaDrift: %v", err)
	}
	if result.Status != "ok" {
		t.Errorf("expected status=ok in a consistent binary, got %q; mismatched routes: %v", result.Status, result.MismatchedRoutes)
	}
	if result.BinaryHash == "" {
		t.Error("binary_hash must not be empty")
	}
	if result.EmbeddedHash == "" {
		t.Error("embedded_hash must not be empty")
	}
	if result.BinaryHash != result.EmbeddedHash {
		t.Errorf("binary_hash %q != embedded_hash %q but status=ok; something is wrong", result.BinaryHash, result.EmbeddedHash)
	}
}

// TestSchemaDrift_DriftDetectedWhenContentsDiffer verifies the drift-detection
// logic by exercising computeAggregateHash with known divergent inputs and
// confirming the aggregate hashes differ.
func TestSchemaDrift_DriftDetectedWhenContentsDiffer(t *testing.T) {
	files := []string{"A.json", "B.json"}

	expected := map[string][]byte{
		"A.json": []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`),
		"B.json": []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"string"}`),
	}
	actual := map[string][]byte{
		"A.json": []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`),
		"B.json": []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"integer"}`), // diverged
	}

	h1 := computeAggregateHash(files, expected)
	h2 := computeAggregateHash(files, actual)
	if h1 == h2 {
		t.Error("diverged schemas produced the same aggregate hash — drift detection is broken")
	}
}

// TestSchemaDrift_Handle_OK verifies that the HTTP handler returns 200 with
// the expected JSON shape when schemas are in sync.
func TestSchemaDrift_Handle_OK(t *testing.T) {
	h := &SchemaDriftHandler{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/schema-drift", nil)
	w := httptest.NewRecorder()
	h.Handle(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("Handle: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp SchemaDriftResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Status != "ok" && resp.Status != "drift" {
		t.Errorf("status must be ok or drift, got %q", resp.Status)
	}
	if resp.BinaryHash == "" {
		t.Error("binary_hash must not be empty")
	}
	if resp.EmbeddedHash == "" {
		t.Error("embedded_hash must not be empty")
	}
	if resp.MismatchedRoutes == nil {
		t.Error("mismatched_routes must not be null (must be an empty array when ok)")
	}
	if resp.Status == "ok" && len(resp.MismatchedRoutes) != 0 {
		t.Errorf("status=ok but mismatched_routes is non-empty: %v", resp.MismatchedRoutes)
	}
}

// TestSchemaDrift_ContentType verifies that the handler sets the correct Content-Type.
func TestSchemaDrift_ContentType(t *testing.T) {
	h := &SchemaDriftHandler{}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/schema-drift", nil)
	w := httptest.NewRecorder()
	h.Handle(w, r)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
}

// TestComputeSchemaDriftForStartup verifies the startup wrapper returns false
// (no drift) in a consistent binary build.
func TestComputeSchemaDriftForStartup(t *testing.T) {
	drift, err := ComputeSchemaDriftForStartup()
	if err != nil {
		t.Fatalf("ComputeSchemaDriftForStartup: %v", err)
	}
	if drift {
		t.Error("ComputeSchemaDriftForStartup returned drift=true in a consistent binary — schemas may be stale, run make schemas")
	}
}
