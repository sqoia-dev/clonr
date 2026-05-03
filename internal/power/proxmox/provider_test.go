// Package proxmox_test exercises the Proxmox power provider with an httptest
// server standing in for the real Proxmox API. Tests focus on the stop+start
// sequencing that SetNextBoot and SetPersistentBootOrder must perform when the
// VM is running so that pending config changes are committed.
//
// See docs/boot-architecture.md §10.7 for the root-cause explanation of WHY
// the stop+start dance is required (Proxmox commits VM config only on
// stop+start, not on /status/reset).
package proxmox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/power"
	"github.com/sqoia-dev/clustr/internal/power/proxmox"
)

// ─── Mock server helpers ──────────────────────────────────────────────────────

// mockPVE records every API call made against it so tests can assert ordering.
type mockPVE struct {
	mu      sync.Mutex
	calls   []string // e.g. "GET /nodes/pve/qemu/202/status/current"
	vmState string   // "running" or "stopped"
}

func (m *mockPVE) recordCall(method, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, method+" "+path)
}

func (m *mockPVE) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.calls))
	copy(result, m.calls)
	return result
}

func (m *mockPVE) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// Strip the /api2/json prefix for recording.
	const prefix = "/api2/json"
	shortPath := path
	if len(path) > len(prefix) {
		shortPath = path[len(prefix):]
	}
	m.recordCall(r.Method, shortPath)

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && shortPath == "/nodes/pve/qemu/202/status/current":
		m.mu.Lock()
		state := m.vmState
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]string{"status": state},
		})

	case r.Method == http.MethodPost && shortPath == "/nodes/pve/qemu/202/status/stop":
		m.mu.Lock()
		m.vmState = "stopped"
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"data": "UPID:pve:stop"})

	case r.Method == http.MethodPost && shortPath == "/nodes/pve/qemu/202/status/start":
		m.mu.Lock()
		m.vmState = "running"
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]interface{}{"data": "UPID:pve:start"})

	case r.Method == http.MethodPut && shortPath == "/nodes/pve/qemu/202/config":
		json.NewEncoder(w).Encode(map[string]interface{}{"data": nil})

	default:
		http.NotFound(w, r)
	}
}

func newMockProvider(t *testing.T, initialState string) (*mockPVE, power.Provider) {
	t.Helper()
	mock := &mockPVE{vmState: initialState}
	ts := httptest.NewServer(mock)
	t.Cleanup(ts.Close)

	prov, err := proxmox.New(power.ProviderConfig{
		Type: "proxmox",
		Fields: map[string]string{
			"api_url":      ts.URL,
			"node":         "pve",
			"vmid":         "202",
			"token_id":     "root@pam!test",
			"token_secret": "test-secret",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return mock, prov
}

// containsInOrder checks that want entries appear in got in the given order
// (not necessarily consecutively).
func containsInOrder(t *testing.T, got []string, want []string) {
	t.Helper()
	idx := 0
	for _, call := range got {
		if idx < len(want) && call == want[idx] {
			idx++
		}
	}
	if idx < len(want) {
		t.Errorf("call sequence missing or out of order:\n  want (in order): %v\n  got: %v", want, got)
	}
}

// ─── SetNextBoot tests ────────────────────────────────────────────────────────

// TestSetNextBoot_RunningVM_StopsPutsStarts asserts the critical invariant:
// when the VM is running, SetNextBoot must stop → PUT config → start so the
// new boot order is committed before the call returns.
// See docs/boot-architecture.md §10.7.
func TestSetNextBoot_RunningVM_StopsPutsStarts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mock, prov := newMockProvider(t, "running")

	if err := prov.SetNextBoot(ctx, power.BootPXE); err != nil {
		t.Fatalf("SetNextBoot: %v", err)
	}

	calls := mock.Calls()
	// Must contain: GET status (pre-check), PUT config, POST stop, POST start — in that order.
	containsInOrder(t, calls, []string{
		"GET /nodes/pve/qemu/202/status/current",
		"PUT /nodes/pve/qemu/202/config",
		"POST /nodes/pve/qemu/202/status/stop",
		"POST /nodes/pve/qemu/202/status/start",
	})
}

// TestSetNextBoot_StoppedVM_PutsConfigOnly asserts that when the VM is already
// stopped, SetNextBoot writes the config but does NOT issue stop or start
// (the orchestrator's subsequent PowerOn/PowerCycle will boot the new order).
func TestSetNextBoot_StoppedVM_PutsConfigOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mock, prov := newMockProvider(t, "stopped")

	if err := prov.SetNextBoot(ctx, power.BootPXE); err != nil {
		t.Fatalf("SetNextBoot: %v", err)
	}

	calls := mock.Calls()
	// Must have the GET status check and PUT config.
	containsInOrder(t, calls, []string{
		"GET /nodes/pve/qemu/202/status/current",
		"PUT /nodes/pve/qemu/202/config",
	})
	// Must NOT have stop or start.
	for _, c := range calls {
		if c == "POST /nodes/pve/qemu/202/status/stop" {
			t.Errorf("SetNextBoot on stopped VM must not issue stop; got calls: %v", calls)
		}
		if c == "POST /nodes/pve/qemu/202/status/start" {
			t.Errorf("SetNextBoot on stopped VM must not issue start; got calls: %v", calls)
		}
	}
}

// TestSetNextBoot_BootPXE_SetsNetFirst asserts the Proxmox config PUT sets
// boot=order=net0;scsi0 when BootPXE is requested.
func TestSetNextBoot_BootPXE_SetsNetFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var capturedBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api2/json/nodes/pve/qemu/202/config" {
			_ = r.ParseForm()
			capturedBody = r.FormValue("boot")
		}
		w.Header().Set("Content-Type", "application/json")
		// Return "stopped" for status, nil for everything else.
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]string{"status": "stopped"},
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{"data": nil})
		}
	}))
	defer ts.Close()

	prov, err := proxmox.New(power.ProviderConfig{
		Type: "proxmox",
		Fields: map[string]string{
			"api_url": ts.URL, "node": "pve", "vmid": "202",
			"token_id": "root@pam!t", "token_secret": "s",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := prov.SetNextBoot(ctx, power.BootPXE); err != nil {
		t.Fatalf("SetNextBoot: %v", err)
	}
	if capturedBody != "order=net0;scsi0" {
		t.Errorf("SetNextBoot(BootPXE) PUT boot = %q; want %q", capturedBody, "order=net0;scsi0")
	}
}

// ─── SetPersistentBootOrder tests ─────────────────────────────────────────────

// TestSetPersistentBootOrder_RunningVM_StopsAndStarts asserts the post-deploy
// flip-back path: SetPersistentBootOrder([BootDisk, BootPXE]) on a running VM
// must issue PUT config (order=scsi0;net0), POST stop, POST start.
// This is the belt-and-suspenders flip-back called by the verify-boot handler
// and the deploy-timeout scanner. See docs/boot-architecture.md §10.
func TestSetPersistentBootOrder_RunningVM_StopsAndStarts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mock, prov := newMockProvider(t, "running")

	if err := prov.SetPersistentBootOrder(ctx, []power.BootDevice{power.BootDisk, power.BootPXE}); err != nil {
		t.Fatalf("SetPersistentBootOrder: %v", err)
	}

	calls := mock.Calls()
	containsInOrder(t, calls, []string{
		"GET /nodes/pve/qemu/202/status/current",
		"PUT /nodes/pve/qemu/202/config",
		"POST /nodes/pve/qemu/202/status/stop",
		"POST /nodes/pve/qemu/202/status/start",
	})
}

// TestSetPersistentBootOrder_DiskFirst_SetsScsiFirst asserts the Proxmox config
// PUT sets boot=order=scsi0;net0 when BootDisk is first in the order list.
func TestSetPersistentBootOrder_DiskFirst_SetsScsiFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var capturedBoot string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_ = r.ParseForm()
			capturedBoot = r.FormValue("boot")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]string{"status": "stopped"},
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{"data": nil})
		}
	}))
	defer ts.Close()

	prov, err := proxmox.New(power.ProviderConfig{
		Type: "proxmox",
		Fields: map[string]string{
			"api_url": ts.URL, "node": "pve", "vmid": "202",
			"token_id": "root@pam!t", "token_secret": "s",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := prov.SetPersistentBootOrder(ctx, []power.BootDevice{power.BootDisk, power.BootPXE}); err != nil {
		t.Fatalf("SetPersistentBootOrder: %v", err)
	}
	if capturedBoot != "order=scsi0;net0" {
		t.Errorf("SetPersistentBootOrder([Disk,PXE]) PUT boot = %q; want %q", capturedBoot, "order=scsi0;net0")
	}
}

// TestSetNextBoot_StopFails_ReturnsError asserts that if the stop call fails,
// SetNextBoot returns an error (does not silently proceed).
func TestSetNextBoot_StopFails_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]string{"status": "running"},
			})
		} else if r.Method == http.MethodPut {
			json.NewEncoder(w).Encode(map[string]interface{}{"data": nil})
		} else if r.Method == http.MethodPost {
			// Fail all POSTs (stop/start).
			http.Error(w, `{"errors":{"stop":"not allowed"}}`, http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	prov, err := proxmox.New(power.ProviderConfig{
		Type: "proxmox",
		Fields: map[string]string{
			"api_url": ts.URL, "node": "pve", "vmid": "202",
			"token_id": "root@pam!t", "token_secret": "s",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := prov.SetNextBoot(ctx, power.BootPXE); err == nil {
		t.Error("SetNextBoot should return error when stop fails; got nil")
	}
}
