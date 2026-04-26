package server

import (
	"fmt"
	"syscall"
)

// diskUsagePct returns the fractional disk usage (0.0–1.0) for the filesystem
// containing dir. Returns an error if the stat call fails.
func diskUsagePct(dir string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	if total == 0 {
		return 0, nil
	}
	avail := float64(stat.Bavail) * float64(stat.Bsize)
	used := total - avail
	return used / total, nil
}
