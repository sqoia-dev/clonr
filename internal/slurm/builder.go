// builder.go — Slurm build-from-source pipeline.
// StartBuild kicks off an async build goroutine and returns the build ID immediately.
// executeBuild runs the full pipeline: download → deps → configure → make → package.
package slurm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/db"
)

// BuildConfig is the input for a Slurm build.
type BuildConfig struct {
	SlurmVersion   string   `json:"slurm_version"`   // e.g. "24.05.3"
	Arch           string   `json:"arch"`            // e.g. "x86_64"
	ConfigureFlags []string `json:"configure_flags"` // extra ./configure flags
}

// slurmBuildsDir is the permanent artifact storage directory.
const slurmBuildsDir = "/var/lib/clustr/slurm-builds"

// slurmWorkspaceBase is the root for per-build workspaces.
const slurmWorkspaceBase = "/var/lib/clustr/builds"

// StartBuild kicks off an async build. Creates the DB record (status: "building")
// and returns the build ID immediately. Compilation runs in a background goroutine.
func (m *Manager) StartBuild(ctx context.Context, cfg BuildConfig, initiatedBy string) (string, error) {
	if cfg.SlurmVersion == "" {
		return "", fmt.Errorf("slurm: build: slurm_version is required")
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if !isSafeVersionString(cfg.SlurmVersion) {
		return "", fmt.Errorf("slurm: build: invalid version string %q", cfg.SlurmVersion)
	}
	if !isSafeVersionString(cfg.Arch) {
		return "", fmt.Errorf("slurm: build: invalid arch string %q", cfg.Arch)
	}

	buildID := uuid.New().String()
	now := time.Now().Unix()

	row := db.SlurmBuildRow{
		ID:             buildID,
		Version:        cfg.SlurmVersion,
		Arch:           cfg.Arch,
		Status:         "building",
		ConfigureFlags: cfg.ConfigureFlags,
		InitiatedBy:    initiatedBy,
		LogKey:         buildID,
		StartedAt:      now,
	}
	if err := m.db.SlurmCreateBuild(ctx, row); err != nil {
		return "", fmt.Errorf("slurm: build: create DB record: %w", err)
	}

	log.Info().
		Str("build_id", buildID).
		Str("version", cfg.SlurmVersion).
		Str("arch", cfg.Arch).
		Msg("slurm: build started")

	go func() {
		bgCtx := context.Background()
		if err := m.executeBuild(bgCtx, buildID, cfg); err != nil {
			log.Error().Err(err).Str("build_id", buildID).Msg("slurm: build pipeline failed")
		}
	}()

	return buildID, nil
}

// executeBuild runs the complete Slurm build pipeline.
func (m *Manager) executeBuild(ctx context.Context, buildID string, cfg BuildConfig) error {
	m.logBuildLine(buildID, fmt.Sprintf("[build] starting Slurm %s for %s", cfg.SlurmVersion, cfg.Arch))

	// Step 1: Create workspace.
	workspace := filepath.Join(slurmWorkspaceBase, buildID)
	if err := os.MkdirAll(workspace, 0755); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("mkdir workspace: %w", err))
	}

	// Step 2: Download Slurm tarball.
	tarURL := fmt.Sprintf("https://download.schedmd.com/slurm/slurm-%s.tar.bz2", cfg.SlurmVersion)
	tarPath := filepath.Join(workspace, fmt.Sprintf("slurm-%s.tar.bz2", cfg.SlurmVersion))
	m.logBuildLine(buildID, fmt.Sprintf("[build] downloading %s", tarURL))
	if err := downloadFile(ctx, tarURL, tarPath); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("download slurm tarball: %w", err))
	}

	// Step 3: Extract tarball.
	srcDir := filepath.Join(workspace, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("mkdir srcdir: %w", err))
	}
	m.logBuildLine(buildID, "[build] extracting tarball")
	if err := runTar(ctx, "-xjf", tarPath, "-C", srcDir); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("extract tarball: %w", err))
	}
	topDir, err := findTopDir(srcDir)
	if err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("find top src dir: %w", err))
	}

	// Step 4: Build dependencies.
	m.logBuildLine(buildID, "[build] building dependencies")
	depPaths, err := m.buildDependencies(ctx, buildID, cfg.SlurmVersion, cfg.Arch, workspace)
	if err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("build dependencies: %w", err))
	}

	// Step 5: Configure Slurm.
	stagingDir := filepath.Join(workspace, "staging")
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("mkdir staging: %w", err))
	}
	configureArgs := buildSlurmConfigureArgs(cfg, depPaths)
	m.logBuildLine(buildID, fmt.Sprintf("[build] ./configure %s", strings.Join(configureArgs, " ")))
	if err := runCmd(ctx, topDir, "./configure", configureArgs...); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("configure: %w", err))
	}

	// Step 6: Compile.
	ncpu := fmt.Sprintf("%d", runtime.NumCPU())
	m.logBuildLine(buildID, fmt.Sprintf("[build] make -j%s", ncpu))
	if err := runCmd(ctx, topDir, "make", "-j"+ncpu); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("make: %w", err))
	}

	// Step 7: Install to staging.
	m.logBuildLine(buildID, fmt.Sprintf("[build] make install DESTDIR=%s", stagingDir))
	if err := runCmd(ctx, topDir, "make", "install", "DESTDIR="+stagingDir); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("make install: %w", err))
	}

	// Step 8: Package staging directory.
	artifactName := fmt.Sprintf("slurm-%s-%s.tar.gz", cfg.SlurmVersion, cfg.Arch)
	artifactTmp := filepath.Join(workspace, artifactName)
	m.logBuildLine(buildID, fmt.Sprintf("[build] packaging artifact %s", artifactName))
	if err := createTarGz(stagingDir, artifactTmp); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("package artifact: %w", err))
	}

	// Step 9: Compute checksum.
	checksum, err := checksumFile(artifactTmp)
	if err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("checksum artifact: %w", err))
	}
	info, err := os.Stat(artifactTmp)
	if err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("stat artifact: %w", err))
	}
	artifactSize := info.Size()

	// Step 10: Move to permanent storage.
	if err := os.MkdirAll(slurmBuildsDir, 0755); err != nil {
		return m.failBuild(ctx, buildID, fmt.Errorf("mkdir slurm-builds: %w", err))
	}
	artifactDst := filepath.Join(slurmBuildsDir, filepath.Base(artifactName))
	if err := os.Rename(artifactTmp, artifactDst); err != nil {
		if err2 := copyFileBytes(artifactTmp, artifactDst); err2 != nil {
			return m.failBuild(ctx, buildID, fmt.Errorf("move artifact: %w (copy: %v)", err, err2))
		}
		_ = os.Remove(artifactTmp)
	}

	// Step 11: Update DB record.
	completedAt := time.Now().Unix()
	if err := m.db.SlurmUpdateBuild(ctx, buildID, db.SlurmBuildUpdate{
		Status:            "completed",
		ArtifactPath:      artifactDst,
		ArtifactChecksum:  checksum,
		ArtifactSizeBytes: artifactSize,
		CompletedAt:       &completedAt,
	}); err != nil {
		log.Error().Err(err).Str("build_id", buildID).Msg("slurm: build: failed to update DB on completion")
		return err
	}

	m.logBuildLine(buildID, fmt.Sprintf("[build] DONE — Slurm %s built successfully", cfg.SlurmVersion))
	return nil
}

// failBuild records failure on the build row and returns the wrapped error.
func (m *Manager) failBuild(ctx context.Context, buildID string, cause error) error {
	m.logBuildLine(buildID, fmt.Sprintf("[build] FAILED: %s", cause.Error()))
	completedAt := time.Now().Unix()
	_ = m.db.SlurmUpdateBuild(ctx, buildID, db.SlurmBuildUpdate{
		Status:       "failed",
		ErrorMessage: cause.Error(),
		CompletedAt:  &completedAt,
	})
	return cause
}

// buildSlurmConfigureArgs constructs the ./configure args for Slurm.
//
// Dependency wiring (all with explicit install paths so configure finds the
// headers/libs we just built rather than system-installed versions):
//
//   - --with-munge=<path>  — link against our built libmunge, not system munge
//   - --with-hwloc=<path>  — topology-aware scheduling
//   - --with-ucx=<path>    — MPI high-speed network transport
//   - --with-pmix=<path>   — PMI-2/PMIx for MPI process management (needs hwloc)
//
// Extra caller-supplied flags from BuildConfig.ConfigureFlags are appended last
// so operators can override defaults (e.g. --with-json=<path> for libjwt).
func buildSlurmConfigureArgs(cfg BuildConfig, depPaths map[string]string) []string {
	args := []string{
		"--prefix=/usr/local",
		"--sysconfdir=/etc/slurm",
		"--enable-pam",
	}

	// Wire each dependency with an explicit path when available.
	// Fall back to --with-<dep> (no path) only when dep was skipped (not built).
	if p, ok := depPaths["munge"]; ok && p != "" {
		args = append(args, "--with-munge="+p)
	} else {
		args = append(args, "--with-munge")
	}
	if p, ok := depPaths["hwloc"]; ok && p != "" {
		args = append(args, "--with-hwloc="+p)
	}
	if p, ok := depPaths["ucx"]; ok && p != "" {
		args = append(args, "--with-ucx="+p)
	}
	if p, ok := depPaths["pmix"]; ok && p != "" {
		args = append(args, "--with-pmix="+p)
	}
	if p, ok := depPaths["libjwt"]; ok && p != "" {
		args = append(args, "--with-jwt="+p)
	} else {
		args = append(args, "--with-jwt")
	}

	return append(args, cfg.ConfigureFlags...)
}

// runTar runs the tar command with explicit flag args (no shell).
func runTar(ctx context.Context, flags ...string) error {
	cmd := exec.CommandContext(ctx, "tar", flags...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar %v: %w\noutput: %s", flags, err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// isSafeVersionString allows only alphanumeric, '.', '-', '_'.
func isSafeVersionString(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		ok := (c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			c == '.' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// copyFileBytes copies src to dst.
func copyFileBytes(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ─── Artifact signed URL ──────────────────────────────────────────────────────

// GenerateArtifactToken creates an HMAC-SHA256 token for artifact download.
// expiresAt is encoded as a Unix timestamp in the payload.
func (m *Manager) GenerateArtifactToken(buildID string, expiresAt time.Time) (string, error) {
	secret, err := m.secretEncryptionKey()
	if err != nil {
		return "", fmt.Errorf("artifact token: %w", err)
	}
	payload := buildID + ":" + fmt.Sprintf("%d", expiresAt.Unix())
	mac := computeHMACSHA256(secret, payload)
	return mac, nil
}

// ValidateArtifactToken validates the HMAC token for an artifact download request.
func (m *Manager) ValidateArtifactToken(buildID, token, expires string) bool {
	secret, err := m.secretEncryptionKey()
	if err != nil {
		return false
	}
	var expiresUnix int64
	if _, err := fmt.Sscanf(expires, "%d", &expiresUnix); err != nil {
		return false
	}
	if time.Now().Unix() > expiresUnix {
		return false
	}
	payload := buildID + ":" + expires
	expected := computeHMACSHA256(secret, payload)
	return hmac.Equal([]byte(token), []byte(expected))
}

// GenerateArtifactURL constructs a signed artifact download URL valid for 1 hour.
func (m *Manager) GenerateArtifactURL(buildID string) (string, error) {
	expires := time.Now().Add(time.Hour)
	token, err := m.GenerateArtifactToken(buildID, expires)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/v1/slurm/builds/%s/artifact?token=%s&expires=%d",
		buildID, token, expires.Unix()), nil
}

// computeHMACSHA256 returns hex-encoded HMAC-SHA256(key, payload).
func computeHMACSHA256(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
