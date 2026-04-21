package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// ─── Switches ─────────────────────────────────────────────────────────────────

// NetworkListSwitches returns all switches ordered by name.
func (db *DB) NetworkListSwitches(ctx context.Context) ([]api.NetworkSwitch, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, role, vendor, model, mgmt_ip, notes, is_managed, created_at, updated_at
		FROM network_switches
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var switches []api.NetworkSwitch
	for rows.Next() {
		var s api.NetworkSwitch
		var isManaged int
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Role, &s.Vendor, &s.Model,
			&s.MgmtIP, &s.Notes, &isManaged, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		s.IsManaged = isManaged != 0
		s.CreatedAt = time.Unix(createdAt, 0).UTC()
		s.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		switches = append(switches, s)
	}
	return switches, rows.Err()
}

// NetworkCreateSwitch inserts a new switch record.
func (db *DB) NetworkCreateSwitch(ctx context.Context, s api.NetworkSwitch) error {
	isManaged := 1
	if !s.IsManaged {
		isManaged = 0
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO network_switches
		    (id, name, role, vendor, model, mgmt_ip, notes, is_managed, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.Name, string(s.Role), s.Vendor, s.Model, s.MgmtIP, s.Notes,
		isManaged, s.CreatedAt.Unix(), s.UpdatedAt.Unix())
	return err
}

// NetworkUpdateSwitch replaces a switch row identified by ID.
func (db *DB) NetworkUpdateSwitch(ctx context.Context, s api.NetworkSwitch) error {
	isManaged := 1
	if !s.IsManaged {
		isManaged = 0
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE network_switches
		SET name=?, role=?, vendor=?, model=?, mgmt_ip=?, notes=?, is_managed=?, updated_at=?
		WHERE id=?
	`, s.Name, string(s.Role), s.Vendor, s.Model, s.MgmtIP, s.Notes,
		isManaged, s.UpdatedAt.Unix(), s.ID)
	return err
}

// NetworkDeleteSwitch removes a switch by ID.
func (db *DB) NetworkDeleteSwitch(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM network_switches WHERE id=?`, id)
	return err
}

// ─── Profiles ─────────────────────────────────────────────────────────────────

// networkScanProfile scans a network_profiles row into an api.NetworkProfile.
// It does not populate Bonds or IB — callers must do that separately.
func networkScanProfile(row interface {
	Scan(dest ...interface{}) error
}) (api.NetworkProfile, error) {
	var p api.NetworkProfile
	var createdAt, updatedAt int64
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &createdAt, &updatedAt); err != nil {
		return api.NetworkProfile{}, err
	}
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return p, nil
}

// networkLoadBondsForProfile fetches bond_configs and their bond_members for the
// given profile IDs. Returns a map of profileID → []BondConfig.
func (db *DB) networkLoadBondsForProfiles(ctx context.Context, profileIDs []string) (map[string][]api.BondConfig, error) {
	if len(profileIDs) == 0 {
		return map[string][]api.BondConfig{}, nil
	}

	// Build placeholders: one ? per ID.
	placeholders := make([]string, len(profileIDs))
	args := make([]interface{}, len(profileIDs))
	for i, id := range profileIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, profile_id, bond_name, mode, mtu, vlan_id, ip_method, ip_cidr,
		       lacp_rate, xmit_hash_policy, sort_order, created_at, updated_at
		FROM bond_configs
		WHERE profile_id IN (`+inClause+`)
		ORDER BY profile_id, sort_order, bond_name
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bondsMap := map[string][]api.BondConfig{}
	bondOrder := map[string][]string{} // profileID → ordered bond IDs
	allBonds := map[string]*api.BondConfig{}

	for rows.Next() {
		var b api.BondConfig
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&b.ID, &b.ProfileID, &b.BondName, &b.Mode, &b.MTU, &b.VLANID,
			&b.IPMethod, &b.IPCIDR, &b.LACPRate, &b.XmitHashPolicy,
			&b.SortOrder, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(createdAt, 0).UTC()
		b.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		b.Members = []api.BondMember{}
		allBonds[b.ID] = &b
		bondOrder[b.ProfileID] = append(bondOrder[b.ProfileID], b.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load all members for these bonds.
	if len(allBonds) > 0 {
		bondIDs := make([]string, 0, len(allBonds))
		for id := range allBonds {
			bondIDs = append(bondIDs, id)
		}
		mPlaceholders := make([]string, len(bondIDs))
		mArgs := make([]interface{}, len(bondIDs))
		for i, id := range bondIDs {
			mPlaceholders[i] = "?"
			mArgs[i] = id
		}
		mRows, err := db.sql.QueryContext(ctx, `
			SELECT id, bond_id, match_mac, match_name, sort_order
			FROM bond_members
			WHERE bond_id IN (`+strings.Join(mPlaceholders, ",")+`)
			ORDER BY bond_id, sort_order
		`, mArgs...)
		if err != nil {
			return nil, err
		}
		defer mRows.Close()
		for mRows.Next() {
			var m api.BondMember
			if err := mRows.Scan(&m.ID, &m.BondID, &m.MatchMAC, &m.MatchName, &m.SortOrder); err != nil {
				return nil, err
			}
			if b, ok := allBonds[m.BondID]; ok {
				b.Members = append(b.Members, m)
			}
		}
		if err := mRows.Err(); err != nil {
			return nil, err
		}
	}

	// Assemble bondsMap in insertion order.
	for profID, ids := range bondOrder {
		for _, bondID := range ids {
			bondsMap[profID] = append(bondsMap[profID], *allBonds[bondID])
		}
	}
	return bondsMap, nil
}

// networkLoadIBProfilesForProfiles fetches ib_profiles for the given profile IDs.
// Returns a map of profileID → *IBProfile (nil when no IB profile exists).
func (db *DB) networkLoadIBProfilesForProfiles(ctx context.Context, profileIDs []string) (map[string]*api.IBProfile, error) {
	if len(profileIDs) == 0 {
		return map[string]*api.IBProfile{}, nil
	}
	placeholders := make([]string, len(profileIDs))
	args := make([]interface{}, len(profileIDs))
	for i, id := range profileIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, profile_id, ipoib_mode, ipoib_mtu, ip_method, pkeys, device_match,
		       created_at, updated_at
		FROM ib_profiles
		WHERE profile_id IN (`+strings.Join(placeholders, ",")+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]*api.IBProfile{}
	for rows.Next() {
		var ib api.IBProfile
		var createdAt, updatedAt int64
		var pkeysRaw string
		if err := rows.Scan(
			&ib.ID, &ib.ProfileID, &ib.IPoIBMode, &ib.IPoIBMTU,
			&ib.IPMethod, &pkeysRaw, &ib.DeviceMatch, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		ib.CreatedAt = time.Unix(createdAt, 0).UTC()
		ib.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		ib.PKeys = []string{}
		if pkeysRaw != "" {
			for _, pk := range strings.Fields(pkeysRaw) {
				ib.PKeys = append(ib.PKeys, pk)
			}
		}
		ibCopy := ib
		result[ib.ProfileID] = &ibCopy
	}
	return result, rows.Err()
}

// NetworkListProfiles returns all profiles with their bonds, bond members, and IB profiles.
func (db *DB) NetworkListProfiles(ctx context.Context) ([]api.NetworkProfile, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM network_profiles
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []api.NetworkProfile
	var ids []string
	for rows.Next() {
		p, err := networkScanProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
		ids = append(ids, p.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	bondsMap, err := db.networkLoadBondsForProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	ibMap, err := db.networkLoadIBProfilesForProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}

	for i := range profiles {
		if bonds, ok := bondsMap[profiles[i].ID]; ok {
			profiles[i].Bonds = bonds
		} else {
			profiles[i].Bonds = []api.BondConfig{}
		}
		profiles[i].IB = ibMap[profiles[i].ID]
	}
	return profiles, nil
}

// NetworkGetProfile returns a single profile by ID with full nested data.
// Returns an error containing "not found" when no row exists.
func (db *DB) NetworkGetProfile(ctx context.Context, id string) (api.NetworkProfile, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM network_profiles WHERE id=?
	`, id)
	p, err := networkScanProfile(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return api.NetworkProfile{}, fmt.Errorf("profile %q not found", id)
		}
		return api.NetworkProfile{}, err
	}

	bondsMap, err := db.networkLoadBondsForProfiles(ctx, []string{p.ID})
	if err != nil {
		return api.NetworkProfile{}, err
	}
	ibMap, err := db.networkLoadIBProfilesForProfiles(ctx, []string{p.ID})
	if err != nil {
		return api.NetworkProfile{}, err
	}
	if bonds, ok := bondsMap[p.ID]; ok {
		p.Bonds = bonds
	} else {
		p.Bonds = []api.BondConfig{}
	}
	p.IB = ibMap[p.ID]
	return p, nil
}

// NetworkCreateProfile inserts a profile + bonds + members + ib_profile transactionally.
func (db *DB) NetworkCreateProfile(ctx context.Context, p api.NetworkProfile) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO network_profiles (id, name, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, p.ID, p.Name, p.Description, p.CreatedAt.Unix(), p.UpdatedAt.Unix())
	if err != nil {
		return err
	}

	for _, b := range p.Bonds {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO bond_configs
			    (id, profile_id, bond_name, mode, mtu, vlan_id, ip_method, ip_cidr,
			     lacp_rate, xmit_hash_policy, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, b.ID, p.ID, b.BondName, b.Mode, b.MTU, b.VLANID, b.IPMethod, b.IPCIDR,
			b.LACPRate, b.XmitHashPolicy, b.SortOrder, b.CreatedAt.Unix(), b.UpdatedAt.Unix())
		if err != nil {
			return err
		}
		for _, m := range b.Members {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO bond_members (id, bond_id, match_mac, match_name, sort_order)
				VALUES (?, ?, ?, ?, ?)
			`, m.ID, b.ID, m.MatchMAC, m.MatchName, m.SortOrder)
			if err != nil {
				return err
			}
		}
	}

	if p.IB != nil {
		pkeysRaw := strings.Join(p.IB.PKeys, " ")
		_, err = tx.ExecContext(ctx, `
			INSERT INTO ib_profiles
			    (id, profile_id, ipoib_mode, ipoib_mtu, ip_method, pkeys, device_match,
			     created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, p.IB.ID, p.ID, p.IB.IPoIBMode, p.IB.IPoIBMTU, p.IB.IPMethod,
			pkeysRaw, p.IB.DeviceMatch, p.IB.CreatedAt.Unix(), p.IB.UpdatedAt.Unix())
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// NetworkUpdateProfile replaces a profile's bonds, members, and ib_profile transactionally.
// The profile row itself is updated in place; child rows are deleted and recreated.
func (db *DB) NetworkUpdateProfile(ctx context.Context, p api.NetworkProfile) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		UPDATE network_profiles SET name=?, description=?, updated_at=? WHERE id=?
	`, p.Name, p.Description, p.UpdatedAt.Unix(), p.ID)
	if err != nil {
		return err
	}

	// Delete child rows — CASCADE handles bond_members when bond_configs is deleted.
	if _, err = tx.ExecContext(ctx, `DELETE FROM bond_configs WHERE profile_id=?`, p.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM ib_profiles WHERE profile_id=?`, p.ID); err != nil {
		return err
	}

	// Re-insert bonds and members.
	for _, b := range p.Bonds {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO bond_configs
			    (id, profile_id, bond_name, mode, mtu, vlan_id, ip_method, ip_cidr,
			     lacp_rate, xmit_hash_policy, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, b.ID, p.ID, b.BondName, b.Mode, b.MTU, b.VLANID, b.IPMethod, b.IPCIDR,
			b.LACPRate, b.XmitHashPolicy, b.SortOrder, b.CreatedAt.Unix(), b.UpdatedAt.Unix())
		if err != nil {
			return err
		}
		for _, m := range b.Members {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO bond_members (id, bond_id, match_mac, match_name, sort_order)
				VALUES (?, ?, ?, ?, ?)
			`, m.ID, b.ID, m.MatchMAC, m.MatchName, m.SortOrder)
			if err != nil {
				return err
			}
		}
	}

	if p.IB != nil {
		pkeysRaw := strings.Join(p.IB.PKeys, " ")
		_, err = tx.ExecContext(ctx, `
			INSERT INTO ib_profiles
			    (id, profile_id, ipoib_mode, ipoib_mtu, ip_method, pkeys, device_match,
			     created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, p.IB.ID, p.ID, p.IB.IPoIBMode, p.IB.IPoIBMTU, p.IB.IPMethod,
			pkeysRaw, p.IB.DeviceMatch, p.IB.CreatedAt.Unix(), p.IB.UpdatedAt.Unix())
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// NetworkDeleteProfile removes a profile by ID.
// Returns an error containing "profile_in_use" when group_network_profiles references it.
// Also returns the names of the groups that reference it.
func (db *DB) NetworkDeleteProfile(ctx context.Context, id string) ([]string, error) {
	// Find any groups referencing this profile.
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.name
		FROM group_network_profiles gnp
		JOIN node_groups ng ON ng.id = gnp.group_id
		WHERE gnp.profile_id = ?
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		groups = append(groups, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(groups) > 0 {
		return groups, fmt.Errorf("profile_in_use")
	}

	_, err = db.sql.ExecContext(ctx, `DELETE FROM network_profiles WHERE id=?`, id)
	return nil, err
}

// ─── Group → Profile assignment ───────────────────────────────────────────────

// NetworkAssignProfileToGroup upserts a group → profile mapping.
func (db *DB) NetworkAssignProfileToGroup(ctx context.Context, groupID, profileID string) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO group_network_profiles (group_id, profile_id)
		VALUES (?, ?)
		ON CONFLICT(group_id) DO UPDATE SET profile_id=excluded.profile_id
	`, groupID, profileID)
	return err
}

// NetworkUnassignProfileFromGroup removes the group → profile mapping for groupID.
func (db *DB) NetworkUnassignProfileFromGroup(ctx context.Context, groupID string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM group_network_profiles WHERE group_id=?`, groupID)
	return err
}

// NetworkGetGroupProfile returns the full NetworkProfile assigned to a group.
// Returns nil when no assignment exists.
func (db *DB) NetworkGetGroupProfile(ctx context.Context, groupID string) (*api.NetworkProfile, error) {
	var profileID string
	err := db.sql.QueryRowContext(ctx, `
		SELECT profile_id FROM group_network_profiles WHERE group_id=?
	`, groupID).Scan(&profileID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p, err := db.NetworkGetProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ─── OpenSM config ────────────────────────────────────────────────────────────

// NetworkGetOpenSMConfig returns the single opensm_config row, or nil if none exists.
func (db *DB) NetworkGetOpenSMConfig(ctx context.Context) (*api.OpenSMConfig, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, enabled, head_node_profile_id, conf_content, log_prefix, sm_priority,
		       created_at, updated_at
		FROM opensm_config LIMIT 1
	`)
	var cfg api.OpenSMConfig
	var enabled int
	var createdAt, updatedAt int64
	err := row.Scan(
		&cfg.ID, &enabled, &cfg.HeadNodeProfileID, &cfg.ConfContent,
		&cfg.LogPrefix, &cfg.SMPriority, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	cfg.Enabled = enabled != 0
	cfg.CreatedAt = time.Unix(createdAt, 0).UTC()
	cfg.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &cfg, nil
}

// NetworkSetOpenSMConfig upserts the opensm_config row.
func (db *DB) NetworkSetOpenSMConfig(ctx context.Context, cfg api.OpenSMConfig) error {
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO opensm_config
		    (id, enabled, head_node_profile_id, conf_content, log_prefix, sm_priority,
		     created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		    enabled=excluded.enabled,
		    head_node_profile_id=excluded.head_node_profile_id,
		    conf_content=excluded.conf_content,
		    log_prefix=excluded.log_prefix,
		    sm_priority=excluded.sm_priority,
		    updated_at=excluded.updated_at
	`, cfg.ID, enabled, cfg.HeadNodeProfileID, cfg.ConfContent,
		cfg.LogPrefix, cfg.SMPriority, cfg.CreatedAt.Unix(), cfg.UpdatedAt.Unix())
	return err
}

// ─── IB status ────────────────────────────────────────────────────────────────

// NetworkHasUnmanagedIBSwitch returns true when any IB switch has is_managed=0.
func (db *DB) NetworkHasUnmanagedIBSwitch(ctx context.Context) (bool, error) {
	var exists bool
	err := db.sql.QueryRowContext(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM network_switches
		    WHERE role='infiniband' AND is_managed=0
		)
	`).Scan(&exists)
	return exists, err
}
