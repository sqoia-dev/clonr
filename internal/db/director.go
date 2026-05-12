package db

import (
	"context"
	"fmt"
	"time"
)

// DirectorGroupView is a NodeGroup summary for the IT Director dashboard.
// PIUserID, PIUsername, and MemberCount were removed in Sprint 43-prime Day 2
// (PI-CODE-WIPE) when pi_member_requests and node_groups.pi_user_id were dropped.
type DirectorGroupView struct {
	ID            string
	Name          string
	Description   string
	NodeCount     int
	DeployedCount int
	GrantCount    int
	PubCount      int
	LastDeployAt  *int64
}

// DirectorSummary holds cluster-wide aggregate stats for the director.
type DirectorSummary struct {
	TotalNodes        int     `json:"total_nodes"`
	TotalDeployed     int     `json:"total_deployed"`
	TotalGroups       int     `json:"total_groups"`
	TotalPIs          int     `json:"total_pis"`
	TotalResearchers  int     `json:"total_researchers"`
	TotalGrants       int     `json:"total_grants"`
	TotalPubs         int     `json:"total_publications"`
	DeploySuccessRate float64 `json:"deploy_success_rate_30d"`
}

// GetDirectorSummary returns cluster-wide aggregate stats for the director view.
func (db *DB) GetDirectorSummary(ctx context.Context) (DirectorSummary, error) {
	var s DirectorSummary

	err := db.sql.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM node_configs) AS total_nodes,
			(SELECT COUNT(*) FROM node_configs WHERE deploy_completed_preboot_at IS NOT NULL) AS total_deployed,
			(SELECT COUNT(*) FROM node_groups) AS total_groups,
			(SELECT COUNT(*) FROM users WHERE role='pi' AND disabled_at IS NULL) AS total_pis,
			(SELECT COUNT(*) FROM users WHERE role='viewer' AND disabled_at IS NULL) AS total_researchers,
			(SELECT COUNT(*) FROM grants) AS total_grants,
			(SELECT COUNT(*) FROM publications) AS total_pubs
	`).Scan(&s.TotalNodes, &s.TotalDeployed, &s.TotalGroups,
		&s.TotalPIs, &s.TotalResearchers, &s.TotalGrants, &s.TotalPubs)
	if err != nil {
		return s, fmt.Errorf("db: director summary: %w", err)
	}

	// Deploy success rate in last 30 days.
	cutoff := time.Now().AddDate(0, 0, -30).Unix()
	var total, succeeded int
	_ = db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       SUM(CASE WHEN action='node.reimage' AND json_extract(new_value,'$.status')='succeeded' THEN 1 ELSE 0 END)
		FROM audit_log WHERE action='node.reimage' AND created_at >= ?
	`, cutoff).Scan(&total, &succeeded)
	if total > 0 {
		s.DeploySuccessRate = float64(succeeded) / float64(total) * 100
	}

	return s, nil
}

// ListDirectorGroups returns all NodeGroups with summary columns for the director.
// No longer joins pi_user_id or queries pi_member_requests (both dropped in migrations 103/119).
func (db *DB) ListDirectorGroups(ctx context.Context) ([]DirectorGroupView, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT
			ng.id, ng.name, COALESCE(ng.description,''),
			(SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
			(SELECT COUNT(*) FROM node_configs nc
			  LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
			  WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
			(SELECT COUNT(*) FROM grants g WHERE g.node_group_id = ng.id) AS grant_count,
			(SELECT COUNT(*) FROM publications p WHERE p.node_group_id = ng.id) AS pub_count,
			(SELECT MAX(nc2.deploy_completed_preboot_at)
			  FROM node_configs nc2
			  LEFT JOIN node_group_memberships m3 ON m3.node_id = nc2.id AND m3.is_primary = 1
			  WHERE m3.group_id = ng.id) AS last_deploy_at
		FROM node_groups ng
		ORDER BY ng.name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list director groups: %w", err)
	}
	defer rows.Close()

	var out []DirectorGroupView
	for rows.Next() {
		var g DirectorGroupView
		var lastDeploy *int64
		if err := rows.Scan(
			&g.ID, &g.Name, &g.Description,
			&g.NodeCount, &g.DeployedCount,
			&g.GrantCount, &g.PubCount, &lastDeploy,
		); err != nil {
			return nil, fmt.Errorf("db: scan director group: %w", err)
		}
		g.LastDeployAt = lastDeploy
		out = append(out, g)
	}
	return out, rows.Err()
}
