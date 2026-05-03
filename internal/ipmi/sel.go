package ipmi

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SELEntry represents a single System Event Log record from ipmitool sel list.
//
// Standard ipmitool sel list output format:
//
//	1 | 04/14/2024 | 12:34:56 | Temperature #0x30 | Upper Critical going high | Asserted
type SELEntry struct {
	ID       string    `json:"id"`
	Date     string    `json:"date"`
	Time     string    `json:"time"`
	Sensor   string    `json:"sensor"`
	Event    string    `json:"event"`
	Severity string    `json:"severity"`  // "info", "warn", "critical"
	Raw      string    `json:"raw"`       // full unparsed line for diagnostics
	Parsed   time.Time `json:"timestamp"` // best-effort parsed timestamp; zero when unparseable
}

// SELSeverity constants for --level filtering.
const (
	SELSeverityInfo     = "info"
	SELSeverityWarn     = "warn"
	SELSeverityCritical = "critical"
)

// GetSEL runs `ipmitool sel list` and returns all parsed entries.
// Returns an empty slice (not an error) when the SEL has no entries.
func (c *Client) GetSEL(ctx context.Context) ([]SELEntry, error) {
	out, err := c.run(ctx, "sel", "list")
	if err != nil {
		// ipmitool exits 0 with a "SEL has no entries" message; only treat
		// non-zero exit as an error when the output doesn't indicate empty SEL.
		if strings.Contains(out, "SEL has no entries") {
			return []SELEntry{}, nil
		}
		return nil, fmt.Errorf("ipmi sel list: %w", err)
	}
	if strings.TrimSpace(out) == "" || strings.Contains(out, "SEL has no entries") {
		return []SELEntry{}, nil
	}
	return parseSEL(out), nil
}

// ClearSEL runs `ipmitool sel clear` to erase all SEL entries on the BMC.
func (c *Client) ClearSEL(ctx context.Context) error {
	_, err := c.run(ctx, "sel", "clear")
	if err != nil {
		return fmt.Errorf("ipmi sel clear: %w", err)
	}
	return nil
}

// parseSEL parses the output of `ipmitool sel list`.
//
// Each non-empty line has the format:
//
//	<id> | <date> | <time> | <sensor> | <event> | <direction>
//
// ipmitool uses " | " as the field separator. The direction field
// ("Asserted", "Deasserted") and various event strings determine severity.
func parseSEL(out string) []SELEntry {
	var entries []SELEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry, ok := parseSELLine(line)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// parseSELLine parses a single ipmitool sel list line.
// Returns (entry, true) on success, (zero, false) when the line does not
// match the expected format.
func parseSELLine(line string) (SELEntry, bool) {
	parts := strings.Split(line, " | ")
	if len(parts) < 5 {
		// Some entries may have fewer columns (e.g. OEM records); skip them.
		return SELEntry{}, false
	}

	id := strings.TrimSpace(parts[0])
	date := strings.TrimSpace(parts[1])
	ts := strings.TrimSpace(parts[2])
	sensor := strings.TrimSpace(parts[3])
	event := strings.TrimSpace(parts[4])
	direction := ""
	if len(parts) >= 6 {
		direction = strings.TrimSpace(parts[5])
	}

	sev := classifySELSeverity(sensor, event, direction)

	// Best-effort timestamp parse.
	var parsed time.Time
	if date != "" && ts != "" && date != "PostInit" {
		t, err := time.Parse("01/02/2006 15:04:05", date+" "+ts)
		if err == nil {
			parsed = t
		}
	}

	return SELEntry{
		ID:       id,
		Date:     date,
		Time:     ts,
		Sensor:   sensor,
		Event:    event,
		Severity: sev,
		Raw:      line,
		Parsed:   parsed,
	}, true
}

// classifySELSeverity maps ipmitool SEL event strings to info/warn/critical.
// The classification follows IPMI spec §41 event type codes and the sensor
// reading type keywords ipmitool emits.
//
// Priority order:
//  1. "non-critical" → warn  (must come before "critical" check; "non-critical"
//     contains "critical" as a substring)
//  2. other critical keywords → critical
//  3. warn keywords → warn
//  4. default → info
func classifySELSeverity(sensor, event, direction string) string {
	combined := strings.ToLower(sensor + " " + event + " " + direction)

	// Tier 1: non-critical variants (warn) — checked first to prevent the
	// "non-critical" substring from accidentally matching "critical" below.
	// Note: avoid short abbreviations like "unc" that also appear in "uncorrectable".
	nonCriticalKeywords := []string{
		"upper non-critical", "lower non-critical",
		"non-critical", // catches standalone occurrences
	}
	for _, kw := range nonCriticalKeywords {
		if strings.Contains(combined, kw) {
			return SELSeverityWarn
		}
	}

	// Tier 2: critical indicators.
	// "non-critical" occurrences are already handled above; remaining matches of
	// "critical" here are genuinely critical (upper/lower critical, stand-alone).
	criticalKeywords := []string{
		"upper critical", "lower critical", "ucr", "lcr",
		"critical",
		"non-recoverable", "hard reset",
		"mce", "uncorrectable", "fatal", "failure", "failed",
		"bus error", "power failure", "voltage failure",
		"drive fault", "raid degraded", "raid failed",
	}
	for _, kw := range criticalKeywords {
		if strings.Contains(combined, kw) {
			return SELSeverityCritical
		}
	}

	// Tier 3: warning indicators.
	warnKeywords := []string{
		"warning", "degraded", "predictive failure",
		"correctable", "corrected",
		"processor thermal trip", "fan redundancy lost",
		"high", "low",
	}
	for _, kw := range warnKeywords {
		if strings.Contains(combined, kw) {
			return SELSeverityWarn
		}
	}

	return SELSeverityInfo
}

// SELHead returns the first n entries from entries (or all if len < n).
func SELHead(entries []SELEntry, n int) []SELEntry {
	if n <= 0 || len(entries) == 0 {
		return entries
	}
	if n >= len(entries) {
		return entries
	}
	return entries[:n]
}

// SELTail returns the last n entries from entries (or all if len < n).
func SELTail(entries []SELEntry, n int) []SELEntry {
	if n <= 0 || len(entries) == 0 {
		return entries
	}
	if n >= len(entries) {
		return entries
	}
	return entries[len(entries)-n:]
}

// SELFilter filters entries by minimum severity level.
// Accepted levels: "info" (all), "warn" (warn + critical), "critical" (critical only).
// An empty level string is treated as "info".
func SELFilter(entries []SELEntry, level string) []SELEntry {
	switch strings.ToLower(level) {
	case "", SELSeverityInfo:
		return entries
	case SELSeverityWarn:
		var out []SELEntry
		for _, e := range entries {
			if e.Severity == SELSeverityWarn || e.Severity == SELSeverityCritical {
				out = append(out, e)
			}
		}
		return out
	case SELSeverityCritical:
		var out []SELEntry
		for _, e := range entries {
			if e.Severity == SELSeverityCritical {
				out = append(out, e)
			}
		}
		return out
	default:
		return entries
	}
}
