// manager_test.go — unit tests for sysaccounts Manager: validation, CRUD, conflict
// detection. Uses a real SQLite DB via the db test helper (S1-5, TD-1).
package sysaccounts_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/sysaccounts"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── DB helpers ──────────────────────────────────────────────────────────────

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newManager(t *testing.T) *sysaccounts.Manager {
	t.Helper()
	return sysaccounts.New(openTestDB(t), nil) // nil allocator: tests specify UIDs explicitly
}

// ─── Group CRUD ───────────────────────────────────────────────────────────────

func TestCreateGroup_Success(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	g, err := m.CreateGroup(ctx, api.SystemGroup{
		Name:        "munge",
		GID:         1001,
		Description: "Munge daemon group",
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.ID == "" {
		t.Error("created group has empty ID")
	}
	if g.Name != "munge" {
		t.Errorf("Name = %q, want munge", g.Name)
	}
	if g.GID != 1001 {
		t.Errorf("GID = %d, want 1001", g.GID)
	}
}

func TestCreateGroup_DuplicateNameConflict(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	if _, err := m.CreateGroup(ctx, api.SystemGroup{Name: "slurm", GID: 1002}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := m.CreateGroup(ctx, api.SystemGroup{Name: "slurm", GID: 1003})
	if err == nil {
		t.Fatal("expected ErrConflict for duplicate group name, got nil")
	}
	if !errors.Is(err, sysaccounts.ErrConflict) {
		t.Errorf("expected ErrConflict, got: %v", err)
	}
}

func TestCreateGroup_DuplicateGIDConflict(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	if _, err := m.CreateGroup(ctx, api.SystemGroup{Name: "munge", GID: 900}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := m.CreateGroup(ctx, api.SystemGroup{Name: "nfs", GID: 900})
	if err == nil {
		t.Fatal("expected ErrConflict for duplicate GID, got nil")
	}
	if !errors.Is(err, sysaccounts.ErrConflict) {
		t.Errorf("expected ErrConflict, got: %v", err)
	}
}

func TestCreateGroup_InvalidName(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	cases := []struct {
		name string
		gid  int
	}{
		{"", 100},                  // empty name
		{"UPPERCASE", 101},         // uppercase not allowed
		{"has space", 102},         // spaces not allowed
		{"123start", 103},          // must start with [a-z_]
		{strings.Repeat("a", 33), 104}, // too long
	}

	for _, c := range cases {
		_, err := m.CreateGroup(ctx, api.SystemGroup{Name: c.name, GID: c.gid})
		if err == nil {
			t.Errorf("expected error for name=%q gid=%d, got nil", c.name, c.gid)
		}
	}
}

func TestCreateGroup_GIDOutOfRange(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	_, err := m.CreateGroup(ctx, api.SystemGroup{Name: "test", GID: 0})
	if err == nil {
		t.Error("expected error for GID=0")
	}
	_, err = m.CreateGroup(ctx, api.SystemGroup{Name: "test2", GID: 65535})
	if err == nil {
		t.Error("expected error for GID=65535")
	}
}

func TestDeleteGroup_BlockedByAccountReference(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	grp, err := m.CreateGroup(ctx, api.SystemGroup{Name: "munge", GID: 1001})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if _, err := m.CreateAccount(ctx, api.SystemAccount{
		Username:   "munge",
		UID:        1001,
		PrimaryGID: 1001,
		Shell:      "/sbin/nologin",
		HomeDir:    "/var/run/munge",
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}

	// Deleting the group while an account references its GID should return ErrConflict.
	err = m.DeleteGroup(ctx, grp.ID)
	if err == nil {
		t.Fatal("expected ErrConflict when deleting group with dependent account")
	}
	if !errors.Is(err, sysaccounts.ErrConflict) {
		t.Errorf("expected ErrConflict, got: %v", err)
	}
}

// ─── Account CRUD ─────────────────────────────────────────────────────────────

func TestCreateAccount_Success(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	// Groups must exist for GID references to be meaningful; the DB layer does
	// not enforce FK on primary_gid, but good practice to create it first.
	if _, err := m.CreateGroup(ctx, api.SystemGroup{Name: "slurm", GID: 995}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	a, err := m.CreateAccount(ctx, api.SystemAccount{
		Username:      "slurm",
		UID:           995,
		PrimaryGID:    995,
		Shell:         "/sbin/nologin",
		HomeDir:       "/var/lib/slurm",
		SystemAccount: true,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if a.ID == "" {
		t.Error("created account has empty ID")
	}
	if a.Username != "slurm" {
		t.Errorf("Username = %q, want slurm", a.Username)
	}
}

func TestCreateAccount_DuplicateUsernameConflict(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	input := api.SystemAccount{Username: "munge", UID: 996, PrimaryGID: 996, Shell: "/sbin/nologin", HomeDir: "/dev/null"}
	if _, err := m.CreateAccount(ctx, input); err != nil {
		t.Fatalf("first create: %v", err)
	}
	input.UID = 997 // different UID, same name
	_, err := m.CreateAccount(ctx, input)
	if err == nil {
		t.Fatal("expected ErrConflict for duplicate username, got nil")
	}
	if !errors.Is(err, sysaccounts.ErrConflict) {
		t.Errorf("expected ErrConflict, got: %v", err)
	}
}

func TestCreateAccount_DuplicateUIDConflict(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	if _, err := m.CreateAccount(ctx, api.SystemAccount{Username: "munge", UID: 500, PrimaryGID: 500, Shell: "/sbin/nologin", HomeDir: "/dev/null"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := m.CreateAccount(ctx, api.SystemAccount{Username: "nfs", UID: 500, PrimaryGID: 501, Shell: "/sbin/nologin", HomeDir: "/dev/null"})
	if err == nil {
		t.Fatal("expected ErrConflict for duplicate UID, got nil")
	}
	if !errors.Is(err, sysaccounts.ErrConflict) {
		t.Errorf("expected ErrConflict, got: %v", err)
	}
}

func TestCreateAccount_InvalidUsername(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	cases := []string{"", "CAPITAL", "has space", "123start"}
	for i, name := range cases {
		_, err := m.CreateAccount(ctx, api.SystemAccount{
			Username: name, UID: 600 + i, PrimaryGID: 600, Shell: "/sbin/nologin", HomeDir: "/dev/null",
		})
		if err == nil {
			t.Errorf("expected error for username=%q, got nil", name)
		}
	}
}

func TestCreateAccount_InvalidShellPath(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	_, err := m.CreateAccount(ctx, api.SystemAccount{
		Username: "badshell", UID: 700, PrimaryGID: 700, Shell: "nologin", HomeDir: "/dev/null",
	})
	if err == nil {
		t.Error("expected error for relative shell path, got nil")
	}
}

// ─── EnsureDefaults ───────────────────────────────────────────────────────────

func TestEnsureDefaults_FillsShellAndHomeDir(t *testing.T) {
	a := &api.SystemAccount{Username: "test"}
	sysaccounts.EnsureDefaults(a)
	if a.Shell != "/sbin/nologin" {
		t.Errorf("Shell = %q, want /sbin/nologin", a.Shell)
	}
	if a.HomeDir != "/dev/null" {
		t.Errorf("HomeDir = %q, want /dev/null", a.HomeDir)
	}
}

func TestEnsureDefaults_DoesNotOverrideExisting(t *testing.T) {
	a := &api.SystemAccount{Username: "test", Shell: "/bin/bash", HomeDir: "/home/test"}
	sysaccounts.EnsureDefaults(a)
	if a.Shell != "/bin/bash" {
		t.Errorf("Shell overridden, got %q", a.Shell)
	}
	if a.HomeDir != "/home/test" {
		t.Errorf("HomeDir overridden, got %q", a.HomeDir)
	}
}

// ─── NodeConfig ───────────────────────────────────────────────────────────────

func TestNodeConfig_NilWhenEmpty(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	cfg, err := m.NodeConfig(ctx)
	if err != nil {
		t.Fatalf("NodeConfig: %v", err)
	}
	if cfg != nil {
		t.Errorf("NodeConfig should be nil when no accounts/groups defined, got %+v", cfg)
	}
}

func TestNodeConfig_NonNilWhenPopulated(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)

	if _, err := m.CreateGroup(ctx, api.SystemGroup{Name: "munge", GID: 1001}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	cfg, err := m.NodeConfig(ctx)
	if err != nil {
		t.Fatalf("NodeConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("NodeConfig returned nil, want non-nil after group creation")
	}
	if len(cfg.Groups) != 1 {
		t.Errorf("Groups count = %d, want 1", len(cfg.Groups))
	}
}
