// reconcile_test.go — unit tests for ReconcileFromNode.
// Uses a fake NodeExecer that returns scripted getent output without
// a real node connection.
package sysaccounts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/sysaccounts"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── fake NodeExecer ──────────────────────────────────────────────────────────

// fakeHub implements sysaccounts.NodeExecer.
// For each registered exec_request it immediately delivers a scripted result
// based on the username in the args.
type fakeHub struct {
	mu       sync.Mutex
	pending  map[string]chan clientd.ExecResultPayload
	results  map[string]clientd.ExecResultPayload // keyed by username
	online   bool
}

func newFakeHub(online bool) *fakeHub {
	return &fakeHub{
		online:  online,
		pending: make(map[string]chan clientd.ExecResultPayload),
		results: make(map[string]clientd.ExecResultPayload),
	}
}

func (h *fakeHub) setResult(username string, r clientd.ExecResultPayload) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results[username] = r
}

func (h *fakeHub) IsConnected(string) bool { return h.online }

func (h *fakeHub) RegisterExec(msgID string) <-chan clientd.ExecResultPayload {
	ch := make(chan clientd.ExecResultPayload, 1)
	h.mu.Lock()
	h.pending[msgID] = ch
	h.mu.Unlock()
	return ch
}

func (h *fakeHub) UnregisterExec(msgID string) {
	h.mu.Lock()
	delete(h.pending, msgID)
	h.mu.Unlock()
}

// Send intercepts the exec_request, extracts the username from args, looks up
// the scripted result, and delivers it on the pending channel.
func (h *fakeHub) Send(nodeID string, msg clientd.ServerMessage) error {
	var req clientd.ExecRequestPayload
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		return fmt.Errorf("fakeHub: unmarshal payload: %w", err)
	}

	// args = ["passwd", "<username>"]
	var username string
	if len(req.Args) >= 2 {
		username = req.Args[1]
	}

	h.mu.Lock()
	result, ok := h.results[username]
	ch := h.pending[req.RefMsgID]
	h.mu.Unlock()

	if !ok {
		// No scripted result → simulate account not found (exit 2).
		result = clientd.ExecResultPayload{
			RefMsgID: req.RefMsgID,
			ExitCode: 2,
			Stderr:   username + ": not found",
		}
	} else {
		result.RefMsgID = req.RefMsgID
	}

	if ch != nil {
		go func() {
			// Small delay to let the caller enter the select.
			time.Sleep(1 * time.Millisecond)
			ch <- result
		}()
	}
	return nil
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestReconcileFromNode_FixesMisAllocatedUID verifies that a munge row with
// UID 10003 (Sprint 13 mis-allocation) gets updated to 996 (the on-node value).
func TestReconcileFromNode_FixesMisAllocatedUID(t *testing.T) {
	d := openTestDB(t)
	m := sysaccounts.New(d, nil)
	ctx := context.Background()

	// Insert munge with the bad UID (bypasses the hard guard by inserting directly
	// into the DB via the test helper).
	if err := d.SysAccountsCreateAccount(ctx, api.SystemAccount{
		ID:         "acct-munge",
		Username:   "munge",
		UID:        10003, // mis-allocated
		PrimaryGID: 996,
		Shell:      "/sbin/nologin",
		HomeDir:    "/var/run/munge",
	}); err != nil {
		t.Fatalf("insert munge: %v", err)
	}

	hub := newFakeHub(true)
	// getent returns: munge:x:996:996:Munge:/var/run/munge:/sbin/nologin
	hub.setResult("munge", clientd.ExecResultPayload{
		ExitCode: 0,
		Stdout:   "munge:x:996:996:Munge:/var/run/munge:/sbin/nologin\n",
	})

	if err := m.ReconcileFromNode(ctx, hub, "node-controller"); err != nil {
		t.Fatalf("ReconcileFromNode: %v", err)
	}

	accounts, err := d.SysAccountsListAccounts(ctx)
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if accounts[0].UID != 996 {
		t.Errorf("expected UID 996 after reconcile, got %d", accounts[0].UID)
	}
}

// TestReconcileFromNode_SkipsCorrectUID verifies that accounts whose UID is
// already in the system range (< 1000) are not touched.
func TestReconcileFromNode_SkipsCorrectUID(t *testing.T) {
	d := openTestDB(t)
	m := sysaccounts.New(d, nil)
	ctx := context.Background()

	if err := d.SysAccountsCreateAccount(ctx, api.SystemAccount{
		ID:         "acct-slurm",
		Username:   "slurm",
		UID:        995,
		PrimaryGID: 995,
		Shell:      "/sbin/nologin",
		HomeDir:    "/var/lib/slurm",
	}); err != nil {
		t.Fatalf("insert slurm: %v", err)
	}

	hub := newFakeHub(true)
	// No scripted result needed — should never call getent for this account.

	if err := m.ReconcileFromNode(ctx, hub, "node-controller"); err != nil {
		t.Fatalf("ReconcileFromNode: %v", err)
	}

	accounts, _ := d.SysAccountsListAccounts(ctx)
	if accounts[0].UID != 995 {
		t.Errorf("UID should not have changed, got %d", accounts[0].UID)
	}
}

// TestReconcileFromNode_NodeOffline verifies that when the controller is not
// connected the function returns nil (non-blocking) and logs a warning.
func TestReconcileFromNode_NodeOffline(t *testing.T) {
	d := openTestDB(t)
	m := sysaccounts.New(d, nil)
	ctx := context.Background()

	hub := newFakeHub(false) // offline
	if err := m.ReconcileFromNode(ctx, hub, "node-controller"); err != nil {
		t.Fatalf("expected nil error when node is offline, got: %v", err)
	}
}

// TestReconcileFromNode_GetentFails verifies that a getent failure for one
// account is skipped without aborting reconciliation of remaining accounts.
func TestReconcileFromNode_GetentFails(t *testing.T) {
	d := openTestDB(t)
	m := sysaccounts.New(d, nil)
	ctx := context.Background()

	// Insert two bad UIDs.
	for _, row := range []struct {
		id       string
		username string
		uid      int
	}{
		{"acct-munge", "munge", 10003},
		{"acct-slurm-bad", "slurmx", 10004},
	} {
		if err := d.SysAccountsCreateAccount(ctx, api.SystemAccount{
			ID:         row.id,
			Username:   row.username,
			UID:        row.uid,
			PrimaryGID: 996,
			Shell:      "/sbin/nologin",
			HomeDir:    "/dev/null",
		}); err != nil {
			t.Fatalf("insert %s: %v", row.username, err)
		}
	}

	hub := newFakeHub(true)
	// munge: success. slurmx: not found (no scripted result → exit 2).
	hub.setResult("munge", clientd.ExecResultPayload{
		ExitCode: 0,
		Stdout:   "munge:x:996:996::/var/run/munge:/sbin/nologin\n",
	})

	if err := m.ReconcileFromNode(ctx, hub, "node-controller"); err != nil {
		t.Fatalf("ReconcileFromNode: %v", err)
	}

	accounts, _ := d.SysAccountsListAccounts(ctx)
	byName := make(map[string]int, len(accounts))
	for _, a := range accounts {
		byName[a.Username] = a.UID
	}

	if byName["munge"] != 996 {
		t.Errorf("munge UID: want 996, got %d", byName["munge"])
	}
	// slurmx: getent failed → UID should be unchanged at 10004.
	if byName["slurmx"] != 10004 {
		t.Errorf("slurmx UID: want 10004 (unchanged), got %d", byName["slurmx"])
	}
}
