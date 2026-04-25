package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/db"
)

// makeGroup creates a minimal NodeGroup for testing.
func makeGroup(name, role string) api.NodeGroup {
	now := time.Now().UTC().Truncate(time.Second)
	return api.NodeGroup{
		ID:          uuid.New().String(),
		Name:        name,
		Description: "test group " + name,
		Role:        role,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// makeTestNode creates a minimal NodeConfig (no image required).
func makeTestNode(hostname, mac string) api.NodeConfig {
	now := time.Now().UTC().Truncate(time.Second)
	return api.NodeConfig{
		ID:         uuid.New().String(),
		Hostname:   hostname,
		PrimaryMAC: mac,
		Interfaces: []api.InterfaceConfig{},
		SSHKeys:    []string{},
		Groups:     []string{},
		CustomVars: map[string]string{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// ─── CRUD happy path ─────────────────────────────────────────────────────────

func TestNodeGroup_CreateAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("compute-nodes", "compute")
	if err := d.CreateNodeGroupFull(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := d.GetNodeGroupFull(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != g.Name {
		t.Errorf("name: got %q want %q", got.Name, g.Name)
	}
	if got.Role != "compute" {
		t.Errorf("role: got %q want compute", got.Role)
	}
}

func TestNodeGroup_Update(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("login-nodes", "login")
	_ = d.CreateNodeGroupFull(ctx, g)

	g.Description = "updated description"
	g.Role = "gpu"
	if err := d.UpdateNodeGroupFull(ctx, g); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := d.GetNodeGroupFull(ctx, g.ID)
	if got.Description != "updated description" {
		t.Errorf("description: got %q", got.Description)
	}
	if got.Role != "gpu" {
		t.Errorf("role: got %q", got.Role)
	}
}

func TestNodeGroup_Delete(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("to-delete", "storage")
	_ = d.CreateNodeGroupFull(ctx, g)

	if err := d.DeleteNodeGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := d.GetNodeGroupFull(ctx, g.ID)
	if err != api.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestNodeGroup_ListWithCount(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g1 := makeGroup("compute-A", "compute")
	g2 := makeGroup("login-B", "login")
	_ = d.CreateNodeGroupFull(ctx, g1)
	_ = d.CreateNodeGroupFull(ctx, g2)

	// Add two nodes to g1.
	n1 := makeTestNode("node-01", "aa:bb:cc:dd:01:01")
	n2 := makeTestNode("node-02", "aa:bb:cc:dd:01:02")
	_ = d.CreateNodeConfig(ctx, n1)
	_ = d.CreateNodeConfig(ctx, n2)
	_ = d.AddGroupMember(ctx, g1.ID, n1.ID)
	_ = d.AddGroupMember(ctx, g1.ID, n2.ID)

	groups, err := d.ListNodeGroupsWithCount(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("count: got %d want 2", len(groups))
	}
	// Groups are ordered by name: compute-A, login-B.
	if groups[0].MemberCount != 2 {
		t.Errorf("compute-A member count: got %d want 2", groups[0].MemberCount)
	}
	if groups[1].MemberCount != 0 {
		t.Errorf("login-B member count: got %d want 0", groups[1].MemberCount)
	}
}

// ─── Membership add / remove idempotency ─────────────────────────────────────

func TestGroupMembership_AddIdempotent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("idempotent-group", "compute")
	_ = d.CreateNodeGroupFull(ctx, g)

	n := makeTestNode("node-idem", "aa:00:00:00:00:01")
	_ = d.CreateNodeConfig(ctx, n)

	// Add twice — should not error on second call.
	if err := d.AddGroupMember(ctx, g.ID, n.ID); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := d.AddGroupMember(ctx, g.ID, n.ID); err != nil {
		t.Fatalf("second add (idempotent): %v", err)
	}

	members, err := d.ListGroupMembers(ctx, g.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("member count: got %d want 1", len(members))
	}
}

func TestGroupMembership_Remove(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("remove-test", "gpu")
	_ = d.CreateNodeGroupFull(ctx, g)

	n1 := makeTestNode("node-rm-01", "aa:00:00:00:01:01")
	n2 := makeTestNode("node-rm-02", "aa:00:00:00:01:02")
	_ = d.CreateNodeConfig(ctx, n1)
	_ = d.CreateNodeConfig(ctx, n2)
	_ = d.AddGroupMember(ctx, g.ID, n1.ID)
	_ = d.AddGroupMember(ctx, g.ID, n2.ID)

	if err := d.RemoveGroupMember(ctx, g.ID, n1.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	members, _ := d.ListGroupMembers(ctx, g.ID)
	if len(members) != 1 {
		t.Errorf("after remove: got %d members want 1", len(members))
	}
	if members[0].ID != n2.ID {
		t.Errorf("remaining member: got %s want %s", members[0].ID, n2.ID)
	}
}

func TestGroupMembership_RemoveIdempotent(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("remove-idem", "admin")
	_ = d.CreateNodeGroupFull(ctx, g)

	n := makeTestNode("node-ri", "aa:00:00:00:02:01")
	_ = d.CreateNodeConfig(ctx, n)
	_ = d.AddGroupMember(ctx, g.ID, n.ID)

	// Remove twice — second call should not error.
	_ = d.RemoveGroupMember(ctx, g.ID, n.ID)
	if err := d.RemoveGroupMember(ctx, g.ID, n.ID); err != nil {
		t.Fatalf("second remove (idempotent): %v", err)
	}

	members, _ := d.ListGroupMembers(ctx, g.ID)
	if len(members) != 0 {
		t.Errorf("member count after double remove: got %d want 0", len(members))
	}
}

func TestGroupMembership_CascadeOnGroupDelete(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("cascade-group", "compute")
	_ = d.CreateNodeGroupFull(ctx, g)

	n := makeTestNode("node-cascade", "aa:00:00:00:03:01")
	_ = d.CreateNodeConfig(ctx, n)
	_ = d.AddGroupMember(ctx, g.ID, n.ID)

	// Delete the group — node should survive but membership should be gone.
	if err := d.DeleteNodeGroup(ctx, g.ID); err != nil {
		t.Fatalf("delete group: %v", err)
	}

	// The node still exists.
	surviving, err := d.GetNodeConfig(ctx, n.ID)
	if err != nil {
		t.Fatalf("node should survive group deletion: %v", err)
	}
	// group_id on node should be cleared.
	if surviving.GroupID != "" {
		t.Errorf("group_id on node should be cleared, got %q", surviving.GroupID)
	}
}

// ─── Group reimage job CRUD ───────────────────────────────────────────────────

func TestGroupReimageJob_CreateAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("rimg-group", "compute")
	_ = d.CreateNodeGroupFull(ctx, g)

	now := time.Now().UTC().Truncate(time.Second)
	job := db.GroupReimageJob{
		ID:                uuid.New().String(),
		GroupID:           g.ID,
		ImageID:           uuid.New().String(),
		Concurrency:       5,
		PauseOnFailurePct: 20,
		Status:            "running",
		TotalNodes:        3,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := d.CreateGroupReimageJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	got, err := d.GetGroupReimageJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.TotalNodes != 3 {
		t.Errorf("total_nodes: got %d want 3", got.TotalNodes)
	}
	if got.Status != "running" {
		t.Errorf("status: got %q want running", got.Status)
	}
}

func TestGroupReimageJob_Update(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("rimg-upd-group", "gpu")
	_ = d.CreateNodeGroupFull(ctx, g)

	now := time.Now().UTC().Truncate(time.Second)
	job := db.GroupReimageJob{
		ID:                uuid.New().String(),
		GroupID:           g.ID,
		ImageID:           uuid.New().String(),
		Concurrency:       2,
		PauseOnFailurePct: 50,
		Status:            "running",
		TotalNodes:        4,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	_ = d.CreateGroupReimageJob(ctx, job)

	job.SucceededNodes = 3
	job.FailedNodes = 1
	job.Status = "complete"
	if err := d.UpdateGroupReimageJob(ctx, job); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := d.GetGroupReimageJob(ctx, job.ID)
	if got.SucceededNodes != 3 {
		t.Errorf("succeeded_nodes: got %d", got.SucceededNodes)
	}
	if got.Status != "complete" {
		t.Errorf("status: got %q", got.Status)
	}
}

func TestGroupReimageJob_Resume(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	g := makeGroup("rimg-resume-group", "login")
	_ = d.CreateNodeGroupFull(ctx, g)

	now := time.Now().UTC().Truncate(time.Second)
	job := db.GroupReimageJob{
		ID:        uuid.New().String(),
		GroupID:   g.ID,
		ImageID:   uuid.New().String(),
		Status:    "paused",
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = d.CreateGroupReimageJob(ctx, job)

	if err := d.ResumeGroupReimageJob(ctx, job.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}

	got, _ := d.GetGroupReimageJob(ctx, job.ID)
	if got.Status != "running" {
		t.Errorf("status after resume: got %q want running", got.Status)
	}
}
