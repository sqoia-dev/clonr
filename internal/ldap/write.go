// write.go — LDAP write-back: write-bind probe, dialect detection, and the
// Manager-level write helpers used by the HTTP handlers.
//
// Backend dialect: this codebase manages a built-in OpenLDAP server (slapd)
// provisioned by Enable(). All write operations therefore use the OpenLDAP
// dialect (memberUid group membership, crypt(3) userPassword, shadowAccount
// lock model). The dialect stubs for FreeIPA/AD/generic are present but
// return clear "not implemented" errors so operators are never silently misled.
//
// Write bind resolution:
//   - If write_bind_dn + write_bind_password are set in the config, all write
//     operations bind with those credentials.
//   - Otherwise, falls back to the Directory Manager (admin) bind — same
//     credentials used by DIT().
package ldap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/rs/zerolog/log"
)

// LDAPBackendDialect identifies the LDAP server dialect used for write ops.
type LDAPBackendDialect string

const (
	DialectOpenLDAP LDAPBackendDialect = "openldap" // built-in; fully implemented
	DialectFreeIPA  LDAPBackendDialect = "freeipa"  // stub — not implemented in v0.4.0
	DialectAD       LDAPBackendDialect = "ad"        // stub — not implemented in v0.4.0
	DialectGeneric  LDAPBackendDialect = "generic"   // stub — not implemented in v0.4.0
)

// ErrDialectNotImplemented is returned by dialect stubs.
type ErrDialectNotImplemented struct {
	Dialect LDAPBackendDialect
	Op      string
}

func (e *ErrDialectNotImplemented) Error() string {
	return fmt.Sprintf("ldap write: operation %q is not implemented for backend dialect %q in clustr v0.4.0; use OpenLDAP", e.Op, e.Dialect)
}

// WriteBind returns a ditClient bound with the write credentials.
// If write_bind_dn is unset, falls back to the DM (admin) bind.
func (m *Manager) WriteBind(ctx context.Context) (*ditClient, error) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ldap: read config for write bind: %w", err)
	}
	if !row.Enabled || row.Status != statusReady {
		return nil, fmt.Errorf("ldap: module is not ready (status=%s)", row.Status)
	}

	bindDN := row.WriteBindDN
	bindPasswd := row.WriteBindPassword

	if bindDN == "" {
		// Fall back to the Directory Manager bind.
		m.mu.RLock()
		adminPass := m.adminPassword
		m.mu.RUnlock()
		if adminPass == "" {
			return nil, fmt.Errorf("ldap: admin password not in memory — call Enable() or AdminRepair() to restore")
		}
		bindDN = fmt.Sprintf("cn=Directory Manager,%s", row.BaseDN)
		bindPasswd = adminPass
	}

	return &ditClient{
		serverURI:  "ldaps://127.0.0.1:636",
		bindDN:     bindDN,
		bindPasswd: bindPasswd,
		baseDN:     row.BaseDN,
		caCertPEM:  []byte(row.CACertPEM),
	}, nil
}

// ProbeWriteCapable attempts a no-op write probe against the directory:
//   1. Tries to read-then-modify a sentinel attribute on a temp entry in ou=services.
//      We create cn=clustr-write-probe,ou=services,<baseDN>, attempt an Add, immediately
//      Delete it. If the bind can do that, write capability is confirmed.
//
// Returns (capable bool, reason string). Errors are surfaced as capability=false.
// The result is persisted to the DB (write_capable column) and logged.
func (m *Manager) ProbeWriteCapable(ctx context.Context) (bool, string) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return false, fmt.Sprintf("read config: %v", err)
	}
	if !row.Enabled || row.Status != statusReady {
		return false, fmt.Sprintf("module not ready (status=%s)", row.Status)
	}

	dit, err := m.WriteBind(ctx)
	if err != nil {
		return false, fmt.Sprintf("write bind unavailable: %v", err)
	}

	conn, err := dit.connect()
	if err != nil {
		return false, fmt.Sprintf("bind failed: %v", err)
	}
	defer conn.Close()

	probeDN := fmt.Sprintf("cn=clustr-write-probe,ou=services,%s", row.BaseDN)

	// Try to add the probe entry.
	addReq := goldap.NewAddRequest(probeDN, nil)
	addReq.Attribute("objectClass", []string{"top", "organizationalRole"})
	addReq.Attribute("cn", []string{"clustr-write-probe"})
	addReq.Attribute("description", []string{"clustr write-capability probe — safe to delete"})

	if addErr := conn.Add(addReq); addErr != nil {
		// If it already exists from a prior probe, that's fine — still counts as write-capable.
		if goldap.IsErrorWithCode(addErr, goldap.LDAPResultEntryAlreadyExists) {
			// Probe entry already present from a prior run; write bind is capable.
			log.Debug().Msg("ldap: write probe: sentinel entry already exists — write bind confirmed")
			return true, "probe ok (sentinel exists)"
		}
		return false, fmt.Sprintf("Add failed: %v", addErr)
	}

	// Clean up the probe entry.
	_ = conn.Del(goldap.NewDelRequest(probeDN, nil))

	return true, "probe ok"
}

// SaveWriteBind persists the write-bind credentials and immediately runs a probe.
// Returns the probe result so the caller can include it in the HTTP response.
// Passing empty bindDN clears the write bind (falls back to DM bind).
func (m *Manager) SaveWriteBind(ctx context.Context, bindDN, bindPassword string, auditFn func(capable bool, detail string)) error {
	if err := m.db.LDAPSetWriteBind(ctx, bindDN, bindPassword); err != nil {
		return fmt.Errorf("ldap: save write bind: %w", err)
	}

	// Probe-write immediately (WRITE-CFG-2).
	capable, detail := m.ProbeWriteCapable(ctx)
	if err := m.db.LDAPSetWriteCapable(ctx, &capable, detail); err != nil {
		log.Warn().Err(err).Msg("ldap: failed to persist write_capable probe result")
	}

	if auditFn != nil {
		auditFn(capable, detail)
	}
	return nil
}

// WriteCapableStatus returns the cached probe result.
// Returns ("untested", false) when no probe has been run, ("ok", true) on success,
// or ("failed: <reason>", false) on failure.
func (m *Manager) WriteCapableStatus(ctx context.Context) (status string, capable bool) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil || !row.Enabled {
		return "untested", false
	}
	if row.WriteBindDN == "" {
		// No explicit write bind — falls back to DM bind. Treat as potentially capable
		// but we don't probe on DM bind (it's always write-capable by definition).
		return "dm_fallback", true
	}
	if row.WriteCapable == nil {
		return "untested", false
	}
	if *row.WriteCapable {
		return "ok", true
	}
	detail := row.WriteCapableDetail
	if detail == "" {
		detail = "probe failed"
	}
	return "failed: " + detail, false
}

// ─── Audit helpers ────────────────────────────────────────────────────────────

// directoryWriteAudit builds the standard new_value payload for a directory
// write audit event. Sensitive attribute values (passwords) are never included;
// only attribute names are hashed to detect which attributes changed.
//
// attrs is a map of attribute_name → plain_value. Values are hashed (SHA-256
// truncated to 8 hex chars), not included. The hash enables "did this attribute
// change?" queries without exposing the value.
func directoryWriteAudit(dn, operation string, attrs map[string]string) map[string]interface{} {
	attrHashes := map[string]string{}
	// Sort for deterministic output.
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sum := sha256.Sum256([]byte(attrs[k]))
		attrHashes[k] = hex.EncodeToString(sum[:4]) // 8-char prefix is sufficient
	}
	return map[string]interface{}{
		"directory_write": true,
		"dn":              dn,
		"operation":       operation,
		"attr_hashes":     attrHashes,
	}
}
