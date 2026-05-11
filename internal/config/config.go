// Package config manages clustr runtime configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)


// ServerConfig holds all runtime configuration for clustr-serverd.
// Values can be loaded from a JSON file or from environment variables.
type ServerConfig struct {
	ListenAddr      string        `json:"listen_addr"`      // default ":8080"
	ImageDir        string        `json:"image_dir"`        // default "/var/lib/clustr/images"
	DBPath          string        `json:"db_path"`          // default "/var/lib/clustr/db/clustr.db"
	AuthToken       string        `json:"auth_token"`       // legacy: from CLUSTR_AUTH_TOKEN; superseded by api_keys table
	AuthDevMode     bool          `json:"auth_dev_mode"`    // from CLUSTR_AUTH_DEV_MODE=1; bypasses auth for local dev ONLY
	SessionSecret   string        `json:"session_secret"`   // CLUSTR_SESSION_SECRET: HMAC key for browser session tokens (32+ bytes)
	SessionSecure   bool          `json:"session_secure"`   // CLUSTR_SESSION_SECURE=1: set Secure flag on session cookie (requires TLS)
	LogLevel        string        `json:"log_level"`        // debug, info, warn, error — default "info"
	LogRetention    time.Duration `json:"log_retention"`    // from CLUSTR_LOG_RETENTION; default 7d (D2)
	LogMaxRowsPerNode int64       `json:"log_max_rows_per_node"` // from CLUSTR_LOG_MAX_ROWS_PER_NODE; default 50000 (D2)
	ClustrBinPath    string        `json:"clustr_bin_path"`   // CLUSTR_BIN_PATH: abs path to clustr CLI binary baked into initramfs; default /usr/local/bin/clustr
	ClientdBinPath  string        `json:"clientd_bin_path"` // CLUSTR_CLIENTD_BIN_PATH: abs path to clustr-clientd binary copied into deployed rootfs; auto-detected when empty
	// VerifyTimeout is the duration after deploy_completed_preboot_at within which
	// the deployed OS must phone home via POST /verify-boot. ADR-0008.
	// From CLUSTR_VERIFY_TIMEOUT (Go duration string, e.g. "5m"). Default: 5 minutes.
	VerifyTimeout   time.Duration `json:"verify_timeout"`   // CLUSTR_VERIFY_TIMEOUT; default 5m
	PXE             PXEConfig     `json:"pxe"`

	// LDAP module directories.
	// LDAPDataDir is the root for slapd mdb files and backups.
	// Default: /var/lib/clustr/ldap
	LDAPDataDir   string `json:"ldap_data_dir"`   // CLUSTR_LDAP_DATA_DIR
	// LDAPConfigDir is where the slapd.d cn=config tree and TLS certs live.
	// Default: /etc/clustr/ldap
	LDAPConfigDir string `json:"ldap_config_dir"` // CLUSTR_LDAP_CONFIG_DIR
	// LDAPPKIDir is where the CA key and certificate are stored.
	// Default: /etc/clustr/pki
	LDAPPKIDir    string `json:"ldap_pki_dir"`    // CLUSTR_LDAP_PKI_DIR

	// RepoDir is the root directory from which clustr-serverd serves the bundled
	// Slurm package repository at /repo/*. Populated by bundle install.
	// Default: /var/lib/clustr/repo
	RepoDir string `json:"repo_dir"` // CLUSTR_REPO_DIR

	// LogArchiveDir is where log purge summary events are written (future cold archive).
	// Default: /var/lib/clustr/log-archive
	LogArchiveDir string `json:"log_archive_dir"` // CLUSTR_LOG_ARCHIVE_DIR

	// AuditRetention is the TTL for audit_log rows (D13).
	// From CLUSTR_AUDIT_RETENTION (Go duration string, e.g. "90d").
	// Default: 0 (server treats as 90 days).
	AuditRetention time.Duration `json:"audit_retention"` // CLUSTR_AUDIT_RETENTION

	// ClusterName is the human-readable name for this clustr installation.
	// Used as the required typed-confirm string in the dangerous-push gate
	// (CLUSTR_DANGEROUS_GATE_ENABLED). Defaults to "clustr" when unset.
	// From CLUSTR_CLUSTER_NAME.
	ClusterName string `json:"cluster_name"` // CLUSTR_CLUSTER_NAME
}

// PXEConfig holds configuration for the built-in PXE (DHCP + TFTP) server.
type PXEConfig struct {
	// Enabled activates the PXE server on startup (CLUSTR_PXE_ENABLED).
	Enabled bool `json:"enabled"`
	// Interface is the network interface to bind the DHCP server to
	// (CLUSTR_PXE_INTERFACE). Empty means auto-detect.
	Interface string `json:"interface"`
	// IPRange is the DHCP pool as "start-end" (CLUSTR_PXE_RANGE).
	// Default: "10.99.0.100-10.99.0.200".
	IPRange string `json:"ip_range"`
	// ServerIP is the IP advertised as next-server in DHCP offers
	// (CLUSTR_PXE_SERVER_IP). Auto-detected from Interface when empty.
	ServerIP string `json:"server_ip"`
	// BootDir is where the kernel and initramfs are stored
	// (CLUSTR_BOOT_DIR). Default: "/var/lib/clustr/boot".
	BootDir string `json:"boot_dir"`
	// TFTPDir is where TFTP-served boot files (ipxe.efi, undionly.kpxe)
	// live (CLUSTR_TFTP_DIR). Default: "/var/lib/clustr/tftpboot".
	TFTPDir string `json:"tftp_dir"`
	// SubnetCIDR is the prefix length of the provisioning subnet advertised via
	// DHCP Option 1 (subnet mask). Must be between 1 and 30 inclusive.
	// Configured via CLUSTR_PXE_SUBNET_CIDR. Default: 24.
	SubnetCIDR int `json:"subnet_cidr"`
	// HTTPPort is the port the clustr-serverd HTTP API listens on, used by the
	// DHCP server when building the iPXE chainload URL. Populated at runtime
	// from ListenAddr — not a user-facing config field.
	HTTPPort string `json:"-"`
}

// Config holds the full runtime configuration for clustr components.
// Kept for JSON-file based loading compatibility.
type Config struct {
	Server ServerConfig `json:"server"`
}

// LoadServerConfig populates ServerConfig from environment variables with
// sensible production defaults. Environment variables take precedence over defaults.
func LoadServerConfig() ServerConfig {
	return ServerConfig{
		ListenAddr:    envOrDefault("CLUSTR_LISTEN_ADDR", ":8080"),
		ImageDir:      envOrDefault("CLUSTR_IMAGE_DIR", "/var/lib/clustr/images"),
		DBPath:        envOrDefault("CLUSTR_DB_PATH", "/var/lib/clustr/db/clustr.db"),
		AuthToken:     os.Getenv("CLUSTR_AUTH_TOKEN"), // legacy, no longer used for auth enforcement
		AuthDevMode:   os.Getenv("CLUSTR_AUTH_DEV_MODE") == "1",
		SessionSecret: os.Getenv("CLUSTR_SESSION_SECRET"),
		SessionSecure: os.Getenv("CLUSTR_SESSION_SECURE") == "1",
		LogLevel:          envOrDefault("CLUSTR_LOG_LEVEL", "info"),
		LogRetention:      parseLogRetention(),
		LogMaxRowsPerNode: parseLogMaxRows(),
		// Default matches the RPM-installed binary location (/usr/bin/clustr),
		// not the legacy "make install" path /usr/local/bin/clustr. The systemd
		// unit shipped by the RPM sets CLUSTR_BIN_PATH=/usr/bin/clustr explicitly,
		// so this default only fires for non-RPM installs (developer hosts, CI).
		// The previous default pointed to /usr/local/bin/clustr which does not
		// exist on RPM hosts, causing initramfs builds to silently fall through
		// to the os.Executable()-relative fallback in handlers/initramfs.go.
		ClustrBinPath:   envOrDefault("CLUSTR_BIN_PATH", "/usr/bin/clustr"),
		ClientdBinPath: os.Getenv("CLUSTR_CLIENTD_BIN_PATH"), // empty = auto-detect at inject time
		VerifyTimeout: parseVerifyTimeout(),
		PXE:           LoadPXEConfig(),
		RepoDir:       envOrDefault("CLUSTR_REPO_DIR", "/var/lib/clustr/repo"),
		LDAPDataDir:   envOrDefault("CLUSTR_LDAP_DATA_DIR", "/var/lib/clustr/ldap"),
		LDAPConfigDir: envOrDefault("CLUSTR_LDAP_CONFIG_DIR", "/etc/clustr/ldap"),
		LDAPPKIDir:    envOrDefault("CLUSTR_LDAP_PKI_DIR", "/etc/clustr/pki"),
		LogArchiveDir:  envOrDefault("CLUSTR_LOG_ARCHIVE_DIR", "/var/lib/clustr/log-archive"),
		AuditRetention: parseAuditRetention(),
		ClusterName:    envOrDefault("CLUSTR_CLUSTER_NAME", "clustr"),
	}
}

// parseVerifyTimeout parses CLUSTR_VERIFY_TIMEOUT as a Go duration string.
// Minimum: 2m (to allow slow hardware POST sequences). Maximum: 30m.
// Falls back to 5m on parse error or when the env var is not set.
// ADR-0008.
func parseVerifyTimeout() time.Duration {
	const defaultTimeout = 5 * time.Minute
	const minTimeout = 2 * time.Minute
	const maxTimeout = 30 * time.Minute

	v := os.Getenv("CLUSTR_VERIFY_TIMEOUT")
	if v == "" {
		return defaultTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultTimeout
	}
	if d < minTimeout {
		return minTimeout
	}
	if d > maxTimeout {
		return maxTimeout
	}
	return d
}

// parseLogRetention parses CLUSTR_LOG_RETENTION as a Go duration string.
// Falls back to 0 (meaning "use the server default") on parse error or
// when the env var is not set. The server's runLogPurger treats 0 as 7d (D2).
func parseLogRetention() time.Duration {
	v := os.Getenv("CLUSTR_LOG_RETENTION")
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}

// parseAuditRetention parses CLUSTR_AUDIT_RETENTION as a Go duration string.
// Falls back to 0 (meaning "use the server default of 90 days") on parse error.
func parseAuditRetention() time.Duration {
	v := os.Getenv("CLUSTR_AUDIT_RETENTION")
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// parseLogMaxRows parses CLUSTR_LOG_MAX_ROWS_PER_NODE as an integer.
// Falls back to 0 (meaning "use the server default") on parse error or
// when the env var is not set. The server's runLogPurger treats 0 as 50000 (D2).
func parseLogMaxRows() int64 {
	v := os.Getenv("CLUSTR_LOG_MAX_ROWS_PER_NODE")
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// LoadPXEConfig populates PXEConfig from environment variables.
func LoadPXEConfig() PXEConfig {
	return PXEConfig{
		Enabled:    os.Getenv("CLUSTR_PXE_ENABLED") == "true",
		Interface:  os.Getenv("CLUSTR_PXE_INTERFACE"),
		IPRange:    envOrDefault("CLUSTR_PXE_RANGE", "10.99.0.100-10.99.0.200"),
		ServerIP:   os.Getenv("CLUSTR_PXE_SERVER_IP"),
		BootDir:    envOrDefault("CLUSTR_BOOT_DIR", "/var/lib/clustr/boot"),
		TFTPDir:    envOrDefault("CLUSTR_TFTP_DIR", "/var/lib/clustr/tftpboot"),
		SubnetCIDR: parsePXESubnetCIDR(),
	}
}

// parsePXESubnetCIDR reads CLUSTR_PXE_SUBNET_CIDR and returns its value,
// validated to [1, 30]. Falls back to 24 if the variable is unset or invalid.
func parsePXESubnetCIDR() int {
	const defaultCIDR = 24
	v := os.Getenv("CLUSTR_PXE_SUBNET_CIDR")
	if v == "" {
		return defaultCIDR
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 30 {
		return defaultCIDR
	}
	return n
}

// Default returns a Config with sensible production defaults.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			ListenAddr: ":8080",
			ImageDir:   "/var/lib/clustr/images",
			DBPath:     "/var/lib/clustr/db/clustr.db",
			LogLevel:   "info",
		},
	}
}

// Load reads a JSON config file at path. Missing fields fall back to defaults.
func Load(path string) (*Config, error) {
	cfg := Default()
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
