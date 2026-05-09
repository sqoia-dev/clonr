-- Migration 105 — Sprint 34 BOOT-POLICY + BOOT-SETTINGS-MODAL.
--
-- Adds three columns to node_configs that drive boot routing during deploys
-- and at-rest persistent boot menu choices.
--
-- Background — why these three together
-- --------------------------------------
-- Up to v0.1.22 the deploy pipeline relied on a *reactive* NVRAM repair
-- (`internal/deploy/efiboot.go:RepairBootOrderForReimage`) that re-orders
-- the BootOrder NVRAM variable AFTER grub2-install runs in the chroot
-- (#225 FIX-EFI).  The repair has two failure modes:
--
--   1.  It assumes "PXE first" is correct for every node.  A login or
--       service node that an operator wants to boot from disk still gets
--       its OS entry shoved behind PXE on every reimage.
--   2.  It runs only at deploy time.  An operator who manually flips the
--       order via efibootmgr or vendor BIOS UI has no way to make the
--       desired order *durable* — the next reimage clobbers it.
--
-- Sprint 34 BOOT-POLICY replaces the reactive repair with an *explicit*
-- per-node policy.  Operators choose the boot order semantic (`network`,
-- `os`) and finalize.go threads it into a single `efibootmgr -o <order>`
-- call rather than the heuristic SetPXEBootFirst dance.
--
-- BOOT-SETTINGS-MODAL piggybacks on the same migration because the modal
-- writes the same row (one ALTER per column is one transaction; no point
-- in fragmenting).  The two settings columns are:
--
--   `netboot_menu_entry`     refs an existing rows id in `boot_entries`
--                            (#160).  When set and the node is NOT in an
--                            active reimage flow, ServeIPXEScript chains
--                            to that entry instead of the default disk
--                            boot menu.  Operator workflow: "Boot this
--                            node into rescue on next PXE."
--
--   `kernel_cmdline`         appended verbatim to the kernel cmdline of
--                            the chained entry (and the deploy initramfs
--                            for nodes in reimage_pending).  Use cases:
--                            forcing console=ttyS0 on serial-only nodes,
--                            adding nomodeset for buggy GPUs.
--
-- Validation discipline
-- --------------------
-- `boot_order_policy` is a CHECK-constrained TEXT column with a fixed
-- enum.  `'auto'` is the back-compat default — finalize.go reads it as
-- "network" semantics so existing deployed nodes do NOT change behaviour
-- on the upgrade.  When the operator picks an explicit policy the
-- semantic shifts (see internal/deploy/efiboot.go:ApplyBootOrderPolicy).
--
-- `netboot_menu_entry` is a TEXT column with no FK to `boot_entries`.
-- Reasons:
--   - boot_entries is a small read-mostly catalog; a stale FK can leave
--     a node un-bootable if an entry is renamed/deleted.  Server-side
--     validation in handlers/nodes.go BootSettings catches the
--     dangling-reference case at write time and at PXE-script render
--     time we degrade gracefully (log warning, fall through to default).
--   - Some deployments will populate boot_entries via the API after
--     migrating; allowing the column to be filled before the entry
--     exists is a feature, not a bug.
--
-- `kernel_cmdline` is unbounded TEXT.  We do not constrain the syntax
-- here — kernel cmdlines are notoriously vendor-specific (e.g. nomodeset
-- vs. `vga=normal nofb nomodeset video=vesafb:off`) and a syntax check
-- in SQL would reject legitimate inputs.  The handler validates length
-- (4 KiB cap) and forbids embedded NUL bytes.  Whitespace is preserved
-- verbatim because the operator may have tab-aligned options.
--
-- Backfill
-- --------
-- `boot_order_policy` defaults to `'auto'` for every existing row, which
-- preserves the v0.1.22 reactive-repair behaviour exactly.  No data
-- migration needed.
-- The two BOOT-SETTINGS columns default to NULL — semantic "no
-- persistent override; use the default per-state PXE routing".
--
-- Forward compatibility
-- ---------------------
-- A future Sprint may add `boot_order_policy = 'manual'` (operator
-- pinned the order via vendor BIOS; clustr leaves NVRAM untouched).
-- The CHECK list is intentionally short — we expand it only when the
-- finalize-side handler is also implemented for the new value.

ALTER TABLE node_configs
    ADD COLUMN boot_order_policy TEXT NOT NULL DEFAULT 'auto'
        CHECK (boot_order_policy IN ('auto','network','os'));

ALTER TABLE node_configs
    ADD COLUMN netboot_menu_entry TEXT;

ALTER TABLE node_configs
    ADD COLUMN kernel_cmdline TEXT;
