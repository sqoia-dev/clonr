// cmd/generate-schemas generates JSON Schema v7 + OpenAPI 3.1 documents from
// the exported struct types in pkg/api.
//
// Usage:
//
//	go run ./cmd/generate-schemas [--output-dir pkg/api/schema/v1]
//
// Output:
//
//	pkg/api/schema/v1/<TypeName>.json  — per-type JSON Schema
//	pkg/api/schema/v1/openapi.json     — OpenAPI 3.1 spec referencing all types
//
// The generator is run by `make schemas`. CI fails if the committed schemas
// differ from the freshly generated ones (`git diff --exit-code pkg/api/schema/`).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/invopop/jsonschema"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// schemaTypes is the list of pkg/api struct types to generate schemas for.
// We enumerate them explicitly so the output is deterministic and we don't
// accidentally include internal or helper types.
var schemaTypes = []reflect.Type{
	reflect.TypeOf(api.NodeConfig{}),
	reflect.TypeOf(api.NodeGroup{}),
	reflect.TypeOf(api.NodeGroupWithCount{}),
	reflect.TypeOf(api.BaseImage{}),
	reflect.TypeOf(api.DiskLayout{}),
	reflect.TypeOf(api.StoredDiskLayout{}),
	reflect.TypeOf(api.PartitionSpec{}),
	reflect.TypeOf(api.Bootloader{}),
	reflect.TypeOf(api.RAIDSpec{}),
	reflect.TypeOf(api.ZFSPool{}),
	reflect.TypeOf(api.FstabEntry{}),
	reflect.TypeOf(api.InterfaceConfig{}),
	reflect.TypeOf(api.BMCNodeConfig{}),
	reflect.TypeOf(api.GPGKey{}),
	reflect.TypeOf(api.Rack{}),
	reflect.TypeOf(api.NodeRackPosition{}),
	reflect.TypeOf(api.ErrorResponse{}),
	reflect.TypeOf(api.ListNodesResponse{}),
	reflect.TypeOf(api.ListImagesResponse{}),
	reflect.TypeOf(api.ListNodeGroupsResponse{}),
	reflect.TypeOf(api.ListDiskLayoutsResponse{}),
	reflect.TypeOf(api.ListRacksResponse{}),
	reflect.TypeOf(api.ListGPGKeysResponse{}),
	reflect.TypeOf(api.ListBootEntriesResponse{}),
	reflect.TypeOf(api.BootEntry{}),
	reflect.TypeOf(api.CreateBootEntryRequest{}),
	reflect.TypeOf(api.UpdateBootEntryRequest{}),
	reflect.TypeOf(api.HealthResponse{}),
	reflect.TypeOf(api.LogEntry{}),
	reflect.TypeOf(api.ListLogsResponse{}),
	reflect.TypeOf(api.RegisterRequest{}),
	reflect.TypeOf(api.RegisterResponse{}),
	reflect.TypeOf(api.ReimageRequest{}),
	reflect.TypeOf(api.CreateReimageRequest{}),
	reflect.TypeOf(api.ListReimagesResponse{}),
	reflect.TypeOf(api.NetworkProfile{}),
	reflect.TypeOf(api.BondConfig{}),
	reflect.TypeOf(api.SlurmNodeConfig{}),
	reflect.TypeOf(api.SlurmConfigFile{}),
	reflect.TypeOf(api.SlurmBuild{}),
	reflect.TypeOf(api.DHCPLease{}),
	reflect.TypeOf(api.DHCPLeasesResponse{}),
	reflect.TypeOf(api.Bundle{}),
	reflect.TypeOf(api.ListBundlesResponse{}),
	reflect.TypeOf(api.StoredDiskLayout{}),
	reflect.TypeOf(api.DeployProgress{}),
	reflect.TypeOf(api.BuildState{}),
}

func main() {
	outDir := flag.String("output-dir", "pkg/api/schema/v1", "directory to write schema files")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "generate-schemas: create output dir: %v\n", err)
		os.Exit(1)
	}

	r := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            false,
		// Use the Go type name as the schema $id so references are stable.
		Namer: func(t reflect.Type) string {
			return t.Name()
		},
	}

	var schemas []schemaEntry

	// Deduplicate: StoredDiskLayout was listed twice in schemaTypes.
	seen := map[string]bool{}
	for _, t := range schemaTypes {
		name := t.Name()
		if seen[name] {
			continue
		}
		seen[name] = true

		schema := r.ReflectFromType(t)
		schemas = append(schemas, schemaEntry{name: name, schema: schema})

		out, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate-schemas: marshal %s: %v\n", name, err)
			os.Exit(1)
		}
		path := filepath.Join(*outDir, name+".json")
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "generate-schemas: write %s: %v\n", path, err)
			os.Exit(1)
		}
	}

	// Build OpenAPI 3.1 spec.
	openapi := buildOpenAPI(schemas)
	oaOut, err := json.MarshalIndent(openapi, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate-schemas: marshal openapi: %v\n", err)
		os.Exit(1)
	}
	oaPath := filepath.Join(*outDir, "openapi.json")
	if err := os.WriteFile(oaPath, append(oaOut, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "generate-schemas: write openapi.json: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("generate-schemas: wrote %d type schemas + openapi.json to %s\n", len(schemas), *outDir)
}

// schemaEntry pairs a type name with its generated JSON schema.
type schemaEntry struct {
	name   string
	schema *jsonschema.Schema
}

// openAPISpec is a minimal OpenAPI 3.1 envelope.
type openAPISpec struct {
	OpenAPI    string                     `json:"openapi"`
	Info       openAPIInfo                `json:"info"`
	Components openAPIComponents          `json:"components"`
	Paths      map[string]any             `json:"paths"`
	Servers    []map[string]string        `json:"servers"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type openAPIComponents struct {
	Schemas map[string]*jsonschema.Schema `json:"schemas"`
}

func buildOpenAPI(schemas []schemaEntry) openAPISpec {
	components := openAPIComponents{
		Schemas: make(map[string]*jsonschema.Schema, len(schemas)),
	}
	for _, s := range schemas {
		components.Schemas[s.name] = s.schema
	}

	return openAPISpec{
		OpenAPI: "3.1.0",
		Info: openAPIInfo{
			Title:       "clustr API",
			Description: "clustr-serverd REST API — node cloning and image management for HPC bare-metal clusters.",
			Version:     "v1",
		},
		Components: components,
		Paths:      map[string]any{},
		Servers: []map[string]string{
			{"url": "http://localhost:8080", "description": "Local clustr-serverd"},
		},
	}
}
