// sysaccounts_test.go — unit tests for sysaccounts injection helpers (S1-5, TD-1).
// Tests cover pure functions and error types. The chroot/groupadd/useradd injection
// paths are skipped if the test is not running as root with a suitable rootfs.
package deploy

import (
	"strings"
	"testing"
)

// ─── parseGetentName ─────────────────────────────────────────────────────────

func TestParseGetentName_StandardLines(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"munge:x:1002:1002::/var/run/munge:/sbin/nologin", "munge"},
		{"slurm:x:995:995:Slurm Workload Manager:/var/lib/slurm:/sbin/nologin", "slurm"},
		{"root:x:0:0:root:/root:/bin/bash", "root"},
		{"nfs-nobody:x:65534:65534::/nonexistent:/usr/sbin/nologin", "nfs-nobody"},
		{"singlefield", "singlefield"},
		{"", ""},
	}

	for _, c := range cases {
		got := parseGetentName(c.line)
		if got != c.want {
			t.Errorf("parseGetentName(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestParseGetentName_GroupLine(t *testing.T) {
	// Group lines: groupname:x:GID:member1,member2
	cases := []struct {
		line string
		want string
	}{
		{"munge:x:1002:", "munge"},
		{"clustr-admins:x:9999:alice,bob", "clustr-admins"},
	}
	for _, c := range cases {
		got := parseGetentName(c.line)
		if got != c.want {
			t.Errorf("parseGetentName(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// ─── ErrUIDConflict ───────────────────────────────────────────────────────────

func TestErrUIDConflict_ErrorString(t *testing.T) {
	err := ErrUIDConflict{UID: 1001, Existing: "munge", Want: "slurm"}
	got := err.Error()
	if got == "" {
		t.Error("ErrUIDConflict.Error() returned empty string")
	}
	for _, substr := range []string{"1001", "munge", "slurm"} {
		if !contains(got, substr) {
			t.Errorf("error string %q missing %q", got, substr)
		}
	}
}

// ─── ErrGIDConflict ───────────────────────────────────────────────────────────

func TestErrGIDConflict_ErrorString(t *testing.T) {
	err := ErrGIDConflict{GID: 995, Existing: "slurm", Want: "munge"}
	got := err.Error()
	if got == "" {
		t.Error("ErrGIDConflict.Error() returned empty string")
	}
	for _, substr := range []string{"995", "slurm", "munge"} {
		if !contains(got, substr) {
			t.Errorf("error string %q missing %q", got, substr)
		}
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
