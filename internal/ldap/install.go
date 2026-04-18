// install.go — auto-install openldap-servers during Enable() on supported distros.
//
// Distro detection reads /etc/os-release. Two families are supported:
//   - EL (Rocky, RHEL, AlmaLinux, CentOS, Oracle): dnf + EPEL
//   - Debian/Ubuntu: apt-get with debconf preseed
//
// Anything else fails loudly so the operator knows they must install manually.

package ldap

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"
)

// distroFamily represents the detected Linux distribution family.
type distroFamily int

const (
	familyEL     distroFamily = iota // Rocky, RHEL, AlmaLinux, CentOS, Oracle Linux
	familyDebian                     // Debian, Ubuntu
)

// osRelease holds the parsed fields from /etc/os-release that we care about.
type osRelease struct {
	id     string
	idLike string
}

// readOSRelease reads and parses /etc/os-release.
func readOSRelease() (osRelease, error) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return osRelease{}, fmt.Errorf("ldap install: open /etc/os-release: %w", err)
	}
	defer f.Close()

	var r osRelease
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		// Strip surrounding quotes from value.
		val = strings.Trim(val, `"'`)
		switch key {
		case "ID":
			r.id = strings.ToLower(val)
		case "ID_LIKE":
			r.idLike = strings.ToLower(val)
		}
	}
	return r, scanner.Err()
}

// detectDistroFamily parses os-release fields and returns the distro family.
func detectDistroFamily(r osRelease) (distroFamily, error) {
	// EL family: explicit IDs or ID_LIKE containing rhel or fedora.
	elIDs := map[string]bool{
		"rocky": true, "rhel": true, "almalinux": true,
		"centos": true, "ol": true,
	}
	if elIDs[r.id] {
		return familyEL, nil
	}
	for _, like := range strings.Fields(r.idLike) {
		if like == "rhel" || like == "fedora" {
			return familyEL, nil
		}
	}

	// Debian family.
	debianIDs := map[string]bool{"debian": true, "ubuntu": true}
	if debianIDs[r.id] {
		return familyDebian, nil
	}
	for _, like := range strings.Fields(r.idLike) {
		if like == "debian" {
			return familyDebian, nil
		}
	}

	return 0, fmt.Errorf(
		"Unsupported distro for auto-install: %s. "+
			"Supported: Rocky/RHEL/AlmaLinux/CentOS/Oracle and Debian/Ubuntu. "+
			"Install openldap-servers manually and retry.",
		r.id,
	)
}

// ensureEPEL checks whether an EPEL repo is enabled; installs epel-release if not.
// Returns an error with operator-actionable text if EPEL cannot be enabled.
func ensureEPEL(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "dnf", "repolist", "--enabled").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ldap install: dnf repolist: %w (output: %s)", err, string(out))
	}

	hasEPEL := false
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if strings.Contains(strings.ToLower(scanner.Text()), "epel") {
			hasEPEL = true
			break
		}
	}

	if hasEPEL {
		log.Info().Msg("ldap install: EPEL repo already enabled")
		return nil
	}

	log.Info().Msg("ldap install: EPEL not found, installing epel-release")
	installOut, installErr := exec.CommandContext(ctx, "dnf", "install", "-y", "epel-release").CombinedOutput()
	if installErr != nil {
		return fmt.Errorf(
			"EPEL not enabled and epel-release not available. "+
				"On RHEL, enable EPEL manually: "+
				"dnf install https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm\n"+
				"(dnf error: %v; output: %s)",
			installErr, string(installOut),
		)
	}
	log.Info().Msg("ldap install: epel-release installed")
	return nil
}

// installOpenLDAPEL installs openldap-servers on EL family distros.
// Idempotent: skips if slapd is already on PATH.
func installOpenLDAPEL(ctx context.Context) error {
	if path, err := exec.LookPath("slapd"); err == nil {
		log.Info().Str("path", path).Msg("ldap install: slapd already on PATH, skipping EL install")
		return nil
	}

	if err := ensureEPEL(ctx); err != nil {
		return err
	}

	log.Info().Msg("ldap install: running dnf install -y openldap-servers")
	out, err := exec.CommandContext(ctx, "dnf", "install", "-y", "openldap-servers").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ldap install: dnf install openldap-servers: %w\n%s", err, string(out))
	}
	log.Info().Msg("ldap install: openldap-servers installed via dnf")
	return nil
}

// installOpenLDAPDebian installs slapd + ldap-utils on Debian family distros.
// Idempotent: skips if slapd is already on PATH.
func installOpenLDAPDebian(ctx context.Context) error {
	if path, err := exec.LookPath("slapd"); err == nil {
		log.Info().Str("path", path).Msg("ldap install: slapd already on PATH, skipping Debian install")
		return nil
	}

	// Preseed to suppress interactive postinst configuration.
	// We do all configuration ourselves via cn=config seed-once.
	preseedCmd := exec.CommandContext(ctx, "debconf-set-selections")
	preseedCmd.Stdin = strings.NewReader("slapd slapd/no_configuration boolean true\n")
	if out, err := preseedCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ldap install: debconf-set-selections: %w (output: %s)", err, string(out))
	}

	log.Info().Msg("ldap install: running apt-get install slapd ldap-utils")
	cmd := exec.CommandContext(ctx, "apt-get", "install", "-y", "--no-install-recommends", "slapd", "ldap-utils")
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ldap install: apt-get install slapd: %w\n%s", err, string(out))
	}
	log.Info().Msg("ldap install: slapd + ldap-utils installed via apt-get")
	return nil
}

// verifySlapdInstall checks that slapd and slapadd are on PATH, and detects the
// slapd system user (ldap on EL, openldap on Debian). Returns the username.
// Returns a descriptive error if any verification step fails.
func verifySlapdInstall() (string, error) {
	if _, err := exec.LookPath("slapd"); err != nil {
		return "", fmt.Errorf("openldap-servers installed but slapd not found at expected path. Manual investigation required.")
	}
	if _, err := exec.LookPath("slapadd"); err != nil {
		return "", fmt.Errorf("openldap-servers installed but slapadd not found at expected path. Manual investigation required.")
	}

	// Detect which system user exists.
	for _, candidate := range []string{"ldap", "openldap"} {
		out, err := exec.Command("id", candidate).CombinedOutput()
		if err == nil {
			log.Info().Str("slapd_user", candidate).Str("id_output", strings.TrimSpace(string(out))).
				Msg("ldap install: slapd system user detected")
			return candidate, nil
		}
	}
	return "", fmt.Errorf("openldap-servers installed but neither 'ldap' nor 'openldap' user found. Manual investigation required.")
}

// EnsureOpenLDAP installs openldap-servers if slapd is not already present, then
// verifies the installation and returns the slapd system username ("ldap" or "openldap").
//
// This is the entry point called by doProvision before any other provisioning step.
func EnsureOpenLDAP(ctx context.Context) (string, error) {
	rel, err := readOSRelease()
	if err != nil {
		return "", fmt.Errorf("ldap install: detect distro: %w", err)
	}

	family, err := detectDistroFamily(rel)
	if err != nil {
		return "", err
	}

	switch family {
	case familyEL:
		if err := installOpenLDAPEL(ctx); err != nil {
			return "", err
		}
	case familyDebian:
		if err := installOpenLDAPDebian(ctx); err != nil {
			return "", err
		}
	}

	return verifySlapdInstall()
}
