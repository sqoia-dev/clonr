// builder_test.go — anti-regression tests for the Slurm build pipeline.
//
// Tests cover:
//   - buildSlurmConfigureArgs: extra ConfigureFlags are included in the output
//   - buildSlurmConfigureArgs: operator --with-<dep>=<path> overrides auto-wired path
//   - buildSlurmConfigureArgs: override detection does not regress the default case
package slurm

import (
	"strings"
	"testing"
)

// TestBuildSlurmConfigureFlagsPassThrough asserts that extra flags supplied via
// BuildConfig.ConfigureFlags appear verbatim in the configure argument list.
// This is the anti-regression test for the "silently dropped" bug.
func TestBuildSlurmConfigureFlagsPassThrough(t *testing.T) {
	cfg := BuildConfig{
		SlurmVersion:   "24.11.4",
		Arch:           "x86_64",
		ConfigureFlags: []string{"--with-pmix", "--with-ucx=/usr/local"},
	}
	depPaths := map[string]string{} // no source-built deps

	args := buildSlurmConfigureArgs(cfg, depPaths)

	// Both extra flags must appear in the output.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--with-pmix") {
		t.Errorf("expected --with-pmix in configure args, got: %s", joined)
	}
	if !strings.Contains(joined, "--with-ucx=/usr/local") {
		t.Errorf("expected --with-ucx=/usr/local in configure args, got: %s", joined)
	}
}

// TestBuildSlurmConfigureFlagsOperatorOverride asserts that when the operator
// supplies --with-ucx=/usr/local, the auto-wired --with-ucx=<dep-path> is NOT
// added to the configure line. The operator's flag must appear exactly once.
func TestBuildSlurmConfigureFlagsOperatorOverride(t *testing.T) {
	cfg := BuildConfig{
		SlurmVersion:   "24.11.4",
		Arch:           "x86_64",
		ConfigureFlags: []string{"--with-ucx=/usr/local", "--with-pmix=/opt/pmix"},
	}
	// Simulate dep-built paths for ucx and pmix.
	depPaths := map[string]string{
		"ucx":  "/var/lib/clustr/builds/abc/deps/ucx-install",
		"pmix": "/var/lib/clustr/builds/abc/deps/pmix-install",
	}

	args := buildSlurmConfigureArgs(cfg, depPaths)
	joined := strings.Join(args, " ")

	// Operator's flags must appear exactly once.
	countUCX := strings.Count(joined, "--with-ucx=")
	if countUCX != 1 {
		t.Errorf("expected --with-ucx= exactly once, got %d occurrences in: %s", countUCX, joined)
	}
	if !strings.Contains(joined, "--with-ucx=/usr/local") {
		t.Errorf("expected operator's --with-ucx=/usr/local to be present, got: %s", joined)
	}

	countPMIx := strings.Count(joined, "--with-pmix=")
	if countPMIx != 1 {
		t.Errorf("expected --with-pmix= exactly once, got %d occurrences in: %s", countPMIx, joined)
	}
	if !strings.Contains(joined, "--with-pmix=/opt/pmix") {
		t.Errorf("expected operator's --with-pmix=/opt/pmix to be present, got: %s", joined)
	}
}

// TestBuildSlurmConfigureBaseFlags asserts that the base flags are always present.
func TestBuildSlurmConfigureBaseFlags(t *testing.T) {
	cfg := BuildConfig{
		SlurmVersion: "24.11.4",
		Arch:         "x86_64",
	}
	depPaths := map[string]string{}

	args := buildSlurmConfigureArgs(cfg, depPaths)
	joined := strings.Join(args, " ")

	for _, want := range []string{"--prefix=/usr/local", "--sysconfdir=/etc/slurm", "--enable-pam"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected base flag %q in configure args, got: %s", want, joined)
		}
	}
}

// TestBuildSlurmConfigureDepPathsWiredWhenNoOverride asserts that when
// ConfigureFlags does NOT override a dep, the dep's built path is auto-wired.
func TestBuildSlurmConfigureDepPathsWiredWhenNoOverride(t *testing.T) {
	cfg := BuildConfig{
		SlurmVersion: "24.11.4",
		Arch:         "x86_64",
		// No ConfigureFlags — operator has not overridden anything.
	}
	depPaths := map[string]string{
		"munge":  "/deps/munge-install",
		"hwloc":  "/deps/hwloc-install",
		"ucx":    "/deps/ucx-install",
		"pmix":   "/deps/pmix-install",
		"libjwt": "/deps/libjwt-install",
	}

	args := buildSlurmConfigureArgs(cfg, depPaths)
	joined := strings.Join(args, " ")

	wantPairs := []struct{ flag, path string }{
		{"--with-munge=", "/deps/munge-install"},
		{"--with-hwloc=", "/deps/hwloc-install"},
		{"--with-ucx=", "/deps/ucx-install"},
		{"--with-pmix=", "/deps/pmix-install"},
		{"--with-jwt=", "/deps/libjwt-install"},
	}
	for _, wp := range wantPairs {
		if !strings.Contains(joined, wp.flag+wp.path) {
			t.Errorf("expected %s%s in configure args, got: %s", wp.flag, wp.path, joined)
		}
	}
}
