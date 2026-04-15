# Boot Matrix Coverage — Sprint 3 Audit

**Date:** 2026-04-15
**Owner:** Gilfoyle (Infra)
**Source:** ADR-0008 validation requirement, validation-strategy.md boot matrix definition
**Sprint:** Sprint 3 prep

---

## Matrix Definition

Full 24-cell matrix: {BIOS, UEFI} × {single-disk, RAID1, RAID5, RAID10} × {small, medium, large}

- **Small:** < 100 GB — 32 GB virtual disk in lab
- **Medium:** 100 GB – 2 TB — 500 GB virtual disk in lab
- **Large:** > 2 TB — 4 TB virtual disk; tests GPT 2 TB boundary behavior

v1.0 minimum bar (from validation-strategy.md):
- All 8 topology cells at medium tier: 16 cells
- Small + large tiers for BIOS/single-disk and UEFI/single-disk: 4 additional cells
- Total minimum: 20 cells
- Remaining 4 large-disk RAID cells: Sprint 2+ (deferred)

---

## Full 24-Cell Coverage Table

| # | Firmware | Topology | Size | Status | VMID / Notes |
|---|----------|----------|------|--------|--------------|
| 1 | BIOS | single-disk | small | YELLOW — partial | VM203: BIOS boot confirmed, serial capture not automated |
| 2 | BIOS | single-disk | medium | NOT STARTED | VM204: VM exists, no image deployed yet |
| 3 | BIOS | single-disk | large | NEEDS NEW VM | No VM; requires 4 TB virtual disk |
| 4 | BIOS | RAID1 | small | NEEDS NEW VM | VM205 exists but needs second 32 GB disk added |
| 5 | BIOS | RAID1 | medium | NEEDS NEW VM | Not provisioned |
| 6 | BIOS | RAID1 | large | DEFERRED (Sprint 2+) | Not provisioned; out of scope for v1.0 |
| 7 | BIOS | RAID5 | small | NEEDS NEW VM | Not provisioned |
| 8 | BIOS | RAID5 | medium | NEEDS NEW VM | Not provisioned |
| 9 | BIOS | RAID5 | large | DEFERRED (Sprint 2+) | Not provisioned; out of scope for v1.0 |
| 10 | BIOS | RAID10 | small | NEEDS NEW VM | Not provisioned |
| 11 | BIOS | RAID10 | medium | NEEDS NEW VM | Not provisioned |
| 12 | BIOS | RAID10 | large | DEFERRED (Sprint 2+) | Not provisioned; out of scope for v1.0 |
| 13 | UEFI | single-disk | small | RED — broken | VM201: EFI partition / NVRAM issues from Sprint 2; Dinesh fixing |
| 14 | UEFI | single-disk | medium | RED — broken | VM202: same root cause as VM201 |
| 15 | UEFI | single-disk | large | NEEDS NEW VM | No VM; requires 4 TB virtual disk + OVMF/UEFI firmware |
| 16 | UEFI | RAID1 | small | RED — broken | VM206: false-green in Sprint 2 |
| 17 | UEFI | RAID1 | medium | RED — broken | VM207: false-green in Sprint 2 |
| 18 | UEFI | RAID1 | large | DEFERRED (Sprint 2+) | Not provisioned; out of scope for v1.0 |
| 19 | UEFI | RAID5 | small | NEEDS NEW VM | Not provisioned |
| 20 | UEFI | RAID5 | medium | NEEDS NEW VM | Not provisioned |
| 21 | UEFI | RAID5 | large | DEFERRED (Sprint 2+) | Not provisioned; out of scope for v1.0 |
| 22 | UEFI | RAID10 | small | NEEDS NEW VM | Not provisioned |
| 23 | UEFI | RAID10 | medium | NEEDS NEW VM | Not provisioned |
| 24 | UEFI | RAID10 | large | DEFERRED (Sprint 2+) | Not provisioned; out of scope for v1.0 |

**Physical hardware requirement (ADR-0008):** Cells 2 (BIOS/single/medium) and 14 (UEFI/single/medium)
must each have at least one passing run on physical bare-metal before v1.0. QEMU virtio does not
faithfully reproduce real NVMe/SATA timing or EFI NVRAM behavior.

---

## Coverage Summary

| Category | Count |
|----------|-------|
| GREEN (fully passing with serial capture) | 0 |
| YELLOW (partial — boot confirmed, no automated serial capture) | 1 |
| RED (existing VM, broken state from Sprint 2) | 4 |
| NOT STARTED (VM exists, no deploy attempted) | 1 |
| NEEDS NEW VM (no VM provisioned) | 12 |
| DEFERRED Sprint 2+ (large RAID cells) | 6 |
| **Total** | **24** |

**Cells reachable with existing VMs:** 6 (VM201, VM202, VM203, VM204, VM205+disk, VM206, VM207)
However VM203 is YELLOW, VM204 is NOT STARTED, VM205 needs a second disk, and VM201/202/206/207 are all RED.

**True GREEN cells:** 0. The v1.0 minimum bar of 20 GREEN cells is not met.

---

## What Must Be Added for Sprint 3 / v1.0

### Repair Existing VMs (no new provisioning required)

| VM | Fix Required |
|----|-------------|
| VM201 | UEFI boot path fix (ADR-0009 refactor — Dinesh) + automated serial capture |
| VM202 | Same fix as VM201 |
| VM206 | UEFI RAID1 path fix + automated serial capture |
| VM207 | UEFI RAID1 path fix + automated serial capture |
| VM203 | Wire up automated serial capture (already boots) |
| VM204 | Run first deploy + automated serial capture |
| VM205 | Add second 32 GB virtual disk via Proxmox; test BIOS RAID1 |

### New VMs to Provision on Proxmox

These are net-new VMs needed to achieve 20-cell v1.0 coverage. Ordered by priority (most
common HPC deployment topologies first).

| Priority | Covers | Firmware | Topology | Size | Proxmox Config |
|----------|--------|----------|----------|------|----------------|
| P1 | Cell 5 | BIOS | RAID1 | medium | 2× 500 GB virtio-scsi; SeaBIOS; 2 vCPU; 2 GB RAM; vmbr10 only |
| P1 | Cell 8 | BIOS | RAID5 | medium | 3× 500 GB virtio-scsi; SeaBIOS; 2 vCPU; 2 GB RAM; vmbr10 only |
| P1 | Cell 11 | BIOS | RAID10 | medium | 4× 500 GB virtio-scsi; SeaBIOS; 2 vCPU; 4 GB RAM; vmbr10 only |
| P1 | Cell 20 | UEFI | RAID5 | medium | 3× 500 GB virtio-scsi; OVMF/UEFI; EFI disk; 2 vCPU; 2 GB RAM; vmbr10 only |
| P1 | Cell 23 | UEFI | RAID10 | medium | 4× 500 GB virtio-scsi; OVMF/UEFI; EFI disk; 2 vCPU; 4 GB RAM; vmbr10 only |
| P2 | Cell 3 | BIOS | single | large | 1× 4 TB virtio-scsi; SeaBIOS; 2 vCPU; 2 GB RAM; vmbr10 only |
| P2 | Cell 15 | UEFI | single | large | 1× 4 TB virtio-scsi; OVMF/UEFI; EFI disk; 2 vCPU; 2 GB RAM; vmbr10 only |
| P3 | Cell 7 | BIOS | RAID5 | small | 3× 32 GB virtio-scsi; SeaBIOS; 2 vCPU; 2 GB RAM; vmbr10 only |
| P3 | Cell 10 | BIOS | RAID10 | small | 4× 32 GB virtio-scsi; SeaBIOS; 2 vCPU; 4 GB RAM; vmbr10 only |
| P3 | Cell 19 | UEFI | RAID5 | small | 3× 32 GB virtio-scsi; OVMF/UEFI; EFI disk; 2 vCPU; 2 GB RAM; vmbr10 only |
| P3 | Cell 22 | UEFI | RAID10 | small | 4× 32 GB virtio-scsi; OVMF/UEFI; EFI disk; 2 vCPU; 4 GB RAM; vmbr10 only |

**Suggested VMID range:** 208–218 (continuing the lab numbering convention).
All new VMs connect to `vmbr10` only (isolated provisioning bridge, no uplink).

**Disk storage estimate for new VMs:**
- P1 VMs: 5 VMs × avg 1.5 TB = ~7.5 TB virtual disk allocation (thin-provisioned)
- P2 VMs: 2 VMs × 4 TB = 8 TB virtual disk allocation (thin-provisioned)
- P3 VMs: 4 VMs × avg 100 GB = ~400 GB virtual disk allocation
- Total new virtual disk: ~16 TB thin-provisioned — check Proxmox storage pool capacity before provisioning

### Physical Hardware Requirement

At least 2 cells must pass on physical bare-metal before v1.0:

| Cell | Requirement | Notes |
|------|-------------|-------|
| Cell 2: BIOS/single/medium | Physical node with real SATA/NVMe disk | One commodity server or repurposed workstation with 500 GB+ disk |
| Cell 14: UEFI/single/medium | Physical node with UEFI firmware + real disk | Same class of hardware; confirm UEFI NVRAM is writable and persistent |

Gilfoyle owns provisioning one physical node for this purpose. The Proxmox lab server itself
(192.168.1.223) could serve as the physical test node during a maintenance window if a
dedicated machine is not available. This requires taking it offline as a PVE host temporarily.

---

## Sprint 3 Gate: Minimum 20 GREEN Cells

To ship v1.0, these 20 cells must all reach GREEN status (ADR-0008 two-phase verified +
serial console capture with login prompt):

**Must fix (existing VMs):** Cells 1, 2, 4, 13, 14, 16, 17 — 7 cells
**Must provision and pass (new VMs):** Cells 5, 7, 8, 10, 11, 19, 20, 22, 23 — 9 cells
**Must pass on physical hardware:** Cells 2 and 14 — subset of above

The 4 remaining large-RAID cells (6, 9, 12, 18) plus the large-single cells (3, 15) remain
DEFERRED unless P2 VMs are provisioned. They do not block v1.0 if the 20-cell minimum is met
without them.

---

## Proxmox VM Creation Reference

Common parameters for all new lab VMs:

```bash
# All new VMs: vmbr10 only (isolated provisioning bridge)
# Boot order: net0 (PXE first), then disk
# No cloud-init, no OS pre-installed (PXE-booted and imaged by clonr)
# Serial console: serial0 (required for automated capture)

# Example: P1 — BIOS/RAID5/medium (Cell 8), suggested VMID 210
qm create 210 \
  --name test-node-210-bios-raid5-med \
  --memory 2048 \
  --cores 2 \
  --net0 virtio,bridge=vmbr10 \
  --boot order=net0 \
  --bios seabios \
  --serial0 socket \
  --vga serial0 \
  --scsihw virtio-scsi-pci \
  --scsi0 local-lvm:500 \
  --scsi1 local-lvm:500 \
  --scsi2 local-lvm:500

# Example: P1 — UEFI/RAID5/medium (Cell 20), suggested VMID 213
qm create 213 \
  --name test-node-213-uefi-raid5-med \
  --memory 2048 \
  --cores 2 \
  --net0 virtio,bridge=vmbr10 \
  --boot order=net0 \
  --bios ovmf \
  --efidisk0 local-lvm:1 \
  --serial0 socket \
  --vga serial0 \
  --scsihw virtio-scsi-pci \
  --scsi0 local-lvm:500 \
  --scsi1 local-lvm:500 \
  --scsi2 local-lvm:500
```

Adjust storage pool name (`local-lvm`) to match your Proxmox pool. Use `qm start <vmid>`
to PXE boot after creation — clonr-serverd handles DHCP/TFTP/deploy from there.
