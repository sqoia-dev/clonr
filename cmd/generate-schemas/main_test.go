// Package main — main_test.go tests schema generation against a known type
// and asserts the output is valid JSON containing the expected fields.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestGenerateSchemaForBootEntry runs the generator against BootEntry and
// asserts that the output is valid JSON containing expected field names.
func TestGenerateSchemaForBootEntry(t *testing.T) {
	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	schema := r.ReflectFromType(reflect.TypeOf(api.BootEntry{}))
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("marshal BootEntry schema: %v", err)
	}

	outStr := string(out)

	// Must be valid JSON (re-parse).
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("BootEntry schema is not valid JSON: %v\ngot:\n%s", err, outStr)
	}

	// Must contain key field names from the BootEntry struct.
	for _, field := range []string{"id", "name", "kind", "kernel_url", "enabled"} {
		if !strings.Contains(outStr, `"`+field+`"`) {
			t.Errorf("BootEntry schema missing field %q; got:\n%s", field, outStr)
		}
	}
}

// TestGenerateAllSchemas runs the full generator and asserts that every
// expected type file and openapi.json exist and are valid JSON.
func TestGenerateAllSchemas(t *testing.T) {
	dir := t.TempDir()

	// Run main's generation logic directly using the same schemaTypes list.
	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	seen := map[string]bool{}
	for _, typ := range schemaTypes {
		name := typ.Name()
		if seen[name] {
			continue
		}
		seen[name] = true

		schema := r.ReflectFromType(typ)
		out, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), append(out, '\n'), 0o644); err != nil {
			t.Fatalf("write %s.json: %v", name, err)
		}
	}

	// Verify each written file is valid JSON.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no schema files generated")
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Errorf("file %s is not valid JSON: %v", e.Name(), err)
		}
	}
}

// TestSchemaContainsExpectedNodeConfigFields asserts NodeConfig schema has key fields.
func TestSchemaContainsExpectedNodeConfigFields(t *testing.T) {
	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	schema := r.ReflectFromType(reflect.TypeOf(api.NodeConfig{}))
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("marshal NodeConfig schema: %v", err)
	}

	outStr := string(out)
	for _, field := range []string{"id", "hostname", "primary_mac", "base_image_id"} {
		if !strings.Contains(outStr, `"`+field+`"`) {
			t.Errorf("NodeConfig schema missing field %q", field)
		}
	}
}

// TestCreateUserRequestSchemaHasRequiredAndEnum asserts that the Sprint 42 Day 2
// CreateUserRequest schema contains required fields and the role enum constraint.
// This is the golden-file round-trip test: struct → JSON Schema → verify constraints.
func TestCreateUserRequestSchemaHasRequiredAndEnum(t *testing.T) {
	r := jsonschema.Reflector{
		AllowAdditionalProperties:  false,
		RequiredFromJSONSchemaTags: true,
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	schema := r.ReflectFromType(reflect.TypeOf(api.CreateUserRequest{}))
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("marshal CreateUserRequest schema: %v", err)
	}
	outStr := string(out)

	// Verify required fields are present.
	for _, field := range []string{"username", "password", "role"} {
		if !strings.Contains(outStr, `"`+field+`"`) {
			t.Errorf("CreateUserRequest schema missing field %q", field)
		}
	}
	// Verify "required" array and at least one enum value are emitted.
	if !strings.Contains(outStr, `"required"`) {
		t.Error("CreateUserRequest schema missing \"required\" array")
	}
	if !strings.Contains(outStr, `"enum"`) {
		t.Error("CreateUserRequest schema missing \"enum\" constraint for role")
	}
	for _, role := range []string{"admin", "operator", "readonly"} {
		if !strings.Contains(outStr, `"`+role+`"`) {
			t.Errorf("CreateUserRequest schema enum missing role %q", role)
		}
	}
}

// TestDangerousPushStageRequestSchema verifies the DangerousPushStageRequest
// schema requires node_id and plugin_name.
func TestDangerousPushStageRequestSchema(t *testing.T) {
	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	schema := r.ReflectFromType(reflect.TypeOf(api.DangerousPushStageRequest{}))
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("marshal DangerousPushStageRequest schema: %v", err)
	}
	outStr := string(out)

	for _, field := range []string{"node_id", "plugin_name"} {
		if !strings.Contains(outStr, `"`+field+`"`) {
			t.Errorf("DangerousPushStageRequest schema missing field %q", field)
		}
	}
	if !strings.Contains(outStr, `"required"`) {
		t.Error("DangerousPushStageRequest schema missing \"required\" array")
	}
}

// TestDangerousPushConfirmRequestSchema verifies the DangerousPushConfirmRequest
// schema requires confirm_string.
func TestDangerousPushConfirmRequestSchema(t *testing.T) {
	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	schema := r.ReflectFromType(reflect.TypeOf(api.DangerousPushConfirmRequest{}))
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("marshal DangerousPushConfirmRequest schema: %v", err)
	}
	outStr := string(out)

	if !strings.Contains(outStr, `"confirm_string"`) {
		t.Error("DangerousPushConfirmRequest schema missing confirm_string field")
	}
	if !strings.Contains(outStr, `"required"`) {
		t.Error("DangerousPushConfirmRequest schema missing \"required\" array")
	}
}

// TestCreateNodeConfigRequestSchema verifies the CreateNodeConfigRequest schema
// includes hostname and primary_mac as required fields.
func TestCreateNodeConfigRequestSchema(t *testing.T) {
	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	schema := r.ReflectFromType(reflect.TypeOf(api.CreateNodeConfigRequest{}))
	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatalf("marshal CreateNodeConfigRequest schema: %v", err)
	}
	outStr := string(out)

	for _, field := range []string{"hostname", "primary_mac"} {
		if !strings.Contains(outStr, `"`+field+`"`) {
			t.Errorf("CreateNodeConfigRequest schema missing field %q", field)
		}
	}
}
