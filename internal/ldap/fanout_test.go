// fanout_test.go — regression test for #109/#110: FanoutLDAPConfig pushes the
// updated CA cert and per-node sssd.conf to enrolled nodes after a CA rotation.
//
// Uses mock implementations of LDAPHubIface and a real in-memory SQLite DB.
// No live slapd or systemd required.
package ldap

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// mockHub simulates a ClientdHub for testing FanoutLDAPConfig.
// It records every config_push message sent to each node and immediately acks.
type mockHub struct {
	mu       sync.Mutex
	messages map[string][]clientd.ConfigPushPayload // nodeID → ordered list of pushes
}

func newMockHub() *mockHub {
	return &mockHub{messages: make(map[string][]clientd.ConfigPushPayload)}
}

func (h *mockHub) IsConnected(nodeID string) bool {
	return true // all nodes "connected" in tests
}

func (h *mockHub) Send(nodeID string, msg clientd.ServerMessage) error {
	if msg.Type != "config_push" {
		return nil
	}
	var p clientd.ConfigPushPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return err
	}
	h.mu.Lock()
	h.messages[nodeID] = append(h.messages[nodeID], p)
	h.mu.Unlock()
	return nil
}

func (h *mockHub) RegisterAck(msgID string) <-chan clientd.AckPayload {
	ch := make(chan clientd.AckPayload, 1)
	// Immediately ack with OK=true.
	ch <- clientd.AckPayload{RefMsgID: msgID, OK: true}
	return ch
}

func (h *mockHub) UnregisterAck(_ string) {}

// receivedTargets returns, in order, the Target fields of all pushes sent to nodeID.
func (h *mockHub) receivedTargets(nodeID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	msgs := h.messages[nodeID]
	targets := make([]string, len(msgs))
	for i, m := range msgs {
		targets[i] = m.Target
	}
	return targets
}

// seedLDAPReady inserts a ready ldap_module_config row and two ldap_node_state rows.
func seedLDAPReady(t *testing.T, d *db.DB, node1, node2 string) {
	t.Helper()
	// LDAPSaveConfig requires a strong CLUSTR_SECRET_KEY to encrypt credentials.
	t.Setenv("CLUSTR_SECRET_KEY", "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	ctx := context.Background()

	if err := d.LDAPSaveConfig(ctx, db.LDAPModuleConfig{
		Enabled:             true,
		Status:              "ready",
		BaseDN:              "dc=cluster,dc=local",
		CACertPEM:           "-----BEGIN CERTIFICATE-----\nfake-ca-pem\n-----END CERTIFICATE-----\n",
		ServiceBindDN:       "cn=node-reader,ou=services,dc=cluster,dc=local",
		ServiceBindPassword: "s3cr3t",
		AdminPasswd:         "adminpass",
		LastProvisionedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("seedLDAPReady: LDAPSaveConfig: %v", err)
	}

	for _, nodeID := range []string{node1, node2} {
		// Create the node_configs row first: ldap_node_state.node_id has a FK to
		// node_configs(id) ON DELETE CASCADE, so the parent row must exist before
		// LDAPRecordNodeConfigured can insert into ldap_node_state.
		hostname := "node-" + nodeID[:8]
		if err := d.CreateNodeConfig(ctx, api.NodeConfig{
			ID:         nodeID,
			Hostname:   hostname,
			PrimaryMAC: "de:ad:be:ef:00:" + nodeID[:2],
		}); err != nil {
			t.Fatalf("seedLDAPReady: CreateNodeConfig %s: %v", nodeID, err)
		}
		if err := d.LDAPRecordNodeConfigured(ctx, nodeID, "hash-"+nodeID); err != nil {
			t.Fatalf("seedLDAPReady: LDAPRecordNodeConfigured %s: %v", nodeID, err)
		}
	}
}

// TestFanoutLDAPConfig_PushesCAAndSSSDToEnrolledNodes is the anti-regression test
// for #109/#110: after Disable+wipe+Enable (CA rotation), FanoutLDAPConfig must
// send "ldap-ca-cert" and "sssd" pushes (in that order) to every enrolled node.
func TestFanoutLDAPConfig_PushesCAAndSSSDToEnrolledNodes(t *testing.T) {
	database := openTestDB(t)
	node1 := uuid.New().String()
	node2 := uuid.New().String()
	seedLDAPReady(t, database, node1, node2)

	hub := newMockHub()
	mgr := New(config.ServerConfig{}, database)
	mgr.SetHub(hub)

	results := mgr.FanoutLDAPConfig(context.Background())

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}

	resultByNode := make(map[string]NodePushResult)
	for _, r := range results {
		resultByNode[r.NodeID] = r
	}

	for _, nodeID := range []string{node1, node2} {
		r, ok := resultByNode[nodeID]
		if !ok {
			t.Errorf("no result for node %s", nodeID)
			continue
		}
		if !r.OK {
			t.Errorf("node %s: expected OK=true, got error: %s", nodeID, r.Error)
		}

		targets := hub.receivedTargets(nodeID)
		if len(targets) != 2 {
			t.Errorf("node %s: expected 2 pushes (ldap-ca-cert, sssd), got %d: %v", nodeID, len(targets), targets)
			continue
		}
		if targets[0] != "ldap-ca-cert" {
			t.Errorf("node %s: first push must be ldap-ca-cert, got %q", nodeID, targets[0])
		}
		if targets[1] != "sssd" {
			t.Errorf("node %s: second push must be sssd, got %q", nodeID, targets[1])
		}

		// Verify the CA cert push carries the right content.
		caPush := hub.messages[nodeID][0]
		if caPush.Content == "" {
			t.Errorf("node %s: ca-cert push has empty content", nodeID)
		}
		if caPush.Checksum == "" || len(caPush.Checksum) < 8 {
			t.Errorf("node %s: ca-cert push has no/short checksum: %q", nodeID, caPush.Checksum)
		}

		// Verify the sssd push contains ldap_uri pointing to an IP (not hostname) — #111.
		sssdPush := hub.messages[nodeID][1]
		if sssdPush.Content == "" {
			t.Errorf("node %s: sssd push has empty content", nodeID)
		}
	}
}

// TestFanoutLDAPConfig_SkipsOfflineNodes verifies that offline nodes are recorded
// as Offline=true and do not block the push to online nodes.
func TestFanoutLDAPConfig_SkipsOfflineNodes(t *testing.T) {
	database := openTestDB(t)
	onlineNode := uuid.New().String()
	offlineNode := uuid.New().String()
	seedLDAPReady(t, database, onlineNode, offlineNode)

	// Hub where offlineNode is not connected.
	hub := &mockHubSelective{
		mockHub:     newMockHub(),
		onlineNodes: map[string]bool{onlineNode: true},
	}
	mgr := New(config.ServerConfig{}, database)
	mgr.SetHub(hub)

	results := mgr.FanoutLDAPConfig(context.Background())

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	byNode := make(map[string]NodePushResult)
	for _, r := range results {
		byNode[r.NodeID] = r
	}

	if !byNode[onlineNode].OK {
		t.Errorf("online node should have OK=true, got: %+v", byNode[onlineNode])
	}
	if !byNode[offlineNode].Offline {
		t.Errorf("offline node should have Offline=true, got: %+v", byNode[offlineNode])
	}
}

// mockHubSelective extends mockHub so specific nodes appear offline.
type mockHubSelective struct {
	*mockHub
	onlineNodes map[string]bool
}

func (h *mockHubSelective) IsConnected(nodeID string) bool {
	return h.onlineNodes[nodeID]
}

func (h *mockHubSelective) Send(nodeID string, msg clientd.ServerMessage) error {
	return h.mockHub.Send(nodeID, msg)
}

func (h *mockHubSelective) RegisterAck(msgID string) <-chan clientd.AckPayload {
	return h.mockHub.RegisterAck(msgID)
}

func (h *mockHubSelective) UnregisterAck(msgID string) {}

// TestFanoutLDAPConfig_NoEnrolledNodes verifies a graceful no-op when no nodes
// are enrolled (LDAP just freshly enabled, no deploys yet).
func TestFanoutLDAPConfig_NoEnrolledNodes(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	database := openTestDB(t)
	// Seed ready state but no node rows.
	if err := database.LDAPSaveConfig(context.Background(), db.LDAPModuleConfig{
		Enabled:             true,
		Status:              "ready",
		BaseDN:              "dc=cluster,dc=local",
		CACertPEM:           "fake-pem",
		ServiceBindPassword: "s3cr3t",
		AdminPasswd:         "adminpass",
		LastProvisionedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("LDAPSaveConfig: %v", err)
	}

	hub := newMockHub()
	mgr := New(config.ServerConfig{}, database)
	mgr.SetHub(hub)

	results := mgr.FanoutLDAPConfig(context.Background())
	if results != nil {
		t.Errorf("expected nil results for empty enrolled set, got %+v", results)
	}
}
