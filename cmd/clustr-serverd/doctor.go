package main

// doctor.go implements "clustr-serverd doctor" — a pre-flight environment
// checker that scans for the most common configuration errors before the server
// starts. Checks produce a PASS / WARN / FAIL result with a one-line
// remediation hint on non-PASS results.
//
// Exit code: number of FAIL results (0 = all PASS/WARN, 1+ = failures).
// WARN results are informational — the server may still start.
//
// Design intent (I1): run this BEFORE starting the server to catch missing
// secrets, wrong interface names, unwritable paths, missing packages, etc.
// The checks mirror what "GET /api/v1/healthz/ready" reports at runtime, plus
// additional pre-start checks the HTTP endpoint cannot make.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/secrets"
)

func init() {
	var flagJSON bool
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run pre-flight environment checks before starting clustr-serverd",
		Long: `doctor checks the host environment for common configuration errors before
clustr-serverd starts. Run it on a fresh install or after a config change to
surface problems before they cause cryptic server startup failures.

Exit code is the number of FAIL results. WARN results are informational.
0 = everything is PASS or WARN (safe to start). 1+ = critical failures.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(flagJSON)
		},
	}
	doctorCmd.Flags().BoolVar(&flagJSON, "json", false, "Output results as JSON")
	rootCmd.AddCommand(doctorCmd)
}

// doctorCheckResult holds the outcome of a single pre-flight check.
type doctorCheckResult struct {
	Name        string `json:"name"`
	Status      string `json:"status"`      // PASS, WARN, FAIL
	Detail      string `json:"detail"`      // brief human-readable detail
	Remediation string `json:"remediation"` // what to do if not PASS
}

func (r doctorCheckResult) String() string {
	icon := map[string]string{
		"PASS": "  PASS",
		"WARN": "  WARN",
		"FAIL": "  FAIL",
	}[r.Status]
	if icon == "" {
		icon = "  ????"
	}
	line := fmt.Sprintf("%-6s  %-42s  %s", icon, r.Name, r.Detail)
	if r.Status != "PASS" && r.Remediation != "" {
		line += "\n               hint: " + r.Remediation
	}
	return line
}

func runDoctor(outputJSON bool) error {
	cfg := config.LoadServerConfig()

	checks := []doctorCheckResult{
		doctorCheckSecretsFile(),
		doctorCheckSecretKey(cfg),
		doctorCheckSessionSecret(cfg),
		doctorCheckDataDir(),
		doctorCheckPathWritable("image dir", cfg.ImageDir, "mkdir -p "+cfg.ImageDir),
		doctorCheckPathWritable("boot dir", cfg.PXE.BootDir, "mkdir -p "+cfg.PXE.BootDir),
		doctorCheckPathWritable("TFTP dir", cfg.PXE.TFTPDir, "mkdir -p "+cfg.PXE.TFTPDir),
		doctorCheckDBParentDir(cfg),
		doctorCheckDBConnectivity(cfg),
		doctorCheckNICCount(),
		doctorCheckPXEInterface(cfg),
		doctorCheckKVM(),
		doctorCheckBinary("qemu-img"),
		doctorCheckBinary("genisoimage"),
		doctorCheckBinary("xorriso"),
		doctorCheckBinary("rsync"),
		doctorCheckBundleInstalled(cfg),
		doctorCheckListenAddr(cfg),
	}

	failCount := 0
	warnCount := 0
	for _, c := range checks {
		switch c.Status {
		case "FAIL":
			failCount++
		case "WARN":
			warnCount++
		}
	}

	if outputJSON {
		type jsonOutput struct {
			FailCount int                 `json:"fail_count"`
			WarnCount int                 `json:"warn_count"`
			Checks    []doctorCheckResult `json:"checks"`
		}
		out := jsonOutput{FailCount: failCount, WarnCount: warnCount, Checks: checks}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		if failCount > 0 {
			return fmt.Errorf("%d check(s) failed", failCount)
		}
		return nil
	}

	fmt.Println("clustr-serverd pre-flight check")
	fmt.Println(strings.Repeat("─", 78))
	for _, c := range checks {
		fmt.Println(c.String())
	}
	fmt.Println(strings.Repeat("─", 78))
	if failCount == 0 {
		if warnCount > 0 {
			fmt.Printf("Result: all checks passed (%d warning(s) — review above)\n", warnCount)
		} else {
			fmt.Println("Result: all checks passed — safe to start clustr-serverd")
		}
		return nil
	}

	fmt.Printf("Result: %d check(s) FAILED — address them before starting the server\n", failCount)
	return fmt.Errorf("%d check(s) failed", failCount)
}

// --- individual checks ---

func doctorCheckSecretsFile() doctorCheckResult {
	name := "secrets.env readable"
	paths := []string{"/etc/clustr/secrets.env"}
	if p := os.Getenv("CLUSTR_SECRETS_FILE"); p != "" {
		paths = append(paths, p)
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err == nil {
			mode := info.Mode().Perm()
			if mode&0o077 != 0 {
				return doctorCheckResult{
					Name:        name,
					Status:      "WARN",
					Detail:      fmt.Sprintf("%s exists but is group/world readable (mode %o)", p, mode),
					Remediation: "chmod 400 " + p,
				}
			}
			return doctorCheckResult{Name: name, Status: "PASS", Detail: p}
		}
	}
	return doctorCheckResult{
		Name:        name,
		Status:      "WARN",
		Detail:      "not found at /etc/clustr/secrets.env",
		Remediation: "Create /etc/clustr/secrets.env — see docs/install.md §3.2",
	}
}

func doctorCheckSecretKey(cfg config.ServerConfig) doctorCheckResult {
	name := "CLUSTR_SECRET_KEY set"
	if cfg.AuthDevMode {
		return doctorCheckResult{Name: name, Status: "WARN", Detail: "CLUSTR_AUTH_DEV_MODE=1 — key validation skipped (dev only)"}
	}
	if err := secrets.ValidateKey(); err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      err.Error(),
			Remediation: "Add CLUSTR_SECRET_KEY=$(openssl rand -hex 32) to /etc/clustr/secrets.env",
		}
	}
	return doctorCheckResult{Name: name, Status: "PASS", Detail: "present and valid length"}
}

func doctorCheckSessionSecret(cfg config.ServerConfig) doctorCheckResult {
	name := "CLUSTR_SESSION_SECRET set"
	if cfg.AuthDevMode {
		return doctorCheckResult{Name: name, Status: "WARN", Detail: "CLUSTR_AUTH_DEV_MODE=1 — skipped"}
	}
	val := os.Getenv("CLUSTR_SESSION_SECRET")
	if val == "" {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      "not set — new random key generated on every restart (all browser sessions lost)",
			Remediation: "Add CLUSTR_SESSION_SECRET=$(openssl rand -hex 64) to /etc/clustr/secrets.env",
		}
	}
	if len(val) < 32 {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      fmt.Sprintf("only %d chars — recommend 64+ hex chars (32 bytes)", len(val)),
			Remediation: "Use: openssl rand -hex 64",
		}
	}
	return doctorCheckResult{Name: name, Status: "PASS", Detail: fmt.Sprintf("%d-char key", len(val))}
}

func doctorCheckDataDir() doctorCheckResult {
	name := "data root /var/lib/clustr"
	root := "/var/lib/clustr"
	info, err := os.Stat(root)
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      "directory does not exist",
			Remediation: "mkdir -p /var/lib/clustr/{db,images,boot,tftpboot,iso-cache,backups,log-archive,tmp}",
		}
	}
	if !info.IsDir() {
		return doctorCheckResult{Name: name, Status: "FAIL", Detail: root + " is not a directory"}
	}
	tmp, err := os.CreateTemp(root, ".doctor-check-*")
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      "not writable: " + err.Error(),
			Remediation: "chmod 755 /var/lib/clustr",
		}
	}
	tmp.Close()
	os.Remove(tmp.Name())
	return doctorCheckResult{Name: name, Status: "PASS", Detail: "exists and writable"}
}

func doctorCheckPathWritable(label, path, remediation string) doctorCheckResult {
	name := label + " writable"
	if path == "" {
		return doctorCheckResult{Name: name, Status: "WARN", Detail: "path not configured — using default"}
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      "cannot create " + path + ": " + err.Error(),
			Remediation: remediation,
		}
	}
	tmp, err := os.CreateTemp(path, ".doctor-check-*")
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      path + " not writable: " + err.Error(),
			Remediation: remediation,
		}
	}
	tmp.Close()
	os.Remove(tmp.Name())

	// Warn if < 5 GB free on the image dir specifically.
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) == nil {
		freeGB := float64(stat.Bavail) * float64(stat.Bsize) / (1024 * 1024 * 1024)
		if freeGB < 5 {
			return doctorCheckResult{
				Name:        name,
				Status:      "WARN",
				Detail:      fmt.Sprintf("%s — only %.1f GB free", path, freeGB),
				Remediation: "Free disk space — image store needs 200 GB+ for production use",
			}
		}
	}
	return doctorCheckResult{Name: name, Status: "PASS", Detail: path}
}

func doctorCheckDBParentDir(cfg config.ServerConfig) doctorCheckResult {
	name := "DB parent dir writable"
	dir := filepath.Dir(cfg.DBPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      "cannot create " + dir + ": " + err.Error(),
			Remediation: "mkdir -p " + dir,
		}
	}
	tmp, err := os.CreateTemp(dir, ".doctor-check-*")
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      dir + " not writable: " + err.Error(),
			Remediation: "chmod 755 " + dir,
		}
	}
	tmp.Close()
	os.Remove(tmp.Name())
	return doctorCheckResult{Name: name, Status: "PASS", Detail: dir}
}

func doctorCheckDBConnectivity(cfg config.ServerConfig) doctorCheckResult {
	name := "DB connectivity"
	if _, err := os.Stat(cfg.DBPath); os.IsNotExist(err) {
		return doctorCheckResult{Name: name, Status: "PASS", Detail: "DB not yet created — will be initialized on first start"}
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      "cannot open: " + err.Error(),
			Remediation: "Check file permissions on " + cfg.DBPath + "; or: sqlite3 " + cfg.DBPath + " 'PRAGMA integrity_check'",
		}
	}
	defer database.Close()
	if err := database.Ping(context.Background()); err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "FAIL",
			Detail:      "ping failed: " + err.Error(),
			Remediation: "Run: sqlite3 " + cfg.DBPath + " 'PRAGMA integrity_check'",
		}
	}
	return doctorCheckResult{Name: name, Status: "PASS", Detail: cfg.DBPath}
}

func doctorCheckNICCount() doctorCheckResult {
	name := "network interfaces (≥2 NICs)"
	ifaces, err := net.Interfaces()
	if err != nil {
		return doctorCheckResult{Name: name, Status: "WARN", Detail: "cannot enumerate: " + err.Error()}
	}
	var real []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if strings.HasPrefix(iface.Name, "docker") ||
			strings.HasPrefix(iface.Name, "br-") ||
			strings.HasPrefix(iface.Name, "veth") ||
			strings.HasPrefix(iface.Name, "virbr") {
			continue
		}
		if iface.Flags&net.FlagUp != 0 {
			real = append(real, iface.Name)
		}
	}
	if len(real) < 2 {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      fmt.Sprintf("found %d usable interface(s): [%s]", len(real), strings.Join(real, ", ")),
			Remediation: "clustr needs 2 NICs: management + provisioning. Single-NIC hosts cannot run the built-in PXE server.",
		}
	}
	return doctorCheckResult{Name: name, Status: "PASS", Detail: strings.Join(real, ", ")}
}

func doctorCheckPXEInterface(cfg config.ServerConfig) doctorCheckResult {
	name := "PXE interface"
	if !cfg.PXE.Enabled {
		return doctorCheckResult{Name: name, Status: "PASS", Detail: "PXE disabled (CLUSTR_PXE_ENABLED not set)"}
	}
	iface := cfg.PXE.Interface
	if iface == "" {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      "CLUSTR_PXE_INTERFACE not set — auto-detection at startup",
			Remediation: "Set CLUSTR_PXE_INTERFACE=<provisioning-nic> in clustr.env (e.g. eth1, ens3, enp3s0)",
		}
	}
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Name == iface {
			if i.Flags&net.FlagUp == 0 {
				return doctorCheckResult{
					Name:        name,
					Status:      "WARN",
					Detail:      iface + " exists but is DOWN",
					Remediation: "ip link set " + iface + " up",
				}
			}
			return doctorCheckResult{Name: name, Status: "PASS", Detail: iface + " UP"}
		}
	}
	return doctorCheckResult{
		Name:        name,
		Status:      "FAIL",
		Detail:      "interface " + iface + " not found",
		Remediation: "Run 'ip link' to list interfaces; update CLUSTR_PXE_INTERFACE in clustr.env",
	}
}

func doctorCheckKVM() doctorCheckResult {
	name := "/dev/kvm accessible"
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      "not found — ISO builds will use software emulation (3-5x slower)",
			Remediation: "Enable KVM in BIOS (vmx/svm) and load: modprobe kvm-intel (or kvm-amd)",
		}
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      "exists but not writable: " + err.Error(),
			Remediation: "Run as root, or: usermod -aG kvm <service-user>",
		}
	}
	f.Close()
	return doctorCheckResult{Name: name, Status: "PASS", Detail: "readable and writable"}
}

func doctorCheckBinary(bin string) doctorCheckResult {
	name := "binary: " + bin
	path, err := exec.LookPath(bin)
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      "not found in PATH",
			Remediation: "dnf install -y " + bin + "  (or: apt install -y " + bin + ")",
		}
	}
	return doctorCheckResult{Name: name, Status: "PASS", Detail: path}
}

func doctorCheckBundleInstalled(cfg config.ServerConfig) doctorCheckResult {
	name := "Slurm bundle installed"
	repoDir := cfg.RepoDir
	if repoDir == "" {
		repoDir = "/var/lib/clustr/repo"
	}
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      "repo dir not found at " + repoDir,
			Remediation: "Run: clustr-serverd bundle install",
		}
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			vf := filepath.Join(repoDir, e.Name(), ".installed-version")
			if _, err := os.Stat(vf); err == nil {
				return doctorCheckResult{Name: name, Status: "PASS", Detail: e.Name() + " present"}
			}
		}
	}
	return doctorCheckResult{
		Name:        name,
		Status:      "WARN",
		Detail:      "no bundle found in " + repoDir,
		Remediation: "Run: clustr-serverd bundle install",
	}
}

func doctorCheckListenAddr(cfg config.ServerConfig) doctorCheckResult {
	name := "listen addr available"
	addr := cfg.ListenAddr
	if addr == "" {
		addr = ":8080"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return doctorCheckResult{
			Name:        name,
			Status:      "WARN",
			Detail:      addr + " is in use (another process may be listening)",
			Remediation: "Check: ss -tlnp | grep 8080",
		}
	}
	ln.Close()
	return doctorCheckResult{Name: name, Status: "PASS", Detail: addr + " available"}
}
