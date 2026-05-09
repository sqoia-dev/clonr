package main

import (
	"strings"
	"testing"
)

// ─── Argv composition ─────────────────────────────────────────────────────────

func TestCommonIPMIArgs_Remote(t *testing.T) {
	args := commonIPMIArgs(ipmiCredentials{Host: "10.0.0.5", Username: "admin", Password: "s3cret"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-h 10.0.0.5", "-u admin", "-p s3cret", "--driver-type=LAN_2_0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
}

func TestCommonIPMIArgs_LocalEmpty(t *testing.T) {
	args := commonIPMIArgs(ipmiCredentials{})
	if len(args) != 0 {
		t.Errorf("local creds should produce empty argv, got %v", args)
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
