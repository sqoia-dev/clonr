-- 092_boot_entries.sql — per-image stateless / netboot menu entries (#160)
--
-- boot_entries rows are rendered into the iPXE menu at PXE-serve time for any
-- node that is in a disk-boot state. Each enabled row becomes an extra menu
-- item below the standard "Boot from disk" and "Reimage" entries.
--
-- Kernel/initrd/cmdline fields are nullable so that kinds that do not require
-- an initrd (e.g. memtest) or a cmdline do not carry empty-string noise.
--
-- Stock entries are inserted by this migration:
--   memtest  — enabled=1 with a placeholder kernel_url; operator must drop the
--              memtest86+ binary at that path or update the URL after install.
--   rescue   — enabled=0 until the operator configures a rescue password via
--              Settings; kernel_url points to vmlinuz, initrd_url points to the
--              rescue.cpio.gz produced by build-initramfs.sh (or its placeholder).

CREATE TABLE boot_entries (
    id          TEXT PRIMARY KEY,               -- UUID v4
    name        TEXT NOT NULL UNIQUE,           -- displayed in iPXE menu
    kind        TEXT NOT NULL,                  -- "kernel" | "iso" | "rescue" | "memtest"
    kernel_url  TEXT NOT NULL,                  -- absolute or relative URL
    initrd_url  TEXT,                           -- nullable for kinds that don't need it
    cmdline     TEXT,                           -- kernel cmdline; nullable
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE INDEX idx_boot_entries_enabled ON boot_entries(enabled);

-- Stock entry: Memtest86+ diagnostic
-- kernel_url points to the static binary under the internal repo path;
-- operator must drop the binary at that path or update this URL after install.
INSERT INTO boot_entries (id, name, kind, kernel_url, initrd_url, cmdline, enabled, created_at, updated_at)
VALUES (
    'be-memtest-default-0001',
    'Memtest86+',
    'memtest',
    '/api/v1/boot/extra/memtest',
    NULL,
    NULL,
    1,
    strftime('%s', 'now'),
    strftime('%s', 'now')
);

-- Stock entry: Rescue shell (busybox + dropbear)
-- Disabled by default — operator must configure a rescue password and enable
-- this entry via Settings > Boot Menu before it appears in the iPXE menu.
-- kernel_url and initrd_url point to the rescue image files served by the
-- boot handler once scripts/build-initramfs.sh produces rescue.cpio.gz.
INSERT INTO boot_entries (id, name, kind, kernel_url, initrd_url, cmdline, enabled, created_at, updated_at)
VALUES (
    'be-rescue-default-0001',
    'Rescue Shell',
    'rescue',
    '/api/v1/boot/vmlinuz',
    '/api/v1/boot/rescue.cpio.gz',
    'console=ttyS0,115200n8 console=tty0',
    0,
    strftime('%s', 'now'),
    strftime('%s', 'now')
);
