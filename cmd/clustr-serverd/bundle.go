package main

// bundle.go implements the "clustr-serverd bundle" subcommand family.
//
// Subcommands:
//
//	bundle install [--from-release TAG] [--from-file PATH] [--sha256 HASH] [--rollback]
//	bundle list
//
// Design: see docs/slurm-build-pipeline.md §7.4 (server-install and self-heal
// flow) and §15 (PR3 handoff).
//
// Atomic swap strategy:
//  1. Extract bundle to <repoDir>/.staging-<random>/
//  2. On success, rename existing <distro>-<arch>/ to .previous-<timestamp>
//  3. Rename .staging-<random>/<distro>-<arch>/ into place
//  4. Write .installed-version JSON
//  5. Write RPM-GPG-KEY-clustr from embedded pubkey (source of truth over bundle copy)
//  6. Keep only the most recent .previous-* to bound disk use

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/server"
)

// Build-time injected variables (via -ldflags in Makefile).
// Defaults point at the bundle released alongside this server binary.
// Override with --from-release or --from-file if needed.
var (
	builtinSlurmBundleVersion = "v24.11.4-clustr1"
	builtinSlurmBundleSHA256  = "d5e397e19bb407b380eacfc03185ab8e1a705365eb598c0e042f80d19a91d9d6"
	builtinSlurmVersion       = "24.11.4"
)

// githubReleaseBaseURL is the base URL for clustr GitHub Release downloads.
const githubReleaseBaseURL = "https://github.com/sqoia-dev/clustr/releases/download"

// installedVersion is the JSON schema written to <subtree>/.installed-version.
// This mirrors server.installedBundleInfo but lives in the cmd package so
// bundle.go can write it without crossing internal/server import boundaries.
type installedVersion struct {
	Distro        string `json:"distro"`
	Arch          string `json:"arch"`
	SlurmVersion  string `json:"slurm_version"`
	ClustrRelease string `json:"clustr_release"`
	InstalledAt   string `json:"installed_at"`
	BundleSHA256  string `json:"bundle_sha256"`
}

// manifest is the subset of manifest.json we read for sanity-checking.
type manifest struct {
	SlurmVersion  string `json:"slurm_version"`
	ClustrRelease int    `json:"clustr_release"`
	Distro        string `json:"distro"`
	Arch          string `json:"arch"`
}

func init() {
	bundleCmd := &cobra.Command{
		Use:   "bundle",
		Short: "Manage the bundled Slurm package repository",
	}

	// ---------- bundle install ----------
	var flagFromRelease string
	var flagFromFile string
	var flagSHA256 string
	var flagRollback bool

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install a Slurm bundle into the repo directory",
		Long: `Install a signed Slurm RPM bundle into the clustr-serverd repo directory.

The bundle is fetched from a GitHub Release tag (--from-release) or from a
local file (--from-file).  When neither flag is provided, the version compiled
into this binary is installed (default: ` + builtinSlurmBundleVersion + `).

Use --rollback to swap the most recent .previous-* backup back into place.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadServerConfig()
			return runBundleInstall(cfg.RepoDir, flagFromRelease, flagFromFile, flagSHA256, flagRollback)
		},
	}
	installCmd.Flags().StringVar(&flagFromRelease, "from-release", "",
		"Install from GitHub Release tag (e.g. slurm-v24.11.4-clustr1)")
	installCmd.Flags().StringVar(&flagFromFile, "from-file", "",
		"Install from a local tarball path")
	installCmd.Flags().StringVar(&flagSHA256, "sha256", "",
		"Expected SHA256 hex digest (required for --from-file when no sibling .sha256 file exists)")
	installCmd.Flags().BoolVar(&flagRollback, "rollback", false,
		"Roll back to the previous bundle (swap .previous-* back into place)")

	// ---------- bundle list ----------
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List installed Slurm bundles",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadServerConfig()
			return runBundleList(cfg.RepoDir)
		},
	}

	bundleCmd.AddCommand(installCmd, listCmd)
	rootCmd.AddCommand(bundleCmd)
}

// runBundleInstall dispatches to the appropriate install path.
func runBundleInstall(repoDir, fromRelease, fromFile, flagSHA256 string, rollback bool) error {
	if rollback {
		return runBundleRollback(repoDir)
	}

	// Default: install the version baked into this binary.
	if fromRelease == "" && fromFile == "" {
		fromRelease = "slurm-" + builtinSlurmBundleVersion
		flagSHA256 = builtinSlurmBundleSHA256
		fmt.Printf("No --from-release or --from-file specified; using built-in version %s\n", builtinSlurmBundleVersion)
	}

	if fromRelease != "" && fromFile != "" {
		return fmt.Errorf("--from-release and --from-file are mutually exclusive")
	}

	if fromRelease != "" {
		return installFromRelease(repoDir, fromRelease, flagSHA256)
	}
	return installFromFile(repoDir, fromFile, flagSHA256)
}

// installFromRelease downloads the bundle tarball from a GitHub Release.
// tag must be the full release tag, e.g. "slurm-v24.11.4-clustr1".
func installFromRelease(repoDir, tag, expectedSHA256 string) error {
	// Derive the bundle filename from the tag.
	// Tag format: slurm-v<version>-clustr<n>
	// Bundle format: clustr-slurm-bundle-v<version>-clustr<n>-el9-x86_64.tar.gz
	bundleVersion := strings.TrimPrefix(tag, "slurm-")
	bundleName := fmt.Sprintf("clustr-slurm-bundle-%s-el9-x86_64.tar.gz", bundleVersion)
	bundleURL := fmt.Sprintf("%s/%s/%s", githubReleaseBaseURL, tag, bundleName)
	sha256URL := bundleURL + ".sha256"

	fmt.Printf("Fetching bundle: %s\n", bundleURL)

	// Download to a temp file first so we can SHA256-verify before extracting.
	tmpFile, err := os.CreateTemp("", "clustr-bundle-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if err := downloadFile(bundleURL, tmpFile); err != nil {
		return fmt.Errorf("download bundle: %w", err)
	}

	// If no expected SHA256 was provided by the caller, try fetching the sidecar.
	if expectedSHA256 == "" {
		fmt.Printf("Fetching SHA256 sidecar: %s\n", sha256URL)
		var sidecarErr error
		expectedSHA256, sidecarErr = fetchSHA256Sidecar(sha256URL)
		if sidecarErr != nil {
			return fmt.Errorf("fetch sha256 sidecar: %w\n"+
				"Provide --sha256 to skip sidecar fetch", sidecarErr)
		}
	}

	if err := verifySHA256(tmpFile.Name(), expectedSHA256); err != nil {
		return fmt.Errorf("SHA256 verification failed: %w", err)
	}
	fmt.Println("SHA256 OK")

	return extractAndInstall(repoDir, tmpFile.Name(), expectedSHA256, bundleVersion)
}

// installFromFile installs from a local tarball.  If expectedSHA256 is empty
// it is read from a sibling <path>.sha256 file.
func installFromFile(repoDir, path, expectedSHA256 string) error {
	if path == "" {
		return fmt.Errorf("--from-file requires a path argument")
	}

	if expectedSHA256 == "" {
		// Try sibling .sha256 file.
		sidecarPath := path + ".sha256"
		data, err := os.ReadFile(sidecarPath)
		if err != nil {
			return fmt.Errorf("no --sha256 flag and no sibling %s: %w", sidecarPath, err)
		}
		expectedSHA256 = strings.TrimSpace(strings.Fields(string(data))[0])
	}

	if err := verifySHA256(path, expectedSHA256); err != nil {
		return fmt.Errorf("SHA256 verification failed: %w", err)
	}
	fmt.Println("SHA256 OK")

	// Derive bundle version from filename if possible.
	// clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz → v24.11.4-clustr1
	bundleVersion := bundleVersionFromFilename(filepath.Base(path))

	return extractAndInstall(repoDir, path, expectedSHA256, bundleVersion)
}

// extractAndInstall verifies + atomically installs the bundle tarball at srcPath.
func extractAndInstall(repoDir, srcPath, bundleSHA256, bundleVersion string) error {
	// Ensure repo dir exists.
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return fmt.Errorf("create repo dir: %w", err)
	}

	// Extract to a random staging dir.  crypto/rand for uniqueness.
	var randBuf [4]byte
	_, _ = rand.Read(randBuf[:])
	stagingDir := filepath.Join(repoDir, fmt.Sprintf(".staging-%x", randBuf))
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	fmt.Printf("Extracting bundle to staging: %s\n", stagingDir)
	mf, err := extractBundle(srcPath, stagingDir)
	if err != nil {
		return fmt.Errorf("extract bundle: %w", err)
	}

	// Verify RPM signatures.
	fmt.Println("Verifying RPM signatures...")
	if err := verifyRPMSignatures(stagingDir, mf.Distro+"-"+mf.Arch); err != nil {
		return fmt.Errorf("RPM signature verification failed: %w", err)
	}
	fmt.Println("RPM signatures OK")

	// Idempotency: check if the same version is already installed.
	subDir := mf.Distro + "-" + mf.Arch
	installedVersionPath := filepath.Join(repoDir, subDir, ".installed-version")
	if data, err := os.ReadFile(installedVersionPath); err == nil {
		var existing installedVersion
		if json.Unmarshal(data, &existing) == nil && existing.BundleSHA256 == bundleSHA256 {
			fmt.Printf("Bundle %s already installed (idempotent — nothing to do)\n", bundleVersion)
			return nil
		}
	}

	// Atomic swap: move current → .previous-<timestamp>, then staging → live.
	destDir := filepath.Join(repoDir, subDir)
	stagingSubDir := filepath.Join(stagingDir, subDir)

	if _, err := os.Stat(destDir); err == nil {
		prevDir := filepath.Join(repoDir, ".previous-"+time.Now().UTC().Format("20060102T150405Z"))
		if err := os.Rename(destDir, prevDir); err != nil {
			return fmt.Errorf("rotate current bundle to previous: %w", err)
		}
		fmt.Printf("Archived current bundle to %s\n", filepath.Base(prevDir))
		// Prune all but the most recent .previous-* to bound disk use.
		_ = pruneOldPreviousDirs(repoDir, 1)
	}

	if err := os.Rename(stagingSubDir, destDir); err != nil {
		return fmt.Errorf("promote staging bundle: %w", err)
	}

	// Write .installed-version.
	iv := installedVersion{
		Distro:        mf.Distro,
		Arch:          mf.Arch,
		SlurmVersion:  mf.SlurmVersion,
		ClustrRelease: fmt.Sprintf("%d", mf.ClustrRelease),
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
		BundleSHA256:  bundleSHA256,
	}
	ivData, _ := json.MarshalIndent(iv, "", "  ")
	if err := os.WriteFile(installedVersionPath, ivData, 0o644); err != nil {
		return fmt.Errorf("write .installed-version: %w", err)
	}

	// Write RPM-GPG-KEY-clustr from embedded key (source of truth).
	if err := server.WriteGPGKeyToRepo(repoDir); err != nil {
		return fmt.Errorf("write GPG key: %w", err)
	}

	fmt.Printf("Bundle %s installed successfully at %s\n", bundleVersion, destDir)
	return nil
}

// runBundleRollback swaps the most recent .previous-* back into place.
//
// Layout after atomic swap:
//   <repoDir>/.previous-<ts>  — the old <distro>-<arch> directory content (renamed wholesale)
//   <repoDir>/<distro>-<arch> — the live directory
//
// Rollback reads .installed-version inside the .previous-* dir to learn the
// distro-arch, then swaps live → trash and .previous-* → live.
func runBundleRollback(repoDir string) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return fmt.Errorf("read repo dir: %w", err)
	}

	var prevDirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".previous-") {
			prevDirs = append(prevDirs, e.Name())
		}
	}
	if len(prevDirs) == 0 {
		return fmt.Errorf("no previous bundle found in %s", repoDir)
	}
	sort.Strings(prevDirs)
	mostRecent := prevDirs[len(prevDirs)-1]

	// Read .installed-version from inside the .previous-* dir to learn distro+arch.
	prevPath := filepath.Join(repoDir, mostRecent)
	ivData, err := os.ReadFile(filepath.Join(prevPath, ".installed-version"))
	if err != nil {
		return fmt.Errorf("read .installed-version from previous bundle: %w", err)
	}
	var iv installedVersion
	if err := json.Unmarshal(ivData, &iv); err != nil {
		return fmt.Errorf("parse .installed-version from previous bundle: %w", err)
	}
	subDir := iv.Distro + "-" + iv.Arch
	if subDir == "-" || iv.Distro == "" {
		return fmt.Errorf("cannot determine distro-arch from previous bundle .installed-version")
	}

	livePath := filepath.Join(repoDir, subDir)

	// Move live → .trash-<ts>, then .previous-* → live.
	trashPath := filepath.Join(repoDir, ".trash-"+time.Now().UTC().Format("20060102T150405Z"))
	if _, err := os.Stat(livePath); err == nil {
		if err := os.Rename(livePath, trashPath); err != nil {
			return fmt.Errorf("archive live bundle before rollback: %w", err)
		}
	}

	if err := os.Rename(prevPath, livePath); err != nil {
		// Try to restore live from trash before returning error.
		_ = os.Rename(trashPath, livePath)
		return fmt.Errorf("rollback rename failed: %w", err)
	}

	// Clean up trash immediately (it's the bundle we just replaced).
	_ = os.RemoveAll(trashPath)

	fmt.Printf("Rolled back to %s\n", mostRecent)
	return nil
}

// runBundleList prints installed bundles in a human-readable table.
func runBundleList(repoDir string) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		fmt.Println("No bundles installed (repo dir not found)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DISTRO-ARCH\tSLURM VERSION\tCLUSTR RELEASE\tINSTALLED AT\tSHA256 (short)")
	found := false
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		vf := filepath.Join(repoDir, e.Name(), ".installed-version")
		data, err := os.ReadFile(vf)
		if err != nil {
			continue
		}
		var iv installedVersion
		if err := json.Unmarshal(data, &iv); err != nil {
			continue
		}
		shortSHA := iv.BundleSHA256
		if len(shortSHA) > 12 {
			shortSHA = shortSHA[:12] + "..."
		}
		fmt.Fprintf(tw, "%s-%s\t%s\t%s\t%s\t%s\n",
			iv.Distro, iv.Arch, iv.SlurmVersion, iv.ClustrRelease, iv.InstalledAt, shortSHA)
		found = true
	}
	if !found {
		fmt.Fprintln(tw, "(none)")
	}
	return tw.Flush()
}

// --- helpers ---

// downloadFile streams url into dst (already open for writing).
func downloadFile(url string, dst *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	_, err = io.Copy(dst, resp.Body)
	return err
}

// fetchSHA256Sidecar downloads a .sha256 sidecar file and returns the hex digest.
// The file may be in "hex  filename" format (sha256sum output) or bare hex.
func fetchSHA256Sidecar(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", err
	}

	parts := strings.Fields(string(data))
	if len(parts) == 0 {
		return "", fmt.Errorf("empty sha256 sidecar")
	}
	return parts[0], nil
}

// verifySHA256 recomputes the SHA256 of the file at path and compares it to
// expected (hex string).  Returns an error with both hashes on mismatch.
func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("got %s, want %s", got, expected)
	}
	return nil
}

// extractBundle extracts the gzip+tar bundle at srcPath into destDir.
// Expected layout inside the tarball (from PR2):
//
//	clustr-slurm-bundle/
//	├── manifest.json
//	├── RPM-GPG-KEY-clustr
//	└── el9-x86_64/
//	    ├── *.rpm
//	    └── repodata/
//
// Returns the manifest parsed from the bundle.
func extractBundle(srcPath, destDir string) (*manifest, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var mf *manifest
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		// Strip the top-level bundle directory prefix so files land directly
		// under destDir.  Path is e.g. "clustr-slurm-bundle/el9-x86_64/foo.rpm"
		// → "el9-x86_64/foo.rpm".
		name := hdr.Name
		if idx := strings.Index(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		if name == "" {
			continue
		}

		// Guard against path traversal.
		cleanName := filepath.Clean(name)
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return nil, fmt.Errorf("path traversal in tarball: %q", hdr.Name)
		}

		destPath := filepath.Join(destDir, cleanName)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", destPath, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return nil, fmt.Errorf("mkdir parent for %s: %w", destPath, err)
			}
			outFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return nil, fmt.Errorf("create %s: %w", destPath, err)
			}
			_, copyErr := io.Copy(outFile, tr)
			outFile.Close()
			if copyErr != nil {
				return nil, fmt.Errorf("write %s: %w", destPath, copyErr)
			}

			// Parse manifest.json in-flight.
			if cleanName == "manifest.json" {
				data, readErr := os.ReadFile(destPath)
				if readErr == nil {
					mf = &manifest{}
					if jsonErr := json.Unmarshal(data, mf); jsonErr != nil {
						mf = nil // non-fatal; we will check below
					}
				}
			}
		}
	}

	if mf == nil {
		return nil, fmt.Errorf("manifest.json not found or invalid in bundle")
	}
	if mf.Distro == "" || mf.Arch == "" {
		return nil, fmt.Errorf("manifest.json missing distro or arch fields")
	}

	return mf, nil
}

// verifyRPMSignatures calls "rpm -K" on every RPM in the subDir to verify
// that each is signed by a known GPG key.  The embedded clustr pubkey is
// imported into a throw-away RPM macro home directory first.
//
// Shell-out is explicitly acceptable here per the design doc ("shelling out
// is fine for MVP").  rpm is available on the target host (Rocky Linux 9).
func verifyRPMSignatures(stagingDir, subDirName string) error {
	rpmDir := filepath.Join(stagingDir, subDirName)
	entries, err := os.ReadDir(rpmDir)
	if err != nil {
		return fmt.Errorf("read rpm dir: %w", err)
	}

	// Write embedded pubkey to a temp file for rpm --import.
	keyFile, err := os.CreateTemp("", "clustr-gpg-*.asc")
	if err != nil {
		return fmt.Errorf("create temp key file: %w", err)
	}
	defer os.Remove(keyFile.Name())

	if _, err := keyFile.Write(server.GPGKeyBytes()); err != nil {
		return fmt.Errorf("write temp key file: %w", err)
	}
	keyFile.Close()

	// Import the key into a temporary rpm db so we don't pollute the system db.
	tmpDB, err := os.MkdirTemp("", "clustr-rpmdb-*")
	if err != nil {
		return fmt.Errorf("create temp rpm db: %w", err)
	}
	defer os.RemoveAll(tmpDB)

	// rpm --import with --dbpath uses an isolated db.
	importCmd := exec.Command("rpm", "--dbpath", tmpDB, "--import", keyFile.Name()) // #nosec G204 -- binary hardcoded; args are clustr-controlled tmp paths
	if out, err := importCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rpm --import: %w\n%s", err, string(out))
	}

	var rpms []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".rpm") {
			rpms = append(rpms, filepath.Join(rpmDir, e.Name()))
		}
	}
	if len(rpms) == 0 {
		return fmt.Errorf("no RPMs found in %s", rpmDir)
	}

	// rpm -K verifies the signature on each package.
	args := append([]string{"--dbpath", tmpDB, "-K"}, rpms...)
	checkCmd := exec.Command("rpm", args...) // #nosec G204 -- binary hardcoded; RPM paths from staging dir under clustr control
	out, err := checkCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rpm -K failed: %w\n%s", err, string(out))
	}
	return nil
}

// pruneOldPreviousDirs removes all but the <keep> most recent .previous-* dirs.
func pruneOldPreviousDirs(repoDir string, keep int) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return err
	}

	var prevDirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".previous-") {
			prevDirs = append(prevDirs, e.Name())
		}
	}
	if len(prevDirs) <= keep {
		return nil
	}

	sort.Strings(prevDirs) // oldest first (timestamps in name)
	toRemove := prevDirs[:len(prevDirs)-keep]
	for _, d := range toRemove {
		_ = os.RemoveAll(filepath.Join(repoDir, d))
	}
	return nil
}

// bundleVersionFromFilename extracts "v24.11.4-clustr1" from
// "clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz".
// Returns "unknown" if the filename doesn't match.
func bundleVersionFromFilename(name string) string {
	name = strings.TrimSuffix(name, ".tar.gz")
	// clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64
	// Strip known prefix and suffix.
	pfx := "clustr-slurm-bundle-"
	if !strings.HasPrefix(name, pfx) {
		return "unknown"
	}
	rest := name[len(pfx):]
	// rest = "v24.11.4-clustr1-el9-x86_64"
	// Split on "-el" to get the version part.
	if idx := strings.Index(rest, "-el"); idx > 0 {
		return rest[:idx]
	}
	return "unknown"
}
