# Boot Architecture (post-deploy and re-image routing)

Status: ACCEPTED — 2026-04-25
Authors: Richard (architecture), Dinesh (implementation), Gilfoyle (host)
Supersedes: ad-hoc grub.efi chain-boot path introduced in `1b70e81`, `ddbab1d`, `51fcc10`, `fd819a5`, `e769882`

---

## TL;DR

Stop serving and chain-loading `grub.efi` from the clustr server after deploy.
Hand control back to the deployed OS's own bootloader, the same way every
production HPC provisioner does.

| Firmware | iPXE post-deploy action               | Booted binary                                         |
|----------|---------------------------------------|-------------------------------------------------------|
| BIOS     | `sanboot --no-describe --drive 0x80`  | `grub2-install --target=i386-pc` MBR + core.img       |
| UEFI     | `exit`                                | OS-installed `\EFI\rocky\grubx64.efi` via NVRAM entry |

Both paths converge on the same model: the deployed OS owns its own boot. The
clustr server's only post-deploy job is the iPXE *routing decision* (deploy vs.
disk), exactly like xCAT, Warewulf, MAAS, Cobbler, FAI.

---

## 1. Current state and failure modes

The post-deploy UEFI path tries to chain a server-built `grub.efi` over HTTP.
We have iterated five times and it is still broken on `vm202` (UEFI/OVMF):

| Attempt   | Approach                                                              | Failure mode                                                              |
|-----------|-----------------------------------------------------------------------|---------------------------------------------------------------------------|
| `51fcc10` | Copy `grub2-install --removable` output back to server                | 912 KB binary, missing modules, rescue prompt                             |
| `1b70e81` `ddbab1d` | `grub2-mkimage` standalone at image-creation time           | XFS module silently absent on 4-partition layouts, rescue prompt          |
| (in-tree) | Hardcoded `set root=(hd0,gpt2)` in stub `grub.cfg`                    | Wrong partition number for node-scope 4-partition layout                  |
| `fd819a5` | Use distro `grubx64.efi` (3.8 MB, full module set)                    | Binary has prefix baked to local ESP; over HTTP it has no `grub.cfg` path |
| `e769882` | Restore standalone `grub2-mkimage` with verified xfs+http             | Still drops to bare `grub>` after `chain` succeeds                        |

The 5-attempt arc has a common root cause: **GRUB's HTTP boot path is not the
right tool for this job.** It assumes a build environment that fully matches
the runtime layout, and prefix derivation is fragile across GRUB versions
(2.06 / 2.10 / Rocky build / upstream). We are spending architecture budget
to replicate what the OS-installed bootloader already does correctly.

vm201 (BIOS) works fine because the BIOS path uses iPXE `sanboot` and never
chains the server's grub binary.

---

## 2. Architectural decision

**Post-deploy boot for UEFI is `exit` to firmware. Period.**

The deployed OS's own bootloader handles boot. We have *already* installed
everything firmware needs to find it:

1. `grub2-install --target=x86_64-efi --removable` runs in the chroot at
   finalize time. It writes:
   - `\EFI\rocky\grubx64.efi` — primary loader path
   - `\EFI\BOOT\BOOTX64.EFI`  — UEFI removable-media fallback path
2. `FixEFIBoot` writes a NVRAM `Boot####` entry labelled "Rocky Linux"
   pointing at `\EFI\rocky\grubx64.efi`, sets it active, and prepends it to
   `BootOrder`.
3. `SetPXEBootFirst` (already in tree, `efiboot.go:243`) ensures PXE entries
   precede the OS entry in `BootOrder` so reimage routing still works.

When iPXE issues `exit` on UEFI:

- Firmware regains control and walks `BootOrder`.
- PXE entry is tried first. If clustr-serverd routes to "disk" again, iPXE
  will `exit` again. (Two-pass exit is normal; the next entry in `BootOrder`
  is then tried.) **In practice clustr never routes a deployed node back to
  PXE-disk in a loop** — the server returns the disk-boot script which itself
  ends in `exit`, and OVMF / real UEFI advance to the next `BootOrder` entry
  after iPXE exits cleanly.
- The OS NVRAM entry boots `\EFI\rocky\grubx64.efi` from the local ESP.
- If NVRAM is empty/wiped (cold-aisle BIOS reset, AC-loss on cheap NVRAM),
  UEFI spec falls through to `\EFI\BOOT\BOOTX64.EFI` from the first FAT-
  formatted partition with the EFI System Partition GUID. We wrote this with
  `--removable`, so this path always works.

This is symmetric with BIOS:

- BIOS `sanboot --drive 0x80` does an INT 13h chainload of LBA 0 on disk 0.
- LBA 0 contains the GRUB stage1 MBR written by `grub2-install --target=i386-pc`.
- Stage1 → core.img → reads `/boot/grub2/grub.cfg` from disk → boots kernel.

Both paths: **server makes the routing decision in iPXE, then hands off to
the OS-installed bootloader.** No server-side grub binary. No HTTP chain-boot
of GRUB. No standalone `grub2-mkimage`.

### Why the original "centralize through server-side grub.efi" idea felt
### attractive — and why it was the wrong reading of the problem

The motivation was always *re-image recovery*: "what if the OS bootloader is
broken? we want a way to force re-image without IPMI." That motivation is
real, and **already solved** by:

1. PXE always-first in BootOrder (set once at rack/stack, enforced by
   `SetPXEBootFirst`).
2. clustr-serverd's `force_reimage=1` query param routes back into the deploy
   initramfs *during the iPXE phase, before any OS bootloader runs.*
3. The "Reimage this node" iPXE menu option (already implemented in both
   BIOS and UEFI templates) re-chains to `/api/v1/boot/ipxe?mac=…&force_reimage=1`.

We never needed to chain a server-side GRUB to *recover* from a broken OS
bootloader — we only need iPXE to be reachable, which is guaranteed by
PXE-first BootOrder.

### Bare-metal vs OVMF: where would this break?

- **OVMF/QEMU/Proxmox VMs**: `exit` works. NVRAM is in pflash and survives
  reboot. The well-known "OVMF restarts PXE on exit instead of falling
  through" behavior is a SeaBIOS quirk, not OVMF — confirmed against the
  EDK2 source. (SeaBIOS is why we use `sanboot` on BIOS instead of `exit`.)

- **Dell PowerEdge (iDRAC)**: NVRAM is durable across reboots and cold
  shutdowns. `BootOrder` is honored. `\EFI\BOOT\BOOTX64.EFI` fallback also
  honored. No issues with `exit`.

- **HPE ProLiant (iLO)**: same story, NVRAM is rock-solid, `exit` lands on
  the next BootOrder entry reliably.

- **SuperMicro X11/X12**: `exit` works but some firmware revisions race on
  the NVRAM read after PXE exits — workaround is to ensure the OS NVRAM
  entry is *not last* in BootOrder (we put it second after PXE, so this
  is fine).

- **IBM POWER / aarch64**: out of scope for v1 — those use IEEE 1275
  Open Firmware or grub-ieee1275 / kexec-style boot, not iPXE-style HTTP
  boot. Different code path entirely.

- **NVRAM-volatile platforms (rare, mostly older Atom-class)**: the
  `--removable` fallback at `\EFI\BOOT\BOOTX64.EFI` covers this. We always
  write that path; UEFI spec requires firmware to fall through to it.

### What about the case where the OS truly is unbootable?

Two layers of defense:

1. **`reimage_pending` is the operator's primary recovery lever.** Set via
   API or the iPXE boot menu. Next PXE boot re-deploys.
2. **PXE-first BootOrder** means the operator can always trigger re-image
   without IPMI by doing two reboots: first one PXE-boots and clustr serves
   the disk-boot script (because state is `deployed_verified`); operator
   marks node `reimage_pending`; second reboot PXE-boots into the deploy
   initramfs.

We do not need server-side GRUB to recover broken OS bootloaders. We re-image.

---

## 3. Migration plan

### 3.1 iPXE script changes — `internal/pxe/boot.go`

Replace the UEFI disk-boot template's `chain {{.ServerURL}}/api/v1/boot/grub.efi?mac=${mac}`
with `exit`. Match BIOS and UEFI templates so they only diverge in the single
`:disk` label body:

```go
// BIOS :disk body (UNCHANGED)
:disk
sanboot --no-describe --drive 0x80 || exit

// UEFI :disk body (NEW)
:disk
exit
```

The `chain ... || exit` fallback in the UEFI template was already conceding
that `exit` is the correct terminal action. Cut out the middleman.

`GenerateDiskBootScript`'s `serverURL` parameter remains (still used for the
`reimage` menu option's `chain` URL). `Version` parameter remains.

### 3.2 Server handler removals — `internal/server/handlers/boot.go`

**Delete entirely:**

- `BootHandler.ServeGrubEFI` (lines 363–428)
- `BootHandler.ServeGrubCfg` (lines 430–472)
- The `ImageDir` field on `BootHandler` (lines 32–35) — no longer needed
  by any handler.

**Route registration:** `internal/server/server.go` lines 438–439. Remove:

```go
r.Get("/boot/grub.efi", boot.ServeGrubEFI)
r.Get("/boot/grub.cfg", boot.ServeGrubCfg)
```

Audit the surrounding `BootHandler` struct literal (around `server.go:367`,
`373`, `377`) and drop the `ImageDir` field assignment for the boot handler
specifically. Other handlers (Images, BuildProgress, Resume) still legitimately
use `cfg.ImageDir` — leave those alone.

Leave `GET /api/v1/boot/ipxe`, `GET /api/v1/boot/vmlinuz`,
`GET /api/v1/boot/initramfs.img`, `GET /api/v1/boot/ipxe.efi`,
`GET /api/v1/boot/undionly.kpxe` untouched.

### 3.3 Image-build path — `internal/image/isoinstaller/extract.go`

**Delete entirely:**

- `BuildStandaloneGrubEFI` (exported wrapper)
- `buildStandaloneGrubEFI` (the grub2-mkimage builder, lines 336–456)
- `copyGrubEFI` (lines 466–502)
- `grubEFICandidates` slice (lines 296–304)

**Modify `ExtractRootfs`:** remove the `copyGrubEFI(rootMnt, opts.RootfsDestDir)`
call (line 289) and its preceding comment block.

**Important:** *Do not* remove the ESP mount in `ExtractRootfs` (lines 263–279).
We still need to mount the ESP so that `grubx64.efi`, `shimx64.efi`, and
`BOOTX64.EFI` are captured into the rootfs blob. The deployed OS uses these
binaries — the ESP mount is unrelated to the server-side grub.efi.

The `--exclude` rules in `contentOnlyExcludes` are fine as-is.

### 3.4 Deploy finalize — `internal/deploy/rsync.go`

**Delete the standalone-rebuild block (lines 914–950).** This is the section
that calls `isoinstaller.BuildStandaloneGrubEFI` and writes
`<ImageDir>/<ImageID>/grub.efi` after `grub2-install`. Replace with a single
log line confirming the OS bootloader was installed; remove the entire
"Updating server boot binary" reportStep.

Remove the now-unused `ImageDir` and `ImageID` fields from the
`FilesystemDeployer` (or just stop populating/reading them in finalize).
Audit other deployer types for the same fields.

**Keep everything else:**

- `grub2-install --target=x86_64-efi --removable --no-nvram` in chroot
- ESP/grubx64.efi presence verification
- `FixEFIBoot` NVRAM entry creation
- `SetPXEBootFirst` BootOrder management

### 3.5 Documentation — keep this file as the canonical reference

Update `internal/server/handlers/boot.go` package doc comment to reference
this file. Update `internal/pxe/boot.go` template comment to reference this
file ("ADR: post-deploy UEFI uses `exit`, not chain-boot — see
docs/boot-architecture.md").

### 3.6 Tests

`internal/pxe/boot_test.go` lines 49–60 and ~99–100 assert the UEFI script
contains `/api/v1/boot/grub.efi`. Invert the assertion: assert the UEFI
script's `:disk` body is `exit` and that the script does NOT reference
`/api/v1/boot/grub.efi`. Keep the `reimage` chain assertion (the menu's
"Reimage this node" option still uses HTTP chain into `/api/v1/boot/ipxe`).

Search and delete any tests that exercised `BuildStandaloneGrubEFI`,
`ServeGrubEFI`, `ServeGrubCfg` (run `grep -rn "BuildStandaloneGrubEFI\|ServeGrubEFI\|ServeGrubCfg" internal/`).

Keep `efiboot_test.go` (`FixEFIBoot`, `SetPXEBootFirst`) — this code is
unchanged and now load-bearing.

---

## 4. Test plan

### 4.1 vm201 (BIOS / SeaBIOS)

Behavior should be unchanged.

1. Mark vm201 `reimage_pending` via API.
2. Power-cycle. Watch serial console.
3. Expect: PXE → iPXE → deploy initramfs → clustr deploy completes →
   `efibootmgr` not invoked (BIOS path) → reboot.
4. Second boot: iPXE menu shows "Boot from disk [auto 5s]" → `sanboot --drive 0x80`
   → grub2 from MBR → kernel → systemd → clustr-clientd phones home.
5. State transitions: `reimage_pending` → `deploying` → `deployed_preboot`
   → `deployed_verified`.

### 4.2 vm202 (UEFI / OVMF)

This is the path that has been broken.

1. Reset OVMF NVRAM to defaults (delete and recreate the VM's pflash if
   needed) so we test the cold-NVRAM path.
2. Mark vm202 `reimage_pending`.
3. Power-cycle. PXE → iPXE → deploy initramfs → finalize:
   - grub2-install --target=x86_64-efi --removable --no-nvram → confirm
     `\EFI\rocky\grubx64.efi` AND `\EFI\BOOT\BOOTX64.EFI` exist on the ESP
   - efibootmgr --create → confirm "Rocky Linux" entry visible
   - SetPXEBootFirst → confirm BootOrder is `[PXE…, Rocky Linux, …]`
4. Reboot. iPXE menu shows "Boot from disk" → `exit`.
5. OVMF walks BootOrder:
   - PXE retried → server returns disk-boot script → iPXE `exit` again
   - OVMF advances → finds "Rocky Linux" → loads `\EFI\rocky\grubx64.efi`
     from local ESP
   - GRUB reads its OWN `/boot/grub2/grub.cfg` from local /boot
   - Kernel + initramfs from /boot
   - systemd → clustr-clientd phones home
6. State transitions: `reimage_pending` → `deploying` → `deployed_preboot`
   → `deployed_verified`.

### 4.3 NVRAM-cleared simulation (vm202)

1. After 4.2 succeeds, `qm set 202 -delete efidisk0 && qm set 202 -efidisk0 ...`
   to wipe NVRAM but keep disk.
2. Reboot. OVMF has no `Rocky Linux` entry. Should fall through to
   `\EFI\BOOT\BOOTX64.EFI` (UEFI removable fallback).
3. Expect: boots OS without re-deploy.

### 4.4 Forced re-image during disk-boot menu

1. Power-cycle vm202 (currently `deployed_verified`).
2. iPXE menu appears → choose "Reimage this node" within the 5s window.
3. Server marks `reimage_pending`, returns deploy script, deploy runs.

### 4.5 Server-side regressions

- `GET /api/v1/boot/grub.efi` returns 404 (route gone)
- `GET /api/v1/boot/grub.cfg` returns 404 (route gone)
- Image directory no longer contains `grub.efi` (image-build path removed)
- `clustr-serverd` starts cleanly (no nil-deref on removed `ImageDir` field)

---

## 5. Bare-metal considerations

The migration is *more* compatible with bare-metal than the current state,
because:

- We stop relying on GRUB-over-HTTP, which has known issues with how
  GRUB derives `prefix` from the chain URL on enterprise UEFI firmware.
- We stop generating EFI binaries server-side, which sidesteps signed-bootloader
  issues if Secure Boot is enabled (the OS-installed shim+grub on the ESP is
  signed; ours isn't).
- We exercise the same code path that every other HPC provisioner uses,
  so we benefit from years of vendor-tested NVRAM behavior on Dell/HPE/SMC.

Specific bare-metal callouts:

- **Secure Boot**: deferred. Our current code disables Secure Boot in test.
  Once we want SB-on, the new architecture is *required* — we cannot ship a
  signed standalone grub.efi from the clustr server without Microsoft signing
  or shim enrollment. The OS-installed shim already handles SB correctly.
- **NVRAM persistence**: Dell, HPE, SMC, Lenovo all persist NVRAM across
  AC loss. Cheap whitebox / Atom-class boards may not — the
  `\EFI\BOOT\BOOTX64.EFI` fallback (already written by `--removable`) handles
  these cases per UEFI spec.
- **BootOrder mutation by firmware**: some firmwares "auto-promote" newly
  added entries; some demote PXE after a successful disk boot. `SetPXEBootFirst`
  runs at every finalize and re-asserts the right order. Long-term we may
  want a periodic "BootOrder repair" sweep, but that's a separate sprint.
- **HTTP Boot vs PXE Boot (UEFI HTTP Boot, "iPXE-less")**: out of scope. We
  ship our own iPXE. The new architecture has no opinion on UEFI HTTP Boot
  vs traditional PXE — same routing, same handoff to OS bootloader.

---

## 6. Rollback plan

If `vm202` still fails to boot after the migration:

1. **Diagnostic step before rollback**: drop into the OVMF UEFI shell from
   the boot picker. From the shell run `bcfg boot dump -v` to print
   BootOrder and entries. Confirm `Rocky Linux` is present and points at
   `\EFI\rocky\grubx64.efi`. If yes, run `fs0:\EFI\rocky\grubx64.efi`
   manually — if THAT works, the bug is in BootOrder/firmware, not in our
   migration.
2. If grubx64.efi at `\EFI\rocky\` is broken: the bug is in `grub2-install`
   chroot invocation, not iPXE. Existing failure that was masked by HTTP
   chain-boot. Fix in chroot path.
3. **True rollback**: revert this commit + the corresponding deploy/rsync.go
   change. Server-side grub.efi build returns. Same broken state as today.

The migration is *strictly less* server-side state than current, so rollback
is mechanical: `git revert` the migration commit.

---

## 7. Out-of-scope (future sprints)

- Secure Boot (requires shim enrollment story)
- IPMI-driven re-image automation (separate "boot-control" surface)
- BootOrder repair sweep (background task that re-asserts PXE-first)
- iSCSI/FCoE root (different bootloader story entirely)
- ARM64 / aarch64 nodes (different iPXE binary, different GRUB target)
