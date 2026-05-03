package hardware

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// CPU represents a physical CPU socket. Multiple logical processors sharing
// the same physical id are merged into one CPU entry.
type CPU struct {
	Model   string
	Cores   int
	Threads int
	MHz     float64
	Flags   []string
}

// DiscoverCPUs parses /proc/cpuinfo and returns one entry per physical CPU.
func DiscoverCPUs() ([]CPU, error) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return nil, fmt.Errorf("hardware/cpu: open /proc/cpuinfo: %w", err)
	}
	defer f.Close()
	return parseCPUInfo(f)
}

// parseCPUInfo is the pure parsing logic, separated so tests can supply
// arbitrary content without touching the filesystem.
func parseCPUInfo(r io.Reader) ([]CPU, error) {
	type rawProcessor struct {
		physicalID string
		model      string
		cores      int
		threads    int
		mhz        float64
		flags      []string
	}

	var procs []rawProcessor
	var cur rawProcessor

	flush := func() {
		if cur.model != "" {
			procs = append(procs, cur)
		}
		cur = rawProcessor{}
	}

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch key {
		case "physical id":
			cur.physicalID = val
		case "model name":
			cur.model = val
		case "cpu cores":
			cur.cores, _ = strconv.Atoi(val)
		case "siblings":
			// siblings = logical CPUs per socket = threads
			cur.threads, _ = strconv.Atoi(val)
		case "cpu MHz":
			cur.mhz, _ = strconv.ParseFloat(val, 64)
		case "flags":
			cur.flags = strings.Fields(val)
		}
	}
	flush()

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("hardware/cpu: scan /proc/cpuinfo: %w", err)
	}

	// Deduplicate by physical id — keep the first occurrence of each socket.
	seen := map[string]bool{}
	var cpus []CPU
	for _, p := range procs {
		if seen[p.physicalID] {
			continue
		}
		seen[p.physicalID] = true
		cpus = append(cpus, CPU{
			Model:   p.model,
			Cores:   p.cores,
			Threads: p.threads,
			MHz:     p.mhz,
			Flags:   p.flags,
		})
	}

	// Single-socket systems often omit "physical id". If nothing was keyed,
	// treat the first processor block as one CPU.
	if len(cpus) == 0 && len(procs) > 0 {
		p := procs[0]
		cpus = append(cpus, CPU{
			Model:   p.model,
			Cores:   p.cores,
			Threads: p.threads,
			MHz:     p.mhz,
			Flags:   p.flags,
		})
	}

	return cpus, nil
}
