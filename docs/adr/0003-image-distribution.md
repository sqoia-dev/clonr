# ADR-0003: Image Distribution at Scale

**Date:** 2026-04-13
**Status:** Accepted

---

## Context

Image blobs are large (10-30 GB for a typical HPC rootfs). clonr must serve them to many nodes concurrently during a rolling or simultaneous deployment. The distribution method determines whether clonr scales to 200-node concurrent deploys or saturates the provisioning server's NIC at 10.

Three viable approaches for a pure-Go, open-source, air-gapped stack:

1. **HTTP with range requests + server-side read cache** — standard HTTP, works with any client, supports resume, saturates the server NIC at ~200 concurrent readers on 1 GbE (bottleneck: 125 MB/s / 10 nodes ≈ 12.5 MB/s per node for simultaneous deploys).
2. **udpcast multicast** — HPC-standard tool, ships with most distros, achieves line rate (10 GbE → ~1.1 GB/s) shared across all receivers simultaneously. Requires multicast-capable L2 network. Not a pure-Go dependency (shells out to `udp-sender`/`udp-receiver`). Already scaffolded in `pkg/deploy/multicast.go`.
3. **BitTorrent (pure-Go: anacrolix/torrent)** — fully distributed, no server NIC bottleneck, no multicast requirement. High implementation complexity; tracker/peer coordination overhead is not worth it below ~500 nodes.

HPC provisioning networks are always multicast-capable (they are isolated L2 segments, never routed). udpcast is already in the Rocky/RHEL repos. The pure-Go constraint applies to the server binary and the initramfs CLI binary — calling an external tool that is already present on the provisioning server is acceptable.

The key constraint for the initramfs client is that it must be statically compilable with CGO_ENABLED=0. The initramfs receiver side of udpcast is `udp-receiver`, a C binary. This means the initramfs must include both the clonr Go binary and `udp-receiver` as a separate static binary — which is already the pattern for `partclone` and `rsync`.

---

## Decision

**v1.0: HTTP range requests with a server-side read cache and configurable concurrency limits.**

The blob download path (`GET /api/v1/images/:id/blob`) already supports HTTP range via standard `net/http` `ServeContent`. Add: a configurable `max_concurrent_blob_streams` server config (default: 20) enforced via a semaphore in the blob handler. This prevents 200 nodes from simultaneously saturating the provisioning server. Nodes queue and retry with backoff. Resume is built-in via `Range` headers.

For a 50-node v1.0 cluster on 1 GbE provisioning network: 50 nodes × ~2 GB effective compressed image = 100 GB transfer. At 125 MB/s aggregate, this is ~13 minutes wall clock for a full cluster reimage with concurrency=20. Acceptable for v1.0.

**v1.1: udpcast multicast as the primary path for simultaneous full-cluster deploys.**

When `distribution_mode: multicast` is set in server config, the server coordinates a multicast session: it calls `udp-sender` with the image blob, nodes join with `udp-receiver`, and the entire cluster receives the image at line rate simultaneously. The server orchestrates the session start/stop and reports progress via the existing deploy event stream.

The HTTP path remains as the fallback for: nodes that miss the multicast window, single-node deploys, and sites where multicast is administratively disabled.

The `pkg/deploy/multicast.go` stub already exists. v1.1 makes it functional.

---

## Consequences

- v1.0 HTTP path requires no new dependencies and works in any network topology. The concurrency cap prevents server overload.
- At 200 nodes with HTTP-only, a full reimage is ~26 minutes at concurrency=20. This is the documented limitation that motivates the v1.1 multicast upgrade.
- udpcast multicast requires a multicast-enabled L2 network and `udp-sender` installed on the provisioning server. Both are standard in HPC environments. Document this as a v1.1 prerequisite.
- BitTorrent is deferred to v2.x if there is ever a multi-site or WAN distribution requirement. It adds ~15k lines of dependency for a use case that does not exist yet.
- The `ImageStore` interface is unchanged by this decision. Distribution is a transport concern, not a storage concern.
