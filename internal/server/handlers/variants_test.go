package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakeVariantsDB is an in-memory variant store for tests.
type fakeVariantsDB struct {
	nodes    map[string]api.NodeConfig
	variants map[string]db.NodeConfigVariant
}

func newFakeVariantsDB() *fakeVariantsDB {
	return &fakeVariantsDB{
		nodes:    map[string]api.NodeConfig{},
		variants: map[string]db.NodeConfigVariant{},
	}
}

func (f *fakeVariantsDB) GetNodeConfig(_ context.Context, id string) (api.NodeConfig, error) {
	if cfg, ok := f.nodes[id]; ok {
		return cfg, nil
	}
	return api.NodeConfig{}, api.ErrNotFound
}

func (f *fakeVariantsDB) CreateVariant(_ context.Context, v db.NodeConfigVariant) error {
	if _, exists := f.variants[v.ID]; exists {
		return fmt.Errorf("variant %s already exists", v.ID)
	}
	f.variants[v.ID] = v
	return nil
}

func (f *fakeVariantsDB) DeleteVariant(_ context.Context, id string) error {
	if _, ok := f.variants[id]; !ok {
		return api.ErrNotFound
	}
	delete(f.variants, id)
	return nil
}

func (f *fakeVariantsDB) GetVariant(_ context.Context, id string) (db.NodeConfigVariant, error) {
	v, ok := f.variants[id]
	if !ok {
		return db.NodeConfigVariant{}, api.ErrNotFound
	}
	return v, nil
}

func (f *fakeVariantsDB) ListVariantsForNode(_ context.Context, nodeID, groupID string, roles []string) ([]db.NodeConfigVariant, error) {
	out := []db.NodeConfigVariant{}
	// 1. role first (lowest priority)
	for _, role := range roles {
		for _, v := range f.variants {
			if v.ScopeKind == db.VariantScopeRole && v.ScopeID == role {
				out = append(out, v)
			}
		}
	}
	// 2. group
	if groupID != "" {
		for _, v := range f.variants {
			if v.ScopeKind == db.VariantScopeGroup && v.ScopeID == groupID {
				out = append(out, v)
			}
		}
	}
	// 3. node-direct (highest)
	for _, v := range f.variants {
		if v.ScopeKind == db.VariantScopeGlobal && v.NodeID == nodeID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeVariantsDB) ListAllVariants(_ context.Context) ([]db.NodeConfigVariant, error) {
	out := make([]db.NodeConfigVariant, 0, len(f.variants))
	for _, v := range f.variants {
		out = append(out, v)
	}
	return out, nil
}

func newVariantsRouter(h *VariantsHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/v1/variants", h.Create)
	r.Delete("/api/v1/variants/{id}", h.Delete)
	r.Get("/api/v1/variants", h.List)
	r.Get("/api/v1/nodes/{id}/effective-config", h.GetEffectiveConfig)
	return r
}

// ─── apply / setAtPath unit tests ─────────────────────────────────────────────

func TestSetAtPath_Scalar(t *testing.T) {
	tree := map[string]any{"kernel_args": "old"}
	got, err := setAtPath(tree, "kernel_args", "new")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if m := got.(map[string]any); m["kernel_args"] != "new" {
		t.Errorf("got %+v", m)
	}
}

func TestSetAtPath_NestedMap(t *testing.T) {
	tree := map[string]any{"bmc": map[string]any{"username": "old", "ip_address": "10.0.0.1"}}
	got, err := setAtPath(tree, "bmc.username", "new")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if m := got.(map[string]any)["bmc"].(map[string]any); m["username"] != "new" || m["ip_address"] != "10.0.0.1" {
		t.Errorf("got %+v", m)
	}
}

func TestSetAtPath_ArrayIndex(t *testing.T) {
	tree := map[string]any{"interfaces": []any{
		map[string]any{"name": "eth0", "ip_address": "10.0.0.1"},
	}}
	got, err := setAtPath(tree, "interfaces[0].ip_address", "10.0.0.2")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	arr := got.(map[string]any)["interfaces"].([]any)
	if arr[0].(map[string]any)["ip_address"] != "10.0.0.2" {
		t.Errorf("got %+v", arr)
	}
}

func TestSetAtPath_CreatesIntermediates(t *testing.T) {
	tree := map[string]any{}
	got, err := setAtPath(tree, "custom_vars.GPU_TYPE", "A100")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v := got.(map[string]any)["custom_vars"].(map[string]any)["GPU_TYPE"]; v != "A100" {
		t.Errorf("got %+v", got)
	}
}

// ─── Resolver priority tests ──────────────────────────────────────────────────

func TestApplyVariants_PriorityRoleLessGroupLessNode(t *testing.T) {
	cfg := api.NodeConfig{
		ID:         "n1",
		Hostname:   "compute-001",
		KernelArgs: "console=ttyS0",
		Tags:       []string{"role:gpu"},
		GroupID:    "grp-1",
	}
	now := time.Now().UTC()
	variants := []db.NodeConfigVariant{
		// Role overlay (lowest).
		{
			ID:            "v1",
			ScopeKind:     db.VariantScopeRole,
			ScopeID:       "gpu",
			AttributePath: "kernel_args",
			ValueJSON:     `"console=ttyS0 nvidia_drm.modeset=1"`,
			CreatedAt:     now,
		},
		// Group overlay (middle) — should beat the role overlay.
		{
			ID:            "v2",
			ScopeKind:     db.VariantScopeGroup,
			ScopeID:       "grp-1",
			AttributePath: "kernel_args",
			ValueJSON:     `"console=ttyS0 group_only=true"`,
			CreatedAt:     now.Add(time.Second),
		},
		// Node-direct (highest) — should beat both.
		{
			ID:            "v3",
			ScopeKind:     db.VariantScopeGlobal,
			NodeID:        "n1",
			AttributePath: "kernel_args",
			ValueJSON:     `"node_specific=yes"`,
			CreatedAt:     now.Add(2 * time.Second),
		},
	}

	overlay, err := ApplyVariants(&cfg, variants)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg.KernelArgs != "node_specific=yes" {
		t.Errorf("kernel_args = %q, want node-direct override", cfg.KernelArgs)
	}
	if overlay["kernel_args"] != "global" {
		t.Errorf("overlay = %v, want kernel_args=global (node-direct)", overlay)
	}
}

func TestApplyVariants_NonOverlappingPathsCombine(t *testing.T) {
	cfg := api.NodeConfig{
		ID:         "n1",
		KernelArgs: "console=ttyS0",
		Tags:       []string{"role:gpu"},
		GroupID:    "grp-1",
	}
	variants := []db.NodeConfigVariant{
		{ScopeKind: db.VariantScopeRole, ScopeID: "gpu", AttributePath: "kernel_args", ValueJSON: `"role-args"`},
		{ScopeKind: db.VariantScopeGroup, ScopeID: "grp-1", AttributePath: "fqdn", ValueJSON: `"compute-001.cluster.example"`},
		{ScopeKind: db.VariantScopeGlobal, NodeID: "n1", AttributePath: "hostname", ValueJSON: `"compute-001-node"`},
	}
	overlay, err := ApplyVariants(&cfg, variants)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg.KernelArgs != "role-args" {
		t.Errorf("kernel_args = %q", cfg.KernelArgs)
	}
	if cfg.FQDN != "compute-001.cluster.example" {
		t.Errorf("fqdn = %q", cfg.FQDN)
	}
	if cfg.Hostname != "compute-001-node" {
		t.Errorf("hostname = %q", cfg.Hostname)
	}
	if overlay["kernel_args"] != "role" || overlay["fqdn"] != "group" || overlay["hostname"] != "global" {
		t.Errorf("overlay = %+v", overlay)
	}
}

// ─── HTTP endpoint tests ──────────────────────────────────────────────────────

func TestVariants_CreateAndDelete(t *testing.T) {
	dbFake := newFakeVariantsDB()
	dbFake.nodes["n1"] = api.NodeConfig{ID: "n1"}
	h := &VariantsHandler{DB: dbFake}
	r := newVariantsRouter(h)

	body, _ := json.Marshal(map[string]any{
		"node_id":        "n1",
		"attribute_path": "kernel_args",
		"value":          "console=ttyS0 quiet",
		"scope_kind":     "global",
	})
	req := httptest.NewRequest("POST", "/api/v1/variants", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp VariantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID == "" {
		t.Errorf("missing id")
	}
	if resp.NodeID != "n1" {
		t.Errorf("node_id = %q", resp.NodeID)
	}

	// Now delete it.
	delReq := httptest.NewRequest("DELETE", "/api/v1/variants/"+resp.ID, nil)
	delW := httptest.NewRecorder()
	r.ServeHTTP(delW, delReq)
	if delW.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", delW.Code, delW.Body.String())
	}
	if _, err := dbFake.GetVariant(context.Background(), resp.ID); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("expected variant to be cleared, got %v", err)
	}
}

func TestVariants_DeleteNotFound(t *testing.T) {
	h := &VariantsHandler{DB: newFakeVariantsDB()}
	r := newVariantsRouter(h)
	req := httptest.NewRequest("DELETE", "/api/v1/variants/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestVariants_CreateValidation(t *testing.T) {
	h := &VariantsHandler{DB: newFakeVariantsDB()}
	r := newVariantsRouter(h)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{
			name: "bad scope_kind",
			body: map[string]any{"attribute_path": "x", "value": 1, "scope_kind": "bogus"},
			want: 400,
		},
		{
			name: "group missing scope_id",
			body: map[string]any{"attribute_path": "x", "value": 1, "scope_kind": "group"},
			want: 400,
		},
		{
			name: "missing attribute_path",
			body: map[string]any{"value": 1, "scope_kind": "global", "node_id": "n1"},
			want: 400,
		},
		{
			name: "bad attribute_path",
			body: map[string]any{"attribute_path": "$ev/il", "value": 1, "scope_kind": "global", "node_id": "n1"},
			want: 400,
		},
		{
			name: "global cluster-wide without confirm",
			body: map[string]any{"attribute_path": "kernel_args", "value": "x", "scope_kind": "global"},
			want: 400,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("POST", "/api/v1/variants", bytes.NewReader(body))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d, body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestVariants_EffectiveConfigReflectsOverlay(t *testing.T) {
	dbFake := newFakeVariantsDB()
	dbFake.nodes["n1"] = api.NodeConfig{
		ID:         "n1",
		Hostname:   "compute-001",
		KernelArgs: "console=ttyS0",
		Tags:       []string{"role:gpu"},
		GroupID:    "grp-1",
	}
	dbFake.variants["v1"] = db.NodeConfigVariant{
		ID:            "v1",
		ScopeKind:     db.VariantScopeRole,
		ScopeID:       "gpu",
		AttributePath: "kernel_args",
		ValueJSON:     `"console=ttyS0 gpu-overlay"`,
		CreatedAt:     time.Now().UTC(),
	}

	h := &VariantsHandler{DB: dbFake}
	r := newVariantsRouter(h)

	req := httptest.NewRequest("GET", "/api/v1/nodes/n1/effective-config", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp EffectiveConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Config.KernelArgs != "console=ttyS0 gpu-overlay" {
		t.Errorf("kernel_args overlay not applied: %q", resp.Config.KernelArgs)
	}
	if resp.OverlayBy["kernel_args"] != "role" {
		t.Errorf("overlay_by = %v", resp.OverlayBy)
	}
	if len(resp.Variants) != 1 {
		t.Errorf("variants returned = %d, want 1", len(resp.Variants))
	}
}

func TestValidAttributePath(t *testing.T) {
	good := []string{
		"kernel_args",
		"bmc.username",
		"interfaces[0]",
		"interfaces[0].ip_address",
		"custom_vars.GPU_TYPE",
		"custom-key.nested",
	}
	bad := []string{
		"",
		".leading",
		"trailing.",
		"$evil",
		"a..b",
		"a[abc]",
	}
	for _, p := range good {
		if !validAttributePath(p) {
			t.Errorf("expected %q to be valid", p)
		}
	}
	for _, p := range bad {
		if validAttributePath(p) {
			t.Errorf("expected %q to be rejected", p)
		}
	}
}
