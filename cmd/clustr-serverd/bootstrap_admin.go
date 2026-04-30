package main

// bootstrap_admin.go implements "clustr-serverd bootstrap-admin".
//
// This subcommand creates the initial admin user (username + password) for
// the clustr web UI without requiring the server to be running. It is the
// recommended path for operators who want to set credentials before first
// start, or who need to recover a locked-out admin account.
//
// Usage:
//   clustr-serverd bootstrap-admin                      # idempotent: creates clustr/clustr if absent
//   clustr-serverd bootstrap-admin --username ops \
//                                  --password "S3cr3t!"  # ADD ops admin alongside existing users
//   clustr-serverd bootstrap-admin --replace-default     # explicit: wipe clustr default and re-create
//   clustr-serverd bootstrap-admin --force               # destroy ALL users and start fresh
//   clustr-serverd bootstrap-admin --force \
//                                  --bypass-complexity   # emergency recovery only
//
// Behaviour:
//   - Default (no flags): if "clustr" admin doesn't exist, create it. If it does, leave it alone.
//   - --username X: ADD a new admin alongside existing users. Does NOT delete prior admins.
//   - --replace-default: explicitly wipe the "clustr" default admin and re-create it.
//   - --force: removes ALL existing users before creating the new account.
//   - --bypass-complexity skips the password complexity validator. Use ONLY for
//     emergency credential recovery. The bypass is recorded in the audit log.
//   - At server startup, log a WARN line if the default "clustr" admin is missing.
//   - GET /api/v1/auth/status includes default_admin_present: bool to surface absence.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
)

func init() {
	var flagUsername string
	var flagPassword string
	var flagForce bool
	var flagReplaceDefault bool
	var flagBypassComplexity bool

	bootstrapAdminCmd := &cobra.Command{
		Use:   "bootstrap-admin",
		Short: "Create or add an admin user for the clustr web UI",
		Long: `bootstrap-admin manages admin accounts for the clustr web UI without
requiring the server to be running.

DEFAULT BEHAVIOUR (no flags):
  If the "clustr" default admin does not exist, it is created with password "clustr".
  If it already exists, this is a no-op. Safe to run repeatedly.

ADD A NEW ADMIN (--username X --password Y):
  Adds a new admin account alongside all existing users. Does NOT remove or replace
  any existing admin, including the "clustr" default. If an account with the same
  username already exists, the command fails — use --force to overwrite explicitly.

REPLACE DEFAULT ADMIN (--replace-default):
  Removes only the "clustr" default admin and re-creates it with the default
  credentials. All other users are preserved. Useful when the default account
  was manually corrupted but other admins must remain intact.

WIPE ALL USERS (--force):
  Removes ALL existing users before creating the new account. Use with caution.

The command reads credentials from --username / --password flags, or from the
CLUSTR_BOOTSTRAP_USERNAME / CLUSTR_BOOTSTRAP_PASSWORD environment variables.

WARNING: --bypass-complexity skips the password strength validator and allows
simple passwords such as "clustr/clustr". This flag is for EMERGENCY CREDENTIAL
RECOVERY ONLY. Using it leaves a weak password in the database. The bypass is
recorded in the audit log. Change the password immediately after recovery.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrapAdmin(flagUsername, flagPassword, flagForce, flagReplaceDefault, flagBypassComplexity)
		},
	}
	bootstrapAdminCmd.Flags().StringVar(&flagUsername, "username", "",
		"Admin username to add (or set CLUSTR_BOOTSTRAP_USERNAME)")
	bootstrapAdminCmd.Flags().StringVar(&flagPassword, "password", "",
		"Admin password (or set CLUSTR_BOOTSTRAP_PASSWORD; minimum 8 chars)")
	bootstrapAdminCmd.Flags().BoolVar(&flagForce, "force", false,
		"Remove ALL existing users and create the specified admin account")
	bootstrapAdminCmd.Flags().BoolVar(&flagReplaceDefault, "replace-default", false,
		"Remove only the default 'clustr' admin and re-create it (other users are preserved)")
	bootstrapAdminCmd.Flags().BoolVar(&flagBypassComplexity, "bypass-complexity", false,
		"[EMERGENCY RECOVERY ONLY] Skip password complexity validation. Leaves a weak password — change it immediately. Recorded in audit log.")

	rootCmd.AddCommand(bootstrapAdminCmd)
}

// AuditActionBootstrapAdminBypassComplexity is written to the audit log whenever
// bootstrap-admin is invoked with --bypass-complexity. Query with:
//
//	GET /api/v1/audit?action=auth.bootstrap_admin.bypass_complexity
//
// The new_value JSON contains "username" so post-incident reviews can identify
// which account was created with a weak password.
const AuditActionBootstrapAdminBypassComplexity = "auth.bootstrap_admin.bypass_complexity"

// DefaultAdminUsername is the default admin username created by bootstrap-admin
// when no --username flag is provided. Matches Grafana/Jenkins/Portainer/MinIO
// conventions to reduce first-run friction.
const DefaultAdminUsername = "clustr"

// DefaultAdminPassword is the default password set when no --password flag is
// provided. Works permanently until the operator changes it via Settings.
// No forced password change — the operator owns their security posture.
const DefaultAdminPassword = "clustr"

func runBootstrapAdmin(username, password string, force, replaceDefault, bypassComplexity bool) error {
	cfg := config.LoadServerConfig()

	// Resolve from env vars if flags not set.
	if username == "" {
		username = os.Getenv("CLUSTR_BOOTSTRAP_USERNAME")
	}
	if password == "" {
		password = os.Getenv("CLUSTR_BOOTSTRAP_PASSWORD")
	}

	// Determine whether we are on the default-credentials path:
	// no username/password provided via flags, env vars, or interactive input yet.
	usingDefaults := false
	if username == "" && password == "" {
		username = DefaultAdminUsername
		password = DefaultAdminPassword
		usingDefaults = true
		bypassComplexity = true // default password intentionally doesn't meet complexity
	}

	// Interactive prompt only when username was explicitly set but password wasn't.
	// (If both were empty we already filled both with defaults above.)
	if username == "" {
		fmt.Print("Admin username: ")
		var u string
		if _, err := fmt.Scanln(&u); err != nil {
			return fmt.Errorf("read username: %w", err)
		}
		username = strings.TrimSpace(u)
	}
	if username == "" {
		return fmt.Errorf("username cannot be empty")
	}

	if password == "" {
		pw, err := promptPassword("Admin password (min 8 chars): ")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		password = pw
	}

	if bypassComplexity {
		// Loud warning — this is a security tradeoff documented in the audit log.
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "WARNING: --bypass-complexity is set.\n")
		fmt.Fprintf(os.Stderr, "  Password complexity validation is SKIPPED.\n")
		fmt.Fprintf(os.Stderr, "  This is for EMERGENCY CREDENTIAL RECOVERY only.\n")
		fmt.Fprintf(os.Stderr, "  A weak password will be stored in the database.\n")
		fmt.Fprintf(os.Stderr, "  This action will be recorded in the audit log.\n")
		fmt.Fprintf(os.Stderr, "  ACTION: Change this password manually as soon as recovery is complete.\n")
		fmt.Fprintf(os.Stderr, "\n")
	} else {
		if err := validateBootstrapPassword(password); err != nil {
			return err
		}
	}

	// Open database.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database %s: %w\n"+
			"  hint: run 'clustr-serverd doctor' to check connectivity", cfg.DBPath, err)
	}
	defer database.Close()

	ctx := context.Background()

	// ── Determine what to do based on flags ───────────────────────────────────
	//
	// Three mutually exclusive modes:
	//   1. --force: wipe ALL users, then create the new account.
	//   2. --replace-default: remove only the "clustr" default admin, then re-create it.
	//   3. Default/--username: ADD a new admin without touching existing users.
	//      Special case for the default "clustr" path: idempotent (skip if already exists).

	if force {
		count, err := database.CountUsers(ctx)
		if err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if count > 0 {
			fmt.Fprintf(os.Stderr, "WARNING: --force specified — deleting all %d existing user(s)\n", count)
			if err := database.DeleteAllUsers(ctx); err != nil {
				return fmt.Errorf("delete existing users: %w", err)
			}
			fmt.Fprintf(os.Stderr, "All users removed.\n")
		}
	} else if replaceDefault {
		// Remove only the "clustr" default admin; leave all other users intact.
		existing, err := database.GetUserByUsername(ctx, DefaultAdminUsername)
		if err == nil {
			// Found — delete just this user.
			if err := database.HardDeleteUser(ctx, existing.ID); err != nil {
				return fmt.Errorf("replace-default: delete %q admin: %w", DefaultAdminUsername, err)
			}
			fmt.Fprintf(os.Stderr, "Removed existing %q admin account.\n", DefaultAdminUsername)
		}
		// If not found, that is fine — we are about to create it.
	} else {
		// ADD mode: check whether this username already exists.
		_, lookupErr := database.GetUserByUsername(ctx, username)
		if lookupErr == nil {
			// User exists.
			if usingDefaults {
				// Default idempotent path: clustr/clustr already present — leave it alone.
				fmt.Printf("\nDefault admin %q already exists. No changes made.\n", DefaultAdminUsername)
				fmt.Printf("  To reset it: clustr-serverd bootstrap-admin --replace-default\n")
				fmt.Printf("  To wipe all users: clustr-serverd bootstrap-admin --force\n\n")
				return nil
			}
			// Explicit --username X with a name that already exists.
			return fmt.Errorf(
				"user %q already exists.\n"+
					"  Use --force to remove all users and recreate, or choose a different username.",
				username,
			)
		}
	}

	// Hash password.
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	userID := uuid.New().String()
	rec := db.UserRecord{
		ID:                 userID,
		Username:           username,
		PasswordHash:       string(hash),
		Role:               db.UserRoleAdmin,
		MustChangePassword: false, // default clustr/clustr works permanently; no forced change
		CreatedAt:          time.Now(),
	}
	if err := database.CreateUser(ctx, rec); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	// Audit log — always record every bootstrap-admin invocation regardless of
	// whether complexity was bypassed. This makes CLI-originated credential events
	// visible in post-incident reviews alongside API-originated user creates.
	auditSvc := db.NewAuditService(database)
	auditSvc.Record(ctx,
		"bootstrap-admin",          // actorID — not a real user ID; marks the CLI actor
		"bootstrap-admin (cli)",    // actorLabel
		db.AuditActionUserCreate,   // action
		"user",                     // resourceType
		userID,                     // resourceID
		"",                         // ipAddr — not applicable for CLI
		nil,
		map[string]string{"username": username, "role": "admin"},
	)

	if bypassComplexity {
		auditSvc.Record(ctx,
			"bootstrap-admin",
			"bootstrap-admin (cli)",
			AuditActionBootstrapAdminBypassComplexity,
			"user",
			userID,
			"",
			nil,
			map[string]string{
				"username": username,
				"warning":  "password complexity bypassed via --bypass-complexity flag; change password immediately",
			},
		)
	}

	// Determine web UI URL from listen addr.
	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = ":8080"
	}
	webURL := "http://" + listenAddr
	if strings.HasPrefix(listenAddr, ":") {
		webURL = "http://localhost" + listenAddr
	}

	fmt.Printf("\nAdmin account created:\n")
	fmt.Printf("  Username: %s\n", username)
	fmt.Printf("  Role:     admin\n")
	fmt.Printf("  Web UI:   %s\n", webURL)
	if usingDefaults {
		fmt.Printf("  Password: %s\n", DefaultAdminPassword)
		fmt.Printf("\nDefault credentials work permanently. Change the password via Settings whenever you want.\n")
	} else if bypassComplexity {
		fmt.Printf("\nACTION REQUIRED: Change this password manually as soon as recovery is complete.\n")
		fmt.Printf("  Log in, then go to Settings to set a stronger password.\n")
	}
	fmt.Printf("\nStart the server with: clustr-serverd\n")
	fmt.Printf("Then log in at: %s\n\n", webURL)

	return nil
}

// promptPassword reads a password from stdin.
// Note: echo cannot be suppressed without an external dependency (golang.org/x/term).
// For non-interactive use, prefer the --password flag or CLUSTR_BOOTSTRAP_PASSWORD env var.
func promptPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text()), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no input")
}

// validateBootstrapPassword checks that the password meets the clustr
// complexity requirements (mirrors the server-side validation):
//   - Minimum 8 characters
//   - At least one uppercase letter
//   - At least one lowercase letter
//   - At least one digit
func validateBootstrapPassword(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	var hasUpper, hasLower, hasDigit bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return fmt.Errorf("password must contain at least one uppercase letter, one lowercase letter, and one digit")
	}
	return nil
}
