package ipmi

import (
	"testing"
)

// fixtureSelList is a representative `ipmitool sel list` output with entries
// spanning info, warning, and critical severities plus an empty-line edge case.
const fixtureSelList = `   1 | 04/14/2024 | 12:00:00 | Power Supply #0x51 | Power Supply input lost (AC/DC) | Asserted
   2 | 04/14/2024 | 12:01:05 | Temperature #0x30 | Upper Critical going high | Asserted
   3 | 04/14/2024 | 12:02:10 | Fan #0x41 | Upper Non-critical going high | Asserted
   4 | 04/14/2024 | 12:03:15 | System Event #0x83 | OEM System boot event | Asserted
   5 | 04/14/2024 | 12:04:20 | Memory #0x60 | Correctable ECC | Asserted
   6 | 04/14/2024 | 12:05:30 | Processor #0x01 | Non-Recoverable Error | Asserted
`

// fixtureSelListEmpty mirrors ipmitool output when the SEL has no entries.
const fixtureSelListEmpty = `SEL has no entries`

func TestParseSEL_Standard(t *testing.T) {
	entries := parseSEL(fixtureSelList)
	if len(entries) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(entries))
	}

	// Entry 1: power supply event — should be critical (AC/DC lost maps to "failure")
	// via "Power Supply input lost"
	e0 := entries[0]
	if e0.ID != "1" {
		t.Errorf("entry[0] ID: want 1, got %q", e0.ID)
	}
	if e0.Date != "04/14/2024" {
		t.Errorf("entry[0] Date: want 04/14/2024, got %q", e0.Date)
	}
	if e0.Sensor != "Power Supply #0x51" {
		t.Errorf("entry[0] Sensor: want 'Power Supply #0x51', got %q", e0.Sensor)
	}

	// Entry 2: "Upper Critical" → critical
	if entries[1].Severity != SELSeverityCritical {
		t.Errorf("entry[1] (Upper Critical) severity: want critical, got %q", entries[1].Severity)
	}

	// Entry 3: "Upper Non-critical" → warn
	if entries[2].Severity != SELSeverityWarn {
		t.Errorf("entry[2] (Upper Non-critical) severity: want warn, got %q", entries[2].Severity)
	}

	// Entry 4: OEM boot event → info (no critical/warn keyword)
	if entries[3].Severity != SELSeverityInfo {
		t.Errorf("entry[3] (OEM boot event) severity: want info, got %q", entries[3].Severity)
	}

	// Entry 5: "Correctable ECC" → warn (correctable maps to warning tier)
	if entries[4].Severity != SELSeverityWarn {
		t.Errorf("entry[4] (Correctable ECC) severity: want warn, got %q", entries[4].Severity)
	}

	// Entry 6: "Non-Recoverable" → critical
	if entries[5].Severity != SELSeverityCritical {
		t.Errorf("entry[5] (Non-Recoverable) severity: want critical, got %q", entries[5].Severity)
	}
}

func TestParseSEL_Empty(t *testing.T) {
	entries := parseSEL(fixtureSelListEmpty)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty SEL, got %d", len(entries))
	}
}

func TestParseSEL_BlankLines(t *testing.T) {
	input := "\n\n   1 | 04/14/2024 | 12:00:00 | Fan #0x41 | Fan failure | Asserted\n\n"
	entries := parseSEL(input)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Severity != SELSeverityCritical {
		t.Errorf("severity: want critical, got %q", entries[0].Severity)
	}
}

func TestParseSEL_MalformedLines(t *testing.T) {
	// Lines with fewer than 5 pipe-separated fields must be silently skipped.
	input := "not a valid line\n   1 | 04/14/2024 | 12:00:00 | Fan | Fan Ok\n"
	entries := parseSEL(input)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (malformed line skipped), got %d", len(entries))
	}
}

func TestSELHead(t *testing.T) {
	entries := parseSEL(fixtureSelList)

	head3 := SELHead(entries, 3)
	if len(head3) != 3 {
		t.Errorf("SELHead(3): want 3, got %d", len(head3))
	}
	if head3[0].ID != "1" {
		t.Errorf("SELHead[0] ID: want 1, got %q", head3[0].ID)
	}
	if head3[2].ID != "3" {
		t.Errorf("SELHead[2] ID: want 3, got %q", head3[2].ID)
	}

	// Requesting more than available returns all.
	headAll := SELHead(entries, 100)
	if len(headAll) != len(entries) {
		t.Errorf("SELHead(100) with 6 entries: want 6, got %d", len(headAll))
	}

	// n=0 returns all unchanged.
	head0 := SELHead(entries, 0)
	if len(head0) != len(entries) {
		t.Errorf("SELHead(0): want %d, got %d", len(entries), len(head0))
	}
}

func TestSELTail(t *testing.T) {
	entries := parseSEL(fixtureSelList)

	tail2 := SELTail(entries, 2)
	if len(tail2) != 2 {
		t.Errorf("SELTail(2): want 2, got %d", len(tail2))
	}
	if tail2[0].ID != "5" {
		t.Errorf("SELTail[0] ID: want 5, got %q", tail2[0].ID)
	}
	if tail2[1].ID != "6" {
		t.Errorf("SELTail[1] ID: want 6, got %q", tail2[1].ID)
	}

	// Requesting more than available returns all.
	tailAll := SELTail(entries, 100)
	if len(tailAll) != len(entries) {
		t.Errorf("SELTail(100) with 6 entries: want 6, got %d", len(tailAll))
	}
}

func TestSELFilter(t *testing.T) {
	entries := parseSEL(fixtureSelList)

	// "info" (or empty) returns everything.
	all := SELFilter(entries, SELSeverityInfo)
	if len(all) != len(entries) {
		t.Errorf("SELFilter(info): want %d, got %d", len(entries), len(all))
	}
	empty := SELFilter(entries, "")
	if len(empty) != len(entries) {
		t.Errorf("SELFilter(''): want %d, got %d", len(entries), len(empty))
	}

	// "warn" returns warn + critical entries only.
	warns := SELFilter(entries, SELSeverityWarn)
	for _, e := range warns {
		if e.Severity == SELSeverityInfo {
			t.Errorf("SELFilter(warn) contains info entry: %+v", e)
		}
	}

	// "critical" returns only critical entries.
	crits := SELFilter(entries, SELSeverityCritical)
	for _, e := range crits {
		if e.Severity != SELSeverityCritical {
			t.Errorf("SELFilter(critical) contains non-critical entry: %+v", e)
		}
	}
	if len(crits) == 0 {
		t.Error("SELFilter(critical): expected at least one critical entry in fixture")
	}

	// Unknown level falls through to returning all.
	unknown := SELFilter(entries, "bogus")
	if len(unknown) != len(entries) {
		t.Errorf("SELFilter(bogus): want %d, got %d", len(entries), len(unknown))
	}
}

func TestClassifySELSeverity(t *testing.T) {
	cases := []struct {
		sensor, event, direction string
		want                     string
	}{
		{"Temperature", "Upper Critical going high", "Asserted", SELSeverityCritical},
		{"Memory", "Uncorrectable ECC", "Asserted", SELSeverityCritical},
		{"Processor", "Non-Recoverable Error", "Asserted", SELSeverityCritical},
		{"Fan", "Fan failure", "Asserted", SELSeverityCritical},
		{"Fan", "Upper Non-critical going high", "Asserted", SELSeverityWarn},
		{"Memory", "Correctable ECC", "Asserted", SELSeverityWarn},
		{"Power Unit", "Power Unit degraded", "Asserted", SELSeverityWarn},
		{"System Event", "OEM boot event", "Asserted", SELSeverityInfo},
		{"Button", "Power button pressed", "Asserted", SELSeverityInfo},
	}
	for _, tc := range cases {
		got := classifySELSeverity(tc.sensor, tc.event, tc.direction)
		if got != tc.want {
			t.Errorf("classifySELSeverity(%q, %q, %q): want %q, got %q",
				tc.sensor, tc.event, tc.direction, tc.want, got)
		}
	}
}
