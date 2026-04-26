# clustr Upgrade Guide

This document covers upgrading a running `clustr-serverd` installation. Install instructions are in [docs/install.md](install.md).

---

## Contents

1. [How migrations work](#1-how-migrations-work)
2. [Upgrade procedure](#2-upgrade-procedure)
3. [Env vars that invalidate sessions on rotation](#3-env-vars-that-invalidate-sessions-on-rotation)
4. [Confirming a successful upgrade](#4-confirming-a-successful-upgrade)
5. [Rollback procedure](#5-rollback-procedure)

---

## 1. How migrations work

clustr uses sequential SQLite migrations numbered by a monotonically increasing integer (e.g. migration 038, 039, …). On startup, `clustr-serverd`:

1. Opens the SQLite database.
2. Reads the current schema version from the `schema_migrations` table.
3. Applies any pending migrations in order, inside a single transaction per migration.
4. If any migration fails, the transaction is rolled back and the server exits with a non-zero status — **the database is never left in a partially-migrated state.**

You do not run migrations manually. Upgrading the binary and restarting the service is the entire migration step.

### What migrations can do safely

- Add new tables or columns (always forward-compatible with older clients reading the same DB).
- Rename columns (SQLite 3.35+, supported on Rocky Linux 9 / Ubuntu 22.04).
- Add indexes.
- Encrypt existing plaintext credential values at first start (LDAP bind password, BMC password).

### What migrations cannot undo

Once a migration runs, the schema change is permanent. Rolling back to an older binary after a migration has run will fail if the older binary does not understand the new schema. This is why the rollback procedure (§5) restores from a backup rather than running the binary in reverse.

---

## 2. Upgrade procedure

### Before you start

1. **Verify your backup is current.** Run the backup manually and confirm it completes:

   ```bash
   # Bare-metal
   bash /opt/clustr/scripts/ops/clustr-backup.sh

   # Docker Compose
   docker exec clustr bash /opt/clustr/scripts/ops/clustr-backup.sh
   ```

   Verify the backup file exists and is not empty:

   ```bash
   ls -lh /var/lib/clustr/backups/
   ```

2. **Check for active reimages.** The upgrade restarts the service; any in-flight reimage will be interrupted and will need to be retried.

   ```bash
   curl -s http://10.99.0.1:8080/api/v1/reimages?status=running \
     -H "Authorization: Bearer <your-admin-key>" | python3 -m json.tool
   # "total": 0 means safe to proceed
   ```

3. **Read the release notes** for the target version. Look for any notes on mandatory env var changes, new required variables, or breaking schema changes.

### Docker Compose upgrade

```bash
# 1. Pull the new image
docker compose pull clustr

# 2. Restart — the server applies pending migrations on startup
cd /etc/clustr
docker compose up -d

# 3. Verify the server started and migrations ran (see §4)
docker compose logs clustr | tail -50
```

### Bare-metal / Ansible upgrade

```bash
# 1. Download the new binary
VERSION="v1.0.0"   # replace with target version
ARCH="$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"

curl -fsSL "https://github.com/sqoia-dev/clustr/releases/download/${VERSION}/clustr-serverd-linux-${ARCH}" \
  -o /usr/local/bin/clustr-serverd.new

# 2. Verify checksum
curl -fsSL "https://github.com/sqoia-dev/clustr/releases/download/${VERSION}/clustr-serverd-linux-${ARCH}.sha256" \
  | sha256sum -c -

# 3. Atomically swap the binary
mv /usr/local/bin/clustr-serverd.new /usr/local/bin/clustr-serverd
chmod +x /usr/local/bin/clustr-serverd

# 4. Restart — migrations run on startup
systemctl restart clustr-serverd

# 5. Confirm (see §4)
journalctl -u clustr-serverd --no-pager | tail -30
```

### Ansible role upgrade

```bash
# Update clustr_version in your inventory or group_vars, then:
ansible-playbook -i inventory.ini deploy/ansible/site.yml --tags install,systemd
```

The role checks the installed version and only downloads/restarts if the version has changed.

---

## 3. Env vars that invalidate sessions on rotation

Rotating the following values forces all active browser sessions to expire. Users will be redirected to the login page with their next request. API key authentication is not affected.

| Variable | Effect on rotation |
|---|---|
| `CLUSTR_SESSION_SECRET` | All browser sessions expire immediately on next request. Users must log in again. **Intentional use case:** suspected session compromise. |

Variables that do NOT invalidate sessions:

| Variable | Effect on rotation |
|---|---|
| `CLUSTR_SECRET_KEY` | Rotated key is used for new writes. Existing encrypted rows (BMC credentials, LDAP passwords) are decrypted with the old key on next read and re-encrypted with the new key automatically. No downtime or session impact. |

### How to rotate `CLUSTR_SESSION_SECRET`

```bash
# Generate a new value
NEW_SECRET=$(openssl rand -hex 64)

# Bare-metal: update secrets.env
# Edit /etc/clustr/secrets.env and replace CLUSTR_SESSION_SECRET=...
# Then:
systemctl restart clustr-serverd

# Docker Compose: update secrets.env in /etc/clustr/secrets.env and:
cd /etc/clustr && docker compose up -d
```

All users will be logged out. There is no way to selectively invalidate individual sessions — rotation invalidates all sessions simultaneously.

### How to rotate `CLUSTR_SECRET_KEY`

This operation re-encrypts BMC and LDAP credentials at rest. It is safe to do at any time — no service downtime beyond the restart.

```bash
# Generate a new 32-byte hex key
NEW_KEY=$(openssl rand -hex 32)

# Update secrets.env, replace CLUSTR_SECRET_KEY=...
# Then restart. On startup, clustr-serverd will:
#   1. Attempt to decrypt existing rows with the new key — will fail (expected).
#   2. Fall back to the old key (only during the migration window).
#   3. Re-encrypt all decrypted values with the new key.
#   4. Subsequent reads use only the new key.
```

**Important:** if you lose `CLUSTR_SECRET_KEY` with no backup copy, BMC and LDAP credentials stored in the database cannot be recovered. Store the key in your secrets manager (Vault, AWS SSM, etc.) alongside the Ansible Vault or your standard credential backup.

---

## 4. Confirming a successful upgrade

### Check the version reported by the server

```bash
curl -s http://10.99.0.1:8080/api/v1/health | python3 -m json.tool
# Look for: "version": "v1.0.0" (the version you just installed)
```

### Check the readiness endpoint

```bash
curl -s http://10.99.0.1:8080/api/v1/healthz/ready | python3 -m json.tool
# All checks must be "ok":
# {
#   "status": "ok",
#   "checks": {
#     "db": "ok",
#     "boot_dir": "ok",
#     "initramfs": "ok"
#   }
# }
```

### Check the startup log for migration output

**Docker Compose:**
```bash
docker compose logs clustr | grep -E "(migration|Migration|schema)"
```

**systemd:**
```bash
journalctl -u clustr-serverd --no-pager | grep -E "(migration|Migration|schema)"
```

A successful migration run looks like:
```
INFO  Applied migration 038 (LDAP credential encryption)
INFO  Applied migration 039 (BMC credential encryption)
INFO  Schema is up to date at version 048
```

If no migration lines appear, the database was already at the current schema version (expected on re-upgrade to the same version).

### Check for errors

```bash
# Docker Compose
docker compose logs clustr | grep -i "error\|fatal\|panic"

# systemd
journalctl -u clustr-serverd --no-pager | grep -i "error\|fatal\|panic"
```

No output means the server started cleanly.

---

## 5. Rollback procedure

Use this procedure if the new binary does not start, or if the new version has a critical regression that requires reverting.

**Prerequisite:** the backup from §2 must exist and be from before the upgrade. The backup verify timer (installed in Sprint 4 / S4-8) confirms backup integrity weekly — check the journal for the most recent result:

```bash
journalctl -u clustr-backup-verify --no-pager | tail -20
```

### Step 1: Stop the server

```bash
# Docker Compose
cd /etc/clustr && docker compose down

# systemd
systemctl stop clustr-serverd
```

### Step 2: Restore the database from backup

```bash
# Find the most recent backup
ls -lt /var/lib/clustr/backups/clustr-*.db | head -5

# Restore (replace with the actual backup filename)
BACKUP_FILE="/var/lib/clustr/backups/clustr-20260706-020001.db"

# Move the current (migrated) DB aside for forensics
mv /var/lib/clustr/db/clustr.db /var/lib/clustr/db/clustr.db.post-upgrade-$(date +%Y%m%d%H%M%S)

# Restore
cp "$BACKUP_FILE" /var/lib/clustr/db/clustr.db
chmod 600 /var/lib/clustr/db/clustr.db
```

### Step 3: Restore the old binary

```bash
# If the autodeploy system saved a .prev backup:
if [ -f /usr/local/bin/clustr-serverd.prev ]; then
    mv /usr/local/bin/clustr-serverd /usr/local/bin/clustr-serverd.failed
    cp /usr/local/bin/clustr-serverd.prev /usr/local/bin/clustr-serverd
    chmod +x /usr/local/bin/clustr-serverd
fi

# Otherwise, download the previous version manually:
# PREV_VERSION="v0.9.0"
# curl -fsSL https://github.com/sqoia-dev/clustr/releases/download/${PREV_VERSION}/clustr-serverd-linux-amd64 \
#   -o /usr/local/bin/clustr-serverd
# chmod +x /usr/local/bin/clustr-serverd
```

### Step 4: Restart and verify

```bash
# systemd
systemctl start clustr-serverd
journalctl -u clustr-serverd -f --no-pager

# Docker Compose: update docker-compose.yml image tag back to the old version, then:
cd /etc/clustr && docker compose up -d
docker compose logs -f clustr
```

Verify the readiness endpoint returns 200 before declaring the rollback complete.

### Step 5: File a bug report

A rollback means something is wrong with the release. Document:
- Which version you upgraded from and to
- The error or symptom that triggered the rollback
- The startup log snippet showing the failure

Open an issue at `https://github.com/sqoia-dev/clustr/issues`.

---

## See Also

- [docs/install.md](install.md) — Fresh installation guide
- [docs/tls-provisioning.md](tls-provisioning.md) — TLS setup with Caddy
- [README.md](../README.md) — Quick Start and architecture overview
