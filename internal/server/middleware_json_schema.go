package server

// middleware_json_schema.go — Sprint 42 Day 2 JSON-SCHEMA middleware.
//
// Validates request bodies against pre-compiled JSON Schemas (draft 2020-12)
// derived from the canonical Go request structs in pkg/api. On validation
// failure, returns a 400 with a structured multi-error array (MULTI-ERROR-ROLLUP)
// that lists every violation rather than only the first.
//
// Usage in buildRouter:
//
//	reg := newSchemaRegistry()
//	r.With(jsonSchemaMiddleware(reg, "POST /api/v1/users")).Post("/users", usersH.HandleCreate)
//
// The registry is built once at server startup; schemas are compiled from the
// embedded pkg/api/schema/v1/*.json files. The middleware is a no-op for routes
// not in the registry and for requests with no body.
//
// Library choice: github.com/santhosh-tekuri/jsonschema/v6 — supports draft
// 2020-12, returns all violation details via BasicOutput() (path + message +
// keyword), and has no transitive dependencies beyond golang.org/x/text.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ValidationViolation is a single schema violation in the MULTI-ERROR-ROLLUP response.
type ValidationViolation struct {
	// Path is the JSON Pointer (RFC 6901) path to the violating field within the
	// request body (e.g. "/node_id", "/payload/timeout_seconds").
	Path string `json:"path"`
	// Message is a human-readable description of the constraint that was violated.
	Message string `json:"message"`
	// Code is a machine-readable keyword identifying the violated constraint
	// (e.g. "required", "minLength", "type", "enum").
	Code string `json:"code"`
}

// ValidationErrorResponse is the 400 body returned when JSON-SCHEMA validation fails.
type ValidationErrorResponse struct {
	Error      string                `json:"error"`
	Violations []ValidationViolation `json:"violations"`
}

// SchemaRegistry maps a canonical route key ("METHOD /path/pattern") to a
// compiled JSON Schema validator. Build once at startup with newSchemaRegistry.
//
// THREAD-SAFETY: SchemaRegistry is read-only after construction — all concurrent
// readers are safe without a mutex. It is never written after newSchemaRegistry returns.
type SchemaRegistry struct {
	compiled map[string]*jsonschema.Schema
}

// newEmptySchemaRegistry returns a registry with no compiled schemas (all routes
// pass through without validation). Used as a graceful fallback when the embedded
// schema files cannot be compiled at startup.
func newEmptySchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{compiled: map[string]*jsonschema.Schema{}}
}

// schemaRouteMap lists the (route-key → schema-type-name) pairs that should be
// validated. The type name must match a file in pkg/api/schema/v1/<name>.json.
// Add new entries here when broadening coverage to more routes.
var schemaRouteMap = map[string]string{
	"POST /api/v1/config/dangerous-push":                      "DangerousPushStageRequest",
	"POST /api/v1/config/dangerous-push/{pending_id}/confirm": "DangerousPushConfirmRequest",
	"POST /api/v1/admin/users":                                "CreateUserRequest",
	"POST /api/v1/users":                                      "CreateUserRequest",
	"POST /api/v1/nodes":                                      "CreateNodeConfigRequest",
}

// newSchemaRegistry reads the embedded schema files from pkg/api.SchemaFS,
// compiles each one, and builds a SchemaRegistry for the routes listed in
// schemaRouteMap. Returns an error if any schema cannot be parsed or compiled.
func newSchemaRegistry() (*SchemaRegistry, error) {
	compiler := jsonschema.NewCompiler()

	// Pre-load every schema from the embedded FS into the compiler so that
	// $ref cross-references between schema files resolve without HTTP round-trips.
	// We use jsonschema.UnmarshalJSON to decode the schema bytes into the
	// library's internal representation (preserves number types correctly).
	entries, err := api.SchemaFS.ReadDir("schema/v1")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := api.SchemaFS.ReadFile("schema/v1/" + e.Name())
		if err != nil {
			return nil, err
		}
		// Decode the schema bytes into the library's internal representation.
		decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		// Use a synthetic "file:///schema/v1/<name>" URL as the schema ID so the
		// compiler can resolve $ref cross-references by absolute URI.
		schemaURL := "file:///schema/v1/" + e.Name()
		if addErr := compiler.AddResource(schemaURL, decoded); addErr != nil {
			return nil, addErr
		}
	}

	reg := &SchemaRegistry{
		compiled: make(map[string]*jsonschema.Schema, len(schemaRouteMap)),
	}

	for routeKey, typeName := range schemaRouteMap {
		schemaURL := "file:///schema/v1/" + typeName + ".json"
		compiled, compErr := compiler.Compile(schemaURL)
		if compErr != nil {
			return nil, compErr
		}
		reg.compiled[routeKey] = compiled
	}

	return reg, nil
}

// get returns the compiled schema for the given route key, or nil if the route
// is not in the registry.
func (r *SchemaRegistry) get(routeKey string) *jsonschema.Schema {
	return r.compiled[routeKey]
}

// jsonSchemaMiddleware returns a chi middleware that validates the request body
// against the schema registered for the given routeKey.
//
//   - If the route has no registered schema: the request passes through unchanged.
//   - If the body is empty or not JSON: the request passes through (structural
//     errors like "not valid JSON" are handled by the downstream handler's own
//     decoder, which already returns a clear error).
//   - If the body is present and the schema is registered: parse as JSON, run
//     all validators, collect every violation, return 400 + violations array on failure.
//   - On success: rewind the body to a fresh reader so the downstream handler can
//     re-decode it without hitting EOF.
func jsonSchemaMiddleware(reg *SchemaRegistry, routeKey string) func(http.Handler) http.Handler {
	compiled := reg.get(routeKey)
	if compiled == nil {
		// No schema for this route — return identity middleware.
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || r.ContentLength == 0 {
				// Empty body — pass through; handler will reject if required.
				next.ServeHTTP(w, r)
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20)) // 2 MiB cap
			_ = r.Body.Close()
			if err != nil {
				http.Error(w, `{"error":"failed to read request body"}`, http.StatusBadRequest)
				return
			}

			// Empty body after trimming whitespace: pass through to handler.
			if len(bytes.TrimSpace(body)) == 0 {
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(w, r)
				return
			}

			// Decode into the library's internal representation using
			// jsonschema.UnmarshalJSON so number types are preserved correctly
			// (the library uses its own decoder that differs from encoding/json).
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
			if err != nil {
				// Not valid JSON — pass through; handler's decoder produces a better error.
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(w, r)
				return
			}

			// Validate against the compiled schema. On success Validate returns nil.
			if err := compiled.Validate(doc); err != nil {
				violations := extractViolations(err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(ValidationErrorResponse{
					Error:      "validation_failed",
					Violations: violations,
				})
				return
			}

			// Validation passed — rewind the body for the downstream handler.
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

// extractViolations converts a santhosh-tekuri/jsonschema ValidationError into a
// flat slice of ValidationViolation using BasicOutput() (MULTI-ERROR-ROLLUP).
//
// BasicOutput() flattens the error tree into a single list of leaf errors.
// Each entry has InstanceLocation (JSON Pointer to the failing field) and
// a human-readable Error string. We parse the KeywordLocation tail to extract
// the constraint keyword (required, minLength, type, enum, …).
func extractViolations(err error) []ValidationViolation {
	if err == nil {
		return nil
	}

	valErr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []ValidationViolation{{
			Path:    "/",
			Message: err.Error(),
			Code:    "unknown",
		}}
	}

	// BasicOutput() returns a flat list of all leaf errors — one per violated
	// constraint — which is exactly the MULTI-ERROR-ROLLUP requirement.
	basic := valErr.BasicOutput()
	if basic == nil {
		return []ValidationViolation{{
			Path:    "/",
			Message: valErr.Error(),
			Code:    "validation_error",
		}}
	}

	// The root OutputUnit is the envelope; actual violations are in Errors.
	// If Errors is empty the root itself describes the single violation.
	units := basic.Errors
	if len(units) == 0 {
		// Single top-level violation.
		units = []jsonschema.OutputUnit{*basic}
	}

	violations := make([]ValidationViolation, 0, len(units))
	seen := make(map[string]struct{}, len(units))

	for _, unit := range units {
		if unit.Valid {
			continue
		}
		path := unit.InstanceLocation
		if path == "" {
			path = "/"
		}
		code := keywordFromLocation(unit.KeywordLocation)
		msg := ""
		if unit.Error != nil {
			msg = unit.Error.String()
		}
		if msg == "" {
			msg = violationMessage(code)
		}

		// Deduplicate by (path, code): $ref expansion can surface the same
		// constraint twice (once for the ref, once for the inlined schema).
		dedupeKey := path + "|" + code
		if _, dup := seen[dedupeKey]; dup {
			continue
		}
		seen[dedupeKey] = struct{}{}

		violations = append(violations, ValidationViolation{
			Path:    path,
			Message: msg,
			Code:    code,
		})
	}

	if len(violations) == 0 {
		violations = append(violations, ValidationViolation{
			Path:    "/",
			Message: valErr.Error(),
			Code:    "validation_error",
		})
	}

	return violations
}

// keywordFromLocation extracts the leaf keyword name from a JSON Pointer keyword
// location string (e.g. "/properties/node_id/required" → "required").
func keywordFromLocation(loc string) string {
	if loc == "" {
		return "validation_error"
	}
	if idx := strings.LastIndex(loc, "/"); idx >= 0 {
		k := loc[idx+1:]
		if k != "" {
			return k
		}
	}
	return loc
}

// violationMessage returns a human-readable message for common schema keywords.
// Falls back to the keyword itself for unrecognised constraints.
func violationMessage(keyword string) string {
	switch keyword {
	case "required":
		return "required field missing"
	case "minLength":
		return "value must not be empty"
	case "type":
		return "value has wrong type"
	case "enum":
		return "value is not one of the allowed values"
	case "minimum":
		return "value is below the minimum"
	case "maximum":
		return "value is above the maximum"
	case "exclusiveMinimum":
		return "value must be greater than the minimum"
	case "exclusiveMaximum":
		return "value must be less than the maximum"
	case "pattern":
		return "value does not match the required pattern"
	case "maxLength":
		return "value exceeds the maximum length"
	default:
		if keyword == "" {
			return "constraint violated"
		}
		return "constraint violated: " + keyword
	}
}

// writeValidationViolations writes a 400 with the MULTI-ERROR-ROLLUP shape.
// Exported for use by handlers that accumulate non-schema validation errors
// and want to return them in the same structured format.
func writeValidationViolations(w http.ResponseWriter, violations []ValidationViolation) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	if encErr := json.NewEncoder(w).Encode(ValidationErrorResponse{
		Error:      "validation_failed",
		Violations: violations,
	}); encErr != nil {
		log.Error().Err(encErr).Msg("json-schema middleware: failed to encode violations")
	}
}
