package hardware

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NIC represents a network interface discovered from /sys/class/net/.
type NIC struct {
	Name   string // kernel interface name, e.g. eth0, eno1, ib0
	MAC    string // hardware address from /sys/class/net/<name>/address
	Speed  string // link speed from /sys/class/net/<name>/speed (Mb/s as string)
	State  string // operational state: up, down, unknown
	Driver string // driver name from /sys/class/net/<name>/device/driver symlink
}

// DiscoverNICs enumerates network interfaces via /sys/class/net/ and reads
// per-interface attributes from sysfs. Virtual and loopback interfaces are
// included; callers can filter by Name (e.g. skip "lo").
func DiscoverNICs() ([]NIC, error) {
	const netDir = "/sys/class/net"

	entries, err := os.ReadDir(netDir)
	if err != nil {
		return nil, fmt.Errorf("hardware/network: readdir %s: %w", netDir, err)
	}

	var nics []NIC
	for _, e := range entries {
		name := e.Name()
		base := filepath.Join(netDir, name)

		nic := NIC{Name: name}
		nic.MAC = readSysStr(filepath.Join(base, "address"))
		nic.State = readSysStr(filepath.Join(base, "operstate"))
		nic.Speed = readSysStr(filepath.Join(base, "speed"))

		// Driver is the final path component of the "driver" symlink inside
		// the device directory. Missing on virtual interfaces — that is fine.
		driverLink := filepath.Join(base, "device", "driver")
		if target, err := os.Readlink(driverLink); err == nil {
			nic.Driver = filepath.Base(target)
		}

		nics = append(nics, nic)
	}

	return nics, nil
}

// readSysStr reads a sysfs attribute file and returns its trimmed content.
// Returns an empty string on any error (missing file, permission denied, etc.).
func readSysStr(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
