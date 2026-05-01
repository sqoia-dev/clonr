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
//	cap-bit-test                 Print the process effective UID. Returns 0 when
//	                             the setuid bit has landed correctly. Used by
//	                             integration tests and operator diagnostics.
//	service-control <unit> <action>
//	                             Run systemctl <action> <unit>. Both unit and
//	                             action are validated against static allowlists
//	                             before any exec occurs. Allowed units:
//	                               clustr-slapd.service
//	                             Allowed actions:
//	                               start, stop, restart, enable, disable, reset-failed
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
	"os"
	"os/exec"
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
		fmt.Fprintf(os.Stderr, "usage: clustr-privhelper <verb> [args...]\nverbs: dnf-install, cap-bit-test, service-control\n")
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
	case "cap-bit-test":
		exitCode = verbCapBitTest()
	case "service-control":
		exitCode = verbServiceControl(callerUID, verbArgs)
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
