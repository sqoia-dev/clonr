// Package clientd — LDAP health probe.
//
// fix/v0.1.22-ldap-reverify: verify-boot was the only path that ever wrote
// node_configs.ldap_ready, and it ran exactly once on first phone-home. If
// sssd happened to be slow/broken when verify-boot ran, the node was stuck
// at "LDAP Failed" forever even after sssd recovered. This file adds a
// recurring probe that piggybacks on the existing 60s heartbeat — every
// heartbeat carries a fresh LDAPHealth snapshot, the server records it, and
// the UI reflects current truth instead of a one-shot first-boot guess.
//
// The probe is intentionally cheap:
//  1. systemctl is-active sssd        — must be active or we stop here
//  2. sssctl domain-list              — discover the configured domain(s)
//  3. sssctl domain-status <domain>   — "Online status: Online" means LDAP is up
//
// No bind, no LDAP I/O from clientd itself: sssd already maintains the
// connection and "Online status" is a faithful summary. This avoids embedding
// LDAP credentials anywhere in clientd.
package clientd

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// LDAPHealthStatus is the per-node LDAP-readiness snapshot reported by clientd
// on every heartbeat. The server uses Configured + Active + Connected to flip
// node_configs.ldap_ready and uses Detail as the human-readable status string
// surfaced in the UI.
type LDAPHealthStatus struct {
	// Configured is true when sssd has at least one domain configured.
	// false means this node was deployed without LDAP client config.
	Configured bool `json:"configured"`
	// Active is true when systemctl reports sssd as active.
	Active bool `json:"active"`
	// Connected is true when sssctl domain-status reports "Online".
	// Only meaningful when Active==true.
	Connected bool `json:"connected"`
	// Domain is the sssd domain the probe checked. Empty when Configured==false.
	Domain string `json:"domain,omitempty"`
	// Detail is a human-readable summary suitable for direct display in the UI:
	//  "sssd online (cluster.local) — connected"
	//  "sssd active but domain offline: …"
	//  "sssd not active"
	//  "sssd not installed"
	//  "no sssd domains configured"
	Detail string `json:"detail"`
}

// ldapProbeTimeout caps the entire probe so a hung sssctl never delays the
// heartbeat path. 5 s is generous: sssctl talks to the local sssd unix socket;
// healthy responses are sub-second.
const ldapProbeTimeout = 5 * time.Second

// collectLDAPHealth runs the probe and returns a populated LDAPHealthStatus.
// Never returns nil — callers can attach the result directly to a heartbeat.
func collectLDAPHealth() *LDAPHealthStatus {
	ctx, cancel := context.WithTimeout(context.Background(), ldapProbeTimeout)
	defer cancel()
	return collectLDAPHealthCtx(ctx)
}

// collectLDAPHealthCtx is the testable form: tests inject a context with a
// short deadline to verify the early-exit paths.
func collectLDAPHealthCtx(ctx context.Context) *LDAPHealthStatus {
	out := &LDAPHealthStatus{}

	// Step 1: is sssd installed at all? `systemctl is-active sssd` returns
	// "inactive"/"unknown" for both "not installed" and "installed but stopped".
	// We disambiguate by checking the unit-file presence via list-unit-files —
	// but that's overkill: the verify-boot probe already used "not_installed"
	// when sssd was absent on the image. Here we just key off is-active.
	stateOut, _ := exec.CommandContext(ctx, "systemctl", "is-active", "sssd").Output()
	state := strings.TrimSpace(string(stateOut))
	if state != "active" {
		out.Active = false
		// Distinguish the common cases for the operator.
		if state == "" || state == "unknown" || state == "inactive" {
			// Look for the unit file to tell "not installed" from "stopped".
			ufOut, _ := exec.CommandContext(ctx, "systemctl", "list-unit-files", "sssd.service", "--no-legend").Output()
			if strings.TrimSpace(string(ufOut)) == "" {
				out.Detail = "sssd not installed"
				return out
			}
			out.Detail = "sssd not active (state=" + state + ")"
			return out
		}
		out.Detail = "sssd not active (state=" + state + ")"
		return out
	}
	out.Active = true

	// Step 2: discover the configured domain. Use sssctl which reads the live
	// daemon state so any reload picks up. One domain per cluster is the
	// clustr norm; if there are multiple we probe the first.
	domOut, err := exec.CommandContext(ctx, "sssctl", "domain-list").Output()
	if err != nil {
		out.Detail = "sssctl domain-list failed: " + err.Error()
		return out
	}
	domains := parseDomainList(string(domOut))
	if len(domains) == 0 {
		out.Configured = false
		out.Detail = "no sssd domains configured"
		return out
	}
	out.Configured = true
	out.Domain = domains[0]

	// Step 3: check the domain's online status. "Online status: Online" is
	// the marker we trust.  sssctl returns 0 even when offline — we have to
	// parse the output.
	statusOut, err := exec.CommandContext(ctx, "sssctl", "domain-status", out.Domain).Output()
	if err != nil {
		out.Detail = "sssctl domain-status failed: " + err.Error()
		return out
	}
	online, marker := parseDomainStatus(string(statusOut))
	out.Connected = online
	if online {
		out.Detail = "sssd online (" + out.Domain + ")"
	} else {
		// Surface the first informative line so the operator doesn't have to
		// SSH in to read it.  marker is the "Online status: …" line if found.
		if marker == "" {
			marker = firstNonEmptyLine(string(statusOut))
		}
		out.Detail = "sssd active but domain offline: " + marker
	}
	return out
}

// parseDomainList extracts domain names from `sssctl domain-list` output.
// The command prints one domain per line; blank lines and "Not running" are
// already handled by checking `is-active sssd` upstream.
func parseDomainList(out string) []string {
	var domains []string
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		// Some sssctl builds emit headers like "Domain list:" — skip lines
		// that contain whitespace (real domain names never do).
		if strings.ContainsAny(s, " \t") {
			continue
		}
		domains = append(domains, s)
	}
	return domains
}

// parseDomainStatus scans `sssctl domain-status <domain>` output for the
// "Online status: Online" marker. Returns (online, the matched line) so the
// caller can include the source line in the detail string when offline.
func parseDomainStatus(out string) (bool, string) {
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		// Match "Online status: Online" / "Online status: Offline"
		if strings.HasPrefix(strings.ToLower(s), "online status:") {
			return strings.Contains(strings.ToLower(s), "online status: online"), s
		}
	}
	return false, ""
}

// firstNonEmptyLine returns the first non-empty trimmed line of s, or s itself
// if every line is blank.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}
