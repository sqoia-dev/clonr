// deps.go — Dependency builder for the Slurm build pipeline.
// Builds munge, hwloc, UCX, PMIx, and libjwt from source.
// Dependencies are built in order and their install paths returned so
// the main Slurm build can reference them via --with-<dep>=<path>.
package slurm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/db"
)

// DepBuild describes one dependency to build.
type DepBuild struct {
	Name    string // "munge", "pmix", "hwloc", "ucx", "libjwt"
	Version string
}

// depBuildOrder is the order in which dependencies must be built.
var depBuildOrder = []string{"munge", "hwloc", "ucx", "pmix", "libjwt"}

// depDownloadURL returns the canonical source tarball URL for a dependency.
func depDownloadURL(name, version string) (string, error) {
	switch name {
	case "munge":
		return fmt.Sprintf("https://github.com/dun/munge/releases/download/munge-%s/munge-%s.tar.xz", version, version), nil
	case "pmix":
		return fmt.Sprintf("https://github.com/openpmix/openpmix/releases/download/v%s/pmix-%s.tar.gz", version, version), nil
	case "hwloc":
		parts := strings.SplitN(version, ".", 3)
		if len(parts) < 2 {
			return "", fmt.Errorf("deps: hwloc: cannot parse version %q", version)
		}
		majorMinor := parts[0] + "." + parts[1]
		return fmt.Sprintf("https://download.open-mpi.org/release/hwloc/v%s/hwloc-%s.tar.gz", majorMinor, version), nil
	case "ucx":
		return fmt.Sprintf("https://github.com/openucx/ucx/releases/download/v%s/ucx-%s.tar.gz", version, version), nil
	case "libjwt":
		// libjwt v2.x+ releases use .tar.xz; v1.x used .tar.gz (now removed from GitHub).
		return fmt.Sprintf("https://github.com/benmcollins/libjwt/releases/download/v%s/libjwt-%s.tar.xz", version, version), nil
	default:
		return "", fmt.Errorf("deps: unknown dependency %q", name)
	}
}

// depArchiveExt returns the file extension for the source tarball of a dependency.
// munge and libjwt (v2.x+) use .tar.xz; all others use .tar.gz.
func depArchiveExt(name string) string {
	switch name {
	case "munge", "libjwt":
		return "xz"
	default:
		return "gz"
	}
}

// buildDependencies builds all required Slurm dependencies for the given Slurm version.
// Returns a map of dep_name → install prefix path.
func (m *Manager) buildDependencies(ctx context.Context, buildID, slurmVersion, arch, workspace string) (map[string]string, error) {
	depRanges, err := m.db.SlurmResolveDepVersions(ctx, slurmVersion)
	if err != nil {
		return nil, fmt.Errorf("deps: resolve dep matrix for slurm %s: %w", slurmVersion, err)
	}

	installPaths := make(map[string]string, len(depBuildOrder))
	depsDir := filepath.Join(workspace, "deps")
	if err := os.MkdirAll(depsDir, 0755); err != nil {
		return nil, fmt.Errorf("deps: mkdir deps: %w", err)
	}

	for _, depName := range depBuildOrder {
		rng, ok := depRanges[depName]
		if !ok {
			m.logBuildLine(buildID, fmt.Sprintf("[deps] no matrix entry for %s — skipping", depName))
			continue
		}
		version := rng.DepVersionMin

		m.logBuildLine(buildID, fmt.Sprintf("[deps] building %s %s", depName, version))

		// Pass installPaths accumulated so far so that deps can reference earlier
		// build outputs via --with-<dep>=<path> configure flags.
		installPrefix, err := m.buildOneDep(ctx, buildID, depName, version, arch, depsDir, workspace, installPaths)
		if err != nil {
			return nil, fmt.Errorf("deps: build %s %s: %w", depName, version, err)
		}
		installPaths[depName] = installPrefix
		m.logBuildLine(buildID, fmt.Sprintf("[deps] %s %s installed at %s", depName, version, installPrefix))

		// Record the dependency in the DB.
		artifactPath := filepath.Join(depsDir, depName+"-"+version+"-"+arch+".tar.gz")
		checksum, _ := checksumFile(artifactPath)
		depRow := db.SlurmBuildDepRow{
			ID:               uuid.New().String(),
			BuildID:          buildID,
			DepName:          depName,
			DepVersion:       version,
			ArtifactPath:     artifactPath,
			ArtifactChecksum: checksum,
		}
		if err := m.db.SlurmInsertBuildDep(ctx, depRow); err != nil {
			log.Warn().Err(err).Str("dep", depName).Msg("deps: failed to record dep artifact in DB")
		}
	}

	return installPaths, nil
}

// depConfigureFlags returns extra ./configure arguments for a dependency, given
// the install paths of already-built dependencies.
//
// Build order is: munge → hwloc → ucx → pmix → libjwt (see depBuildOrder).
// Later deps reference earlier install paths via --with-<dep>=<path>.
//
//   - pmix: needs hwloc headers/libs → --with-hwloc=<hwloc-install>
//   - slurm (main binary, not a dep): wired in builder.go; listed here for
//     documentation only — slurm configure flags come from BuildConfig.ConfigureFlags
//     plus auto-wiring in builder.go.
//
// hwloc, ucx, munge, libjwt: no cross-dep configure flags needed beyond --prefix.
func depConfigureFlags(name string, installPaths map[string]string) []string {
	switch name {
	case "pmix":
		var flags []string
		if p, ok := installPaths["hwloc"]; ok && p != "" {
			flags = append(flags, "--with-hwloc="+p)
		}
		return flags
	default:
		return nil
	}
}

// buildOneDep builds a single dependency or reuses an existing install.
//
// installPaths holds the install prefixes of previously built dependencies.
// Each dep may reference earlier dep outputs via depConfigureFlags (e.g. PMIx
// needs --with-hwloc=<hwloc-install-path> to find hwloc headers/libs).
func (m *Manager) buildOneDep(ctx context.Context, buildID, name, version, arch, depsDir, workspace string, installPaths map[string]string) (string, error) {
	installPrefix := filepath.Join(depsDir, name+"-install")

	// Reuse if already installed (idempotent on retry).
	if _, err := os.Stat(installPrefix); err == nil {
		m.logBuildLine(buildID, fmt.Sprintf("[deps] %s %s: reusing existing install", name, version))
		return installPrefix, nil
	}

	srcURL, err := depDownloadURL(name, version)
	if err != nil {
		return "", err
	}
	m.logBuildLine(buildID, fmt.Sprintf("[deps] %s: downloading %s", name, srcURL))

	ext := depArchiveExt(name)
	tarPath := filepath.Join(workspace, name+"-"+version+".tar."+ext)
	if err := downloadFile(ctx, srcURL, tarPath); err != nil {
		return "", fmt.Errorf("download %s: %w", srcURL, err)
	}

	srcDir := filepath.Join(workspace, name+"-"+version+"-src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir srcdir: %w", err)
	}
	m.logBuildLine(buildID, fmt.Sprintf("[deps] %s: extracting source", name))

	// Use system tar for both gz and xz — it handles both transparently.
	if err := tarExtract(ctx, tarPath, srcDir); err != nil {
		return "", fmt.Errorf("extract %s: %w", name, err)
	}

	topDir, err := findTopDir(srcDir)
	if err != nil {
		return "", fmt.Errorf("find top dir in %s: %w", srcDir, err)
	}

	if err := os.MkdirAll(installPrefix, 0755); err != nil {
		return "", fmt.Errorf("mkdir install prefix: %w", err)
	}

	// Build configure args: start with --prefix, add per-dep cross-dep flags.
	configureArgs := []string{"--prefix=" + installPrefix}
	configureArgs = append(configureArgs, depConfigureFlags(name, installPaths)...)
	m.logBuildLine(buildID, fmt.Sprintf("[deps] %s: ./configure %s", name, strings.Join(configureArgs, " ")))
	if err := runCmd(ctx, topDir, "./configure", configureArgs...); err != nil {
		return "", fmt.Errorf("configure %s: %w", name, err)
	}

	ncpu := fmt.Sprintf("%d", runtime.NumCPU())
	m.logBuildLine(buildID, fmt.Sprintf("[deps] %s: make -j%s", name, ncpu))
	if err := runCmd(ctx, topDir, "make", "-j"+ncpu); err != nil {
		return "", fmt.Errorf("make %s: %w", name, err)
	}

	m.logBuildLine(buildID, fmt.Sprintf("[deps] %s: make install", name))
	if err := runCmd(ctx, topDir, "make", "install"); err != nil {
		return "", fmt.Errorf("make install %s: %w", name, err)
	}

	// Package the install as an artifact (non-fatal if it fails).
	artifactPath := filepath.Join(depsDir, name+"-"+version+"-"+arch+".tar.gz")
	if err := createTarGz(installPrefix, artifactPath); err != nil {
		log.Warn().Err(err).Str("dep", name).Msg("deps: failed to package dep artifact")
	}

	return installPrefix, nil
}

// tarExtract extracts any tarball (gz, xz, bz2) using the system tar command.
// Setpgid: true isolates the tar subprocess from the clustr-serverd process group
// so a server restart during dep extraction does not kill it mid-stream.
func tarExtract(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "tar", "-xf", src, "-C", dst)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar -xf: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// ─── Munge key management ─────────────────────────────────────────────────────

// GenerateMungeKey creates a new random 1024-byte munge key and stores it encrypted.
func (m *Manager) GenerateMungeKey(ctx context.Context) error {
	key := make([]byte, 1024)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("slurm: generate munge key: %w", err)
	}
	encryptedHex, err := m.encryptSecret(key)
	if err != nil {
		return fmt.Errorf("slurm: encrypt munge key: %w", err)
	}
	if err := m.db.SlurmUpsertSecret(ctx, db.SlurmSecretRow{
		KeyType:        "munge.key",
		EncryptedValue: encryptedHex,
		RotatedAt:      time.Now().Unix(),
		RotatedBy:      "system",
	}); err != nil {
		return fmt.Errorf("slurm: store munge key: %w", err)
	}
	log.Info().Msg("slurm: munge key generated and stored")
	return nil
}

// GetMungeKey retrieves and decrypts the munge key.
func (m *Manager) GetMungeKey(ctx context.Context) ([]byte, error) {
	row, err := m.db.SlurmGetSecret(ctx, "munge.key")
	if err != nil {
		return nil, fmt.Errorf("slurm: get munge key: %w", err)
	}
	return m.decryptSecret(row.EncryptedValue)
}

// RotateMungeKey generates a new munge key (overwriting the old one).
// Callers should push the new key to all nodes after calling this.
func (m *Manager) RotateMungeKey(ctx context.Context) error {
	return m.GenerateMungeKey(ctx)
}

// ─── Encryption (AES-256-GCM) ─────────────────────────────────────────────────

// encryptSecret encrypts plaintext using AES-256-GCM.
// Returns hex(nonce || ciphertext).
func (m *Manager) encryptSecret(plaintext []byte) (string, error) {
	key, err := m.secretEncryptionKey()
	if err != nil {
		return "", err
	}
	return aesGCMEncrypt(key, plaintext)
}

// decryptSecret decrypts a hex-encoded ciphertext produced by encryptSecret.
func (m *Manager) decryptSecret(ciphertextHex string) ([]byte, error) {
	key, err := m.secretEncryptionKey()
	if err != nil {
		return nil, err
	}
	return aesGCMDecrypt(key, ciphertextHex)
}

// defaultSecretKey is the insecure fallback value that was previously used when
// CLUSTR_SECRET_KEY was unset. It is checked at enable-time so deployments that
// accidentally ship without an explicit key are refused rather than silently
// using a publicly-known encryption key.
const defaultSecretKey = "clustr-slurm-secrets-v1" //#nosec G101 -- intentional canary; validateSecretKey() hard-fails if CLUSTR_SECRET_KEY equals this well-known default.

// validateSecretKey returns an error if CLUSTR_SECRET_KEY is unset or is the
// well-known default. Call this before enabling the Slurm module.
func validateSecretKey() error {
	v := os.Getenv("CLUSTR_SECRET_KEY")
	if v == "" {
		return fmt.Errorf(
			"slurm: CLUSTR_SECRET_KEY must be set to a strong random value before enabling Slurm. " +
				"Generate one with: openssl rand -hex 32",
		)
	}
	if v == defaultSecretKey {
		return fmt.Errorf(
			"slurm: CLUSTR_SECRET_KEY is set to the insecure default value %q. " +
				"Set it to a strong random value before enabling Slurm. " +
				"Generate one with: openssl rand -hex 32",
			defaultSecretKey,
		)
	}
	return nil
}

// secretEncryptionKey derives a 32-byte AES key from CLUSTR_SECRET_KEY.
// Returns an error if the key is unset or is the insecure default — callers
// should have already called validateSecretKey() at enable-time, but this
// provides a hard stop for any path that bypasses that check.
func (m *Manager) secretEncryptionKey() ([]byte, error) {
	envKey := os.Getenv("CLUSTR_SECRET_KEY")
	if envKey == "" || envKey == defaultSecretKey {
		return nil, fmt.Errorf(
			"slurm: CLUSTR_SECRET_KEY is not set or is the insecure default. " +
				"Set CLUSTR_SECRET_KEY to a strong random value (openssl rand -hex 32) " +
				"and restart clustr-serverd",
		)
	}
	raw, err := hex.DecodeString(envKey)
	if err == nil && len(raw) == 32 {
		return raw, nil
	}
	// Arbitrary string (not valid hex-32): hash to 32 bytes.
	h := sha256.Sum256([]byte(envKey))
	return h[:], nil
}

// aesGCMEncrypt encrypts plaintext with AES-256-GCM.
// Returns hex(nonce || ciphertext || tag).
func aesGCMEncrypt(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return hex.EncodeToString(ciphertext), nil
}

// aesGCMDecrypt decrypts a hex-encoded AES-256-GCM ciphertext.
func aesGCMDecrypt(key []byte, ciphertextHex string) ([]byte, error) {
	data, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:ns], data[ns:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// ─── Matrix seeding ───────────────────────────────────────────────────────────

// seedDepMatrix loads the bundled deps_matrix.json and seeds the DB.
// INSERT OR IGNORE makes it idempotent.
func (m *Manager) seedDepMatrix(ctx context.Context) error {
	type matrixEntry struct {
		SlurmVersionMin string `json:"slurm_version_min"`
		SlurmVersionMax string `json:"slurm_version_max"`
		DepName         string `json:"dep_name"`
		DepVersionMin   string `json:"dep_version_min"`
		DepVersionMax   string `json:"dep_version_max"`
	}
	var entries []matrixEntry
	if err := json.Unmarshal(depsMatrixJSON, &entries); err != nil {
		return fmt.Errorf("slurm: parse deps_matrix.json: %w", err)
	}

	rows := make([]db.SlurmDepMatrixRow, 0, len(entries))
	now := time.Now().Unix()
	for _, e := range entries {
		rows = append(rows, db.SlurmDepMatrixRow{
			ID:              uuid.New().String(),
			SlurmVersionMin: e.SlurmVersionMin,
			SlurmVersionMax: e.SlurmVersionMax,
			DepName:         e.DepName,
			DepVersionMin:   e.DepVersionMin,
			DepVersionMax:   e.DepVersionMax,
			Source:          "bundled",
			CreatedAt:       now,
		})
	}
	if err := m.db.SlurmSeedDepMatrix(ctx, rows); err != nil {
		return fmt.Errorf("slurm: seed dep matrix: %w", err)
	}
	log.Info().Int("entries", len(rows)).Msg("slurm: dep matrix seeded")
	return nil
}

// ─── Shell command helper ─────────────────────────────────────────────────────

// runCmd runs an external command in dir. Args are passed directly to exec.Command
// (no shell) preventing injection. Combined output is logged at debug level.
//
// Build isolation: cmd.SysProcAttr sets Setpgid=true so the subprocess runs in
// its own process group. This is critical for long-running build commands (make,
// configure): without it, when systemd sends SIGTERM to clustr-serverd during a
// service restart, the signal is delivered to the entire process group including
// any in-flight make subprocesses — killing active builds mid-compilation.
// With Setpgid=true, the make tree runs in a new process group and is not
// affected by signals directed at the clustr-serverd process group.
//
// The build goroutine uses context.Background() (no deadline) so ctx cancellation
// is not the kill path — process group isolation is the correct fix.
func runCmd(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Debug().Str("cmd", name).Str("dir", dir).Msgf("output:\n%s", strings.TrimRight(string(out), "\n"))
	}
	if err != nil {
		return fmt.Errorf("%s %v: %w\noutput: %s", name, args, err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// ─── File / archive helpers ───────────────────────────────────────────────────

// downloadFile downloads srcURL to dst atomically (writes dst+".tmp" then renames).
func downloadFile(ctx context.Context, srcURL, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http get %s: %w", srcURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http get %s: status %d", srcURL, resp.StatusCode)
	}

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write download: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	return os.Rename(tmp, dst)
}

// findTopDir returns the single top-level directory within dir after extraction.
func findTopDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("readdir %s: %w", dir, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("extracted tarball is empty in %s", dir)
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return dir, nil
}

// createTarGz creates a gzip-compressed tar archive of src at dst.
func createTarGz(src, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// filepath.Walk uses os.Lstat so symlinks appear as-is (not followed).
	// We must resolve the symlink target ourselves for tar.FileInfoHeader,
	// which takes the link target as its second argument. Passing "" produces
	// a symlink entry with an empty target, which tar refuses to extract.
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		// Resolve symlink target before building the tar header.
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", path, err)
			}
		}

		hdr, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		hdr.Name = relPath
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// Only copy data for regular files; symlinks and directories have no body.
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
}

// checksumFile returns the hex-encoded SHA-256 of the file at path.
func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// logBuildLine emits a log line tagged with the build ID and broadcasts to
// any SSE subscribers watching GET /slurm/builds/{build_id}/log-stream.
func (m *Manager) logBuildLine(buildID, line string) {
	log.Info().Str("build_id", buildID).Msg(line)
	m.buildLogsMu.Lock()
	s, ok := m.buildLogs[buildID]
	if !ok {
		s = &buildLogState{subs: map[chan string]struct{}{}}
		m.buildLogs[buildID] = s
	}
	m.buildLogsMu.Unlock()
	s.publish(line)
}

// finishBuildLog marks the build log done and closes subscriber channels.
// Called by executeBuild (via StartBuild goroutine) on completion or failure.
func (m *Manager) finishBuildLog(buildID string) {
	m.buildLogsMu.RLock()
	s, ok := m.buildLogs[buildID]
	m.buildLogsMu.RUnlock()
	if ok {
		s.close()
	}
}

// SubscribeBuildLog returns a channel that delivers all past log lines plus
// future lines as they arrive. Returns a cancel func to stop the subscription.
// Used by the SSE handler in routes.go.
func (m *Manager) SubscribeBuildLog(buildID string) (<-chan string, func()) {
	m.buildLogsMu.Lock()
	s, ok := m.buildLogs[buildID]
	if !ok {
		// Build hasn't emitted any lines yet (or doesn't exist): create an empty state.
		s = &buildLogState{subs: map[chan string]struct{}{}}
		m.buildLogs[buildID] = s
	}
	m.buildLogsMu.Unlock()
	return s.subscribe()
}
