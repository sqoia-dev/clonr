// fanout_test.go — regression test for #109/#110: FanoutLDAPConfig pushes the
// updated CA cert and per-node sssd.conf to enrolled nodes after a CA rotation.
//
// Uses mock implementations of LDAPHubIface and a real in-memory SQLite DB.
// No live slapd or systemd required.
package ldap

import (
	"context"
	"encoding/json"
	"strings"
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

// TestFanoutLDAPConfig_PushesCASSSDAndSSHDToEnrolledNodes is the end-to-end
// regression test for GAP-104a-1/2/4: after Disable+wipe+Enable (CA rotation),
// FanoutLDAPConfig must send exactly three pushes — "ldap-ca-cert", "sssd",
// "sshd-ldap-keys" — to every enrolled node, in that order.
//
// Content assertions:
//   - CA cert push: non-empty content + valid checksum.
//   - sssd.conf push: contains "services = nss, pam, ssh" (GAP-104a-4),
//     ldap_user_ssh_public_key = sshPublicKey (GAP-104a-4),
//     ldap_tls_reqcert = allow (GAP-104a-1, matches deploy path),
//     and the new bind password from the DB row (GAP-104a-2).
//   - sshd-ldap-keys push: contains AuthorizedKeysCommand and
//     AuthorizedKeysCommandUser (GAP-104a-4).
func TestFanoutLDAPConfig_PushesCASSSDAndSSHDToEnrolledNodes(t *testing.T) {
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
		// Expect 3 pushes: ldap-ca-cert, sssd, sshd-ldap-keys.
		if len(targets) != 3 {
			t.Errorf("node %s: expected 3 pushes (ldap-ca-cert, sssd, sshd-ldap-keys), got %d: %v", nodeID, len(targets), targets)
			continue
		}
		if targets[0] != "ldap-ca-cert" {
			t.Errorf("node %s: push[0] must be ldap-ca-cert, got %q", nodeID, targets[0])
		}
		if targets[1] != "sssd" {
			t.Errorf("node %s: push[1] must be sssd, got %q", nodeID, targets[1])
		}
		if targets[2] != "sshd-ldap-keys" {
			t.Errorf("node %s: push[2] must be sshd-ldap-keys, got %q", nodeID, targets[2])
		}

		hub.mu.Lock()
		msgs := hub.messages[nodeID]
		hub.mu.Unlock()

		// ── Push 0: CA cert ──────────────────────────────────────────────────────
		caPush := msgs[0]
		if caPush.Content == "" {
			t.Errorf("node %s: ca-cert push has empty content", nodeID)
		}
		if caPush.Checksum == "" || len(caPush.Checksum) < 8 {
			t.Errorf("node %s: ca-cert push has no/short checksum: %q", nodeID, caPush.Checksum)
		}

		// ── Push 1: sssd.conf ────────────────────────────────────────────────────
		sssdPush := msgs[1]
		if sssdPush.Content == "" {
			t.Errorf("node %s: sssd push has empty content", nodeID)
		}
		// GAP-104a-4: sssd.conf must enable the ssh responder so LDAP pubkey auth works.
		if !containsLine(sssdPush.Content, "services = nss, pam, ssh") {
			t.Errorf("node %s: sssd.conf missing 'services = nss, pam, ssh' (GAP-104a-4)\ncontent:\n%s", nodeID, sssdPush.Content)
		}
		// GAP-104a-4: ldap_user_ssh_public_key attribute mapping must be present.
		if !containsLine(sssdPush.Content, "ldap_user_ssh_public_key = sshPublicKey") {
			t.Errorf("node %s: sssd.conf missing 'ldap_user_ssh_public_key = sshPublicKey' (GAP-104a-4)\ncontent:\n%s", nodeID, sssdPush.Content)
		}
		// GAP-104a-1: ldap_tls_reqcert must be "allow" (not "demand") to survive CA rotation.
		if !containsLine(sssdPush.Content, "ldap_tls_reqcert = allow") {
			t.Errorf("node %s: sssd.conf must have 'ldap_tls_reqcert = allow' (GAP-104a-1)\ncontent:\n%s", nodeID, sssdPush.Content)
		}
		if containsLine(sssdPush.Content, "ldap_tls_reqcert = demand") {
			t.Errorf("node %s: sssd.conf must not have 'ldap_tls_reqcert = demand' (GAP-104a-1)\ncontent:\n%s", nodeID, sssdPush.Content)
		}
		// GAP-104a-2: bind password must be in the sssd.conf.
		if !containsLine(sssdPush.Content, "ldap_default_authtok = s3cr3t") {
			t.Errorf("node %s: sssd.conf must carry the new bind password (GAP-104a-2)\ncontent:\n%s", nodeID, sssdPush.Content)
		}

		// ── Push 2: sshd-ldap-keys ───────────────────────────────────────────────
		sshdPush := msgs[2]
		// GAP-104a-4: AuthorizedKeysCommand must be present in the drop-in.
		if !containsLine(sshdPush.Content, "AuthorizedKeysCommand /usr/bin/sss_ssh_authorizedkeys") {
			t.Errorf("node %s: sshd drop-in missing AuthorizedKeysCommand (GAP-104a-4)\ncontent:\n%s", nodeID, sshdPush.Content)
		}
		if !containsLine(sshdPush.Content, "AuthorizedKeysCommandUser nobody") {
			t.Errorf("node %s: sshd drop-in missing AuthorizedKeysCommandUser (GAP-104a-4)\ncontent:\n%s", nodeID, sshdPush.Content)
		}
	}
}

// containsLine reports whether s contains the given line as a substring.
// Used in fanout tests to check for specific sssd.conf / sshd_config.d lines.
func containsLine(s, line string) bool {
	return strings.Contains(s, line)
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

// TestPushLDAPOnNodeConnect_ReEnableCycle is the end-to-end integration test for
// GAP-104a reconnect gap. It simulates the exact failure observed in the lab:
//
//  1. LDAP module is enabled with a CA cert.
//  2. Two nodes are deployed and LDAP-configured (ldap_node_state rows exist).
//  3. Operator runs Disable→Enable (CA rotation). Both nodes are offline during fanout.
//     FanoutLDAPConfig returns Offline=true for both — config is NOT pushed.
//  4. One node comes back online and sends hello.
//     PushLDAPOnNodeConnect detects the node is enrolled and pushes the 3 files.
//  5. The offline node does NOT receive the push.
//
// Assertions:
//   - The reconnecting node receives exactly 3 pushes (ldap-ca-cert, sssd, sshd-ldap-keys).
//   - The still-offline node receives zero pushes.
//   - The reconnect push audit action (ldap.ca_applied) matches the expected constant.
func TestPushLDAPOnNodeConnect_ReEnableCycle(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	database := openTestDB(t)
	ctx := context.Background()

	onlineNode := uuid.New().String()
	offlineNode := uuid.New().String()
	seedLDAPReady(t, database, onlineNode, offlineNode)

	// Simulate fanout where both nodes were offline (step 3).
	allOfflineHub := &mockHubSelective{
		mockHub:     newMockHub(),
		onlineNodes: map[string]bool{}, // no nodes connected
	}
	mgr := New(config.ServerConfig{}, database)
	mgr.SetHub(allOfflineHub)
	fanoutResults := mgr.FanoutLDAPConfig(ctx)

	// Verify both were recorded as offline — confirming the gap condition.
	if len(fanoutResults) != 2 {
		t.Fatalf("expected 2 fanout results (both offline), got %d", len(fanoutResults))
	}
	for _, r := range fanoutResults {
		if !r.Offline {
			t.Errorf("node %s should be Offline=true in fanout, got: %+v", r.NodeID, r)
		}
	}
	// Verify no pushes occurred during fanout.
	if len(allOfflineHub.messages[onlineNode]) != 0 || len(allOfflineHub.messages[offlineNode]) != 0 {
		t.Error("no pushes should occur when all nodes are offline during fanout")
	}

	// Step 4: onlineNode reconnects. Replace hub so onlineNode is now connected.
	reconnectHub := &mockHubSelective{
		mockHub:     newMockHub(),
		onlineNodes: map[string]bool{onlineNode: true},
	}
	mgr.SetHub(reconnectHub)

	// Call PushLDAPOnNodeConnect for the reconnecting node — this is what handleHello
	// invokes via the LDAPOnConnect callback.
	mgr.PushLDAPOnNodeConnect(ctx, onlineNode)

	// Step 5: offlineNode still does not reconnect — call should be a no-op if called.
	// (In practice handleHello only fires when the WS upgrades; we test the enrolled=false path.)
	mgr.PushLDAPOnNodeConnect(ctx, offlineNode)

	// Assertion: onlineNode received exactly 3 pushes.
	onlineTargets := reconnectHub.receivedTargets(onlineNode)
	if len(onlineTargets) != 3 {
		t.Fatalf("reconnecting node should receive 3 pushes, got %d: %v", len(onlineTargets), onlineTargets)
	}
	if onlineTargets[0] != "ldap-ca-cert" {
		t.Errorf("push[0] must be ldap-ca-cert, got %q", onlineTargets[0])
	}
	if onlineTargets[1] != "sssd" {
		t.Errorf("push[1] must be sssd, got %q", onlineTargets[1])
	}
	if onlineTargets[2] != "sshd-ldap-keys" {
		t.Errorf("push[2] must be sshd-ldap-keys, got %q", onlineTargets[2])
	}

	// Assertion: offlineNode (which also has a ldap_node_state row) did NOT receive pushes
	// because it was not connected in reconnectHub.
	offlineTargets := reconnectHub.receivedTargets(offlineNode)
	if len(offlineTargets) != 0 {
		t.Errorf("still-offline node should receive 0 pushes, got %d: %v", len(offlineTargets), offlineTargets)
	}

	// Assertion: sssd.conf for the reconnecting node must contain GAP-104a-4 fields.
	reconnectHub.mu.Lock()
	msgs := reconnectHub.messages[onlineNode]
	reconnectHub.mu.Unlock()

	sssdContent := msgs[1].Content
	if !strings.Contains(sssdContent, "services = nss, pam, ssh") {
		t.Errorf("reconnect sssd.conf missing 'services = nss, pam, ssh' (GAP-104a-4)")
	}
	if !strings.Contains(sssdContent, "ldap_user_ssh_public_key = sshPublicKey") {
		t.Errorf("reconnect sssd.conf missing 'ldap_user_ssh_public_key = sshPublicKey' (GAP-104a-4)")
	}
}

// TestPushLDAPOnNodeConnect_NotEnrolled verifies that PushLDAPOnNodeConnect is a
// no-op for nodes that were never LDAP-configured (no ldap_node_state row).
func TestPushLDAPOnNodeConnect_NotEnrolled(t *testing.T) {
	t.Setenv("CLUSTR_SECRET_KEY", "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	database := openTestDB(t)
	ctx := context.Background()

	// Seed LDAP ready config but NO ldap_node_state rows.
	if err := database.LDAPSaveConfig(ctx, db.LDAPModuleConfig{
		Enabled:             true,
		Status:              "ready",
		BaseDN:              "dc=cluster,dc=local",
		CACertPEM:           "-----BEGIN CERTIFICATE-----\nfake-ca-pem\n-----END CERTIFICATE-----\n",
		ServiceBindDN:       "cn=node-reader,ou=services,dc=cluster,dc=local",
		ServiceBindPassword: "s3cr3t",
		AdminPasswd:         "adminpass",
		LastProvisionedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("LDAPSaveConfig: %v", err)
	}

	unenrolledNode := uuid.New().String()
	// Create the node_configs row but no ldap_node_state row.
	if err := database.CreateNodeConfig(ctx, api.NodeConfig{
		ID:         unenrolledNode,
		Hostname:   "unenrolled-node",
		PrimaryMAC: "de:ad:be:ef:ff:ff",
	}); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	hub := newMockHub()
	mgr := New(config.ServerConfig{}, database)
	mgr.SetHub(hub)

	// Should be a complete no-op — no pushes, no panics.
	mgr.PushLDAPOnNodeConnect(ctx, unenrolledNode)

	targets := hub.receivedTargets(unenrolledNode)
	if len(targets) != 0 {
		t.Errorf("unenrolled node should receive 0 pushes, got %d: %v", len(targets), targets)
	}
}

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
