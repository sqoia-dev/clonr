package external

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/ipmi"
)

// fakeFreeIPMIRunner satisfies ipmi.FreeIPMIRunner for tests.
type fakeFreeIPMIRunner struct {
	out  string
	err  error
	last []string
}

func (f *fakeFreeIPMIRunner) Run(ctx context.Context, argv ...string) (string, error) {
	f.last = argv
	return f.out, f.err
}

func TestBMCCollectArgv(t *testing.T) {
	t.Parallel()
	got := BMCCollectArgv("192.168.1.50", "ADMIN", "secret")
	// We only assert the binary + the non-secret credential markers;
	// the exact flag list is owned by internal/ipmi.SensorsArgv
	// (covered by its own unit tests).
	if len(got) == 0 {
		t.Fatal("BMCCollectArgv: empty argv")
	}
	if got[0] != "ipmi-sensors" {
		t.Fatalf("BMCCollectArgv[0] = %q, want ipmi-sensors", got[0])
	}
	if !containsArgv(got, "192.168.1.50") {
		t.Fatalf("BMCCollectArgv missing host: %v", got)
	}
	if !containsArgv(got, "ADMIN") {
		t.Fatalf("BMCCollectArgv missing user: %v", got)
	}

	// SECURITY INVARIANT: the BMC password MUST NEVER appear on argv.
	// /proc/<pid>/cmdline is world-readable on Linux, so any local
	// user could observe a -p <password> substring during the
	// freeipmi process lifetime.  The password is delivered to
	// freeipmi out-of-band via a 0600 temp file referenced by
	// --password-file=<path> (see internal/ipmi.runWithPassword).
	// BMCCollectArgv is the password-free canonical-shape builder
	// used for logging / inspection — keep it that way.
	if containsArgv(got, "secret") {
		t.Fatalf("BMCCollectArgv leaked password into argv: %v", got)
	}
	if containsArgv(got, "-p") {
		t.Fatalf("BMCCollectArgv contains -p flag (password leak risk): %v", got)
	}
	for _, a := range got {
		if strings.Contains(a, "secret") {
			t.Fatalf("BMCCollectArgv leaked password substring in arg %q: %v", a, got)
		}
		// --password-file is the out-of-band channel and is appended
		// at exec time by runWithPassword, NOT by the argv builder.
		// Catch any future regression that puts it on the canonical
		// shape (which would defeat the safe-to-log property).
		if strings.HasPrefix(a, "--password-file") {
			t.Fatalf("BMCCollectArgv must not include --password-file (added at exec time): %v", got)
		}
	}

	// Empty addr → nil argv (the collector uses this to short-circuit
	// before invoking the runner).
	if got := BMCCollectArgv("", "u", "p"); got != nil {
		t.Fatalf("BMCCollectArgv with empty addr: got %v, want nil", got)
	}
}

func TestBMCCollect_RunnerError_PopulatesErrorField(t *testing.T) {
	t.Parallel()
	r := &fakeFreeIPMIRunner{err: errors.New("connection refused")}
	c := &BMCCollector{Runner: r}
	pl := c.Collect(context.Background(), "192.168.1.50", "ADMIN", "secret")
	if pl.Error == "" {
		t.Fatal("Collect: expected non-empty Error on runner failure")
	}
	if !strings.Contains(pl.Error, "connection refused") {
		t.Fatalf("Collect Error: %q does not mention runner failure", pl.Error)
	}
	if len(pl.Sensors) != 0 {
		t.Fatalf("Collect: Sensors should be empty on failure, got %v", pl.Sensors)
	}
	if pl.CollectedAt.IsZero() {
		t.Fatal("Collect: CollectedAt should be set even on failure")
	}
	if pl.Source != "ipmi-sensors" {
		t.Fatalf("Collect: Source = %q, want ipmi-sensors", pl.Source)
	}
}

func TestBMCCollect_NoAddr_ErrorOnly(t *testing.T) {
	t.Parallel()
	c := &BMCCollector{}
	pl := c.Collect(context.Background(), "", "u", "p")
	if pl.Error == "" {
		t.Fatal("Collect with empty addr: expected Error to be set")
	}
}

func TestBMCCollect_ParsesSensorOutput(t *testing.T) {
	t.Parallel()
	// Replicates the freeipmi --comma-separated-output --output-sensor-state
	// shape: ID,Name,Type,State,Reading,Units,Event
	csv := "1,CPU1 Temp,Temperature,Nominal,42.00,C,'OK'\n" +
		"2,Fan1,Fan,Nominal,3500.00,RPM,'OK'\n"
	r := &fakeFreeIPMIRunner{out: csv}
	c := &BMCCollector{Runner: r}
	pl := c.Collect(context.Background(), "192.168.1.50", "u", "p")
	if pl.Error != "" {
		t.Fatalf("Collect: unexpected Error: %q", pl.Error)
	}
	cpu, ok := pl.Sensors["CPU1 Temp"]
	if !ok {
		t.Fatalf("Collect: missing CPU1 Temp sensor; got: %v", pl.Sensors)
	}
	if cpu.Value != "42.00" || cpu.Unit != "C" {
		t.Fatalf("Collect CPU1 Temp: got value=%q unit=%q, want 42.00/C", cpu.Value, cpu.Unit)
	}
	fan, ok := pl.Sensors["Fan1"]
	if !ok {
		t.Fatal("Collect: missing Fan1")
	}
	if fan.Unit != "RPM" {
		t.Fatalf("Collect Fan1: unit %q, want RPM", fan.Unit)
	}
	// Sanity: argv passed to runner targets the right host.
	if r.last == nil {
		t.Fatal("Collect: runner was not called")
	}
	if !containsArgv(r.last, "192.168.1.50") {
		t.Fatalf("Collect: argv missing host: %v", r.last)
	}
}

// Ensure the collector type satisfies the package-level expectations.
var _ ipmi.FreeIPMIRunner = (*fakeFreeIPMIRunner)(nil)
