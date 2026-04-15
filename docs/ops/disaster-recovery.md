# Disaster Recovery — clonr

**Last Verified:** 2026-04-13 — applies to clonr main @ 4d1f66f

## Scope

This document covers recovery of the clonr provisioning server from unexpected process death, power loss, or operator error. It does not cover storage hardware failure (covered under backup-restore.md) or network fabric loss (out of scope for v0.x).

---

## Test Results: Kill-and-Recover Smoke Test

**Executed:** 2026-04-12, clonr-server 192.168.1.151  
**Binary:** clonr-serverd v0.x (dev build)  
**Hardened unit:** deployed as part of G2 (NoNewPrivileges, ProtectSystem=strict, MemoryMax=2G)

### Setup state

All four active nodes (vm201–vm204) had `reimage_pending=true` and `reimage_requests` rows in `triggered` status — actively waiting to be picked up by the scheduler. This simulates nodes that were mid-queue when the server died.

---

### Run 1: SIGTERM (graceful shutdown)

```bash
systemctl kill -s SIGTERM clonr-serverd
# wait 10 seconds
systemctl start clonr-serverd
```

**Observed behavior:**

- Server shut down cleanly: journal showed `reimage scheduler stopped` before exit.
- On restart: all listeners came back immediately (HTTP :8080, DHCP :67, TFTP :69).
- `reimage_pending` flag: **preserved** across restart for all four nodes — flag is stored in the DB, not in process memory.
- `reimage_requests` status: remained `triggered` — no state was corrupted. Scheduler re-registered and will pick these up on next tick.
- SQLite WAL integrity: `ok`.
- No log entries were lost (journal is persistent, systemd unit logs to journal).

**Verdict:** Clean recovery. Nodes with `reimage_pending=true` will re-enter the deploy queue on next scheduler poll cycle without manual intervention.

---

### Run 2: SIGKILL (ungraceful kill — no shutdown handlers run)

```bash
systemctl kill -s SIGKILL clonr-serverd
# wait 10 seconds
systemctl start clonr-serverd
```

**Observed behavior:**

- systemd detected the kill and scheduled a restart (journal: `Scheduled restart job, restart counter is at 1`).
- Since `Restart=on-failure` is set, systemd would have auto-restarted within 5 seconds even without the manual `systemctl start`.
- `reimage_pending` flag: **preserved** across SIGKILL. WAL checkpointing had already flushed the flag to the DB file; the SIGKILL did not corrupt it.
- `reimage_requests` status: remained `triggered` — no zombie `deploying` rows created. The scheduler had not yet transitioned any row to `deploying` at time of kill.
- `PRAGMA integrity_check`: returned `ok`. SQLite WAL mode is resilient to hard kills; the WAL file is replayed on next open.
- HTTP health endpoint: responsive immediately after restart.

**Verdict:** Clean recovery. No manual intervention required.

---

## Known-Bad States (Require Manual DB Intervention)

### ZOMBIE-1: Node stuck in `deploying` reimage_request with server dead

**How it occurs:** Server is killed after transitioning a `reimage_requests` row from `triggered` → `deploying` but before it transitions to `completed` or `failed`. On restart, the scheduler sees a row already in `deploying` and may not re-queue it (depending on scheduler logic). The node on the network is retrying blob downloads with no server acknowledgment.

**Detection:**

```bash
sqlite3 /var/lib/clonr/db/clonr.db \
  "SELECT id, node_id, status, started_at FROM reimage_requests WHERE status = 'deploying';"
```

**Fix (Sprint 2 ticket — see below):** Until the scheduler has crash-recovery logic, manually reset stuck rows:

```bash
# Reset a stuck deploying row to pending so the scheduler picks it up again
sqlite3 /var/lib/clonr/db/clonr.db \
  "UPDATE reimage_requests SET status = 'pending', started_at = NULL WHERE status = 'deploying' AND id = '<row-id>';"
```

Then restart clonr-serverd — it will pick up the pending row on next scheduler tick.

**Current test result:** This state was NOT triggered during our tests because the scheduler had not yet transitioned any rows to `deploying` at the time of the kills. It is a theoretical risk during an active deploy.

---

### ZOMBIE-2: Orphaned tmp ISO files after SIGKILL during image build

**How it occurs:** qemu-kvm ISO build writes to `/var/lib/clonr/tmp/clonr-iso-<random>.iso`. A SIGKILL during the build leaves a partial ISO on disk. The build is not resumable; it will be re-triggered on the next deploy attempt.

**Detection:**

```bash
ls -lh /var/lib/clonr/tmp/clonr-iso-*.iso
```

**Fix:** These are safe to delete if clonr-serverd is not currently building an image. Confirm with:

```bash
sqlite3 /var/lib/clonr/db/clonr.db "SELECT status FROM reimage_requests WHERE status = 'deploying';"
# If empty, no active builds — safe to delete tmp ISOs
rm /var/lib/clonr/tmp/clonr-iso-*.iso
```

Note: As of 2026-04-12, the tmp dir contained ~7GB of stale ISO artifacts from previous test sessions. These are safe to purge when no deploy is active.

---

## Standard Recovery Procedure

### Scenario: clonr-serverd dies unexpectedly

```bash
# 1. Check what happened
journalctl -u clonr-serverd -n 100 --no-pager

# 2. Verify DB integrity
sqlite3 /var/lib/clonr/db/clonr.db "PRAGMA integrity_check;"

# 3. Check for zombie deploying rows
sqlite3 /var/lib/clonr/db/clonr.db "SELECT id, node_id, status FROM reimage_requests WHERE status IN ('deploying');"

# 4. If no zombies: just restart
systemctl start clonr-serverd

# 5. If zombies exist: reset them first (see ZOMBIE-1 above), then restart
# 6. Verify recovery
systemctl status clonr-serverd --no-pager
curl http://127.0.0.1:8080/api/v1/health
```

### Scenario: Restore from backup after data loss

See [backup-restore.md](backup-restore.md) for the full procedure. Short form:

```bash
sudo /opt/clonr/scripts/clonr-restore.sh /var/lib/clonr/backups/clonr-<timestamp>.db
```

### Scenario: Wrong binary deployed, rollback needed

```bash
# clonr-serverd binary is at /usr/local/bin/clonr-serverd
# There is no automatic binary rollback in v0.x
# Keep the previous binary at /usr/local/bin/clonr-serverd.prev as a manual convention

systemctl stop clonr-serverd
cp /usr/local/bin/clonr-serverd.prev /usr/local/bin/clonr-serverd
systemctl start clonr-serverd
```

---

## Sprint 2 Tickets from DR Test

### DR-S2-1: Scheduler crash recovery for `deploying` rows

**Priority:** High (correctness — affects deploy reliability)  
**Description:** On server restart, the reimage scheduler should scan for any `reimage_requests` rows in `deploying` status and transition them back to `pending` (with a `crash_recovered` note in error_message) so they are automatically re-queued. Currently this requires manual DB surgery.  
**Effort:** S (add startup recovery sweep in scheduler init path)

### DR-S2-2: Stale tmp ISO cleanup on startup

**Priority:** Low (disk hygiene)  
**Description:** On startup, clonr-serverd should enumerate `/var/lib/clonr/tmp/clonr-iso-*.iso` files older than 1 hour and delete them. Currently 7GB of stale build artifacts accumulate and require manual cleanup.  
**Effort:** XS

### DR-S2-3: Binary rollback convention — keep `.prev` copy on deploy

**Priority:** Medium (operability)  
**Description:** The deploy playbook should copy the current binary to `/usr/local/bin/clonr-serverd.prev` before overwriting it. No automation exists today; a bad binary requires manual intervention.  
**Effort:** XS (add one line to deploy script)
