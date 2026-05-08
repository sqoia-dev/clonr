package clientd

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// heartbeatServices is the whitelist of systemd services checked on every heartbeat.
// These are the services most relevant on HPC compute nodes.
var heartbeatServices = []string{
	"sssd",
	"munge",
	"slurmd",
	"slurmctld",
	"sshd",
	"chronyd",
}

// collectHeartbeat reads system metrics from /proc and systemctl,
// returning a populated HeartbeatPayload.
func collectHeartbeat(version string) HeartbeatPayload {
	hb := HeartbeatPayload{
		ClientdVersion: version,
	}

	// /proc/uptime — first field is uptime in seconds.
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
				hb.UptimeSeconds = v
			}
		}
	}

	// /proc/loadavg — "load1 load5 load15 running/total lastpid"
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
				hb.Load1 = v
			}
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				hb.Load5 = v
			}
			if v, err := strconv.ParseFloat(fields[2], 64); err == nil {
				hb.Load15 = v
			}
		}
	}

	// /proc/meminfo — scan for MemTotal and MemAvailable.
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				hb.MemTotalKB = parseMemInfoKB(line)
			} else if strings.HasPrefix(line, "MemAvailable:") {
				hb.MemAvailKB = parseMemInfoKB(line)
			}
		}
		f.Close()
	}

	// /proc/sys/kernel/osrelease — kernel version.
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		hb.KernelVersion = strings.TrimSpace(string(data))
	}

	// Disk usage — call syscall.Statfs on key mount points.
	for _, mp := range []string{"/", "/var", "/tmp", "/home"} {
		var st syscall.Statfs_t
		if err := syscall.Statfs(mp, &st); err == nil {
			total := int64(st.Blocks) * int64(st.Bsize)
			free := int64(st.Bavail) * int64(st.Bsize)
			hb.DiskUsage = append(hb.DiskUsage, DiskUsage{
				MountPoint: mp,
				TotalBytes: total,
				UsedBytes:  total - free,
			})
		}
	}

	// Service status — check each whitelisted service via `systemctl is-active`.
	for _, svc := range heartbeatServices {
		st := checkService(svc)
		hb.Services = append(hb.Services, st)
	}

	// fix/v0.1.22-ldap-reverify: LDAP health snapshot piggybacks on every
	// heartbeat so the server can keep node_configs.ldap_ready current.
	// The probe is bounded by ldapProbeTimeout (5 s) so it cannot delay the
	// 60 s heartbeat cadence even if sssd is wedged.
	hb.LDAPHealth = collectLDAPHealth()

	return hb
}

// parseMemInfoKB extracts the numeric KB value from a /proc/meminfo line
// such as "MemTotal:       16384 kB".
func parseMemInfoKB(line string) int64 {
	// Format: "KeyName:    VALUE kB"
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		if v, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			return v
		}
	}
	return 0
}

// checkService runs `systemctl is-active <name>` and returns the ServiceStatus.
func checkService(name string) ServiceStatus {
	out, err := exec.Command("systemctl", "is-active", name).Output()
	state := strings.TrimSpace(string(out))
	if state == "" {
		if err != nil {
			state = "unknown"
		} else {
			state = "inactive"
		}
	}
	return ServiceStatus{
		Name:   name,
		Active: state == "active",
		State:  state,
	}
}
