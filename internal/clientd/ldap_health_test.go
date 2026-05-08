package clientd

import (
	"os/exec"
	"strings"
	"testing"
)

// TestParseDomainList covers the cheap parsing path so we don't regress on
// the exact sssctl output format.
func TestParseDomainList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single", "cluster.local\n", []string{"cluster.local"}},
		{"multi", "cluster.local\nipa.example.com\n", []string{"cluster.local", "ipa.example.com"}},
		{"empty", "\n\n", nil},
		{"with header", "Domain list:\nDomains:\ncluster.local\n", []string{"cluster.local"}},
		{"trailing whitespace", "  cluster.local  \n", []string{"cluster.local"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDomainList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("idx %d: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseDomainStatus covers the "Online status: Online" / "Offline" parse.
// The actual sssctl output is verbose; we only care about that one line.
func TestParseDomainStatus(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		online    bool
		hasMarker bool
	}{
		{
			name: "online",
			in: `Online status: Online

Active servers:
LDAP: ldap.example.com`,
			online:    true,
			hasMarker: true,
		},
		{
			name:      "offline",
			in:        "Online status: Offline\n\nActive servers: None",
			online:    false,
			hasMarker: true,
		},
		{
			name:      "case insensitive header",
			in:        "online status: Online",
			online:    true,
			hasMarker: true,
		},
		{
			name:      "no marker",
			in:        "Domain not configured\n",
			online:    false,
			hasMarker: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			online, marker := parseDomainStatus(tc.in)
			if online != tc.online {
				t.Fatalf("online: got %v want %v", online, tc.online)
			}
			gotMarker := marker != ""
			if gotMarker != tc.hasMarker {
				t.Fatalf("hasMarker: got %v (%q) want %v", gotMarker, marker, tc.hasMarker)
			}
		})
	}
}

// TestFirstNonEmptyLine sanity-checks the fallback used when no "Online status"
// line is present.
func TestFirstNonEmptyLine(t *testing.T) {
	if got := firstNonEmptyLine("\n\n  hello\nworld"); got != "hello" {
		t.Fatalf("got %q want %q", got, "hello")
	}
	if got := firstNonEmptyLine("only line"); got != "only line" {
		t.Fatalf("got %q", got)
	}
	if got := firstNonEmptyLine("   \n\t\n"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// TestCollectLDAPHealth_NotInstalled exercises the early-exit path on a host
// where sssd is missing entirely. We can't easily fake systemctl in a unit
// test, so this test only runs in the negative case (no systemctl on PATH).
// Most CI containers have systemctl; this test will skip there. The probe's
// real behaviour is exercised by integration tests on cloner.
func TestCollectLDAPHealth_NotInstalled(t *testing.T) {
	if _, err := exec.LookPath("systemctl"); err == nil {
		t.Skip("systemctl is present — this test only covers the absent path")
	}
	h := collectLDAPHealth()
	if h == nil {
		t.Fatal("collectLDAPHealth returned nil")
	}
	if h.Active {
		t.Fatalf("expected Active=false on host without systemctl, got %+v", h)
	}
	if h.Detail == "" {
		t.Fatalf("expected non-empty Detail")
	}
	if !strings.Contains(strings.ToLower(h.Detail), "sssd") {
		t.Fatalf("expected Detail to mention sssd, got %q", h.Detail)
	}
}
