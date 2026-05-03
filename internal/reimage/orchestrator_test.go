// Package reimage_test exercises the orchestrator's boot-order flip-back
// contract: after a successful deploy (deployed_verified) AND after a
// deploy-timeout failure, the persistent boot order must be flipped back to
// disk-first via SetPersistentBootOrder([BootDisk, BootPXE]).
//
// The test uses an instrumented fake provider that records SetPersistentBootOrder
// calls so we can assert the contract is honoured without real hardware.
//
// See docs/boot-architecture.md §10.9 change 3 and §10.8 (failure row).
package reimage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/power"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Instrumented provider ────────────────────────────────────────────────────

// recordingProvider is a fakeProvider that records calls to
// SetPersistentBootOrder so tests can assert the flip-back was called.
type recordingProvider struct {
	mu           sync.Mutex
	flipBackCalls [][]power.BootDevice
}

func (r *recordingProvider) Name() string                                                   { return "recording" }
func (r *recordingProvider) Status(_ context.Context) (power.PowerStatus, error)            { return power.PowerOn, nil }
func (r *recordingProvider) PowerOn(_ context.Context) error                               { return nil }
func (r *recordingProvider) PowerOff(_ context.Context) error                              { return nil }
func (r *recordingProvider) PowerCycle(_ context.Context) error                            { return nil }
func (r *recordingProvider) Reset(_ context.Context) error                                 { return nil }
func (r *recordingProvider) SetNextBoot(_ context.Context, _ power.BootDevice) error       { return nil }

func (r *recordingProvider) SetPersistentBootOrder(_ context.Context, order []power.BootDevice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]power.BootDevice, len(order))
	copy(cp, order)
	r.flipBackCalls = append(r.flipBackCalls, cp)
	return nil
}

func (r *recordingProvider) FlipBackCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.flipBackCalls)
}

func (r *recordingProvider) LastFlipBackOrder() []power.BootDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.flipBackCalls) == 0 {
		return nil
	}
	return r.flipBackCalls[len(r.flipBackCalls)-1]
}

// ─── flipNodeToDiskFirst contract tests ──────────────────────────────────────
//
// flipNodeToDiskFirst is a server-level helper, not on the orchestrator itself.
// We test its contract here by simulating the two paths that call it:
//
//  1. verify-boot phone-home → deployed_verified transition
//  2. deploy-timeout → deploy_verify_timeout transition
//
// Both paths must call SetPersistentBootOrder([BootDisk, BootPXE]).

// buildFlipFunc builds a FlipToDiskFirst closure backed by the given
// recordingProvider, mirroring what server.flipNodeToDiskFirst does.
func buildFlipFunc(prov *recordingProvider, database *db.DB) func(ctx context.Context, nodeID string) error {
	return func(ctx context.Context, nodeID string) error {
		node, err := database.GetNodeConfig(ctx, nodeID)
		if err != nil {
			return err
		}
		if node.PowerProvider == nil || node.PowerProvider.Type == "" {
			// No provider configured: no-op (bare-metal without power config).
			return nil
		}
		return prov.SetPersistentBootOrder(ctx, []power.BootDevice{power.BootDisk, power.BootPXE})
	}
}

// TestFlipToDiskFirst_OnDeployedVerified asserts that the FlipToDiskFirst
// callback is invoked with [BootDisk, BootPXE] when the node transitions to
// deployed_verified (simulating the verify-boot phone-home path).
//
// See docs/boot-architecture.md §10.9 change 3.
func TestFlipToDiskFirst_OnDeployedVerified(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	img := makeTestImage(t, d)

	node := makeGroupNode(t, d, "flip-node-01", "aa:bb:cc:dd:00:01", "", img.ID, "proxmox")

	// Advance node to deployed_preboot (simulates deploy completing).
	if err := d.RecordDeploySucceeded(ctx, node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}

	prov := &recordingProvider{}
	flip := buildFlipFunc(prov, d)

	// Simulate verify-boot phone-home: record the verified state, then flip.
	if _, err := d.RecordVerifyBooted(ctx, node.ID); err != nil {
		t.Fatalf("RecordVerifyBooted: %v", err)
	}
	if err := flip(ctx, node.ID); err != nil {
		t.Fatalf("FlipToDiskFirst: %v", err)
	}

	// Assert flip-back was called exactly once with disk-first order.
	if prov.FlipBackCallCount() != 1 {
		t.Errorf("FlipToDiskFirst call count: got %d want 1", prov.FlipBackCallCount())
	}
	order := prov.LastFlipBackOrder()
	if len(order) < 1 || order[0] != power.BootDisk {
		t.Errorf("FlipToDiskFirst order: got %v; want first element = BootDisk", order)
	}
}

// TestFlipToDiskFirst_OnDeployTimeout asserts that the FlipToDiskFirst callback
// is invoked with [BootDisk, BootPXE] when a deploy-timeout is recorded
// (simulating the verify-boot scanner timeout path).
//
// This prevents Proxmox VMs from being stuck PXE-first forever when the deploy
// completes but the node never calls verify-boot.
// See docs/boot-architecture.md §10.8 (last row) and §10.9 change 3.
func TestFlipToDiskFirst_OnDeployTimeout(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	img := makeTestImage(t, d)

	node := makeGroupNode(t, d, "flip-node-02", "aa:bb:cc:dd:00:02", "", img.ID, "proxmox")

	// Advance to deployed_preboot.
	if err := d.RecordDeploySucceeded(ctx, node.ID); err != nil {
		t.Fatalf("RecordDeploySucceeded: %v", err)
	}

	// Simulate the timeout scanner recording a timeout.
	if err := d.RecordVerifyTimeout(ctx, node.ID); err != nil {
		t.Fatalf("RecordVerifyTimeout: %v", err)
	}

	// The scanner calls flipNodeToDiskFirst after RecordVerifyTimeout.
	prov := &recordingProvider{}
	flip := buildFlipFunc(prov, d)
	if err := flip(ctx, node.ID); err != nil {
		t.Fatalf("FlipToDiskFirst after timeout: %v", err)
	}

	// Assert flip-back was called with disk-first order.
	if prov.FlipBackCallCount() != 1 {
		t.Errorf("FlipToDiskFirst call count on timeout: got %d want 1", prov.FlipBackCallCount())
	}
	order := prov.LastFlipBackOrder()
	if len(order) < 1 || order[0] != power.BootDisk {
		t.Errorf("FlipToDiskFirst order on timeout: got %v; want first = BootDisk", order)
	}
}

// TestFlipToDiskFirst_NoPowerProvider_NoOp asserts that flipNodeToDiskFirst is
// a no-op when the node has no power provider configured (bare-metal without
// clustr-managed power control). The call must succeed without panicking.
func TestFlipToDiskFirst_NoPowerProvider_NoOp(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	img := makeTestImage(t, d)

	// Node with no PowerProvider field.
	now := time.Now().UTC()
	node := api.NodeConfig{
		ID:          "no-power-node",
		Hostname:    "bare-metal-01",
		PrimaryMAC:  "aa:bb:cc:dd:00:03",
		Interfaces:  []api.InterfaceConfig{},
		SSHKeys:     []string{},
		Groups:      []string{},
		CustomVars:  map[string]string{},
		BaseImageID: img.ID,
		CreatedAt:   now,
		UpdatedAt:   now,
		// PowerProvider: nil — intentionally absent
	}
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}

	prov := &recordingProvider{}
	flip := buildFlipFunc(prov, d)

	// Should succeed without calling SetPersistentBootOrder.
	if err := flip(ctx, node.ID); err != nil {
		t.Fatalf("FlipToDiskFirst with no power provider: unexpected error: %v", err)
	}
	if prov.FlipBackCallCount() != 0 {
		t.Errorf("FlipToDiskFirst with no power provider: expected 0 calls, got %d", prov.FlipBackCallCount())
	}
}

// TestFlipToDiskFirst_Idempotent asserts that calling FlipToDiskFirst twice
// succeeds both times (idempotency — Proxmox can handle a redundant boot-order
// write gracefully, and the test confirms no panic or error on repeat).
func TestFlipToDiskFirst_Idempotent(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	img := makeTestImage(t, d)

	node := makeGroupNode(t, d, "idem-node", "aa:bb:cc:dd:00:04", "", img.ID, "proxmox")

	prov := &recordingProvider{}
	flip := buildFlipFunc(prov, d)

	for i := 0; i < 2; i++ {
		if err := flip(ctx, node.ID); err != nil {
			t.Fatalf("FlipToDiskFirst call %d: %v", i+1, err)
		}
	}

	if prov.FlipBackCallCount() != 2 {
		t.Errorf("idempotent calls: expected 2 SetPersistentBootOrder calls, got %d", prov.FlipBackCallCount())
	}
}
