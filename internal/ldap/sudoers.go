// sudoers.go — LDAP sudoers group management.
// Provisions and manages the clustr-admins LDAP group that grants sudo access
// on deployed nodes via a sudoers drop-in written during finalization.
package ldap

import (
	"context"
	"fmt"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	sudoersDefaultGroupCN  = "clustr-admins"
	sudoersDefaultGIDNumber = 50000
)

// EnableSudoers provisions the clustr-admins LDAP group (if it does not already
// exist) and sets sudoers_enabled=1 in the DB. Idempotent: if the group already
// exists in LDAP, the create step is silently skipped.
func (m *Manager) EnableSudoers(ctx context.Context) error {
	cfg, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("ldap sudoers: read config: %w", err)
	}
	if !cfg.Enabled {
		return fmt.Errorf("ldap sudoers: LDAP module is not enabled")
	}
	if cfg.Status != statusReady {
		return fmt.Errorf("ldap sudoers: LDAP module is not ready (status=%s)", cfg.Status)
	}

	groupCN := sudoersDefaultGroupCN

	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("ldap sudoers: get DIT client: %w", err)
	}

	conn, err := dit.connect()
	if err != nil {
		return fmt.Errorf("ldap sudoers: connect to LDAP: %w", err)
	}
	defer conn.Close()

	// Create the posixGroup entry. Skip idempotently if it already exists.
	groupDN := fmt.Sprintf("cn=%s,ou=groups,%s", groupCN, dit.baseDN)
	addReq := goldap.NewAddRequest(groupDN, nil)
	addReq.Attribute("objectClass", []string{"top", "posixGroup"})
	addReq.Attribute("cn", []string{groupCN})
	addReq.Attribute("gidNumber", []string{fmt.Sprintf("%d", sudoersDefaultGIDNumber)})
	addReq.Attribute("description", []string{"clustr managed sudoers group"})

	if err := conn.Add(addReq); err != nil {
		if !goldap.IsErrorWithCode(err, goldap.LDAPResultEntryAlreadyExists) {
			return fmt.Errorf("ldap sudoers: create group %s: %w", groupDN, err)
		}
		log.Info().Str("dn", groupDN).Msg("ldap sudoers: group already exists, skipping create")
	} else {
		log.Info().Str("dn", groupDN).Msg("ldap sudoers: group created")
	}

	if err := m.db.LDAPSetSudoersEnabled(ctx, true, groupCN); err != nil {
		return fmt.Errorf("ldap sudoers: persist enabled state: %w", err)
	}

	log.Info().Str("group_cn", groupCN).Msg("ldap sudoers: enabled")
	return nil
}

// DisableSudoers sets sudoers_enabled=0 in the DB. Does NOT delete the LDAP group.
func (m *Manager) DisableSudoers(ctx context.Context) error {
	if err := m.db.LDAPSetSudoersEnabled(ctx, false, ""); err != nil {
		return fmt.Errorf("ldap sudoers: persist disabled state: %w", err)
	}
	log.Info().Msg("ldap sudoers: disabled (group preserved in LDAP)")
	return nil
}

// SudoersStatus returns the enabled flag, group CN, and current member UIDs.
// Members is nil (not empty slice) when sudoers is disabled or LDAP is not ready.
func (m *Manager) SudoersStatus(ctx context.Context) (enabled bool, groupCN string, members []string, err error) {
	cfg, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return false, "", nil, fmt.Errorf("ldap sudoers: read config: %w", err)
	}

	if !cfg.SudoersEnabled || !cfg.Enabled || cfg.Status != statusReady {
		return cfg.SudoersEnabled, cfg.SudoersGroupCN, nil, nil
	}

	dit, ditErr := m.ReaderDIT(ctx)
	if ditErr != nil {
		// Return status without members rather than failing entirely.
		log.Warn().Err(ditErr).Msg("ldap sudoers: could not get reader DIT, returning status without members")
		return cfg.SudoersEnabled, cfg.SudoersGroupCN, nil, nil
	}

	groups, listErr := dit.ListGroups()
	if listErr != nil {
		log.Warn().Err(listErr).Str("group_cn", cfg.SudoersGroupCN).Msg("ldap sudoers: list groups failed, returning status without members")
		return cfg.SudoersEnabled, cfg.SudoersGroupCN, nil, nil
	}

	for _, g := range groups {
		if g.CN == cfg.SudoersGroupCN {
			members = g.MemberUIDs
			break
		}
	}
	if members == nil {
		members = []string{}
	}

	return cfg.SudoersEnabled, cfg.SudoersGroupCN, members, nil
}

// GrantSudo adds uid to the sudoers LDAP group.
func (m *Manager) GrantSudo(ctx context.Context, uid string) error {
	cfg, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("ldap sudoers: read config: %w", err)
	}
	if !cfg.SudoersEnabled {
		return fmt.Errorf("ldap sudoers: sudoers is not enabled")
	}

	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("ldap sudoers: get DIT client: %w", err)
	}

	if err := dit.AddGroupMember(cfg.SudoersGroupCN, uid); err != nil {
		return fmt.Errorf("ldap sudoers: add %s to group %s: %w", uid, cfg.SudoersGroupCN, err)
	}

	log.Info().Str("uid", uid).Msg("sudoers: granted sudo access")
	return nil
}

// RevokeSudo removes uid from the sudoers LDAP group.
func (m *Manager) RevokeSudo(ctx context.Context, uid string) error {
	cfg, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("ldap sudoers: read config: %w", err)
	}
	if !cfg.SudoersEnabled {
		return fmt.Errorf("ldap sudoers: sudoers is not enabled")
	}

	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("ldap sudoers: get DIT client: %w", err)
	}

	if err := dit.RemoveGroupMember(cfg.SudoersGroupCN, uid); err != nil {
		return fmt.Errorf("ldap sudoers: remove %s from group %s: %w", uid, cfg.SudoersGroupCN, err)
	}

	log.Info().Str("uid", uid).Msg("sudoers: revoked sudo access")
	return nil
}

// SudoersNodeConfig returns the sudoers config for injection into NodeConfig during
// finalization. Returns (nil, nil) if sudoers is disabled, LDAP is not ready, or
// the LDAP module itself is disabled. Returns (nil, err) on DB error only.
func (m *Manager) SudoersNodeConfig(ctx context.Context) (*api.SudoersNodeConfig, error) {
	cfg, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ldap sudoers: read config: %w", err)
	}
	if !cfg.Enabled || cfg.Status != statusReady || !cfg.SudoersEnabled {
		return nil, nil
	}
	return &api.SudoersNodeConfig{
		GroupCN:  cfg.SudoersGroupCN,
		NoPasswd: true,
	}, nil
}
