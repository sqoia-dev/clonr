package db_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

func openVariantsDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "variants.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestVariants_CreateAndDelete(t *testing.T) {
	d := openVariantsDB(t)
	ctx := context.Background()

	v := db.NodeConfigVariant{
		ID:            uuid.New().String(),
		NodeID:        "node-1",
		AttributePath: "kernel_args",
		ValueJSON:     `"console=ttyS0"`,
		ScopeKind:     db.VariantScopeGlobal,
		ScopeID:       "",
		CreatedAt:     time.Now().UTC(),
	}
	if err := d.CreateVariant(ctx, v); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := d.GetVariant(ctx, v.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AttributePath != v.AttributePath || got.ScopeKind != v.ScopeKind {
		t.Errorf("get returned %+v", got)
	}

	if err := d.DeleteVariant(ctx, v.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := d.GetVariant(ctx, v.ID); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestVariants_DeleteMissing(t *testing.T) {
	d := openVariantsDB(t)
	if err := d.DeleteVariant(context.Background(), "no-such-id"); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestVariants_ListForNodePriorityOrder(t *testing.T) {
	d := openVariantsDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(scope db.VariantScopeKind, scopeID, nodeID, path, val string, t time.Time) db.NodeConfigVariant {
		return db.NodeConfigVariant{
			ID: uuid.New().String(), NodeID: nodeID, AttributePath: path, ValueJSON: val,
			ScopeKind: scope, ScopeID: scopeID, CreatedAt: t,
		}
	}
	role := mk(db.VariantScopeRole, "gpu", "", "kernel_args", `"role"`, now)
	group := mk(db.VariantScopeGroup, "grp-1", "", "kernel_args", `"group"`, now.Add(time.Second))
	direct := mk(db.VariantScopeGlobal, "", "node-1", "kernel_args", `"node"`, now.Add(2*time.Second))

	for _, v := range []db.NodeConfigVariant{role, group, direct} {
		if err := d.CreateVariant(ctx, v); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	got, err := d.ListVariantsForNode(ctx, "node-1", "grp-1", []string{"gpu"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}
	want := []db.VariantScopeKind{db.VariantScopeRole, db.VariantScopeGroup, db.VariantScopeGlobal}
	for i, v := range got {
		if v.ScopeKind != want[i] {
			t.Errorf("[%d] scope = %s, want %s", i, v.ScopeKind, want[i])
		}
	}
}

func TestVariants_RejectsInvalidScope(t *testing.T) {
	d := openVariantsDB(t)
	v := db.NodeConfigVariant{
		ID:            uuid.New().String(),
		AttributePath: "x",
		ValueJSON:     `"y"`,
		ScopeKind:     db.VariantScopeKind("wat"),
		CreatedAt:     time.Now().UTC(),
	}
	if err := d.CreateVariant(context.Background(), v); err == nil {
		t.Errorf("expected error for invalid scope")
	}
}
