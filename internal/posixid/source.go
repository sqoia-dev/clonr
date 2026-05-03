// source.go — DB-backed IDSource implementation.
// DBLDAPSource combines a *db.DB (for system_accounts/system_groups) with
// optional LDAP UID/GID lister functions to satisfy the IDSource interface.
// The lister functions are provided by the caller (typically the ldap.Manager)
// to avoid an import cycle between posixid ↔ ldap.
package posixid

import (
	"context"
	"fmt"

	"github.com/sqoia-dev/clustr/internal/db"
)

// DBLDAPSource satisfies IDSource using the DB for sys account IDs and optional
// caller-supplied functions for live LDAP directory IDs. If UIDs/GIDs is nil
// (LDAP not yet enabled or not reachable), those lists are treated as empty.
type DBLDAPSource struct {
	DB   *db.DB
	UIDs func(ctx context.Context) ([]int, error) // LDAP uidNumber lister; nil = skip
	GIDs func(ctx context.Context) ([]int, error) // LDAP gidNumber lister; nil = skip
}

func (s *DBLDAPSource) ListLDAPUIDs(ctx context.Context) ([]int, error) {
	if s.UIDs == nil {
		return nil, nil
	}
	return s.UIDs(ctx)
}

func (s *DBLDAPSource) ListLDAPGIDs(ctx context.Context) ([]int, error) {
	if s.GIDs == nil {
		return nil, nil
	}
	return s.GIDs(ctx)
}

func (s *DBLDAPSource) ListSysUIDs(ctx context.Context) ([]int, error) {
	return s.DB.SysAccountsListUIDs(ctx)
}

func (s *DBLDAPSource) ListSysGIDs(ctx context.Context) ([]int, error) {
	return s.DB.SysAccountsListGIDs(ctx)
}

// GetConfig reads allocation config for the given role from posixid_role_ranges.
// Falls back to posixid_config (migration 081 legacy row) for unknown roles so
// existing callers that haven't migrated yet still get a sensible config.
func (s *DBLDAPSource) GetConfig(ctx context.Context, role Role) (Config, error) {
	row, err := s.DB.PosixIDGetRoleRange(ctx, string(role))
	if err != nil {
		// If the role row is missing (e.g. fresh DB before 084 migration runs, or
		// an unknown role), fall back to the legacy posixid_config row.
		legacy, legErr := s.DB.PosixIDGetConfig(ctx)
		if legErr != nil {
			return Config{}, fmt.Errorf("posixid source: get config for role %q (role row missing, legacy fallback also failed): %w", role, legErr)
		}
		reservedUID, err := ParseRanges(legacy.ReservedUIDRanges)
		if err != nil {
			return Config{}, fmt.Errorf("posixid source: parse legacy reserved_uid_ranges: %w", err)
		}
		reservedGID, err := ParseRanges(legacy.ReservedGIDRanges)
		if err != nil {
			return Config{}, fmt.Errorf("posixid source: parse legacy reserved_gid_ranges: %w", err)
		}
		return Config{
			UIDMin:            legacy.UIDMin,
			UIDMax:            legacy.UIDMax,
			GIDMin:            legacy.GIDMin,
			GIDMax:            legacy.GIDMax,
			ReservedUIDRanges: reservedUID,
			ReservedGIDRanges: reservedGID,
		}, nil
	}

	reservedUID, err := ParseRanges(row.ReservedUIDRanges)
	if err != nil {
		return Config{}, fmt.Errorf("posixid source: parse reserved_uid_ranges for role %q: %w", role, err)
	}
	reservedGID, err := ParseRanges(row.ReservedGIDRanges)
	if err != nil {
		return Config{}, fmt.Errorf("posixid source: parse reserved_gid_ranges for role %q: %w", role, err)
	}

	return Config{
		UIDMin:            row.UIDMin,
		UIDMax:            row.UIDMax,
		GIDMin:            row.GIDMin,
		GIDMax:            row.GIDMax,
		ReservedUIDRanges: reservedUID,
		ReservedGIDRanges: reservedGID,
	}, nil
}
