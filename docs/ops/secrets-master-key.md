# Secrets Master Key — Operations Guide

**Version:** v1.0 (stub)
**Owner:** Gilfoyle (Infra)
**Last updated:** 2026-04-15
**Related:** ADR-0002 (Secrets Storage), ADR-0009 (Content-Only Images, pending)

---

## Overview

clonr-serverd uses a 256-bit (32-byte) AES master key to encrypt sensitive fields stored in
the SQLite database — primarily node BMC passwords and any future per-node secret material.
The key is never stored in the database itself. It is loaded at startup from a file on disk and
held in process memory. If the key is lost, encrypted fields in the database become permanently
unrecoverable.

This document covers: placement, ownership, how the server loads it, rotation procedure, and
disaster recovery.

---

## Placement and Ownership

| Property | Value |
|----------|-------|
| Path | `/etc/clonr/secret-master.key` |
| Format | 32 raw bytes (binary, not hex or base64) |
| Permissions | `0400` (owner read-only) |
| Owner | `root:root` |
| Directory | `/etc/clonr/` — mode `0700`, owned `root:root` |
| Env var | `CLONR_SECRET_MASTER_KEY_PATH` (set in systemd unit) |

The directory `/etc/clonr/` is intentionally mode `0700` — nothing inside it should be
readable by non-root processes. clonr-serverd runs as root on this host (confirmed by
`systemctl show clonr-serverd -p User` returning empty, which means the unit inherits root).

The systemd unit at `/etc/systemd/system/clonr-serverd.service` (and its repo copy at
`deploy/systemd/clonr-serverd.service`) sets:

```
Environment=CLONR_SECRET_MASTER_KEY_PATH=/etc/clonr/secret-master.key
```

The server reads this path at startup via `pkg/config`. If the path is set but the file does
not exist or is not readable, the server fails fast with a fatal error before accepting any
connections. This is intentional: a server that starts without the master key would silently
fail all encryption/decryption operations.

### Note on ReadOnlyPaths

The systemd `ReadOnlyPaths=/etc/clonr/secret-master.key` directive was tested and rejected.
On Rocky Linux 9 / systemd v252 with `ProtectSystem=false`, enabling `ReadOnlyPaths` triggers
mount namespace setup (`status=226/NAMESPACE`) even without `ProtectSystem`. This is the same
root cause documented for `ProtectSystem` and `PrivateTmp` in the unit file comments.
The key's protection is provided solely by the `0400` file permission and `0700` directory
permission. These are sufficient: the only process running as root on this host is clonr-serverd
itself, and there are no other local user accounts with shell access.

---

## Offline Backup

A copy of the key is maintained at:

```
/home/ubuntu/sqoia-dev-secrets/clonr/secret-master.key.backup
```

on the control host (the Ubuntu machine with SSH access to the lab). This file is mode `0400`.

**This is the only recovery path if the key is lost on the server.** The secrets directory
is never committed to git. Treat it the same as an SSL private key.

For production deployments (Sprint 3+), the backup location should be a hardware security
module (HSM), an encrypted offline USB drive kept in a secure physical location, or a secrets
manager vault (HashiCorp Vault, etc.). The current backup to a local file on the control host
is a v0.x lab arrangement only.

---

## How the Server Loads the Key

At startup, `pkg/config` reads `CLONR_SECRET_MASTER_KEY_PATH`, opens the file, and reads
exactly 32 bytes. The key is held in a `[32]byte` in the server's config struct and passed
to `pkg/db` at initialization. It is never logged, never serialized to JSON, and never sent
over the API.

The `pkg/db` encryption layer uses AES-256-GCM. Each encrypted value is stored as:
`nonce (12 bytes) || ciphertext || tag (16 bytes)`, base64-encoded in the SQLite column.
The nonce is randomly generated per encryption operation — two encryptions of the same
plaintext produce different ciphertexts.

---

## Key Generation

To generate a new key (initial setup or after loss):

```bash
# On the clonr server host, as root
mkdir -p /etc/clonr
chmod 0700 /etc/clonr
openssl rand -out /etc/clonr/secret-master.key 32
chmod 0400 /etc/clonr/secret-master.key
chown root:root /etc/clonr/secret-master.key
```

Then copy the key to offline backup storage before starting the server.

---

## Rotation Procedure (v1.0 Stub)

**Status:** The rotation procedure is stubbed here. Full implementation lands in v1.1 when
`pkg/db` gains a key-rotation CLI command. The design is documented here as an operational
contract so the implementation matches operator expectations.

### Planned Rotation Procedure (v1.1)

1. Generate a new 32-byte key: `openssl rand -out /etc/clonr/secret-master.key.new 32`
2. Run: `clonr-serverd --rotate-key --old-key /etc/clonr/secret-master.key --new-key /etc/clonr/secret-master.key.new`
   - This command reads every encrypted column in the database, decrypts with the old key,
     re-encrypts with the new key, and writes back atomically in a single SQLite transaction.
   - If the transaction fails for any reason, the database is unchanged. The old key remains valid.
3. On success, atomically replace the key file:
   ```bash
   chmod 0400 /etc/clonr/secret-master.key.new
   mv /etc/clonr/secret-master.key.new /etc/clonr/secret-master.key
   ```
4. Restart clonr-serverd: `systemctl restart clonr-serverd`
5. Update the offline backup with the new key.
6. Shred the old key material: `shred -u /path/to/old-key-backup`

### Current Limitations (v1.0)

- There is no `--rotate-key` command yet. Do not attempt key rotation in v1.0.
- If rotation is required before v1.1 (e.g., suspected key compromise), follow the disaster
  recovery procedure below. This will lose all encrypted field values — they must be re-entered
  manually after recovery.

---

## Disaster Recovery — Key Lost

If the master key file is lost and no backup exists, all encrypted database columns are
permanently unrecoverable. There is no brute-force path: AES-256-GCM with a random 32-byte
key provides approximately 2^256 keyspace.

### Recovery Steps

1. Stop clonr-serverd: `systemctl stop clonr-serverd`
2. Back up the current (encrypted but unrecoverable) database:
   ```bash
   cp /var/lib/clonr/db/clonr.db /var/lib/clonr/db/clonr.db.encrypted-unrecoverable.$(date +%Y%m%d)
   ```
3. Generate a new master key: `openssl rand -out /etc/clonr/secret-master.key 32`
4. Copy the new key to offline backup immediately.
5. Initialize a fresh database: `rm /var/lib/clonr/db/clonr.db`
   - clonr-serverd will create a new empty database on next start.
6. Start clonr-serverd: `systemctl start clonr-serverd`
7. Re-enroll all nodes: all node records, images, and BMC credentials must be re-created.
   Node API keys from the old database are invalid (they were in the database but the database
   is now empty). Nodes will re-enroll on next PXE boot via the standard MAC registration flow.
8. Re-enter all BMC passwords via the admin API or UI.
9. Document the incident: record what was lost, when, and why.

### Recovery from Backup

If the backup at `/home/ubuntu/sqoia-dev-secrets/clonr/secret-master.key.backup` (or
production equivalent) is available:

1. Stop clonr-serverd.
2. Copy the backup key to the server:
   ```bash
   scp /path/to/backup/secret-master.key.backup root@<server>:/etc/clonr/secret-master.key
   ssh root@<server> 'chmod 0400 /etc/clonr/secret-master.key && chown root:root /etc/clonr/secret-master.key'
   ```
3. Start clonr-serverd. All encrypted fields decrypt normally.
4. Verify: `systemctl status clonr-serverd` — confirm no decryption errors in the first 30s of logs.

---

## Verification

After any key operation, verify the server is using the correct key:

```bash
# Server should start cleanly with no encryption-related errors
journalctl -u clonr-serverd -n 20 --no-pager | grep -E "(key|crypt|secret|fatal|error)"

# Confirm the env var is set in the running process
systemctl show clonr-serverd -p Environment | grep MASTER_KEY

# Confirm the file exists with correct permissions
ls -la /etc/clonr/secret-master.key
# Expected: -r-------- 1 root root 32 ...
```
