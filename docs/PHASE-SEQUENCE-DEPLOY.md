# Deploy Phase Sequence (Sprint 25 #157 + #159 coordination)

**Author:** Dinesh (Sprint 25 #157)
**Date:** 2026-05-02
**Status:** Reference — coordinate with #159 BIOS stream before modifying

---

## Ordered phase sequence for initramfs deploy

```
Phase 1 — Hardware discovery
  clustr deploy --auto calls hardware.Discover()
  Outputs: NIC list, disk list, firmware type (bios|uefi)

Phase 2 — Server registration
  POST /api/v1/nodes/register with HardwareProfile + DetectedFirmware
  Server returns action=deploy|wait + NodeConfig

Phase 3 — BIOS apply (if assigned)
  *** TODO(#159): hook here for bios-only phase ***
  When NodeConfig.BIOSProfileID != "" and NodeConfig.BIOSOnly == true:
    - clustr applies BIOS settings via clustr-privhelper
    - Reports outcome to server
    - If bios_only=true: skips image fetch → partition → write; exits
  If no BIOS profile assigned: skip to Phase 4.

Phase 4 — Image fetch (multicast OR unicast)
  Decision tree:
    A. multicast_join=true in NodeConfig → enroll in multicast session
       - POST /api/v1/multicast/enqueue
       - Poll GET /api/v1/multicast/sessions/{id}/wait (retry on 202)
       - On 200 + descriptor: fork/exec udp-receiver --pipe "tar -xz ..."
       - On 200 + fallback=true: fall through to unicast (B)
    B. unicast (default) → GET /api/v1/images/{id}/blob → stream to disk

Phase 5 — Partition + write
  Run disk layout partitioning (sgdisk, mkfs).
  Write image bytes to target partition.

Phase 6 — Finalize
  - Install bootloader (grub2-install --removable --no-nvram)
  - Write /etc/hosts (cluster host roster)
  - Write LDAP/SSH config
  - POST /api/v1/nodes/{node_id}/deploy-complete
  - Reboot → PXE → disk-boot

Phase 7 — Outcome reporting (multicast path only)
  After udp-receiver exits (Phase 4A):
    POST /api/v1/multicast/sessions/{id}/members/{node_id}/outcome
    outcome = success | failed | fellback_unicast
  Called by runAutoDeployMode before entering Phase 5.
```

---

## Coordination notes for #159

- **#157 touches:** Phase 4 (multicast fetch branch), `cmd/clustr/main.go` `runAutoDeployMode`, `scripts/initramfs-init.sh` (cmdline parsing of `clustr.multicast` + `clustr.session_poll_url`).
- **#159 touches:** Phase 3 (BIOS apply hook), `internal/bios/`, privhelper verbs, a new initramfs binary (`intel-syscfg` or equivalent). Phase 3 slot is reserved here; the TODO comment lives in `runAutoDeployMode` in `cmd/clustr/main.go`.
- **Shared files:** `cmd/clustr/main.go` and `scripts/initramfs-init.sh`. Merge strategy: #157 lands first; #159 rebases on top and fills the TODO in Phase 3.
- **Migration numbers:** #157 = 093, #159 = 094/095 (pre-agreed).

---

## Fallback logic (Phase 4A)

If udp-receiver exits non-zero (packet loss above threshold, ENOBUFS, etc.):
1. Log the error with the session ID.
2. Report `outcome=fellback_unicast` to the server.
3. Fall back to unicast HTTP fetch (Phase 4B).
4. Proceed normally through Phase 5–7.

Total worst-case elapsed time = 60s window + udp-receiver attempt + unicast fetch.
No worse than today's baseline (unicast only).
