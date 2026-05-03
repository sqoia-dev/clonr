package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

func makeReimageRequest(nodeID, imageID string) api.ReimageRequest {
	return api.ReimageRequest{
		ID:          uuid.New().String(),
		NodeID:      nodeID,
		ImageID:     imageID,
		Status:      api.ReimageStatusPending,
		RequestedBy: "api",
		DryRun:      false,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
}

// TestReimageStateTransitions verifies the happy path:
// pending → triggered → in_progress → complete
func TestReimageStateTransitions_HappyPath(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	if err := d.CreateBaseImage(ctx, img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	node := makeNode(uuid.New().String(), img.ID)
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}

	req := makeReimageRequest(node.ID, img.ID)
	if err := d.CreateReimageRequest(ctx, req); err != nil {
		t.Fatalf("create reimage: %v", err)
	}

	// pending → triggered
	if err := d.UpdateReimageRequestStatus(ctx, req.ID, api.ReimageStatusTriggered, ""); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	got, err := d.GetReimageRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after trigger: %v", err)
	}
	if got.Status != api.ReimageStatusTriggered {
		t.Errorf("status: got %s want triggered", got.Status)
	}
	if got.TriggeredAt == nil {
		t.Error("triggered_at should be set")
	}

	// triggered → in_progress
	if err := d.UpdateReimageRequestStatus(ctx, req.ID, api.ReimageStatusInProgress, ""); err != nil {
		t.Fatalf("in_progress: %v", err)
	}
	got, _ = d.GetReimageRequest(ctx, req.ID)
	if got.Status != api.ReimageStatusInProgress {
		t.Errorf("status: got %s want in_progress", got.Status)
	}
	if got.StartedAt == nil {
		t.Error("started_at should be set")
	}

	// in_progress → complete
	if err := d.UpdateReimageRequestStatus(ctx, req.ID, api.ReimageStatusComplete, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, _ = d.GetReimageRequest(ctx, req.ID)
	if got.Status != api.ReimageStatusComplete {
		t.Errorf("status: got %s want complete", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at should be set after complete")
	}
	// No exit code on success.
	if got.ExitCode != nil {
		t.Errorf("exit_code should be nil on success, got %d", *got.ExitCode)
	}
}

// TestReimageStateTransitions_FailedWithExitCode verifies:
// pending → in_progress → failed with exit code/phase captured
func TestReimageStateTransitions_FailedWithExitCode(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)
	node := makeNode(uuid.New().String(), img.ID)
	_ = d.CreateNodeConfig(ctx, node)

	req := makeReimageRequest(node.ID, img.ID)
	_ = d.CreateReimageRequest(ctx, req)

	// pending → in_progress
	_ = d.UpdateReimageRequestStatus(ctx, req.ID, api.ReimageStatusInProgress, "")

	// in_progress → failed with classified exit detail
	const wantCode = 8
	const wantName = "extract"
	const wantPhase = "extract"
	const wantMsg = "deploy failed at extract: tar: ./usr/bin/ld: Cannot hard link: ENOENT"

	if err := d.UpdateReimageRequestFailed(ctx, req.ID, wantMsg, wantCode, wantName, wantPhase); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, err := d.GetReimageRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after failed: %v", err)
	}

	if got.Status != api.ReimageStatusFailed {
		t.Errorf("status: got %s want failed", got.Status)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at should be set on failure")
	}
	if got.ExitCode == nil {
		t.Fatal("exit_code should be set on failure")
	}
	if *got.ExitCode != wantCode {
		t.Errorf("exit_code: got %d want %d", *got.ExitCode, wantCode)
	}
	if got.ExitName != wantName {
		t.Errorf("exit_name: got %s want %s", got.ExitName, wantName)
	}
	if got.Phase != wantPhase {
		t.Errorf("phase: got %s want %s", got.Phase, wantPhase)
	}
	if got.ErrorMessage != wantMsg {
		t.Errorf("error_message: got %q want %q", got.ErrorMessage, wantMsg)
	}
}

// TestReimageListFilterByStatus verifies that ListReimageRequests returns
// all requests for a node (all statuses) and that callers can filter.
func TestReimageListFilterByStatus(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)
	node := makeNode(uuid.New().String(), img.ID)
	_ = d.CreateNodeConfig(ctx, node)

	// Create one succeeded and one failed request.
	req1 := makeReimageRequest(node.ID, img.ID)
	_ = d.CreateReimageRequest(ctx, req1)
	_ = d.UpdateReimageRequestStatus(ctx, req1.ID, api.ReimageStatusComplete, "")

	req2 := makeReimageRequest(node.ID, img.ID)
	_ = d.CreateReimageRequest(ctx, req2)
	_ = d.UpdateReimageRequestFailed(ctx, req2.ID, "tar error", 8, "extract", "extract")

	all, err := d.ListReimageRequests(ctx, node.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("count: got %d want 2", len(all))
	}

	// Verify the failed record carries exit detail.
	var failedReq *api.ReimageRequest
	for i := range all {
		if all[i].Status == api.ReimageStatusFailed {
			failedReq = &all[i]
		}
	}
	if failedReq == nil {
		t.Fatal("no failed request found in list")
	}
	if failedReq.ExitCode == nil || *failedReq.ExitCode != 8 {
		t.Errorf("exit_code in list: got %v", failedReq.ExitCode)
	}
	if failedReq.Phase != "extract" {
		t.Errorf("phase in list: got %s", failedReq.Phase)
	}
}

// TestGetActiveReimageForNode verifies that non-terminal records are found
// and terminal records are excluded.
func TestGetActiveReimageForNode(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)
	node := makeNode(uuid.New().String(), img.ID)
	_ = d.CreateNodeConfig(ctx, node)

	// No active reimage — should return nil.
	active, err := d.GetActiveReimageForNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("get active (empty): %v", err)
	}
	if active != nil {
		t.Errorf("expected nil for empty node, got %+v", active)
	}

	// Create an in-progress request — should be active.
	req := makeReimageRequest(node.ID, img.ID)
	_ = d.CreateReimageRequest(ctx, req)
	_ = d.UpdateReimageRequestStatus(ctx, req.ID, api.ReimageStatusInProgress, "")

	active, err = d.GetActiveReimageForNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("get active (in_progress): %v", err)
	}
	if active == nil {
		t.Fatal("expected active request, got nil")
	}
	if active.ID != req.ID {
		t.Errorf("active id: got %s want %s", active.ID, req.ID)
	}

	// Transition to failed — should no longer be active.
	_ = d.UpdateReimageRequestFailed(ctx, req.ID, "boom", 5, "download", "download")

	active, err = d.GetActiveReimageForNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("get active (after failed): %v", err)
	}
	if active != nil {
		t.Errorf("expected nil after terminal state, got %+v", active)
	}
}
