# build/slurm — Slurm Build Pipeline Artifacts

This directory contains the version pins and public key material for the clustr
Slurm build pipeline. See `docs/slurm-build-pipeline.md` for the full design.

---

## Files

| File | Purpose |
|---|---|
| `versions.yml` | Single source-of-truth: Slurm version, tarball SHA256, build targets |
| `keys/clustr-release.asc.pub` | clustr RPM signing public key (armored GPG export) |

---

## Key Fingerprints

### clustr Release Signing Key

| Field | Value |
|---|---|
| Identity | `clustr Release Signing <release@sqoia.dev>` |
| Type | RSA 4096 |
| Created | 2026-04-27 |
| Expires | 2028-04-26 (2-year term; rotate annually per policy below) |
| Full fingerprint | `9EDB 9E63 AB84 551E 25C1  4168 41E5 1A66 53BB A540` |
| Short key ID | `41E51A6653BBA540` |
| File | `keys/clustr-release.asc.pub` |

This key is used by the `slurm-build.yml` GitHub Actions workflow to sign RPMs
via `rpm --addsign`. The private key is stored in the GitHub Actions secret
`CLUSTR_RPM_SIGNING_KEY` (ASCII-armored). The passphrase is in
`CLUSTR_RPM_SIGNING_PASSPHRASE`.

The public key is committed here and embedded in the `clustr-serverd` binary
via `//go:embed build/slurm/keys/clustr-release.asc.pub` so a freshly
installed clustr-server can write the key into deployed nodes' chroots without
a network round-trip.

**Private key storage (NOT in this repo):**
`/home/ubuntu/sqoia-dev-secrets/keys/clustr-rpm-signing-private.asc` (chmod 600)
Passphrase: `/home/ubuntu/sqoia-dev-secrets/keys/clustr-rpm-signing-passphrase.txt` (chmod 600)

### SchedMD Signing Key

As of 2026-04-27, SchedMD does **not** publish detached GPG signatures (`.asc`)
for Slurm release tarballs. They provide SHA256/MD5/SHA1 checksum files at
`https://download.schedmd.com/slurm/SHA256` served over HTTPS.

The `schedmd_signing_key_id: "0xCB8E2EB3D04E78CC"` entry in `versions.yml` is
a forward-reference placeholder. The `slurm-build.yml` workflow verifies the
tarball SHA256 against the pinned value in `versions.yml` AND cross-checks
against the live SchedMD SHA256 file at build time. The `gpg --verify` step is
skipped until SchedMD publishes `.asc` files.

There is no `keys/schedmd-release.asc` file because there is no key to commit.
If SchedMD begins publishing detached signatures, update `versions.yml` with
the confirmed fingerprint, commit the key here, and enable the `gpg --verify`
step in `slurm-build.yml`.

---

## Key Rotation Policy

The clustr signing key has a 2-year validity period. Rotate **annually** (do
not wait for expiry) per the policy in `docs/slurm-build-pipeline.md §6.3`:

1. Generate a new RSA 4096 key with identity `clustr Release Signing <release@sqoia.dev>`.
2. Export the armored public key to `build/slurm/keys/clustr-release.asc.pub`,
   replacing the current file.
3. Update the fingerprint table in this README.
4. Store the new private key at
   `/home/ubuntu/sqoia-dev-secrets/keys/clustr-rpm-signing-private.asc` and
   the passphrase at
   `/home/ubuntu/sqoia-dev-secrets/keys/clustr-rpm-signing-passphrase.txt`.
5. Update the GitHub Actions secrets `CLUSTR_RPM_SIGNING_KEY` and
   `CLUSTR_RPM_SIGNING_PASSPHRASE`.
6. **Keep the old key trusted for one year of overlap** — do not revoke it
   until any clusters running Slurm RPMs signed with the old key have been
   redeployed or the old RPMs have been replaced.
7. Ship the new public key in a clustr-server release (the embed is in the
   binary; a new release is required to distribute the updated key to deployed
   nodes automatically).
8. Document the rotation in `docs/security/key-rotation.md` (to be created in
   sprint 2 per the design doc).

---

## Verification Chain

How to verify the Slurm tarball before building:

```bash
# 1. SHA256 pin check (pinned in versions.yml, also confirmed against SchedMD)
sha256sum slurm-24.11.4.tar.bz2
# Expected: a2baee7b9c0775d64bd623891865a1402266280156e6e03da6c7f362fb405748

# 2. Cross-check against SchedMD live checksum (build workflow does this automatically)
curl -s https://download.schedmd.com/slurm/SHA256 | grep slurm-24.11.4.tar.bz2

# 3. GPG verify (once SchedMD publishes .asc files — currently not available)
# gpg --import build/slurm/keys/schedmd-release.asc
# gpg --verify slurm-24.11.4.tar.bz2.asc slurm-24.11.4.tar.bz2
```

To verify a signed RPM against the clustr key:

```bash
rpm --import build/slurm/keys/clustr-release.asc.pub
rpm -K slurm-24.11.4-1.el9.x86_64.rpm
```
