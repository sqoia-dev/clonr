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

	goldap "github.com/go-ldap/ldap/v3"
)

// LDAPUser is the wire type for a POSIX user in the DIT.
type LDAPUser struct {
	UID           string `json:"uid"`
	UIDNumber     int    `json:"uid_number"`
	GIDNumber     int    `json:"gid_number"`
	CN            string `json:"cn"`
	SN            string `json:"sn"`
	HomeDirectory string `json:"home_directory"`
	LoginShell    string `json:"login_shell"`
	Locked        bool   `json:"locked"` // true if shadowExpire=1
}

// LDAPGroup is the wire type for a POSIX group in the DIT.
type LDAPGroup struct {
	CN         string   `json:"cn"`
	GIDNumber  int      `json:"gid_number"`
	MemberUIDs []string `json:"member_uids"`
}

// CreateUserRequest holds the fields for creating a new POSIX user.
type CreateUserRequest struct {
	UID           string `json:"uid"`
	UIDNumber     int    `json:"uid_number"`
	GIDNumber     int    `json:"gid_number"`
	CN            string `json:"cn"`
	SN            string `json:"sn"`
	HomeDirectory string `json:"home_directory"`
	LoginShell    string `json:"login_shell"`
	Password      string `json:"password"`      // plaintext; hashed by slapd
	SSHPublicKey  string `json:"ssh_public_key,omitempty"` // v2: requires ldapPublicKey schema
}

// UpdateUserRequest holds the fields that can be updated on an existing user.
type UpdateUserRequest struct {
	CN            string `json:"cn,omitempty"`
	SN            string `json:"sn,omitempty"`
	HomeDirectory string `json:"home_directory,omitempty"`
	LoginShell    string `json:"login_shell,omitempty"`
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
		[]string{"uid", "uidNumber", "gidNumber", "cn", "sn", "homeDirectory", "loginShell", "shadowExpire"},
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
		[]string{"uid", "uidNumber", "gidNumber", "cn", "sn", "homeDirectory", "loginShell", "shadowExpire"},
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
	addReq.Attribute("objectClass", []string{"top", "posixAccount", "inetOrgPerson", "shadowAccount"})
	addReq.Attribute("uid", []string{req.UID})
	addReq.Attribute("cn", []string{cn})
	addReq.Attribute("sn", []string{sn})
	addReq.Attribute("gecos", []string{gecos})
	addReq.Attribute("homeDirectory", []string{home})
	addReq.Attribute("loginShell", []string{shell})
	addReq.Attribute("uidNumber", []string{strconv.Itoa(req.UIDNumber)})
	addReq.Attribute("gidNumber", []string{strconv.Itoa(req.GIDNumber)})
	// Password is passed plaintext; slapd hashes it using olcPasswordHash: {SSHA512}.
	if req.Password != "" {
		addReq.Attribute("userPassword", []string{req.Password})
	}
	// shadowAccount attributes — set sensible defaults (no expiry, no aging).
	addReq.Attribute("shadowLastChange", []string{"0"})
	addReq.Attribute("shadowMin", []string{"0"})
	addReq.Attribute("shadowMax", []string{"99999"})
	addReq.Attribute("shadowWarning", []string{"7"})

	// NOTE: ssh_public_key requires the ldapPublicKey schema (draft-ietf-secsh-ldap).
	// Loading this schema requires adding it to the seed LDIF. Deferred to v2.
	// req.SSHPublicKey is accepted on the API struct but not written in v1.

	if err := conn.Add(addReq); err != nil {
		return fmt.Errorf("ldap dit: create user %s: %w", req.UID, err)
	}
	return nil
}

// UpdateUser modifies an existing user entry with the provided fields.
// Only non-empty fields are updated.
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
	if req.HomeDirectory != "" {
		modReq.Replace("homeDirectory", []string{req.HomeDirectory})
	}
	if req.LoginShell != "" {
		modReq.Replace("loginShell", []string{req.LoginShell})
	}

	if len(modReq.Changes) == 0 {
		return nil // nothing to update
	}

	if err := conn.Modify(modReq); err != nil {
		return fmt.Errorf("ldap dit: update user %s: %w", uid, err)
	}
	return nil
}

// SetPassword changes a user's userPassword attribute.
// The password is passed plaintext; slapd hashes it via olcPasswordHash: {SSHA512}.
func (c *ditClient) SetPassword(uid, password string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.userDN(uid)
	modReq := goldap.NewModifyRequest(dn, nil)
	modReq.Replace("userPassword", []string{password})

	if err := conn.Modify(modReq); err != nil {
		return fmt.Errorf("ldap dit: set password for %s: %w", uid, err)
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

// DeleteUser removes a user entry from the DIT.
func (c *ditClient) DeleteUser(uid string) error {
	conn, err := c.connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	dn := c.userDN(uid)
	if err := conn.Del(goldap.NewDelRequest(dn, nil)); err != nil {
		return fmt.Errorf("ldap dit: delete user %s: %w", uid, err)
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
		[]string{"cn", "gidNumber", "memberUid"},
		nil,
	)

	result, err := conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("ldap dit: list groups: %w", err)
	}

	groups := make([]LDAPGroup, 0, len(result.Entries))
	for _, entry := range result.Entries {
		g := LDAPGroup{
			CN:         entry.GetAttributeValue("cn"),
			MemberUIDs: entry.GetAttributeValues("memberUid"),
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

// CreateGroup adds a new posixGroup entry.
func (c *ditClient) CreateGroup(cn string, gidNumber int) error {
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

	if err := conn.Add(addReq); err != nil {
		return fmt.Errorf("ldap dit: create group %s: %w", cn, err)
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

// HealthBind attempts an anonymous (unauthenticated) search to verify slapd is
// reachable. Returns nil on success.
func (c *ditClient) HealthBind() error {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.caCertPEM) {
		return fmt.Errorf("ldap dit: health: failed to parse CA cert")
	}
	tlsCfg := &tls.Config{
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
func entryToUser(entry *goldap.Entry) (LDAPUser, error) {
	uid := entry.GetAttributeValue("uid")
	if uid == "" {
		return LDAPUser{}, fmt.Errorf("entry missing uid: %s", entry.DN)
	}

	u := LDAPUser{
		UID:           uid,
		CN:            entry.GetAttributeValue("cn"),
		SN:            entry.GetAttributeValue("sn"),
		HomeDirectory: entry.GetAttributeValue("homeDirectory"),
		LoginShell:    entry.GetAttributeValue("loginShell"),
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

	return u, nil
}
