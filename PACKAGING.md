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

### Build tool: nfpm

**Decision: nfpm.** Justification:

- Produces RPM and DEB from a single YAML config with `nfpm pkg --packager rpm` /
  `nfpm pkg --packager deb`. No `rpmbuild` spec file, no Ruby/fpm dependency.
- Go binary, installable in CI with `go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest`
  or via the official release tarball. Zero system-package deps on the GitHub Actions runner.
- Actively maintained (goreleaser project), well-documented, handles pre/post install
  scripts, systemd units, config files, and file permissions natively.
- Alternative evaluated: `fpm` — rejected because it requires Ruby and the gem ecosystem
  in CI, adds ~2 min to runner setup, and nfpm covers identical functionality.

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
2. `chown -R clustr:clustr /var/lib/clustr /var/log/clustr` — note: the service itself
   runs as root due to nspawn/loop/DHCP capability requirements, so the `clustr` user
   is a convention for file ownership rather than a service account in the traditional sense.
   The unit does not carry `User=clustr`.
3. Runs `systemctl daemon-reload`.
4. Does NOT run `systemctl enable` or `systemctl start` — operator opts in.

#### `clustr` package (CLI)

| Path | Purpose |
|------|---------|
| `/usr/bin/clustr` | CLI binary (CGO_ENABLED=0 static) |

No systemd unit, no config directory, no post-install script.

### Repo hosting: pkg.sqoia.dev on the existing Linode Nanode

**Decision: option (a) — `pkg.sqoia.dev` as a Caddy vhost on the existing Linode.**

Justification:
- The Nanode serves two static sites and Uptime Kuma; disk (25 GB SSD) is not under
  pressure. Package artifacts for amd64+arm64 RPM+DEB are ~20–50 MB per release; at
  12 releases/year the cumulative footprint is well under 1 GB. A retention policy
  (keep last 3 releases) caps disk usage indefinitely.
- `pkg.sqoia.dev` is a professional URL. `dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/clustr.repo`
  is clean and operator-friendly. The GitHub Releases flat-repo approach works but
  requires a non-standard repo URL format that confuses some dnf/apt versions.
- Implementation: rsync the package tree from CI to `/var/www/pkg.sqoia.dev/clustr/`
  after signing. Caddy serves it as static files. createrepo_c generates RPM metadata;
  dpkg-scanpackages generates DEB metadata. Both run in CI before the rsync.
- DNS: add `pkg.sqoia.dev` A record pointing to `66.175.212.72` (Linode Nanode IP).
  Caddy auto-provisions TLS via Let's Encrypt.

**GPG signing key strategy:**

- Generate a dedicated Ed25519 GPG key for clustr package signing: `clustr-release@sqoia.dev`.
  This key is separate from SSL certs and SSH keys — single-purpose, rotatable independently.
- Private key: stored as a GitHub Actions secret `CLUSTR_GPG_PRIVATE_KEY` (armored export).
  Passphrase stored as `CLUSTR_GPG_PASSPHRASE`. CI imports the key ephemerally per run;
  the key never touches disk on any persistent host.
- Public key: committed to the repo at `build/keys/RPM-GPG-KEY-clustr-packages.asc` and
  served at `https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr` (RPM) and
  `https://pkg.sqoia.dev/clustr/clustr.gpg` (DEB binary format via `gpg --dearmor`).
- The existing `build/slurm/keys/clustr-release.asc.pub` is a bundle signing key for Slurm
  RPMs inside the initramfs — a different trust domain. Do not reuse it.

### Release flow

```
push tag v* →
  CI: go test ./... (gate)
  CI: go build clustr-serverd (linux/amd64, linux/arm64)
  CI: go build clustr (linux/amd64, linux/arm64)
  CI: nfpm pkg --packager rpm  (amd64 + arm64, both packages)
  CI: nfpm pkg --packager deb  (amd64 + arm64, both packages)
  CI: gpg sign all .rpm files (rpmsign --addsign)
  CI: gpg sign all .deb files (debsign or dpkg-sig)
  CI: createrepo_c → RPM repodata
  CI: dpkg-scanpackages → DEB Packages + Release + InRelease (gpg-signed)
  CI: rsync repo tree to pkg.sqoia.dev via SSH (Linode deploy key)
  CI: attach all .rpm/.deb + .sha256 to GitHub Release
```

### Versioning

Semver driven by git tags. `nfpm` reads `${VERSION}` from the environment, set to
`github.ref_name` with the leading `v` stripped (e.g. tag `v1.13.0` → version `1.13.0`).

---

## Phase 3 — Verification Log

### Fresh Rocky 9 VM install (2026-04-29)

*To be populated after Phase 4 execution.*

Steps planned:
1. Provision fresh Rocky 9 VM in Proxmox lab.
2. `sudo rpm --import https://pkg.sqoia.dev/clustr/RPM-GPG-KEY-clustr`
3. `sudo dnf config-manager --add-repo https://pkg.sqoia.dev/clustr/clustr.repo`
4. `sudo dnf install clustr-serverd`
5. `sudo systemctl enable --now clustr-serverd`
6. `sudo clustr-serverd bootstrap-admin`
7. Open `http://<vm-ip>:8080/` from another lab host, log in with `clustr`/`clustr`.
8. Confirm password change prompt on first login.
9. Verify service survives `systemctl restart clustr-serverd`.
