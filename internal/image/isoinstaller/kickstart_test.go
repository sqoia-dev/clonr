package isoinstaller

import (
	"strings"
	"testing"
)

// testTemplateData returns a minimal templateData suitable for kickstart generation.
func testTemplateData() templateData {
	return templateData{
		RootPasswordHash: "$6$rounds=4096$test$fakehash",
		DiskSizeGB:       20,
		TargetDisk:       "vda",
	}
}

// TestGenerateKickstart_Firmware_UEFI verifies that firmware=uefi produces an
// ESP (vfat /boot/efi) partition directive and no biosboot directive.
func TestGenerateKickstart_Firmware_UEFI(t *testing.T) {
	opts := BuildOptions{
		Firmware: "uefi",
	}
	cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
	if err != nil {
		t.Fatalf("generateKickstart error: %v", err)
	}

	ks := cfg.KickstartContent

	if !strings.Contains(ks, "vfat") {
		t.Errorf("expected vfat ESP partition for uefi, got kickstart:\n%s", ks)
	}
	if !strings.Contains(ks, "/boot/efi") {
		t.Errorf("expected /boot/efi mount point for uefi, got kickstart:\n%s", ks)
	}
	if strings.Contains(ks, "biosboot") {
		t.Errorf("unexpected biosboot directive in uefi kickstart:\n%s", ks)
	}
	if !strings.Contains(ks, "Firmware: uefi") {
		t.Errorf("expected 'Firmware: uefi' header comment, got:\n%s", ks)
	}
}

// TestGenerateKickstart_Firmware_BIOS verifies that firmware=bios produces a
// biosboot partition directive and no vfat/ESP directive.
func TestGenerateKickstart_Firmware_BIOS(t *testing.T) {
	opts := BuildOptions{
		Firmware: "bios",
	}
	cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
	if err != nil {
		t.Fatalf("generateKickstart error: %v", err)
	}

	ks := cfg.KickstartContent

	if !strings.Contains(ks, "biosboot") {
		t.Errorf("expected biosboot partition for bios firmware, got kickstart:\n%s", ks)
	}
	if strings.Contains(ks, "/boot/efi") {
		t.Errorf("unexpected /boot/efi directive in bios kickstart:\n%s", ks)
	}
	if strings.Contains(ks, "vfat") {
		t.Errorf("unexpected vfat (ESP) directive in bios kickstart:\n%s", ks)
	}
	if !strings.Contains(ks, "Firmware: bios") {
		t.Errorf("expected 'Firmware: bios' header comment, got:\n%s", ks)
	}
}

// TestGenerateKickstart_Firmware_Empty verifies that an empty firmware field
// defaults to uefi (backward-compatible behavior).
func TestGenerateKickstart_Firmware_Empty(t *testing.T) {
	opts := BuildOptions{
		Firmware: "",
	}
	cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
	if err != nil {
		t.Fatalf("generateKickstart error: %v", err)
	}

	ks := cfg.KickstartContent

	// Empty firmware must default to uefi.
	if !strings.Contains(ks, "vfat") {
		t.Errorf("empty firmware should default to uefi (vfat ESP), got kickstart:\n%s", ks)
	}
	if strings.Contains(ks, "biosboot") {
		t.Errorf("unexpected biosboot in default (empty firmware) kickstart:\n%s", ks)
	}
}

// TestGenerateKickstart_Firmware_Invalid verifies that an invalid firmware value
// returns an error rather than silently falling back to uefi (fix: issue #7).
func TestGenerateKickstart_Firmware_Invalid(t *testing.T) {
	opts := BuildOptions{
		Firmware: "legacy", // not a recognized value
	}
	_, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
	if err == nil {
		t.Fatal("expected error for unknown firmware value, got nil")
	}
	if !strings.Contains(err.Error(), "unknown firmware") {
		t.Errorf("error should mention unknown firmware, got: %v", err)
	}
}

// TestGenerateKickstart_CustomKickstart verifies that a custom kickstart bypasses
// template rendering entirely (firmware field is ignored).
func TestGenerateKickstart_CustomKickstart(t *testing.T) {
	custom := "# my custom kickstart\nreboot\n"
	opts := BuildOptions{Firmware: "bios"}
	cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, custom)
	if err != nil {
		t.Fatalf("generateKickstart with custom error: %v", err)
	}
	if cfg.KickstartContent != custom {
		t.Errorf("expected custom kickstart to pass through unchanged, got:\n%s", cfg.KickstartContent)
	}
}

// TestGenerateKickstart_BaseEnvironment_Default verifies that an empty
// BaseEnvironment defaults to "minimal-environment" for backward compat.
func TestGenerateKickstart_BaseEnvironment_Default(t *testing.T) {
	opts := BuildOptions{Firmware: "uefi"} // BaseEnvironment intentionally empty
	cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
	if err != nil {
		t.Fatalf("generateKickstart error: %v", err)
	}
	if !strings.Contains(cfg.KickstartContent, "@^minimal-environment") {
		t.Errorf("expected @^minimal-environment default, got kickstart:\n%s", cfg.KickstartContent)
	}
}

// TestGenerateKickstart_BaseEnvironment_Custom verifies that a non-empty
// BaseEnvironment is used verbatim in the %%packages stanza.
func TestGenerateKickstart_BaseEnvironment_Custom(t *testing.T) {
	opts := BuildOptions{Firmware: "uefi", BaseEnvironment: "server-product-environment"}
	cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
	if err != nil {
		t.Fatalf("generateKickstart error: %v", err)
	}
	if !strings.Contains(cfg.KickstartContent, "@^server-product-environment") {
		t.Errorf("expected @^server-product-environment, got kickstart:\n%s", cfg.KickstartContent)
	}
	if strings.Contains(cfg.KickstartContent, "@^minimal-environment") {
		t.Errorf("unexpected @^minimal-environment when BaseEnvironment=server-product-environment:\n%s", cfg.KickstartContent)
	}
}

// TestGenerateKickstart_SlurmFreeRoles verifies that the HPC role package lists
// do NOT include Slurm packages for RHEL-family distros. Slurm is installed at
// deploy time from the clustr-server's bundled repo; baking it into the base
// image causes file conflicts (PR5 Failure A). See docs/imagebuilder.md.
func TestGenerateKickstart_SlurmFreeRoles(t *testing.T) {
	slurmPkgs := []string{
		"slurm", "slurm-slurmctld", "slurm-slurmd", "slurm-slurmdbd",
		"slurm-pmi", "slurm-ohpc", "slurm-ohpc-pmi", "munge",
	}

	slurmRoles := []string{"head-node", "compute", "gpu-compute"}

	for _, roleID := range slurmRoles {
		roleID := roleID
		t.Run(roleID, func(t *testing.T) {
			opts := BuildOptions{
				Firmware: "uefi",
				RoleIDs:  []string{roleID},
			}
			cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
			if err != nil {
				t.Fatalf("generateKickstart(%s): %v", roleID, err)
			}
			ks := cfg.KickstartContent
			// Extract the %packages block only (stop at %end).
			pkgStart := strings.Index(ks, "%packages")
			pkgEnd := strings.Index(ks[pkgStart:], "%end")
			pkgBlock := ""
			if pkgStart >= 0 && pkgEnd >= 0 {
				pkgBlock = ks[pkgStart : pkgStart+pkgEnd]
			}
			for _, pkg := range slurmPkgs {
				// Check each package name as a standalone line token to avoid
				// false positives from comment lines.
				for _, line := range strings.Split(pkgBlock, "\n") {
					trimmed := strings.TrimSpace(line)
					if trimmed == pkg {
						t.Errorf("role %s: kickstart %%packages contains Slurm package %q — "+
							"Slurm must not be baked into the base image (see docs/imagebuilder.md)",
							roleID, pkg)
					}
				}
			}
		})
	}
}

// TestGenerateKickstart_SlurmVerificationStep verifies that the kickstart
// %post section includes an rpm -qa Slurm verification step that fails the
// build if any Slurm packages are found in the image.
func TestGenerateKickstart_SlurmVerificationStep(t *testing.T) {
	opts := BuildOptions{Firmware: "uefi", RoleIDs: []string{"compute"}}
	cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
	if err != nil {
		t.Fatalf("generateKickstart error: %v", err)
	}
	ks := cfg.KickstartContent

	// The %post block must contain the verification command.
	if !strings.Contains(ks, "rpm -qa | grep -iE") {
		t.Errorf("%%post block is missing the Slurm-free verification step (rpm -qa | grep -iE)\nkickstart:\n%s", ks)
	}
	// Must have the exit 1 on failure.
	if !strings.Contains(ks, "exit 1") {
		t.Errorf("%%post Slurm verification block is missing 'exit 1' on failure\nkickstart:\n%s", ks)
	}
}

// TestGenerateKickstart_NoOpenHPCRepo verifies that the generated kickstart
// does not reference the OpenHPC yum repo URL or repo file.
func TestGenerateKickstart_NoOpenHPCRepo(t *testing.T) {
	for _, roleID := range []string{"head-node", "compute", "gpu-compute"} {
		roleID := roleID
		t.Run(roleID, func(t *testing.T) {
			opts := BuildOptions{Firmware: "uefi", RoleIDs: []string{roleID}}
			cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
			if err != nil {
				t.Fatalf("generateKickstart(%s): %v", roleID, err)
			}
			ks := cfg.KickstartContent
			for _, forbidden := range []string{
				"openhpc.community",
				"OpenHPC.repo",
				"ohpc",
			} {
				if strings.Contains(strings.ToLower(ks), strings.ToLower(forbidden)) {
					t.Errorf("role %s: kickstart references OpenHPC artifact %q — "+
						"OpenHPC must not be referenced in the base image kickstart",
						roleID, forbidden)
				}
			}
		})
	}
}

// TestGenerateKickstart_BootPackages verifies that both BIOS and UEFI boot
// packages are always present in the %packages section, regardless of the
// firmware mode. This implements ADR-0009 (content-only images): a single image
// must work for both firmware modes without runtime package installation at
// deploy time (deploy initramfs has no DNS).
func TestGenerateKickstart_BootPackages(t *testing.T) {
	required := []string{
		"grub2-pc",
		"grub2-pc-modules",
		"grub2-efi-x64",
		"grub2-efi-x64-modules",
		"shim-x64",
		"efibootmgr",
	}

	for _, fw := range []string{"bios", "uefi", ""} {
		fw := fw
		label := fw
		if label == "" {
			label = "(empty/default)"
		}
		t.Run("firmware="+label, func(t *testing.T) {
			opts := BuildOptions{Firmware: fw}
			cfg, err := generateKickstart(DistroRocky, testTemplateData(), opts, "")
			if err != nil {
				t.Fatalf("generateKickstart error: %v", err)
			}
			ks := cfg.KickstartContent
			for _, pkg := range required {
				if !strings.Contains(ks, pkg) {
					t.Errorf("firmware=%s: expected package %q in %%packages, got kickstart:\n%s", fw, pkg, ks)
				}
			}
		})
	}
}
