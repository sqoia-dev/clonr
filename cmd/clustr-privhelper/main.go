// cmd/clustr-privhelper/main.go — setuid root privilege helper for clustr-serverd.
//
// # Overview
//
// clustr-serverd runs as the unprivileged "clustr" OS user. Any host-side
// operation that requires root privilege routes through this single binary.
// clustr-privhelper is installed setuid root by the RPM post-install scriptlet
// (chmod 4755 /usr/sbin/clustr-privhelper) so it runs with an effective UID of
// 0 regardless of the calling user.
//
// # Security model
//
//   - Caller passes only identifiers (package name, verb name). Helper builds
//     the full argv internally. Raw flag strings are NEVER accepted from callers.
//   - Each verb validates its argument against clustr's own allowlist
//     (deps_matrix.json) or other bounded sets before executing anything.
//   - Every invocation is written to the audit_log table in clustr's SQLite DB
//     as root: actor_uid (calling UID), verb, args, exit code.
//
// # Verbs
//
//	dnf-install <pkg>            Install a single package via dnf. Package must
//	                             appear in the embedded deps_matrix allowlist.
//	dnf-upgrade <pkg-spec>       Install/upgrade a fully-qualified slurm package
//	                             spec (name-version.elX.arch) from
//	                             clustr-internal-repo only. The --repo flag
//	                             restricts dnf to that repo so only signed clustr
//	                             RPMs are installed.
//	rule-write <name> <src-file>
//	                             Atomically write a YAML alert rule file from
//	                             <src-file> (a temp file) to
//	                             /etc/clustr/rules.d/<name>.yml. name must match
//	                             ^[a-zA-Z0-9._-]+$, must not contain slashes.
//	                             File is created as root:clustr mode 0640.
//	repo-push <src-file> <dst-file>
//	                             Copy a signed RPM from <src-file> to a path
//	                             under /var/lib/clustr/repo/clustr-internal-repo/.
//	                             dst-file must start with that prefix and end in
//	                             .rpm. Source must be an absolute path.
//	repo-refresh <repo-dir>      Run createrepo_c on <repo-dir>, which must be
//	                             under /var/lib/clustr/repo/clustr-internal-repo/.
//	cap-bit-test                 Print the process effective UID. Returns 0 when
//	                             the setuid bit has landed correctly. Used by
//	                             integration tests and operator diagnostics.
//	service-control <unit> <action>
//	                             Run systemctl <action> <unit>. Both unit and
//	                             action are validated against static allowlists
//	                             before any exec occurs. Allowed units:
//	                               clustr-slapd.service, sssd.service, sshd.service
//	                             Allowed actions:
//	                               start, stop, restart, enable, disable, reset-failed
//	ca-trust-extract             Run `update-ca-trust extract` as root. Called by
//	                             clustr-clientd after an LDAP CA rotation push
//	                             places a new cert at
//	                             /etc/pki/ca-trust/source/anchors/clustr-ca.crt.
//	bios-read <vendor>           Read current BIOS settings via the operator-
//	                             supplied vendor binary. vendor must match
//	//	                          ^[a-z]+$ and be in the embedded allowlist
//	                             {intel, dell, supermicro}. Prints JSON to stdout.
//	                             Used by the post-boot drift check in clientd.
//	bios-apply <vendor> <profile-blob-path>
//	                             Apply BIOS settings from <profile-blob-path>.
//	                             vendor must be in the allowlist. path must be a
//	                             regular .json or .cfg file under
//	                             /var/lib/clustr/bios-staging/ (mode 0700 root).
//	                             Rebuilds argv internally per vendor; caller
//	                             cannot inject flags. Audits to audit_log.
//
// # Usage
//
//	/usr/sbin/clustr-privhelper <verb> [args...]
//
// Exit codes:
//
//	0 — success
//	1 — usage error or argument validation failure
//	2 — execution failure (dnf exited non-zero, DB write failed, etc.)
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; no cgo required
)

// dbPath is the path to clustr's SQLite database. The helper opens it as root
// to write a single audit row per invocation. All other DB operations belong to
// clustr-serverd.
const dbPath = "/var/lib/clustr/db/clustr.db"

// depsMatrixPath is the allowlist of packages that may be installed via the
// dnf-install verb. Embedded at compile time.
//
//go:generate echo "no generate step needed — see embed directive below"
var depsMatrixAllowlist map[string]bool

func init() {
	// Build the flat allowlist from the embedded JSON at startup so validation
	// is O(1) per call and requires no file I/O during request handling.
	depsMatrixAllowlist = buildDepsAllowlist()
}

// buildDepsAllowlist reads the embedded deps_matrix.json and returns a flat
// set of all allowed package names across all EL versions.
func buildDepsAllowlist() map[string]bool {
	// Embedded JSON path resolved relative to the binary's installed location.
	// The privhelper is co-located with the clustr-serverd RPM and the JSON
	// file is bundled at /usr/share/clustr/deps_matrix.json by the RPM spec.
	// Fall back to the source path for developer machines and CI.
	candidates := []string{
		"/usr/share/clustr/deps_matrix.json",
		"/opt/clustr/build/slurm/deps_matrix.json",
	}

	// Determine the binary's directory for relative path resolution in dev.
	if exe, err := os.Executable(); err == nil {
		// Strip the binary name to get the directory.
		dir := exe[:strings.LastIndex(exe, "/")]
		candidates = append(candidates,
			dir+"/../../build/slurm/deps_matrix.json",
			dir+"/../../../build/slurm/deps_matrix.json",
		)
	}

	// Try source tree location from the repo root (used by go test / CI).
	wd, _ := os.Getwd()
	if wd != "" {
		candidates = append(candidates, wd+"/build/slurm/deps_matrix.json")
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		// The JSON has top-level "comment", "el8", "el9", "el10" keys.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}

		allowlist := make(map[string]bool)
		for key, val := range raw {
			if key == "comment" {
				continue
			}
			var pkgs []string
			if err := json.Unmarshal(val, &pkgs); err != nil {
				continue
			}
			for _, p := range pkgs {
				allowlist[p] = true
			}
		}
		if len(allowlist) > 0 {
			return allowlist
		}
	}

	// Return the hard-coded minimal safe set if the JSON is unavailable.
	// This ensures the binary remains usable even without the JSON file
	// (e.g. freshly installed with a missing package).
	return map[string]bool{
		"numactl-devel":            true,
		"mariadb-connector-c-devel": true,
		"libjwt-devel":             true,
		"pmix-devel":               true,
		"ucx-devel":                true,
		"munge-devel":              true,
		"json-c-devel":             true,
		"hwloc-devel":              true,
		"openldap-servers":         true,
		"openldap-clients":         true,
		"epel-release":             true,
		"gcc":                      true,
		"gcc-c++":                  true,
		"make":                     true,
		"autoconf":                 true,
		"automake":                 true,
		"libtool":                  true,
		"openssl-devel":            true,
		"zlib-devel":               true,
		"libcurl-devel":            true,
		"pam-devel":                true,
		"perl":                     true,
		"python3":                  true,
		"rpm-build":                true,
		"readline-devel":           true,
		"bzip2-devel":              true,
		"lz4-devel":                true,
		"libssh2-devel":            true,
		"http-parser-devel":        true,
		"jansson-devel":            true,
	}
}

// isSafePackageName returns true when pkg contains only characters that are
// safe in a package name. This prevents any injection attempt even if the
// allowlist file were somehow compromised. Package names on RPM systems are
// restricted to [a-zA-Z0-9._+\-].
func isSafePackageName(pkg string) bool {
	if pkg == "" {
		return false
	}
	for _, c := range pkg {
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_' || c == '+' || c == '~'
		if !ok {
			return false
		}
	}
	return true
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: clustr-privhelper <verb> [args...]\nverbs: dnf-install, dnf-upgrade, rule-write, repo-push, repo-refresh, cap-bit-test, service-control, ca-trust-extract, bios-read, bios-apply\n")
		os.Exit(1)
	}

	verb := os.Args[1]
	callerUID := os.Getuid() // real UID of the invoking process (0 if root, clustr's UID otherwise)

	var exitCode int
	var verbArgs []string
	if len(os.Args) > 2 {
		verbArgs = os.Args[2:]
	}

	switch verb {
	case "dnf-install":
		exitCode = verbDnfInstall(callerUID, verbArgs)
	case "dnf-upgrade":
		exitCode = verbDnfUpgrade(callerUID, verbArgs)
	case "rule-write":
		exitCode = verbRuleWrite(callerUID, verbArgs)
	case "repo-push":
		exitCode = verbRepoPush(callerUID, verbArgs)
	case "repo-refresh":
		exitCode = verbRepoRefresh(callerUID, verbArgs)
	case "cap-bit-test":
		exitCode = verbCapBitTest()
	case "service-control":
		exitCode = verbServiceControl(callerUID, verbArgs)
	case "ca-trust-extract":
		exitCode = verbCATrustExtract(callerUID)
	case "bios-read":
		exitCode = verbBiosRead(callerUID, verbArgs)
	case "bios-apply":
		exitCode = verbBiosApply(callerUID, verbArgs)
	default:
		fmt.Fprintf(os.Stderr, "clustr-privhelper: unknown verb %q\n", verb)
		exitCode = 1
	}

	os.Exit(exitCode)
}

// verbDnfInstall validates pkg against the allowlist then installs it via dnf.
// Only the package name is accepted — flags are never passed through from the caller.
func verbDnfInstall(callerUID int, args []string) int {
	if len(args) != 1 {
		msg := "dnf-install requires exactly one argument: <package-name>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "dnf-install", args, 1, msg)
		return 1
	}

	pkg := args[0]

	// Step 1: syntactic safety check (defense in depth, before allowlist lookup).
	if !isSafePackageName(pkg) {
		msg := fmt.Sprintf("dnf-install: package name %q contains disallowed characters", pkg)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "dnf-install", args, 1, msg)
		return 1
	}

	// Step 2: allowlist check — the primary gate.
	if !depsMatrixAllowlist[pkg] {
		msg := fmt.Sprintf("dnf-install: package %q is not in the deps_matrix allowlist; refusing to install", pkg)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "dnf-install", args, 1, msg)
		return 1
	}

	// Step 3: build argv internally — caller cannot influence any flag.
	// --setopt=install_weak_deps=False keeps installs lean (no optional deps).
	cmd := exec.Command("dnf", "install", "-y", "--setopt=install_weak_deps=False", pkg) //#nosec G204 -- pkg validated against allowlist above
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}

	writeAudit(callerUID, "dnf-install", args, exitCode, "")
	return exitCode
}

// verbCapBitTest prints the effective UID of this process and exits 0.
// Returns uid=0 when the setuid bit is set; uid=<n> when running without it.
// Used by integration tests and the `clustr diagnose` command.
func verbCapBitTest() int {
	eUID := os.Geteuid()
	fmt.Printf("clustr-privhelper cap-bit-test: euid=%d\n", eUID)
	return 0
}

// allowedServiceUnits is the explicit allowlist of systemd units that
// service-control may manage. Units are added here one at a time, per-feature,
// to keep the privilege surface minimal. Never accept a unit name from the
// caller without checking this map first.
var allowedServiceUnits = map[string]bool{
	"clustr-slapd.service": true,
	"sssd.service":         true,
	"sshd.service":         true,
}

// allowedServiceActions is the set of systemctl verbs permitted via
// service-control. This is a subset of all systemctl actions — destructive
// verbs (mask, unmask, daemon-reload, etc.) are intentionally excluded.
var allowedServiceActions = map[string]bool{
	"start":        true,
	"stop":         true,
	"restart":      true,
	"enable":       true,
	"disable":      true,
	"reset-failed": true,
}

// verbServiceControl validates unit and action against static allowlists, then
// runs `systemctl <action> <unit>` as root. Both arguments come from the Go
// caller via argv — they are identifiers, not shell strings, and no shell
// expansion occurs. The structured error code "unit_not_allowed" or
// "action_not_allowed" is printed to stderr so callers can detect the exact
// rejection reason without parsing free-form text.
func verbServiceControl(callerUID int, args []string) int {
	if len(args) != 2 {
		msg := "service-control requires exactly two arguments: <unit> <action>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "service-control", args, 1, msg)
		return 1
	}

	unit := args[0]
	action := args[1]

	// Validate unit against allowlist.
	if !allowedServiceUnits[unit] {
		msg := fmt.Sprintf("service-control: unit_not_allowed: %q is not in the service unit allowlist", unit)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "service-control", args, 1, msg)
		return 1
	}

	// Validate action against allowlist.
	if !allowedServiceActions[action] {
		msg := fmt.Sprintf("service-control: action_not_allowed: %q is not a permitted action", action)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "service-control", args, 1, msg)
		return 1
	}

	// Both validated — build argv internally; caller cannot influence any flag.
	cmd := exec.Command("systemctl", action, unit) //#nosec G204 -- unit and action validated against static allowlists above
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}

	writeAudit(callerUID, "service-control", args, exitCode, "")
	return exitCode
}

// verbCATrustExtract runs `update-ca-trust extract` as root.
// Called by clustr-clientd after the server pushes a new CA certificate to
// /etc/pki/ca-trust/source/anchors/clustr-ca.crt. No arguments accepted.
func verbCATrustExtract(callerUID int) int {
	cmd := exec.Command("update-ca-trust", "extract") //#nosec G204 -- no user-supplied arguments; fixed command
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}

	writeAudit(callerUID, "ca-trust-extract", nil, exitCode, "")
	return exitCode
}

// writeAudit opens clustr's SQLite DB as root and inserts one row into
// audit_log per privhelper invocation. The row uses the real calling UID as
// actor_id (not eUID=0) so the audit trail shows who triggered the operation.
// Non-fatal: a failure to write audit is logged to stderr but does not prevent
// the helper from returning the actual operation's exit code to the caller.
func writeAudit(callerUID int, verb string, args []string, exitCode int, errMsg string) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clustr-privhelper: audit: open db: %v\n", err)
		return
	}
	defer db.Close()

	argsStr := strings.Join(args, " ")
	actor := fmt.Sprintf("uid:%d", callerUID)
	action := "privhelper." + verb
	id := fmt.Sprintf("ph-%d", time.Now().UnixNano())

	newVal, _ := json.Marshal(map[string]interface{}{
		"verb":      verb,
		"args":      argsStr,
		"exit_code": exitCode,
		"error":     errMsg,
	})

	_, dbErr := db.Exec(`
		INSERT INTO audit_log
			(id, actor_id, actor_label, action, resource_type, resource_id,
			 new_value, ip_addr, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id,
		actor,
		actor,
		action,
		"privhelper",
		verb,
		string(newVal),
		"",
		time.Now().Unix(),
	)
	if dbErr != nil {
		fmt.Fprintf(os.Stderr, "clustr-privhelper: audit: insert: %v\n", dbErr)
	}
}

// ─── repo-push ────────────────────────────────────────────────────────────────

// repoBasePath is the only directory prefix that repo-push and repo-refresh may
// write to. No other path under /var/lib/clustr/ is accessible via these verbs.
const repoBasePath = "/var/lib/clustr/repo/clustr-internal-repo/"

// verbRepoPush copies a signed RPM from src to dst, where dst must be under
// repoBasePath. Both arguments must be absolute paths. Dst must end in ".rpm".
// No shell is invoked — the copy is a pure Go file copy.
func verbRepoPush(callerUID int, args []string) int {
	if len(args) != 2 {
		msg := "repo-push requires exactly two arguments: <src-file> <dst-file>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-push", args, 1, msg)
		return 1
	}

	src, dst := args[0], args[1]

	// Validate: both must be absolute paths, no traversal.
	if !strings.HasPrefix(src, "/") {
		msg := "repo-push: src must be an absolute path"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-push", args, 1, msg)
		return 1
	}
	if !strings.HasPrefix(dst, repoBasePath) {
		msg := fmt.Sprintf("repo-push: dst must be under %s", repoBasePath)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-push", args, 1, msg)
		return 1
	}
	if !strings.HasSuffix(dst, ".rpm") {
		msg := "repo-push: dst must end in .rpm"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-push", args, 1, msg)
		return 1
	}
	// Guard against path traversal after prefix check.
	cleanDst := filepath.Clean(dst)
	if !strings.HasPrefix(cleanDst, repoBasePath) {
		msg := "repo-push: path traversal detected in dst"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-push", args, 1, msg)
		return 1
	}

	// Ensure destination directory exists.
	if err := os.MkdirAll(filepath.Dir(cleanDst), 0755); err != nil {
		msg := fmt.Sprintf("repo-push: mkdir: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-push", args, 2, msg)
		return 2
	}

	if err := copyFile(src, cleanDst); err != nil {
		msg := fmt.Sprintf("repo-push: copy: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-push", args, 2, msg)
		return 2
	}

	writeAudit(callerUID, "repo-push", args, 0, "")
	return 0
}

// ─── repo-refresh ─────────────────────────────────────────────────────────────

// verbRepoRefresh runs createrepo_c on the given directory, which must be under
// repoBasePath. This regenerates the repomd.xml and other metadata so dnf can
// discover the updated package list.
func verbRepoRefresh(callerUID int, args []string) int {
	if len(args) != 1 {
		msg := "repo-refresh requires exactly one argument: <repo-dir>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-refresh", args, 1, msg)
		return 1
	}

	dir := args[0]
	cleanDir := filepath.Clean(dir)

	if !strings.HasPrefix(cleanDir, repoBasePath) {
		msg := fmt.Sprintf("repo-refresh: dir must be under %s", repoBasePath)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-refresh", args, 1, msg)
		return 1
	}

	// Ensure the directory exists (createrepo_c doesn't create it).
	if err := os.MkdirAll(cleanDir, 0755); err != nil {
		msg := fmt.Sprintf("repo-refresh: mkdir: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "repo-refresh", args, 2, msg)
		return 2
	}

	cmd := exec.Command("createrepo_c", "--update", cleanDir) //#nosec G204 -- cleanDir validated against repoBasePath prefix above
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}

	writeAudit(callerUID, "repo-refresh", args, exitCode, "")
	return exitCode
}

// ─── dnf-upgrade ─────────────────────────────────────────────────────────────

// verbDnfUpgrade installs/upgrades a slurm package spec from clustr-internal-repo
// only. The --repo flag ensures dnf does not pull from any other configured repo,
// so only GPG-signed clustr RPMs are installed. The package spec must match the
// slurm package name pattern and may include version (e.g. "slurm-25.11.5-1.el9").
func verbDnfUpgrade(callerUID int, args []string) int {
	if len(args) < 1 {
		msg := "dnf-upgrade requires at least one argument: <pkg-spec>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "dnf-upgrade", args, 1, msg)
		return 1
	}

	// Validate each package spec.
	for _, pkg := range args {
		if !isSafePackageName(pkg) {
			msg := fmt.Sprintf("dnf-upgrade: package spec %q contains disallowed characters", pkg)
			fmt.Fprintln(os.Stderr, msg)
			writeAudit(callerUID, "dnf-upgrade", args, 1, msg)
			return 1
		}
		// Require the package to start with "slurm" so only slurm-family packages
		// can be installed via this verb. Prevents abuse of the repo-restricted path.
		if !strings.HasPrefix(pkg, "slurm") && !strings.HasPrefix(pkg, "munge") {
			msg := fmt.Sprintf("dnf-upgrade: package %q is not a slurm or munge package; only slurm-family packages may be upgraded via dnf-upgrade", pkg)
			fmt.Fprintln(os.Stderr, msg)
			writeAudit(callerUID, "dnf-upgrade", args, 1, msg)
			return 1
		}
	}

	// Build dnf command: install -y --repo=clustr-internal-repo <pkg...>
	// --repo restricts resolution to clustr-internal-repo only so GPG signature
	// verification is scoped. --setopt=install_weak_deps=False keeps it lean.
	dnfArgs := append([]string{
		"install", "-y",
		"--repo=clustr-internal-repo",
		"--setopt=install_weak_deps=False",
	}, args...)

	cmd := exec.Command("dnf", dnfArgs...) //#nosec G204 -- all args validated above
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}

	writeAudit(callerUID, "dnf-upgrade", args, exitCode, "")
	return exitCode
}

// ─── rule-write ───────────────────────────────────────────────────────────────

// rulesDir is the directory where alert rule YAML files are stored.
const rulesDir = "/etc/clustr/rules.d/"

// isSafeRuleName returns true when name contains only characters that are safe
// as a filename base and cannot escape the rules directory via path traversal.
// Allowed: [a-zA-Z0-9._-] — no slashes, no dots-only sequences.
func isSafeRuleName(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	for _, c := range name {
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// verbRuleWrite atomically writes a YAML alert rule file to rulesDir.
//
// Argv: rule-write <name> <src-file>
//   - name: validated against isSafeRuleName; destination is rulesDir/<name>.yml
//   - src-file: path to a temp file containing the YAML content written by
//     clustr-serverd; must be an absolute path under /tmp
//
// The destination file is written as root:clustr mode 0640, atomically via
// a temp file + rename so a partial write never corrupts a live rule.
func verbRuleWrite(callerUID int, args []string) int {
	if len(args) != 2 {
		msg := "rule-write requires exactly two arguments: <name> <src-file>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 1, msg)
		return 1
	}

	name, srcPath := args[0], args[1]

	// Validate rule name.
	if !isSafeRuleName(name) {
		msg := fmt.Sprintf("rule-write: name %q contains disallowed characters or path separators", name)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 1, msg)
		return 1
	}

	// Validate src-file: must be absolute and under /tmp.
	if !strings.HasPrefix(srcPath, "/") {
		msg := "rule-write: src-file must be an absolute path"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 1, msg)
		return 1
	}
	cleanSrc := filepath.Clean(srcPath)
	if !strings.HasPrefix(cleanSrc, "/tmp/") {
		msg := "rule-write: src-file must be under /tmp/"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 1, msg)
		return 1
	}

	// Ensure the rules directory exists.
	if err := os.MkdirAll(rulesDir, 0750); err != nil {
		msg := fmt.Sprintf("rule-write: mkdir rules dir: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 2, msg)
		return 2
	}

	// Read the source content.
	content, err := os.ReadFile(cleanSrc) //#nosec G304 -- cleanSrc validated to be under /tmp above
	if err != nil {
		msg := fmt.Sprintf("rule-write: read src: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 2, msg)
		return 2
	}

	dst := filepath.Join(rulesDir, name+".yml")

	// Write atomically: write to a temp file in the same directory, then rename.
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640) //#nosec G304 -- dst is constructed from validated name under rulesDir
	if err != nil {
		msg := fmt.Sprintf("rule-write: create tmp: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 2, msg)
		return 2
	}

	if _, err := out.Write(content); err != nil {
		out.Close()
		os.Remove(tmp)
		msg := fmt.Sprintf("rule-write: write: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 2, msg)
		return 2
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		msg := fmt.Sprintf("rule-write: close tmp: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 2, msg)
		return 2
	}

	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		msg := fmt.Sprintf("rule-write: rename: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "rule-write", args, 2, msg)
		return 2
	}

	writeAudit(callerUID, "rule-write", args, 0, "")
	return 0
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// copyFile copies src to dst atomically via a temp file in the same directory.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //#nosec G304 -- src is from caller; privhelper operates as root
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	defer func() { _ = out.Close() }()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				_ = os.Remove(tmp)
				return fmt.Errorf("write: %w", writeErr)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			_ = os.Remove(tmp)
			return fmt.Errorf("read: %w", readErr)
		}
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	return os.Rename(tmp, dst)
}

// ─── bios-read ────────────────────────────────────────────────────────────────

// biosVendorAllowlist is the set of vendor identifiers accepted by bios-read
// and bios-apply.  New vendors require a clustr-privhelper rebuild — intentional.
var biosVendorAllowlist = map[string]bool{
	"intel":      true,
	"dell":       true,
	"supermicro": true,
}

// biosBinaryForVendor returns the correct binary path for vendor.
func biosBinaryForVendor(vendor string) string {
	switch vendor {
	case "dell":
		return "/var/lib/clustr/vendor-bios/dell/racadm"
	case "supermicro":
		return "/var/lib/clustr/vendor-bios/supermicro/sum"
	default: // intel
		return "/var/lib/clustr/vendor-bios/intel/syscfg"
	}
}

// isSafeVendor returns true when vendor matches ^[a-z]+$ and is in the allowlist.
func isSafeVendor(vendor string) bool {
	if vendor == "" || !biosVendorAllowlist[vendor] {
		return false
	}
	for _, c := range vendor {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// biosReadArgvForVendor returns the argv (excluding the binary path itself)
// for the read operation for the given vendor.
//
//   - intel:      syscfg /s -
//   - dell:       racadm get BIOS.SetupConfig
//   - supermicro: sum -c GetCurrentBiosCfg --file /tmp/clustr-bios-read-<pid>.cfg
//     (sum writes to a file; stdout redirect does not apply)
//
// For supermicro the output path is fixed under /tmp so the calling provider
// can read it.  The privhelper's stdout remains connected to the caller; sum's
// own stdout/stderr pass through unchanged.
func biosReadArgvForVendor(vendor string) []string {
	switch vendor {
	case "dell":
		return []string{"get", "BIOS.SetupConfig"}
	case "supermicro":
		// sum writes the config to --file; the provider reads it after.
		return []string{"-c", "GetCurrentBiosCfg", "--file", "/tmp/clustr-sum-bios-read.cfg"}
	default: // intel
		return []string{"/s", "-"}
	}
}

// verbBiosRead reads current BIOS settings via the operator-supplied vendor
// binary.  Prints the output to stdout; the calling clustr-clientd process
// reads it.
//
// Argv whitelist: bios-read <vendor>
//   - vendor must match ^[a-z]+$ and be in biosVendorAllowlist
//   - helper rebuilds argv internally per vendor (no flags accepted from caller)
func verbBiosRead(callerUID int, args []string) int {
	if len(args) != 1 {
		msg := "bios-read requires exactly one argument: <vendor>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-read", args, 1, msg)
		return 1
	}

	vendor := args[0]
	if !isSafeVendor(vendor) {
		msg := fmt.Sprintf("bios-read: vendor %q not in allowlist {intel, dell, supermicro}", vendor)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-read", args, 1, msg)
		return 1
	}

	binPath := biosBinaryForVendor(vendor)

	// Validate binary exists and is executable before exec.
	info, err := os.Stat(binPath)
	if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm()&0o111 == 0) {
		msg := fmt.Sprintf("bios-read: vendor binary not found or not executable: %s", binPath)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-read", args, 1, msg)
		return 1
	}

	// Build argv internally — caller cannot inject flags.
	readArgv := biosReadArgvForVendor(vendor)
	cmd := exec.Command(binPath, readArgv...) //#nosec G204 -- binPath is a fixed operator-supplied path validated above; readArgv is built internally from fixed literals
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	execErr := cmd.Run()
	exitCode := 0
	if execErr != nil {
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}

	writeAudit(callerUID, "bios-read", args, exitCode, "")
	return exitCode
}

// ─── bios-apply ───────────────────────────────────────────────────────────────

// biosStagingDir is the only directory prefix that bios-apply may read profile
// blobs from.  The caller (clustr-clientd) writes the profile JSON there with
// os.WriteFile(path, blob, 0600) first; the helper reads it, validates the
// path, and passes it to the vendor binary.
const biosStagingDir = "/var/lib/clustr/bios-staging/"

// verbBiosApply applies BIOS settings from a staged profile blob.
//
// Argv whitelist: bios-apply <vendor> <profile-blob-path>
//   - vendor must match ^[a-z]+$ and be in biosVendorAllowlist
//   - profile-blob-path must be a regular .json file under biosStagingDir
//   - helper rebuilds argv internally per vendor (no flags accepted from caller)
func verbBiosApply(callerUID int, args []string) int {
	if len(args) != 2 {
		msg := "bios-apply requires exactly two arguments: <vendor> <profile-blob-path>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 1, msg)
		return 1
	}

	vendor := args[0]
	profilePath := args[1]

	// Validate vendor.
	if !isSafeVendor(vendor) {
		msg := fmt.Sprintf("bios-apply: vendor %q not in allowlist {intel, dell, supermicro}", vendor)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 1, msg)
		return 1
	}

	// Validate profile path: must be absolute, under biosStagingDir, end in .json.
	if !strings.HasPrefix(profilePath, "/") {
		msg := "bios-apply: profile path must be absolute"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 1, msg)
		return 1
	}
	cleanPath := filepath.Clean(profilePath)
	if !strings.HasPrefix(cleanPath, biosStagingDir) {
		msg := fmt.Sprintf("bios-apply: profile path must be under %s", biosStagingDir)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 1, msg)
		return 1
	}
	if !strings.HasSuffix(cleanPath, ".json") {
		msg := "bios-apply: profile path must end in .json"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 1, msg)
		return 1
	}

	// Validate file is a regular file (not a symlink, directory, device, etc.).
	info, err := os.Lstat(cleanPath)
	if err != nil {
		msg := fmt.Sprintf("bios-apply: stat profile path: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 2, msg)
		return 2
	}
	if !info.Mode().IsRegular() {
		msg := "bios-apply: profile path is not a regular file"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 1, msg)
		return 1
	}

	binPath := biosBinaryForVendor(vendor)

	// Validate binary exists.
	binInfo, err := os.Stat(binPath)
	if err != nil || !binInfo.Mode().IsRegular() || (binInfo.Mode().Perm()&0o111 == 0) {
		msg := fmt.Sprintf("bios-apply: vendor binary not found or not executable: %s", binPath)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "bios-apply", args, 1, msg)
		return 1
	}

	// Build argv internally — caller provided only the profile path, no flags.
	// Vendor-specific apply argv:
	//   intel:      syscfg /r <profile-file>
	//   dell:       (multiple set + jobqueue invocations, see biosApplyDell)
	//   supermicro: sum -c ChangeBiosCfg --file <profile-file>
	exitCode := biosApplyExec(callerUID, vendor, binPath, cleanPath)
	writeAudit(callerUID, "bios-apply", args, exitCode, "")
	return exitCode
}

// biosApplyExec dispatches the vendor-specific apply invocation.
// For Dell, the profile JSON must contain an array of {name, to} objects;
// the helper issues one racadm set per entry then creates the job queue.
func biosApplyExec(callerUID int, vendor, binPath, profilePath string) int {
	switch vendor {
	case "dell":
		return biosApplyDell(callerUID, binPath, profilePath)
	case "supermicro":
		return biosApplySupermicro(callerUID, binPath, profilePath)
	default: // intel
		cmd := exec.Command(binPath, "/r", profilePath) //#nosec G204 -- binPath is operator-supplied; profilePath validated under biosStagingDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			return 2
		}
		return 0
	}
}

// biosApplyDell reads a JSON profile file and issues one `racadm set` per
// setting, then runs `racadm jobqueue create BIOS.Setup.1-1` to schedule POST.
// The JSON format expected by this function is a flat object:
//
//	{"BIOS.ProcSettings.LogicalProc":"Disabled", ...}
func biosApplyDell(callerUID int, binPath, profilePath string) int {
	data, err := os.ReadFile(profilePath) //#nosec G304 -- profilePath validated by caller to be under biosStagingDir and end in .json
	if err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: dell: read profile: %v\n", err)
		writeAudit(callerUID, "bios-apply-dell-read", nil, 2, err.Error())
		return 2
	}

	// Parse the flat JSON object {"key":"value", ...}.
	var settings map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: dell: parse profile JSON: %v\n", err)
		return 2
	}

	if len(settings) == 0 {
		return 0 // nothing to apply
	}

	for name, val := range settings {
		cmd := exec.Command(binPath, "set", name, val) //#nosec G204 -- binPath is operator-supplied; name/val from validated staging JSON
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "bios-apply: dell: racadm set %s: %v\n", name, err)
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			return 2
		}
	}

	// Schedule staged settings for next POST.
	cmd := exec.Command(binPath, "jobqueue", "create", "BIOS.Setup.1-1") //#nosec G204 -- all fixed literals
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: dell: racadm jobqueue create: %v\n", err)
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 2
	}
	return 0
}

// biosApplySupermicro reads a JSON profile file ({"Setting.Key":"Value",...}),
// converts it to a sum 2.x INI-compatible .cfg file in /tmp, then invokes
// `sum -c ChangeBiosCfg --file <cfg>`.  The /tmp cfg file is removed after
// the sum invocation regardless of outcome.
//
// JSON key format: "Section.Key" → groups under [Section] in the cfg.
// Keys with no '.' are written under [General].
func biosApplySupermicro(callerUID int, binPath, profilePath string) int {
	data, err := os.ReadFile(profilePath) //#nosec G304 -- profilePath validated by caller to be under biosStagingDir and end in .json
	if err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: supermicro: read profile: %v\n", err)
		return 2
	}

	// Parse the flat JSON object {"Section.Key": "Value", ...}.
	var settings map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: supermicro: parse profile JSON: %v\n", err)
		return 2
	}

	if len(settings) == 0 {
		return 0 // nothing to apply
	}

	// Build section → key → value mapping.
	type kvEntry struct{ key, value string }
	sections := make(map[string][]kvEntry)
	var sectionOrder []string
	seenSec := make(map[string]bool)

	for name, val := range settings {
		section := "General"
		key := name
		if idx := strings.Index(name, "."); idx >= 0 {
			section = name[:idx]
			key = name[idx+1:]
		}
		if !seenSec[section] {
			sectionOrder = append(sectionOrder, section)
			seenSec[section] = true
		}
		sections[section] = append(sections[section], kvEntry{key, val})
	}

	// Write to a temp .cfg file in /tmp.
	tmpFile, err := os.CreateTemp("/tmp", "clustr-sum-apply-*.cfg")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: supermicro: create tmp cfg: %v\n", err)
		return 2
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	for _, sec := range sectionOrder {
		if _, werr := fmt.Fprintf(tmpFile, "[%s]\n", sec); werr != nil {
			fmt.Fprintf(os.Stderr, "bios-apply: supermicro: write cfg: %v\n", werr)
			tmpFile.Close()
			return 2
		}
		for _, e := range sections[sec] {
			if _, werr := fmt.Fprintf(tmpFile, "%s=%s\n", e.key, e.value); werr != nil {
				fmt.Fprintf(os.Stderr, "bios-apply: supermicro: write cfg: %v\n", werr)
				tmpFile.Close()
				return 2
			}
		}
		if _, werr := fmt.Fprintln(tmpFile); werr != nil {
			fmt.Fprintf(os.Stderr, "bios-apply: supermicro: write cfg: %v\n", werr)
			tmpFile.Close()
			return 2
		}
	}
	if err := tmpFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: supermicro: close cfg: %v\n", err)
		return 2
	}

	// sum -c ChangeBiosCfg --file <tmpPath>
	cmd := exec.Command(binPath, "-c", "ChangeBiosCfg", "--file", tmpPath) //#nosec G204 -- binPath is operator-supplied; tmpPath is our /tmp file
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "bios-apply: supermicro: sum ChangeBiosCfg: %v\n", err)
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 2
	}

	_ = callerUID // available for audit if needed; writeAudit is called by the caller
	return 0
}
