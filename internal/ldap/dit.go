// dit.go — LDAP client wrapper for user and group CRUD against the data DIT.
// All operations bind as the Directory Manager (admin) using ldaps://.
// The go-ldap library is used for all LDAP protocol operations.
//
// Schema: classic NIS (posixAccount + inetOrgPerson + shadowAccount for users,
// posixGroup with memberUid for groups). No rfc2307bis / groupOfNames.
//
// Locking: set shadowExpire=1 to disable an account. sssd honors this via
// ldap_account_expire_policy = shadow.

package ldap

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strconv"
	"strings"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// LDAPUser is the wire type for a POSIX user in the DIT.
type LDAPUser struct {
	UID           string     `json:"uid"`
	UIDNumber     int        `json:"uid_number"`
	GIDNumber     int        `json:"gid_number"`
	CN            string     `json:"cn"`
	SN            string     `json:"sn"`
	GivenName     string     `json:"given_name,omitempty"`
	Mail          string     `json:"mail,omitempty"`
	HomeDirectory string     `json:"home_directory"`
	LoginShell    string     `json:"login_shell"`
	SSHPublicKeys []string   `json:"ssh_public_keys,omitempty"` // sshPublicKey attribute values
	Locked        bool       `json:"locked"`               // true if shadowExpire=1
	LastLogin     *time.Time `json:"last_login,omitempty"` // pwdLastSuccess, nil if never
}

// LDAPGroup is the wire type for a POSIX group in the DIT.
type LDAPGroup struct {
	CN          string   `json:"cn"`
	GIDNumber   int      `json:"gid_number"`
	MemberUIDs  []string `json:"member_uids"`
	Description string   `json:"description,omitempty"`
}

// CreateUserRequest holds the fields for creating a new POSIX user.
// UIDNumber == 0 means "auto-allocate"; GIDNumber == 0 means "auto-allocate".
type CreateUserRequest struct {
	UID           string   `json:"uid"`
	UIDNumber     int      `json:"uid_number"`    // 0 = auto-allocate via posixid.Allocator
	GIDNumber     int      `json:"gid_number"`    // 0 = auto-allocate via posixid.Allocator
	CN            string   `json:"cn"`
	SN            string   `json:"sn"`
	GivenName     string   `json:"given_name,omitempty"`
	Mail          string   `json:"mail,omitempty"`         // #94: email field → mail attribute
	HomeDirectory string   `json:"home_directory"`
	LoginShell    string   `json:"login_shell"`
	Password      string   `json:"password"`               // plaintext; hashed by slapd
	SSHPublicKeys []string `json:"ssh_public_keys,omitempty"` // #94: one per line; each written as sshPublicKey
}

// UpdateUserRequest holds the fields that can be updated on an existing user via PATCH.
// Only non-zero / non-nil fields are applied.
type UpdateUserRequest struct {
	CN            string   `json:"cn,omitempty"`
	SN            string   `json:"sn,omitempty"`
	GivenName     string   `json:"given_name,omitempty"`
	Mail          string   `json:"mail,omitempty"`          // mail attribute
	HomeDirectory string   `json:"home_directory,omitempty"`
	LoginShell    string   `json:"login_shell,omitempty"`
	GIDNumber     *int     `json:"gid_number,omitempty"`    // pointer so 0 is distinguishable from "not set"
	SSHPublicKeys []string `json:"ssh_public_keys,omitempty"` // replaces all sshPublicKey values
}

// ditClient wraps a go-ldap connection, binding as Directory Manager.
type ditClient struct {
	serverURI  string // ldaps://hostname:636
	bindDN     string // cn=Directory Manager,dc=...
	bindPasswd string // plaintext DM password (in-memory only)
	baseDN     string
	caCertPEM  []byte
}

// connect opens a new LDAP connection and binds as Directory Manager.
// The caller is responsible for calling conn.Close() when done.
func (c *ditClient) connect() (*goldap.Conn, error) {
	// Build TLS config with our CA cert.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.caCertPEM) {
		return nil, fmt.Errorf("ldap dit: failed to parse CA cert for connection pool")
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: serverNameFromURI(c.serverURI),
	}

	conn, err := goldap.DialURL(c.serverURI, goldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return nil, fmt.Errorf("ldap dit: dial %s: %w", c.serverURI, err)
	}

	if err := conn.Bind(c.bindDN, c.bindPasswd); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ldap dit: bind as %s: %w", c.bindDN, err)
	}
	return conn, nil
}

// serverNameFromURI extracts the hostname from an ldaps://host:port URI.
func serverNameFromURI(uri string) string {
	s := strings.TrimPrefix(uri, "ldaps://")
	s = strings.TrimPrefix(s, "ldap://")
	if idx := strings.Index(s, ":"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// userDN returns the DN for a user entry.
func (c *ditClient) userDN(uid string) string {
	return fmt.Sprintf("uid=%s,ou=people,%s", goldap.EscapeDN(uid), c.baseDN)
}

// groupDN returns the DN for a group entry.
func (c *ditClient) groupDN(cn string) string {
	return fmt.Sprintf("cn=%s,ou=groups,%s", goldap.EscapeDN(cn), c.baseDN)
}

// ListUsers returns all posixAccount entries from ou=people.
func (c *ditClient) ListUsers() ([]LDAPUser, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := goldap.NewSearchRequest(
		fmt.Sprintf("ou=people,%s", c.baseDN),
		goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases,
		0, 0, false,
		"(objectClass=posixAccount)",
		// pwdLastSuccess is an operational attribute — must be named explicitly;
		// it is NOT returned by a bare "*" wildcard search.
		// sshPublicKey is from the ldapPublicKey auxiliary schema.
		[]string{"uid", "uidNumber", "gidNumber", "cn", "sn", "givenName", "mail", "homeDirectory", "loginShell", "shadowExpire", "pwdLastSuccess", "sshPublicKey"},
		nil,
	)

	result, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("ldap dit: list users: %w", err)
	}

	users := make([]LDAPUser, 0, len(result.Entries))
	for _, entry := range result.Entries {
		u, err := entryToUser(entry)
		if err != nil {
			continue // skip malformed entries
		}
		users = append(users, u)
	}
	return users, nil
}

// GetUser retrieves a single user by UID.
func (c *ditClient) GetUser(uid string) (*LDAPUser, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := goldap.NewSearchRequest(
		fmt.Sprintf("ou=people,%s", c.baseDN),
		goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases,
		1, 0, false,
		fmt.Sprintf("(uid=%s)", goldap.EscapeFilter(uid)),
		[]string{"uid", "uidNumber", "gidNumber", "cn", "sn", "givenName", "mail", "homeDirectory", "loginShell", "shadowExpire", "sshPublicKey"},
		nil,
	)

	result, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("ldap dit: get user %s: %w", uid, err)
	}
	if len(result.Entries) == 0 {
		return nil, fmt.Errorf("ldap dit: user %q not found", uid)
	}

	u, err := entryToUser(result.Entries[0])
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser adds a new posixAccount + inetOrgPerson + shadowAccount entry.
func (c *ditClient) CreateUser(req CreateUserRequest) error {
	if req.UID == "" {
		return fmt.Errorf("ldap dit: create user: uid is required")
	}
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.userDN(req.UID)
	shell := req.LoginShell
	if shell == "" {
		shell = "/bin/bash"
	}
	home := req.HomeDirectory
	if home == "" {
		home = fmt.Sprintf("/home/%s", req.UID)
	}
	cn := req.CN
	if cn == "" {
		cn = req.UID
	}
	sn := req.SN
	if sn == "" {
		sn = req.UID
	}
	gecos := req.CN

	addReq := goldap.NewAddRequest(dn, nil)
	// objectClasses: ldapPublicKey enables the sshPublicKey attribute.
	objectClasses := []string{"top", "posixAccount", "inetOrgPerson", "shadowAccount", "ldapPublicKey"}
	addReq.Attribute("objectClass", objectClasses)
	addReq.Attribute("uid", []string{req.UID})
	addReq.Attribute("cn", []string{cn})
	addReq.Attribute("sn", []string{sn})
	addReq.Attribute("gecos", []string{gecos})
	addReq.Attribute("homeDirectory", []string{home})
	addReq.Attribute("loginShell", []string{shell})
	addReq.Attribute("uidNumber", []string{strconv.Itoa(req.UIDNumber)})
	addReq.Attribute("gidNumber", []string{strconv.Itoa(req.GIDNumber)})
	// Send the plaintext password; ppolicy (olcPPolicyHashCleartext: TRUE) will
	// hash it using slapd's native crypt(3) (glibc), ensuring bind verification works.
	if req.Password != "" {
		addReq.Attribute("userPassword", []string{req.Password})
	}
	// shadowAccount attributes — set sensible defaults (no expiry, no aging).
	addReq.Attribute("shadowLastChange", []string{"0"})
	addReq.Attribute("shadowMin", []string{"0"})
	addReq.Attribute("shadowMax", []string{"99999"})
	addReq.Attribute("shadowWarning", []string{"7"})
	// Optional attributes from #94.
	if req.GivenName != "" {
		addReq.Attribute("givenName", []string{req.GivenName})
	}
	if req.Mail != "" {
		addReq.Attribute("mail", []string{req.Mail})
	}
	// SSH public keys — each line written as a separate sshPublicKey value.
	if len(req.SSHPublicKeys) > 0 {
		keys := filterNonEmpty(req.SSHPublicKeys)
		if len(keys) > 0 {
			addReq.Attribute("sshPublicKey", keys)
		}
	}

	if err := conn.Add(addReq); err != nil {
		return fmt.Errorf("ldap dit: create user %s: %w", req.UID, err)
	}

	// Auto-create the User Private Group (UPG) — mirrors RHEL useradd behaviour.
	// The group shares the user's uid as its CN and the same gidNumber.
	upgDN := c.groupDN(req.UID)
	grpReq := goldap.NewAddRequest(upgDN, nil)
	grpReq.Attribute("objectClass", []string{"top", "posixGroup"})
	grpReq.Attribute("cn", []string{req.UID})
	grpReq.Attribute("gidNumber", []string{strconv.Itoa(req.GIDNumber)})
	grpReq.Attribute("memberUid", []string{req.UID})
	grpReq.Attribute("description", []string{fmt.Sprintf("User private group for %s", req.UID)})

	if err := conn.Add(grpReq); err != nil {
		// If the group already exists, silently continue — idempotent.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultEntryAlreadyExists) {
			return nil
		}
		return fmt.Errorf("ldap dit: create user private group for %s: %w", req.UID, err)
	}
	return nil
}

// UpdateUser modifies an existing user entry with the provided fields.
// Only non-empty / non-nil fields are updated.
func (c *ditClient) UpdateUser(uid string, req UpdateUserRequest) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.userDN(uid)
	modReq := goldap.NewModifyRequest(dn, nil)

	if req.CN != "" {
		modReq.Replace("cn", []string{req.CN})
	}
	if req.SN != "" {
		modReq.Replace("sn", []string{req.SN})
	}
	if req.GivenName != "" {
		modReq.Replace("givenName", []string{req.GivenName})
	}
	if req.Mail != "" {
		// Replace; empty string to clear is handled in PatchUser below.
		modReq.Replace("mail", []string{req.Mail})
	}
	if req.HomeDirectory != "" {
		modReq.Replace("homeDirectory", []string{req.HomeDirectory})
	}
	if req.LoginShell != "" {
		modReq.Replace("loginShell", []string{req.LoginShell})
	}
	if req.GIDNumber != nil {
		modReq.Replace("gidNumber", []string{strconv.Itoa(*req.GIDNumber)})
	}
	// SSH keys: replace entire sshPublicKey attribute with the new set.
	if req.SSHPublicKeys != nil {
		keys := filterNonEmpty(req.SSHPublicKeys)
		if len(keys) > 0 {
			modReq.Replace("sshPublicKey", keys)
		} else {
			// Empty slice means "remove all SSH keys".
			modReq.Delete("sshPublicKey", []string{})
		}
	}

	if len(modReq.Changes) == 0 {
		return nil // nothing to update
	}

	if err := conn.Modify(modReq); err != nil {
		// Tolerate NoSuchAttribute when deleting sshPublicKey that was never set.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchAttribute) {
			return nil
		}
		return fmt.Errorf("ldap dit: update user %s: %w", uid, err)
	}
	return nil
}

// SetPassword changes a user's userPassword attribute.
// The plaintext password is sent to slapd over ldaps://; ppolicy
// (olcPPolicyHashCleartext: TRUE) hashes it using slapd's native crypt(3)
// (glibc), ensuring bind verification works correctly.
//
// If forceChange is true, pwdReset is set to TRUE in a second Modify after the
// password is committed, causing the ppolicy overlay to require the user to
// change their password at next login. If forceChange is false, pwdReset is
// deleted in a second Modify, tolerating NoSuchAttribute so the call is
// idempotent regardless of prior ppolicy state.
//
// IMPORTANT: pwdReset is handled in a separate Modify from userPassword.
// Combining them in a single multi-change Modify causes slapd to roll back the
// entire operation when pwdReset does not exist on the entry (NoSuchAttribute
// applies to the full transaction, not just the offending change). Splitting
// the writes makes the password replace unconditional and the pwdReset
// cleanup best-effort.
//
// pwdChangedTime is intentionally NOT touched — it is NO-USER-MODIFICATION and
// is managed entirely by the ppolicy overlay.
func (c *ditClient) SetPassword(uid, password string, forceChange bool) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.userDN(uid)

	// Step 1: replace userPassword unconditionally.
	pwReq := goldap.NewModifyRequest(dn, nil)
	pwReq.Replace("userPassword", []string{password})
	if err := conn.Modify(pwReq); err != nil {
		return fmt.Errorf("ldap dit: set password for %s: %w", uid, err)
	}

	// Step 2: handle pwdReset in a separate Modify so a missing attribute
	// cannot roll back the password change above.
	resetReq := goldap.NewModifyRequest(dn, nil)
	if forceChange {
		resetReq.Replace("pwdReset", []string{"TRUE"})
	} else {
		// Delete with empty value list removes the attribute. Tolerate
		// NoSuchAttribute — the user may never have had pwdReset set.
		resetReq.Delete("pwdReset", []string{})
	}
	if err := conn.Modify(resetReq); err != nil {
		if !forceChange && goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchAttribute) {
			// pwdReset was absent — the password is already committed, so this
			// is a no-op. Return success.
			return nil
		}
		return fmt.Errorf("ldap dit: set pwdReset for %s: %w", uid, err)
	}
	return nil
}

// LockUser disables an account by setting shadowExpire=1.
// sssd with ldap_account_expire_policy=shadow treats shadowExpire=1 as disabled.
func (c *ditClient) LockUser(uid string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.userDN(uid)
	modReq := goldap.NewModifyRequest(dn, nil)
	modReq.Replace("shadowExpire", []string{"1"})

	if err := conn.Modify(modReq); err != nil {
		return fmt.Errorf("ldap dit: lock user %s: %w", uid, err)
	}
	return nil
}

// UnlockUser re-enables an account by removing the shadowExpire attribute.
func (c *ditClient) UnlockUser(uid string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.userDN(uid)
	modReq := goldap.NewModifyRequest(dn, nil)
	// Delete the shadowExpire attribute entirely (no value = remove).
	modReq.Delete("shadowExpire", []string{})

	if err := conn.Modify(modReq); err != nil {
		// If shadowExpire did not exist, that's fine — user is already unlocked.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchAttribute) {
			return nil
		}
		return fmt.Errorf("ldap dit: unlock user %s: %w", uid, err)
	}
	return nil
}

// DeleteUser removes a user entry from the DIT and auto-cleans up group
// memberships. The operation order is:
//  1. Delete the posixAccount entry.
//  2. Search ou=groups for all groups carrying memberUid=<uid> and issue a
//     Modify Delete for each. NoSuchAttribute is tolerated (race condition).
//  3. Delete the user's UPG (cn=<uid>,ou=groups) if it exists and has no
//     remaining members besides the deleted user. If other members remain the
//     UPG is left untouched — it is a misconfiguration but not worth blocking.
func (c *ditClient) DeleteUser(uid string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Step 1: delete the posixAccount entry.
	dn := c.userDN(uid)
	if err := conn.Del(goldap.NewDelRequest(dn, nil)); err != nil {
		return fmt.Errorf("ldap dit: delete user %s: %w", uid, err)
	}

	// Step 2: remove this uid from every group that lists it as a memberUid.
	groupBase := fmt.Sprintf("ou=groups,%s", c.baseDN)
	searchReq := goldap.NewSearchRequest(
		groupBase,
		goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases,
		0, 0, false,
		fmt.Sprintf("(&(objectClass=posixGroup)(memberUid=%s))", goldap.EscapeFilter(uid)),
		[]string{"dn"},
		nil,
	)

	result, err := conn.Search(searchReq)
	if err != nil {
		return fmt.Errorf("ldap dit: delete user %s: search group memberships: %w", uid, err)
	}

	for _, entry := range result.Entries {
		modReq := goldap.NewModifyRequest(entry.DN, nil)
		modReq.Delete("memberUid", []string{uid})
		if err := conn.Modify(modReq); err != nil {
			// Tolerate NoSuchAttribute — another process may have already removed it.
			if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchAttribute) {
				continue
			}
			return fmt.Errorf("ldap dit: delete user %s: remove memberUid from %s: %w", uid, entry.DN, err)
		}
	}

	// Step 3: delete the UPG (cn=<uid>,ou=groups) if it exists and is empty.
	upgDN := c.groupDN(uid)
	upgSearch := goldap.NewSearchRequest(
		upgDN,
		goldap.ScopeBaseObject,
		goldap.NeverDerefAliases,
		1, 0, false,
		"(objectClass=posixGroup)",
		[]string{"memberUid"},
		nil,
	)

	upgResult, err := conn.Search(upgSearch)
	if err != nil {
		// NoSuchObject means the UPG was never created — nothing to do.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject) {
			return nil
		}
		return fmt.Errorf("ldap dit: delete user %s: search UPG: %w", uid, err)
	}

	if len(upgResult.Entries) == 0 {
		// UPG does not exist; nothing to clean up.
		return nil
	}

	// Collect remaining members (the deleted user's uid is already gone from the
	// group after step 2, but be defensive and filter it out anyway).
	remainingMembers := upgResult.Entries[0].GetAttributeValues("memberUid")
	otherMembers := remainingMembers[:0]
	for _, m := range remainingMembers {
		if m != uid {
			otherMembers = append(otherMembers, m)
		}
	}

	if len(otherMembers) > 0 {
		// Other users are still in the UPG — misconfiguration, but leave it alone.
		return nil
	}

	if err := conn.Del(goldap.NewDelRequest(upgDN, nil)); err != nil {
		// If it disappeared between our search and our delete, that's fine.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject) {
			return nil
		}
		return fmt.Errorf("ldap dit: delete user %s: delete UPG: %w", uid, err)
	}

	return nil
}

// ListGroups returns all posixGroup entries from ou=groups.
func (c *ditClient) ListGroups() ([]LDAPGroup, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := goldap.NewSearchRequest(
		fmt.Sprintf("ou=groups,%s", c.baseDN),
		goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases,
		0, 0, false,
		"(objectClass=posixGroup)",
		[]string{"cn", "gidNumber", "memberUid", "description"},
		nil,
	)

	result, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("ldap dit: list groups: %w", err)
	}

	groups := make([]LDAPGroup, 0, len(result.Entries))
	for _, entry := range result.Entries {
		g := LDAPGroup{
			CN:          entry.GetAttributeValue("cn"),
			MemberUIDs:  entry.GetAttributeValues("memberUid"),
			Description: entry.GetAttributeValue("description"),
		}
		if n, err := strconv.Atoi(entry.GetAttributeValue("gidNumber")); err == nil {
			g.GIDNumber = n
		}
		if g.MemberUIDs == nil {
			g.MemberUIDs = []string{}
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// CreateGroup adds a new posixGroup entry. If description is non-empty it is
// set as the description attribute on the entry.
func (c *ditClient) CreateGroup(cn string, gidNumber int, description string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.groupDN(cn)
	addReq := goldap.NewAddRequest(dn, nil)
	addReq.Attribute("objectClass", []string{"top", "posixGroup"})
	addReq.Attribute("cn", []string{cn})
	addReq.Attribute("gidNumber", []string{strconv.Itoa(gidNumber)})
	if description != "" {
		addReq.Attribute("description", []string{description})
	}

	if err := conn.Add(addReq); err != nil {
		return fmt.Errorf("ldap dit: create group %s: %w", cn, err)
	}
	return nil
}

// UpdateGroup modifies an existing group entry. Currently only description is
// mutable (CN and gidNumber are locked after creation per the schema policy).
// Passing an empty description replaces the attribute with an empty value list,
// which removes it from the entry — same pattern as UnlockUser on shadowExpire.
func (c *ditClient) UpdateGroup(cn, description string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.groupDN(cn)
	modReq := goldap.NewModifyRequest(dn, nil)

	if description != "" {
		modReq.Replace("description", []string{description})
	} else {
		// DELETE with an empty value list removes the attribute entirely.
		modReq.Delete("description", []string{})
	}

	if err := conn.Modify(modReq); err != nil {
		// If the attribute didn't exist and we're trying to delete it, that's fine.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchAttribute) {
			return nil
		}
		return fmt.Errorf("ldap dit: update group %s: %w", cn, err)
	}
	return nil
}

// DeleteGroup removes a group entry.
func (c *ditClient) DeleteGroup(cn string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.groupDN(cn)
	if err := conn.Del(goldap.NewDelRequest(dn, nil)); err != nil {
		return fmt.Errorf("ldap dit: delete group %s: %w", cn, err)
	}
	return nil
}

// AddGroupMember adds a uid to the memberUid attribute of a group.
func (c *ditClient) AddGroupMember(groupCN, uid string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.groupDN(groupCN)
	modReq := goldap.NewModifyRequest(dn, nil)
	modReq.Add("memberUid", []string{uid})

	if err := conn.Modify(modReq); err != nil {
		return fmt.Errorf("ldap dit: add member %s to group %s: %w", uid, groupCN, err)
	}
	return nil
}

// RemoveGroupMember removes a uid from the memberUid attribute of a group.
func (c *ditClient) RemoveGroupMember(groupCN, uid string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.groupDN(groupCN)
	modReq := goldap.NewModifyRequest(dn, nil)
	modReq.Delete("memberUid", []string{uid})

	if err := conn.Modify(modReq); err != nil {
		return fmt.Errorf("ldap dit: remove member %s from group %s: %w", uid, groupCN, err)
	}
	return nil
}

// writeServiceAccountPassword issues a single atomic Modify on dn that:
//   - Replace("userPassword", passwd)    — plaintext; ppolicy hashes via glibc crypt(3)
//   - Delete("pwdReset", [])             — tolerates NoSuchAttribute
//   - Delete("pwdAccountLockedTime", []) — tolerates NoSuchAttribute
//
// This guarantees that after any clustr-driven write to a service account's
// password the entry is in a clean ppolicy state: no must-change wall and no
// lockout.
//
// Background: ppolicy with pwdMustChange:TRUE auto-sets pwdReset:TRUE on any
// entry whose userPassword is modified by the rootdn. Correct for human users
// (force-change-after-admin-reset feature), but catastrophic for cn=node-reader
// which is a non-interactive service account. Clearing the two attributes in
// the same Modify ensures the net result is a clean entry.
//
// pwdFailureTime is intentionally NOT deleted here — it is marked
// NO-USER-MODIFICATION in the ppolicy schema and slapd will reject any attempt
// to touch it with LDAP Result Code 19 (Constraint Violation). slapd resets
// pwdFailureTime automatically on a successful bind, so no manual cleanup is
// needed.
//
// NoSuchAttribute errors from either Delete operation are silently tolerated so
// the call is idempotent regardless of prior ppolicy state.
//
// This function must NOT be used for regular human users — their pwdReset
// handling is managed by SetPassword() based on the operator's force_change
// checkbox per spec.
func writeServiceAccountPassword(conn *goldap.Conn, dn, passwd string) error {
	modReq := goldap.NewModifyRequest(dn, nil)
	modReq.Replace("userPassword", []string{passwd})
	modReq.Delete("pwdReset", []string{})
	modReq.Delete("pwdAccountLockedTime", []string{})

	if err := conn.Modify(modReq); err != nil {
		// Tolerate NoSuchAttribute: if neither pwdReset nor pwdAccountLockedTime
		// was present, LDAP returns this code for the first Delete that finds
		// nothing to remove. The userPassword Replace always succeeds before the
		// Deletes are evaluated, so we can safely ignore this.
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchAttribute) {
			return nil
		}
		return err
	}
	return nil
}

// HealthBind attempts an anonymous (unauthenticated) search to verify slapd is
// reachable. Returns nil on success.
func (c *ditClient) HealthBind() error {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.caCertPEM) {
		return fmt.Errorf("ldap dit: health: failed to parse CA cert")
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: serverNameFromURI(c.serverURI),
	}
	conn, err := goldap.DialURL(c.serverURI, goldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return fmt.Errorf("ldap dit: health dial: %w", err)
	}
	conn.Close()
	return nil
}

// entryToUser converts an LDAP entry to an LDAPUser struct.
// GetQuotaAttrs reads the specified LDAP attribute values for uid.
// Returns empty strings when the attributes are absent on the entry.
func (c *ditClient) GetQuotaAttrs(uid, usedAttr, limitAttr string) (usedRaw, limitRaw string, err error) {
	conn, cerr := c.connect()
	if cerr != nil {
		return "", "", cerr
	}
	defer conn.Close()

	dn := c.userDN(uid)

	// Build the attribute list — only request non-empty attribute names.
	attrs := []string{}
	if usedAttr != "" {
		attrs = append(attrs, usedAttr)
	}
	if limitAttr != "" {
		attrs = append(attrs, limitAttr)
	}
	if len(attrs) == 0 {
		return "", "", nil
	}

	req := goldap.NewSearchRequest(
		dn,
		goldap.ScopeBaseObject,
		goldap.NeverDerefAliases,
		1, 0, false,
		"(objectClass=*)",
		attrs,
		nil,
	)
	sr, err := conn.Search(req)
	if err != nil {
		return "", "", fmt.Errorf("ldap dit: get quota attrs for %s: %w", uid, err)
	}
	if len(sr.Entries) == 0 {
		return "", "", nil
	}
	entry := sr.Entries[0]
	if usedAttr != "" {
		usedRaw = entry.GetAttributeValue(usedAttr)
	}
	if limitAttr != "" {
		limitRaw = entry.GetAttributeValue(limitAttr)
	}
	return usedRaw, limitRaw, nil
}

func entryToUser(entry *goldap.Entry) (LDAPUser, error) {
	uid := entry.GetAttributeValue("uid")
	if uid == "" {
		return LDAPUser{}, fmt.Errorf("entry missing uid: %s", entry.DN)
	}

	sshKeys := entry.GetAttributeValues("sshPublicKey")
	if sshKeys == nil {
		sshKeys = []string{}
	}

	u := LDAPUser{
		UID:           uid,
		CN:            entry.GetAttributeValue("cn"),
		SN:            entry.GetAttributeValue("sn"),
		GivenName:     entry.GetAttributeValue("givenName"),
		Mail:          entry.GetAttributeValue("mail"),
		HomeDirectory: entry.GetAttributeValue("homeDirectory"),
		LoginShell:    entry.GetAttributeValue("loginShell"),
		SSHPublicKeys: sshKeys,
	}

	if n, err := strconv.Atoi(entry.GetAttributeValue("uidNumber")); err == nil {
		u.UIDNumber = n
	}
	if n, err := strconv.Atoi(entry.GetAttributeValue("gidNumber")); err == nil {
		u.GIDNumber = n
	}

	shadowExpire := entry.GetAttributeValue("shadowExpire")
	if shadowExpire == "1" {
		u.Locked = true
	}

	// pwdLastSuccess is a GeneralizedTime operational attribute written by the
	// ppolicy overlay on every successful bind. Parse it if present.
	if raw := entry.GetAttributeValue("pwdLastSuccess"); raw != "" {
		if t, err := time.Parse("20060102150405Z", raw); err == nil {
			u.LastLogin = &t
		}
	}

	return u, nil
}

// ─── Project plugin helpers ───────────────────────────────────────────────────

// EnsureOU creates an organizationalUnit entry under parentDN if it does not exist.
// Returns nil on success or if the OU already exists.
func (c *ditClient) EnsureOU(ouName, parentDN string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := fmt.Sprintf("ou=%s,%s", goldap.EscapeDN(ouName), parentDN)
	addReq := goldap.NewAddRequest(dn, nil)
	addReq.Attribute("objectClass", []string{"top", "organizationalUnit"})
	addReq.Attribute("ou", []string{ouName})

	if addErr := conn.Add(addReq); addErr != nil {
		if goldap.IsErrorWithCode(addErr, goldap.LDAPResultEntryAlreadyExists) {
			return nil // idempotent
		}
		return fmt.Errorf("ldap dit: ensure OU %s: %w", dn, addErr)
	}
	return nil
}

// EnsureProjectGroup creates a posixGroup under parentOU, or verifies it exists.
// Returns the full DN of the group.
func (c *ditClient) EnsureProjectGroup(cn string, gidNumber int, description, parentOU string) (string, error) {
	conn, err := c.connect()
	if err != nil {
		return "", err
	}
	defer conn.Close()

	dn := fmt.Sprintf("cn=%s,%s", goldap.EscapeDN(cn), parentOU)
	addReq := goldap.NewAddRequest(dn, nil)
	addReq.Attribute("objectClass", []string{"top", "posixGroup"})
	addReq.Attribute("cn", []string{cn})
	addReq.Attribute("gidNumber", []string{strconv.Itoa(gidNumber)})
	if description != "" {
		addReq.Attribute("description", []string{description})
	}

	if addErr := conn.Add(addReq); addErr != nil {
		if goldap.IsErrorWithCode(addErr, goldap.LDAPResultEntryAlreadyExists) {
			return dn, nil // already exists — return the DN
		}
		return "", fmt.Errorf("ldap dit: ensure project group %s: %w", dn, addErr)
	}
	return dn, nil
}

// GetGroupMembers returns the memberUid list for a posixGroup identified by CN.
// Returns an empty slice if the group has no members.
func (c *ditClient) GetGroupMembers(cn string) ([]string, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Search the full DIT for the group by CN since it may be in any OU.
	req := goldap.NewSearchRequest(
		c.baseDN,
		goldap.ScopeWholeSubtree,
		goldap.NeverDerefAliases,
		1, 0, false,
		fmt.Sprintf("(&(objectClass=posixGroup)(cn=%s))", goldap.EscapeFilter(cn)),
		[]string{"memberUid"},
		nil,
	)

	sr, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("ldap dit: get group members for %s: %w", cn, err)
	}
	if len(sr.Entries) == 0 {
		return nil, fmt.Errorf("ldap dit: group %q not found", cn)
	}
	return sr.Entries[0].GetAttributeValues("memberUid"), nil
}

// filterNonEmpty returns only non-empty, non-whitespace-only strings from ss.
func filterNonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
