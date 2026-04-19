// slapd.go — slapd bootstrap and systemd wrappers for the clonr LDAP module.
//
// IMPORTANT: This file contains privileged operations that ASSUME root access
// at Enable() time. Specifically:
//   - slapadd -n 0 (seeding the cn=config backend) must run as root or the
//     ldap user (whichever owns the slapd.d/ directory).
//   - systemctl mask slapd.service prevents the distro unit from conflicting
//     with clonr-slapd.service.
//   - update-ca-trust and cert file writes to /etc/clonr/ require root.
//
// clonr-serverd is expected to run as root (or with the necessary capabilities)
// when Enable() is called. Normal operation (health checks, DIT CRUD) does NOT
// require root — those operations use the LDAP protocol over the network.
//
// Polkit rule at internal/ldap/assets/50-clonr-slapd.rules grants the clonr
// user start|stop|restart|reload on clonr-slapd.service only.
// Both files are embedded into the binary and installed by EnsureSystemdUnit()
// during Enable().

package ldap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/rs/zerolog/log"
)

// slapdSeedTemplate is the template for the cn=config seed LDIF.
// Embedded at compile time from the templates directory.
var slapdSeedTemplate *template.Template

func init() {
	slapdSeedTemplate = template.Must(template.ParseFS(templateFS, "templates/slapd-seed.ldif.tmpl"))
}

// slapdSeedData holds the values templated into the cn=config seed LDIF.
type slapdSeedData struct {
	BaseDN          string
	DC1             string
	DC2             string
	ConfigDir       string
	DataDir         string
	CACertPath      string
	ServerCertPath  string
	ServerKeyPath   string
	AdminPassword   string // plaintext; slapd hashes via olcPasswordHash: {CRYPT}
	ServicePassword string // plaintext; slapd hashes via olcPasswordHash: {CRYPT}
	SlapdUser       string // OS user that slapd runs as: "ldap" (EL) or "openldap" (Debian)
}

// parseDCComponents splits a baseDN like "dc=cluster,dc=local" into ["cluster", "local"].
// Returns an error if the baseDN does not contain at least two dc= components.
func parseDCComponents(baseDN string) (dc1, dc2 string, err error) {
	parts := strings.Split(baseDN, ",")
	var dcs []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(strings.ToLower(p), "dc=") {
			dcs = append(dcs, strings.TrimPrefix(strings.ToLower(p), "dc="))
		}
	}
	if len(dcs) < 2 {
		return "", "", fmt.Errorf("base_dn %q must contain at least two dc= components (e.g. dc=cluster,dc=local)", baseDN)
	}
	return dcs[0], dcs[1], nil
}

// renderSlapdSeedLDIF renders the cn=config seed LDIF template.
func renderSlapdSeedLDIF(data slapdSeedData) ([]byte, error) {
	var buf bytes.Buffer
	if err := slapdSeedTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("ldap slapd: render seed LDIF: %w", err)
	}
	return buf.Bytes(), nil
}

// chownTree resolves slapdUser to a uid/gid via user.Lookup and then walks
// the entire directory tree rooted at dir, calling os.Chown on every entry.
// Returns a clear error if the user cannot be resolved.
func chownTree(dir, slapdUser string) error {
	u, err := user.Lookup(slapdUser)
	if err != nil {
		if _, ok := err.(user.UnknownUserError); ok {
			return fmt.Errorf("ldap slapd: detected slapd user %q from package install no longer resolves: %w", slapdUser, err)
		}
		return fmt.Errorf("ldap slapd: lookup slapd user %q: %w", slapdUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("ldap slapd: parse uid for %q: %w", slapdUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("ldap slapd: parse gid for %q: %w", slapdUser, err)
	}
	return filepath.WalkDir(dir, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return fmt.Errorf("ldap slapd: chown %s: %w", path, err)
		}
		return nil
	})
}

// SeedConfig seeds the cn=config backend from a generated LDIF.
// It runs slapadd -n 0 -F <configDir> against the rendered LDIF.
//
// Idempotency: if configDir already contains a seeded cn=config (detected by
// the presence of cn=config/cn.ldif), this function WIPES the directory and
// re-seeds from scratch. This ensures Enable() after a failed Enable() is safe.
// The slapd data directory (mdb) is NOT touched here — only cn=config.
func SeedConfig(ctx context.Context, data slapdSeedData) error {
	configDir := data.ConfigDir

	// Check whether cn=config has already been seeded.
	cnLDIF := filepath.Join(configDir, "cn=config.ldif")
	if _, err := os.Stat(cnLDIF); err == nil {
		log.Warn().Str("config_dir", configDir).
			Msg("ldap slapd: cn=config already seeded — wiping and re-seeding (safe: called only on re-Enable)")
		if err := os.RemoveAll(configDir); err != nil {
			return fmt.Errorf("ldap slapd: remove existing config dir: %w", err)
		}
	}

	// (Re)create the config dir with restricted permissions.
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("ldap slapd: mkdir config dir: %w", err)
	}

	ldif, err := renderSlapdSeedLDIF(data)
	if err != nil {
		return err
	}

	// Write LDIF to a temp file then slapadd it.
	tmpLDIF, err := os.CreateTemp("", "clonr-slapd-seed-*.ldif")
	if err != nil {
		return fmt.Errorf("ldap slapd: create temp LDIF: %w", err)
	}
	defer os.Remove(tmpLDIF.Name())

	if _, err := tmpLDIF.Write(ldif); err != nil {
		tmpLDIF.Close()
		return fmt.Errorf("ldap slapd: write temp LDIF: %w", err)
	}
	tmpLDIF.Close()

	cmd := exec.CommandContext(ctx, "slapadd", "-n", "0", "-F", configDir, "-l", tmpLDIF.Name())
	cmd.Env = os.Environ() // inherit env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ldap slapd: slapadd failed: %w\noutput:\n%s", err, string(out))
	}
	log.Info().Str("output", string(out)).Msg("ldap slapd: slapadd cn=config seeded")

	// Recursively chown the entire config tree to the slapd system user.
	// slapadd ran as root and created all files/dirs owned by root:root;
	// slapd is started as slapdUser and cannot read root-owned 0600 files.
	// File modes (0600 on files, 0700 on dirs) are intentional — only ownership changes.
	if err := chownTree(configDir, data.SlapdUser); err != nil {
		return fmt.Errorf("ldap slapd: chown config tree post-slapadd: %w", err)
	}

	return nil
}

// WriteServerCert writes the server TLS cert and key to the configured paths.
// Creates parent directories as needed. Key is written 0600.
// slapdUser is the OS user that slapd runs as ("ldap" on EL, "openldap" on Debian).
func WriteServerCert(configDir string, certPEM, keyPEM []byte, slapdUser string) error {
	// The TLS dir holds the server private key and must be 0700 ldap:ldap.
	// Its parent (/etc/clonr/ldap) is 0755 so slapd can traverse to it.
	tlsDir := filepath.Join(configDir, "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		return fmt.Errorf("ldap slapd: mkdir tls dir: %w", err)
	}
	// Chown the tls dir itself to the slapd user so it can enter it.
	if err := chownTree(tlsDir, slapdUser); err != nil {
		return fmt.Errorf("ldap slapd: chown tls dir: %w", err)
	}

	certPath := filepath.Join(tlsDir, "server.crt")
	keyPath := filepath.Join(tlsDir, "server.key")

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("ldap slapd: write server cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("ldap slapd: write server key: %w", err)
	}

	// Chown cert and key files to the slapd system user.
	if err := chownTree(tlsDir, slapdUser); err != nil {
		return fmt.Errorf("ldap slapd: chown tls files: %w", err)
	}
	return nil
}

// WriteCACert writes the CA certificate to the PKI dir and the slapd TLS dir.
func WriteCACert(pkiDir, ldapConfigDir string, caCertPEM, caKeyPEM []byte) error {
	if err := os.MkdirAll(pkiDir, 0o755); err != nil {
		return fmt.Errorf("ldap slapd: mkdir pki dir: %w", err)
	}
	caKeyPath := filepath.Join(pkiDir, "ca.key")
	caCertPath := filepath.Join(pkiDir, "ca.crt")

	if err := os.WriteFile(caKeyPath, caKeyPEM, 0o600); err != nil {
		return fmt.Errorf("ldap slapd: write CA key: %w", err)
	}
	if err := os.WriteFile(caCertPath, caCertPEM, 0o644); err != nil {
		return fmt.Errorf("ldap slapd: write CA cert: %w", err)
	}

	// Also write CA cert into the slapd TLS dir for the olcTLSCACertificateFile reference.
	// The TLS dir holds private key material and is created at 0700; WriteServerCert
	// will chown the whole dir to slapdUser after writing the key.
	tlsDir := filepath.Join(ldapConfigDir, "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		return fmt.Errorf("ldap slapd: mkdir ldap tls dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, "ca.crt"), caCertPEM, 0o644); err != nil {
		return fmt.Errorf("ldap slapd: write ldap CA cert: %w", err)
	}
	return nil
}

// UpdateCATrust runs update-ca-trust to register the CA cert with the system trust store.
func UpdateCATrust(ctx context.Context, caCertPEM []byte) error {
	anchorPath := "/etc/pki/ca-trust/source/anchors/clonr-ca.crt"
	if err := os.WriteFile(anchorPath, caCertPEM, 0o644); err != nil {
		return fmt.Errorf("ldap slapd: write CA to trust anchors: %w", err)
	}
	out, err := exec.CommandContext(ctx, "update-ca-trust", "extract").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ldap slapd: update-ca-trust: %w (output: %s)", err, string(out))
	}
	log.Info().Msg("ldap slapd: system CA trust updated")
	return nil
}

// MaskDistroSlapd runs systemctl mask slapd.service to prevent the distro unit
// from starting and conflicting with clonr-slapd.service.
func MaskDistroSlapd(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "mask", "slapd.service").CombinedOutput()
	if err != nil {
		// Non-fatal: distro slapd may not exist on all platforms.
		log.Warn().Err(err).Str("output", string(out)).
			Msg("ldap slapd: could not mask distro slapd.service (non-fatal — may not be installed)")
	}
	return nil
}

// EnsureSystemdUnit writes the embedded clonr-slapd.service and polkit rule to
// their system paths, then runs daemon-reload if either file changed.
//
// Idempotent: if both files already exist with identical content, no writes
// occur and daemon-reload is skipped.
//
// Must be called AFTER EnsureOpenLDAP (so the ldap user and slapd binary exist)
// and AFTER MaskDistroSlapd (so the single daemon-reload picks up both our unit
// and the masked distro unit at once).
func EnsureSystemdUnit(ctx context.Context) error {
	const (
		unitDst   = "/etc/systemd/system/clonr-slapd.service"
		polkitDst = "/etc/polkit-1/rules.d/50-clonr-slapd.rules"
	)

	unitSrc, err := assetFS.ReadFile("assets/clonr-slapd.service")
	if err != nil {
		return fmt.Errorf("ldap slapd: read embedded clonr-slapd.service: %w", err)
	}

	polkitSrc, err := assetFS.ReadFile("assets/50-clonr-slapd.rules")
	if err != nil {
		return fmt.Errorf("ldap slapd: read embedded 50-clonr-slapd.rules: %w", err)
	}

	changed := false

	// Write unit file if missing or content differs.
	unitExisting, readErr := os.ReadFile(unitDst)
	if readErr != nil || !bytes.Equal(unitExisting, unitSrc) {
		if err := os.MkdirAll(filepath.Dir(unitDst), 0o755); err != nil {
			return fmt.Errorf("ldap slapd: mkdir /etc/systemd/system: %w", err)
		}
		if err := os.WriteFile(unitDst, unitSrc, 0o644); err != nil {
			return fmt.Errorf("ldap slapd: write clonr-slapd.service: %w", err)
		}
		log.Info().Str("path", unitDst).Msg("ldap slapd: wrote clonr-slapd.service")
		changed = true
	} else {
		log.Info().Str("path", unitDst).Msg("ldap slapd: clonr-slapd.service unchanged, skipping write")
	}

	// Write polkit rule if missing or content differs.
	polkitExisting, readErr := os.ReadFile(polkitDst)
	if readErr != nil || !bytes.Equal(polkitExisting, polkitSrc) {
		if err := os.MkdirAll(filepath.Dir(polkitDst), 0o755); err != nil {
			return fmt.Errorf("ldap slapd: mkdir /etc/polkit-1/rules.d: %w", err)
		}
		if err := os.WriteFile(polkitDst, polkitSrc, 0o644); err != nil {
			return fmt.Errorf("ldap slapd: write 50-clonr-slapd.rules: %w", err)
		}
		log.Info().Str("path", polkitDst).Msg("ldap slapd: wrote 50-clonr-slapd.rules")
		changed = true
	} else {
		log.Info().Str("path", polkitDst).Msg("ldap slapd: 50-clonr-slapd.rules unchanged, skipping write")
	}

	// Only daemon-reload if something changed.
	if changed {
		out, err := exec.CommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput()
		if err != nil {
			return fmt.Errorf("ldap slapd: systemctl daemon-reload: %w (output: %s)", err, string(out))
		}
		log.Info().Msg("ldap slapd: daemon-reload complete")
	}

	// Verify systemd sees the unit. `systemctl cat` exits non-zero if unknown.
	if out, err := exec.CommandContext(ctx, "systemctl", "cat", "clonr-slapd.service").CombinedOutput(); err != nil {
		return fmt.Errorf(
			"ldap slapd: systemd unit clonr-slapd.service was written but systemd does not see it. "+
				"Try: systemctl daemon-reload (cat output: %s)",
			string(out),
		)
	}

	return nil
}

// StartSlapd starts the clonr-slapd.service via systemctl.
func StartSlapd(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "start", "clonr-slapd.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ldap slapd: systemctl start clonr-slapd: %w (output: %s)", err, string(out))
	}
	return nil
}

// StopSlapd stops the clonr-slapd.service via systemctl.
func StopSlapd(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "stop", "clonr-slapd.service").CombinedOutput()
	if err != nil {
		// Non-fatal: service may already be stopped.
		log.Warn().Err(err).Str("output", string(out)).
			Msg("ldap slapd: systemctl stop clonr-slapd (may already be stopped)")
	}
	return nil
}

// EnableSlapdService runs systemctl enable clonr-slapd.service so it starts on boot.
func EnableSlapdService(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "enable", "clonr-slapd.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ldap slapd: systemctl enable clonr-slapd: %w (output: %s)", err, string(out))
	}
	return nil
}

// SlapcatBackup runs slapcat -n 1 to export the mdb data DIT to a backup LDIF file.
// Returns the path of the created backup file.
func SlapcatBackup(ctx context.Context, backupDir string) (string, error) {
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return "", fmt.Errorf("ldap slapd: mkdir backup dir: %w", err)
	}

	// Use a timestamp-based filename.
	outCmd := exec.CommandContext(ctx, "date", "+%Y%m%d-%H%M%S")
	tsBytes, err := outCmd.Output()
	if err != nil {
		// Fallback to a fixed name if date fails.
		tsBytes = []byte("backup")
	}
	ts := strings.TrimSpace(string(tsBytes))
	filename := fmt.Sprintf("%s.ldif", ts)
	path := filepath.Join(backupDir, filename)

	out, err := exec.CommandContext(ctx, "slapcat", "-n", "1", "-l", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ldap slapd: slapcat: %w (output: %s)", err, string(out))
	}
	return filename, nil
}

// CreateDataDir creates the mdb data directory with correct ownership.
// slapdUser is the OS user that slapd runs as ("ldap" on EL, "openldap" on Debian).
//
// The parent of dataDir (e.g. /var/lib/clonr/ldap) is created at 0755 so that
// the slapd user can traverse into it. The dataDir itself (e.g. .../ldap/data)
// is created at 0700 — slapd owns it exclusively.
func CreateDataDir(_ context.Context, dataDir string, slapdUser string) error {
	// Create the traversable parent at 0755 before the restricted dataDir.
	parent := filepath.Dir(dataDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("ldap slapd: mkdir data parent dir: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("ldap slapd: mkdir data dir: %w", err)
	}
	// Recursively chown the data tree (dir + any pre-existing mdb files) to
	// the slapd system user so slapd can write its mdb databases there.
	if err := chownTree(dataDir, slapdUser); err != nil {
		return fmt.Errorf("ldap slapd: chown data dir: %w", err)
	}
	return nil
}
