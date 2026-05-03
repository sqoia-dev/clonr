package main

import "testing"

func TestIsSafeVendor(t *testing.T) {
	tests := []struct {
		vendor string
		want   bool
	}{
		{"intel", true},
		{"dell", true},
		{"supermicro", true},
		{"", false},
		{"Intel", false},           // uppercase not allowed
		{"amiga", false},           // not in allowlist
		{"intel; rm -rf /", false}, // injection attempt
		{"dell dell", false},       // space
	}
	for _, tc := range tests {
		t.Run(tc.vendor, func(t *testing.T) {
			got := isSafeVendor(tc.vendor)
			if got != tc.want {
				t.Errorf("isSafeVendor(%q) = %v, want %v", tc.vendor, got, tc.want)
			}
		})
	}
}

func TestVerbBiosReadArgValidation(t *testing.T) {
	// No arguments — should fail.
	code := verbBiosRead(1000, nil)
	if code != 1 {
		t.Errorf("verbBiosRead(nil args): exit %d, want 1", code)
	}

	// Wrong arg count.
	code = verbBiosRead(1000, []string{"intel", "extra"})
	if code != 1 {
		t.Errorf("verbBiosRead(2 args): exit %d, want 1", code)
	}

	// Unknown vendor.
	code = verbBiosRead(1000, []string{"amiga"})
	if code != 1 {
		t.Errorf("verbBiosRead(unknown vendor): exit %d, want 1", code)
	}

	// Valid vendor but binary absent — should fail with code 1.
	code = verbBiosRead(1000, []string{"intel"})
	if code != 1 {
		t.Errorf("verbBiosRead(intel, absent binary): exit %d, want 1", code)
	}
}

func TestVerbBiosApplyArgValidation(t *testing.T) {
	// No arguments.
	code := verbBiosApply(1000, nil)
	if code != 1 {
		t.Errorf("verbBiosApply(nil args): exit %d, want 1", code)
	}

	// Unknown vendor.
	code = verbBiosApply(1000, []string{"amiga", "/var/lib/clustr/bios-staging/test.json"})
	if code != 1 {
		t.Errorf("verbBiosApply(unknown vendor): exit %d, want 1", code)
	}

	// Relative path (not absolute).
	code = verbBiosApply(1000, []string{"intel", "relative/path.json"})
	if code != 1 {
		t.Errorf("verbBiosApply(relative path): exit %d, want 1", code)
	}

	// Path not under biosStagingDir.
	code = verbBiosApply(1000, []string{"intel", "/etc/shadow"})
	if code != 1 {
		t.Errorf("verbBiosApply(wrong dir): exit %d, want 1", code)
	}

	// Path does not end in .json.
	code = verbBiosApply(1000, []string{"intel", "/var/lib/clustr/bios-staging/test.cfg"})
	if code != 1 {
		t.Errorf("verbBiosApply(non-.json): exit %d, want 1", code)
	}

	// Path traversal attempt.
	code = verbBiosApply(1000, []string{"intel", "/var/lib/clustr/bios-staging/../../../etc/shadow.json"})
	if code != 1 {
		t.Errorf("verbBiosApply(traversal): exit %d, want 1", code)
	}

	// Valid path but file absent — lstat fails.
	code = verbBiosApply(1000, []string{"intel", "/var/lib/clustr/bios-staging/nonexistent-abc.json"})
	if code != 2 {
		t.Errorf("verbBiosApply(absent file): exit %d, want 2", code)
	}
}
