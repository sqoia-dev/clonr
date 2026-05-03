package dell

import (
	"errors"
	"testing"

	"github.com/sqoia-dev/clustr/internal/bios"
)

// ─── parseRacadmSettings ────────────────────────────────────────────────────

func TestParseRacadmSettings_Empty(t *testing.T) {
	got := parseRacadmSettings("")
	if len(got) != 0 {
		t.Fatalf("got %d settings, want 0", len(got))
	}
}

func TestParseRacadmSettings_SimpleKeyValue(t *testing.T) {
	input := `BIOS.ProcSettings.LogicalProc=Enabled
BIOS.SysSecurity.SecureBoot=Disabled
BIOS.BootSettings.BootMode=Uefi
`
	got := parseRacadmSettings(input)
	if len(got) != 3 {
		t.Fatalf("got %d settings, want 3: %+v", len(got), got)
	}
	if got[0].Name != "BIOS.ProcSettings.LogicalProc" || got[0].Value != "Enabled" {
		t.Errorf("got[0] = %+v, want Name=BIOS.ProcSettings.LogicalProc Value=Enabled", got[0])
	}
	if got[2].Name != "BIOS.BootSettings.BootMode" || got[2].Value != "Uefi" {
		t.Errorf("got[2] = %+v, want Name=BIOS.BootSettings.BootMode Value=Uefi", got[2])
	}
}

func TestParseRacadmSettings_SectionHeadersSkipped(t *testing.T) {
	input := `[Key=value]
[BIOS]
BIOS.ProcSettings.LogicalProc=Enabled
[NIC.Slot.1-1]
Attribute=Value
`
	got := parseRacadmSettings(input)
	if len(got) != 2 {
		t.Fatalf("got %d settings, want 2: %+v", len(got), got)
	}
	if got[0].Name != "BIOS.ProcSettings.LogicalProc" {
		t.Errorf("got[0].Name = %q, want BIOS.ProcSettings.LogicalProc", got[0].Name)
	}
}

func TestParseRacadmSettings_CommentsAndBlanksSkipped(t *testing.T) {
	input := `# This is a comment
BIOS.SysProfileSettings.SysProfile=PerfOptimized

# Another comment
BIOS.ProcSettings.ProcVirtualization=Enabled
`
	got := parseRacadmSettings(input)
	if len(got) != 2 {
		t.Fatalf("got %d settings, want 2: %+v", len(got), got)
	}
}

func TestParseRacadmSettings_ValueWithEquals(t *testing.T) {
	input := `Key=value=with=equals`
	got := parseRacadmSettings(input)
	if len(got) != 1 {
		t.Fatalf("got %d settings, want 1", len(got))
	}
	if got[0].Name != "Key" || got[0].Value != "value=with=equals" {
		t.Errorf("got %+v, want Name=Key Value=value=with=equals", got[0])
	}
}

func TestParseRacadmSettings_WhitespaceTrimmed(t *testing.T) {
	input := `  BIOS.ProcSettings.LogicalProc  =  Enabled  `
	got := parseRacadmSettings(input)
	if len(got) != 1 {
		t.Fatalf("got %d settings, want 1", len(got))
	}
	if got[0].Name != "BIOS.ProcSettings.LogicalProc" {
		t.Errorf("Name not trimmed: got %q", got[0].Name)
	}
	if got[0].Value != "Enabled" {
		t.Errorf("Value not trimmed: got %q", got[0].Value)
	}
}

func TestParseRacadmSettings_Fixture(t *testing.T) {
	// Realistic racadm get BIOS.SetupConfig fixture.
	fixture := `[Key=Value]
[BIOS]
# Dell PowerEdge R640 — BIOS.SetupConfig
BIOS.SysProfileSettings.SysProfile=PerfOptimized
BIOS.ProcSettings.LogicalProc=Enabled
BIOS.ProcSettings.ProcVirtualization=Enabled
BIOS.ProcSettings.NumaNodesPerSocket=1
BIOS.IntegratedDevices.IoAt=Disabled
BIOS.SysSecurity.SecureBoot=Disabled
BIOS.BootSettings.BootMode=Uefi
`
	got := parseRacadmSettings(fixture)
	if len(got) != 7 {
		t.Fatalf("got %d settings, want 7: %+v", len(got), got)
	}

	want := map[string]string{
		"BIOS.SysProfileSettings.SysProfile": "PerfOptimized",
		"BIOS.ProcSettings.LogicalProc":      "Enabled",
		"BIOS.BootSettings.BootMode":         "Uefi",
	}
	idx := make(map[string]string, len(got))
	for _, s := range got {
		idx[s.Name] = s.Value
	}
	for k, v := range want {
		if got := idx[k]; got != v {
			t.Errorf("%s: got %q, want %q", k, got, v)
		}
	}
}

// ─── dellProvider method tests ──────────────────────────────────────────────

func TestDellProviderVendor(t *testing.T) {
	p := &dellProvider{binPath: "/nonexistent/racadm"}
	if p.Vendor() != "dell" {
		t.Errorf("Vendor() = %q, want dell", p.Vendor())
	}
}

func TestDellProviderBinaryMissing(t *testing.T) {
	p := &dellProvider{binPath: "/nonexistent/path/racadm"}
	ctx := t.Context()

	_, err := p.ReadCurrent(ctx)
	if err == nil {
		t.Fatal("ReadCurrent: expected error when binary missing, got nil")
	}
	if !errors.Is(err, bios.ErrBinaryMissing) {
		t.Errorf("ReadCurrent: got %v, want to wrap ErrBinaryMissing", err)
	}

	// nil changes short-circuits before binary check — nil error expected.
	_, err = p.Apply(ctx, nil)
	if err != nil {
		t.Fatalf("Apply(nil changes): expected nil error, got %v", err)
	}

	// Non-nil changes with absent binary should return ErrBinaryMissing.
	_, err = p.Apply(ctx, []bios.Change{
		{Setting: bios.Setting{Name: "BIOS.ProcSettings.LogicalProc", Value: "Disabled"}, From: "Enabled", To: "Disabled"},
	})
	if err == nil {
		t.Fatal("Apply(non-nil changes) with absent binary: expected error, got nil")
	}
	if !errors.Is(err, bios.ErrBinaryMissing) {
		t.Errorf("Apply: got %v, want to wrap ErrBinaryMissing", err)
	}

	supported, err := p.SupportedSettings(ctx)
	if err != nil {
		t.Fatalf("SupportedSettings with absent binary: expected nil error (non-fatal), got %v", err)
	}
	if supported != nil {
		t.Fatalf("SupportedSettings with absent binary: expected nil slice, got %v", supported)
	}
}

// ─── Diff ───────────────────────────────────────────────────────────────────

func TestDellProviderDiff(t *testing.T) {
	p := &dellProvider{binPath: "/nonexistent/racadm"}

	current := []bios.Setting{
		{Name: "BIOS.ProcSettings.LogicalProc", Value: "Enabled"},
		{Name: "BIOS.SysSecurity.SecureBoot", Value: "Disabled"},
		{Name: "BIOS.BootSettings.BootMode", Value: "Uefi"},
	}
	desired := []bios.Setting{
		{Name: "BIOS.ProcSettings.LogicalProc", Value: "Disabled"}, // change
		{Name: "BIOS.SysSecurity.SecureBoot", Value: "Disabled"},   // no change
		{Name: "BIOS.SysProfileSettings.SysProfile", Value: "PerfOptimized"}, // new
	}

	changes, err := p.Diff(desired, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("Diff returned %d changes, want 2: %+v", len(changes), changes)
	}

	// Verify the two expected changes are present.
	idx := make(map[string]bios.Change, len(changes))
	for _, c := range changes {
		idx[c.Name] = c
	}

	if c, ok := idx["BIOS.ProcSettings.LogicalProc"]; !ok {
		t.Error("expected change for LogicalProc, not found")
	} else {
		if c.From != "Enabled" || c.To != "Disabled" {
			t.Errorf("LogicalProc change: got From=%q To=%q, want From=Enabled To=Disabled", c.From, c.To)
		}
	}

	if c, ok := idx["BIOS.SysProfileSettings.SysProfile"]; !ok {
		t.Error("expected change for SysProfile (new setting), not found")
	} else {
		if c.From != "" || c.To != "PerfOptimized" {
			t.Errorf("SysProfile change: got From=%q To=%q, want From= To=PerfOptimized", c.From, c.To)
		}
	}
}

func TestDellProviderDiffNoDrift(t *testing.T) {
	p := &dellProvider{binPath: "/nonexistent/racadm"}

	current := []bios.Setting{
		{Name: "BIOS.ProcSettings.LogicalProc", Value: "Enabled"},
	}
	desired := []bios.Setting{
		{Name: "BIOS.ProcSettings.LogicalProc", Value: "Enabled"},
	}

	changes, err := p.Diff(desired, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("Diff: got %d changes for identical settings, want 0: %+v", len(changes), changes)
	}
}

func TestDellProviderDiffCaseInsensitiveName(t *testing.T) {
	p := &dellProvider{binPath: "/nonexistent/racadm"}

	current := []bios.Setting{
		{Name: "BIOS.ProcSettings.LogicalProc", Value: "Enabled"},
	}
	desired := []bios.Setting{
		{Name: "bios.procsettings.logicalproc", Value: "Enabled"},
	}

	changes, err := p.Diff(desired, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("Diff: case-insensitive name match produced %d changes, want 0: %+v", len(changes), changes)
	}
}
