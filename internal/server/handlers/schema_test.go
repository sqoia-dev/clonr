package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestGetTypeSchema_NodeConfig fetches /api/v1/schemas/NodeConfig and verifies
// the response is valid JSON containing the expected field names.
func TestGetTypeSchema_NodeConfig(t *testing.T) {
	h := NewSchemaHandler()

	// Use chi router to set URL param.
	r := chi.NewRouter()
	r.Get("/api/v1/schemas/{type}", h.GetTypeSchema)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/schemas/NodeConfig", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /schemas/NodeConfig: got %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Must be valid JSON.
	body := w.Body.Bytes()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, body)
	}

	// The schema must have at minimum a "$schema", "$id", or "properties" key.
	hasSchema := raw["$schema"] != nil || raw["$id"] != nil || raw["properties"] != nil
	if !hasSchema {
		t.Errorf("NodeConfig schema missing expected top-level keys; got keys: %v", func() []string {
			ks := make([]string, 0, len(raw))
			for k := range raw {
				ks = append(ks, k)
			}
			return ks
		}())
	}
}

// TestGetTypeSchema_NotFound asserts 404 for unknown types.
func TestGetTypeSchema_NotFound(t *testing.T) {
	h := NewSchemaHandler()

	r := chi.NewRouter()
	r.Get("/api/v1/schemas/{type}", h.GetTypeSchema)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/schemas/NonExistentType", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown type, got %d", w.Code)
	}
}

// TestGetTypeSchema_JSONInjection verifies that a type name containing JSON
// special characters (e.g. a closing quote + extra field) is escaped properly
// and does not produce syntactically broken or semantically injected JSON.
// Regression test for SEC-2 sub-item 1: string-concat injection in the 404 path.
func TestGetTypeSchema_JSONInjection(t *testing.T) {
	h := NewSchemaHandler()

	// A payload that would break naive string concatenation:
	//   {"error":"schema not found for type: foo","admin":true}
	// …if the type name were interpolated without escaping.
	malicious := `foo","admin":true`

	// chi URL routing won't reach the handler if the path contains special chars
	// that the router rejects, so inject the param via chi's RouteContext directly —
	// same technique used across the handler test suite.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/schemas/x", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("type", malicious)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.GetTypeSchema(rr, req)

	body := rr.Body.Bytes()

	// The response must be valid JSON — injection would produce parse failure.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("response is not valid JSON (possible injection): %v\nbody: %s", err, body)
	}

	// The "admin" key must NOT appear at the top level — it should be part of
	// the escaped error string value, not a standalone field.
	if _, found := raw["admin"]; found {
		t.Errorf("injection succeeded: 'admin' key appeared in response JSON\nbody: %s", body)
	}

	// The error string must contain the raw injected text verbatim (escaped).
	errVal, _ := raw["error"].(string)
	if !strings.Contains(errVal, `","admin":true`) {
		t.Errorf("error value does not contain the injected text verbatim; got error=%q", errVal)
	}
}

// TestGetOpenAPI asserts that /api/v1/openapi.json returns valid JSON.
func TestGetOpenAPI(t *testing.T) {
	h := NewSchemaHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	w := httptest.NewRecorder()
	h.GetOpenAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json: got %d, want 200", w.Code)
	}

	var raw map[string]any
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}

	if ver, _ := raw["openapi"].(string); ver != "3.1.0" {
		t.Errorf("openapi version = %q, want 3.1.0", ver)
	}
}
