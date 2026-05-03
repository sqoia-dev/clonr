package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/db"
)

// TestAutoPolicyConfig_GetAndUpdate verifies the singleton config round-trip.
func TestAutoPolicyConfig_GetAndUpdate(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Default row seeded by migration 073.
	cfg, err := d.GetAutoPolicyConfig(ctx)
	if err != nil {
		t.Fatalf("GetAutoPolicyConfig: %v", err)
	}
	if cfg.Enabled {
		t.Error("default config should be disabled")
	}
	if cfg.DefaultRole != "compute" {
		t.Errorf("default role: got %q want compute", cfg.DefaultRole)
	}

	// Update.
	cfg.Enabled = true
	cfg.DefaultNodeCount = 4
	cfg.DefaultRole = "gpu"
	cfg.NotifyAdminsOnCreate = true
	if err := d.UpdateAutoPolicyConfig(ctx, *cfg); err != nil {
		t.Fatalf("UpdateAutoPolicyConfig: %v", err)
	}

	got, err := d.GetAutoPolicyConfig(ctx)
	if err != nil {
		t.Fatalf("GetAutoPolicyConfig after update: %v", err)
	}
	if !got.Enabled {
		t.Error("enabled should be true after update")
	}
	if got.DefaultNodeCount != 4 {
		t.Errorf("node_count: got %d want 4", got.DefaultNodeCount)
	}
	if got.DefaultRole != "gpu" {
		t.Errorf("role: got %q want gpu", got.DefaultRole)
	}
}

// TestAutoComputeState_SetAndGet verifies the state JSON round-trip.
func TestAutoComputeState_SetAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("auto-test-group", "compute")
	if err := d.CreateNodeGroupFull(ctx, g); err != nil {
		t.Fatalf("create group: %v", err)
	}

	// No state yet.
	state, fin, err := d.GetAutoComputeState(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetAutoComputeState (empty): %v", err)
	}
	if state != "" {
		t.Errorf("expected empty state, got %q", state)
	}
	if fin != nil {
		t.Error("expected nil finalized_at")
	}

	// Set state.
	stateJSON := `{"v":"1","node_group_id":"` + g.ID + `","created_at":"2026-04-27T00:00:00Z"}`
	if err := d.SetAutoComputeState(ctx, g.ID, stateJSON); err != nil {
		t.Fatalf("SetAutoComputeState: %v", err)
	}

	state2, fin2, err := d.GetAutoComputeState(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetAutoComputeState after set: %v", err)
	}
	if state2 != stateJSON {
		t.Errorf("state: got %q want %q", state2, stateJSON)
	}
	if fin2 != nil {
		t.Error("finalized_at should be nil before finalization")
	}
}

// TestAutoComputeState_Finalize verifies the 24-hour window finalizer.
func TestAutoComputeState_Finalize(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("finalize-group", "compute")
	if err := d.CreateNodeGroupFull(ctx, g); err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := d.SetAutoComputeState(ctx, g.ID, `{"v":"1"}`); err != nil {
		t.Fatalf("SetAutoComputeState: %v", err)
	}

	// Finalize.
	if err := d.FinalizeAutoComputeState(ctx, g.ID); err != nil {
		t.Fatalf("FinalizeAutoComputeState: %v", err)
	}

	_, fin, err := d.GetAutoComputeState(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetAutoComputeState after finalize: %v", err)
	}
	if fin == nil {
		t.Error("finalized_at should be set after finalization")
	}
	if time.Since(*fin) > 5*time.Second {
		t.Errorf("finalized_at should be recent, got %s", fin)
	}
}

// TestAutoComputeState_ListPending verifies the pending group scanner.
func TestAutoComputeState_ListPending(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g1 := makeGroup("pending-a", "compute")
	g2 := makeGroup("pending-b", "compute")
	g3 := makeGroup("pending-finalized", "compute")
	for _, g := range []interface{ GetID() string }{} {
		_ = g
	}

	if err := d.CreateNodeGroupFull(ctx, g1); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateNodeGroupFull(ctx, g2); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateNodeGroupFull(ctx, g3); err != nil {
		t.Fatal(err)
	}

	_ = d.SetAutoComputeState(ctx, g1.ID, `{"v":"1"}`)
	_ = d.SetAutoComputeState(ctx, g2.ID, `{"v":"1"}`)
	_ = d.SetAutoComputeState(ctx, g3.ID, `{"v":"1"}`)
	// Finalize g3 — it should not appear in pending list.
	_ = d.FinalizeAutoComputeState(ctx, g3.ID)

	pending, err := d.ListPendingAutoComputeGroups(ctx)
	if err != nil {
		t.Fatalf("ListPendingAutoComputeGroups: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("pending count: got %d want 2", len(pending))
	}
	ids := map[string]bool{}
	for _, p := range pending {
		ids[p.GroupID] = true
	}
	if !ids[g1.ID] || !ids[g2.ID] {
		t.Error("expected g1 and g2 in pending list")
	}
	if ids[g3.ID] {
		t.Error("g3 is finalized and should not appear in pending list")
	}
}

// TestAutoComputeState_Clear verifies undo clears the state.
func TestAutoComputeState_Clear(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("clear-group", "compute")
	if err := d.CreateNodeGroupFull(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = d.SetAutoComputeState(ctx, g.ID, `{"v":"1"}`)

	if err := d.ClearAutoComputeState(ctx, g.ID); err != nil {
		t.Fatalf("ClearAutoComputeState: %v", err)
	}

	state, _, err := d.GetAutoComputeState(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetAutoComputeState after clear: %v", err)
	}
	if state != "" {
		t.Errorf("state should be empty after clear, got %q", state)
	}
}

// TestOnboardingCompleted verifies the wizard completion flag.
func TestOnboardingCompleted(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Create a test user.
	userID := uuid.New().String()
	err := d.CreateUser(ctx, db.UserRecord{
		ID:           userID,
		Username:     "onboard-test-user",
		PasswordHash: "pw-hash",
		Role:         db.UserRolePI,
		CreatedAt:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Initially not completed.
	done, err := d.IsOnboardingCompleted(ctx, userID)
	if err != nil {
		t.Fatalf("IsOnboardingCompleted: %v", err)
	}
	if done {
		t.Error("onboarding should not be completed initially")
	}

	// Mark as completed.
	if err := d.MarkOnboardingCompleted(ctx, userID); err != nil {
		t.Fatalf("MarkOnboardingCompleted: %v", err)
	}

	done2, err := d.IsOnboardingCompleted(ctx, userID)
	if err != nil {
		t.Fatalf("IsOnboardingCompleted after mark: %v", err)
	}
	if !done2 {
		t.Error("onboarding should be completed after mark")
	}

	// Idempotent — can call again without error.
	if err := d.MarkOnboardingCompleted(ctx, userID); err != nil {
		t.Fatalf("MarkOnboardingCompleted (idempotent): %v", err)
	}
}
