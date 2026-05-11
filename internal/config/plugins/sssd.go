package plugins

// sssd.go — Sprint 36 Day 3
//
// SSSDPlugin renders /etc/sssd/sssd.conf for the node identified by
// state.NodeID. The file is fully owned by clustr (no anchors); the
// rendered output is byte-identical to what the imperative finalization
// path produces via deploy.renderSSSDConf.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// sssdStagePayload is the portion of the dangerous-push stage request body
// that the SSSD plugin's ValidatePayload reads.  Only the fields that SSSD
// semantic-validation actually inspects are decoded; unknown fields are
// ignored (the JSON-SCHEMA middleware already validated structure).
type sssdStagePayload struct {
	// The stage endpoint accepts plugin_name + node_id.  SSSD validates the
	// ldap_uri field when it appears inside an optional "config" sub-object.
	// If the caller did not send a config sub-object, ValidatePayload is a no-op.
	Config *sssdPayloadConfig `json:"config,omitempty"`
}

type sssdPayloadConfig struct {
	ServerURI string `json:"ldap_uri,omitempty"`
	BaseDN    string `json:"base_dn,omitempty"`
}

// Compile-time assertion: SSSDPlugin implements config.PayloadValidator.
var _ config.PayloadValidator = SSSDPlugin{}

// sssdWatchKey is the config-tree path the SSSD plugin subscribes to.
//
// LDAPConfig is a structured sub-object on NodeConfig; it is updated by the
// LDAP module (POST /api/v1/modules/ldap/enable and related routes), not by
// the generic UpdateNode PUT. The observer receives a Notify call with this
// key whenever the LDAP module writes new bind credentials or CA cert.
const sssdWatchKey = "nodes.*.ldap_config"

// SSSDPlugin renders /etc/sssd/sssd.conf for the node identified by
// state.NodeID. It is stateless and safe for concurrent invocation.
//
// Render contract:
//   - Pure and idempotent: same NodeID + same LDAPConfig → same output.
//   - No anchors: /etc/sssd/sssd.conf is single-purpose and fully managed by
//     clustr. A full overwrite is safe.
//   - Returns a nil slice when LDAPConfig is nil, so the observer does not
//     fire a push for nodes where LDAP is not enabled.
//
// Registration: call config.Register(SSSDPlugin{}) once at startup inside the
// reactiveConfigPluginsOnce.Do block.
type SSSDPlugin struct{}

// Name returns the stable plugin identifier used in DB rows and WS messages.
func (SSSDPlugin) Name() string { return "sssd" }

// WatchedKeys returns the config-tree path this plugin subscribes to.
// The observer indexes this key verbatim; the LDAP module must emit the same
// literal string via config.Notify when LDAPConfig changes.
func (SSSDPlugin) WatchedKeys() []string {
	return []string{sssdWatchKey}
}

// SSSDWatchKey returns the literal watch-key string so callers (server wiring,
// tests) can reference it without importing the unexported constant.
func SSSDWatchKey() string { return sssdWatchKey }

// Metadata returns the execution and safety invariants for the SSSD plugin.
//
// Priority 80: must run after hostname/hosts (Foundation band) but before any
// slurm config that references LDAP-resolved users. Sits in the Middleware
// band (51–100).
//
// Dangerous=true (Sprint 41 Day 3): a misrendered sssd.conf breaks login for all
// LDAP-resolved users. On a lab where every operator is LDAP-backed this is
// lockout-class. Operators must supply the typed cluster name via the
// POST /api/v1/config/dangerous-push → confirm handshake before delivery.
// The confirmation gate is feature-flagged behind CLUSTR_DANGEROUS_GATE_ENABLED=1;
// when the flag is off the regular push path operates as before (no gate).
//
// Backup is wired in Sprint 41 Day 4. Paths cover the full files SSSD reads on
// startup: sssd.conf (main config), conf.d/ (drop-in overrides), and the SSSD
// ldb caches under /var/lib/sss/db/ (corrupted caches can cause sssd to fail to
// start even with a correct sssd.conf). MaxBackups=10 keeps the last 10 renders.
func (SSSDPlugin) Metadata() config.PluginMetadata {
	return config.PluginMetadata{
		Priority:     80,
		Dangerous:    true,
		DangerReason: "SSSD restart can sever active operator SSH sessions if PAM stack is broken; a misrendered sssd.conf breaks login for all LDAP users and requires console access to recover",
		Backup: &config.BackupSpec{
			Paths:   []string{"/etc/sssd/sssd.conf", "/etc/sssd/conf.d/", "/var/lib/sss/db/"},
			RetainN: 10,
		},
	}
}

// ValidatePayload implements config.PayloadValidator for the SSSD plugin.
//
// It performs semantic validation of the dangerous-push stage payload beyond
// the structural JSON-SCHEMA check.  Validation is conservative: only clearly
// wrong values are rejected.
//
// Rules:
//  1. If a "config.ldap_uri" field is present it must be a valid LDAP URI
//     (scheme must be "ldap" or "ldaps"; host must be non-empty).
//  2. If a "config.base_dn" field is present it must be non-empty and contain
//     at least one "dc=" component (sanity check, not a full RFC 4514 parse).
//
// If the payload carries no "config" sub-object, or the body is not parseable
// as JSON, this method returns an empty slice — the JSON-SCHEMA middleware has
// already ensured structural validity by this point so an unparseable body is
// treated as "no overrides to validate".
func (SSSDPlugin) ValidatePayload(payload []byte) []config.PayloadValidationError {
	if len(payload) == 0 {
		return nil
	}

	var p sssdStagePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		// Structural parse failure: JSON-SCHEMA already validated the shape;
		// treat as no config sub-object to avoid double-reporting.
		return nil
	}
	if p.Config == nil {
		// No SSSD-specific config block in the payload — nothing to validate.
		return nil
	}

	var violations []config.PayloadValidationError

	if uri := p.Config.ServerURI; uri != "" {
		if err := validateLDAPURI(uri); err != nil {
			violations = append(violations, config.PayloadValidationError{
				Path:    "config.ldap_uri",
				Message: err.Error(),
				Code:    "invalid_uri",
			})
		}
	}

	if dn := p.Config.BaseDN; dn != "" {
		if !strings.Contains(strings.ToLower(dn), "dc=") {
			violations = append(violations, config.PayloadValidationError{
				Path:    "config.base_dn",
				Message: "base_dn must contain at least one dc= component (e.g. dc=cluster,dc=local)",
				Code:    "invalid_base_dn",
			})
		}
	}

	return violations
}

// validateLDAPURI checks that uri is a well-formed LDAP URI.
// Only the scheme and host are inspected — path/query/fragment are not part of
// the SSSD ldap_uri field and are rejected by conservative check.
func validateLDAPURI(uri string) error {
	u, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("not a valid URI: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "ldap" && scheme != "ldaps" {
		return fmt.Errorf("scheme must be ldap or ldaps, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("host must not be empty")
	}
	return nil
}

// Render returns a single InstallInstruction that writes /etc/sssd/sssd.conf
// for the node identified by state.NodeID.
//
// Returns nil, nil when state.NodeConfig.LDAPConfig is nil — the observer
// treats a nil slice as "nothing to push" and skips the WS send.
func (SSSDPlugin) Render(state config.ClusterState) ([]api.InstallInstruction, error) {
	ldapCfg := state.NodeConfig.LDAPConfig
	if ldapCfg == nil {
		return nil, nil
	}
	if ldapCfg.BaseDN == "" {
		return nil, fmt.Errorf("sssd plugin: LDAPConfig.BaseDN is empty")
	}

	domain := ldapDomainFromBaseDN(ldapCfg.BaseDN)
	content := renderSSSDConf(ldapCfg, domain)

	instr := api.InstallInstruction{
		Opcode:  "overwrite",
		Target:  "/etc/sssd/sssd.conf",
		Payload: content,
		// No Anchors: /etc/sssd/sssd.conf is a single-purpose file fully
		// owned by this plugin. A full overwrite is correct and idempotent.
	}

	return []api.InstallInstruction{instr}, nil
}

// renderSSSDConf renders the sssd.conf content for a node. The output is
// byte-identical to the output of deploy.renderSSSDConf in
// internal/deploy/finalize.go — this is the reactive-config equivalent.
//
// The function is duplicated here rather than imported from the deploy package
// to avoid creating a cyclic import (deploy → config → deploy). The deploy
// package is a leaf package; config must not import it.
func renderSSSDConf(cfg *api.LDAPNodeConfig, domain string) string {
	return fmt.Sprintf(`# sssd.conf — generated by clustr-serverd
# DO NOT EDIT — managed by clustr. Regenerated on each reimage.

[sssd]
services = nss, pam, ssh
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

ldap_tls_reqcert = allow
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
`, domain, domain,
		cfg.ServerURI, cfg.BaseDN,
		cfg.ServiceBindDN, cfg.ServiceBindPasswd,
		cfg.BaseDN, cfg.BaseDN)
}

// ldapDomainFromBaseDN extracts the first DC component from a base DN.
// "dc=cluster,dc=local" → "cluster"
// Duplicated from internal/deploy/finalize.go for the same cycle-avoidance
// reason as renderSSSDConf above.
func ldapDomainFromBaseDN(baseDN string) string {
	parts := strings.SplitN(baseDN, ",", 2)
	if len(parts) == 0 {
		return baseDN
	}
	first := strings.TrimSpace(parts[0])
	if after, ok := strings.CutPrefix(first, "dc="); ok {
		return after
	}
	return first
}
