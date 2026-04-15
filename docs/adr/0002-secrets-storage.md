# ADR-0002: Secrets Storage

**Date:** 2026-04-13
**Status:** Accepted
**Last Verified:** 2026-04-13 — applies to clonr main @ fbccab9

---

## Context

clonr stores secrets that have materially different sensitivity levels:

- **BMC credentials** (IPMI username/password per node): required to power-cycle and control every node in the cluster. Compromise gives an attacker physical control of the hardware.
- **API key hashes**: already stored as SHA-256 hashes; the raw token is never persisted. Not a plaintext secret concern.
- **Kickstart/first-boot secrets** (temporary root passwords, join tokens for SSSD/LDAP during first boot): short-lived but sensitive during the provisioning window.
- **LDAP bind credentials** (if clonr manages LDAP client config): service account password with read access to the directory.

The current codebase stores BMC credentials as plaintext JSON in `node_configs.custom_vars`. This is wrong for any environment where the SQLite file is on a shared filesystem or backed up to an unencrypted store.

Go's `crypto/aes` + `crypto/cipher` (AES-256-GCM) is available in the standard library with no CGO dependency. libsodium secretbox would require CGO and breaks the static binary guarantee. AES-256-GCM is the correct choice.

---

## Decision

**Envelope encryption with a file-resident master key.**

A 32-byte random master key is generated on first run and written to a file at a configurable path (default: `/etc/clonr/master.key`, mode 0600, owned by the clonr service user). The SQLite database file does NOT contain the master key. An attacker who exfiltrates the database file without the key file gets ciphertext only.

Encryption: AES-256-GCM. Each encrypted value is stored as `base64(nonce || ciphertext || tag)` — a self-contained blob. The nonce is 12 bytes, random per encryption operation. Tag is 16 bytes (GCM standard).

What is encrypted:
- `node_configs.bmc_password` — per-node IPMI credential
- `node_configs.kickstart_secrets` — a JSON blob of ephemeral first-boot credentials; wiped after successful deployment
- `ldap_deployments.bind_password` — LDAP service account (if LDAP is managed by clonr)

What is NOT encrypted:
- Everything else. Hostnames, MACs, IPs, disk layouts, image checksums, hardware profiles — not secrets. Encrypting them adds complexity with no security benefit.
- API key hashes — already one-way; no encryption needed.

Schema: no new column types. Encrypted values are stored as TEXT with the encrypted blob. Application code is responsible for encrypt-on-write / decrypt-on-read in the `pkg/db/` query wrappers. No transparent encryption at the SQLite layer.

Key rotation: `clonr key rotate-master` re-encrypts all encrypted fields with a new master key in a single SQLite transaction. The old key file is overwritten atomically after the transaction commits. Rotation is a rare, manual operation.

Backup guidance (in operator docs): backup the SQLite file and the master key file separately, to separate destinations. The master key file must never be co-located with the database in a backup.

**v1.1:** If Vault integration is requested, it replaces the file-resident master key only. The encrypt/decrypt logic in `pkg/db/` is unchanged — Vault becomes a KMS that provides the 32-byte AES key on demand. The application-level encryption format is identical.

---

## Consequences

- Static binary guarantee preserved. No CGO, no libsodium.
- The master key file is the single point of control. Lose it without a backup and encrypted fields are unrecoverable (intentional — this is the threat model).
- Operators running clonr in a container must mount the key file as a volume secret, not bake it into the image. This must be explicit in the deployment docs.
- Rotation is atomic at the DB transaction level. A crash mid-rotation leaves the old key valid — the rotation can be re-run safely.
- The encrypt-on-write pattern means legacy plaintext values in `custom_vars` are not automatically migrated. A one-time migration command (`clonr migrate encrypt-secrets`) handles this.
