package main

// bootstrap_admin.go implements "clustr-serverd bootstrap-admin".
//
// This subcommand creates the initial admin user (username + password) for
// the clustr web UI without requiring the server to be running. It is the
// recommended path for operators who want to set credentials before first
// start, or who need to recover a locked-out admin account.
//
// Usage:
//   clustr-serverd bootstrap-admin                      # interactive
//   clustr-serverd bootstrap-admin --username ops \
//                                  --password "S3cr3t!"  # non-interactive
//   clustr-serverd bootstrap-admin --force               # clobber existing admins
//   clustr-serverd bootstrap-admin --force \
//                                  --bypass-complexity   # emergency recovery only
//
// Behaviour:
//   - If admin accounts already exist, refuses to create another unless --force.
//   - --force deletes ALL existing users and starts fresh. Use with caution.
//   - Created user has must_change_password=false (operator knows the password
//     they set). The auto-generated clustr/clustr default (if any) is removed.
//   - --bypass-complexity skips the password complexity validator. Use ONLY for
//     emergency credential recovery (e.g. clustr/clustr). The bypass is recorded
//     in the audit log so post-incident review can detect weak-password use.
//   - On success, also prints the web UI login URL based on CLUSTR_LISTEN_ADDR.
//   - Idempotent when called multiple times with --force (always overwrites).

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
	var flagBypassComplexity bool

	bootstrapAdminCmd := &cobra.Command{
		Use:   "bootstrap-admin",
		Short: "Create the initial admin user for the clustr web UI",
		Long: `bootstrap-admin creates an admin account for the clustr web UI without
requiring the server to be running. Use this on fresh installs to set your
own credentials instead of changing the default clustr/clustr password.

If admin users already exist, the command refuses to proceed unless --force
is specified. --force removes all existing users before creating the new one.

The command reads credentials from --username / --password flags, or from
the CLUSTR_BOOTSTRAP_USERNAME / CLUSTR_BOOTSTRAP_PASSWORD environment
variables. If neither is provided, you will be prompted interactively.

WARNING: --bypass-complexity skips the password strength validator and allows
simple passwords such as "clustr/clustr". This flag is for EMERGENCY CREDENTIAL
RECOVERY ONLY — for example, when the server binary is the only way back in and
a strong password cannot be typed interactively. Using it leaves a weak password
in the database. The bypass is recorded in the audit log. Change the password
immediately after recovery.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrapAdmin(flagUsername, flagPassword, flagForce, flagBypassComplexity)
		},
	}
	bootstrapAdminCmd.Flags().StringVar(&flagUsername, "username", "",
		"Admin username (or set CLUSTR_BOOTSTRAP_USERNAME)")
	bootstrapAdminCmd.Flags().StringVar(&flagPassword, "password", "",
		"Admin password (or set CLUSTR_BOOTSTRAP_PASSWORD; minimum 8 chars)")
	bootstrapAdminCmd.Flags().BoolVar(&flagForce, "force", false,
		"Remove all existing users and create the specified admin account")
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

func runBootstrapAdmin(username, password string, force, bypassComplexity bool) error {
	cfg := config.LoadServerConfig()

	// Resolve from env vars if flags not set.
	if username == "" {
		username = os.Getenv("CLUSTR_BOOTSTRAP_USERNAME")
	}
	if password == "" {
		password = os.Getenv("CLUSTR_BOOTSTRAP_PASSWORD")
	}

	// Interactive prompt if still empty.
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
		fmt.Fprintf(os.Stderr, "  Change the password immediately after recovery.\n")
		fmt.Fprintf(os.Stderr, "  This action will be recorded in the audit log.\n")
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

	// Check for existing admins.
	count, err := database.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}

	if count > 0 && !force {
		admins, _ := database.CountActiveAdmins(ctx)
		return fmt.Errorf(
			"%d user(s) already exist in the database (%d admin(s)).\n"+
				"  To reset all users and create a fresh admin, re-run with --force.\n"+
				"  WARNING: --force deletes ALL existing users.",
			count, admins)
	}

	if count > 0 && force {
		fmt.Fprintf(os.Stderr, "WARNING: --force specified — deleting all %d existing user(s)\n", count)
		if err := database.DeleteAllUsers(ctx); err != nil {
			return fmt.Errorf("delete existing users: %w", err)
		}
		fmt.Fprintf(os.Stderr, "All users removed.\n")
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
		MustChangePassword: false,
		CreatedAt:          time.Now(),
	}
	if err := database.CreateUser(ctx, rec); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	// Audit log — always record the create; additionally record a bypass event
	// when --bypass-complexity was used so post-incident reviews can find it.
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
	if bypassComplexity {
		fmt.Printf("\nACTION REQUIRED: Change the password immediately — it does not meet complexity requirements.\n")
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
