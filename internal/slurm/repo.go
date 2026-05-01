// repo.go — clustr-internal-repo management.
//
// This file handles:
//   - Per-cluster GPG key generation (RSA 4096, stored encrypted in slurm_secrets)
//   - nfpm-based RPM packaging of slurm build artifacts
//   - Repo push (via clustr-privhelper repo-push) and metadata refresh (createrepo_c)
//   - HTTP handler serving /repo/clustr-internal-repo/... and the GPG public key
//
// The nfpm template produces one RPM per Slurm sub-package matching the upstream
// Fedora spec split: slurm, slurmd, slurmctld, slurmdbd, slurm-libs, slurm-pam_slurm.
// This gives nodes clean dep-resolution: installing "slurmctld" pulls slurm-libs
// automatically just like upstream packages.
package slurm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/privhelper"
)

// repoBaseDir is the server-side root for all repo files.
const repoBaseDir = "/var/lib/clustr/repo/clustr-internal-repo"

// ─── GPG key generation ───────────────────────────────────────────────────────

// InitRepoGPGKey generates a per-cluster RSA 4096 GPG key and stores it.
// The private key is encrypted in slurm_secrets under key_type "repo.gpg.private".
// The public key (ASCII-armored) is stored plaintext on slurm_module_config.
// Idempotent — if a key already exists this is a no-op.
func (m *Manager) InitRepoGPGKey(ctx context.Context) error {
	// Idempotent: if a key already exists, skip.
	if _, err := m.db.SlurmGetRepoGPGConfig(ctx); err == nil {
		log.Debug().Msg("slurm: repo GPG key already exists — skipping init")
		return nil
	}

	cfg, err := m.db.SlurmGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("slurm: repo GPG key: get config: %w", err)
	}

	clusterName := cfg.ClusterName
	if clusterName == "" {
		clusterName = "clustr"
	}

	// Build a unique GPG identity for this cluster.
	identity := fmt.Sprintf("clustr-internal-repo/%s <repo@clustr.internal>", clusterName)

	log.Info().Str("identity", identity).Msg("slurm: generating per-cluster repo GPG key")

	// Generate GPG key in a temporary GNUPGHOME so we don't pollute the system keyring.
	tmpHome, err := os.MkdirTemp("", "clustr-gpg-*")
	if err != nil {
		return fmt.Errorf("slurm: repo GPG key: create temp gnupghome: %w", err)
	}
	defer os.RemoveAll(tmpHome)

	// Write a batch file for unattended key generation.
	batchContent := fmt.Sprintf(`%%no-protection
Key-Type: RSA
Key-Length: 4096
Subkey-Type: RSA
Subkey-Length: 4096
Name-Real: clustr-internal-repo/%s
Name-Comment: per-cluster repo signing key
Name-Email: repo@clustr.internal
Expire-Date: 2y
%%commit
`, clusterName)

	batchFile := filepath.Join(tmpHome, "keygen.batch")
	if err := os.WriteFile(batchFile, []byte(batchContent), 0600); err != nil {
		return fmt.Errorf("slurm: repo GPG key: write batch: %w", err)
	}

	// Generate the key.
	genCmd := exec.CommandContext(ctx, "gpg", "--batch", "--gen-key", batchFile)
	genCmd.Env = append(os.Environ(), "GNUPGHOME="+tmpHome)
	if out, err := genCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("slurm: repo GPG key: gpg --gen-key: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}

	// Export public key (ASCII-armored).
	pubCmd := exec.CommandContext(ctx, "gpg", "--armor", "--export", "repo@clustr.internal")
	pubCmd.Env = append(os.Environ(), "GNUPGHOME="+tmpHome)
	pubOut, err := pubCmd.Output()
	if err != nil {
		return fmt.Errorf("slurm: repo GPG key: export public key: %w", err)
	}

	// Export private key (ASCII-armored) for encrypted storage.
	privCmd := exec.CommandContext(ctx, "gpg", "--armor", "--export-secret-keys", "repo@clustr.internal")
	privCmd.Env = append(os.Environ(), "GNUPGHOME="+tmpHome)
	privOut, err := privCmd.Output()
	if err != nil {
		return fmt.Errorf("slurm: repo GPG key: export private key: %w", err)
	}

	// Extract key ID from the fingerprint list.
	keyID, err := extractGPGKeyID(ctx, tmpHome)
	if err != nil {
		log.Warn().Err(err).Msg("slurm: repo GPG key: could not extract key ID (non-fatal)")
		keyID = "unknown"
	}

	// Store the encrypted private key.
	encryptedHex, err := m.encryptSecret(privOut)
	if err != nil {
		return fmt.Errorf("slurm: repo GPG key: encrypt private key: %w", err)
	}
	if err := m.db.SlurmUpsertSecret(ctx, db.SlurmSecretRow{
		KeyType:        "repo.gpg.private",
		EncryptedValue: encryptedHex,
		RotatedAt:      time.Now().Unix(),
		RotatedBy:      "system",
	}); err != nil {
		return fmt.Errorf("slurm: repo GPG key: store private key: %w", err)
	}

	// Store the public key (plaintext) on the module config row.
	if err := m.db.SlurmSetRepoGPGConfig(ctx, db.SlurmRepoGPGConfig{
		PublicKeyArmored: string(pubOut),
		KeyID:            keyID,
	}); err != nil {
		return fmt.Errorf("slurm: repo GPG key: store public key config: %w", err)
	}

	log.Info().Str("key_id", keyID).Msg("slurm: per-cluster repo GPG key generated")
	return nil
}

// extractGPGKeyID runs gpg --list-keys in the temp home and extracts the short key ID.
func extractGPGKeyID(ctx context.Context, gnupgHome string) (string, error) {
	cmd := exec.CommandContext(ctx, "gpg", "--list-keys", "--with-colons", "repo@clustr.internal")
	cmd.Env = append(os.Environ(), "GNUPGHOME="+gnupgHome)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Parse colon-delimited output: look for "pub" lines with fingerprint field.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "pub:") {
			parts := strings.Split(line, ":")
			if len(parts) >= 5 && parts[4] != "" {
				// parts[4] is the long key ID in some gpg versions.
				return parts[4], nil
			}
		}
	}
	return "unknown", nil
}

// getRepoGPGPrivateKey retrieves and decrypts the repo GPG private key.
func (m *Manager) getRepoGPGPrivateKey(ctx context.Context) ([]byte, error) {
	row, err := m.db.SlurmGetSecret(ctx, "repo.gpg.private")
	if err != nil {
		return nil, fmt.Errorf("slurm: get repo GPG private key: %w", err)
	}
	return m.decryptSecret(row.EncryptedValue)
}

// ─── nfpm RPM packaging ───────────────────────────────────────────────────────

// nfpmTemplate is the nfpm config template for all Slurm sub-packages.
// It is instantiated once per sub-package with SubPackageData.
const nfpmTemplate = `name: "{{.Name}}"
arch: "{{.Arch}}"
platform: "linux"
version: "{{.Version}}"
release: "clustr1"
epoch: "0"
description: "{{.Description}}"
maintainer: "clustr-server <release@sqoia.dev>"
license: "GPL-2.0"
rpm:
  group: "System Environment/Base"
  summary: "{{.Summary}}"
  compression: "gzip"
  scripts:
    preinstall: ""
    postinstall: ""
contents:
{{- range .Contents}}
  - src: "{{.Src}}"
    dst: "{{.Dst}}"
    type: "{{.Type}}"
    file_info:
      mode: {{.Mode}}
{{- end}}
`

// subPkgDef defines what goes into each Slurm sub-package.
type subPkgDef struct {
	Name        string
	Summary     string
	Description string
	// GlobPatterns are relative to the staging dir (e.g. "usr/sbin/slurmd").
	GlobPatterns []string
}

// slurmSubPackages defines the Slurm sub-package split, mirroring upstream Fedora.
// Files are matched by glob against the staging directory produced by "make install".
var slurmSubPackages = []subPkgDef{
	{
		Name:         "slurm-libs",
		Summary:      "Slurm shared libraries",
		Description:  "Shared libraries for Slurm workload manager (clustr build).",
		GlobPatterns: []string{"usr/local/lib/libslurm*.so*", "usr/local/lib/slurm/*.so"},
	},
	{
		Name:         "slurm",
		Summary:      "Slurm base package",
		Description:  "Slurm workload manager base binaries (clustr build).",
		GlobPatterns: []string{"usr/local/bin/*", "usr/local/sbin/sinfo", "usr/local/sbin/squeue", "usr/local/sbin/scontrol", "usr/local/sbin/sacctmgr", "usr/local/sbin/sacct", "usr/local/sbin/sbatch", "usr/local/sbin/srun", "usr/local/sbin/salloc", "usr/local/sbin/scancel"},
	},
	{
		Name:         "slurmd",
		Summary:      "Slurm compute node daemon",
		Description:  "Slurmd compute node daemon (clustr build).",
		GlobPatterns: []string{"usr/local/sbin/slurmd"},
	},
	{
		Name:         "slurmctld",
		Summary:      "Slurm controller daemon",
		Description:  "Slurmctld controller daemon (clustr build).",
		GlobPatterns: []string{"usr/local/sbin/slurmctld"},
	},
	{
		Name:         "slurmdbd",
		Summary:      "Slurm database daemon",
		Description:  "Slurmdbd accounting daemon (clustr build).",
		GlobPatterns: []string{"usr/local/sbin/slurmdbd"},
	},
	{
		Name:         "slurm-pam_slurm",
		Summary:      "Slurm PAM module",
		Description:  "PAM module for Slurm job access control (clustr build).",
		GlobPatterns: []string{"lib/security/pam_slurm*.so", "usr/local/lib/security/pam_slurm*.so"},
	},
}

// nfpmContentEntry is one entry in the nfpm contents list.
type nfpmContentEntry struct {
	Src  string
	Dst  string
	Type string
	Mode string
}

// nfpmSubPackageData is the data passed to nfpmTemplate for one sub-package.
type nfpmSubPackageData struct {
	Name        string
	Arch        string
	Version     string
	Summary     string
	Description string
	Contents    []nfpmContentEntry
}

// buildSlurmRPMs uses nfpm to build signed RPMs from the staging dir and pushes
// them into the repo dir. Called at the end of executeBuild (Step 8+).
//
// Steps:
//  1. For each sub-package, glob files from stagingDir and render an nfpm config.
//  2. Run nfpm pkg --packager rpm to produce the RPM into a temp dir.
//  3. Sign each RPM with the per-cluster GPG key via rpm --addsign.
//  4. Push each RPM into the repo dir via clustr-privhelper repo-push.
//  5. Run createrepo_c via clustr-privhelper repo-refresh.
func (m *Manager) buildSlurmRPMs(ctx context.Context, buildID, slurmVersion, arch, stagingDir string) error {
	m.logBuildLine(buildID, "[rpm] packaging Slurm sub-package RPMs with nfpm")

	// Determine EL major version from the build host.
	elMajor := detectELMajor()
	m.logBuildLine(buildID, fmt.Sprintf("[rpm] EL major: %s, arch: %s", elMajor, arch))

	// Resolve the nfpm arch name (nfpm uses "aarch64" and "x86_64").
	nfpmArch := arch
	if nfpmArch == "" {
		nfpmArch = runtime.GOARCH
		if nfpmArch == "amd64" {
			nfpmArch = "x86_64"
		}
	}

	// Temp dir for intermediate nfpm work.
	tmpDir, err := os.MkdirTemp("", "clustr-rpm-build-*")
	if err != nil {
		return fmt.Errorf("rpm: mkdir tmpdir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Set up a temporary GNUPGHOME with the private key for signing.
	gnupgHome, cleanup, err := m.setupGPGHome(ctx)
	if err != nil {
		return fmt.Errorf("rpm: setup gpg home: %w", err)
	}
	defer cleanup()

	// Repo destination directory: /{repoBaseDir}/{el-major}/{arch}/
	repoDir := filepath.Join(repoBaseDir, elMajor, nfpmArch)

	var builtRPMs []string

	for _, pkg := range slurmSubPackages {
		// Collect files for this sub-package.
		entries := collectNfpmContents(stagingDir, pkg.GlobPatterns)
		if len(entries) == 0 {
			m.logBuildLine(buildID, fmt.Sprintf("[rpm] sub-package %s: no files matched (skipping)", pkg.Name))
			continue
		}

		// Render the nfpm config.
		data := nfpmSubPackageData{
			Name:        pkg.Name,
			Arch:        nfpmArch,
			Version:     slurmVersion,
			Summary:     pkg.Summary,
			Description: pkg.Description,
			Contents:    entries,
		}
		cfgContent, err := renderNfpmConfig(data)
		if err != nil {
			return fmt.Errorf("rpm: render nfpm config for %s: %w", pkg.Name, err)
		}

		cfgFile := filepath.Join(tmpDir, pkg.Name+"-nfpm.yaml")
		if err := os.WriteFile(cfgFile, []byte(cfgContent), 0600); err != nil {
			return fmt.Errorf("rpm: write nfpm config for %s: %w", pkg.Name, err)
		}

		// Run nfpm pkg.
		rpmName := fmt.Sprintf("%s-%s-clustr1.%s.%s.rpm", pkg.Name, slurmVersion, elMajor, nfpmArch)
		rpmPath := filepath.Join(tmpDir, rpmName)

		m.logBuildLine(buildID, fmt.Sprintf("[rpm] building %s", rpmName))
		nfpmCmd := exec.CommandContext(ctx, "nfpm", "pkg", "--packager", "rpm",
			"--config", cfgFile, "--target", rpmPath)
		nfpmCmd.Env = os.Environ()
		if out, err := nfpmCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("rpm: nfpm pkg %s: %w\noutput: %s", pkg.Name, err, strings.TrimRight(string(out), "\n"))
		}

		// Sign the RPM.
		m.logBuildLine(buildID, fmt.Sprintf("[rpm] signing %s", rpmName))
		if err := m.signRPM(ctx, gnupgHome, rpmPath); err != nil {
			return fmt.Errorf("rpm: sign %s: %w", pkg.Name, err)
		}

		builtRPMs = append(builtRPMs, rpmPath)
		m.logBuildLine(buildID, fmt.Sprintf("[rpm] built and signed: %s", rpmName))
	}

	if len(builtRPMs) == 0 {
		return fmt.Errorf("rpm: no RPMs were produced — check staging dir structure at %s", stagingDir)
	}

	// Push each RPM into the repo dir.
	for _, rpmPath := range builtRPMs {
		dst := filepath.Join(repoDir, filepath.Base(rpmPath))
		m.logBuildLine(buildID, fmt.Sprintf("[rpm] pushing to repo: %s", filepath.Base(rpmPath)))
		if err := privhelper.RepoPush(ctx, rpmPath, dst); err != nil {
			return fmt.Errorf("rpm: repo-push %s: %w", filepath.Base(rpmPath), err)
		}
	}

	// Refresh repo metadata.
	m.logBuildLine(buildID, fmt.Sprintf("[rpm] running createrepo_c on %s", repoDir))
	if err := privhelper.RepoRefresh(ctx, repoDir); err != nil {
		return fmt.Errorf("rpm: repo-refresh: %w", err)
	}

	m.logBuildLine(buildID, fmt.Sprintf("[rpm] %d RPMs published to %s", len(builtRPMs), repoDir))
	return nil
}

// setupGPGHome creates a temp GNUPGHOME and imports the per-cluster private key.
// Returns the temp dir path and a cleanup func.
func (m *Manager) setupGPGHome(ctx context.Context) (string, func(), error) {
	privKey, err := m.getRepoGPGPrivateKey(ctx)
	if err != nil {
		return "", func() {}, fmt.Errorf("get private key: %w", err)
	}

	tmpHome, err := os.MkdirTemp("", "clustr-gpg-sign-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("mkdir gnupghome: %w", err)
	}

	cleanup := func() { os.RemoveAll(tmpHome) }

	// Import the private key.
	importCmd := exec.CommandContext(ctx, "gpg", "--batch", "--import")
	importCmd.Env = append(os.Environ(), "GNUPGHOME="+tmpHome)
	importCmd.Stdin = bytes.NewReader(privKey)
	if out, err := importCmd.CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("gpg --import: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}

	return tmpHome, cleanup, nil
}

// signRPM signs an RPM in-place using rpm --addsign with the key in gnupgHome.
func (m *Manager) signRPM(ctx context.Context, gnupgHome, rpmPath string) error {
	// Write a minimal rpmmacros file pointing at the right gnupghome and key.
	macrosFile := filepath.Join(gnupgHome, "rpmmacros")
	macrosContent := fmt.Sprintf(`%%_gpg_name repo@clustr.internal
%%__gpg /usr/bin/gpg
%%_gpg_path %s
%%_signature gpg
`, gnupgHome)
	if err := os.WriteFile(macrosFile, []byte(macrosContent), 0600); err != nil {
		return fmt.Errorf("write rpmmacros: %w", err)
	}

	// rpm --addsign reads %_rpmmacrofiles; we set HOME to point at our temp dir.
	signCmd := exec.CommandContext(ctx, "rpm", "--addsign", rpmPath)
	signCmd.Env = append(os.Environ(),
		"GNUPGHOME="+gnupgHome,
		"HOME="+gnupgHome, // rpm looks for ~/.rpmmacros
	)
	// Write the rpmmacros to the fake HOME as well.
	homeRPMMacros := filepath.Join(gnupgHome, ".rpmmacros")
	if err := os.WriteFile(homeRPMMacros, []byte(macrosContent), 0600); err != nil {
		return fmt.Errorf("write ~/.rpmmacros: %w", err)
	}

	if out, err := signCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rpm --addsign: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// collectNfpmContents globs for files matching patterns under stagingDir and
// returns nfpm content entries. Patterns are relative to stagingDir.
func collectNfpmContents(stagingDir string, patterns []string) []nfpmContentEntry {
	var entries []nfpmContentEntry
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		fullPattern := filepath.Join(stagingDir, pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil || len(matches) == 0 {
			continue
		}
		for _, src := range matches {
			if seen[src] {
				continue
			}
			seen[src] = true
			// dst is the path on the target system: remove stagingDir prefix.
			rel, err := filepath.Rel(stagingDir, src)
			if err != nil {
				continue
			}
			dst := "/" + rel
			mode := "0755"
			if strings.HasSuffix(src, ".so") || strings.Contains(src, ".so.") {
				mode = "0644"
			}
			entries = append(entries, nfpmContentEntry{
				Src:  src,
				Dst:  dst,
				Type: "file",
				Mode: mode,
			})
		}
	}
	return entries
}

// renderNfpmConfig renders the nfpm YAML config for one sub-package.
func renderNfpmConfig(data nfpmSubPackageData) (string, error) {
	tmpl, err := template.New("nfpm").Parse(nfpmTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// detectELMajor returns the EL major version string (e.g. "el9") for the current host.
// Falls back to "el9" if detection fails.
func detectELMajor() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "el9"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VERSION_ID=") {
			ver := strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"`)
			if len(ver) >= 1 {
				return "el" + ver[:1]
			}
		}
	}
	return "el9"
}

// ─── Repo HTTP handler ────────────────────────────────────────────────────────

// HandleRepoFile serves GET /repo/clustr-internal-repo/... as a static file handler.
// No authentication required — yum repos are public by design; GPG signature
// verification is the trust mechanism.
func (m *Manager) HandleRepoFile(w http.ResponseWriter, r *http.Request) {
	// Strip the /repo/clustr-internal-repo prefix to get the relative path.
	const prefix = "/repo/clustr-internal-repo"
	relPath := strings.TrimPrefix(r.URL.Path, prefix)
	if relPath == "" || relPath == "/" {
		http.Error(w, "directory listing not supported", http.StatusForbidden)
		return
	}

	// Guard against path traversal.
	clean := filepath.Clean(relPath)
	if strings.Contains(clean, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(repoBaseDir, clean)

	// Serve the GPG public key from the DB (not disk) so it's always in sync.
	if clean == "/RPM-GPG-KEY-clustr-internal-repo" {
		m.serveGPGPublicKey(w, r)
		return
	}

	http.ServeFile(w, r, fullPath)
}

// serveGPGPublicKey writes the ASCII-armored GPG public key for this cluster's
// clustr-internal-repo. Returns 404 if no key has been generated yet.
func (m *Manager) serveGPGPublicKey(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.db.SlurmGetRepoGPGConfig(r.Context())
	if err != nil {
		http.Error(w, "GPG key not generated (run POST /api/v1/slurm/repo/init-gpg)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(cfg.PublicKeyArmored))
}

// ─── Repo config file for nodes ──────────────────────────────────────────────

// GenerateRepoFile returns the content of the yum repo file to be pushed to
// each node at provisioning time. serverURL is the base URL of clustr-serverd
// as reachable from the node.
func GenerateRepoFile(serverURL string) string {
	base := strings.TrimRight(serverURL, "/")
	return fmt.Sprintf(`[clustr-internal-repo]
name=clustr internal repo
baseurl=%s/repo/clustr-internal-repo/$releasever/$basearch/
enabled=1
gpgcheck=1
gpgkey=%s/repo/clustr-internal-repo/RPM-GPG-KEY-clustr-internal-repo
`, base, base)
}

// GetRepoGPGPublicKey returns the ASCII-armored public key for this cluster.
// Returns empty string if no key has been generated yet.
func (m *Manager) GetRepoGPGPublicKey(ctx context.Context) string {
	cfg, err := m.db.SlurmGetRepoGPGConfig(ctx)
	if err != nil {
		return ""
	}
	return cfg.PublicKeyArmored
}

// ─── Random bytes ─────────────────────────────────────────────────────────────

// randomBytes is a convenience wrapper around crypto/rand for use in this file.
// (Named to avoid conflict with other rand usage in the package.)
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

// ─── Repo file push ───────────────────────────────────────────────────────────

// PushRepoFileTo pushes /etc/yum.repos.d/clustr-internal-repo.repo to a single
// node via the generic config_push mechanism.  The node's clientd writes the file
// atomically and runs "dnf clean metadata" afterwards.
//
// If the node is offline this returns an error; callers should treat it as
// non-fatal and log it (the file will be pushed on the next slurm sync/push).
func (m *Manager) PushRepoFileTo(ctx context.Context, nodeID string) error {
	if m.hub == nil {
		return fmt.Errorf("slurm: repo file push: hub not available")
	}
	if !m.hub.IsConnected(nodeID) {
		return fmt.Errorf("slurm: repo file push: node %s is offline", nodeID)
	}

	serverURL := strings.TrimRight(m.ServerURL, "/")
	if serverURL == "" {
		return fmt.Errorf("slurm: repo file push: ServerURL not configured")
	}

	content := GenerateRepoFile(serverURL)
	sum := sha256.Sum256([]byte(content))
	checksum := fmt.Sprintf("sha256:%x", sum)

	msgID := newUUID()

	payloadBytes, err := json.Marshal(clientd.ConfigPushPayload{
		Target:   "clustr-internal-repo",
		Content:  content,
		Checksum: checksum,
	})
	if err != nil {
		return fmt.Errorf("slurm: repo file push: marshal payload: %w", err)
	}

	msg := clientd.ServerMessage{
		Type:    "config_push",
		MsgID:   msgID,
		Payload: json.RawMessage(payloadBytes),
	}

	ackCh := m.hub.RegisterAck(msgID)
	defer m.hub.UnregisterAck(msgID)

	if err := m.hub.Send(nodeID, msg); err != nil {
		return fmt.Errorf("slurm: repo file push: send to node %s: %w", nodeID, err)
	}

	select {
	case ack := <-ackCh:
		if !ack.OK {
			return fmt.Errorf("slurm: repo file push: node %s nacked: %s", nodeID, ack.Error)
		}
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("slurm: repo file push: ack timeout for node %s", nodeID)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PushRepoFileToAllNodes pushes the clustr-internal-repo yum repo file to all
// currently-connected Slurm-managed nodes.  Offline nodes are skipped (non-fatal).
// Returns the count of successful pushes and a summary of failures.
func (m *Manager) PushRepoFileToAllNodes(ctx context.Context) (int, []string) {
	if m.hub == nil {
		return 0, []string{"hub not available"}
	}

	connected := m.hub.ConnectedNodes()
	var succeeded int
	var failures []string

	for _, nodeID := range connected {
		if err := m.PushRepoFileTo(ctx, nodeID); err != nil {
			log.Warn().Err(err).Str("node_id", nodeID).Msg("slurm: repo file push to node failed (non-fatal)")
			failures = append(failures, fmt.Sprintf("%s: %v", nodeID, err))
		} else {
			succeeded++
			log.Info().Str("node_id", nodeID).Msg("slurm: repo file pushed to node")
		}
	}

	return succeeded, failures
}

