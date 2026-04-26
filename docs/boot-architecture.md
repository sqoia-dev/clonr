# Boot Architecture (post-deploy and re-image routing)

Status: ACCEPTED — 2026-04-25 (sections 1–7); AMENDED 2026-04-25 (section 8); AMENDED 2026-04-25 (section 9 — withdrawn, see §10); AMENDED 2026-04-25 (section 10 — current)
Authors: Richard (architecture), Dinesh (implementation), Gilfoyle (host + diagnosis)
Supersedes: ad-hoc grub.efi chain-boot path introduced in `1b70e81`, `ddbab1d`, `51fcc10`, `fd819a5`, `e769882`

> Reader note: section 8 supersedes the parts of sections 2 and 3.4 that
> describe `FixEFIBoot` as load-bearing. After the migration in sections 1–7
> landed, vm202 still failed at the OVMF picker. Diagnosis showed the NVRAM
> entry created by `FixEFIBoot` was the active source of failure, not the
> recovery mechanism. The OS-installed `\EFI\BOOT\BOOTX64.EFI` from
> `grub2-install --removable` is in fact the only correct boot target, and
> NVRAM-managed entries must be removed. Read sections 1–7 for context, then
> read section 8 for the actual decision.
>
> Section 9 (boot-order toggle model) was a misread of the diagnosis. It
> assumed the Proxmox `strict=on` boot-picker behaviour generalised to real
> firmware and that the right fix was to push everyone onto a one-shot
> "rewrite + cold-cycle" path. That's wrong: every server-class BMC
> auto-walks `BootOrder` after a returning PXE entry per UEFI spec. Forcing
> bare metal to mimic the Proxmox quirk would invert the abstraction and
> introduce a new failure mode (cold-cycle on every reimage trigger) on
> hardware that doesn't need it. **Section 10 supersedes section 9 in its
> entirety.** The decision is hybrid: bare metal stays on the §8 model
> (permanent disk-first BootOrder + one-shot IPMI override + firmware
> auto-walk); Proxmox dev VMs need a Proxmox-specific dance because Proxmox
> rewrites NVRAM on every cold start and OVMF `strict=on` defeats firmware
> auto-walk. Read sections 1–7 for context, section 8 for the NVRAM
> decision, then **section 10** for the production architecture and the
> isolated dev workaround.

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

---

## 8. Amendment (2026-04-25): vm202 post-migration root cause + Option B

Status: ACCEPTED — 2026-04-25
Diagnosis: Gilfoyle. Architectural review: Richard.
Supersedes: section 2 ("FixEFIBoot writes a NVRAM `Boot####` entry … this is
load-bearing"), section 3.4's "Keep … FixEFIBoot NVRAM entry creation" bullet,
and section 4.2 step 3's `efibootmgr --create` checkpoint. All other sections
remain in force.

### 8.1 What actually broke on vm202 after the migration landed

After sections 1–7 shipped (server-side `grub.efi` removed, iPXE switched to
`exit` on UEFI), vm202 still landed at the OVMF picker and would not boot
without manual intervention. Diagnosis from inside the OVMF shell, against the
running deploy and the post-deploy ESP, identified **three compounding defects
inside the `FixEFIBoot` NVRAM entry** — none of which are visible from
`efibootmgr -v` output alone unless you decode the device path GUID and stat
the binary it points at.

| #   | Defect                                | What `FixEFIBoot` writes                                  | Reality on the deployed ESP                                     |
| --- | ------------------------------------- | --------------------------------------------------------- | --------------------------------------------------------------- |
| 1   | Stale ESP partition GUID              | `ded642be-…` (preserved from a prior deployment of vm202) | Fresh `parted mklabel gpt` regenerates PARTUUID every reimage; actual ESP PARTUUID is `0bc3fc6a-…` |
| 2   | Wrong loader binary                   | `\EFI\rocky\grubx64.efi` (3.96 MB, RPM-shipped, prefix-less, timestamp 1979-12-31) | Loader has no embedded `prefix=` so it asks for `\EFI\rocky\grub.cfg` and drops to `grub>` |
| 3   | Missing on-ESP grub.cfg               | Implicit dependency on `\EFI\rocky\grub.cfg`              | Not present. Real `grub.cfg` lives on `gpt2:/grub2/grub.cfg`. The only binary that knows that is the prefix-baked `\EFI\BOOT\BOOTX64.EFI` written by `grub2-install --removable --no-nvram` |

The reason this was masked during sections 1–7's review is a leaky abstraction
in efibootmgr semantics: NVRAM entries reference a partition by **GPT
PartitionGUID embedded in the EFI device path**, not by partition number or
disk path. `efibootmgr --create --disk /dev/sda --part 1` reads the *current*
PARTUUID and bakes it into the variable. NVRAM is stored in OVMF pflash, which
**survives the reimage** (the data disk is wiped, pflash is not). On every
reimage the new ESP gets a new PARTUUID; the NVRAM entry does not. Result:
NVRAM points at a partition that no longer exists.

We assumed in section 2 that `FixEFIBoot` was idempotent because we re-ran it
every deploy. It is — but it is idempotent against the *running* PARTUUID. The
defect is that we treat the NVRAM entry as durable state when it is in fact
deploy-scoped state. Either it must be torn down and recreated every deploy
(and only after `parted` runs), which we *do* but the entry's referenced
PARTUUID still gets stale relative to the entry observed by the *next* boot
(the `efivars` write happens, the variable contains the right GUID at write
time, then the partition table changes underneath it on the next reimage —
because `removeStaleEntries` runs *before* `--create`, against pflash that is
already pointing at last-deploy's PARTUUID, while the disk was just
re-partitioned to have a different one). This is structurally fragile in a way
that survives any amount of `removeStaleEntries` polishing.

Defect 2 is independent and equally fatal: the binary at `\EFI\rocky\grubx64.efi`
is the **distro RPM-shipped binary preserved from the rootfs tar**, not the
binary `grub2-install` produces. `grub2-install --target=x86_64-efi
--removable --no-nvram` writes `\EFI\BOOT\BOOTX64.EFI` (225 KB, prefix
`(,gpt2)/grub2` baked in) and **does not overwrite `\EFI\rocky\grubx64.efi`**.
Section 2 conflated the two. Only the `BOOTX64.EFI` path has the prefix
needed to find `grub.cfg`. The `\EFI\rocky\grubx64.efi` path is dead weight
that happens to load and then immediately drop to a bare grub prompt.

Defect 3 follows from 2: we never write `\EFI\rocky\grub.cfg`, so even if the
binary at that path were good, it would still drop to `grub>`.

**Net**: the working post-deploy boot path is and has always been OVMF's
removable-media auto-discovery → `\EFI\BOOT\BOOTX64.EFI`. The NVRAM entry
created by `FixEFIBoot` is not a recovery mechanism — it is a competing,
broken path that the firmware tries first and that fails before the working
fallback even gets a chance.

### 8.2 Decision: Option B — delete the custom NVRAM entry, rely on UEFI removable-media auto-discovery

`FixEFIBoot`'s `efibootmgr --create` invocation is removed. We do not write a
named OS NVRAM entry at deploy time. UEFI firmware's removable-media boot
service (UEFI 2.x §3.5.1.1) is the canonical post-deploy boot path on every
deployment, every reimage, every cold start, every NVRAM wipe.

**Why this is correct, not just convenient:**

1. `\EFI\BOOT\BOOTX64.EFI` is rebuilt cleanly inside the chroot every deploy
   by `grub2-install --target=x86_64-efi --removable --no-nvram` (see
   `internal/deploy/finalize.go:1043-1054`). It has the right module set, the
   right prefix, and finds `grub.cfg` at `(,gpt2)/grub2/grub.cfg`. We have
   already verified this path works — it is the only path that has ever
   worked end-to-end on vm202.
2. UEFI removable-media discovery does not depend on PARTUUID. Firmware
   enumerates ESPs by PartitionType GUID (`C12A7328-F81F-11D2-BA4B-00A0C93EC93B`),
   not PartitionGUID. Reimaging the disk does not affect discoverability.
3. UEFI removable-media discovery does not depend on persistent NVRAM
   variables. AC loss, pflash wipe, cold-aisle reset, manufacturer NVRAM
   reset — all are recoverable. NVRAM-managed entries are not.
4. The Proxmox-level boot order (`qm set 202 --boot order=net0;scsi0`) and
   the equivalent BMC/IPMI boot order on real bare metal handle "PXE first,
   then disk." NVRAM `BootOrder` is redundant for that purpose and, given
   defect 1, actively harmful.
5. Secure Boot path is unchanged from current architecture: deferred. When
   we enable SB we'll switch the removable-media binary to `shimx64.efi`
   (signed) which loads `grubx64.efi` (signed) — same removable-media
   discovery, same no-NVRAM-required model.

### 8.3 Bare-metal validation

Question (from review): "Is removable-media auto-discovery consistent across
Dell iDRAC, HPE iLO, SuperMicro, Lenovo XClarity firmware?"

Answer: **Yes, with one operational caveat below.** Removable-media
auto-discovery is mandatory per UEFI 2.x §3.5.1.1 ("Boot Manager Policy for
Removable Media"). All four major server OEMs implement it:

| Vendor                    | Removable-media discovery   | Notes                                                                                  |
| ------------------------- | --------------------------- | -------------------------------------------------------------------------------------- |
| Dell PowerEdge (iDRAC)    | Yes, default-on             | Discovered ESPs surface as "Hard drive C: …" entries. Behaves identically to a named entry. |
| HPE ProLiant (iLO)        | Yes, default-on             | Surfaces as "Embedded RAID / Generic USB" depending on backplane. SmartArray controllers may need "UEFI Boot from Disk" enabled in BIOS Setup, factory-default-on. |
| SuperMicro (X11/X12/X13)  | Yes, default-on             | One known firmware quirk on X11SPL-F < BIOS 4.x: discovered entries are not persisted across `chassis power cycle` if NVRAM has *any* named OS entry. We don't write one — bug doesn't apply. |
| Lenovo XClarity (SR/ThinkSystem) | Yes, default-on      | Surfaces as "Local HDD". UEFI Setup → Boot Manager → "Boot from File" also works as fallback for ops. |

**Operational caveat — boot order at the BMC level:** On bare metal, the BMC
needs to know "try PXE first, then disk." This is set once at rack/stack
time via:

- Dell: `racadm set BIOS.BiosBootSettings.BootSeq NIC.Integrated.1-1-1, HardDisk.List.1-1`
- HPE:  `ilorest set BootOrder=NetworkBoot,HardDisk` (or via REST API)
- SMC:  `SUM` tool: `SetBiosOption Boot ... Boot#0001=Network`
- Lenovo: `OneCLI` or XClarity REST: same shape

This already exists in our ops runbook for rack provisioning. No code change.
The Proxmox equivalent (`qm set 202 --boot order=net0;scsi0`) is set in
`scripts/dev-vm/create.sh` (or wherever the VMs are bootstrapped — verify
this is the case for vm202 before testing).

### 8.4 OVMF "boot picker after iPXE exit" — disposition

Question (from review): "OVMF showing the boot picker after iPXE exit — does
that NOT happen on real firmware? Or do we need a workaround?"

This was over-stated in my prior diagnosis. Let me separate two distinct
behaviors:

1. **OVMF picker on cold boot when no boot target succeeds.** This is the
   "I have nothing to boot" failure mode. If `\EFI\BOOT\BOOTX64.EFI` is
   present and bootable, OVMF does NOT show the picker — it boots the
   binary. The picker we observed pre-fix was OVMF after the broken NVRAM
   entry failed *and* (in some test runs) before BOOTX64.EFI was found,
   depending on whether BootOrder had any other discoverable entry. Once
   the broken NVRAM entry is removed, the picker stops appearing.

2. **iPXE `exit` fallthrough.** On UEFI iPXE, `exit` returns
   `EFI_SUCCESS` from `StartImage`. The firmware's BDS (Boot Device
   Selection) phase then continues to the *next* `BootOrder` entry. This
   works on OVMF and on all four bare-metal vendors above. The
   misleading SeaBIOS-restarts-PXE quirk we documented in section 2 is
   specific to the BIOS path and does not apply here.

   The thing that *did* sometimes look like "OVMF re-runs PXE forever" in
   our testing was actually iPXE exiting → firmware retrying PXE entry
   (because that's next in BootOrder) → server returning the disk-boot
   script again → iPXE exiting again → firmware advancing to next entry
   → finding the broken NVRAM entry → loader fails → firmware shows
   picker. With the NVRAM entry deleted and Proxmox boot order set to
   `net0;scsi0`, the chain becomes: iPXE exits → firmware retries net0
   (PXE) → second iPXE exit → firmware advances to scsi0 → removable-media
   discovery on scsi0's ESP → boots `\EFI\BOOT\BOOTX64.EFI` cleanly.

   We accept the two-pass PXE on the post-deploy first boot. It costs
   roughly 3-5 seconds and is unavoidable given that the server is
   authoritative on "should this node be reimaged or boot from disk."
   This is the same behavior the original section 2 design described and
   it's correct.

3. **Optional optimization (defer): `BootNext`.** We could have
   `clustr-clientd` write `BootNext = <PXE entry>` on every clean shutdown
   so the next boot goes to PXE on the first try (saving the second
   pass). Or `BootNext = <disk discovery entry>` to skip PXE entirely on
   the next boot if we know reimage isn't pending. **Defer.** This is a
   reversible optimization and adds a clientd dependency on `efibootmgr`
   and on knowing the PXE entry's `Boot####` number, which varies by
   firmware. Not worth it until we measure boot-time as a real problem.

### 8.5 File-by-file change list for Dinesh

All paths absolute under `/home/ubuntu/sqoia-dev/staging/clustr/`.

#### Change 1 — remove the NVRAM entry creation from finalize

File: `internal/deploy/rsync.go`, lines 903–915 (the `Step 5: create/repair
the NVRAM boot entry` block, including the `reportStep`, the log line, the
`FixEFIBoot(...)` call, and its error-wrap).

Action: **delete the block entirely.** Replace with a single info log:

```go
log.Info().Msg("finalize: skipping NVRAM entry creation — relying on UEFI removable-media discovery of \\EFI\\BOOT\\BOOTX64.EFI (see docs/boot-architecture.md §8)")
```

The preceding `Step 4: verify grubx64.efi exists post-install` (lines
891–899) verifies `\EFI\rocky\grubx64.efi`. **Change** that verification to
also stat `\EFI\BOOT\BOOTX64.EFI` — that's now the load-bearing binary. The
`grubx64.efi` check can stay as a soft "did the chroot install run at all"
sanity check, but the `BOOTX64.EFI` check should be the one that escalates
to `BootloaderError` on miss.

Suggested replacement for the verification block (lines 891–899):

```go
reportStep("Verifying bootloader binary")
bootx64Path := filepath.Join(espMountPath, "EFI", "BOOT", "BOOTX64.EFI")
if _, err := os.Stat(bootx64Path); err != nil {
    return &BootloaderError{
        Targets: []string{d.targetDisk},
        Cause: fmt.Errorf("UEFI: grub2-install --removable exited 0 but %s is missing — "+
            "removable-media boot will fail: %w", bootx64Path, err),
    }
}
log.Info().Str("path", bootx64Path).Msg("  ✓ BOOTX64.EFI verified post-install (removable-media boot target)")
// Soft check: \EFI\rocky\grubx64.efi is the RPM-shipped binary, not load-bearing.
if _, err := os.Stat(grubx64Path); err != nil {
    log.Warn().Err(err).Str("path", grubx64Path).Msg("finalize: \\EFI\\rocky\\grubx64.efi missing (non-fatal — BOOTX64.EFI is load-bearing)")
}
```

The exact `espMountPath` variable name should match what's in scope at that
point — Dinesh: check the surrounding code, the variable holding the ESP
mount root is what's already used to compute `grubx64Path`.

#### Change 2 — remove the manual `clustr efi-boot` codepath in main.go

File: `cmd/clustr/main.go`, lines 682–710 (the `if flagFixEFI { ... }` block
inside the deploy flow).

Action: **delete the block entirely.** This is the legacy `--fix-efi` flag
codepath from the manual deploy command, which calls `FixEFIBoot` against
the deployed disk. With Option B it has no purpose. If `flagFixEFI` is
still defined, leave the flag for backwards compat but make it a no-op with
a deprecation warning.

File: `cmd/clustr/main.go`, lines 1215–1258 (the `if !layoutHasESP { ... }
else { ... }` block that calls `deploy.FixEFIBoot` from the autodeploy
path).

Action: **delete the entire `else` body (lines 1215–1258), keep the
`!layoutHasESP` info-log branch.** Replace the `else` with a single info
log:

```go
} else {
    deployLog.Info().
        Str("disk", deployer.ResolvedDisk()).
        Int("esp_part", effectiveESPPartNum).
        Msg("EFI boot setup: skipping NVRAM entry — relying on UEFI removable-media discovery (see docs/boot-architecture.md §8)")
}
```

The lengthy `NOTE: SetPXEBootFirst is intentionally NOT called here` comment
becomes obsolete with the block — drop it. The "PXE first" guarantee now
comes entirely from the Proxmox/BMC-level boot order, not NVRAM.

File: `cmd/clustr/main.go`, line 128, and the `newFixEFIBootCmd()` function
at line 1415.

Action: **leave `newFixEFIBootCmd()` in place but mark deprecated** in its
short/long help text. It's a manual operator escape hatch that may still be
useful for diagnostics on a node we don't want to reimage. Do not register
it as `clustr fix-efi-boot` if the registration line currently does so —
keep it registered, but the help text should say "DEPRECATED: clustr no
longer manages EFI NVRAM entries; use this only as a manual diagnostic.
See docs/boot-architecture.md §8."

#### Change 3 — gut `FixEFIBoot` and `removeStaleEntries`

File: `internal/deploy/efiboot.go`.

Action: **delete `FixEFIBoot`, `removeStaleEntries`, `parseNewBootNum`,
`setBootEntry`, `setBootOrderFirst`** (lines 17–207). They are no longer
called from the deploy or autodeploy paths. The `newFixEFIBootCmd` operator
escape hatch should call a renamed shim — recommend keeping a thin
`ManualCreateEFIEntry` exported function with a doc comment that says "ONLY
for manual diagnostic use — clustr does not call this in normal flow. See
docs/boot-architecture.md §8." Move it next to the parse helpers and
explicitly comment that it has the stale-PARTUUID hazard.

Or, simpler: delete `newFixEFIBootCmd` too (Change 2 alternative). Less
code to maintain.

**Keep** `EFIBootEntry`, `listBootEntries`, `parseBootOrder`, and
`SetPXEBootFirst`. Reason:

- `EFIBootEntry` and `listBootEntries` are general-purpose efibootmgr
  parsers used by `SetPXEBootFirst` and useful for diagnostics.
- `SetPXEBootFirst` is still potentially useful on bare metal where the
  BMC-level boot order is not configurable from inside the OS but
  efivar-mediated `BootOrder` is. We do not call it from the deploy path
  today (per the existing `NOTE` in main.go line 1249), but we may want to
  in the future. Keeping the function is cheap; pulling it out of the
  binary saves nothing.
- The unit tests for these (in `efiboot_test.go` if it exists, or
  `bootloader_test.go`) need to be updated only to drop coverage of the
  deleted functions.

#### Change 4 — Proxmox boot order verification (one-time ops, not code)

File: none (verification step).

Action (Gilfoyle, before we hand the test plan to Dinesh): confirm
`qm config 202 | grep ^boot` shows `boot: order=net0;scsi0`. If it doesn't:
`qm set 202 --boot order=net0;scsi0`. Same check on vm201 (BIOS — should be
`order=net0;scsi0` as well). Document the requirement in the dev-VM
provisioning script (`scripts/dev-vm/create.sh` or equivalent — Dinesh
locate and add a comment + the `--boot order=...` line if missing).

#### Change 5 — update tests

File: `internal/deploy/bootloader_test.go` and any `efiboot_test.go`.

Action:

- Delete tests that exercise `FixEFIBoot`, `removeStaleEntries`,
  `parseNewBootNum`, `setBootEntry`, `setBootOrderFirst`.
- Keep tests for `parseBootOrder`, `listBootEntries` parsing,
  `SetPXEBootFirst`.
- **Add** a finalize-path integration test (or extend existing) that
  asserts: (a) we do NOT shell out to `efibootmgr --create` during finalize
  on a UEFI deploy, (b) the post-finalize ESP contains `\EFI\BOOT\BOOTX64.EFI`,
  (c) `BootloaderError` is returned if `BOOTX64.EFI` is missing post-install.

#### Change 6 — update doc cross-references

File: `internal/pxe/boot.go` line 137 comment, and
`internal/server/handlers/boot.go` line 26 area.

Action: both currently say "the OS NVRAM entry written by FixEFIBoot loads
\EFI\rocky\grubx64.efi from the local ESP." Update to: "UEFI removable-media
discovery loads \EFI\BOOT\BOOTX64.EFI from the local ESP (no NVRAM
dependency — see docs/boot-architecture.md §8)."

### 8.6 Test plan

Replaces section 4 entirely for the UEFI path; section 4.1 (BIOS regression)
is unchanged.

#### 8.6.1 vm201 (BIOS / SeaBIOS) — regression check

Identical to section 4.1. We made no changes to the BIOS path. Pass criteria
unchanged: `reimage_pending` → deploy → reboot → `sanboot --drive 0x80` →
GRUB MBR → kernel → systemd → clustr-clientd phones home → `deployed_verified`.

#### 8.6.2 vm202 (UEFI / OVMF) — the real test

Pre-conditions:

1. `qm config 202 | grep ^boot` shows `boot: order=net0;scsi0`. If not, set
   it: `qm set 202 --boot order=net0;scsi0`.
2. Wipe vm202's pflash to simulate a clean NVRAM (catches any leftover
   broken `rocky10` entry from prior testing): `qm set 202 --delete
   efidisk0 && qm set 202 --efidisk0 local-lvm:0,efitype=4m,pre-enrolled-keys=0`.
   Confirm with `qm start 202` then check the OVMF setup screen shows no
   custom OS entries.
3. Confirm the new clustr build is on `cloner` (192.168.1.151) via
   autodeploy: `journalctl -u clustr-autodeploy.service -n 20`.

Test:

1. Mark vm202 `reimage_pending` via API.
2. Power-cycle (`qm reset 202`). Watch serial console.
3. Expect: PXE → iPXE → deploy initramfs → finalize:
   - `grub2-install --target=x86_64-efi --removable --no-nvram` runs in
     chroot; verification step confirms `\EFI\BOOT\BOOTX64.EFI` exists,
     `\EFI\rocky\grubx64.efi` may or may not (warn-only).
   - **NO `efibootmgr --create` is invoked.** Confirm in deploy log: no
     line matching `FixEFIBoot` or `efibootmgr.*--create`.
   - `efibootmgr -v` post-finalize shows only the firmware-default PXE
     entries plus any auto-discovered "UEFI QEMU HARDDISK" (or
     `\EFI\BOOT\BOOTX64.EFI`-discovered) entry. No `Rocky Linux` /
     `rocky10` named entry.
4. Reboot. iPXE menu → "Boot from disk" → `exit`.
5. OVMF behavior:
   - net0 retried (Proxmox boot order entry 1) → server returns disk-boot
     script (state is now `deployed_preboot`) → iPXE `exit`.
   - scsi0 tried (Proxmox boot order entry 2) → OVMF removable-media
     discovery finds the ESP via PartitionType GUID → loads
     `\EFI\BOOT\BOOTX64.EFI` → GRUB reads `(,gpt2)/grub2/grub.cfg` →
     kernel → systemd → clustr-clientd phones home.
6. State transitions: `reimage_pending` → `deploying` → `deployed_preboot`
   → `deployed_verified`.

Pass criteria: vm202 reaches `deployed_verified` without manual intervention
at the OVMF picker, on three consecutive reimage cycles (catches
intermittent PARTUUID/discovery races). Time from `reimage_pending` set to
`deployed_verified` should be within 10% of vm201's time for the same
image.

#### 8.6.3 NVRAM-wipe simulation (vm202)

After 8.6.2 succeeds, repeat step (2) of pre-conditions (delete + recreate
efidisk0). Reboot vm202 *without* setting `reimage_pending`. Expected: net0
PXE → server returns disk-boot script (state = `deployed_verified`) →
iPXE `exit` → scsi0 → BOOTX64.EFI → boots OS as if nothing happened. This
is the "AC loss / cold-aisle reset" scenario.

#### 8.6.4 Forced reimage during disk-boot menu (vm202)

Unchanged from section 4.4.

#### 8.6.5 Negative test: ensure no NVRAM entry leaks into the OS

After 8.6.2, ssh into the deployed vm202 and run `efibootmgr -v` from
inside the OS. Expected output: zero entries with label matching `Rocky`,
`Linux`, `clustr`, or any custom string. Only firmware/PXE entries plus
the auto-discovered hard-disk entry. If any custom OS entry is present, a
codepath we missed is still calling `efibootmgr --create`; grep the binary
and the deploy log to find it.

### 8.7 Bare-metal callouts (refined from section 5)

Section 5 still applies for the high-level "no server-side grub.efi"
guidance. Specific to Option B:

- **Dell PowerEdge / iDRAC**: removable-media discovery is on by default.
  Set `BIOS.BiosBootSettings.BootSeq` to `NIC.Integrated.1-1-1,
  HardDisk.List.1-1` once. No NVRAM management from clustr needed.
- **HPE ProLiant / iLO**: removable-media discovery is on by default.
  SmartArray controller must have "UEFI Boot from Disk" enabled (factory
  default). Set BootOrder via iLO REST API or `ilorest`. No NVRAM
  management from clustr needed.
- **SuperMicro X11/X12/X13**: removable-media discovery is on by default.
  X11 BIOS < 4.x has a known bug where discovered entries don't persist
  across `chassis power cycle` if NVRAM contains a named OS entry — Option
  B sidesteps this entirely because we don't write a named entry. Set
  BootOrder via `SUM` tool or IPMI.
- **Lenovo ThinkSystem / XClarity**: removable-media discovery is on by
  default. Surfaces as "Local HDD" in Boot Manager. Set BootOrder via
  XClarity REST or OneCLI.
- **Whitebox / Atom-class with volatile NVRAM**: removable-media discovery
  is the *only* working boot path. Option B is required, not optional, on
  these. Section 5's "the `--removable` fallback handles these cases" is
  now the primary path on every machine, not a fallback.

### 8.8 Rollback

If 8.6.2 fails: do NOT roll back to `FixEFIBoot`. The diagnosis in 8.1
showed that path was the source of failure. Instead:

1. From OVMF shell or rescue ISO, manually invoke `\EFI\BOOT\BOOTX64.EFI`
   on the ESP. If that boots: the bug is in BootOrder or auto-discovery,
   not in our code. Check `qm config 202` for the boot order setting.
2. If `\EFI\BOOT\BOOTX64.EFI` is missing or doesn't boot: the bug is in
   the chroot `grub2-install --removable` invocation. Check
   `internal/deploy/finalize.go:1043-1054`. This is a pre-existing failure
   that was being masked by the broken `FixEFIBoot` path getting tried first.
3. Worst-case true rollback: `git revert` the Option B commit. Returns to
   the post-section-7 state where vm202 was already broken. There is no
   "earlier good state" to roll back to — the broken state predates this
   sprint.

### 8.9 What changes for sections 1–7

Section 2 paragraph "We have *already* installed everything firmware needs
to find it" — items (2) and (3) are obsolete. Item (1) is correct and is
the only mechanism we use.

Section 3.4 "Keep everything else: ... `FixEFIBoot` NVRAM entry creation,
`SetPXEBootFirst` BootOrder management" — `FixEFIBoot` invocation is
removed. `SetPXEBootFirst` is preserved as code but is not called from the
deploy path (it wasn't being called anyway per main.go:1249's NOTE).

Section 4.2 step 3 second bullet (`efibootmgr --create → confirm "Rocky
Linux" entry visible`) — replaced by 8.6.2 step 3, which asserts the
opposite: no custom OS entry should be present.

All other sections (1, 3.1–3.3, 3.5–3.6, 4.1, 4.3, 4.4, 4.5, 5, 6, 7)
remain accurate.

---

## 9. WITHDRAWN — boot-order toggle as universal model

Status: WITHDRAWN — 2026-04-25
Withdrawn because: see §10 reader note. The toggle model is correct as the
*Proxmox-specific* implementation of `SetNextBoot`, but it is wrong as the
universal post-deploy contract. Bare-metal BMCs do not behave like Proxmox
config rewrites. Promoting the Proxmox quirk into the architecture would
ship a worse system to production.

Do not implement the section that was here. Implement §10.

---

## 10. Hybrid architecture: production = firmware auto-walk; Proxmox = isolated dev quirk

Status: ACCEPTED — 2026-04-25
Decision owner: Richard.
Supersedes: §9 in full; clarifies (does not change) §8.2's "the Proxmox-level
boot order and the equivalent BMC/IPMI boot order on real bare metal handle
'PXE first, then disk'" — that sentence was directionally right but
under-specified what each backend actually does. §10 specifies it.

### 10.1 Re-statement of the problem

§8 landed: `\EFI\BOOT\BOOTX64.EFI` written via `grub2-install --removable`
is the load-bearing post-deploy boot binary. NVRAM-managed entries are
removed. Routing is "PXE first → server returns disk-boot script on a
deployed node → iPXE `exit` → firmware advances `BootOrder` → loads
BOOTX64.EFI from the ESP."

§8 left two questions un-answered, and §9 answered both wrong:

1. **What sets "PXE first" persistently** — Proxmox config? IPMI? NVRAM
   `BootOrder`? §8 said "the Proxmox-level boot order and the equivalent
   BMC/IPMI boot order on real bare metal." That's a different mechanism
   on each backend, with different semantics, and the abstraction has to
   handle both cleanly.
2. **What "advances `BootOrder` after iPXE exit" actually does** on each
   backend. §8 implicitly assumed firmware auto-walk works everywhere.
   Diagnosis on vm202 showed OVMF on q35 with QEMU's default
   `-boot strict=on` does NOT auto-walk after PXE returns — it opens the
   Boot Manager picker. §9 misread this as a universal problem and proposed
   a universal fix; in fact it's a Proxmox-only artifact.

### 10.2 Bare-metal firmware behaviour (validation, not assumption)

UEFI 2.x §3.1.2 ("Globally Defined Variables") and §3.1.4 ("Boot Manager")
specify `BootOrder` semantics: the firmware boot manager loads the image
referenced by each `Boot####` entry **in order** until one returns
`EFI_SUCCESS` from a useful boot OR the list is exhausted. A returning PXE
NIC entry (iPXE `exit` returns `EFI_SUCCESS`) is treated as "this entry
finished without booting an OS" and the manager **must** advance to the
next entry. This is mandatory firmware behaviour, not a vendor option.

Vendor-specific confirmation:

| Vendor                | Auto-walk after returning network entry | Citation                                                                                          |
| --------------------- | --------------------------------------- | ------------------------------------------------------------------------------------------------- |
| Dell PowerEdge / iDRAC| Yes, default                            | Dell BIOS/UEFI Configuration Reference Guide §"Boot Sequence" — sequential evaluation of BootSeq  |
| HPE ProLiant / iLO    | Yes, default                            | HPE UEFI System Utilities User Guide — "One-Time Boot Menu" semantics; persistent BootOrder walked sequentially |
| SuperMicro X11/X12/X13| Yes, default                            | Supermicro UEFI BIOS User Guide §"Boot" — "Boot Option Priorities" walked sequentially            |
| Lenovo ThinkSystem    | Yes, default                            | Lenovo XClarity Controller Boot Manager docs — sequential evaluation of `BootOrder`               |
| EDK2/TianoCore (ref)  | Yes by spec; gated by `strict` on QEMU  | EDK2 `BdsDxe` source, `BdsBootDeviceSelect()`; QEMU `-boot strict=on` blocks fallthrough          |

Comparable HPC provisioners that target both bare-metal and VMs handle this
the same way:

- **xCAT** (`nodeset` → `osimage`): sets BootOrder to NIC-first via OEM
  tooling at install time; expects firmware auto-walk to disk after PXE
  returns the "boot from disk" iPXE script. No NVRAM-management daemon.
  Reference: xCAT-2.16 docs, "Configure BMC IPMI Boot Order".
- **Warewulf 4**: identical model. PXE-first is set once via BMC at
  commissioning. Per-deploy boot routing is done in iPXE on the server
  side. No persistent NVRAM mutation per node from Warewulf.
- **MAAS**: uses `chassis bootdev pxe` IPMI override **only** for
  forced reimage commissioning. Steady-state nodes have BMC-level boot
  order disk-first; reimage = one-shot override + power cycle.
- **Cobbler / Foreman**: same pattern; one-shot IPMI override for
  reinstall; firmware default boot order handles steady state.

The four major systems all converge on the same model: **persistent BMC
boot order set once at commissioning + one-shot override for reimage +
firmware auto-walk handles fallthrough.** None of them rewrite persistent
boot order on every deploy.

`OVMF + q35 + strict=on` is the outlier. It's not "OVMF" — it's the QEMU
boot-strictness flag that Proxmox sets by default. Proxmox sets `strict=on`
to make boot-order changes deterministic for users (the displayed boot
order is exactly what the VM tries; no surprises from firmware reading
disk signatures). That's a reasonable Proxmox UX choice and a hostile one
for our use case.

### 10.3 The architectural decision: Hybrid (Option C from the problem statement)

clustr is architected for **bare-metal HPC nodes**. The Proxmox dev
environment is a convenience for development, not the production target.
The architecture is therefore:

1. **Production (bare metal, IPMI provider)**: persistent BMC boot order is
   `[disk, network]`, set ONCE at commissioning via vendor tooling
   (`racadm`, `ilorest`, `SUM`, OneCLI). Set by an operator, not by
   clustr. clustr's role on reimage is to issue a **one-shot IPMI override**
   `chassis bootdev pxe options=efiboot` followed by `chassis power cycle`.
   The override is consumed on next boot (BMC firmware semantics — IPMI
   spec §28). After deploy completes and iPXE `exit`s, firmware
   auto-walks `BootOrder` to disk per §10.2. **clustr never permanently
   mutates the BMC boot order** — that's an operator-owned commissioning
   decision.

2. **Dev (Proxmox, Proxmox provider)**: persistent VM boot order is
   `[disk, network]`, set ONCE at VM creation via the dev-VM provisioning
   script. clustr's role on reimage is to **toggle the persistent boot
   order to network-first**, **cold-cycle the VM** (so Proxmox commits
   the pending config), then **after deploy completes and the node has
   verified, toggle the persistent boot order back to disk-first** and
   leave it there. This is the "boot-order toggle" model — but it is a
   Proxmox-implementation-specific interpretation of `SetNextBoot`, not
   the universal contract.

3. **The Provider abstraction's `SetNextBoot(dev)` semantically means
   "make `dev` the boot target on the next boot, then default."** The IPMI
   provider implements this by issuing a non-persistent IPMI bootdev
   override (which is genuinely one-shot per IPMI spec). The Proxmox
   provider implements this by writing the persistent config to put `dev`
   first, and after the deploy completes the orchestrator calls
   `FlipToDisk` (which writes the persistent config back to disk-first).
   The two implementations have the same *observable* behaviour from the
   orchestrator's point of view: "next boot goes to `dev`, subsequent
   boots default." That's what matters. The fact that the Proxmox impl
   is two writes vs IPMI's one is a backend implementation detail that
   stays inside the provider package.

This is **Option C** from the problem statement, with one clarification:
the abstraction surface stays as it is today (`SetNextBoot` +
`SetPersistentBootOrder`). We do not introduce a capability flag. We do
not add `SetNextBootOnce` vs `SetPersistentBootOrder` as separate methods
beyond what already exists. The abstraction is correct; the Proxmox
*implementation* of it is what needs to be fixed.

### 10.4 Why not Option A (firmware-walks everywhere, document Proxmox as known dev failure)

Tempting because it's the simplest model and it's exactly what every
production HPC system does. Rejected because:

- **Dev environment must work.** vm201 (BIOS) and vm202 (UEFI) are how
  every clustr engineer iterates. "Manually click through the OVMF picker
  on every reimage" is not acceptable; it makes the inner-loop dev
  experience worse than xCAT's. Eight engineers × five reimages/day ×
  manual intervention = the dev environment becomes a tax.
- **`-boot strict=off` workaround is brittle.** Proxmox does not expose
  `strict` as a first-class config option; it's set via the `args:` field
  which is "expert mode" and Proxmox warns against it. It will be a
  forgotten footgun the moment a new dev VM is provisioned without it,
  and the failure mode (boot picker on every reimage) is silent.
- **The Proxmox provider can absorb the quirk cleanly.** It already has
  full control of the VM config via the API. Toggling boot order on
  reimage trigger, then flipping back on deploy completion, is two HTTP
  PUTs the provider already knows how to make. The complexity is contained
  to one file (`internal/power/proxmox/provider.go`). It does not leak
  into the orchestrator, the deploy pipeline, the iPXE templates, or the
  doc the way a "you must edit args: in dev VMs" workaround would.

### 10.5 Why not Option B (universal one-shot semantic, IPMI does override + Proxmox does cold-cycle)

This was §9's pitch and it's wrong because it conflates "what the
abstraction guarantees" with "how each backend achieves it":

- Option B says `SetNextBoot` should *always* trigger a cold cycle as part
  of its contract. That's wrong on IPMI. IPMI `chassis bootdev` is a
  pre-`chassis power cycle` setup call; the cycle is the caller's
  decision, not part of `SetNextBoot`. Folding the cycle into the call
  removes the orchestrator's ability to batch-flip multiple nodes before
  cycling them (a real HPC requirement when reimaging a 256-node group;
  see `internal/reimage/group.go`).
- Option B also says the Proxmox impl's "cold cycle to commit pending
  config" is genuinely one-shot. It isn't — the config is persistent
  until clustr writes it back. If the orchestrator calls `SetNextBoot(PXE)`
  and then crashes before the deploy completes, the VM's persistent
  config is now PXE-first forever. That's a bigger bug than what we're
  fixing.

The right model is: `SetNextBoot` is a one-shot intent at the orchestrator
layer. The Proxmox provider compensates for Proxmox's lack of true
one-shot semantics by ensuring the post-deploy `FlipToDisk` call always
runs and is idempotent — which is what the existing belt-and-suspenders
`FlipToDisk` call at `cmd/clustr/main.go:1320` is for, *if* it actually
runs after a cold cycle (it does today, but only because it's called from
inside the deploy script after the OS has booted, which is a fragile
sequencing assumption — see §10.7).

### 10.6 Provider contract clarification (no signature change)

`internal/power/power.go` interface is unchanged. The doc comments are
sharpened to nail down the semantics:

```go
// SetNextBoot sets the boot target for the NEXT boot of this node, after
// which the node returns to its persistent default boot order.
//
// Semantics MUST be observable as one-shot from the orchestrator's
// perspective. Implementations MAY achieve this differently:
//
//   - IPMI: issues a non-persistent chassis bootdev override; consumed on
//     next boot per IPMI spec §28. Pair with PowerCycle to actually boot.
//
//   - Proxmox: writes the persistent VM boot order to put `dev` first,
//     because Proxmox has no one-shot concept. The caller is responsible
//     for restoring the default (disk-first) order after the deploy
//     completes via SetPersistentBootOrder([BootDisk, BootPXE]). The
//     orchestrator's post-deploy FlipToDisk codepath is what makes the
//     observable behaviour one-shot end-to-end.
//
// Callers MUST NOT assume SetNextBoot implies a power cycle — issue
// PowerCycle separately. Callers MUST issue SetPersistentBootOrder
// after a successful deploy when SetNextBoot was used to flip to PXE
// (the Proxmox provider needs this; the IPMI provider treats it as a
// no-op-ish reaffirmation, which is fine).
SetNextBoot(ctx context.Context, dev BootDevice) error

// SetPersistentBootOrder sets the persistent boot order for this node.
//
// On Proxmox: writes the VM config and (CRITICAL) cold-cycles the VM if
// the VM is currently running and pending-config-commit is required for
// the change to take effect on the next boot. Returns once the new
// order is in the running config.
//
// On IPMI: best-effort writes the BMC's persistent BootOrder via the
// vendor-appropriate path (Dell racadm, HPE ilorest, etc.) when the
// vendor adapter supports it; returns ErrNotSupported when it doesn't.
// In production, persistent BootOrder is operator-owned at commissioning
// and clustr should not call this on IPMI nodes during normal operation.
SetPersistentBootOrder(ctx context.Context, order []BootDevice) error
```

The behavioural change is in the **Proxmox implementation**, not the
interface.

### 10.7 The actual bug from the problem statement, restated in §10 terms

The bug Gilfoyle's diagnostics #4 documented is real and is in the Proxmox
provider:

> Proxmox applies boot-order config changes only on full stop+start.
> `PowerCycle` issues `reset` (warm), so pending changes never apply.
> Currently vm202 has running `boot: order=net0;scsi0` and pending
> `boot: order=scsi0;net0` (from `FlipToDisk` after a prior deploy that
> never took effect because reset doesn't commit pending config).

Root cause in §10.6 contract terms: the Proxmox provider's
`SetPersistentBootOrder` (called via `FlipToDisk` after deploy) writes the
config but does NOT cold-cycle to commit it. The next `PowerCycle` is a
warm reset (per `internal/power/proxmox/provider.go:176` — `Reset` calls
`/status/reset`), which Proxmox treats as a runtime restart that does not
flush pending VM config to the running state.

Fix lands in **two places** in the Proxmox provider:

1. `SetPersistentBootOrder` writes the config AND, if the VM is currently
   `running`, performs a stop+start (not reset) to commit. This makes the
   "flip back to disk-first" path actually take effect.
2. `SetNextBoot(dev)` writes the config AND, if the VM is currently
   `running`, performs a stop+start. Then a subsequent `PowerCycle` (or
   the orchestrator's natural deploy-then-cycle flow) boots into the new
   order. Note: in the reimage orchestrator path,
   `provider.SetNextBoot(BootPXE)` is followed by `provider.PowerCycle`
   (`internal/reimage/orchestrator.go:145–166`). On Proxmox we want the
   `SetNextBoot` call to leave the VM in a state where the *next* power
   transition boots PXE. The cleanest implementation is: `SetNextBoot`
   does `stop` + `config-write` + `start` directly when the VM is running,
   and is a `config-write` + nothing else when the VM is stopped (the
   orchestrator's subsequent `PowerCycle`/`PowerOn` will boot into the
   new order). This means `PowerCycle` after `SetNextBoot` on a running
   VM becomes a no-op or a redundant restart — that's fine; idempotency
   beats double-effort optimization.

### 10.8 Bare-metal vs Proxmox semantics — the explicit table

| Concern                                   | Bare metal (IPMI provider)                                                                  | Proxmox VM (proxmox provider)                                                                            |
| ----------------------------------------- | ------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| Persistent boot order, who sets it        | Operator at commissioning via vendor tool (`racadm`, `ilorest`, etc.); never by clustr      | Dev-VM provisioning script (`scripts/dev-vm/create.sh`); set to `order=scsi0;net0` (disk-first)          |
| Persistent boot order, what it is         | `[disk, network]` (disk-first)                                                              | `order=scsi0;net0` (disk-first)                                                                          |
| `SetNextBoot(BootPXE)` impl               | `chassis bootdev pxe options=efiboot,persistent=false`; verified via bootparam read-back    | Stop VM (if running) → `PUT config boot=order=net0;scsi0` → start VM (if was running)                    |
| `SetNextBoot` semantics observable        | One-shot per IPMI spec — consumed on next boot                                              | Persistent until `SetPersistentBootOrder([disk, pxe])` is called post-deploy                             |
| Post-deploy "flip back to disk"           | No-op (override was one-shot; persistent order is already disk-first)                       | `SetPersistentBootOrder([BootDisk, BootPXE])` → stop → `PUT config boot=order=scsi0;net0` → start        |
| What happens after iPXE `exit` post-deploy| Firmware auto-walks BootOrder to next entry (disk) per UEFI spec; loads BOOTX64.EFI         | Currently fails (OVMF `strict=on` shows picker). With §10 fix: persistent order is already disk-first, so iPXE exit hands control back and firmware tries scsi0 → BOOTX64.EFI |
| Reboot mid-deploy (power flap)            | Override might re-fire if BMC implements `boot_flags_valid`; safe — node PXEs again         | New persistent order is PXE-first; node PXEs again; deploy resumes — safe                                |
| Deploy crash / orchestrator dies          | Override is one-shot; node PXEs once, server returns disk-boot script (state still `deploying`), node tries to disk-boot from a half-installed disk → fails to boot → operator intervenes | Persistent order is PXE-first FOREVER until manual cleanup. Mitigation: orchestrator timeout sets state to `deploy_failed` and proactively calls `SetPersistentBootOrder([disk, pxe])` |

### 10.9 File-by-file change list for Dinesh

All paths absolute under `/home/ubuntu/sqoia-dev/staging/clustr/`.

#### Change 1 — fix the Proxmox provider to make config writes actually take effect

File: `internal/power/proxmox/provider.go`

Action 1a: replace `SetNextBoot` (lines 184–190) with an implementation
that, after writing the config, ensures the new order is in the *running*
state. Pseudocode:

```go
func (p *Provider) SetNextBoot(ctx context.Context, dev power.BootDevice) error {
    status, err := p.Status(ctx)
    if err != nil {
        return fmt.Errorf("proxmox: SetNextBoot: pre-check status: %w", err)
    }
    if err := p.setBootOrder(ctx, dev); err != nil {
        return err
    }
    if status == power.PowerOn {
        // Proxmox commits pending VM config on stop+start, NOT on reset.
        // Without this, subsequent PowerCycle (which is /status/reset)
        // boots the OLD order and the deploy never PXEs.
        if err := p.PowerOff(ctx); err != nil {
            return fmt.Errorf("proxmox: SetNextBoot: stop to commit pending config: %w", err)
        }
        if err := p.waitForStatus(ctx, power.PowerOff, 30*time.Second); err != nil {
            return fmt.Errorf("proxmox: SetNextBoot: wait for stop: %w", err)
        }
        if err := p.PowerOn(ctx); err != nil {
            return fmt.Errorf("proxmox: SetNextBoot: start after config commit: %w", err)
        }
    }
    return nil
}
```

Add `waitForStatus(ctx, want PowerStatus, timeout time.Duration) error` as
a small helper that polls `Status` every 500ms until match or timeout.

Action 1b: replace `SetPersistentBootOrder` (lines 192–201) with the same
shape: write config, if running stop+start to commit. Same helper.

Action 1c: leave `setBootOrder`, `PowerOn`, `PowerOff`, `Reset`,
`PowerCycle` as they are. `PowerCycle` semantics stay correct (warm reset
when running). The orchestrator's `PowerCycle` after `SetNextBoot` is now
a redundant kick; harmless because §10.7's approach has already booted
the VM into the new order via stop+start. If we want to clean up later,
add a Provider-level optimization where `SetNextBoot` on Proxmox returns
a sentinel ("already booted into new order") and the orchestrator
short-circuits the subsequent `PowerCycle`. **Defer that** — premature
optimization, and the orchestrator's "PowerCycle is always called after
SetNextBoot" invariant should not be broken to save one HTTP call.

Action 1d: update the package doc-comment at the top of the file. Replace
"Authentication: …" preamble's silence on boot-order with a paragraph:

```
// Boot-order semantics: Proxmox persists VM config changes ONLY on
// stop+start. /status/reset (warm reset) does not commit pending boot
// changes. Therefore SetNextBoot and SetPersistentBootOrder on a running
// VM perform an explicit stop → config-write → start sequence so the
// new order takes effect on the next boot. This is the Proxmox-specific
// implementation of the one-shot SetNextBoot semantic documented in
// internal/power/power.go. See docs/boot-architecture.md §10.
```

#### Change 2 — sharpen the Provider interface doc comments

File: `internal/power/power.go`

Action: replace the doc comments on `SetNextBoot` (lines 57–60) and
`SetPersistentBootOrder` (lines 62–65) with the text from §10.6 above.
**Do not change the signatures.** Add a one-line reference at the package
doc comment (line 1): `// See docs/boot-architecture.md §10 for the
contract semantics across IPMI and Proxmox.`

#### Change 3 — orchestrator: ensure post-deploy flip-back always runs on Proxmox

File: `internal/reimage/orchestrator.go`

Action: audit the post-deploy success path. The orchestrator's
responsibility on success is `SetPersistentBootOrder([BootDisk, BootPXE])`
to move the persistent default back to disk-first (no-op on IPMI,
critical on Proxmox). Today that flip happens in
`cmd/clustr/main.go:1320` via `FlipToDisk`, which is called from inside
the deploy initramfs after the rsync/finalize completes — i.e. while the
node is still running the deploy initramfs, before the post-deploy
reboot. That ordering is fine for IPMI (no-op) and for Proxmox, *as long
as the FlipToDisk call's HTTP path to the clustr server succeeds*.

Make this more robust:

- **Server-side authoritative flip**: when the node phones home and state
  transitions to `deployed_verified` (handler in `internal/server/handlers/nodes.go`
  that handles the verify-boot phone-home), the server itself should
  call `provider.SetPersistentBootOrder(ctx, []power.BootDevice{power.BootDisk, power.BootPXE})`.
  This makes the flip-back idempotent and orchestrator-driven, not
  client-initiated.
- **Keep** the existing client-initiated `FlipToDisk` at
  `cmd/clustr/main.go:1320` as a belt-and-suspenders early flip (helps in
  cases where the node verify phase is slow but we want the persistent
  order corrected as soon as deploy artifacts are written). It's a
  duplicate-effort that's safe to leave.
- **Add an orchestrator timeout-failure flip**: if the deploy times out
  in `internal/reimage/orchestrator.go`'s post-`PowerCycle` watch loop,
  call `provider.SetPersistentBootOrder(ctx, []power.BootDevice{power.BootDisk, power.BootPXE})`
  before transitioning the node to `deploy_failed`. This prevents the
  "Proxmox VM stuck PXE-first forever" failure mode in §10.8's last row.

#### Change 4 — Proxmox dev-VM provisioning: assert disk-first persistent order

File: `scripts/dev-vm/create.sh` (or wherever vm201/vm202 are bootstrapped
— Dinesh: locate. Likely `scripts/` or `deploy/`).

Action: ensure the script ends with `qm set <vmid> --boot order=scsi0;net0`
(disk-first persistent order). Add a comment referencing §10. If the
script doesn't exist (VMs were created by hand), document the requirement
in `docs/dev-environment.md` (create if missing) and add a short shell
snippet operators can run.

#### Change 5 — recover vm202's current broken state

File: none (one-time ops; Gilfoyle).

Action: vm202 currently has `running boot: order=net0;scsi0` and `pending
boot: order=scsi0;net0`. Stop the VM (`qm stop 202`), then start it
(`qm start 202`) to commit the pending config. Verify with
`qm config 202 | grep ^boot` shows `boot: order=scsi0;net0`. This is a
one-time cleanup; once Change 1 ships, the same situation can't recur.

#### Change 6 — IPMI provider doc-comment refinement

File: `internal/power/ipmi/provider.go`

Action: the package doc-comment (lines 1–22) currently says "SetNextBoot
uses persistent boot overrides by default so the setting survives across
power cycles and is not consumed if the node reboots mid-deploy." That
overloads the word "persistent." Clarify:

```
//   - SetNextBoot uses IPMI's "persistent flag" on the bootdev override.
//     This is IPMI-spec terminology for "the override survives a
//     mid-boot crash or power flap until the node successfully boots
//     once" — it is NOT the same as setting a persistent BootOrder via
//     the BMC's UEFI settings. After the node boots once, the override
//     is consumed and the BMC's persistent BootOrder takes over.
//     This matches the one-shot semantics documented in
//     internal/power/power.go SetNextBoot. See docs/boot-architecture.md §10.
```

No code change. Just terminology clarification so future readers don't
conflate "persistent override" (what the IPMI flag does) with "persistent
BootOrder" (what `SetPersistentBootOrder` does).

#### Change 7 — tests

Files:

- `internal/power/proxmox/provider_test.go` (create if missing)
- `internal/reimage/orchestrator_test.go`

Action 7a (Proxmox): mock the Proxmox HTTP API (httptest server). Test:

- `SetNextBoot(BootPXE)` on a running VM issues PUT config + POST stop +
  POST start, in that order.
- `SetNextBoot(BootPXE)` on a stopped VM issues PUT config only.
- `SetPersistentBootOrder([BootDisk, BootPXE])` on a running VM issues
  PUT config (with `order=scsi0;net0`) + POST stop + POST start.
- `SetNextBoot` returns an error if the stop-or-start request fails.

Action 7b (orchestrator): the existing `fakeProvider` in `group_test.go`
satisfies the interface; add an assertion that on deploy success the
orchestrator calls `SetPersistentBootOrder([BootDisk, BootPXE])` (record
the call on the fake provider). Add an assertion that on deploy failure
(timeout), the same call is made.

Action 7c (test plan vm202 + bare metal): see §10.10.

#### Change 8 — doc cross-references

File: `internal/power/proxmox/provider.go` line 1, `internal/power/ipmi/provider.go` line 1,
`internal/reimage/orchestrator.go` line 1, `cmd/clustr/main.go` near the
`FlipToDisk` call site (~line 1320).

Action: each gets a one-line reference comment: `// See docs/boot-architecture.md §10`.

### 10.10 Test plan

Replaces §8.6 *additions* (does not replace §8.6 — the BOOTX64.EFI/NVRAM
assertions there still hold). Adds boot-order specific assertions.

#### 10.10.1 Proxmox dev (vm202) — primary test

Pre-conditions:

1. After Change 5 cleanup: `qm config 202 | grep ^boot` shows
   `boot: order=scsi0;net0`. **No pending changes** (`qm pending 202`
   shows nothing for `boot:`).
2. New clustr build is on `cloner` via autodeploy.
3. vm202 state in clustr is `deployed_verified`.

Test:

1. Mark vm202 `reimage_pending` via API.
2. Orchestrator fires: confirm in clustr-serverd logs that
   `SetNextBoot(BootPXE)` is called. On the Proxmox API side (check
   `journalctl -u pveproxy.service` on the PVE host or run
   `pvesh get /cluster/tasks --limit 10` in parallel), confirm: PUT
   `/nodes/pve/qemu/202/config` (boot=order=net0;scsi0), POST
   `/nodes/pve/qemu/202/status/stop`, POST
   `/nodes/pve/qemu/202/status/start`.
3. Then orchestrator calls `PowerCycle` (POST `/status/reset` since VM
   is now running). VM resets — boots into new (PXE-first) order.
4. iPXE → deploy → finalize → `\EFI\BOOT\BOOTX64.EFI` written to ESP.
5. Late in deploy script, `FlipToDisk` is called from
   `cmd/clustr/main.go:1320`. Confirm in clustr-serverd logs: PUT
   `/nodes/pve/qemu/202/config` (boot=order=scsi0;net0), POST
   `/status/stop`, POST `/status/start`. **At this point the running
   config is disk-first.** This is critical: the next reboot must boot
   disk → BOOTX64.EFI, not PXE.
6. Node reboots after deploy script completes. iPXE script may run
   briefly if Proxmox's "stop/start" left the VM at a fresh boot menu
   that includes net0 — verify in OVMF console: net0 is tried first,
   server returns disk-boot script (state is `deployed_preboot`), iPXE
   `exit`. **With persistent order now `scsi0;net0`, scsi0 is next** —
   firmware loads BOOTX64.EFI from ESP cleanly. No picker.

   *Note:* If after Change 1 lands, the running config at step 6 is
   already `scsi0;net0` (because Change 1 step 1c keeps the orchestrator's
   `PowerCycle` semantics intact), then on the post-deploy reboot the VM
   boots scsi0 first, doesn't even hit PXE, and the post-deploy
   verify-boot phone-home happens immediately. That's the optimal flow
   and is what we expect. The two-pass PXE described in §8.4.2 only
   happens on the *re-image* path (where order is intentionally
   PXE-first), not on post-deploy.
7. clustr-clientd phones home → state transitions to
   `deployed_preboot` → `deployed_verified`.
8. Server-side flip-back (Change 3): on `deployed_verified` transition,
   `SetPersistentBootOrder([BootDisk, BootPXE])` is called. This is a
   no-op since order is already `scsi0;net0` (the running config is
   already correct from step 5). Confirm the call is idempotent — PUT
   should succeed, stop+start should still happen (or, optimization: if
   running config matches desired, skip the stop+start). For now, accept
   the redundant restart; verify it doesn't disrupt the
   `deployed_verified` state.

Pass criteria: vm202 reaches `deployed_verified` without manual
intervention at the OVMF picker on three consecutive reimage cycles.
Final running config of vm202 after each cycle: `boot: order=scsi0;net0`.

#### 10.10.2 Bare metal — deferred test (no hardware available in dev)

We do not have bare-metal HPC nodes in the dev environment to test §10's
IPMI path live. The IPMI provider has not changed in this sprint (only
its doc comments). Pass criteria for bare metal is structural:

- `internal/power/ipmi/provider.go` `SetNextBoot` continues to call
  `chassis bootdev` with `Persistent: true` (IPMI-spec persistent flag,
  meaning "survive mid-boot crash, consumed on next boot") — verified by
  reading the code, no behaviour change.
- The orchestrator's call sequence on bare metal is:
  `SetNextBoot(BootPXE)` → `PowerCycle` → deploy → (iPXE exit, firmware
  auto-walks to disk per §10.2) → `clustr-clientd` phones home →
  `SetPersistentBootOrder` is a no-op or `ErrNotSupported` (the IPMI
  provider's current impl just calls `SetNextBoot(order[0])`, which
  reissues the override — harmless).
- First bare-metal node deployment (when we get hardware) is itself the
  test. Add an integration-level smoke test there.

#### 10.10.3 Negative test: orchestrator crash mid-deploy on Proxmox

Simulate: mark vm202 `reimage_pending`, watch orchestrator call
`SetNextBoot(BootPXE)` (running config now `net0;scsi0`), then `kill
-9` clustr-serverd before deploy completes. Restart clustr-serverd.
Observe state.

Expected with Change 3: on next state-reconcile sweep (or manual
inspection), the orchestrator notices the in-flight deploy is stale
(deploy timeout exceeded) and calls
`SetPersistentBootOrder([BootDisk, BootPXE])` to recover the persistent
order, transitioning state to `deploy_failed`. Operator can then mark
the node `reimage_pending` again to retry. Without Change 3, the VM
would be PXE-first forever and any subsequent reboot would re-trigger
the deploy initramfs.

#### 10.10.4 BIOS regression (vm201)

Unchanged from §4.1 / §8.6.1. BIOS path uses `sanboot --drive 0x80`, not
`exit`, so firmware auto-walk is irrelevant. With the Proxmox provider
fix, vm201's reimage path goes from `SetNextBoot(BootPXE)` (now
stop+config+start) → `PowerCycle` → iPXE → deploy → flip back to
`scsi0;net0` → reboot → `sanboot` works. Verify three reimage cycles
land at `deployed_verified`.

### 10.11 What changes for §8

§8 remains correct in its NVRAM/BOOTX64.EFI decision. §8.4.2 ("two-pass
PXE") was over-stated — with the §10 fix, post-deploy first boot is a
*single*-pass: persistent order is disk-first, so the post-deploy reboot
goes straight to disk. The two-pass behaviour was a symptom of the
config-not-committed bug, not an inherent design property. §8.4.3
("BootNext optimization") remains deferred and is now even less needed.

§8.7's bare-metal callouts remain correct; they describe the operator's
one-time commissioning step (set persistent boot order via vendor tool),
which is exactly §10.3 item 1.

### 10.12 Architectural confidence + kill criteria

Confidence: **high (85%)** that this is the right model for production
bare metal. The four reference systems (xCAT, Warewulf, MAAS, Cobbler)
all converge on it; UEFI 2.x specifies it; vendor docs confirm it.

Confidence: **high (90%)** that the Proxmox-specific stop+start fix is
correct. Diagnostic #4 isolates the exact mechanism; the Proxmox API
docs confirm config commit semantics.

Kill criteria — signals that §10 is wrong and we need to revisit:

1. Bare-metal HPC node, after reimage, fails to boot disk despite
   firmware showing disk-first BootOrder. (Indicates UEFI auto-walk is
   not actually universal — would require Option C variant where IPMI
   provider also rewrites BootOrder per deploy.)
2. Proxmox's stop+start sequence introduces a race where another consumer
   of the Proxmox API (e.g. the dev environment cluster manager)
   competes for VM-config writes. (Indicates we need a per-VM lock at the
   provider level — not hard, but a refactor.)
3. The "orchestrator-driven flip-back on `deployed_verified`" runs but
   races a power flap, leaving the VM in `running` config PXE-first
   while persistent config is disk-first. (Indicates the
   stop+start-to-commit pattern has a subtle window — would require a
   verify-then-retry loop in the Proxmox provider.)

If any of these fire, revisit §10 — don't paper over with workarounds.
