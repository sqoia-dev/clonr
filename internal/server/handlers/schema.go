// Package handlers — schema.go serves the embedded JSON Schema files (#161).
//
// Routes:
//
//	GET /api/v1/schemas/{type}    — per-type JSON Schema (e.g. /api/v1/schemas/NodeConfig)
//	GET /api/v1/openapi.json      — OpenAPI 3.1 spec
package handlers

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// SchemaHandler serves embedded JSON Schema files.
type SchemaHandler struct {
	// schemaFS is the embedded schema FS sub-tree rooted at schema/v1.
	schemaFS fs.FS
}

// NewSchemaHandler constructs a SchemaHandler using the embedded schema files.
func NewSchemaHandler() *SchemaHandler {
	sub, err := fs.Sub(api.SchemaFS, "schema/v1")
	if err != nil {
		// Fires when: the embed path "schema/v1" does not exist in api.SchemaFS,
		// which can only happen if the go:embed directive in pkg/api/ was changed
		// or the generated schema directory was deleted before compilation.
		// Why panic vs error-return: this is a build-time invariant; if it fires the
		// binary cannot serve any schema route and returning nil would silently corrupt
		// every /api/v1/schemas/* response.  Panic at init time surfaces the mistake
		// immediately during development and is impossible to trigger at steady-state.
		panic("schema: sub FS: " + err.Error())
	}
	return &SchemaHandler{schemaFS: sub}
}


// GetTypeSchema handles GET /api/v1/schemas/{type}.
// Returns the JSON Schema for the named type (e.g. "NodeConfig").
// Returns 404 if the type has no generated schema file.
func (h *SchemaHandler) GetTypeSchema(w http.ResponseWriter, r *http.Request) {
	typeName := chi.URLParam(r, "type")
	// Sanitise: reject names containing path separators or dots (path traversal guard).
	if typeName == "" || strings.ContainsAny(typeName, "/\\.") {
		http.Error(w, `{"error":"invalid type name"}`, http.StatusBadRequest)
		return
	}

	data, err := fs.ReadFile(h.schemaFS, typeName+".json")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		msg, _ := json.Marshal(api.ErrorResponse{
			Error: "schema not found for type: " + typeName,
			Code:  "not_found",
		})
		_, _ = w.Write(msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// GetOpenAPI handles GET /api/v1/openapi.json.
// Returns the full OpenAPI 3.1 spec.
func (h *SchemaHandler) GetOpenAPI(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(h.schemaFS, "openapi.json")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"openapi spec not available"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
