// dit_test.go — unit tests for LDAP DIT helper functions (S1-5, TD-1).
// No external LDAP server required: tests cover entry parsing, URI helpers,
// and other pure functions.
package ldap

import (
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// ─── serverNameFromURI ────────────────────────────────────────────────────────

func TestServerNameFromURI_LDAPS(t *testing.T) {
	cases := []struct {
		uri  string
		want string
	}{
		{"ldaps://ldap.example.com:636", "ldap.example.com"},
		{"ldaps://10.0.0.5:636", "10.0.0.5"},
		{"ldap://ldap.example.com:389", "ldap.example.com"},
		{"ldaps://ldap.example.com", "ldap.example.com"},
		{"ldap.example.com:636", "ldap.example.com"},
		{"ldap.example.com", "ldap.example.com"},
	}

	for _, c := range cases {
		got := serverNameFromURI(c.uri)
		if got != c.want {
			t.Errorf("serverNameFromURI(%q) = %q, want %q", c.uri, got, c.want)
		}
	}
}

// ─── entryToUser ─────────────────────────────────────────────────────────────

// newTestEntry constructs a minimal goldap.Entry with the given attributes.
func newTestEntry(dn string, attrs map[string][]string) *goldap.Entry {
	var entryAttrs []*goldap.EntryAttribute
	for name, vals := range attrs {
		entryAttrs = append(entryAttrs, &goldap.EntryAttribute{
			Name:   name,
			Values: vals,
		})
	}
	return goldap.NewEntry(dn, entryAttrs)
}

func TestEntryToUser_BasicFields(t *testing.T) {
	entry := newTestEntry("uid=alice,ou=people,dc=example,dc=com", map[string][]string{
		"uid":           {"alice"},
		"uidNumber":     {"1001"},
		"gidNumber":     {"1001"},
		"cn":            {"Alice Example"},
		"sn":            {"Example"},
		"homeDirectory": {"/home/alice"},
		"loginShell":    {"/bin/bash"},
	})

	u, err := entryToUser(entry)
	if err != nil {
		t.Fatalf("entryToUser: %v", err)
	}

	if u.UID != "alice" {
		t.Errorf("UID = %q, want %q", u.UID, "alice")
	}
	if u.UIDNumber != 1001 {
		t.Errorf("UIDNumber = %d, want 1001", u.UIDNumber)
	}
	if u.GIDNumber != 1001 {
		t.Errorf("GIDNumber = %d, want 1001", u.GIDNumber)
	}
	if u.CN != "Alice Example" {
		t.Errorf("CN = %q, want %q", u.CN, "Alice Example")
	}
	if u.HomeDirectory != "/home/alice" {
		t.Errorf("HomeDirectory = %q, want /home/alice", u.HomeDirectory)
	}
	if u.LoginShell != "/bin/bash" {
		t.Errorf("LoginShell = %q, want /bin/bash", u.LoginShell)
	}
	if u.Locked {
		t.Error("Locked = true, want false (shadowExpire not set)")
	}
	if u.LastLogin != nil {
		t.Error("LastLogin should be nil when pwdLastSuccess absent")
	}
}

func TestEntryToUser_LockedByShAdowExpire(t *testing.T) {
	entry := newTestEntry("uid=bob,ou=people,dc=example,dc=com", map[string][]string{
		"uid":           {"bob"},
		"uidNumber":     {"1002"},
		"gidNumber":     {"1002"},
		"cn":            {"Bob"},
		"sn":            {"Bob"},
		"homeDirectory": {"/home/bob"},
		"loginShell":    {"/bin/bash"},
		"shadowExpire":  {"1"},
	})

	u, err := entryToUser(entry)
	if err != nil {
		t.Fatalf("entryToUser: %v", err)
	}
	if !u.Locked {
		t.Error("Locked = false, want true (shadowExpire=1)")
	}
}

func TestEntryToUser_UnlockedShAdowExpireZero(t *testing.T) {
	entry := newTestEntry("uid=carol,ou=people,dc=example,dc=com", map[string][]string{
		"uid":          {"carol"},
		"uidNumber":    {"1003"},
		"gidNumber":    {"1003"},
		"cn":           {"Carol"},
		"sn":           {"Carol"},
		"shadowExpire": {"0"},
	})

	u, err := entryToUser(entry)
	if err != nil {
		t.Fatalf("entryToUser: %v", err)
	}
	if u.Locked {
		t.Error("Locked = true, want false (shadowExpire=0 is not disabled)")
	}
}

func TestEntryToUser_LastLoginParsed(t *testing.T) {
	// pwdLastSuccess format: YYYYMMDDHHmmssZ (GeneralizedTime)
	entry := newTestEntry("uid=dave,ou=people,dc=example,dc=com", map[string][]string{
		"uid":            {"dave"},
		"uidNumber":      {"1004"},
		"gidNumber":      {"1004"},
		"cn":             {"Dave"},
		"sn":             {"Dave"},
		"pwdLastSuccess": {"20260101120000Z"},
	})

	u, err := entryToUser(entry)
	if err != nil {
		t.Fatalf("entryToUser: %v", err)
	}
	if u.LastLogin == nil {
		t.Fatal("LastLogin is nil, want non-nil (pwdLastSuccess present)")
	}
	want := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if !u.LastLogin.Equal(want) {
		t.Errorf("LastLogin = %v, want %v", u.LastLogin, want)
	}
}

func TestEntryToUser_MissingUIDReturnsError(t *testing.T) {
	entry := newTestEntry("ou=people,dc=example,dc=com", map[string][]string{
		"uidNumber": {"9999"},
		"cn":        {"NoUID"},
	})
	_, err := entryToUser(entry)
	if err == nil {
		t.Error("expected error for missing uid attribute, got nil")
	}
}

// ─── userDN / groupDN construction ───────────────────────────────────────────

func TestDITClientDNs(t *testing.T) {
	c := &ditClient{baseDN: "dc=hpc,dc=example,dc=com"}

	wantUser := "uid=alice,ou=people,dc=hpc,dc=example,dc=com"
	if got := c.userDN("alice"); got != wantUser {
		t.Errorf("userDN = %q, want %q", got, wantUser)
	}

	wantGroup := "cn=clustr-admins,ou=groups,dc=hpc,dc=example,dc=com"
	if got := c.groupDN("clustr-admins"); got != wantGroup {
		t.Errorf("groupDN = %q, want %q", got, wantGroup)
	}
}

// ─── HashPasswordCrypt ───────────────────────────────────────────────────────

func TestHashPasswordCrypt_ProducesValidPrefix(t *testing.T) {
	hash, err := HashPasswordCrypt("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPasswordCrypt: %v", err)
	}

	// Must start with {CRYPT}$6$rounds=100000$
	if !strings.HasPrefix(hash, "{CRYPT}$6$rounds=100000$") {
		t.Errorf("hash = %q, want prefix {CRYPT}$6$rounds=100000$", hash)
	}
}

func TestHashPasswordCrypt_Uniqueness(t *testing.T) {
	h1, err := HashPasswordCrypt("samepassword")
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	h2, err := HashPasswordCrypt("samepassword")
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	// Each call uses a fresh random salt so the hashes must differ.
	if h1 == h2 {
		t.Error("two hashes of the same password are identical — salt is not random")
	}
}

func TestHashPasswordCrypt_MinimumLength(t *testing.T) {
	hash, err := HashPasswordCrypt("x")
	if err != nil {
		t.Fatalf("HashPasswordCrypt short pw: %v", err)
	}
	// A SHA-512 crypt hash is always longer than 100 characters.
	if len(hash) < 100 {
		t.Errorf("hash too short (%d chars), expected >= 100", len(hash))
	}
}
