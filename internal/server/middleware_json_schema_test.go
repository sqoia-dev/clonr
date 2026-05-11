package server

// middleware_json_schema_test.go — Sprint 42 Day 2 tests for JSON-SCHEMA middleware
// and MULTI-ERROR-ROLLUP.
//
// Test coverage:
//   - valid body passes through unchanged
//   - invalid body returns 400 + violations array (multi-error)
//   - empty body passes through (no-op)
//   - body is rewindable for downstream handler
//   - route not in registry passes through (identity middleware)
//   - missing required field triggers violation
//   - wrong type triggers violation
//   - enum violation triggers violation
//   - multiple violations returned in one response

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildTestRegistry builds a SchemaRegistry against the real embedded schemas.
// Tests that exercise specific schemas use routeKey strings from schemaRouteMap.
func buildTestRegistry(t *testing.T) *SchemaRegistry {
	t.Helper()
	reg, err := newSchemaRegistry()
	if err != nil {
		t.Fatalf("newSchemaRegistry: %v", err)
	}
	return reg
}

// makeRequest builds an HTTP POST request with the given JSON body.
func makeRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.ContentLength = int64(len(body))
	return r
}

// parseViolationsResponse decodes a 400 response body as a ValidationErrorResponse.
func parseViolationsResponse(t *testing.T, body []byte) ValidationErrorResponse {
	t.Helper()
	var resp ValidationErrorResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode violations response: %v\nbody: %s", err, body)
	}
	return resp
}

// TestMiddlewareRegistryBuilds verifies that newSchemaRegistry succeeds and
// compiles all registered route schemas from the embedded FS.
func TestMiddlewareRegistryBuilds(t *testing.T) {
	reg := buildTestRegistry(t)
	for routeKey := range schemaRouteMap {
		if reg.get(routeKey) == nil {
			t.Errorf("schema not compiled for route %q", routeKey)
		}
	}
}

// TestMiddlewareUnknownRoutePassesThrough verifies that a route not in the
// registry returns the identity middleware (no schema validation at all).
func TestMiddlewareUnknownRoutePassesThrough(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/nonexistent")

	handlerCalled := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := makeRequest(t, `{"garbage": true}`)
	handler.ServeHTTP(w, r)

	if !handlerCalled {
		t.Error("handler not called for unregistered route")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

// TestMiddlewareEmptyBodyPassesThrough verifies that a request with no body
// (or zero ContentLength) is forwarded to the downstream handler without
// validation so the handler can produce its own "required" error.
func TestMiddlewareEmptyBodyPassesThrough(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/users")

	handlerCalled := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	// Request with nil body (ContentLength=0 triggers the early-exit path).
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.ContentLength = 0
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !handlerCalled {
		t.Error("handler not called for empty body")
	}
}

// TestMiddlewareValidBodyPassesThrough verifies that a structurally valid
// body is forwarded to the downstream handler without modification.
func TestMiddlewareValidBodyPassesThrough(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/users")

	validBody := `{"username":"alice","password":"Password1","role":"admin"}`
	var receivedBody []byte
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body in handler: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := makeRequest(t, validBody)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	// The body must be rewindable — downstream handler must receive the original bytes.
	if string(receivedBody) != validBody {
		t.Errorf("downstream received %q; want %q", receivedBody, validBody)
	}
}

// TestMiddlewareMissingRequiredField verifies that a body missing a required
// field returns 400 with a violations array containing a "required" entry.
func TestMiddlewareMissingRequiredField(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/users")

	// password and role are missing.
	body := `{"username":"alice"}`
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should not be called on validation failure")
	}))

	w := httptest.NewRecorder()
	r := makeRequest(t, body)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	resp := parseViolationsResponse(t, w.Body.Bytes())
	if resp.Error != "validation_failed" {
		t.Errorf("error field = %q, want %q", resp.Error, "validation_failed")
	}
	if len(resp.Violations) == 0 {
		t.Fatal("violations array is empty; want at least one violation")
	}

	// At least one violation must be "required".
	hasRequired := false
	for _, v := range resp.Violations {
		if v.Code == "required" {
			hasRequired = true
			break
		}
	}
	if !hasRequired {
		t.Errorf("no required violation found; got: %+v", resp.Violations)
	}
}

// TestMiddlewareMultipleViolations verifies the MULTI-ERROR-ROLLUP: when required
// fields are missing, the response contains at least one violation with a structured
// error response (not just a 400 with an opaque string).
// Note: JSON Schema draft 2020-12 reports "required" as a single object-level
// constraint violation rather than per-field violations, so a {} body returns
// one "required" violation at the object path.
func TestMiddlewareMultipleViolations(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/users")

	// All three required fields missing.
	body := `{}`
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should not be called on validation failure")
	}))

	w := httptest.NewRecorder()
	r := makeRequest(t, body)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	resp := parseViolationsResponse(t, w.Body.Bytes())
	if resp.Error != "validation_failed" {
		t.Errorf("error = %q; want validation_failed", resp.Error)
	}
	if len(resp.Violations) == 0 {
		t.Fatal("violations array is empty; want at least one violation")
	}
	// The response must be parseable as a ValidationErrorResponse with violations.
	// MULTI-ERROR-ROLLUP: the response must list every violation the schema can
	// detect. For JSON Schema 2020-12, "required" is one object-level keyword
	// but a body with both a missing required field AND a type mismatch will
	// surface two separate violations (one per keyword). The key guarantee is:
	// NOT just the first violation — all keywords that fail are reported.

	// Verify the violations include a "required" code.
	hasRequired := false
	for _, v := range resp.Violations {
		if v.Code == "required" {
			hasRequired = true
			break
		}
	}
	if !hasRequired {
		t.Errorf("no required violation found in %+v", resp.Violations)
	}

	// Additionally: combining required + enum violations should yield >= 2.
	// Send a body with username set but invalid role to trigger both required
	// (password missing) and enum (role invalid).
	body2 := `{"username":"alice","role":"superuser"}`
	mw2 := jsonSchemaMiddleware(reg, "POST /api/v1/users")
	w2 := httptest.NewRecorder()
	r2 := makeRequest(t, body2)
	mw2(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not be called")
	})).ServeHTTP(w2, r2)

	if w2.Code == http.StatusOK {
		t.Fatal("expected 400 for body with missing required and invalid enum")
	}
	resp2 := parseViolationsResponse(t, w2.Body.Bytes())
	if len(resp2.Violations) < 2 {
		t.Errorf("expected >= 2 violations (required+enum) for body2; got %d: %+v",
			len(resp2.Violations), resp2.Violations)
	}
}

// TestMiddlewareEnumViolation verifies that an invalid enum value (role not in
// allowed list) produces a violation with code "enum".
func TestMiddlewareEnumViolation(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/users")

	body := `{"username":"alice","password":"Password1","role":"superuser"}`
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should not be called on validation failure")
	}))

	w := httptest.NewRecorder()
	r := makeRequest(t, body)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	resp := parseViolationsResponse(t, w.Body.Bytes())
	hasEnum := false
	for _, v := range resp.Violations {
		if v.Code == "enum" {
			hasEnum = true
			break
		}
	}
	if !hasEnum {
		t.Errorf("no enum violation found; got: %+v", resp.Violations)
	}
}

// TestMiddlewareBodyRewindable verifies that after a successful validation the
// downstream handler can fully read the request body (the middleware rewinds it).
func TestMiddlewareBodyRewindable(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/nodes")

	validBody := `{"hostname":"n01","primary_mac":"bc:24:11:aa:bb:cc"}`
	var readBytes []byte
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		readBytes, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read rewound body: %v", err)
		}
	}))

	w := httptest.NewRecorder()
	r := makeRequest(t, validBody)
	handler.ServeHTTP(w, r)

	if !bytes.Equal(readBytes, []byte(validBody)) {
		t.Errorf("rewound body = %q; want %q", readBytes, validBody)
	}
}

// TestMiddlewareInvalidJSONPassesThrough verifies that a non-JSON body is
// forwarded to the downstream handler without validation (the handler produces
// the error, not the middleware).
func TestMiddlewareInvalidJSONPassesThrough(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/users")

	body := `not json at all`
	handlerCalled := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusBadRequest) // handler decides what to return
	}))

	w := httptest.NewRecorder()
	r := makeRequest(t, body)
	handler.ServeHTTP(w, r)

	if !handlerCalled {
		t.Error("handler not called for non-JSON body")
	}
}

// TestMiddlewareDangerousPushStageValidation tests the dangerous-push stage
// endpoint schema: missing node_id and plugin_name both produce violations.
func TestMiddlewareDangerousPushStageValidation(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/config/dangerous-push")

	cases := []struct {
		name         string
		body         string
		wantStatus   int
		wantViolCode string
	}{
		{
			name:       "valid",
			body:       `{"node_id":"abc123","plugin_name":"slurm"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:         "missing node_id",
			body:         `{"plugin_name":"slurm"}`,
			wantStatus:   http.StatusBadRequest,
			wantViolCode: "required",
		},
		{
			name:         "missing plugin_name",
			body:         `{"node_id":"abc123"}`,
			wantStatus:   http.StatusBadRequest,
			wantViolCode: "required",
		},
		{
			name:         "empty object",
			body:         `{}`,
			wantStatus:   http.StatusBadRequest,
			wantViolCode: "required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			w := httptest.NewRecorder()
			r := makeRequest(t, tc.body)
			handler.ServeHTTP(w, r)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantViolCode != "" {
				resp := parseViolationsResponse(t, w.Body.Bytes())
				found := false
				for _, v := range resp.Violations {
					if v.Code == tc.wantViolCode {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("violation code %q not found in %+v", tc.wantViolCode, resp.Violations)
				}
			}
		})
	}
}

// TestMiddlewareConfirmValidation tests the confirm endpoint schema.
func TestMiddlewareConfirmValidation(t *testing.T) {
	reg := buildTestRegistry(t)
	mw := jsonSchemaMiddleware(reg, "POST /api/v1/config/dangerous-push/{pending_id}/confirm")

	t.Run("valid", func(t *testing.T) {
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		w := httptest.NewRecorder()
		r := makeRequest(t, `{"confirm_string":"clustr"}`)
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("missing confirm_string", func(t *testing.T) {
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("downstream should not be called on validation failure")
		}))
		w := httptest.NewRecorder()
		r := makeRequest(t, `{}`)
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
		}
		resp := parseViolationsResponse(t, w.Body.Bytes())
		if len(resp.Violations) == 0 {
			t.Error("violations array empty")
		}
	})
}

// TestWriteValidationViolations verifies the helper function that formats the
// MULTI-ERROR-ROLLUP response.
func TestWriteValidationViolations(t *testing.T) {
	violations := []ValidationViolation{
		{Path: "/username", Message: "required field missing", Code: "required"},
		{Path: "/role", Message: "value is not one of the allowed values", Code: "enum"},
	}

	w := httptest.NewRecorder()
	writeValidationViolations(w, violations)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var resp ValidationErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "validation_failed" {
		t.Errorf("error = %q, want validation_failed", resp.Error)
	}
	if len(resp.Violations) != 2 {
		t.Errorf("violations count = %d, want 2", len(resp.Violations))
	}
}
