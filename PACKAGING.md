# clustr Packaging Design

This document records all decisions about how clustr is built, signed, and distributed
as native OS packages. It is the authoritative reference for the release pipeline.

---

## Phase 1 — Audit Findings

Completed 2026-04-29.

### Container artifacts removed
- `Dockerfile` — multi-stage Alpine build; was the only container artifact at repo root
- `.github/workflows/docker.yml` — built/pushed `ghcr.io/sqoia-dev/clustr-server:{semver,sha}` on tag push
- `deploy/docker-compose/docker-compose.yml` — referenced `ghcr.io/sqoia-dev/clustr-server:latest`

### No live Docker consumers
- `cloner` (192.168.1.151) runs `clustr-autodeploy` which builds from source (`go build`) — never
  pulled a Docker image. Confirmed via `/opt/clustr/deploy/systemd/clustr-autodeploy.service`.
- No other host or CI step pulls `ghcr.io/sqoia-dev/clustr*`.
- Safe to delete all container artifacts with zero impact on any live system.

### Existing release pipeline (pre-migration)
- `.github/workflows/release.yml` produced: `clustr-linux-{amd64,arm64}` (CLI) and
  `clustr-serverd-linux-amd64` (server) as raw binaries attached to GitHub Releases.
- No packages, no signing, no repo hosting.

### Systemd unit
- Canonical unit: `/etc/systemd/system/clustr-serverd.service` on cloner.
  Repo copy at `deploy/systemd/clustr-serverd.service` was already in sync.
- Key characteristics: runs as root (required by nspawn + loop + DHCP capabilities),
  `ExecStart=/usr/sbin/clustr-serverd --pxe`, EnvironmentFile from `/etc/clustr/secrets.env`.

### Configuration model
- Pure environment-variable based (`internal/config/config.go`). No config file is read
  at startup unless explicitly pointed to via `CLUSTR_CONFIG` (JSON). All env vars have
  sane defaults. The `EnvironmentFile=/etc/clustr/clustr-serverd.conf` we ship in the
  package uses shell-style `KEY=VALUE` lines loaded by systemd before exec.

---

## Phase 2 — Design Decisions

### Distribution: RPM only, EL8 / EL9 / EL10

**Decision: RPM packages for RHEL-family distros only. No DEB. No Debian/Ubuntu support.**

Rationale: clustr's audience is HPC / lab / bare-metal operators running Rocky Linux,
RHEL, or AlmaLinux. The Slurm bundles clustr ships are already EL-targeted RPMs. The
bundled base image is Rocky 9. DEB support would require maintaining a separate signing
pipeline, apt metadata generation, and installation path for a user population that is
negligible in the scientific computing and HPC world. Operators on non-EL systems build
from source.

### Build tool: nfpm

**Decision: nfpm.** Justification:

- Produces RPM from a single YAML config with `nfpm pkg --packager rpm`.
  No `rpmbuild` spec file, no Ruby/fpm dependency.
- Go binary, installable in CI from the official release tarball. Zero system-package
  deps on the GitHub Actions runner.
- Actively maintained (goreleaser project). Handles pre/post install scripts, systemd
  units, config files, and file permissions natively.
- Alternative evaluated: `fpm` — rejected because it requires Ruby, adds ~2 min to
  runner setup, and nfpm covers identical functionality.

### Package layout

#### `clustr-serverd` package

| Path | Purpose |
|------|---------|
| `/usr/sbin/clustr-serverd` | Server binary |
| `/usr/lib/systemd/system/clustr-serverd.service` | Systemd unit |
| `/etc/clustr/clustr-serverd.conf` | Environment file (loaded by systemd EnvironmentFile=) |
| `/var/lib/clustr/` | Data root (DB, images, boot, TFTP, repo, LDAP, tmp) |
| `/var/log/clustr/` | Log directory |

Post-install script:
1. Creates `clustr` user and group (system account, no login shell, no home dir) if absent.
2. `chown -R root:clustr /var/lib/clustr /var/log/clustr /etc/clustr` — the service itself
   runs as root due to nspawn/loop/DHCP capability requirements. The `clustr` group is a
   convention for file ownership rather than a service account in the traditional sense.
   The unit does not carry `User=clustr`.
3. Runs `systemctl daemon-reload`.
4. Does NOT run `systemctl enable` or `systemctl start` — operator opts in.

#### `clustr` package (CLI)

| Path | Purpose |
|------|---------|
| `/usr/bin/clustr` | CLI binary (CGO_ENABLED=0 static) |

No systemd unit, no config directory, no post-install script.

### Filename convention

```
clustr-serverd-1.13.0-1.el9.x86_64.rpm
clustr-serverd-1.13.0-1.el8.x86_64.rpm
clustr-serverd-1.13.0-1.el10.x86_64.rpm
clustr-1.13.0-1.el9.x86_64.rpm
clustr-1.13.0-1.el8.x86_64.rpm
clustr-1.13.0-1.el10.x86_64.rpm
```

The `el8`/`el9`/`el10` value comes from the `EL_RELEASE` env var passed to nfpm, which
sets the RPM `Release` field to `1.el9` (etc). The Go static binary is identical across
EL targets — we repackage it three times so dnf metadata is correct per repo subdir and
dnf `--releasever` queries work predictably.

### Repo hosting: pkg.sqoia.dev on the existing Linode Nanode

**Decision: `pkg.sqoia.dev` as a Caddy vhost on the existing Linode (66.175.212.72).**

Justification:
- The Nanode serves two static sites and Uptime Kuma; disk is not under pressure.
  Package artifacts for amd64+arm64 × 3 EL targets are ~20–50 MB per release.
  At 12 releases/year the cumulative footprint is well under 1 GB.
- Clean URL: `dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el9/clustr.repo`
- TLS: Caddy auto-provisions Let's Encrypt certificate for `pkg.sqoia.dev`.
- DNS: `pkg.sqoia.dev` A record → `66.175.212.72`.

**Repo layout on disk:**
```
/var/www/pkg.sqoia.dev/clustr/
  RPM-GPG-KEY-clustr        # armored public key for rpm --import
  el8/
    clustr.repo             # [clustr] dnf repo file for EL8
    x86_64/                 # RPM packages + createrepo_c repodata
    aarch64/                # (if arm64 packages present)
  el9/
    clustr.repo
    x86_64/
    aarch64/
  el10/
    clustr.repo
    x86_64/
    aarch64/
```

**Deploy access:**
- `deploy` user on the Linode owns `/var/www/pkg.sqoia.dev/` (755 deploy:deploy).
- GitHub Actions rsync uses a dedicated SSH key (`PKG_DEPLOY_KEY` GHA secret).
  The private key is ephemeral in CI; the public key is in `deploy`'s `authorized_keys`.
- `createrepo_c` runs in CI (on the GitHub Actions runner) before rsync, so the
  Linode does not need `createrepo_c` installed.

### GPG signing key

**Key identity:** `clustr Release Signing <release@sqoia.dev>`
**Algorithm:** RSA 4096 (existing key, generated 2026-04-27)
**Fingerprint:** `9EDB 9E63 AB84 551E 25C1  4168 41E5 1A66 53BB A540`
**Expires:** 2028-04-26

**Storage:**
- Private key: `CLUSTR_GPG_PRIVATE_KEY` GitHub Actions secret (armored export).
  Passphrase: `CLUSTR_GPG_PASSPHRASE` GitHub Actions secret.
  Private key never written to any persistent host disk.
- Public key: `build/keys/RPM-GPG-KEY-clustr.pub` in repo (human-readable; operators
  can verify the key they import matches the repo copy).
  Also served at `https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr` (fetched by rpm).

**Why not reuse the Slurm bundle key?**
The Slurm bundle signing key (`build/slurm/keys/clustr-release.asc.pub`) is a different
trust domain — it signs Slurm RPMs embedded inside initramfs images. Keeping package
distribution signing and bundle signing separate allows independent rotation.

### Release flow

```
push tag v* →
  CI: go test ./... (gate)
  CI: go build clustr-serverd (linux/amd64, linux/arm64) — static binary
  CI: go build clustr (linux/amd64, linux/arm64) — static binary
  CI: for EL in el8 el9 el10; nfpm pkg --packager rpm (both packages × 2 arches × 3 EL)
  CI: rpmsign --addsign all .rpm files with CLUSTR_GPG_PRIVATE_KEY
  CI: createrepo_c per {elN}/{arch}/ subdirectory
  CI: gpg --detach-sign repomd.xml per subdirectory
  CI: rsync repo tree to deploy@66.175.212.72:/var/www/pkg.sqoia.dev/clustr/ via PKG_DEPLOY_KEY
  CI: attach all .rpm + raw binaries + .sha256 to GitHub Release
```

### Versioning

Semver driven by git tags. `nfpm` reads `${VERSION}` from the environment, set to
`github.ref_name` with the leading `v` stripped (e.g. tag `v1.13.0` → version `1.13.0`).

---

## Phase 3 — Verification Log

### v0.1.4 install verification (2026-04-29)

**Date:** 2026-04-29
**Host:** `cloner-server` (192.168.1.151) — Rocky Linux 9.7 (Blue Onyx), x86_64, dev clustr
  install already present (dev binary at /usr/local/bin/clustr-serverd from source).
  RPM installed alongside dev install — both coexist without conflict.

**Installation steps executed:**

```
rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr
dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/el9/clustr.repo
dnf install -y clustr-serverd
```

**`rpm -qi clustr-serverd` output:**
```
Name        : clustr-serverd
Version     : 0.1.4
Release     : 1.el9
Architecture: x86_64
Signature   : RSA/SHA512, Tue 28 Apr 2026 10:45:50 PM PDT, Key ID 69486116d6a2fc98
Packager    : Sqoia Labs <hello@sqoia.dev>
URL         : https://github.com/sqoia-dev/clustr
Summary     : clustr server — HPC node cloning and image management
```

**Package layout verified:**
- `/usr/sbin/clustr-serverd` — binary (24268984 bytes, mode 0755)
- `/usr/lib/systemd/system/clustr-serverd.service` — RPM-variant unit
- `/etc/clustr/clustr-serverd.conf` — EnvironmentFile (config|noreplace)
- `/etc/clustr/`, `/var/lib/clustr/`, `/var/log/clustr/` — directories created by nfpm

**Post-install script:**
- `id clustr` → `uid=989(clustr) gid=988(clustr) groups=988(clustr)` — system user created

**Binary version check:**
```
/usr/sbin/clustr-serverd version
clustr-serverd v0.1.4
  slurm bundle:   v24.11.4-clustr5
  slurm version:  24.11.4
  schema version: 75
```

**Web UI:**
- `curl -sI http://10.99.0.1:8080/` → `HTTP/1.1 200 OK` with correct CSP/security headers

**bootstrap-admin (--force, simulating fresh install):**
```
Admin account created:
  Username: clustr
  Role:     admin
  Password: clustr (force password change required)
```

**Login API response with `clustr/clustr`:**
```json
{"force_password_change": true, "ok": true, "role": "admin"}
```
Session cookie flow confirmed. Username+password fields used (no API key paste).
`force_password_change: true` enforced on first login.

**GPG key fingerprint (verified from pkg.sqoia.dev):**
`9EDB 9E63 AB84 551E 25C1 4168 41E5 1A66 53BB A540`

**Release pipeline fixes required before this version worked:**
1. nfpm v2.41.4 does not exist — bumped to v2.46.3 (v0.1.1)
2. nfpm v2.46.x does not expand `${ARCH}` in `src:` content paths — fixed with
   `envsubst '${ARCH}'` pre-processing before nfpm runs (v0.1.2)
3. RPM signing: `passphrase-fd 3` heredoc fails in rpmsign subprocess — fixed with
   `--passphrase-file` temp file approach (v0.1.3)
4. `--armor` in gpg sign cmd produces ASCII signature, RPM header requires binary
   detached sig — removed `--armor` from `__gpg_sign_cmd` (v0.1.4)

**aarch64 status:** aarch64 packages build and publish successfully. aarch64 RPMs are
present in all three EL subdirectories on pkg.sqoia.dev (el8/aarch64, el9/aarch64,
el10/aarch64). aarch64 install on real ARM hardware not verified in this round — deferred
to v0.1.5 milestone when an EL9 ARM host is available in the lab.

**pkg.sqoia.dev endpoint checks:**
- `https://pkg.sqoia.dev/clustr/el9/x86_64/repodata/repomd.xml` → 200 OK
- `https://pkg.sqoia.dev/clustr/el8/x86_64/repodata/repomd.xml` → 200 OK
- `https://pkg.sqoia.dev/clustr/el10/x86_64/repodata/repomd.xml` → 200 OK
- `https://pkg.sqoia.dev/clustr/el9/aarch64/repodata/repomd.xml` → 200 OK
- `https://pkg.sqoia.dev/clustr/el9/clustr.repo` → correct [clustr] stanza with
  `baseurl=https://pkg.sqoia.dev/clustr/el9/$basearch`
- `https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr` → RSA-4096 key, expires 2028-04-26
