package main

// admin_backup.go — Sprint 43-prime Day 4 BACKUP-CLI
//
// Implements `clustr admin backup --out <path>`
//
// This is a LOCAL-ONLY command; it does not contact clustr-serverd.
// It must be run on the clustr-serverd host by root (or a user with
// read access to the data directory) before any reprovision or
// destructive operation.
//
// What it archives:
//   - /var/lib/clustr/db/         — SQLite database + stats DB
//   - /var/lib/clustr/images/     — base image blobs and rootfs trees
//   - /var/lib/clustr/repo/       — signed bundle RPMs (el9-x86_64, etc.)
//   - /var/lib/clustr/boot/       — kernel/initramfs assets
//   - /var/lib/clustr/tftpboot/   — iPXE binaries
//   - /var/lib/clustr/ldap/       — slapd MDB files
//   - /var/lib/clustr/iso-cache/  — cached ISO downloads
//   - /etc/clustr/                — runtime config (not secrets.env — that is
//                                   captured by the full /etc/clustr/ tree which
//                                   includes secrets.env; see NOTE below)
//
// NOTE: /etc/clustr/secrets.env contains CLUSTR_SESSION_SECRET and
// CLUSTR_SECRET_KEY.  These are captured inside the archive because a reprovision
// needs them to reconstruct a working server.  The archive itself must be stored
// on a trusted, access-controlled destination.  Never push the archive to a
// public or shared location.
//
// Usage:
//
//	clustr admin backup --out /backup/$(date +%F).tar.zst
//	clustr admin backup --out /mnt/nas/clustr-backup.tar.zst --data-dir /var/lib/clustr
//
// The command requires GNU tar with zstd support (tar 1.34+, standard on
// Rocky Linux 9 via the tar package).  If tar is not found or does not
// support --zstd, the command exits with a clear error message.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// defaultDataDir is the standard clustr-serverd data directory.
const defaultDataDir = "/var/lib/clustr"

// etcClusterDir is the config directory always included in the backup.
const etcClusterDir = "/etc/clustr"

func newAdminBackupCmd() *cobra.Command {
	var (
		flagOut     string
		flagDataDir string
		flagNoEtc   bool
	)

	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Archive the full data directory to a tar.zst file",
		Long: `Archive the clustr data directory and config to a compressed tar archive.

Run this command on the clustr-serverd host BEFORE any reprovision or
destructive host operation. The archive captures:

  - Database (db/)
  - Base images and rootfs blobs (images/)
  - Bundle RPM repositories (repo/)
  - Boot assets and TFTP binaries (boot/, tftpboot/)
  - LDAP MDB data (ldap/)
  - ISO cache (iso-cache/)
  - Config directory (/etc/clustr/ unless --no-etc is set)

Restore: extract with 'tar -x --zstd -f <archive> -C /'

Requirements:
  - GNU tar with zstd support (tar 1.34+, standard on Rocky Linux 9)
  - Sufficient space at the destination for the archive
  - Read access to the data directory (typically requires root)

Example:
  clustr admin backup --out /mnt/nas/clustr-$(date +%F).tar.zst`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdminBackup(flagOut, flagDataDir, flagNoEtc)
		},
	}

	cmd.Flags().StringVar(&flagOut, "out", "", "Destination path for the tar.zst archive (required)")
	cmd.Flags().StringVar(&flagDataDir, "data-dir", defaultDataDir, "clustr data directory to archive")
	cmd.Flags().BoolVar(&flagNoEtc, "no-etc", false, "Skip /etc/clustr/ from the archive (not recommended)")

	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runAdminBackup(out, dataDir string, noEtc bool) error {
	// ── Resolve absolute paths ────────────────────────────────────────────────

	absOut, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("backup: resolve output path: %w", err)
	}

	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("backup: resolve data dir: %w", err)
	}

	// ── Pre-flight checks ─────────────────────────────────────────────────────

	// Reject if output already exists — operator should supply a dated path.
	if _, err := os.Stat(absOut); err == nil {
		return fmt.Errorf("backup: output file already exists: %s\n  Use a different path or remove the existing file first", absOut)
	}

	// Verify the data directory exists and is a directory.
	info, err := os.Stat(absDataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("backup: data directory does not exist: %s", absDataDir)
		}
		return fmt.Errorf("backup: stat data dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup: data-dir is not a directory: %s", absDataDir)
	}

	// Verify GNU tar supports --zstd.  A quick capability probe avoids a
	// confusing mid-run failure on systems with an older tar.
	if err := probeZstdTar(); err != nil {
		return err
	}

	// ── Build tar arguments ───────────────────────────────────────────────────

	// We always create the archive atomically: write to a .tmp file and rename
	// on success.  This prevents a partial archive from being mistaken for a
	// complete one.
	tmpOut := absOut + ".tmp"

	// Remove the tmp file if we exit early for any reason.
	defer func() {
		_ = os.Remove(tmpOut)
	}()

	// Collect what to include.  We archive from absolute paths using -C / so
	// extraction via 'tar -x -C /' restores everything in place.
	tarArgs := buildTarArgs(tmpOut, absDataDir, noEtc)

	fmt.Fprintf(os.Stderr, "clustr backup: starting archive\n")
	fmt.Fprintf(os.Stderr, "  data dir : %s\n", absDataDir)
	if !noEtc {
		fmt.Fprintf(os.Stderr, "  etc dir  : %s\n", etcClusterDir)
	}
	fmt.Fprintf(os.Stderr, "  output   : %s\n", absOut)
	fmt.Fprintf(os.Stderr, "\n")

	start := time.Now()

	tarCmd := exec.Command("tar", tarArgs...) //nolint:gosec // args are controlled internally
	tarCmd.Stdout = os.Stdout
	tarCmd.Stderr = os.Stderr // stream tar's verbose output so operator can see progress

	if err := tarCmd.Run(); err != nil {
		// Remove the partial tmp file before returning the error.
		_ = os.Remove(tmpOut)
		return fmt.Errorf("backup: tar failed: %w", err)
	}

	// ── Atomic rename ─────────────────────────────────────────────────────────
	if err := os.Rename(tmpOut, absOut); err != nil {
		return fmt.Errorf("backup: rename tmp to final: %w", err)
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	elapsed := time.Since(start).Round(time.Second)

	fi, err := os.Stat(absOut)
	if err != nil {
		return fmt.Errorf("backup: stat final archive: %w", err)
	}
	sizeMiB := float64(fi.Size()) / (1024 * 1024)

	fmt.Fprintf(os.Stderr, "\nclustr backup: complete\n")
	fmt.Fprintf(os.Stderr, "  archive  : %s\n", absOut)
	fmt.Fprintf(os.Stderr, "  size     : %.1f MiB\n", sizeMiB)
	fmt.Fprintf(os.Stderr, "  elapsed  : %s\n", elapsed)
	fmt.Fprintf(os.Stderr, "\nTo restore: tar -x --zstd -f %s -C /\n", absOut)

	return nil
}

// buildTarArgs constructs the argument slice for the tar invocation.
//
// We use 'tar -c --zstd -v -f <out> -C / <relative-path> ...' so that
// paths inside the archive are absolute (var/lib/clustr/..., etc/clustr/...)
// and 'tar -x -C /' restores them correctly.
func buildTarArgs(tmpOut, dataDir string, noEtc bool) []string {
	args := []string{
		"-c",          // create
		"--zstd",      // compress with zstd
		"-v",          // verbose: print each file as it is archived
		"-f", tmpOut,  // output file
		"--warning=no-file-changed", // suppress "file changed as we read it" warnings for live DBs
		"-C", "/",     // change to / so paths in archive are absolute
	}

	// Strip leading '/' from dataDir to get the relative path from '/'.
	relData := strings.TrimPrefix(dataDir, "/")
	args = append(args, relData)

	if !noEtc {
		relEtc := strings.TrimPrefix(etcClusterDir, "/")
		// Only include /etc/clustr if it actually exists — skip silently if not.
		if _, err := os.Stat(etcClusterDir); err == nil {
			args = append(args, relEtc)
		}
	}

	return args
}

// probeZstdTar checks that the system's tar binary supports --zstd.
// Some minimal Docker-derived environments ship tar without zstd support.
func probeZstdTar() error {
	// 'tar --zstd --help' exits 0 and prints usage on GNU tar 1.34+.
	// We run it with stderr redirected to /dev/null to suppress any output.
	probe := exec.Command("tar", "--zstd", "--help") //nolint:gosec // fixed args
	probe.Stdout = nil
	probe.Stderr = nil // swallow help output
	if err := probe.Run(); err != nil {
		// Not a fatal exec error — we just lack --zstd support.
		return fmt.Errorf(
			"backup: tar does not support --zstd on this system\n" +
				"  Install 'zstd' package and ensure GNU tar >= 1.34 is in PATH\n" +
				"  (On Rocky Linux 9: dnf install tar zstd)",
		)
	}
	return nil
}
