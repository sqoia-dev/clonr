-- migration 111: Sprint 37 DISKLESS Bundle A — node operating_mode column.
--
-- Per node_configs row, operating_mode declares how the cluster expects this
-- node to boot and (optionally) install:
--
--   'block_install'        — current/default behavior. Initramfs writes the
--                            base image to disk via clustr-deploy and reboots
--                            into the on-disk OS. This is the only mode wired
--                            end-to-end as of Bundle A — every other value is
--                            schema-and-protocol only and serves a TODO
--                            sentinel iPXE script (no half-broken boot path).
--
--   'filesystem_install'   — TODO (Bundle B). Conceptually a chroot/rsync
--                            install path rather than a block-image dump;
--                            reserves the enum slot.
--
--   'stateless_nfs'        — TODO (Bundle B). Compute node PXE-boots, mounts
--                            the cluster NFS export of
--                            /var/lib/clustr/images/<id>/rootfs/, never writes
--                            to disk. One symlink update rolls the cluster.
--
--   'stateless_ram'        — TODO (Bundle B). Compute node PXE-boots and
--                            loads a fully RAM-resident rootfs initrd; no
--                            NFS dependency at runtime.
--
-- The column is NOT NULL with a CHECK constraint on the enum so a bad operator
-- API call cannot strand a node in an unrenderable state — invalid values are
-- rejected by SQLite at write time, before the row is persisted, regardless of
-- whether the API-layer validator is bypassed.
--
-- Default 'block_install' preserves bit-for-bit boot behavior for every node
-- that exists at upgrade time. The Bundle A iPXE branch in
-- internal/server/handlers/boot.go reads this column and dispatches.

ALTER TABLE node_configs
  ADD COLUMN operating_mode TEXT NOT NULL DEFAULT 'block_install'
  CHECK (operating_mode IN ('block_install', 'filesystem_install', 'stateless_nfs', 'stateless_ram'));
