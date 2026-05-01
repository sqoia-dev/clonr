// Package sysaccounts manages local POSIX system accounts and groups that are
// injected into every deployed node's /etc/passwd, /etc/group, and /etc/shadow
// during the finalize step. These are independent of the LDAP module — they
// cover service accounts (slurm, munge, nfs) that must exist before the network
// is up on first boot.
package sysaccounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/posixid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// nameRe validates usernames and group names: must start with [a-z_] and
// contain only [a-z0-9_-]. Max 32 chars enforced separately.
var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// ErrConflict is returned when a create/update would violate a uniqueness constraint
// (duplicate name, UID, or GID).
var ErrConflict = errors.New("conflict")

// Manager owns DB access for the system accounts module. It is safe for
// concurrent use.
type Manager struct {
	db        *db.DB
	allocator *posixid.Allocator
}

// New creates a new Manager. allocator is the shared POSIX ID allocator;
// it may be nil for callers that don't need auto-allocation (e.g. tests).
func New(database *db.DB, allocator *posixid.Allocator) *Manager {
	return &Manager{db: database, allocator: allocator}
}

// ─── Read methods ─────────────────────────────────────────────────────────────

// Groups returns all defined system groups, ordered by GID ascending.
func (m *Manager) Groups(ctx context.Context) ([]api.SystemGroup, error) {
	return m.db.SysAccountsListGroups(ctx)
}

// Accounts returns all defined system accounts, ordered by UID ascending.
func (m *Manager) Accounts(ctx context.Context) ([]api.SystemAccount, error) {
	return m.db.SysAccountsListAccounts(ctx)
}

// NodeConfig builds and returns a SystemAccountsNodeConfig for embedding in
// NodeConfig at reimage-request time. Returns nil (not an error) when no
// accounts or groups are defined so that finalize skips injection cleanly.
func (m *Manager) NodeConfig(ctx context.Context) (*api.SystemAccountsNodeConfig, error) {
	groups, err := m.Groups(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := m.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 && len(accounts) == 0 {
		return nil, nil
	}
	return &api.SystemAccountsNodeConfig{
		Groups:   groups,
		Accounts: accounts,
	}, nil
}

// ─── Group CRUD ───────────────────────────────────────────────────────────────

// CreateGroup validates and inserts a new system group.
// Returns ErrConflict if the name or GID is already in use.
func (m *Manager) CreateGroup(ctx context.Context, g api.SystemGroup) (api.SystemGroup, error) {
	if err := validateGroup(g); err != nil {
		return api.SystemGroup{}, err
	}

	// Conflict check.
	existing, err := m.db.SysAccountsListGroups(ctx)
	if err != nil {
		return api.SystemGroup{}, fmt.Errorf("sysaccounts: list groups for conflict check: %w", err)
	}
	for _, eg := range existing {
		if eg.Name == g.Name {
			return api.SystemGroup{}, fmt.Errorf("%w: group name %q is already in use", ErrConflict, g.Name)
		}
		if eg.GID == g.GID {
			return api.SystemGroup{}, fmt.Errorf("%w: GID %d is already in use by group %q", ErrConflict, g.GID, eg.Name)
		}
	}

	now := time.Now().UTC()
	g.ID = uuid.New().String()
	g.CreatedAt = now
	g.UpdatedAt = now

	if err := m.db.SysAccountsCreateGroup(ctx, g); err != nil {
		return api.SystemGroup{}, fmt.Errorf("sysaccounts: create group: %w", err)
	}
	log.Info().Str("name", g.Name).Int("gid", g.GID).Msg("sysaccounts: group created")
	return g, nil
}

// UpdateGroup replaces a group definition. GID changes are validated for
// conflicts before writing.
func (m *Manager) UpdateGroup(ctx context.Context, id string, g api.SystemGroup) (api.SystemGroup, error) {
	if err := validateGroup(g); err != nil {
		return api.SystemGroup{}, err
	}

	existing, err := m.db.SysAccountsListGroups(ctx)
	if err != nil {
		return api.SystemGroup{}, fmt.Errorf("sysaccounts: list groups for conflict check: %w", err)
	}

	var current *api.SystemGroup
	for i, eg := range existing {
		if eg.ID == id {
			current = &existing[i]
			break
		}
	}
	if current == nil {
		return api.SystemGroup{}, fmt.Errorf("group %q not found", id)
	}

	for _, eg := range existing {
		if eg.ID == id {
			continue
		}
		if eg.Name == g.Name {
			return api.SystemGroup{}, fmt.Errorf("%w: group name %q is already in use by a different group", ErrConflict, g.Name)
		}
		if eg.GID == g.GID {
			return api.SystemGroup{}, fmt.Errorf("%w: GID %d is already in use by group %q", ErrConflict, g.GID, eg.Name)
		}
	}

	// If GID is changing, check no account references the old GID.
	if g.GID != current.GID {
		accounts, err := m.db.SysAccountsListAccounts(ctx)
		if err != nil {
			return api.SystemGroup{}, fmt.Errorf("sysaccounts: list accounts for gid reference check: %w", err)
		}
		var conflicting []string
		for _, a := range accounts {
			if a.PrimaryGID == current.GID {
				conflicting = append(conflicting, a.Username)
			}
		}
		if len(conflicting) > 0 {
			return api.SystemGroup{}, fmt.Errorf("%w: GID change would orphan accounts: %s — update those accounts first",
				ErrConflict, strings.Join(conflicting, ", "))
		}
	}

	g.ID = id
	g.CreatedAt = current.CreatedAt
	g.UpdatedAt = time.Now().UTC()

	if err := m.db.SysAccountsUpdateGroup(ctx, g); err != nil {
		return api.SystemGroup{}, fmt.Errorf("sysaccounts: update group: %w", err)
	}
	log.Info().Str("id", id).Str("name", g.Name).Int("gid", g.GID).Msg("sysaccounts: group updated")
	return g, nil
}

// DeleteGroup removes a group by ID. Returns ErrConflict if any account
// references this group's GID as its primary_gid.
func (m *Manager) DeleteGroup(ctx context.Context, id string) error {
	groups, err := m.db.SysAccountsListGroups(ctx)
	if err != nil {
		return fmt.Errorf("sysaccounts: list groups: %w", err)
	}
	var target *api.SystemGroup
	for i, g := range groups {
		if g.ID == id {
			target = &groups[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("group %q not found", id)
	}

	accounts, err := m.db.SysAccountsListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("sysaccounts: list accounts: %w", err)
	}
	var conflicting []string
	for _, a := range accounts {
		if a.PrimaryGID == target.GID {
			conflicting = append(conflicting, a.Username)
		}
	}
	if len(conflicting) > 0 {
		return fmt.Errorf("%w: cannot delete group — accounts reference its GID %d: %s",
			ErrConflict, target.GID, strings.Join(conflicting, ", "))
	}

	if err := m.db.SysAccountsDeleteGroup(ctx, id); err != nil {
		return fmt.Errorf("sysaccounts: delete group: %w", err)
	}
	log.Info().Str("id", id).Str("name", target.Name).Msg("sysaccounts: group deleted")
	return nil
}

// ─── Account CRUD ─────────────────────────────────────────────────────────────

// CreateAccount validates and inserts a new system account.
// Returns ErrConflict if the username or UID is already in use.
// If a.UID == 0 and an allocator is configured, a UID is auto-allocated.
func (m *Manager) CreateAccount(ctx context.Context, a api.SystemAccount) (api.SystemAccount, error) {
	// Auto-allocate UID if requested and allocator is available.
	if a.UID == 0 && m.allocator != nil {
		uid, err := m.allocator.AllocateUID(ctx)
		if err != nil {
			return api.SystemAccount{}, fmt.Errorf("sysaccounts: auto-allocate uid: %w", err)
		}
		a.UID = uid
	}

	if err := validateAccount(a); err != nil {
		return api.SystemAccount{}, err
	}

	existing, err := m.db.SysAccountsListAccounts(ctx)
	if err != nil {
		return api.SystemAccount{}, fmt.Errorf("sysaccounts: list accounts for conflict check: %w", err)
	}
	for _, ea := range existing {
		if ea.Username == a.Username {
			return api.SystemAccount{}, fmt.Errorf("%w: username %q is already in use", ErrConflict, a.Username)
		}
		if ea.UID == a.UID {
			return api.SystemAccount{}, fmt.Errorf("%w: UID %d is already in use by account %q", ErrConflict, a.UID, ea.Username)
		}
	}

	now := time.Now().UTC()
	a.ID = uuid.New().String()
	a.CreatedAt = now
	a.UpdatedAt = now

	if err := m.db.SysAccountsCreateAccount(ctx, a); err != nil {
		return api.SystemAccount{}, fmt.Errorf("sysaccounts: create account: %w", err)
	}
	log.Info().Str("username", a.Username).Int("uid", a.UID).Msg("sysaccounts: account created")
	return a, nil
}

// UpdateAccount replaces an account definition. UID changes are validated for
// conflicts before writing.
func (m *Manager) UpdateAccount(ctx context.Context, id string, a api.SystemAccount) (api.SystemAccount, error) {
	if err := validateAccount(a); err != nil {
		return api.SystemAccount{}, err
	}

	existing, err := m.db.SysAccountsListAccounts(ctx)
	if err != nil {
		return api.SystemAccount{}, fmt.Errorf("sysaccounts: list accounts for conflict check: %w", err)
	}

	var current *api.SystemAccount
	for i, ea := range existing {
		if ea.ID == id {
			current = &existing[i]
			break
		}
	}
	if current == nil {
		return api.SystemAccount{}, fmt.Errorf("account %q not found", id)
	}

	for _, ea := range existing {
		if ea.ID == id {
			continue
		}
		if ea.Username == a.Username {
			return api.SystemAccount{}, fmt.Errorf("%w: username %q is already in use by a different account", ErrConflict, a.Username)
		}
		if ea.UID == a.UID {
			return api.SystemAccount{}, fmt.Errorf("%w: UID %d is already in use by account %q", ErrConflict, a.UID, ea.Username)
		}
	}

	a.ID = id
	a.CreatedAt = current.CreatedAt
	a.UpdatedAt = time.Now().UTC()

	if err := m.db.SysAccountsUpdateAccount(ctx, a); err != nil {
		return api.SystemAccount{}, fmt.Errorf("sysaccounts: update account: %w", err)
	}
	log.Info().Str("id", id).Str("username", a.Username).Int("uid", a.UID).Msg("sysaccounts: account updated")
	return a, nil
}

// DeleteAccount removes an account by ID.
func (m *Manager) DeleteAccount(ctx context.Context, id string) error {
	accounts, err := m.db.SysAccountsListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("sysaccounts: list accounts: %w", err)
	}
	var target *api.SystemAccount
	for i, a := range accounts {
		if a.ID == id {
			target = &accounts[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("account %q not found", id)
	}

	if err := m.db.SysAccountsDeleteAccount(ctx, id); err != nil {
		return fmt.Errorf("sysaccounts: delete account: %w", err)
	}
	log.Info().Str("id", id).Str("username", target.Username).Msg("sysaccounts: account deleted")
	return nil
}

// ─── Validation ───────────────────────────────────────────────────────────────

func validateGroup(g api.SystemGroup) error {
	if g.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(g.Name) > 32 {
		return fmt.Errorf("name must be 32 characters or fewer")
	}
	if !nameRe.MatchString(g.Name) {
		return fmt.Errorf("name must match ^[a-z_][a-z0-9_-]*$")
	}
	if g.GID < 1 || g.GID > 65534 {
		return fmt.Errorf("gid must be between 1 and 65534")
	}
	return nil
}

func validateAccount(a api.SystemAccount) error {
	if a.Username == "" {
		return fmt.Errorf("username is required")
	}
	if len(a.Username) > 32 {
		return fmt.Errorf("username must be 32 characters or fewer")
	}
	if !nameRe.MatchString(a.Username) {
		return fmt.Errorf("username must match ^[a-z_][a-z0-9_-]*$")
	}
	if a.UID < 1 || a.UID > 65534 {
		return fmt.Errorf("uid must be between 1 and 65534")
	}
	if a.PrimaryGID < 1 || a.PrimaryGID > 65534 {
		return fmt.Errorf("primary_gid must be between 1 and 65534")
	}
	if a.Shell == "" {
		a.Shell = "/sbin/nologin"
	}
	if a.Shell[0] != '/' {
		return fmt.Errorf("shell must be an absolute path")
	}
	if a.HomeDir == "" {
		a.HomeDir = "/dev/null"
	}
	if a.HomeDir[0] != '/' {
		return fmt.Errorf("home_dir must be an absolute path")
	}
	return nil
}

// EnsureDefaults fills in any missing defaults on an account before validation
// so the API layer doesn't need to re-derive these.
func EnsureDefaults(a *api.SystemAccount) {
	if a.Shell == "" {
		a.Shell = "/sbin/nologin"
	}
	if a.HomeDir == "" {
		a.HomeDir = "/dev/null"
	}
}
