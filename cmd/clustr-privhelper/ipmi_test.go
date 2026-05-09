package main

import (
	"os"
	"strings"
	"testing"
)

// ─── Argv composition ─────────────────────────────────────────────────────────

// TestCommonIPMIArgs_Remote_NoPasswordOnArgv asserts the freeipmi argv does
// NOT carry the BMC password.  /proc/<pid>/cmdline is world-readable while
// the helper runs, so any local user could observe a -p <password>
// substring.  Codex post-ship review (issue 1) flagged the previous
// argv-based path; the password now lives in a 0600 temp file passed via
// -f, populated by writePasswordFile.
func TestCommonIPMIArgs_Remote_NoPasswordOnArgv(t *testing.T) {
	args := commonIPMIArgs(ipmiCredentials{Host: "10.0.0.5", Username: "admin", Password: "s3cret"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-h 10.0.0.5", "-u admin", "--driver-type=LAN_2_0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	// Critical: the password must NEVER appear on argv.  Two ways the bug
	// could regress: a literal -p flag, or the password substring
	// anywhere in the joined argv string.
	for _, banned := range []string{"-p", "s3cret"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("password leaked via argv: %q present in %s", banned, joined)
		}
	}
}

func TestCommonIPMIArgs_LocalEmpty(t *testing.T) {
	args := commonIPMIArgs(ipmiCredentials{})
	if len(args) != 0 {
		t.Errorf("local creds should produce empty argv, got %v", args)
	}
}

// TestWritePasswordFile_Roundtrip verifies the 0600 password file is the
// out-of-band channel for the BMC password.  Empty password produces an
// empty path (caller skips -f).
func TestWritePasswordFile_Roundtrip(t *testing.T) {
	path, err := writePasswordFile("s3cret")
	if err != nil {
		t.Fatalf("writePasswordFile: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path for non-empty password")
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "s3cret" {
		t.Errorf("contents = %q, want %q", got, "s3cret")
	}

	emptyPath, err := writePasswordFile("")
	if err != nil {
		t.Fatalf("writePasswordFile(empty): %v", err)
	}
	if emptyPath != "" {
		t.Errorf("empty password should yield empty path, got %q", emptyPath)
	}
}

// TestAppendPasswordFlag verifies the -f flag is appended only when a
// password file is present.
func TestAppendPasswordFlag(t *testing.T) {
	got := appendPasswordFlag([]string{"-h", "x"}, "")
	if len(got) != 2 {
		t.Errorf("empty pwPath should not append; got %v", got)
	}
	got = appendPasswordFlag([]string{"-h", "x"}, "/tmp/pw")
	if len(got) != 4 || got[2] != "-f" || got[3] != "/tmp/pw" {
		t.Errorf("expected [-h x -f /tmp/pw], got %v", got)
	}
}

// ─── Field validation ─────────────────────────────────────────────────────────

func TestIsSafeBMCField(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"", true},
		{"10.0.0.5", true},
		{"bmc.example.com", true},
		{"admin", true},
		{"user_name-1.2", true},
		{"v:1@host", true},
		{"a b", false},
		{"`whoami`", false},
		{"$(id)", false},
		{"; rm -rf /", false},
		{strings.Repeat("a", 257), false},
	}
	for _, tc := range tests {
		t.Run(tc.v, func(t *testing.T) {
			if got := isSafeBMCField(tc.v); got != tc.want {
				t.Errorf("isSafeBMCField(%q) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

func TestIsSafeChannel(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"1", true},
		{"7", true},
		{"16", true},
		{"0", false},
		{"17", false},
		{"", false},
		{"a", false},
		{"-1", false},
		{"100", false},
	}
	for _, tc := range tests {
		t.Run(tc.v, func(t *testing.T) {
			if got := isSafeChannel(tc.v); got != tc.want {
				t.Errorf("isSafeChannel(%q) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

func TestValidateCreds_Reject(t *testing.T) {
	bad := ipmiCredentials{Host: "10.0.0.5; rm -rf /", Username: "admin"}
	if err := validateCreds(bad); err == nil {
		t.Fatal("expected validation to reject host with shell metachar")
	}
	bad2 := ipmiCredentials{Host: "10.0.0.5", Username: "admin\nfoo"}
	if err := validateCreds(bad2); err == nil {
		t.Fatal("expected validation to reject username with newline")
	}
}

// ─── Verb argument validation (no exec) ───────────────────────────────────────

func TestVerbIPMIPower_BadArgCount(t *testing.T) {
	if code := verbIPMIPower(1000, nil); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
	if code := verbIPMIPower(1000, []string{"on", "extra"}); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestVerbIPMIPower_BadAction(t *testing.T) {
	if code := verbIPMIPower(1000, []string{"detonate"}); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestVerbIPMISEL_BadOp(t *testing.T) {
	if code := verbIPMISEL(1000, []string{"truncate"}); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestVerbIPMISensors_BadArgCount(t *testing.T) {
	if code := verbIPMISensors(1000, []string{"junk"}); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestVerbIPMILANSet_BadChannel(t *testing.T) {
	if code := verbIPMILANSet(1000, []string{"99", "ipaddr", "10.0.0.1"}); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestVerbIPMILANSet_BadField(t *testing.T) {
	if code := verbIPMILANSet(1000, []string{"1", "format", "/dev/sda"}); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

func TestVerbIPMILANSet_BadValue(t *testing.T) {
	if code := verbIPMILANSet(1000, []string{"1", "ipaddr", "10.0.0.1; reboot"}); code != 1 {
		t.Errorf("want 1, got %d", code)
	}
}

// ─── Allowlist parity check ───────────────────────────────────────────────────

func TestPowerActionAllowlist_Locked(t *testing.T) {
	expect := map[string]string{
		"status": "--stat",
		"on":     "--on",
		"off":    "--off",
		"cycle":  "--cycle",
		"reset":  "--reset",
	}
	if len(allowedIPMIPowerActions) != len(expect) {
		t.Errorf("allowedIPMIPowerActions size = %d, want %d", len(allowedIPMIPowerActions), len(expect))
	}
	for k, v := range expect {
		if got := allowedIPMIPowerActions[k]; got != v {
			t.Errorf("allowedIPMIPowerActions[%q] = %q, want %q", k, got, v)
		}
	}
}

func TestSELOpAllowlist_Locked(t *testing.T) {
	if len(allowedIPMISELOps) != 2 {
		t.Errorf("allowedIPMISELOps size = %d, want 2", len(allowedIPMISELOps))
	}
	for _, k := range []string{"list", "clear"} {
		if _, ok := allowedIPMISELOps[k]; !ok {
			t.Errorf("allowedIPMISELOps missing %q", k)
		}
	}
}

func TestLANFieldAllowlist_Locked(t *testing.T) {
	for _, k := range []string{"ipsrc", "ipaddr", "netmask", "defgw", "username", "password", "enable-user"} {
		if !allowedLANField[k] {
			t.Errorf("allowedLANField missing %q", k)
		}
	}
	if allowedLANField["arbitrary"] {
		t.Errorf("allowedLANField should not accept arbitrary fields")
	}
}
