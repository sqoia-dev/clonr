package isoinstaller

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
)

// kernelBootSupported returns true for distro families where we can reliably
// use direct kernel boot (-kernel/-initrd/-append) to skip the GRUB/isolinux
// boot menu entirely. Rocky, AlmaLinux, CentOS, and RHEL all use the standard
// Anaconda layout with images/pxeboot/vmlinuz + initrd.img.
func kernelBootSupported(d Distro) bool {
	switch d {
	case DistroRocky, DistroAlmaLinux, DistroCentOS, DistroRHEL:
		return true
	default:
		return false
	}
}

// KernelBootFiles holds paths to the extracted kernel and initrd for direct boot.
type KernelBootFiles struct {
	Vmlinuz  string // path to extracted vmlinuz in the work dir
	InitrdImg string // path to extracted initrd.img in the work dir
	ISOLabel string // ISO volume label, used for inst.stage2=hd:LABEL=<label>
}

// PrepareKernelBoot extracts vmlinuz and initrd.img from the installer ISO
// into workDir and reads the ISO volume label. Returns an error if the ISO
// does not contain the expected Anaconda pxeboot layout.
//
// The ISO is accessed read-only via 7z (p7zip) so no root or loop-mount is
// required. p7zip is a single build-time dependency already available on
// RHEL/Rocky (epel) and Debian/Ubuntu (p7zip-full).
func PrepareKernelBoot(isoPath, workDir string, log zerolog.Logger) (*KernelBootFiles, error) {
	label, err := ReadISOVolumeLabel(isoPath)
	if err != nil {
		return nil, fmt.Errorf("kernelboot: read ISO volume label: %w", err)
	}
	log.Debug().Str("iso_label", label).Msg("kernelboot: ISO volume label detected")

	vmlinuzDst := filepath.Join(workDir, "vmlinuz")
	initrdDst := filepath.Join(workDir, "initrd.img")

	if err := extractFromISO(isoPath, "images/pxeboot/vmlinuz", vmlinuzDst); err != nil {
		return nil, fmt.Errorf("kernelboot: extract vmlinuz: %w", err)
	}
	if err := extractFromISO(isoPath, "images/pxeboot/initrd.img", initrdDst); err != nil {
		return nil, fmt.Errorf("kernelboot: extract initrd.img: %w", err)
	}

	log.Info().
		Str("vmlinuz", vmlinuzDst).
		Str("initrd", initrdDst).
		Str("iso_label", label).
		Msg("kernelboot: kernel and initrd extracted for direct boot")

	return &KernelBootFiles{
		Vmlinuz:   vmlinuzDst,
		InitrdImg: initrdDst,
		ISOLabel:  label,
	}, nil
}

// ReadISOVolumeLabel returns the ISO 9660 volume label using isoinfo or blkid.
// This is the same label Anaconda uses for inst.stage2=hd:LABEL=<label>.
func ReadISOVolumeLabel(isoPath string) (string, error) {
	// Try isoinfo first (part of genisoimage / cdrtools — already a clonr dep).
	if path, err := exec.LookPath("isoinfo"); err == nil {
		out, err := exec.Command(path, "-d", "-i", isoPath).CombinedOutput()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "Volume id:") {
					label := strings.TrimSpace(strings.TrimPrefix(line, "Volume id:"))
					if label != "" {
						return label, nil
					}
				}
			}
		}
	}

	// Fall back to blkid.
	if path, err := exec.LookPath("blkid"); err == nil {
		out, err := exec.Command(path, "-o", "value", "-s", "LABEL", isoPath).CombinedOutput()
		if err == nil {
			label := strings.TrimSpace(string(out))
			if label != "" {
				return label, nil
			}
		}
	}

	return "", fmt.Errorf("neither isoinfo nor blkid could read the volume label from %s", isoPath)
}

// extractFromISO extracts a single file from an ISO image to dst.
// It tries, in order:
//  1. 7z (p7zip) — no privileges required, works anywhere.
//  2. isoinfo -i <iso> -x <path> — available wherever genisoimage is installed.
//
// The isoPath within the ISO uses forward slashes as returned by 7z/isoinfo.
func extractFromISO(isoPath, isoFilePath, dst string) error {
	// Try 7z first: `7z e -so <iso> <path>` prints to stdout.
	if path7z, err := exec.LookPath("7z"); err == nil {
		out, err := exec.Command(path7z, "e", "-so", isoPath, isoFilePath).Output()
		if err == nil && len(out) > 0 {
			if writeErr := os.WriteFile(dst, out, 0o644); writeErr != nil {
				return fmt.Errorf("write %s: %w", dst, writeErr)
			}
			return nil
		}
	}

	// Try isoinfo: `isoinfo -i <iso> -x /<PATH-UPPERCASE>` — isoinfo paths are
	// uppercase in the ISO 9660 Joliet/RockRidge namespace.
	if pathIsoinfo, err := exec.LookPath("isoinfo"); err == nil {
		// isoinfo wants the path with a leading slash and uppercase.
		isoInfoPath := "/" + strings.ToUpper(strings.ReplaceAll(isoFilePath, "/", "/"))
		out, err := exec.Command(pathIsoinfo, "-i", isoPath, "-x", isoInfoPath).Output()
		if err == nil && len(out) > 0 {
			if writeErr := os.WriteFile(dst, out, 0o644); writeErr != nil {
				return fmt.Errorf("write %s: %w", dst, writeErr)
			}
			return nil
		}
	}

	return fmt.Errorf("could not extract %s from ISO: install 7z (p7zip-full) or isoinfo (genisoimage)", isoFilePath)
}

// buildKernelAppendLine builds the Anaconda kernel command line for direct boot.
// Parameters are tuned for fully automated, serial-console-only installs.
func buildKernelAppendLine(isoLabel string) string {
	return strings.Join([]string{
		// Tell Anaconda where to find the stage2 squashfs (the installer runtime).
		// LABEL= is matched by the kernel's built-in label scanner; Anaconda
		// falls back to a UUID scan if the label isn't found, but providing the
		// exact label is reliable and fast.
		fmt.Sprintf("inst.stage2=hd:LABEL=%s", isoLabel),

		// Kickstart is on the OEMDRV-labelled seed ISO that Anaconda auto-detects.
		// Specifying it explicitly here is belt-and-suspenders and avoids the 30s
		// OEMDRV scan timeout on some Anaconda versions.
		"inst.ks=hd:LABEL=OEMDRV:/ks.cfg",

		// Direct serial output to the first serial port so our log capture works.
		"console=ttyS0,115200",

		// inst.cmdline: disable the interactive fallback TUI. Combined with
		// a complete kickstart this makes Anaconda exit non-zero instead of
		// prompting if something is misconfigured — fail-fast beats silent hang.
		"inst.cmdline",

		// inst.notmux: disable tmux multiplexing inside Anaconda. Without this,
		// serial output is split across tmux panes and the log is unreadable.
		"inst.notmux",
	}, " ")
}
