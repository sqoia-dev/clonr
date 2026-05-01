// manager.go — LDAP module Manager: lifecycle (Enable/Disable), health checks,
// background workers, and the NodeConfig projection used by the deploy pipeline.
package ldap

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"
	goldap "github.com/go-ldap/ldap/v3"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/posixid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// statusProvisioning / statusReady etc. mirror the DB status column values.
const (
	statusDisabled     = "disabled"
	statusProvisioning = "provisioning"
	statusReady        = "ready"
	statusError        = "error"
)

// LDAPHubIface is the subset of ClientdHub needed by the LDAP manager for
// config fanout. Defined here as an interface to avoid an import cycle between
// the ldap and server packages. The concrete *server.ClientdHub satisfies it.
type LDAPHubIface interface {
	Send(nodeID string, msg clientd.ServerMessage) error
	IsConnected(nodeID string) bool
	RegisterAck(msgID string) <-chan clientd.AckPayload
	UnregisterAck(msgID string)
}

// certExpiryWarnDays — warn when the server cert expires within this many days.
const certExpiryWarnDays = 30

// Manager owns the LDAP module lifecycle and provides the API surface for
// users/groups/status. It is safe for concurrent use.
type Manager struct {
	cfg       config.ServerConfig
	db        *db.DB
	audit     *db.AuditService
	allocator *posixid.Allocator
	mu        sync.RWMutex

	// In-memory DM password — set on Enable(), cleared on Disable().
	// Never persisted; the DB only stores its bcrypt hash.
	adminPassword string

	// Cached service bind password (in-memory copy for DIT operations).
	// The plaintext is also stored in the DB row (v1 limitation — see migration comment).
	servicePassword string

	// slapdUser is the OS system user that slapd runs as.
	// On EL (Rocky/RHEL/AlmaLinux/CentOS): "ldap".
	// On Debian/Ubuntu: "openldap".
	// Detected during Enable() by EnsureOpenLDAP() and threaded into all chown calls.
	slapdUser string

	// projectPluginCfg is the OpenLDAP project plugin configuration (G1 / CF-24).
	// Zero value uses defaultProjectPluginConfig() defaults (set in New()).
	projectPluginCfg ProjectPluginConfig

	// hub is the optional clientd hub for fanout pushes. Set after construction
	// via SetHub(). When nil, fanout operations are skipped (dev/test mode).
	hub LDAPHubIface
}

// SetHub wires the clientd hub into the LDAP manager so Enable() can fanout
// CA cert and sssd.conf pushes to enrolled nodes after a CA rotation.
// Call this after server.New() from buildRouter (mirrors the slurm pattern).
func (m *Manager) SetHub(hub LDAPHubIface) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hub = hub
}

// New creates a new LDAP Manager. Call StartBackgroundWorkers to start health checks.
func New(cfg config.ServerConfig, database *db.DB) *Manager {
	m := &Manager{
		cfg:              cfg,
		db:               database,
		audit:            db.NewAuditService(database),
		projectPluginCfg: defaultProjectPluginConfig(),
	}

	// Wire up the posixid allocator. The LDAP UID/GID listers are closures that
	// read live LDAP entries via the reader DIT on every allocation call.
	// If LDAP is not yet enabled, ListLDAPUIDs/GIDs return nil gracefully.
	src := &posixid.DBLDAPSource{
		DB: database,
		UIDs: func(ctx context.Context) ([]int, error) {
			dit, err := m.ReaderDIT(ctx)
			if err != nil {
				return nil, nil // LDAP not ready — skip; collision check still uses sys_accounts
			}
			users, err := dit.ListUsers()
			if err != nil {
				return nil, nil
			}
			uids := make([]int, 0, len(users))
			for _, u := range users {
				if u.UIDNumber > 0 {
					uids = append(uids, u.UIDNumber)
				}
			}
			return uids, nil
		},
		GIDs: func(ctx context.Context) ([]int, error) {
			dit, err := m.ReaderDIT(ctx)
			if err != nil {
				return nil, nil
			}
			groups, err := dit.ListGroups()
			if err != nil {
				return nil, nil
			}
			gids := make([]int, 0, len(groups))
			for _, g := range groups {
				if g.GIDNumber > 0 {
					gids = append(gids, g.GIDNumber)
				}
			}
			return gids, nil
		},
	}
	m.allocator = posixid.New(src)

	// Restore in-memory passwords from DB on startup if module is ready.
	m.restoreInMemoryPasswords(context.Background())
	return m
}

// restoreInMemoryPasswords loads the service and admin bind passwords from the DB
// on startup so that health checks and DIT operations work without an Enable() call.
// Prior to migration 028, admin_passwd was empty for existing installs and the
// "not in memory" error remained reachable until one re-Enable(); post-migration
// it is populated on every Enable() and survives restarts.
func (m *Manager) restoreInMemoryPasswords(ctx context.Context) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return // DB not yet migrated or module never enabled — fine
	}
	if row.Status == statusReady || row.Status == statusError {
		m.mu.Lock()
		m.servicePassword = row.ServiceBindPassword
		m.adminPassword = row.AdminPasswd // may be "" on pre-028 rows; DIT() guards against this
		m.mu.Unlock()
	}
}

// ─── Enable ───────────────────────────────────────────────────────────────────

// EnableRequest is the body for POST /api/v1/ldap/enable.
type EnableRequest struct {
	BaseDN        string `json:"base_dn"`
	AdminPassword string `json:"admin_password"`
}

// Enable provisions slapd: generates certs, seeds cn=config, starts the service,
// and seeds the data DIT. It is idempotent — calling it during an ongoing
// provisioning is a no-op (returns current status).
//
// This method performs privileged operations (slapadd, systemctl mask, cert
// writes to /etc/clustr/) and MUST be called with root privileges.
func (m *Manager) Enable(ctx context.Context, req EnableRequest) error {
	if req.BaseDN == "" {
		return fmt.Errorf("ldap: base_dn is required")
	}
	if req.AdminPassword == "" {
		return fmt.Errorf("ldap: admin_password is required")
	}

	// Validate base_dn format.
	if _, _, err := parseDCComponents(req.BaseDN); err != nil {
		return err
	}

	// Check current state.
	current, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("ldap: read module config: %w", err)
	}

	// Idempotent: if provisioning is already in progress, return without action.
	if current.Status == statusProvisioning {
		return nil
	}

	// If the base DN is locked, reject any attempt to change it.
	if current.BaseDNLocked && current.BaseDN != "" && current.BaseDN != req.BaseDN {
		return fmt.Errorf("ldap: base_dn is locked after first node was provisioned; current base DN is %q", current.BaseDN)
	}

	// If the module is already enabled and a base_dn change is requested,
	// reject if any nodes are already configured.
	if current.Enabled && current.BaseDN != "" && current.BaseDN != req.BaseDN {
		count, err := m.db.LDAPCountConfiguredNodes(ctx)
		if err != nil {
			return fmt.Errorf("ldap: count configured nodes: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("ldap: base_dn cannot be changed after nodes have been configured (found %d configured nodes)", count)
		}
	}

	// Transition to provisioning.
	if err := m.db.LDAPSetStatus(ctx, statusProvisioning, "starting provisioning"); err != nil {
		return fmt.Errorf("ldap: set provisioning status: %w", err)
	}

	// Run provisioning asynchronously so the HTTP handler can return 202.
	go m.doProvision(context.Background(), req)
	return nil
}

// doProvision runs the full provisioning sequence in a background goroutine.
// Sets status=ready or status=error when complete.
func (m *Manager) doProvision(ctx context.Context, req EnableRequest) {
	dc1, dc2, _ := parseDCComponents(req.BaseDN) // already validated

	setError := func(detail string) {
		if err := m.db.LDAPSetStatus(ctx, statusError, detail); err != nil {
			log.Error().Err(err).Msg("ldap: failed to set error status")
		}
	}

	// Read the existing config row so we can reuse the service bind password if
	// one was already generated on a prior Enable() attempt (Part A of Code-49 fix).
	existingRow, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		setError(fmt.Sprintf("read existing config: %v", err))
		return
	}
	existingServicePasswd := existingRow.ServiceBindPassword

	ldapCfg := m.cfg.LDAPConfigDir
	pkiDir := m.cfg.LDAPPKIDir
	ldapDataDir := m.cfg.LDAPDataDir

	// ── Step 0: Ensure openldap-servers is installed ──────────────────────────
	log.Info().Msg("ldap: step 0/6: ensuring openldap-servers is installed")
	_ = m.db.LDAPSetStatus(ctx, statusProvisioning, "Installing openldap-servers...")

	slapdUser, err := EnsureOpenLDAP(ctx)
	if err != nil {
		setError(fmt.Sprintf("openldap-servers install failed: %v", err))
		return
	}

	// Store the detected slapd system user on the Manager for this session.
	m.mu.Lock()
	m.slapdUser = slapdUser
	m.mu.Unlock()

	log.Info().Str("slapd_user", slapdUser).Msg("ldap: openldap-servers ready")

	// MaskDistroSlapd must run AFTER the package lands (the unit doesn't exist until then).
	if err := MaskDistroSlapd(ctx); err != nil {
		log.Warn().Err(err).Msg("ldap: mask distro slapd (non-fatal)")
	}

	// Install the clustr-slapd systemd unit, then daemon-reload.
	// Runs after MaskDistroSlapd so the single reload picks up both changes.
	log.Info().Msg("ldap: step 0b/6: installing systemd unit")
	_ = m.db.LDAPSetStatus(ctx, statusProvisioning, "Installing systemd unit...")

	if err := EnsureSystemdUnit(ctx); err != nil {
		setError(fmt.Sprintf("install systemd unit failed: %v", err))
		return
	}

	// ── Step 0c: Ensure clustr parent dirs are world-traversable ──────────────
	// All directories on the path to slapd's data dir must be 0755 so the
	// unprivileged slapd daemon (running as uid "ldap" or "openldap") can
	// traverse them. A partial prior install may have left /var/lib/clustr/ldap
	// at 0750 root:clustr (from the RPM post-install scriptlet) — that blocks
	// traversal entirely because the "ldap" OS user is not in the "clustr" group.
	// The fix: always chmod each parent to 0755 before proceeding.
	//
	// Security posture: slapd needs only execute (traverse) on these directories.
	// The data dir itself (/var/lib/clustr/ldap/data) is 0700 chowned to slapdUser,
	// so slapd's mdb files are still exclusively accessible to slapd.
	log.Info().Msg("ldap: step 0c/6: ensuring parent dir permissions")
	for _, d := range []string{
		"/etc/clustr",
		"/etc/clustr/ldap",
		"/var/lib/clustr",
		ldapDataDir, // /var/lib/clustr/ldap — may be 0750 from RPM post-install; must be 0755
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			setError(fmt.Sprintf("mkdir %s failed: %v", d, err))
			return
		}
		if err := os.Chmod(d, 0o755); err != nil {
			setError(fmt.Sprintf("chmod %s failed: %v", d, err))
			return
		}
	}

	// Validate the data dir is now traversable as the slapd system user.
	// If stat fails after the explicit chmod above, something deeper is wrong
	// (e.g. SELinux, stacked permissions, filesystem issue) and we must fail fast
	// rather than letting slapd crash-loop later with a cryptic permission error.
	if slapdUser != "" {
		if err := validateDataDirTraversable(ldapDataDir, slapdUser); err != nil {
			setError(fmt.Sprintf("permission_denied_data_dir: %v — "+
				"remediation: verify SELinux context (restorecon -Rv /var/lib/clustr/ldap) "+
				"or check for ACLs blocking %s; data dir must be executable by the slapd user", err, slapdUser))
			return
		}
	}

	// Stop any running slapd from a prior attempt so the fresh start picks up new certs.
	log.Info().Msg("ldap: stopping any prior slapd instance")
	StopSlapd(ctx)

	// ── Step 1: Generate certificates ────────────────────────────────────────
	log.Info().Msg("ldap: step 1/6: generating certificates")
	_ = m.db.LDAPSetStatus(ctx, statusProvisioning, "generating certificates")

	hostname := detectHostname()
	primaryIP := detectPrimaryIP()

	caBundle, caKey, caCert, err := generateCA(fmt.Sprintf("clustr LDAP CA (%s)", dc1))
	if err != nil {
		setError(fmt.Sprintf("cert generation failed: %v", err))
		return
	}

	serverBundle, err := generateServerCert(hostname, primaryIP, caKey, caCert)
	if err != nil {
		setError(fmt.Sprintf("server cert generation failed: %v", err))
		return
	}

	// ── Step 2: Write cert files ──────────────────────────────────────────────
	log.Info().Msg("ldap: step 2/6: writing config and certificates")
	_ = m.db.LDAPSetStatus(ctx, statusProvisioning, "writing config")

	if err := WriteCACert(pkiDir, ldapCfg, caBundle.CertPEM, caBundle.KeyPEM); err != nil {
		setError(fmt.Sprintf("write CA cert failed: %v", err))
		return
	}
	if err := WriteServerCert(ldapCfg, serverBundle.CertPEM, serverBundle.KeyPEM, slapdUser); err != nil {
		setError(fmt.Sprintf("write server cert failed: %v", err))
		return
	}

	// Register CA in system trust store (non-fatal).
	if err := UpdateCATrust(ctx, caBundle.CertPEM); err != nil {
		log.Warn().Err(err).Msg("ldap: update-ca-trust failed (non-fatal)")
	}

	// Create data directory, using the detected slapd system user for ownership.
	dataDir := ldapDataDir + "/data"
	if err := CreateDataDir(ctx, dataDir, slapdUser); err != nil {
		setError(fmt.Sprintf("create data dir failed: %v", err))
		return
	}

	// Generate service account password — reuse the existing one if already persisted.
	// On the first Enable() the column is empty and we generate a fresh password.
	// On subsequent Enable() retries we keep the same value so the DB row and the
	// LDAP entry's userPassword hash stay in sync (Part A of the Code-49 fix).
	svcPasswd := existingServicePasswd
	if svcPasswd == "" {
		svcPasswd, err = generateRandomPassword(24)
		if err != nil {
			setError(fmt.Sprintf("generate service password: %v", err))
			return
		}
	}

	// Admin password bcrypt hash for DB audit field.
	adminHash, err := bcrypt.GenerateFromPassword([]byte(req.AdminPassword), 12)
	if err != nil {
		setError(fmt.Sprintf("hash admin password: %v", err))
		return
	}

	// Resolve paths.
	caCertPath := ldapCfg + "/tls/ca.crt"
	serverCertPath := ldapCfg + "/tls/server.crt"
	serverKeyPath := ldapCfg + "/tls/server.key"
	configDir := ldapCfg + "/slapd.d"

	seedData := slapdSeedData{
		BaseDN:          req.BaseDN,
		DC1:             dc1,
		DC2:             dc2,
		ConfigDir:       configDir,
		DataDir:         dataDir,
		CACertPath:      caCertPath,
		ServerCertPath:  serverCertPath,
		ServerKeyPath:   serverKeyPath,
		AdminPassword:   req.AdminPassword, // plaintext; slapd hashes via olcPasswordHash: {CRYPT}
		ServicePassword: svcPasswd,
		SlapdUser:       slapdUser,
	}

	if err := SeedConfig(ctx, seedData); err != nil {
		setError(fmt.Sprintf("slapadd config seed failed: %v", err))
		return
	}

	// ── Step 3: Start slapd ───────────────────────────────────────────────────
	log.Info().Msg("ldap: step 3/6: starting slapd")
	_ = m.db.LDAPSetStatus(ctx, statusProvisioning, "starting slapd")

	if err := EnableSlapdService(ctx); err != nil {
		setError(fmt.Sprintf("enable slapd service failed: %v", err))
		return
	}
	if err := StartSlapd(ctx); err != nil {
		setError(fmt.Sprintf("start slapd failed: %v", err))
		return
	}

	// ── Step 4: Wait for slapd readiness ─────────────────────────────────────
	serverURI := "ldaps://127.0.0.1:636"
	adminBindDN := fmt.Sprintf("cn=Directory Manager,%s", req.BaseDN)

	dit := &ditClient{
		serverURI:  serverURI,
		bindDN:     adminBindDN,
		bindPasswd: req.AdminPassword,
		baseDN:     req.BaseDN,
		caCertPEM:  caBundle.CertPEM,
	}

	var slapdReady bool
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		if err := dit.HealthBind(); err == nil {
			slapdReady = true
			break
		}
		log.Info().Int("attempt", i+1).Msg("ldap: waiting for slapd to be ready...")
	}
	if !slapdReady {
		setError("slapd did not become ready within 30 seconds")
		return
	}

	// ── Step 5: Seed the data DIT ─────────────────────────────────────────────
	log.Info().Msg("ldap: step 5/6: seeding DIT")
	_ = m.db.LDAPSetStatus(ctx, statusProvisioning, "seeding DIT")

	if err := m.seedDIT(ctx, dit, req.BaseDN, svcPasswd); err != nil {
		setError(fmt.Sprintf("DIT seed failed: %v", err))
		return
	}

	// ── Step 6: Persist config to DB ─────────────────────────────────────────
	log.Info().Msg("ldap: step 6/6: saving config to database")
	serviceBindDN := fmt.Sprintf("cn=node-reader,ou=services,%s", req.BaseDN)

	dbCfg := db.LDAPModuleConfig{
		Enabled:             true,
		Status:              statusReady,
		StatusDetail:        "",
		BaseDN:              req.BaseDN,
		CACertPEM:           string(caBundle.CertPEM),
		CAKeyPEM:            string(caBundle.KeyPEM),
		CACertFingerprint:   caBundle.Fingerprint,
		ServerCertPEM:       string(serverBundle.CertPEM),
		ServerKeyPEM:        string(serverBundle.KeyPEM),
		ServerCertNotAfter:  serverBundle.NotAfter,
		AdminPasswordHash:   string(adminHash),
		ServiceBindDN:       serviceBindDN,
		ServiceBindPassword: svcPasswd,
		// AdminPasswd persists the plaintext DM password (migration 028).
		// Same threat model as ServiceBindPassword — SQLite file-permission protected.
		// Future hardening: encrypt both at rest in a coordinated pass.
		AdminPasswd:         req.AdminPassword,
		LastProvisionedAt:   time.Now(),
	}

	if err := m.db.LDAPSaveConfig(ctx, dbCfg); err != nil {
		setError(fmt.Sprintf("save config to DB: %v", err))
		return
	}

	// Store in-memory passwords.
	m.mu.Lock()
	m.adminPassword = req.AdminPassword
	m.servicePassword = svcPasswd
	m.mu.Unlock()

	log.Info().Str("base_dn", req.BaseDN).Msg("ldap: module enabled and ready")

	// ── Step 7: Fanout new CA + sssd.conf to enrolled nodes ──────────────────
	// When the operator does Disable+wipe+Enable (CA rotation), any previously
	// deployed nodes have a stale CA and stale sssd.conf (new service bind
	// password). Push the updated files to every connected enrolled node so
	// they recover without waiting for the next reimage cycle.
	// Non-blocking: runs in the same goroutine after DB save so the result is
	// available for logging, but any per-node failure is non-fatal.
	m.mu.RLock()
	hub := m.hub
	m.mu.RUnlock()

	if hub != nil {
		results := m.FanoutLDAPConfig(ctx)
		okCount := 0
		for _, r := range results {
			if r.OK {
				okCount++
			}
		}
		log.Info().
			Int("nodes_pushed", okCount).
			Int("nodes_total", len(results)).
			Msg("ldap: post-enable fanout complete")
	} else {
		log.Debug().Msg("ldap: no hub wired — skipping post-enable fanout (dev/test mode)")
	}
}

// seedDIT creates the base DN entry, standard OUs, and the node-reader service
// account directly via go-ldap AddRequest calls. It is idempotent: entries that
// already exist (ldap.ResultEntryAlreadyExists) are silently skipped, so
// re-running Enable() does not fail.
//
// GAP-S18-1: This function also creates cn=clustr-admins,ou=groups during every
// Enable() so the sudoers group is always present before any GrantSudo call.
// GAP-S18-2: If the legacy cn=clonr-admins group is found in the DIT and
// cn=clustr-admins is absent, a ModifyDN rename is performed so existing
// installs upgrade without operator intervention.
func (m *Manager) seedDIT(ctx context.Context, dit *ditClient, baseDN, servicePassword string) error {
	conn, err := dit.connect()
	if err != nil {
		return fmt.Errorf("ldap: seed DIT: connect as admin: %w", err)
	}
	defer conn.Close()

	// Parse DC components for the base DN entry.
	dc1, _, err := parseDCComponents(baseDN)
	if err != nil {
		return fmt.Errorf("ldap: seed DIT: %w", err)
	}

	// Allocate a GID for the clustr-admins sudoers group.
	// AllocateGID is safe to call during provisioning: the LDAP GID lister
	// returns nil,nil when LDAP is not yet in statusReady, so the allocator
	// falls back to DB-only collision checking against system_groups. At seed
	// time the LDAP DIT is empty, so the allocated GID is always fresh.
	// #113: LDAP groups use RoleLDAPUser range (10000-60000).
	sudoersGID, err := m.allocator.AllocateGID(ctx, posixid.RoleLDAPUser)
	if err != nil {
		// Non-fatal for the overall seed: fall back to a known-safe default so
		// that the DIT seed can proceed. The SeedSudoersGroup call below will
		// attempt LDAP and produce a cleaner error if this GID collides.
		log.Warn().Err(err).Msg("ldap: seed DIT: AllocateGID failed, using fallback GID 10001 for clustr-admins")
		sudoersGID = 10001
	}

	type seedEntry struct {
		dn    string
		attrs map[string][]string
		// credentialed marks entries whose userPassword must be kept in sync with the
		// DB on every seed run. On EntryAlreadyExists, a Modify REPLACE is issued
		// instead of silently skipping, so repeated Enable() calls cannot leave the
		// LDAP entry with a stale hash while the DB holds a newer plaintext password.
		credentialed bool
	}

	entries := []seedEntry{
		{
			dn: baseDN,
			attrs: map[string][]string{
				"objectClass": {"top", "dcObject", "organization"},
				"dc":          {dc1},
				"o":           {dc1},
			},
		},
		{
			dn: fmt.Sprintf("ou=people,%s", baseDN),
			attrs: map[string][]string{
				"objectClass": {"top", "organizationalUnit"},
				"ou":          {"people"},
			},
		},
		{
			dn: fmt.Sprintf("ou=groups,%s", baseDN),
			attrs: map[string][]string{
				"objectClass": {"top", "organizationalUnit"},
				"ou":          {"groups"},
			},
		},
		{
			dn: fmt.Sprintf("ou=services,%s", baseDN),
			attrs: map[string][]string{
				"objectClass": {"top", "organizationalUnit"},
				"ou":          {"services"},
			},
		},
		{
			dn: fmt.Sprintf("cn=node-reader,ou=services,%s", baseDN),
			attrs: map[string][]string{
				"objectClass":  {"top", "simpleSecurityObject", "organizationalRole"},
				"cn":           {"node-reader"},
				"description":  {"clustr node read-only service account (managed by clustr-serverd)"},
				"userPassword": {servicePassword},
			},
			credentialed: true,
		},
		// ── ppolicy container and default policy ──────────────────────────────
		// The policy entry lives in the DIT (-n 1), not cn=config, so it is
		// seeded here via go-ldap. The overlay entry in the LDIF seed points to
		// cn=default,ou=policies,<baseDN>.
		{
			dn: fmt.Sprintf("ou=policies,%s", baseDN),
			attrs: map[string][]string{
				"objectClass": {"top", "organizationalUnit"},
				"ou":          {"policies"},
			},
		},
		{
			// objectClass: device is the structural class (cn is its only
			// mandatory attribute). pwdPolicy is auxiliary and contributes
			// the pwd* attributes. This is the canonical pattern when no
			// other structural class is appropriate.
			dn: fmt.Sprintf("cn=default,ou=policies,%s", baseDN),
			attrs: map[string][]string{
				"objectClass":            {"top", "device", "pwdPolicy"},
				"cn":                     {"default"},
				"pwdAttribute":           {"userPassword"},
				"pwdMustChange":          {"TRUE"},
				"pwdAllowUserChange":     {"TRUE"},
				"pwdInHistory":           {"0"},
				"pwdMinAge":              {"0"},
				"pwdMaxAge":              {"0"},
				"pwdMinLength":           {"8"},
				"pwdLockout":             {"TRUE"},
				"pwdLockoutDuration":     {"0"}, // permanent until admin unlocks
				"pwdMaxFailure":          {"5"},
				"pwdFailureCountInterval": {"300"},
				"pwdGraceAuthnLimit":     {"0"},
				"pwdCheckQuality":        {"0"},
				"pwdExpireWarning":       {"0"},
				"pwdSafeModify":          {"FALSE"},
			},
		},
		// ── GAP-S18-1: clustr-admins sudoers group ────────────────────────────
		// Seeded here so the group always exists before any GrantSudo call.
		// Without this, POST /api/v1/ldap/sudoers/members returns LDAP Error 32
		// "No Such Object" because AddGroupMember targets a DN that was never created.
		// The GID is allocated from the posixid allocator — allocator-consistent
		// and safe to call during provisioning (LDAP GID lister returns nil on
		// statusProvisioning, so no collision check against live LDAP is needed;
		// the DB system_groups table is still consulted for correctness).
		{
			dn: fmt.Sprintf("cn=%s,ou=groups,%s", sudoersDefaultGroupCN, baseDN),
			attrs: map[string][]string{
				"objectClass": {"top", "posixGroup"},
				"cn":          {sudoersDefaultGroupCN},
				"gidNumber":   {strconv.Itoa(sudoersGID)},
				"description": {"Members of this group have full sudo on cluster nodes"},
			},
		},
	}

	for _, e := range entries {
		req := goldap.NewAddRequest(e.dn, nil)
		for attr, vals := range e.attrs {
			req.Attribute(attr, vals)
		}
		if err := conn.Add(req); err != nil {
			if goldap.IsErrorWithCode(err, goldap.LDAPResultEntryAlreadyExists) {
				if e.credentialed {
					// Self-heal: the entry exists but its userPassword hash may be from
					// an earlier Enable() run. Use writeServiceAccountPassword so that
					// the single atomic Modify also clears any ppolicy state (pwdReset,
					// pwdAccountLockedTime, pwdFailureTime) that ppolicy auto-set when
					// the rootdn last touched this entry's userPassword.
					if modErr := writeServiceAccountPassword(conn, e.dn, servicePassword); modErr != nil {
						return fmt.Errorf("ldap: seed DIT: self-heal userPassword for %s: %w", e.dn, modErr)
					}
					log.Debug().Str("dn", e.dn).Msg("ldap: seed DIT: entry exists — userPassword and ppolicy state updated")
				} else {
					log.Debug().Str("dn", e.dn).Msg("ldap: seed DIT: entry already exists, skipping")
				}
				continue
			}
			return fmt.Errorf("ldap: seed DIT: add %s: %w", e.dn, err)
		}
		log.Debug().Str("dn", e.dn).Msg("ldap: seed DIT: entry created")

		// AddRequest cannot include Delete operations, so ppolicy's auto-set of
		// pwdReset:TRUE (triggered by the rootdn writing userPassword on Add) must
		// be cleared in a separate follow-up Modify. This is only needed for
		// credentialed service account entries.
		if e.credentialed {
			if modErr := writeServiceAccountPassword(conn, e.dn, servicePassword); modErr != nil {
				return fmt.Errorf("ldap: seed DIT: post-add ppolicy cleanup for %s: %w", e.dn, modErr)
			}
			log.Debug().Str("dn", e.dn).Msg("ldap: seed DIT: post-add ppolicy state cleared for service account")
		}
	}

	// GAP-S18-2: Rename legacy cn=clonr-admins group to cn=clustr-admins if present.
	// This handles installs that were provisioned before the clonr→clustr rename
	// (2026-04-25). The three-state logic:
	//   - only clonr-admins exists → rename it to clustr-admins via ModifyDN
	//   - both exist → warn and leave alone (operator manually created clustr-admins)
	//   - only clustr-admins exists → no-op (already seeded above)
	if err := m.migrateClonrAdminsGroup(conn, baseDN); err != nil {
		// Non-fatal: log the error but don't abort the seed. The sudoers group
		// was already seeded above; the rename is a best-effort upgrade path.
		log.Warn().Err(err).Msg("ldap: seed DIT: clonr-admins migration failed (non-fatal)")
	}

	log.Info().Msg("ldap: data DIT seeded (base DN + OUs + node-reader account + clustr-admins group)")
	return nil
}

// migrateClonrAdminsGroup renames cn=clonr-admins,ou=groups,<baseDN> to
// cn=clustr-admins if the old entry exists and the new one does not.
// This is a no-op when only cn=clustr-admins exists (already correct)
// or when both exist (operator managed — leave alone with a warning).
func (m *Manager) migrateClonrAdminsGroup(conn *goldap.Conn, baseDN string) error {
	const legacyCN = "clonr-admins"
	legacyDN := fmt.Sprintf("cn=%s,ou=groups,%s", legacyCN, baseDN)
	newDN := fmt.Sprintf("cn=%s,ou=groups,%s", sudoersDefaultGroupCN, baseDN)

	// Check legacy entry existence.
	legacyExists, err := ldapEntryExists(conn, legacyDN)
	if err != nil {
		return fmt.Errorf("ldap: migrate clonr-admins: search legacy: %w", err)
	}
	if !legacyExists {
		return nil // no legacy entry — nothing to migrate
	}

	// Legacy entry exists. Check if new entry already exists too.
	newExists, err := ldapEntryExists(conn, newDN)
	if err != nil {
		return fmt.Errorf("ldap: migrate clonr-admins: search new: %w", err)
	}

	if newExists {
		// Both exist — operator may have created cn=clustr-admins manually.
		// Leave both alone; log a warning so the operator can clean up.
		log.Warn().
			Str("legacy_dn", legacyDN).
			Str("new_dn", newDN).
			Msg("ldap: seed DIT: both cn=clonr-admins and cn=clustr-admins exist; leaving both in place — manually remove cn=clonr-admins after verifying membership is correct")
		return nil
	}

	// Only legacy exists: rename it.
	modDNReq := goldap.NewModifyDNRequest(legacyDN, fmt.Sprintf("cn=%s", sudoersDefaultGroupCN), true, "")
	if err := conn.ModifyDN(modDNReq); err != nil {
		return fmt.Errorf("ldap: migrate clonr-admins: ModifyDN %s → %s: %w", legacyDN, newDN, err)
	}
	log.Info().
		Str("from", legacyDN).
		Str("to", newDN).
		Msg("ldap: seed DIT: renamed legacy cn=clonr-admins to cn=clustr-admins")
	return nil
}

// ldapEntryExists returns true if an entry with the given DN exists in the DIT.
// Returns (false, nil) for LDAP No Such Object, (false, err) for other errors.
func ldapEntryExists(conn *goldap.Conn, dn string) (bool, error) {
	req := goldap.NewSearchRequest(
		dn,
		goldap.ScopeBaseObject,
		goldap.NeverDerefAliases,
		1, 0, false,
		"(objectClass=*)",
		[]string{"dn"},
		nil,
	)
	result, err := conn.Search(req)
	if err != nil {
		if goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject) {
			return false, nil
		}
		return false, err
	}
	return len(result.Entries) > 0, nil
}

// ─── Disable ──────────────────────────────────────────────────────────────────

// DisableMode controls what happens to the LDAP data on disable.
type DisableMode string

const (
	DisableModeDetach  DisableMode = "detach"  // stop slapd, preserve data
	DisableModeDestroy DisableMode = "destroy" // stop slapd, wipe data dir + config
)

// DisableRequest is the body for POST /api/v1/ldap/disable.
type DisableRequest struct {
	Mode              DisableMode `json:"confirm"`
	NodesAcknowledged bool        `json:"nodes_acknowledged"`
}

// AffectedNodesError is returned when nodes depend on LDAP and the operator
// has not acknowledged the impact.
type AffectedNodesError struct {
	NodeIDs []string
}

func (e *AffectedNodesError) Error() string {
	return fmt.Sprintf("ldap: %d node(s) are configured with LDAP; acknowledge by setting nodes_acknowledged=true", len(e.NodeIDs))
}

// Disable stops the LDAP module. If nodes are configured, returns AffectedNodesError
// unless NodesAcknowledged is true.
func (m *Manager) Disable(ctx context.Context, req DisableRequest) error {
	if req.Mode != DisableModeDetach && req.Mode != DisableModeDestroy {
		return fmt.Errorf("ldap: confirm must be 'detach' or 'destroy'")
	}

	current, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("ldap: read module config: %w", err)
	}
	if !current.Enabled {
		return nil // already disabled
	}

	// Check for affected nodes.
	if !req.NodesAcknowledged {
		nodeIDs, err := m.db.LDAPListConfiguredNodeIDs(ctx)
		if err != nil {
			return fmt.Errorf("ldap: list configured nodes: %w", err)
		}
		if len(nodeIDs) > 0 {
			return &AffectedNodesError{NodeIDs: nodeIDs}
		}
	}

	// Stop slapd (non-fatal — may already be stopped).
	if err := StopSlapd(ctx); err != nil {
		log.Warn().Err(err).Msg("ldap: stop slapd on disable (non-fatal)")
	}

	ldapCfg := m.cfg.LDAPConfigDir
	ldapData := m.cfg.LDAPDataDir

	if req.Mode == DisableModeDestroy {
		log.Warn().Msg("ldap: destroy mode — wiping slapd data dir and config")
		if err := os.RemoveAll(ldapData + "/data"); err != nil {
			log.Error().Err(err).Msg("ldap: wipe data dir (non-fatal)")
		}
		if err := os.RemoveAll(ldapCfg + "/slapd.d"); err != nil {
			log.Error().Err(err).Msg("ldap: wipe config dir (non-fatal)")
		}
	}

	// Clear in-memory passwords.
	m.mu.Lock()
	m.adminPassword = ""
	m.servicePassword = ""
	m.mu.Unlock()

	return m.db.LDAPDisable(ctx)
}

// ─── Status ───────────────────────────────────────────────────────────────────

// StatusResponse is the response for GET /api/v1/ldap/status.
type StatusResponse struct {
	Enabled             bool       `json:"enabled"`
	Status              string     `json:"status"`
	StatusDetail        string     `json:"status_detail"`
	BaseDN              string     `json:"base_dn"`
	BaseDNLocked        bool       `json:"base_dn_locked"`
	CAFingerprint       string     `json:"ca_fingerprint,omitempty"`
	ServerCertExpiresAt *time.Time `json:"server_cert_expires_at,omitempty"`
	CertExpiryWarning   bool       `json:"cert_expiry_warning"`
	// ConfigDrift is always false in v1. v2 will detect slapd cn=config changes.
	ConfigDrift         bool       `json:"config_drift"`
	ConfiguredNodeCount int        `json:"configured_node_count"`
}

// Status reads the current LDAP module state from the DB.
func (m *Manager) Status(ctx context.Context) (StatusResponse, error) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusResponse{Status: statusDisabled}, nil
		}
		return StatusResponse{}, fmt.Errorf("ldap: get status: %w", err)
	}

	count, _ := m.db.LDAPCountConfiguredNodes(ctx)

	resp := StatusResponse{
		Enabled:             row.Enabled,
		Status:              row.Status,
		StatusDetail:        row.StatusDetail,
		BaseDN:              row.BaseDN,
		BaseDNLocked:        row.BaseDNLocked,
		CAFingerprint:       row.CACertFingerprint,
		ConfiguredNodeCount: count,
	}

	if !row.ServerCertNotAfter.IsZero() {
		t := row.ServerCertNotAfter
		resp.ServerCertExpiresAt = &t
		if time.Until(t) < time.Duration(certExpiryWarnDays)*24*time.Hour {
			resp.CertExpiryWarning = true
		}
	}

	return resp, nil
}

// ─── NodeConfig projection ────────────────────────────────────────────────────

// NodeConfig returns the LDAPNodeConfig struct for injection into api.NodeConfig
// during the deploy pipeline. Returns nil if the module is not enabled or not ready.
func (m *Manager) NodeConfig(ctx context.Context) (*api.LDAPNodeConfig, error) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil || !row.Enabled || row.Status != statusReady {
		return nil, nil
	}

	m.mu.RLock()
	svcPasswd := m.servicePassword
	m.mu.RUnlock()

	// If in-memory password is empty (server restarted), fall back to DB value.
	if svcPasswd == "" {
		svcPasswd = row.ServiceBindPassword
	}

	// #111: use the server's primary IP (not hostname) in ldap_uri so fresh nodes
	// without DNS for "clustr-server" can authenticate on first boot.
	// The server cert SAN already includes the IP (Sprint 15 #101), so TLS validation
	// passes against an IP URI. Falls back to hostname if IP detection fails.
	serverAddr := detectPrimaryIP()
	if serverAddr == "" {
		serverAddr = detectHostname()
	}
	serverURI := fmt.Sprintf("ldaps://%s:636", serverAddr)

	return &api.LDAPNodeConfig{
		ServerURI:         serverURI,
		BaseDN:            row.BaseDN,
		ServiceBindDN:     row.ServiceBindDN,
		ServiceBindPasswd: svcPasswd,
		CACertPEM:         row.CACertPEM,
	}, nil
}

// ─── Background workers ───────────────────────────────────────────────────────

// StartBackgroundWorkers launches the health-check goroutine and the project plugin worker.
func (m *Manager) StartBackgroundWorkers(ctx context.Context) {
	go m.runHealthChecker(ctx)
	m.StartProjectPluginWorker(ctx)
}

// runHealthChecker ticks every 30 seconds and checks slapd reachability.
func (m *Manager) runHealthChecker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Info().Msg("ldap: health checker started")
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("ldap: health checker stopping")
			return
		case <-ticker.C:
			m.runHealthCheck(ctx)
		}
	}
}

func (m *Manager) runHealthCheck(ctx context.Context) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil || !row.Enabled || row.Status == statusProvisioning {
		return
	}

	caCertPEM := []byte(row.CACertPEM)
	if len(caCertPEM) == 0 {
		return
	}

	dit := &ditClient{
		serverURI: "ldaps://127.0.0.1:636",
		caCertPEM: caCertPEM,
	}

	checkErr := dit.HealthBind()
	now := time.Now()

	if checkErr != nil {
		detail := fmt.Sprintf("slapd unreachable: %v", checkErr)
		_ = m.db.LDAPUpdateHealthCheck(ctx, now, detail)
		if row.Status == statusReady {
			_ = m.db.LDAPSetStatus(ctx, statusError, detail)
		}
		log.Error().Err(checkErr).Msg("ldap: health check failed — slapd unreachable")
		return
	}

	_ = m.db.LDAPUpdateHealthCheck(ctx, now, "")

	// Restore ready status if it was in error.
	if row.Status == statusError {
		_ = m.db.LDAPSetStatus(ctx, statusReady, "")
	}

	// Cert expiry warning.
	if !row.ServerCertNotAfter.IsZero() && time.Until(row.ServerCertNotAfter) < time.Duration(certExpiryWarnDays)*24*time.Hour {
		log.Warn().
			Time("expires_at", row.ServerCertNotAfter).
			Msg("ldap: server certificate expiry warning — v2 will auto-renew; for now, manually re-run Enable()")
	}
}

// ─── DIT client factory ───────────────────────────────────────────────────────

// DIT constructs a ditClient for user/group operations, binding as Directory Manager.
// Returns an error if the module is not ready or the admin password is unavailable.
func (m *Manager) DIT(ctx context.Context) (*ditClient, error) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ldap: read config for DIT client: %w", err)
	}
	if !row.Enabled || row.Status != statusReady {
		return nil, fmt.Errorf("ldap: module is not ready (status=%s)", row.Status)
	}

	m.mu.RLock()
	adminPass := m.adminPassword
	m.mu.RUnlock()

	if adminPass == "" {
		return nil, fmt.Errorf("ldap: admin password not in memory — call Enable() to restore (server was restarted)")
	}

	return &ditClient{
		serverURI:  "ldaps://127.0.0.1:636",
		bindDN:     fmt.Sprintf("cn=Directory Manager,%s", row.BaseDN),
		bindPasswd: adminPass,
		baseDN:     row.BaseDN,
		caCertPEM:  []byte(row.CACertPEM),
	}, nil
}

// ReaderDIT constructs a ditClient that binds as the node-reader service account.
// Use this for all read-only operations (ListUsers, ListGroups, GetUser, GetGroup,
// group member fetches). The node-reader credentials are always persisted in the DB,
// so this never hits the "password not in memory" class of error.
func (m *Manager) ReaderDIT(ctx context.Context) (*ditClient, error) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("ldap: read config for reader DIT client: %w", err)
	}
	if !row.Enabled || row.Status != statusReady {
		return nil, fmt.Errorf("ldap: module is not ready (status=%s)", row.Status)
	}

	m.mu.RLock()
	svcPasswd := m.servicePassword
	m.mu.RUnlock()

	// Fall back to DB value if the in-memory copy is empty (defensive; should not
	// happen post-restoreInMemoryPasswords, but guards against edge cases).
	if svcPasswd == "" {
		svcPasswd = row.ServiceBindPassword
	}

	return &ditClient{
		serverURI:  "ldaps://127.0.0.1:636",
		bindDN:     row.ServiceBindDN,
		bindPasswd: svcPasswd,
		baseDN:     row.BaseDN,
		caCertPEM:  []byte(row.CACertPEM),
	}, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// generateRandomPassword generates a cryptographically random alphanumeric password.
func generateRandomPassword(length int) (string, error) {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = chars[b%byte(len(chars))]
	}
	return string(buf), nil
}

// detectHostname returns the system hostname, or "localhost" on error.
func detectHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	return h
}

// detectPrimaryIP returns the primary IP of the system via hostname resolution.
// Falls back to empty string if detection fails.
func detectPrimaryIP() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	addrs, err := net.LookupHost(h)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

// ConfigHashForNode computes the SHA-256 hash of a rendered sssd.conf for drift detection.
func ConfigHashForNode(hostname string, cfg *api.LDAPNodeConfig) (string, error) {
	if cfg == nil {
		return "", nil
	}
	tmpl, err := template.ParseFS(templateFS, "templates/sssd.conf.tmpl")
	if err != nil {
		return "", fmt.Errorf("ldap: parse sssd.conf template: %w", err)
	}
	var buf bytes.Buffer
	data := struct {
		Hostname          string
		Domain            string
		ServerURI         string
		BaseDN            string
		ServiceBindDN     string
		ServiceBindPasswd string
	}{
		Hostname:          hostname,
		Domain:            domainFromBaseDN(cfg.BaseDN),
		ServerURI:         cfg.ServerURI,
		BaseDN:            cfg.BaseDN,
		ServiceBindDN:     cfg.ServiceBindDN,
		ServiceBindPasswd: cfg.ServiceBindPasswd,
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("ldap: render sssd.conf for hash: %w", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// marshalConfigPushPayload encodes a clientd.ConfigPushPayload as raw JSON bytes
// for inclusion as a ServerMessage.Payload field. Kept here to avoid importing
// encoding/json twice in callers that already have it.
func marshalConfigPushPayload(target, content, checksum string) (json.RawMessage, error) {
	p := clientd.ConfigPushPayload{
		Target:   target,
		Content:  content,
		Checksum: checksum,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// renderFanoutSSSDConf renders sssd.conf content for a push to an already-deployed node.
// Identical to deploy/finalize.go:renderSSSDConf but lives here to avoid cross-package
// dependency. Both must stay in sync — if the template changes, update both.
func renderFanoutSSSDConf(hostname, domain, serverURI, baseDN, serviceBindDN, serviceBindPasswd string) string {
	return fmt.Sprintf(`# sssd.conf — generated by clustr-serverd for node %s
# DO NOT EDIT — managed by clustr. Regenerated on each reimage.

[sssd]
services = nss, pam
domains = %s

[nss]
homedir_substring = /home

[pam]
offline_credentials_expiration = 7

[domain/%s]
id_provider = ldap
auth_provider = ldap
chpass_provider = ldap
access_provider = ldap

ldap_uri = %s
ldap_search_base = %s

ldap_default_bind_dn = %s
ldap_default_authtok_type = password
ldap_default_authtok = %s

ldap_user_object_class = posixAccount
ldap_user_search_base = ou=people,%s
ldap_user_name = uid
ldap_user_uid_number = uidNumber
ldap_user_gid_number = gidNumber
ldap_user_home_directory = homeDirectory
ldap_user_shell = loginShell
ldap_user_gecos = gecos
ldap_user_shadow_expire = shadowExpire

ldap_group_object_class = posixGroup
ldap_group_search_base = ou=groups,%s
ldap_group_name = cn
ldap_group_gid_number = gidNumber
ldap_group_member = memberUid

ldap_tls_reqcert = demand
ldap_tls_cacert = /etc/pki/ca-trust/source/anchors/clustr-ca.crt

ldap_account_expire_policy = shadow
ldap_access_order = ppolicy, expire
ldap_use_ppolicy = true
ldap_pwd_policy = none

ldap_id_use_start_tls = false
ldap_referrals = false
enumerate = false
cache_credentials = true
entry_cache_timeout = 300
`, hostname, domain, domain,
		serverURI, baseDN,
		serviceBindDN, serviceBindPasswd,
		baseDN, baseDN)
}

// domainFromBaseDN extracts the first DC component from a base DN.
// "dc=cluster,dc=local" → "cluster"
func domainFromBaseDN(baseDN string) string {
	parts := strings.SplitN(baseDN, ",", 2)
	if len(parts) > 0 {
		p := strings.TrimSpace(parts[0])
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "dc=") {
			return lower[3:]
		}
	}
	return baseDN
}

// NodePushResult records the outcome of a single node's config push attempt.
type NodePushResult struct {
	NodeID  string `json:"node_id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Offline bool   `json:"offline,omitempty"` // true when node is not connected
}

// FanoutLDAPConfig pushes the current CA cert and a per-node sssd.conf to every
// enrolled node that is currently connected. It fires after doProvision() completes
// so nodes recover immediately after a CA rotation without waiting for the next
// reimage cycle.
//
// Push order for each node:
//  1. "ldap-ca-cert"  — /etc/pki/ca-trust/source/anchors/clustr-ca.crt
//     Follow-up: update-ca-trust extract + systemctl restart sssd.
//  2. "sssd"          — /etc/sssd/sssd.conf
//     Follow-up: systemctl restart sssd.
//
// Both pushes fire sequentially per node; nodes are processed concurrently.
// Per-node results are returned for status reporting.
func (m *Manager) FanoutLDAPConfig(ctx context.Context) []NodePushResult {
	m.mu.RLock()
	hub := m.hub
	m.mu.RUnlock()

	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil || !row.Enabled || row.Status != statusReady {
		log.Warn().Msg("ldap fanout: module not ready — skipping push")
		return nil
	}

	nodeIDs, err := m.db.LDAPListConfiguredNodeIDs(ctx)
	if err != nil {
		log.Error().Err(err).Msg("ldap fanout: failed to list configured node IDs")
		return nil
	}
	if len(nodeIDs) == 0 {
		log.Info().Msg("ldap fanout: no enrolled nodes — nothing to push")
		return nil
	}

	m.mu.RLock()
	svcPasswd := m.servicePassword
	m.mu.RUnlock()
	if svcPasswd == "" {
		svcPasswd = row.ServiceBindPassword
	}

	serverIP := detectPrimaryIP()
	if serverIP == "" {
		// Fall back to hostname if IP detection fails — this should be rare.
		serverIP = detectHostname()
	}
	serverURI := fmt.Sprintf("ldaps://%s:636", serverIP)

	caCertPEM := row.CACertPEM
	results := make([]NodePushResult, 0, len(nodeIDs))
	var resultsMu sync.Mutex

	var wg sync.WaitGroup
	for _, nodeID := range nodeIDs {
		if hub == nil || !hub.IsConnected(nodeID) {
			resultsMu.Lock()
			results = append(results, NodePushResult{NodeID: nodeID, OK: false, Offline: true})
			resultsMu.Unlock()
			log.Info().Str("node_id", nodeID).Msg("ldap fanout: node offline — will receive config on reconnect")
			continue
		}

		wg.Add(1)
		go func(nid string) {
			defer wg.Done()
			res := m.pushLDAPToNode(ctx, hub, nid, row, caCertPEM, svcPasswd, serverURI)
			resultsMu.Lock()
			results = append(results, res)
			resultsMu.Unlock()
		}(nodeID)
	}
	wg.Wait()

	ok := 0
	for _, r := range results {
		if r.OK {
			ok++
		}
	}
	log.Info().
		Int("total", len(results)).
		Int("ok", ok).
		Int("offline", len(results)-ok).
		Msg("ldap fanout: complete")

	return results
}

// pushLDAPToNode sends the CA cert and sssd.conf to a single node sequentially.
// Returns a NodePushResult summarising the outcome.
func (m *Manager) pushLDAPToNode(
	ctx context.Context,
	hub LDAPHubIface,
	nodeID string,
	row db.LDAPModuleConfig,
	caCertPEM, svcPasswd, serverURI string,
) NodePushResult {
	// 1. Push CA cert.
	if err := m.sendConfigPush(ctx, hub, nodeID, "ldap-ca-cert", caCertPEM); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("ldap fanout: ca-cert push failed")
		return NodePushResult{NodeID: nodeID, OK: false, Error: "ca-cert push: " + err.Error()}
	}
	log.Info().Str("node_id", nodeID).Msg("ldap fanout: ca-cert push acked")

	// 2. Push sssd.conf (per-node — uses node's own hostname from DB).
	nodeHostname := nodeID // fallback when DB lookup fails
	if nodeCfg, err := m.db.GetNodeConfig(ctx, nodeID); err == nil && nodeCfg.Hostname != "" {
		nodeHostname = nodeCfg.Hostname
	}

	domain := domainFromBaseDN(row.BaseDN)
	sssdContent := renderFanoutSSSDConf(
		nodeHostname, domain, serverURI,
		row.BaseDN, row.ServiceBindDN, svcPasswd,
	)
	if err := m.sendConfigPush(ctx, hub, nodeID, "sssd", sssdContent); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("ldap fanout: sssd.conf push failed")
		return NodePushResult{NodeID: nodeID, OK: false, Error: "sssd push: " + err.Error()}
	}
	log.Info().Str("node_id", nodeID).Msg("ldap fanout: sssd.conf push acked")

	return NodePushResult{NodeID: nodeID, OK: true}
}

// sendConfigPush sends a single config_push message to a node and waits for ack.
// Times out after 30 seconds per node.
func (m *Manager) sendConfigPush(ctx context.Context, hub LDAPHubIface, nodeID, target, content string) error {
	sum := sha256.Sum256([]byte(content))
	checksum := fmt.Sprintf("sha256:%x", sum)

	msgID := uuid.New().String()

	payloadJSON, err := marshalConfigPushPayload(target, content, checksum)
	if err != nil {
		return fmt.Errorf("marshal config_push payload: %w", err)
	}

	serverMsg := clientd.ServerMessage{
		Type:    "config_push",
		MsgID:   msgID,
		Payload: payloadJSON,
	}

	ackCh := hub.RegisterAck(msgID)
	defer hub.UnregisterAck(msgID)

	if err := hub.Send(nodeID, serverMsg); err != nil {
		return fmt.Errorf("send to node: %w", err)
	}

	select {
	case ack := <-ackCh:
		if !ack.OK {
			return fmt.Errorf("node ack error: %s", ack.Error)
		}
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("ack timeout (30s)")
	case <-ctx.Done():
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	}
}

// RecordNodeConfigured records that a node has been configured with LDAP and,
// if this is the first such node, locks the base DN to prevent future changes.
// Called from the server-side deploy-complete handler.
func (m *Manager) RecordNodeConfigured(ctx context.Context, nodeID, configHash string) error {
	if err := m.db.LDAPRecordNodeConfigured(ctx, nodeID, configHash); err != nil {
		return err
	}
	// Lock the base DN once at least one node is configured. Idempotent.
	if err := m.db.LDAPLockBaseDN(ctx); err != nil {
		log.Warn().Err(err).Str("node_id", nodeID).Msg("ldap: failed to lock base DN after first node provisioned (non-fatal)")
	}
	return nil
}

// LockBaseDN locks the base DN to prevent future changes. Idempotent.
// Exposed publicly so callers outside this package can trigger the lock
// if needed (e.g., from an admin override endpoint in the future).
func (m *Manager) LockBaseDN(ctx context.Context) error {
	return m.db.LDAPLockBaseDN(ctx)
}

// Allocator returns the shared posixid.Allocator so other modules (e.g. sysaccounts)
// can route their UID/GID operations through the same policy and collision checks.
func (m *Manager) Allocator() *posixid.Allocator {
	return m.allocator
}

// ─── Admin repair ─────────────────────────────────────────────────────────────

// AdminRepairResult is the response body for POST /api/v1/ldap/admin/repair.
type AdminRepairResult struct {
	Status   string `json:"status"`
	Repaired bool   `json:"repaired"`
}

// AdminRepair verifies the supplied admin password against the stored bcrypt
// hash, persists the plaintext to the DB (backfilling pre-028 installs),
// populates the in-memory cache, and then self-heals the node-reader entry's
// userPassword to match the current service_bind_password.
//
// The repair is verified by re-binding as the service account before returning.
// Returns an error if the password does not match or if the LDAP repair fails.
func (m *Manager) AdminRepair(ctx context.Context, adminPassword string) (AdminRepairResult, error) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return AdminRepairResult{}, fmt.Errorf("ldap: read config for repair: %w", err)
	}
	if !row.Enabled || row.Status != statusReady {
		return AdminRepairResult{}, fmt.Errorf("ldap: module is not enabled or not ready (status=%s)", row.Status)
	}
	if row.AdminPasswordHash == "" {
		return AdminRepairResult{}, fmt.Errorf("ldap: no admin password hash found — module may not have been fully provisioned")
	}

	// Step 1: Verify supplied password against the stored bcrypt hash.
	if err := bcrypt.CompareHashAndPassword([]byte(row.AdminPasswordHash), []byte(adminPassword)); err != nil {
		return AdminRepairResult{}, fmt.Errorf("password does not match the one set on Enable")
	}

	// Step 2: Persist plaintext to DB and populate in-memory cache.
	if err := m.db.LDAPSetAdminPasswd(ctx, adminPassword); err != nil {
		return AdminRepairResult{}, fmt.Errorf("ldap: persist admin_passwd: %w", err)
	}
	m.mu.Lock()
	m.adminPassword = adminPassword
	m.mu.Unlock()

	// Step 3: Bind as admin and issue a Modify REPLACE on node-reader's userPassword.
	adminBindDN := fmt.Sprintf("cn=Directory Manager,%s", row.BaseDN)
	nodeReaderDN := fmt.Sprintf("cn=node-reader,ou=services,%s", row.BaseDN)
	caCertPEM := []byte(row.CACertPEM)

	adminDIT := &ditClient{
		serverURI:  "ldaps://127.0.0.1:636",
		bindDN:     adminBindDN,
		bindPasswd: adminPassword,
		baseDN:     row.BaseDN,
		caCertPEM:  caCertPEM,
	}

	conn, err := adminDIT.connect()
	if err != nil {
		return AdminRepairResult{}, fmt.Errorf("ldap: bind as admin for repair: %w", err)
	}
	defer conn.Close()

	// Use writeServiceAccountPassword so the single atomic Modify also clears
	// any ppolicy state (pwdReset, pwdAccountLockedTime, pwdFailureTime) that
	// ppolicy auto-set when the rootdn last touched this entry's userPassword.
	// Pass plaintext — ppolicy (olcPPolicyHashCleartext: TRUE) hashes via glibc crypt(3).
	if err := writeServiceAccountPassword(conn, nodeReaderDN, row.ServiceBindPassword); err != nil {
		return AdminRepairResult{}, fmt.Errorf("ldap: Modify REPLACE on node-reader userPassword: %w", err)
	}
	log.Info().Str("dn", nodeReaderDN).Msg("ldap: repair: node-reader userPassword and ppolicy state updated")

	// Step 4: Verify repair by re-binding as the service account.
	// Use a fresh connect() rather than HealthBind() so we actually test
	// the credentials, not just TLS reachability.
	svcDIT := &ditClient{
		serverURI:  "ldaps://127.0.0.1:636",
		bindDN:     row.ServiceBindDN,
		bindPasswd: row.ServiceBindPassword,
		baseDN:     row.BaseDN,
		caCertPEM:  caCertPEM,
	}
	svcConn, err := svcDIT.connect()
	if err != nil {
		return AdminRepairResult{}, fmt.Errorf("ldap: repair verification failed — service bind still rejected after Modify REPLACE: %w", err)
	}
	svcConn.Close()

	log.Info().Msg("ldap: admin repair complete — service bind verified OK")
	return AdminRepairResult{Status: "ok", Repaired: true}, nil
}

// ─── Portal / self-service methods ────────────────────────────────────────────

// PortalUserInfo holds the minimal LDAP user attributes surfaced to a researcher.
type PortalUserInfo struct {
	UID         string   `json:"uid"`
	DisplayName string   `json:"display_name"`
	Email       string   `json:"email,omitempty"`
	Groups      []string `json:"groups"`
}

// GetPortalUserInfo returns the LDAP account info for uid using the reader bind.
// Returns nil, nil when the LDAP module is not ready.
func (m *Manager) GetPortalUserInfo(ctx context.Context, uid string) (*PortalUserInfo, error) {
	dit, err := m.ReaderDIT(ctx)
	if err != nil {
		// Module not ready — return nil gracefully.
		return nil, nil //nolint:nilerr
	}

	user, err := dit.GetUser(uid)
	if err != nil {
		return nil, fmt.Errorf("ldap: get portal user %s: %w", uid, err)
	}

	// Resolve group memberships for this user.
	groups, err := dit.ListGroups()
	var memberOf []string
	if err == nil {
		for _, g := range groups {
			for _, m := range g.MemberUIDs {
				if m == uid {
					memberOf = append(memberOf, g.CN)
					break
				}
			}
		}
	}
	if memberOf == nil {
		memberOf = []string{}
	}

	info := &PortalUserInfo{
		UID:         user.UID,
		DisplayName: user.GivenName + " " + user.SN,
		Email:       user.Mail,
		Groups:      memberOf,
	}
	// Trim display name in case either part is empty.
	info.DisplayName = strings.TrimSpace(info.DisplayName)
	if info.DisplayName == "" {
		info.DisplayName = user.UID
	}
	return info, nil
}

// ChangeOwnPassword verifies currentPassword against the user's LDAP bind,
// then uses the admin DIT to set newPassword. This is the self-service password
// change for the researcher portal — the user must know their current password.
func (m *Manager) ChangeOwnPassword(ctx context.Context, uid, currentPassword, newPassword string) error {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("ldap: read config: %w", err)
	}
	if !row.Enabled || row.Status != statusReady {
		return fmt.Errorf("ldap module is not ready (status=%s)", row.Status)
	}

	// Step 1: verify the current password by binding as the user.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(row.CACertPEM)) {
		return fmt.Errorf("ldap: failed to parse CA cert for user bind")
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		ServerName: "127.0.0.1",
	}

	conn, err := goldap.DialURL("ldaps://127.0.0.1:636", goldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return fmt.Errorf("ldap: dial for user bind: %w", err)
	}
	defer conn.Close()

	userDN := fmt.Sprintf("uid=%s,ou=people,%s", goldap.EscapeDN(uid), row.BaseDN)
	if bindErr := conn.Bind(userDN, currentPassword); bindErr != nil {
		return fmt.Errorf("invalid credentials")
	}
	conn.Close()

	// Step 2: use admin DIT to set the new password.
	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("ldap: get admin DIT for password change: %w", err)
	}
	return dit.SetPassword(uid, newPassword, false)
}

// GetPortalQuota reads LDAP quota attributes for uid.
// usedAttr and limitAttr are the LDAP attribute names configured by the operator.
// Returns nil when the attributes are not configured or not present on the entry.
func (m *Manager) GetPortalQuota(ctx context.Context, uid, usedAttr, limitAttr string) (*PortalQuota, error) {
	if usedAttr == "" && limitAttr == "" {
		return nil, nil
	}

	dit, err := m.ReaderDIT(ctx)
	if err != nil {
		return nil, nil //nolint:nilerr
	}

	usedRaw, limitRaw, err := dit.GetQuotaAttrs(uid, usedAttr, limitAttr)
	if err != nil {
		// Non-fatal — quota attributes may not exist.
		return nil, nil //nolint:nilerr
	}

	quota := &PortalQuota{
		Configured: true,
		UsedRaw:    usedRaw,
		LimitRaw:   limitRaw,
	}
	// Try to parse as int64 bytes for structured rendering.
	if n, err := strconv.ParseInt(strings.TrimSpace(usedRaw), 10, 64); err == nil {
		quota.UsedBytes = &n
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(limitRaw), 10, 64); err == nil {
		quota.LimitBytes = &n
	}
	return quota, nil
}

// PortalQuota holds storage quota info for a researcher.
type PortalQuota struct {
	UsedBytes  *int64 `json:"used_bytes,omitempty"`
	LimitBytes *int64 `json:"limit_bytes,omitempty"`
	UsedRaw    string `json:"used_raw,omitempty"`
	LimitRaw   string `json:"limit_raw,omitempty"`
	Configured bool   `json:"configured"`
}

// AddUserToGroup adds uid to the POSIX group identified by groupCN.
// Used by PI self-service member management (Sprint C.5 CF-08).
// Returns an error if the LDAP module is not ready or the group does not exist.
func (m *Manager) AddUserToGroup(ctx context.Context, uid, groupCN string) error {
	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("ldap: get DIT for group member add: %w", err)
	}
	return dit.AddGroupMember(groupCN, uid)
}

// RemoveUserFromGroup removes uid from the POSIX group identified by groupCN.
// Used by PI self-service member management (Sprint C.5 CF-08).
func (m *Manager) RemoveUserFromGroup(ctx context.Context, uid, groupCN string) error {
	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("ldap: get DIT for group member remove: %w", err)
	}
	return dit.RemoveGroupMember(groupCN, uid)
}
