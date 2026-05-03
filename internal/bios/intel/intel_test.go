package intel

import (
	"testing"

	"github.com/sqoia-dev/clustr/internal/bios"
)

func TestParseSyscfgSettings(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantN  int
		first  [2]string // [name, value] of first entry
	}{
		{
			name:  "empty output",
			input: "",
			wantN: 0,
		},
		{
			name: "simple key=value lines",
			input: `Intel(R) Hyper-Threading Technology=Enable
Power Performance Tuning=OS Controls EPB
Energy Efficient Turbo=Enable
`,
			wantN: 3,
			first: [2]string{"Intel(R) Hyper-Threading Technology", "Enable"},
		},
		{
			name: "blank lines and non-kv lines skipped",
			input: `SYSCFG Version 14.0
===
Intel(R) Hyper-Threading Technology=Enable

PCIe ASPM Support=Disable
`,
			wantN: 2,
			first: [2]string{"Intel(R) Hyper-Threading Technology", "Enable"},
		},
		{
			name: "value with equals sign",
			input: `Setting=value=with=equals`,
			wantN: 1,
			first: [2]string{"Setting", "value=with=equals"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSyscfgSettings(tc.input)
			if len(got) != tc.wantN {
				t.Fatalf("got %d settings, want %d: %+v", len(got), tc.wantN, got)
			}
			if tc.wantN > 0 {
				if got[0].Name != tc.first[0] {
					t.Errorf("first name: got %q, want %q", got[0].Name, tc.first[0])
				}
				if got[0].Value != tc.first[1] {
					t.Errorf("first value: got %q, want %q", got[0].Value, tc.first[1])
				}
			}
		})
	}
}

func TestIntelProviderVendor(t *testing.T) {
	p := &intelProvider{binPath: "/nonexistent/path"}
	if p.Vendor() != "intel" {
		t.Errorf("Vendor() = %q, want %q", p.Vendor(), "intel")
	}
}

func TestIntelProviderBinaryMissing(t *testing.T) {
	p := &intelProvider{binPath: "/nonexistent/path/syscfg"}

	ctx := t.Context()

	_, err := p.ReadCurrent(ctx)
	if err == nil {
		t.Fatal("ReadCurrent: expected ErrBinaryMissing, got nil")
	}

	// nil changes short-circuits before the binary check — nil error expected.
	_, err = p.Apply(ctx, nil)
	if err != nil {
		t.Fatalf("Apply(nil changes): expected nil error, got %v", err)
	}

	// Non-nil changes with absent binary should return ErrBinaryMissing.
	_, err = p.Apply(ctx, []bios.Change{{Setting: bios.Setting{Name: "HT", Value: "Disable"}, From: "Enable", To: "Disable"}})
	if err == nil {
		t.Fatal("Apply(non-nil changes) with absent binary: expected error, got nil")
	}

	supported, err := p.SupportedSettings(ctx)
	if err != nil {
		t.Fatalf("SupportedSettings with absent binary: expected nil error (non-fatal), got %v", err)
	}
	if supported != nil {
		t.Fatalf("SupportedSettings with absent binary: expected nil slice, got %v", supported)
	}
}
