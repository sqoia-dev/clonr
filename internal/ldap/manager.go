// manager.go — LDAP module Manager: lifecycle (Enable/Disable), health checks,
// background workers, and the NodeConfig projection used by the deploy pipeline.
package ldap

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"

	"github.com/sqoia-dev/clonr/internal/config"
	"github.com/sqoia-dev/clonr/internal/db"
	"github.com/sqoia-dev/clonr/pkg/api"
)

// statusProvisioning / statusReady etc. mirror the DB status column values.
const (
	statusDisabled     = "disabled"
	statusProvisioning = "provisioning"
	statusReady        = "ready"
	statusError        = "error"
)

// certExpiryWarnDays — warn when the server cert expires within this many days.
const certExpiryWarnDays = 30

// Manager owns the LDAP module lifecycle and provides the API surface for
// users/groups/status. It is safe for concurrent use.
type Manager struct {
	cfg config.ServerConfig
	db  *db.DB
	mu  sync.RWMutex

	// In-memory DM password — set on Enable(), cleared on Disable().
	// Never persisted; the DB only stores its bcrypt hash.
	adminPassword string

	// Cached service bind password (in-memory copy for DIT operations).
	// The plaintext is also stored in the DB row (v1 limitation — see migration comment).
	servicePassword string
}

// New creates a new LDAP Manager. Call StartBackgroundWorkers to start health checks.
func New(cfg config.ServerConfig, database *db.DB) *Manager {
	m := &Manager{
		cfg: cfg,
		db:  database,
	}
	// Restore in-memory passwords from DB on startup if module is ready.
	m.restoreInMemoryPasswords(context.Background())
	return m
}

// restoreInMemoryPasswords loads the service_bind_password from the DB on startup
// so that health checks and DIT operations work without an Enable() call.
// The admin password cannot be restored — DIT operations that require admin bind
// will fail after a restart until Enable() is called again.
func (m *Manager) restoreInMemoryPasswords(ctx context.Context) {
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return // DB not yet migrated or module never enabled — fine
	}
	if row.Status == statusReady || row.Status == statusError {
		m.mu.Lock()
		m.servicePassword = row.ServiceBindPassword
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
// writes to /etc/clonr/) and MUST be called with root privileges.
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
	if err := m.db.LDAPSetStatus(ctx, statusProvisioning, "generating certificates"); err != nil {
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

	ldapCfg := m.cfg.LDAPConfigDir
	pkiDir := m.cfg.LDAPPKIDir
	ldapDataDir := m.cfg.LDAPDataDir

	// ── Step 1: Generate certificates ────────────────────────────────────────
	log.Info().Msg("ldap: step 1/6: generating certificates")
	_ = m.db.LDAPSetStatus(ctx, statusProvisioning, "generating certificates")

	hostname := detectHostname()
	primaryIP := detectPrimaryIP()

	caBundle, caKey, caCert, err := generateCA(fmt.Sprintf("clonr LDAP CA (%s)", dc1))
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
	if err := WriteServerCert(ldapCfg, serverBundle.CertPEM, serverBundle.KeyPEM); err != nil {
		setError(fmt.Sprintf("write server cert failed: %v", err))
		return
	}

	// Register CA in system trust store (non-fatal).
	if err := UpdateCATrust(ctx, caBundle.CertPEM); err != nil {
		log.Warn().Err(err).Msg("ldap: update-ca-trust failed (non-fatal)")
	}

	// Mask distro slapd and create data directory.
	if err := MaskDistroSlapd(ctx); err != nil {
		log.Warn().Err(err).Msg("ldap: mask distro slapd (non-fatal)")
	}
	dataDir := ldapDataDir + "/data"
	if err := CreateDataDir(ctx, dataDir); err != nil {
		setError(fmt.Sprintf("create data dir failed: %v", err))
		return
	}

	// Generate service account password.
	svcPasswd, err := generateRandomPassword(24)
	if err != nil {
		setError(fmt.Sprintf("generate service password: %v", err))
		return
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
		AdminPassword:   req.AdminPassword, // plaintext; slapd hashes per olcPasswordHash
		ServicePassword: svcPasswd,
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

	ouData := ouSeedData{
		BaseDN:          req.BaseDN,
		DC1:             dc1,
		DC2:             dc2,
		ServicePassword: svcPasswd,
	}
	if err := m.seedDIT(ctx, dit, ouData); err != nil {
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
}

// seedDIT loads the OU seed LDIF into the running slapd via ldapadd.
func (m *Manager) seedDIT(ctx context.Context, dit *ditClient, data ouSeedData) error {
	ldif, err := renderOUSeedLDIF(data)
	if err != nil {
		return err
	}

	tmpLDIF, err := os.CreateTemp("", "clonr-ou-seed-*.ldif")
	if err != nil {
		return fmt.Errorf("ldap: create temp OU LDIF: %w", err)
	}
	defer os.Remove(tmpLDIF.Name())

	if _, err := tmpLDIF.Write(ldif); err != nil {
		tmpLDIF.Close()
		return fmt.Errorf("ldap: write temp OU LDIF: %w", err)
	}
	tmpLDIF.Close()

	out, err := runLDAPAdd(ctx, dit.serverURI, dit.bindDN, dit.bindPasswd, dit.caCertPEM, tmpLDIF.Name())
	if err != nil {
		return fmt.Errorf("ldap: ldapadd OU seed: %w (output: %s)", err, string(out))
	}

	log.Info().Msg("ldap: data DIT seeded (base DN + OUs + node-reader account)")
	return nil
}

// runLDAPAdd runs ldapadd with the given credentials and LDIF file.
// Writes the CA cert to a temp file for TLS verification.
func runLDAPAdd(ctx context.Context, uri, bindDN, passwd string, caCertPEM []byte, ldifPath string) ([]byte, error) {
	tmpCA, err := os.CreateTemp("", "clonr-ca-*.crt")
	if err != nil {
		return nil, fmt.Errorf("ldap: create temp CA cert for ldapadd: %w", err)
	}
	defer os.Remove(tmpCA.Name())

	if _, err := tmpCA.Write(caCertPEM); err != nil {
		tmpCA.Close()
		return nil, err
	}
	tmpCA.Close()

	cmd := exec.CommandContext(ctx,
		"ldapadd",
		"-x",
		"-H", uri,
		"-D", bindDN,
		"-w", passwd,
		"-f", ldifPath,
	)
	cmd.Env = append(os.Environ(), "LDAPTLS_CACERT="+tmpCA.Name())
	return cmd.CombinedOutput()
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

	// Build the ldaps URI with the server's hostname.
	serverURI := fmt.Sprintf("ldaps://%s:636", detectHostname())

	return &api.LDAPNodeConfig{
		ServerURI:         serverURI,
		BaseDN:            row.BaseDN,
		ServiceBindDN:     row.ServiceBindDN,
		ServiceBindPasswd: svcPasswd,
		CACertPEM:         row.CACertPEM,
	}, nil
}

// ─── Background workers ───────────────────────────────────────────────────────

// StartBackgroundWorkers launches the health-check goroutine.
func (m *Manager) StartBackgroundWorkers(ctx context.Context) {
	go m.runHealthChecker(ctx)
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
