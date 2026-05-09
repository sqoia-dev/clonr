package main

// canonical_phase_test.go — Codex post-ship review issue #12.
//
// canonicalPhase collapses the deploy progressFn's phase string to a
// closed enum before the value is forwarded to remoteWriter.SetPhase.
// Without this collapse, every ad-hoc deployer message would land as a
// high-cardinality phase tag in the upstream stream-log channel.

import "testing"

func TestCanonicalPhase_ExactCanonicalsPassThrough(t *testing.T) {
	canonicals := []string{
		"hardware", "register", "bios", "wait-for-assign", "image-fetch",
		"preflight", "multicast", "partitioning", "formatting",
		"downloading", "extracting", "finalizing", "deploy-complete",
	}
	for _, p := range canonicals {
		t.Run(p, func(t *testing.T) {
			if got := canonicalPhase(p); got != p {
				t.Errorf("canonicalPhase(%q) = %q, want %q", p, got, p)
			}
		})
	}
}

// TestCanonicalPhase_AdHocStringsCollapseToParent locks down the
// reason this fix exists: deploy backends emit ad-hoc text like
// "extract complete" or "downloading retry attempt 3" or "partition
// table updated", which the previous code passed verbatim to
// SetPhase.  We collapse to a known parent or drop entirely.
func TestCanonicalPhase_AdHocStringsCollapseToParent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Direct synonyms.
		{"extract complete", "extracting"},
		{"Extracting filesystem", "extracting"},
		{"download starting", "downloading"},
		{"fetching image blob", "downloading"},
		{"partition rescan", "partitioning"},
		{"formatting /dev/nvme0n1p2", "formatting"},
		{"finalizing chroot", "finalizing"},
		// British spelling.
		{"finalising chroot", "finalizing"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := canonicalPhase(tc.in); got != tc.want {
				t.Errorf("canonicalPhase(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCanonicalPhase_EmptyAndUnknownReturnEmpty — caller treats "" as
// "leave the upstream phase tag alone".
func TestCanonicalPhase_EmptyAndUnknownReturnEmpty(t *testing.T) {
	for _, in := range []string{
		"",
		"retry attempt 3",            // matches retry → "" (no transition)
		"reconnecting to multicast",  // reconnect → ""
		"waiting for goblet of fire", // genuine garbage
	} {
		if got := canonicalPhase(in); got != "" {
			t.Errorf("canonicalPhase(%q) = %q, want empty", in, got)
		}
	}
}
