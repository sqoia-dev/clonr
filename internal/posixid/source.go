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

func (s *DBLDAPSource) GetConfig(ctx context.Context) (Config, error) {
	row, err := s.DB.PosixIDGetConfig(ctx)
	if err != nil {
		return Config{}, fmt.Errorf("posixid source: get config: %w", err)
	}

	reservedUID, err := ParseRanges(row.ReservedUIDRanges)
	if err != nil {
		return Config{}, fmt.Errorf("posixid source: parse reserved_uid_ranges: %w", err)
	}
	reservedGID, err := ParseRanges(row.ReservedGIDRanges)
	if err != nil {
		return Config{}, fmt.Errorf("posixid source: parse reserved_gid_ranges: %w", err)
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
