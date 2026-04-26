#!/bin/bash
# create.sh — Provision clustr dev VMs (vm201 BIOS, vm202 UEFI) on the local
# Proxmox host (192.168.1.223). Run from the Proxmox host as root.
#
# Boot-order contract (docs/boot-architecture.md §10):
#   Persistent default = scsi0;net0 (disk-first).
#   clustr issues a one-shot PXE override per reimage via SetNextBoot(BootPXE),
#   which stops the VM, writes net0;scsi0 to the Proxmox config, and starts it.
#   After deploy the verify-boot handler calls SetPersistentBootOrder([disk,pxe])
#   which stops the VM, writes scsi0;net0 back to config, and starts it.
#
# This script sets the persistent default to scsi0;net0 at VM creation time.
# If you reprovisioned a VM by hand, run the "assert-boot-order" function below
# or just: qm set <vmid> --boot order=scsi0;net0

set -euo pipefail

PVE_NODE="${PVE_NODE:-pve}"
STORAGE="${STORAGE:-local-lvm}"
BRIDGE_LAN="${BRIDGE_LAN:-vmbr0}"
BRIDGE_PROV="${BRIDGE_PROV:-vmbr10}"

# VM IDs and roles used in the clustr dev environment.
VM201_ID=201   # BIOS / SeaBIOS node — exercises the grub2 MBR path
VM202_ID=202   # UEFI / OVMF node    — exercises the BOOTX64.EFI path

log()  { echo "[create.sh] $*"; }
die()  { echo "[create.sh] FATAL: $*" >&2; exit 1; }

# assert_boot_order verifies (and optionally sets) the Proxmox boot order for a
# VM. The persistent default MUST be scsi0;net0 (disk-first) per §10.
# clustr's SetNextBoot(BootPXE) will toggle it to net0;scsi0 at reimage time and
# SetPersistentBootOrder([disk,pxe]) will flip it back after deploy.
assert_boot_order() {
    local vmid="$1"
    local want="order=scsi0;net0"
    local current
    current=$(qm config "${vmid}" 2>/dev/null | grep '^boot:' | awk '{print $2}' || echo "")
    if [[ "${current}" == "${want}" ]]; then
        log "VM${vmid}: boot order is already ${want} — OK"
    else
        log "VM${vmid}: setting boot order to ${want} (was: '${current}')"
        qm set "${vmid}" --boot "${want}"
        # Stop+start to commit the pending config if the VM is running.
        # (Proxmox only commits VM config changes on stop+start, not on reset.)
        local status
        status=$(qm status "${vmid}" 2>/dev/null | awk '{print $2}' || echo "stopped")
        if [[ "${status}" == "running" ]]; then
            log "VM${vmid}: stopping to commit boot order change..."
            qm stop "${vmid}" --skiplock 2>/dev/null || true
            sleep 3
            log "VM${vmid}: starting..."
            qm start "${vmid}"
        fi
        log "VM${vmid}: boot order set to ${want}"
    fi
}

# create_vm201 — BIOS / SeaBIOS test node (32 GB disk, no EFI disk).
create_vm201() {
    local vmid="${VM201_ID}"
    if qm config "${vmid}" &>/dev/null; then
        log "VM${vmid} already exists — skipping creation"
        assert_boot_order "${vmid}"
        return
    fi
    log "Creating VM${vmid} (BIOS/SeaBIOS)..."
    qm create "${vmid}" \
        --name "clustr-node-bios" \
        --node "${PVE_NODE}" \
        --memory 2048 \
        --cores 2 \
        --sockets 1 \
        --cpu host \
        --machine q35 \
        --bios seabios \
        --ostype l26 \
        --net0 "virtio,bridge=${BRIDGE_PROV},firewall=0" \
        --scsihw virtio-scsi-pci \
        --scsi0 "${STORAGE}:32,format=raw" \
        --serial0 socket \
        --vga serial0

    # Persistent boot order: disk-first (scsi0), network as fallback (net0).
    # See docs/boot-architecture.md §10 — this is the REQUIRED default.
    # clustr's SetNextBoot(BootPXE) overrides to net0;scsi0 per-reimage,
    # then SetPersistentBootOrder flips back here after each deploy.
    qm set "${vmid}" --boot order=scsi0;net0

    log "VM${vmid} created with boot order=scsi0;net0"
}

# create_vm202 — UEFI / OVMF test node (32 GB disk + 4 MB EFI disk for NVRAM).
create_vm202() {
    local vmid="${VM202_ID}"
    if qm config "${vmid}" &>/dev/null; then
        log "VM${vmid} already exists — skipping creation"
        assert_boot_order "${vmid}"
        return
    fi
    log "Creating VM${vmid} (UEFI/OVMF)..."
    qm create "${vmid}" \
        --name "clustr-node-uefi" \
        --node "${PVE_NODE}" \
        --memory 2048 \
        --cores 2 \
        --sockets 1 \
        --cpu host \
        --machine q35 \
        --bios ovmf \
        --ostype l26 \
        --efidisk0 "${STORAGE}:0,efitype=4m,pre-enrolled-keys=0" \
        --net0 "virtio,bridge=${BRIDGE_PROV},firewall=0" \
        --scsihw virtio-scsi-pci \
        --scsi0 "${STORAGE}:32,format=raw" \
        --serial0 socket \
        --vga serial0

    # Persistent boot order: disk-first (scsi0), network as fallback (net0).
    # See docs/boot-architecture.md §10 — this is the REQUIRED default.
    # clustr's SetNextBoot(BootPXE) overrides to net0;scsi0 per-reimage,
    # then SetPersistentBootOrder flips back here after each deploy.
    qm set "${vmid}" --boot order=scsi0;net0

    log "VM${vmid} created with boot order=scsi0;net0"
}

# ─── Main ─────────────────────────────────────────────────────────────────────

if [[ "${EUID}" -ne 0 ]]; then
    die "Must run as root on the Proxmox host"
fi

case "${1:-all}" in
    201) create_vm201 ;;
    202) create_vm202 ;;
    assert)
        # Just assert/fix boot order on existing VMs without recreating them.
        for vmid in "${VM201_ID}" "${VM202_ID}"; do
            if qm config "${vmid}" &>/dev/null; then
                assert_boot_order "${vmid}"
            else
                log "VM${vmid} does not exist — skipping"
            fi
        done
        ;;
    all|*)
        create_vm201
        create_vm202
        ;;
esac

log "Done. Verify with: qm config 201 | grep ^boot && qm config 202 | grep ^boot"
log "Expected output: boot: order=scsi0;net0 for both VMs."
