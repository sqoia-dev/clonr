package deploy

import (
	"fmt"
	"os"
	"strings"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// Distro identifies an OS family and major version for driver dispatch.
// Family values in v1: "el" (RHEL/Rocky/Alma/CentOS Stream family), "ubuntu".
// Major is the distribution major version: 8, 9, or 10 for EL; 24 for Ubuntu 24.04.
type Distro struct {
	Family string
	Major  int
}

// DistroDriver abstracts the OS-specific network-configuration and bootloader
// installation steps of node finalization. Each driver targets one OS family +
// major version combination.
//
// Callers:
//   - writeDistroSystemFiles (called from applyNodeConfig) → WriteSystemFiles
//   - FilesystemDeployer.Finalize bootloader phase          → InstallBootloader
//
// Both methods operate on a mounted rootfs at root; no chroot(2) is required
// for pure file-write operations.
//
// Registration: each driver file calls RegisterDriver in its init() so no
// manual wiring is needed by callers.
type DistroDriver interface {
	// Distro identifies which OS family + major version this driver targets.
	Distro() Distro

	// WriteSystemFiles writes distro-specific network configuration and any
	// distro-specific identity or bootstrap files into the deployed rootfs at root.
	// Generic files (/etc/hostname, /etc/hosts, SSH keys, /etc/shadow) are
	// handled by the caller before this method is invoked; drivers handle only
	// the distro-specific parts (NM keyfiles for EL, netplan for Ubuntu, etc.).
	WriteSystemFiles(root string, cfg api.NodeConfig) error

	// InstallBootloader installs and configures the bootloader for this distro.
	// Called post-WriteSystemFiles, pre-unmount.
	InstallBootloader(ctx *bootloaderCtx) error
}

// bootloaderCtx carries everything InstallBootloader needs.
type bootloaderCtx struct {
	// Ctx is the request context for cancellation and timeout.
	Ctx interface {
		Done() <-chan struct{}
		Err() error
	}
	// MountRoot is the path to the mounted deployed rootfs.
	MountRoot string
	// TargetDisk is the primary raw disk device (e.g. "/dev/sda").
	TargetDisk string
	// AllTargets holds all raw disk devices that need a bootloader written
	// (equal to [TargetDisk] for single-disk layouts; all RAID member disks
	// for RAID-on-whole-disk layouts). Must not be empty for BIOS deploys.
	AllTargets []string
	// IsRAID is true when any RAID arrays are present in the disk layout.
	IsRAID bool
	// IsRAIDOnWholeDisk is true when RAID partitions reference md device paths
	// directly (no dedicated bios_grub partition on each member disk).
	IsRAIDOnWholeDisk bool
	// IsEFI is true when the disk layout contains an ESP partition and the
	// bootloader must be installed via the UEFI path (grub2-install --target=x86_64-efi
	// inside the deployed chroot). Mutually exclusive with BIOS in practice.
	IsEFI bool
}

// drivers is the registry populated by each driver's init() function.
var drivers = map[string]DistroDriver{}

// driverKey returns the canonical map key for a Distro.
func driverKey(d Distro) string {
	return fmt.Sprintf("%s/%d", d.Family, d.Major)
}

// RegisterDriver adds d to the global driver registry.
// Called from init() in each driver file; safe to call multiple times with
// the same key (last registration wins, but that is a programming error).
func RegisterDriver(d DistroDriver) {
	drivers[driverKey(d.Distro())] = d
}

// driverFor returns the DistroDriver for the given distro.
// Returns ErrNoDriver if no driver is registered.
func driverFor(d Distro) (DistroDriver, error) {
	drv, ok := drivers[driverKey(d)]
	if !ok {
		return nil, &ErrNoDriver{D: d}
	}
	return drv, nil
}

// detectDistro inspects filesystem markers inside root to identify the OS
// family and major version. Returns an error if detection fails.
//
// Heuristics (checked in order):
//  1. /etc/debian_version present → Debian family; check os-release for Ubuntu.
//  2. /etc/redhat-release present → RHEL/Rocky/Alma family; parse major from os-release.
//  3. /etc/os-release generic fallback.
func detectDistro(root string) (Distro, error) {
	// Ubuntu / Debian: /etc/debian_version is present on all Debian derivatives.
	if fileExists(distroPath(root, "etc/debian_version")) {
		osRel, err := readOSRelease(root)
		if err == nil {
			id := osRel["ID"]
			verID := osRel["VERSION_ID"]
			if id == "ubuntu" {
				major := parseMajorVersion(verID)
				if major > 0 {
					return Distro{Family: "ubuntu", Major: major}, nil
				}
			}
			if id == "debian" {
				major := parseMajorVersion(verID)
				if major > 0 {
					return Distro{Family: "debian", Major: major}, nil
				}
			}
		}
		// Generic Debian (no os-release or unrecognised) — major 0; driverFor
		// returns ErrNoDriver for unknown majors.
		return Distro{Family: "debian", Major: 0}, nil
	}

	// RHEL/Rocky/Alma: /etc/redhat-release is present on all EL derivatives.
	if fileExists(distroPath(root, "etc/redhat-release")) {
		osRel, _ := readOSRelease(root)
		major := parseMajorVersion(osRel["VERSION_ID"])
		if major == 0 {
			major = parseRHELMajorFromFile(distroPath(root, "etc/redhat-release"))
		}
		if major == 0 {
			major = 9 // conservative default for unrecognised EL releases
		}
		return Distro{Family: "el", Major: major}, nil
	}

	// Generic os-release fallback for distros that don't write the above markers.
	osRel, err := readOSRelease(root)
	if err != nil {
		return Distro{}, &ErrDistroUnknown{Root: root}
	}
	id := osRel["ID"]
	major := parseMajorVersion(osRel["VERSION_ID"])
	switch id {
	case "ubuntu":
		return Distro{Family: "ubuntu", Major: major}, nil
	case "rhel", "rocky", "almalinux", "centos", "fedora":
		return Distro{Family: "el", Major: major}, nil
	case "sles", "sle_hpc":
		return Distro{Family: "sles", Major: major}, nil
	case "debian":
		return Distro{Family: "debian", Major: major}, nil
	}
	// Check ID_LIKE for SLES variants (e.g. SLES for SAP sets ID=sles_sap but
	// ID_LIKE=sles).
	idLike := osRel["ID_LIKE"]
	for _, like := range strings.Fields(idLike) {
		switch like {
		case "sles":
			return Distro{Family: "sles", Major: major}, nil
		case "debian":
			return Distro{Family: "debian", Major: major}, nil
		}
	}
	return Distro{}, &ErrDistroUnknown{Root: root}
}

// ErrNoDriver is returned when no DistroDriver is registered for the detected distro.
type ErrNoDriver struct {
	D Distro
}

func (e *ErrNoDriver) Error() string {
	return fmt.Sprintf("no DistroDriver registered for %s/%d", e.D.Family, e.D.Major)
}

// ErrDistroUnknown is returned when detectDistro cannot identify the OS family.
type ErrDistroUnknown struct {
	Root string
}

func (e *ErrDistroUnknown) Error() string {
	return "cannot detect distro family in root " + e.Root
}

// distroPath joins root and a forward-slash-separated relative path.
// The result is safe to use with os.Stat and os.ReadFile.
func distroPath(root, rel string) string {
	if strings.HasSuffix(root, "/") {
		return root + rel
	}
	return root + "/" + rel
}

// readOSRelease parses /etc/os-release inside root into a key→value map.
// Keys are returned upper-cased as written in os-release (e.g. "ID", "VERSION_ID").
// Values have surrounding double-quotes stripped.
func readOSRelease(root string) (map[string]string, error) {
	data, err := os.ReadFile(distroPath(root, "etc/os-release"))
	if err != nil {
		return nil, fmt.Errorf("readOSRelease: %w", err)
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := line[:idx]
		val := line[idx+1:]
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		m[key] = val
	}
	return m, nil
}

// parseRHELMajorFromFile reads the first line of a redhat-release-style file
// and returns the major version integer.
// Example: "Rocky Linux release 9.5 (Blue Onyx)" → 9.
func parseRHELMajorFromFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	// First line only; scan for " N." or " N " or " N\n".
	line := strings.SplitN(string(data), "\n", 2)[0]
	for i := 0; i < len(line)-1; i++ {
		if line[i] == ' ' && line[i+1] >= '0' && line[i+1] <= '9' {
			j := i + 1
			for j < len(line) && line[j] >= '0' && line[j] <= '9' {
				j++
			}
			if j == len(line) || line[j] == '.' || line[j] == ' ' {
				return parseDecimalInt(line[i+1 : j])
			}
		}
	}
	return 0
}

// parseMajorVersion extracts the leading decimal integer from a version string.
// "9.5" → 9, "24.04" → 24, "10" → 10, "" → 0.
func parseMajorVersion(v string) int {
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	return parseDecimalInt(v[:end])
}

// parseDecimalInt parses a base-10 integer string; returns 0 on any error.
func parseDecimalInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
