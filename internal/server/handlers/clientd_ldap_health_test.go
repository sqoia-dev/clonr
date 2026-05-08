package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakeLDAPDB is a minimal stub of ClientdDBIface for the applyLDAPHealth
// unit tests. Only LDAPNodeIsConfigured and RecordNodeLDAPReady are exercised;
// the rest of the iface returns zero values so the type satisfies the
// interface but never fires from this test.
type fakeLDAPDB struct {
	configured     bool
	configuredErr  error
	recordedReady  *bool
	recordedDetail string
	recordCount    int
	recordErr      error
}

func (f *fakeLDAPDB) UpsertHeartbeat(context.Context, string, *db.HeartbeatRow) error { return nil }
func (f *fakeLDAPDB) GetHeartbeat(context.Context, string) (*db.HeartbeatRow, error) {
	return nil, nil
}
func (f *fakeLDAPDB) UpdateLastSeen(context.Context, string) error         { return nil }
func (f *fakeLDAPDB) InsertLogBatch(context.Context, []api.LogEntry) error { return nil }
func (f *fakeLDAPDB) GetNodeConfig(context.Context, string) (api.NodeConfig, error) {
	return api.NodeConfig{}, nil
}
func (f *fakeLDAPDB) InsertStatsBatch(context.Context, []db.NodeStatRow) error { return nil }

func (f *fakeLDAPDB) LDAPNodeIsConfigured(_ context.Context, _ string) (bool, error) {
	return f.configured, f.configuredErr
}

func (f *fakeLDAPDB) RecordNodeLDAPReady(_ context.Context, _ string, ready bool, detail string) error {
	f.recordCount++
	f.recordedReady = &ready
	f.recordedDetail = detail
	return f.recordErr
}

// TestApplyLDAPHealth_NodeConfigured covers the happy path where the node was
// deployed with LDAP and the probe says sssd is online — we expect ready=true
// to be written through.
func TestApplyLDAPHealth_NodeConfigured(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Configured: true,
		Active:     true,
		Connected:  true,
		Domain:     "cluster.local",
		Detail:     "sssd online (cluster.local)",
	})
	if d.recordCount != 1 {
		t.Fatalf("expected 1 RecordNodeLDAPReady call, got %d", d.recordCount)
	}
	if d.recordedReady == nil || !*d.recordedReady {
		t.Fatalf("expected ready=true, got %v", d.recordedReady)
	}
	if d.recordedDetail != "sssd online (cluster.local)" {
		t.Fatalf("unexpected detail: %q", d.recordedDetail)
	}
}

// TestApplyLDAPHealth_NodeConfiguredButOffline covers the case where the node
// is LDAP-configured but the probe shows sssd is broken — we expect
// ready=false to be written through with the probe's Detail.
func TestApplyLDAPHealth_NodeConfiguredButOffline(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: false,
		Detail:    "sssd active but domain offline: …",
	})
	if d.recordCount != 1 {
		t.Fatalf("expected 1 RecordNodeLDAPReady call, got %d", d.recordCount)
	}
	if d.recordedReady == nil || *d.recordedReady {
		t.Fatalf("expected ready=false, got %v", d.recordedReady)
	}
}

// TestApplyLDAPHealth_NodeNotConfigured covers a node deployed without LDAP —
// we MUST NOT write to node_configs.ldap_ready (the column stays NULL so
// pkg/api.NodeConfig.State() does not flag the node as "LDAP failed").
func TestApplyLDAPHealth_NodeNotConfigured(t *testing.T) {
	d := &fakeLDAPDB{configured: false}
	applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: true,
	})
	if d.recordCount != 0 {
		t.Fatalf("expected 0 RecordNodeLDAPReady calls for non-LDAP node, got %d", d.recordCount)
	}
}

// TestApplyLDAPHealth_NilHealth — defensive: nil snapshot is a no-op.
func TestApplyLDAPHealth_NilHealth(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applyLDAPHealth(context.Background(), d, "node-1", nil)
	if d.recordCount != 0 {
		t.Fatalf("expected 0 RecordNodeLDAPReady calls for nil snapshot, got %d", d.recordCount)
	}
}

// TestApplyLDAPHealth_LookupError — when LDAPNodeIsConfigured fails we must
// not write a misleading row; we log and skip.
func TestApplyLDAPHealth_LookupError(t *testing.T) {
	d := &fakeLDAPDB{configuredErr: errors.New("db down")}
	applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: true,
	})
	if d.recordCount != 0 {
		t.Fatalf("expected no record when LDAPNodeIsConfigured errors, got %d", d.recordCount)
	}
}

// TestApplyLDAPHealth_DefaultDetails — when the probe omits Detail (defensive
// path) we synthesize a sane string so the UI never shows an empty cell.
func TestApplyLDAPHealth_DefaultDetails(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: true,
		// Detail intentionally empty
	})
	if d.recordedDetail == "" {
		t.Fatalf("expected non-empty default detail")
	}
}
