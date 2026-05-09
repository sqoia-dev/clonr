// multicast_jitter.go — Sprint 33 MULTICAST-JITTER.
//
// When 256 nodes finish a multicast image transfer in the same one-second
// window, every node POSTs /deploy-complete simultaneously and the server's
// request rate spikes to 256/s. Each /deploy-complete handler does a non-
// trivial DB write (state transition + reimage_pending clear + audit
// row), so the spike spills into request latency and, on the small
// pkg.sqoia.dev tier, was observed to time some POSTs out — which the
// node code retries 3x with backoff and eventually classifies as a
// deploy-complete failure even though the deploy itself succeeded.
//
// Fix: each multicast-completed node sleeps a deterministic 0-60s before
// posting /deploy-complete. The seed is the node's primary MAC, so retries
// land on the same value (a transient HTTP failure at second 17 retries at
// second 17, not at a fresh random-on-restart offset that would re-bunch
// the herd). Different MACs map to different values, spreading the load.
package main

import (
	"hash/fnv"
	"math/rand"
	"time"
)

// multicastJitterMaxSeconds is the upper bound for the jitter sleep.
//
// 60s is large enough to flatten 256 nodes to ~5/s assuming uniform
// distribution, and small enough that the operator does not interpret the
// gap between "image written" and "deploy-complete reported" as a hang.
// The bound is also exposed as a const so tests can assert it.
const multicastJitterMaxSeconds = 60

// jitterSleepIfMulticast is the helper called from runAutoDeployMode after
// multicast image delivery succeeds, before POST /deploy-complete.
//
// When usedMulticast is false (unicast HTTP path) the function is a no-op
// — there is no thundering herd to suppress because unicast deploys are
// already serialized by the per-blob HTTP byte-rate limit at the server.
//
// The sleep duration is deterministic in primaryMAC: the same node reaches
// the same offset every time, which is critical for retries (a node that
// failed at offset=17 retries at offset=17, not at a fresh random offset
// that would re-introduce bunching at the retry attempt). Different MACs
// map to different offsets, spreading the load across the full window.
//
// The sleep is delegated through the `sleep` parameter so tests can inject
// a recorder that captures the duration without actually sleeping.
func jitterSleepIfMulticast(usedMulticast bool, primaryMAC string, sleep func(time.Duration)) {
	if !usedMulticast {
		return
	}
	d := jitterDuration(primaryMAC)
	sleep(d)
}

// jitterDuration computes the 0-60s offset for a given primary MAC. It is
// the pure-function core of the jitter logic, exposed for direct testing.
//
// We use FNV-1a as a cheap, well-distributed hash (much better than Go's
// default rand-source seed-from-int64 for short MAC strings — using
// rand.NewSource(int64(crc32(mac))) was tried and clusters values around
// the low end). FNV-1a's 32-bit output is then used to seed a math/rand
// instance, and we draw a single Intn(60) from it for the second-offset.
//
// Why not just `h.Sum32() % 60`? Because modulo against a non-power-of-2
// over a 32-bit hash introduces a tiny bias toward low values. Seeding a
// rand.Rand and calling Intn gives a uniform distribution over [0, 60).
// The cost (one Source.Int63 call) is negligible vs. the 0-60s sleep.
//
// Empty MAC is treated as seed=0 — same node, same value — which is
// fine: an unconfigured node will stack with any other unconfigured node,
// but the multicast path requires a registered MAC so this is a defensive
// fallback rather than a real case.
func jitterDuration(primaryMAC string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(primaryMAC))
	r := rand.New(rand.NewSource(int64(h.Sum32())))
	secs := r.Intn(multicastJitterMaxSeconds)
	return time.Duration(secs) * time.Second
}
