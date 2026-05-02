package selector

import (
	"context"
	"fmt"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// DBAdapter wraps *db.DB to satisfy the SelectorDB interface.
// Use NewDBAdapter to construct it.
type DBAdapter struct {
	db *db.DB
}

// NewDBAdapter wraps the given database for use with Resolve.
func NewDBAdapter(database *db.DB) *DBAdapter {
	return &DBAdapter{db: database}
}

// ListAllNodes returns all node configs as lightweight SelectorNode values.
func (a *DBAdapter) ListAllNodes(ctx context.Context) ([]SelectorNode, error) {
	nodes, err := a.db.ListNodeConfigs(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]SelectorNode, len(nodes))
	for i, n := range nodes {
		out[i] = SelectorNode{
			ID:       n.ID,
			Hostname: n.Hostname,
			Active:   n.State() == api.NodeStateDeployedVerified,
		}
	}
	return out, nil
}

// ListGroupMemberIDs resolves group name → group ID → member node IDs.
func (a *DBAdapter) ListGroupMemberIDs(ctx context.Context, groupName string) ([]NodeID, error) {
	grp, err := a.db.GetNodeGroupByName(ctx, groupName)
	if err != nil {
		return nil, fmt.Errorf("group %q not found: %w", groupName, err)
	}
	members, err := a.db.ListGroupMembers(ctx, grp.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]NodeID, len(members))
	for i, m := range members {
		ids[i] = m.ID
	}
	return ids, nil
}

// ListNodeIDsByRackNames delegates to the DB layer added in #149.
func (a *DBAdapter) ListNodeIDsByRackNames(ctx context.Context, rackNames []string) ([]NodeID, error) {
	return a.db.ListNodeIDsByRackNames(ctx, rackNames)
}
