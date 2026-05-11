package main

// backup.go — Sprint 41 Day 4
//
// Two new privhelper verbs for plugin pre-render snapshotting:
//
//   backup-write --plugin <name> --node-id <uuid> --paths <comma-sep> --out <tarball-path>
//     Reads the listed absolute paths, packs them into a gzipped tarball
//     preserving directory structure, writes to --out.
//     Path validation: no "..", no null bytes, must start with an allowed prefix.
//     Audits to audit_log on success.
//
//   backup-restore --tarball <path> --node-id <uuid> --plugin <name>
//     Extracts a tarball produced by backup-write back to the filesystem.
//     Tarball path must be under the backup base dir.
//     Audits to audit_log on success.
//
// Design: docs/design/sprint-41-auth-safety.md §5.

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// backupBaseDir is the only directory that backup-write may write tarballs to,
// and backup-restore may read from.
const backupBaseDir = "/var/lib/clustr/backups/"

// backupPathAllowlistPrefixes lists the filesystem prefixes that backup-write
// is allowed to read. Paths outside these prefixes are rejected.
// This prevents a compromised caller from exfiltrating arbitrary root-owned files.
var backupPathAllowlistPrefixes = []string{
	"/etc/",
	"/var/lib/sssd/",
	"/var/lib/sss/",
}

// isSafeBackupPath returns true when path is safe to snapshot:
//   - absolute path
//   - no ".." components in the raw path (before cleaning)
//   - no null bytes
//   - starts with one of the allowlist prefixes (checked against cleaned path,
//     both with and without a trailing slash so directories match)
func isSafeBackupPath(path string) bool {
	if path == "" {
		return false
	}
	if strings.ContainsRune(path, 0) {
		return false
	}
	if !filepath.IsAbs(path) {
		return false
	}
	// Reject ".." before cleaning — this prevents traversal even if Clean would
	// later resolve it. filepath.Clean("/etc/../etc/passwd") → "/etc/passwd"
	// which would pass the allowlist, so we must check the raw path first.
	for _, component := range strings.Split(path, "/") {
		if component == ".." {
			return false
		}
	}
	clean := filepath.Clean(path)
	for _, prefix := range backupPathAllowlistPrefixes {
		// Match both with and without the trailing slash (so "/var/lib/sssd/"
		// and "/var/lib/sssd" both resolve correctly).
		p := strings.TrimSuffix(prefix, "/")
		if strings.HasPrefix(clean, p+"/") || clean == p {
			return true
		}
	}
	return false
}

// isSafePluginName returns true when name contains only safe identifier chars
// (letters, digits, underscores, hyphens — no slashes, no dots, no spaces).
func isSafePluginName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// isSafeNodeID returns true when id looks like a UUID (hex + hyphens).
func isSafeNodeID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		ok := (c >= 'a' && c <= 'f') ||
			(c >= 'A' && c <= 'F') ||
			(c >= '0' && c <= '9') ||
			c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// verbBackupWrite packs the listed paths into a gzipped tarball at --out.
//
// Argv: backup-write --plugin <name> --node-id <uuid> --paths <csv> --out <path>
func verbBackupWrite(callerUID int, args []string) int {
	// Parse flags manually to stay consistent with the rest of the privhelper
	// (no cobra/flag in this binary).
	var pluginName, nodeID, pathsCSV, outPath string
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--plugin":
			pluginName = args[i+1]
			i++
		case "--node-id":
			nodeID = args[i+1]
			i++
		case "--paths":
			pathsCSV = args[i+1]
			i++
		case "--out":
			outPath = args[i+1]
			i++
		}
	}

	if pluginName == "" || nodeID == "" || pathsCSV == "" || outPath == "" {
		msg := "backup-write: --plugin, --node-id, --paths, and --out are required"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 1, msg)
		return 1
	}

	if !isSafePluginName(pluginName) {
		msg := fmt.Sprintf("backup-write: plugin name %q contains disallowed characters", pluginName)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 1, msg)
		return 1
	}
	if !isSafeNodeID(nodeID) {
		msg := fmt.Sprintf("backup-write: node-id %q is not a valid UUID", nodeID)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 1, msg)
		return 1
	}

	// Validate --out: must be under backupBaseDir, must end in .tar.gz.
	cleanOut := filepath.Clean(outPath)
	if !strings.HasPrefix(cleanOut, backupBaseDir) {
		msg := fmt.Sprintf("backup-write: --out must be under %s", backupBaseDir)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 1, msg)
		return 1
	}
	if !strings.HasSuffix(cleanOut, ".tar.gz") {
		msg := "backup-write: --out must end in .tar.gz"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 1, msg)
		return 1
	}

	// Parse and validate paths.
	rawPaths := strings.Split(pathsCSV, ",")
	var safePaths []string
	for _, p := range rawPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !isSafeBackupPath(p) {
			msg := fmt.Sprintf("backup-write: path %q rejected: must be absolute, no .., and under an allowed prefix (/etc/, /var/lib/sssd/, /var/lib/sss/)", p)
			fmt.Fprintln(os.Stderr, msg)
			writeAudit(callerUID, "backup-write", args, 1, msg)
			return 1
		}
		safePaths = append(safePaths, filepath.Clean(p))
	}

	if len(safePaths) == 0 {
		msg := "backup-write: no valid paths provided"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 1, msg)
		return 1
	}

	// Ensure the output directory exists.
	if err := os.MkdirAll(filepath.Dir(cleanOut), 0700); err != nil {
		msg := fmt.Sprintf("backup-write: mkdir output dir: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 2, msg)
		return 2
	}

	// Create the tarball.
	if err := createTarball(cleanOut, safePaths); err != nil {
		msg := fmt.Sprintf("backup-write: create tarball: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-write", args, 2, msg)
		return 2
	}

	writeAudit(callerUID, "backup-write", args, 0, "")
	return 0
}

// verbBackupRestore extracts a tarball produced by backup-write back to the filesystem.
//
// Argv: backup-restore --tarball <path> --node-id <uuid> --plugin <name>
func verbBackupRestore(callerUID int, args []string) int {
	var tarballPath, nodeID, pluginName string
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--tarball":
			tarballPath = args[i+1]
			i++
		case "--node-id":
			nodeID = args[i+1]
			i++
		case "--plugin":
			pluginName = args[i+1]
			i++
		}
	}

	if tarballPath == "" || nodeID == "" || pluginName == "" {
		msg := "backup-restore: --tarball, --node-id, and --plugin are required"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 1, msg)
		return 1
	}

	if !isSafePluginName(pluginName) {
		msg := fmt.Sprintf("backup-restore: plugin name %q contains disallowed characters", pluginName)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 1, msg)
		return 1
	}
	if !isSafeNodeID(nodeID) {
		msg := fmt.Sprintf("backup-restore: node-id %q is not a valid UUID", nodeID)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 1, msg)
		return 1
	}

	// Tarball must be under backupBaseDir and end in .tar.gz.
	cleanTarball := filepath.Clean(tarballPath)
	if !strings.HasPrefix(cleanTarball, backupBaseDir) {
		msg := fmt.Sprintf("backup-restore: --tarball must be under %s", backupBaseDir)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 1, msg)
		return 1
	}
	if !strings.HasSuffix(cleanTarball, ".tar.gz") {
		msg := "backup-restore: --tarball must end in .tar.gz"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 1, msg)
		return 1
	}

	// Verify the file is a regular file (not a symlink or device).
	info, err := os.Lstat(cleanTarball)
	if err != nil {
		msg := fmt.Sprintf("backup-restore: stat tarball: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 2, msg)
		return 2
	}
	if !info.Mode().IsRegular() {
		msg := "backup-restore: tarball path is not a regular file"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 1, msg)
		return 1
	}

	// Extract to filesystem root "/". Path entries in the tarball were written
	// path-preserving by createTarball (e.g. "etc/sssd/sssd.conf" relative to "/").
	if err := extractTarball(cleanTarball, "/"); err != nil {
		msg := fmt.Sprintf("backup-restore: extract: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "backup-restore", args, 2, msg)
		return 2
	}

	writeAudit(callerUID, "backup-restore", args, 0, "")
	return 0
}

// ─── tarball helpers ──────────────────────────────────────────────────────────

// createTarball creates a gzipped tarball at outPath, adding each path in
// paths. Directories are added recursively. Paths are stored relative to "/"
// (path-preserving: "/etc/sssd/sssd.conf" → "etc/sssd/sssd.conf").
func createTarball(outPath string, paths []string) error {
	// Write to a temp file in the same directory, then rename atomically.
	dir := filepath.Dir(outPath)
	tmp, err := os.CreateTemp(dir, ".backup-write-*.tar.gz.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	gw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gw)

	for _, p := range paths {
		if err := addPathToTar(tw, p); err != nil {
			// If the path doesn't exist, skip it (the file may not be present on all nodes).
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "backup-write: skipping missing path %s\n", p)
				continue
			}
			return fmt.Errorf("add %s to tar: %w", p, err)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return fmt.Errorf("rename to final path: %w", err)
	}
	success = true
	return nil
}

// addPathToTar adds a path (file or directory tree) to a tar.Writer.
// Paths are stored relative to "/" (strip the leading slash).
func addPathToTar(tw *tar.Writer, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return filepath.Walk(path, func(fp string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				// Missing file inside a directory: skip it.
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if fi.IsDir() {
				return nil // directories are implied by file paths in the tarball
			}
			return addFileToTar(tw, fp, fi)
		})
	}

	return addFileToTar(tw, path, info)
}

// addFileToTar adds a single regular file to a tar.Writer.
// The name stored in the tar header is the path relative to "/" (no leading slash).
// The caller (verbBackupWrite) is responsible for validating all paths against
// the allowlist before invoking createTarball.
func addFileToTar(tw *tar.Writer, path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return nil // skip symlinks, devices — only regular files
	}

	clean := filepath.Clean(path)

	f, err := os.Open(clean) //#nosec G304 -- caller validated paths via isSafeBackupPath
	if err != nil {
		if os.IsNotExist(err) {
			return nil // file vanished between Walk and Open; skip
		}
		return fmt.Errorf("open %s: %w", clean, err)
	}
	defer f.Close()

	hdr := &tar.Header{
		Name:    strings.TrimPrefix(clean, "/"), // path-preserving, relative to "/"
		Size:    info.Size(),
		Mode:    int64(info.Mode().Perm()),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write header %s: %w", clean, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write content %s: %w", clean, err)
	}
	return nil
}

// extractTarball extracts a gzipped tarball to rootDir. Each entry's Name is
// joined with rootDir to produce the destination path. Only regular files are
// extracted. Extraction validates that each entry path is within the backup
// allowlist (isSafeBackupPath) to prevent tarbomb attacks.
//
// rootDir should be "/" for production restores.
func extractTarball(tarballPath, rootDir string) error {
	f, err := os.Open(tarballPath) //#nosec G304 -- tarballPath validated under backupBaseDir by caller
	if err != nil {
		return fmt.Errorf("open tarball: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		// Skip non-regular files (directories, symlinks, etc.).
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}

		// Reconstruct the absolute destination path.
		// hdr.Name is stored without a leading slash (e.g. "etc/sssd/sssd.conf").
		destPath := filepath.Join(rootDir, hdr.Name)
		cleanDest := filepath.Clean(destPath)

		// If rootDir is "/" we can validate against the allowlist directly.
		if rootDir == "/" && !isSafeBackupPath(cleanDest) {
			return fmt.Errorf("restore: entry %q maps to disallowed destination %q; aborting", hdr.Name, cleanDest)
		}

		// Ensure parent directory exists.
		if err := os.MkdirAll(filepath.Dir(cleanDest), 0o755); err != nil {
			return fmt.Errorf("restore: mkdir parent for %s: %w", cleanDest, err)
		}

		// Write to a temp file then rename atomically.
		tmpPath := cleanDest + ".restore.tmp"
		out, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode)) //#nosec G304 -- cleanDest validated via allowlist
		if err != nil {
			return fmt.Errorf("restore: create %s: %w", cleanDest, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("restore: write %s: %w", cleanDest, err)
		}
		if err := out.Close(); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("restore: close %s: %w", cleanDest, err)
		}
		if err := os.Rename(tmpPath, cleanDest); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("restore: rename to %s: %w", cleanDest, err)
		}
	}
	return nil
}
