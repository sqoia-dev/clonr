package deploy

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// ErrUIDConflict is returned when the deployed image already has an account at
// the requested UID with a different username.
type ErrUIDConflict struct {
	UID      int
	Existing string
	Want     string
}

func (e ErrUIDConflict) Error() string {
	return fmt.Sprintf("UID %d is occupied by %q (want %q) — UID conflict in deployed image", e.UID, e.Existing, e.Want)
}

// ErrGIDConflict is returned when the deployed image already has a group at
// the requested GID with a different name.
type ErrGIDConflict struct {
	GID      int
	Existing string
	Want     string
}

func (e ErrGIDConflict) Error() string {
	return fmt.Sprintf("GID %d is occupied by %q (want %q) — GID conflict in deployed image", e.GID, e.Existing, e.Want)
}

// injectSystemAccounts creates groups and accounts in the deployed filesystem
// via groupadd/useradd in chroot. Each entry is processed independently so a
// single failure does not block the remaining accounts.
//
// Groups are created first (sorted by GID), accounts second (sorted by UID).
// Idempotent: entries already present with matching name+id are silently skipped.
// Conflicts (same UID/GID, different name) are logged as warnings and skipped.
func injectSystemAccounts(ctx context.Context, mountRoot string, cfg *api.SystemAccountsNodeConfig) error {
	log := logger()

	// Sort groups by GID, accounts by UID for deterministic injection order.
	groups := make([]api.SystemGroup, len(cfg.Groups))
	copy(groups, cfg.Groups)
	sort.Slice(groups, func(i, j int) bool { return groups[i].GID < groups[j].GID })

	accounts := make([]api.SystemAccount, len(cfg.Accounts))
	copy(accounts, cfg.Accounts)
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].UID < accounts[j].UID })

	// ── Groups ────────────────────────────────────────────────────────────────
	for _, g := range groups {
		if err := injectGroup(ctx, mountRoot, g); err != nil {
			// Non-fatal per account: log and continue.
			log.Warn().Err(err).Str("group", g.Name).Int("gid", g.GID).
				Msg("finalize: system account group injection failed (non-fatal, continuing)")
		}
	}

	// ── Accounts ──────────────────────────────────────────────────────────────
	for _, a := range accounts {
		if err := injectAccount(ctx, mountRoot, a); err != nil {
			log.Warn().Err(err).Str("username", a.Username).Int("uid", a.UID).
				Msg("finalize: system account injection failed (non-fatal, continuing)")
		}
	}

	return nil
}

// injectGroup creates a single group in the deployed chroot if it does not
// already exist with the correct GID.
func injectGroup(ctx context.Context, mountRoot string, g api.SystemGroup) error {
	log := logger()

	// Check whether the GID is already occupied.
	existing, err := chrootGetentGroup(ctx, mountRoot, fmt.Sprintf("%d", g.GID))
	if err == nil && existing != "" {
		// GID is present — check name.
		existingName := parseGetentName(existing)
		if existingName == g.Name {
			log.Debug().Str("group", g.Name).Int("gid", g.GID).
				Msg("finalize: group already exists with matching name+GID — skipping (idempotent)")
			return nil
		}
		return ErrGIDConflict{GID: g.GID, Existing: existingName, Want: g.Name}
	}

	// GID not present — create the group.
	cmd := exec.CommandContext(ctx, "chroot", mountRoot,
		"groupadd", "--gid", fmt.Sprintf("%d", g.GID), g.Name)
	if err := runAndLog(ctx, "groupadd-"+g.Name, cmd); err != nil {
		return fmt.Errorf("groupadd %q (gid %d): %w", g.Name, g.GID, err)
	}
	log.Info().Str("group", g.Name).Int("gid", g.GID).Msg("finalize: group created")
	return nil
}

// injectAccount creates a single account in the deployed chroot if it does not
// already exist with the correct UID.
func injectAccount(ctx context.Context, mountRoot string, a api.SystemAccount) error {
	log := logger()

	// Check whether the UID is already occupied.
	existing, err := chrootGetentPasswd(ctx, mountRoot, fmt.Sprintf("%d", a.UID))
	if err == nil && existing != "" {
		existingName := parseGetentName(existing)
		if existingName == a.Username {
			log.Debug().Str("username", a.Username).Int("uid", a.UID).
				Msg("finalize: account already exists with matching name+UID — skipping (idempotent)")
			return nil
		}
		return ErrUIDConflict{UID: a.UID, Existing: existingName, Want: a.Username}
	}

	// UID not present — build useradd command.
	args := []string{
		mountRoot,
		"useradd",
		"--uid", fmt.Sprintf("%d", a.UID),
		"--gid", fmt.Sprintf("%d", a.PrimaryGID),
		"--shell", a.Shell,
		"--home-dir", a.HomeDir,
		"--password", "!",
	}
	if a.CreateHome {
		args = append(args, "--create-home")
	} else {
		args = append(args, "--no-create-home")
	}
	if a.SystemAccount {
		args = append(args, "--system")
	}
	if a.Comment != "" {
		args = append(args, "--comment", a.Comment)
	}
	args = append(args, a.Username)

	cmd := exec.CommandContext(ctx, "chroot", args...)
	if err := runAndLog(ctx, "useradd-"+a.Username, cmd); err != nil {
		return fmt.Errorf("useradd %q (uid %d): %w", a.Username, a.UID, err)
	}
	log.Info().Str("username", a.Username).Int("uid", a.UID).Msg("finalize: account created")
	return nil
}

// chrootGetentGroup runs getent group <key> in the deployed chroot and returns
// the raw output. Returns an error (and empty string) when the entry is not found.
func chrootGetentGroup(ctx context.Context, mountRoot, key string) (string, error) {
	out, err := exec.CommandContext(ctx, "chroot", mountRoot, "getent", "group", key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// chrootGetentPasswd runs getent passwd <key> in the deployed chroot and returns
// the raw output. Returns an error (and empty string) when the entry is not found.
func chrootGetentPasswd(ctx context.Context, mountRoot, key string) (string, error) {
	out, err := exec.CommandContext(ctx, "chroot", mountRoot, "getent", "passwd", key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// parseGetentName extracts the name (first colon-delimited field) from a getent
// output line such as "munge:x:1002:1002::/var/run/munge:/sbin/nologin".
func parseGetentName(line string) string {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
