package api

import "embed"

// SchemaFS embeds the generated JSON Schema files from schema/v1/.
// These are served at GET /api/v1/schemas/{type} and GET /api/v1/openapi.json.
// Regenerate with: make schemas
//
//go:embed schema/v1/*.json
var SchemaFS embed.FS
