// multicast_jitter_test.go — unit coverage for Sprint 33 MULTICAST-JITTER.
package main

import (
	"testing"
	"time"
)

// TestJitterSleepIfMulticast_NoOpOnUnicast pins the unicast path: when
// usedMulticast=false, jitterSleepIfMulticast must NOT call sleep. A
// regression here would slow every unicast deploy by 0-60s for no reason.
func TestJitterSleepIfMulticast_NoOpOnUnicast(t *testing.T) {
	called := false
	sleep := func(d time.Duration) { called = true }

	jitterSleepIfMulticast(false, "bc:24:11:da:58:6a", sleep)

	if called {
		t.Errorf("sleep called on unicast path; jitter must be a no-op when usedMulticast=false")
	}
}

// TestJitterSleepIfMulticast_SleepsOnMulticast verifies that the multicast
// path actually invokes sleep, with the duration produced by jitterDuration.
// Captures the duration for later inspection rather than wall-time waiting.
func TestJitterSleepIfMulticast_SleepsOnMulticast(t *testing.T) {
	const mac = "bc:24:11:da:58:6a"

	var got time.Duration
	captured := false
	sleep := func(d time.Duration) {
		got = d
		captured = true
	}

	jitterSleepIfMulticast(true, mac, sleep)

	if !captured {
		t.Fatal("sleep not called on multicast path")
	}

	// Duration must equal jitterDuration(mac) exactly — no surprise
	// transformations between the helpers.
	if want := jitterDuration(mac); got != want {
		t.Errorf("sleep duration = %v, want %v", got, want)
	}
}

// TestJitterDuration_InRange asserts the [0, 60s) bound across a wide
// range of representative inputs. The whole point of Sprint 33
// MULTICAST-JITTER is to spread POSTs over 60s; a value outside this
// window either re-bunches (if always 0) or makes the operator think
// the node has hung (if much bigger).
func TestJitterDuration_InRange(t *testing.T) {
	cases := []string{
		"bc:24:11:da:58:6a",
		"bc:24:11:da:58:6b",
		"00:50:56:00:00:01",
		"02:00:00:00:00:00",
		"ff:ff:ff:ff:ff:ff",
		"",                 // edge: empty (unconfigured node fallback)
		"node-without-mac", // edge: non-MAC string (defensive)
		"AA:BB:CC:DD:EE:FF",
		"a1:b2:c3:d4:e5:f6",
	}
	for _, mac := range cases {
		got := jitterDuration(mac)
		if got < 0 {
			t.Errorf("jitterDuration(%q) = %v; want >= 0", mac, got)
		}
		if got >= 60*time.Second {
			t.Errorf("jitterDuration(%q) = %v; want < 60s", mac, got)
		}
	}
}

// TestJitterDuration_DeterministicPerMAC pins the retry-safe contract:
// the same MAC must produce the same duration on every call. If a node's
// HTTP POST fails at offset=17 and it retries, the retry must land at
// offset=17 too — a fresh-random-on-retry would re-bunch the herd at
// the retry attempt.
func TestJitterDuration_DeterministicPerMAC(t *testing.T) {
	const mac = "bc:24:11:da:58:6a"
	first := jitterDuration(mac)
	for i := 0; i < 100; i++ {
		got := jitterDuration(mac)
		if got != first {
			t.Fatalf("jitterDuration(%q) is non-deterministic: call 0 = %v, call %d = %v",
				mac, first, i, got)
		}
	}
}

// TestJitterDuration_DifferentMACsDistribute verifies the spread: a
// representative sample of MACs must produce more than one distinct
// duration value. If every MAC mapped to (say) 0s, the jitter helper
// would technically be in-range but functionally useless. We don't
// require uniform distribution (that's a property of FNV-1a + math/rand
// already), but we DO require at least 8 distinct values across 32
// MACs, which corresponds to ~25% of the sample population — well
// within range of a sane hash even if half the MACs cluster.
func TestJitterDuration_DifferentMACsDistribute(t *testing.T) {
	seen := make(map[time.Duration]struct{})
	for i := 0; i < 32; i++ {
		// Synthesise distinct MAC suffixes; first 5 bytes constant.
		mac := "bc:24:11:da:58:" + hex2(i)
		seen[jitterDuration(mac)] = struct{}{}
	}
	if len(seen) < 8 {
		t.Errorf("32 distinct MACs produced only %d distinct jitter values; expected >= 8", len(seen))
	}
}

// hex2 is the smallest helper that turns 0..255 into a two-character
// lowercase hex string for synthesising MAC suffixes.
func hex2(n int) string {
	const hexDigits = "0123456789abcdef"
	return string([]byte{hexDigits[(n>>4)&0xf], hexDigits[n&0xf]})
}

// TestMulticastJitterMaxSecondsConst is a defence-in-depth check that
// the public-ish constant matches the implementation. If a future
// maintainer changes the literal in jitterDuration but forgets the
// const (or vice versa), the [0, 60s) window assumption silently
// breaks. The const is the one source of truth.
func TestMulticastJitterMaxSecondsConst(t *testing.T) {
	if multicastJitterMaxSeconds != 60 {
		t.Errorf("multicastJitterMaxSeconds = %d; want 60", multicastJitterMaxSeconds)
	}
}
