package hardware

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// MemoryInfo holds key memory metrics parsed from /proc/meminfo.
// All values are in kilobytes, matching the kernel's native unit.
type MemoryInfo struct {
	TotalKB     uint64
	AvailableKB uint64
	SwapTotalKB uint64
}

// DiscoverMemory parses /proc/meminfo and returns memory statistics.
func DiscoverMemory() (*MemoryInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("hardware/memory: open /proc/meminfo: %w", err)
	}
	defer f.Close()
	return parseMemInfo(f)
}

// parseMemInfo is the pure parsing logic for /proc/meminfo content.
func parseMemInfo(r io.Reader) (*MemoryInfo, error) {
	info := &MemoryInfo{}
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		line := scanner.Text()
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		// Values are like "16384 kB" — split off the unit before parsing.
		fields := strings.Fields(strings.TrimSpace(val))
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}

		switch key {
		case "MemTotal":
			info.TotalKB = n
		case "MemAvailable":
			info.AvailableKB = n
		case "SwapTotal":
			info.SwapTotalKB = n
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("hardware/memory: scan /proc/meminfo: %w", err)
	}

	return info, nil
}
