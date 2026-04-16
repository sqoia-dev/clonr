package reimage_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/internal/db"
	"github.com/sqoia-dev/clonr/internal/power"
	"github.com/sqoia-dev/clonr/internal/reimage"
)

// ─── Fake power providers ─────────────────────────────────────────────────────

// fakeProvider succeeds all operations.
type fakeProvider struct{}

func (f *fakeProvider) Name() string                               { return "fake" }
func (f *fakeProvider) Status(_ context.Context) (power.PowerStatus, error) { return power.PowerOn, nil }
func (f *fakeProvider) PowerOn(_ context.Context) error            { return nil }
func (f *fakeProvider) PowerOff(_ context.Context) error           { return nil }
func (f *fakeProvider) PowerCycle(_ context.Context) error         { return nil }
func (f *fakeProvider) Reset(_ context.Context) error              { return nil }
func (f *fakeProvider) SetNextBoot(_ context.Context, _ power.BootDevice) error              { return nil }
func (f *fakeProvider) SetPersistentBootOrder(_ context.Context, _ []power.BootDevice) error { return nil }

// failProvider always fails PowerCycle.
type failProvider struct{}

var errPowerFailed = fmt.Errorf("simulated power failure")

func (f *failProvider) Name() string                               { return "fail" }
func (f *failProvider) Status(_ context.Context) (power.PowerStatus, error) { return power.PowerUnknown, errPowerFailed }
func (f *failProvider) PowerOn(_ context.Context) error            { return errPowerFailed }
func (f *failProvider) PowerOff(_ context.Context) error           { return errPowerFailed }
func (f *failProvider) PowerCycle(_ context.Context) error         { return errPowerFailed }
func (f *failProvider) Reset(_ context.Context) error              { return errPowerFailed }
func (f *failProvider) SetNextBoot(_ context.Context, _ power.BootDevice) error              { return errPowerFailed }
func (f *failProvider) SetPersistentBootOrder(_ context.Context, _ []power.BootDevice) error { return errPowerFailed }

// ─── Test helpers ─────────────────────────────────────────────────────────────

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newOrchestratorWithFake(t *testing.T, database *db.DB, providerType string, prov power.Provider) *reimage.Orchestrator {
	t.Helper()
	reg := power.NewRegistry()
	reg.Register(providerType, func(_ power.ProviderConfig) (power.Provider, error) {
		return prov, nil
	})
	return reimage.New(database, reg, zerolog.Nop())
}

func makeTestImage(t *testing.T, d *db.DB) api.BaseImage {
	t.Helper()
	img := api.BaseImage{
		ID: uuid.New().String(), Name: "test-img", Version: "1.0",
		OS: "Rocky", Arch: "x86_64", Status: api.ImageStatusReady,
		Format: api.ImageFormatFilesystem, DiskLayout: api.DiskLayout{},
		Tags: []string{}, CreatedAt: time.Now().UTC(),
	}
	if err := d.CreateBaseImage(context.Background(), img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	return img
}

func makeTestGroup(t *testing.T, d *db.DB, name string) api.NodeGroup {
	t.Helper()
	g := api.NodeGroup{
		ID: uuid.New().String(), Name: name, Role: "compute",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := d.CreateNodeGroupFull(context.Background(), g); err != nil {
		t.Fatalf("create group: %v", err)
	}
	return g
}

func makeGroupNode(t *testing.T, d *db.DB, hostname, mac, groupID, imageID, provType string) api.NodeConfig {
	t.Helper()
	now := time.Now().UTC()
	n := api.NodeConfig{
		ID:          uuid.New().String(),
		Hostname:    hostname,
		PrimaryMAC:  mac,
		Interfaces:  []api.InterfaceConfig{},
		SSHKeys:     []string{},
		Groups:      []string{},
		CustomVars:  map[string]string{},
		BaseImageID: imageID,
		GroupID:     groupID,
		PowerProvider: &api.PowerProviderConfig{
			Type:   provType,
			Fields: map[string]string{},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := d.CreateNodeConfig(context.Background(), n); err != nil {
		t.Fatalf("create node %s: %v", hostname, err)
	}
	return n
}

func addMember(t *testing.T, d *db.DB, groupID, nodeID string) {
	t.Helper()
	if err := d.AddGroupMember(context.Background(), groupID, nodeID); err != nil {
		t.Fatalf("add member: %v", err)
	}
}

// pollJobUntilTerminal polls GetGroupReimageJob until status is one of the
// terminal states or the 10-second deadline is exceeded.
func pollJobUntilTerminal(t *testing.T, d *db.DB, jobID string) db.GroupReimageJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := d.GetGroupReimageJob(context.Background(), jobID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		switch job.Status {
		case "complete", "failed", "paused":
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout waiting for group reimage job to reach terminal state")
	return db.GroupReimageJob{}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestGroupReimage_DispatchesConcurrent verifies all nodes get triggered.
func TestGroupReimage_DispatchesConcurrent(t *testing.T) {
	d := openTestDB(t)
	orch := newOrchestratorWithFake(t, d, "fake", &fakeProvider{})
	img := makeTestImage(t, d)
	g := makeTestGroup(t, d, "concurrent-group")

	const nodeCount = 4
	for i := 0; i < nodeCount; i++ {
		hostname := fmt.Sprintf("node-%02d", i)
		mac := fmt.Sprintf("aa:bb:cc:dd:ee:%02x", i+1)
		n := makeGroupNode(t, d, hostname, mac, g.ID, img.ID, "fake")
		addMember(t, d, g.ID, n.ID)
	}

	jobID, err := orch.TriggerGroupReimage(context.Background(), g.ID, img.ID, 2, 50)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	job := pollJobUntilTerminal(t, d, jobID)
	t.Logf("job: status=%s triggered=%d succeeded=%d failed=%d",
		job.Status, job.TriggeredNodes, job.SucceededNodes, job.FailedNodes)

	if job.TriggeredNodes != nodeCount {
		t.Errorf("triggered_nodes: got %d want %d", job.TriggeredNodes, nodeCount)
	}
	if job.Status != "complete" {
		t.Errorf("status: got %q want complete", job.Status)
	}
}

// TestGroupReimage_PauseOnFailureThreshold verifies that exceeding the failure
// percentage causes the job to pause rather than continue dispatching.
func TestGroupReimage_PauseOnFailureThreshold(t *testing.T) {
	d := openTestDB(t)
	orch := newOrchestratorWithFake(t, d, "fail", &failProvider{})
	img := makeTestImage(t, d)
	g := makeTestGroup(t, d, "pause-group")

	// Two nodes — first wave of 2 will both fail.
	for i := 0; i < 2; i++ {
		hostname := fmt.Sprintf("failnode-%02d", i)
		mac := fmt.Sprintf("ff:bb:cc:dd:ee:%02x", i+1)
		n := makeGroupNode(t, d, hostname, mac, g.ID, img.ID, "fail")
		addMember(t, d, g.ID, n.ID)
	}

	// 0% threshold: any failure should pause.
	jobID, err := orch.TriggerGroupReimage(context.Background(), g.ID, img.ID, 2, 0)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	job := pollJobUntilTerminal(t, d, jobID)
	t.Logf("job: status=%s failed=%d", job.Status, job.FailedNodes)

	// With a failing provider and 0% threshold, job must be paused or failed.
	if job.Status != "paused" && job.Status != "failed" {
		t.Errorf("expected paused or failed, got %q", job.Status)
	}
	if job.FailedNodes == 0 {
		t.Error("expected at least one failed node")
	}
}

// TestGroupReimage_EmptyGroupReturnsError verifies that reimaging an empty group
// returns an error immediately without creating a job.
func TestGroupReimage_EmptyGroupReturnsError(t *testing.T) {
	d := openTestDB(t)
	orch := newOrchestratorWithFake(t, d, "fake", &fakeProvider{})
	img := makeTestImage(t, d)
	g := makeTestGroup(t, d, "empty-group")

	_, err := orch.TriggerGroupReimage(context.Background(), g.ID, img.ID, 5, 20)
	if err == nil {
		t.Error("expected error for empty group, got nil")
	}
}

// TestGroupReimage_WaitsAndContinues verifies that a multi-wave dispatch
// (more nodes than concurrency) completes all nodes.
func TestGroupReimage_WaitsAndContinues(t *testing.T) {
	d := openTestDB(t)
	orch := newOrchestratorWithFake(t, d, "fake", &fakeProvider{})
	img := makeTestImage(t, d)
	g := makeTestGroup(t, d, "multi-wave-group")

	const nodeCount = 6
	for i := 0; i < nodeCount; i++ {
		hostname := fmt.Sprintf("wave-node-%02d", i)
		mac := fmt.Sprintf("bb:cc:dd:ee:ff:%02x", i+1)
		n := makeGroupNode(t, d, hostname, mac, g.ID, img.ID, "fake")
		addMember(t, d, g.ID, n.ID)
	}

	// Concurrency=2 → 3 waves of 2. All should complete.
	jobID, err := orch.TriggerGroupReimage(context.Background(), g.ID, img.ID, 2, 50)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	job := pollJobUntilTerminal(t, d, jobID)
	if job.Status != "complete" {
		t.Errorf("status: got %q want complete", job.Status)
	}
	if job.TriggeredNodes != nodeCount {
		t.Errorf("triggered_nodes: got %d want %d", job.TriggeredNodes, nodeCount)
	}
}
