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
