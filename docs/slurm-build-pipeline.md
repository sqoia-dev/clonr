# Slurm Build & Distribution Pipeline

**Status:** Design — not yet implemented
**Owner:** Richard (architecture), Dinesh (implementation)
**Decision date:** 2026-04-27
**Supersedes:** OpenHPC repo dependency (`slurm_repo_url` pointing at
`repos.openhpc.community`)
**References:**
- [Slurm Quickstart Admin — Prereqs](https://slurm.schedmd.com/quickstart_admin.html#prereqs)
- [SchedMD Downloads](https://www.schedmd.com/downloads.php)
- [Slurm release tarball signing keys](https://www.schedmd.com/security_pubkey.php)
- [MUNGE upstream](https://github.com/dun/munge)

---

## 1. Headline decision

**clustr will build Slurm from upstream source tarballs in GitHub Actions, sign
the resulting RPM/DEB packages with our own GPG key, and distribute them as
GitHub Release assets attached to a `slurm-vX.Y.Z-clustrN` tag. The clustr
deploy flow will install these packages per-deploy by pointing `dnf` at a
release-asset–backed `.repo` file (no external network dependency on
schedmd.com or openhpc.community at deploy time).**

This replaces the OpenHPC repo dependency in `internal/slurm/manager.go` and
`internal/deploy/finalize.go`. The fields `SlurmRepoURL` /
`EnableRequest.SlurmRepoURL` will be retained as a typed value
(`"clustr-builtin"` by default; opaque URL string for power-user override) so
the migration is non-breaking for existing DB rows.

---

## 2. Why we are doing this

| Driver | OpenHPC (today) | clustr-built (target) |
|---|---|---|
| EL10 support | 404 — does not exist | We control the matrix, ship when ready |
| Patch latency | Months behind upstream CVE fixes | Tag-and-build same day |
| Supply-chain provenance | Trust openhpc.community + their build hosts | Trust SchedMD signing key + our CI + our signing key |
| Air-gap / offline install | Requires mirror | Single tarball + repo file, drop on a USB |
| Differentiation | None — everyone uses OpenHPC | "Turnkey HPC, signed by us, no third-party repo" |
| Founder directive | "I dont want to use openhpc repo" | Aligned |

The cost is real: we now own a build pipeline, a signing key, a CVE-watch
process, and a release cadence. This document is the contract for that work.

---

## 3. Scope and non-goals

### In scope (MVP, this sprint)
- Build Slurm from `slurm-X.Y.Z.tar.bz2` for **Rocky/AlmaLinux 9 (EL9), x86_64**
- GPG-verify the upstream tarball against SchedMD's signing key
- Produce a set of RPMs from the upstream `slurm.spec` (in-tarball) via
  `rpmbuild --tb`
- Sign the RPMs with a clustr GPG key
- Attach RPMs + a `clustr-slurm.repo` file + `RPM-GPG-KEY-clustr` to a
  GitHub Release at tag `slurm-vX.Y.Z-clustr1`
- Pull MUNGE from EPEL for now (documented exit ramp)
- Wire `internal/deploy/finalize.go` to consume the new repo URL by default

### In scope (next two sprints, "production-shaped")
- EL10 x86_64 build target (validate spec file builds clean on EL10)
- cosign signing of release artifacts (in addition to RPM GPG signing)
- Scheduled weekly check for new Slurm upstream releases (file an issue)
- MUNGE built from source (mirror the same pipeline)
- Sigstore/Rekor transparency log entry per release

### Out of scope (will not build until demand exists)
- Ubuntu 22.04 / 24.04 DEB packages
- aarch64 builds
- Cross-distro single-tarball + install script ("conda-style") install path
- A self-hosted apt/yum repo server (we use GitHub Releases as the CDN)
- Building Slurm with NVIDIA / GPU plugin support (`--with-nvml`) — adds CUDA
  to the build matrix; defer until a customer asks
- Building with `--with-pmix`, `--with-ofed`, `--with-hdf5` — same reason

The discipline here is: **ship one OS one arch first, prove the pipeline, then
expand the matrix**. A second target costs less than the first because the
pipeline is the artifact, not the binary.

---

## 4. Build environment

### 4.1 Where we build

**GitHub Actions, hosted runners, containerized per-target.**

- Runner: `ubuntu-latest` (free tier, public repo = unlimited minutes)
- Build container: official Rocky Linux base images pulled per-target
  - EL9: `docker.io/rockylinux/rockylinux:9`
  - EL10: `docker.io/rockylinux/rockylinux:10` (when added)
- Why containers (and not the runner OS): the Slurm spec file expects an
  RPM-based build host with the right `BuildRequires`. We get a clean,
  reproducible, version-pinned environment per target without standing up our
  own runner.

**Why not a self-hosted runner on the Linode Nanode or Proxmox?**

- Nanode is 1 GB RAM. A Slurm build with `make -j2` peaks well above that.
  Already excluded by the "never build locally — OOMs the host" rule in our
  team feedback.
- Proxmox lab runner is plausible but introduces a single point of failure
  (the homelab) for what is now part of our release process. Hosted runners
  cost zero, scale to multiple targets in parallel, and require zero ops.
  Revisit if/when we need GPU builds (NVML/CUDA) — those benefit from a beefy
  self-hosted runner with a real GPU.

### 4.2 Build matrix (MVP and one-step-out)

| Target | Container | Arch | MVP | Status |
|---|---|---|---|---|
| EL9 | `rockylinux/rockylinux:9` | x86_64 | YES | Sprint 1 |
| EL10 | `rockylinux/rockylinux:10` | x86_64 | NO | Sprint 2 |
| EL9 | — | aarch64 | NO | Defer |
| EL10 | — | aarch64 | NO | Defer |
| Ubuntu 22.04 | `ubuntu:22.04` | x86_64 | NO | Defer (no customer ask) |

### 4.3 Build dependencies

Pulled directly from the [Slurm admin
prereqs](https://slurm.schedmd.com/quickstart_admin.html#prereqs) and the
`BuildRequires` declared in the upstream `slurm.spec`. The build container
install step must satisfy all of these before `rpmbuild --tb`:

```
# Toolchain
gcc gcc-c++ make rpm-build automake autoconf libtool

# Runtime libs that Slurm links against (each one toggles a feature)
munge munge-devel              # auth (REQUIRED)
mariadb-devel                  # slurmdbd (accounting)
pam-devel                      # pam_slurm
readline-devel                 # interactive scontrol/sacctmgr
hwloc-devel libcurl-devel      # topology, REST tokens
http-parser-devel json-c-devel libyaml-devel libjwt-devel  # slurmrestd
freeipmi-devel rrdtool-devel lz4-devel zlib-devel
numactl-devel dbus-devel kernel-headers
perl-devel perl-ExtUtils-MakeMaker  # perlapi
python3
```

**Optional plugins held back from MVP** (each one expands BuildRequires and
runtime QA surface):
- NVML (`cuda-nvml-devel`) — GPU jobs
- PMIx (`pmix-devel`) — MPI
- HDF5 (`hdf5-devel`) — job profiling
- InfiniBand / OFED — fabric counters

The MVP ships a plain CPU build that is sufficient for the turnkey 2-node
demo. Plugin matrix is a v1.1 conversation.

EPEL must be enabled in the build container to get `munge-devel`,
`http-parser-devel`, `libjwt-devel`, etc. on EL9.

---

## 5. Source acquisition and verification

### 5.1 Where the tarball comes from

`https://download.schedmd.com/slurm/slurm-${SLURM_VERSION}.tar.bz2`

Pinned per release in a single source-of-truth file:
`build/slurm/versions.yml`

```yaml
# build/slurm/versions.yml
slurm:
  version: "24.11.4"          # upstream SchedMD tag
  tarball_sha256: "<sha256>"  # pinned, verified, committed
  schedmd_signing_key_id: "0xCB8E2EB3D04E78CC"  # SchedMD release key
clustr_release: 1             # bump on rebuild without upstream version change
munge:
  source: "epel"              # MVP — exit to "github" in sprint 3
  version: "0.5.16"
```

The full release tag is composed: `slurm-v${slurm.version}-clustr${clustr_release}`,
e.g. `slurm-v24.11.4-clustr1`. The `clustr_release` field exists so we can
reissue a build (e.g., to fix a packaging bug or rebuild against a refreshed
EPEL) without bumping the upstream Slurm version.

### 5.2 Verification chain

The build job MUST perform all three checks before invoking `rpmbuild`:

1. **SHA256 pin check** — recompute `sha256sum` on the downloaded tarball,
   compare against `versions.yml`. Mismatch = abort.
2. **GPG signature check** — fetch
   `slurm-${VERSION}.tar.bz2.asc` from the same SchedMD URL, import the
   SchedMD signing key (key ID also pinned in `versions.yml`, key body
   committed at `build/slurm/keys/schedmd-release.asc`), run
   `gpg --verify`. Failure = abort.
3. **Container provenance** — pin the build container by digest, not tag.
   `rockylinux/rockylinux:9@sha256:<digest>`. Update via PR.

This is a hard chain: any operator (or attacker) substituting the upstream
tarball must compromise all three pins simultaneously, all of which are in
the repo and reviewed at PR time.

### 5.3 Cutting a new release

The release process is a PR that touches one file:

1. Edit `build/slurm/versions.yml` — bump `version`, update `tarball_sha256`,
   reset `clustr_release: 1`
2. Open PR — CI does a dry-run build on EL9 and posts the package list as a
   comment
3. Merge to main
4. Manually tag: `git tag slurm-v${VERSION}-clustr${REL} && git push --tags`
5. Tag push triggers `slurm-build.yml`, which builds, signs, and creates the
   GitHub Release

For a routine clustr release that does not bump Slurm, no Slurm rebuild
happens — the Slurm artifacts live in their own tag namespace, decoupled from
clustr server tags. (clustr-server tags continue to be `vN.M.P`.)

---

## 6. Build outputs

### 6.1 Format: RPMs from the in-tarball spec

Slurm's upstream tarball ships its own `slurm.spec`. The canonical build is:

```
rpmbuild -ta --define "_topdir $PWD/rpmbuild" slurm-${VERSION}.tar.bz2
```

This produces ~20 sub-packages. We ship all of them (the consumer picks what
to install based on role — see §8). Naming follows the upstream convention:
`slurm-X.Y.Z-1.el9.x86_64.rpm`, etc.

Key sub-packages we care about:

| RPM | Purpose | Installed on |
|---|---|---|
| `slurm` | Common libs + tools | All Slurm nodes |
| `slurm-slurmctld` | Controller daemon | Controller |
| `slurm-slurmd` | Worker daemon | Workers |
| `slurm-slurmdbd` | Accounting daemon | Controller (or DB host) |
| `slurm-slurmrestd` | REST API | Controller (optional) |
| `slurm-libpmi` | PMI for MPI | Workers (if MPI) |
| `slurm-perlapi` | Perl bindings (sview, contribs) | Optional |
| `slurm-pam_slurm` | PAM module | Workers |
| `slurm-contribs` | sview, etc. | Optional |
| `slurm-devel` | Headers | Build hosts only |

### 6.2 Why RPMs and not "single tarball + install script"

Three reasons:

1. **The Slurm project supports it.** The `slurm.spec` is upstream-maintained,
   handles user creation, systemd units, and the package split correctly.
   Reinventing this is busywork that drifts.
2. **`dnf` already exists in the deploy chroot.** `installSlurmInChroot` in
   `internal/deploy/finalize.go` already knows how to add a `.repo` file and
   run `dnf install`. Switching from "OpenHPC URL" to "our URL" is a one-line
   config change.
3. **Native dependency resolution.** A single-tarball install script has to
   re-implement RPM's dep graph for `mariadb-libs`, `pam`, `munge`, etc.
   Painful, fragile.

We will ship a tarball+script path eventually for non-RPM distros, but only
when a customer explicitly asks.

### 6.3 Signing

Each RPM is GPG-signed at the end of the build job:

```
rpm --addsign rpmbuild/RPMS/x86_64/*.rpm
```

The signing key is a clustr-owned GPG key (NOT SchedMD's). Key ID pinned in
the README and at `build/slurm/keys/clustr-release.asc.pub`. Private key
stored in GitHub Actions secrets as `CLUSTR_RPM_SIGNING_KEY` (ASCII-armored)
and `CLUSTR_RPM_SIGNING_PASSPHRASE`.

**Key rotation policy:** generate a new signing key annually, ship the new
public key in every clustr release, and keep the old key trusted for one year
of overlap. Document at `docs/security/key-rotation.md` (separate doc, not
this one).

---

## 7. Hosting and distribution

### 7.1 Decision: GitHub Releases as the repo

Three options were considered:

| Option | Cost | Ops | Verifiability | Verdict |
|---|---|---|---|---|
| **GitHub Releases** | $0 | Zero — GitHub hosts | Asset URLs are stable; SHA256 in release notes | **CHOSEN for MVP** |
| Self-hosted yum repo (Caddy + createrepo_c) | Linode storage | Maintain `createrepo_c` cron, TLS cert, repo signing | Standard yum repo workflow | Defer — adds an SPOF on our $7 Linode |
| OCI artifacts (RPMs as ghcr.io blobs) | $0 | Need a fetch-shim in the deploy path | cosign-friendly | Defer — interesting for v2, no benefit today |

The build job assembles a directory layout that looks exactly like a yum
repo, runs `createrepo_c` on it, then attaches the whole thing as a tarball
**and** as individual files to the GitHub Release. The release page also
hosts a generated `clustr-slurm.repo` file:

```ini
# clustr-slurm.repo (uploaded as release asset)
[clustr-slurm]
name=clustr Slurm (built from source)
baseurl=https://github.com/sqoia-dev/clustr/releases/download/slurm-v24.11.4-clustr1/el9-x86_64
enabled=1
gpgcheck=1
gpgkey=https://github.com/sqoia-dev/clustr/releases/download/slurm-v24.11.4-clustr1/RPM-GPG-KEY-clustr
```

Note the `baseurl` points at a directory that `createrepo_c` populated with
`repodata/`. `dnf` follows the same pattern it uses for any other repo. Zero
custom client logic.

### 7.2 Why not own a real repo server yet

Pre-launch, single founder, $7 host already serving two static sites. A
broken `createrepo_c` cron at 2 AM is a self-inflicted page. GitHub Releases
gives us:
- 99.99% uptime
- A worldwide CDN
- Cryptographic provenance (the tag is signed by GitHub)
- Stable URLs we can point `dnf` at directly

We trade off: download speed in some regions (negligible for a 2-node demo
cluster), and we are coupled to GitHub. Both are fine until a real customer
needs an air-gapped install (at which point we hand them a USB with
`createrepo_c` output on it — same artifacts, different transport).

### 7.3 Provenance and supply chain

Every release will publish:

1. RPMs (signed with clustr key)
2. `repodata/` (created by `createrepo_c`)
3. `clustr-slurm.repo` file (points to release URL)
4. `RPM-GPG-KEY-clustr` (public key)
5. `manifest.json` — `{slurm_version, clustr_release, build_sha, container_digest, build_timestamp, sha256_per_rpm}`
6. `manifest.json.sig` — cosign signature (sprint 2)
7. `BUILD-LOG.txt` — full `rpmbuild` stdout/stderr

Human-readable release notes link upstream Slurm changelog and any clustr
patches applied (none planned for MVP — we ship vanilla upstream).

---

## 8. Integration with the clustr deploy flow

### 8.1 Today

- Operator calls `POST /api/v1/modules/slurm/enable
  {"slurm_repo_url": "https://repos.openhpc.community/OpenHPC/3/EL_9"}`
- `internal/slurm/manager.go` stores the URL on `slurm_module_config`
- `internal/deploy/finalize.go::installSlurmInChroot` writes
  `/etc/yum.repos.d/clustr-slurm.repo` inside the chroot pointing at that URL,
  then runs `dnf install -y slurm slurm-slurmctld slurm-slurmd munge`

### 8.2 Target

- The Slurm module defaults to `slurm_repo_url = "clustr-builtin"`
- On enable, the manager resolves `"clustr-builtin"` to the URL embedded at
  build time: `https://github.com/sqoia-dev/clustr/releases/download/slurm-v${X}-clustr${R}/el${EL}-${ARCH}`
- The clustr server binary embeds `slurmBuiltinVersion = "v24.11.4-clustr1"`
  via `-ldflags "-X internal/slurm.builtinVersion=..."`, set in
  `cmd/server/main.go` build args. Bumping the bundled Slurm version is a
  clustr-server release.
- `installSlurmInChroot` is unchanged in shape — it still writes a `.repo`
  file and runs `dnf install`. Only the URL source changes. The
  `gpgcheck=1` line is added, and the public key is also dropped into
  `/etc/pki/rpm-gpg/` inside the chroot. We can ship the public key embedded
  in the clustr server binary (it's ~3 KB) so the deploy is self-contained.
- Operators who want to override (for testing a new build, or for an
  air-gapped mirror) can still pass an explicit URL — same field, same
  validation.

**Decision: per-deploy install (NOT bake into base image).**

| Approach | Pro | Con |
|---|---|---|
| **Per-deploy `dnf install`** (today) | Base image stays generic, version bump = clustr server bump only, no image rebuild required | Adds ~30s to deploy, requires network to GitHub at deploy time |
| Bake Slurm into base image | Faster deploy, air-gap friendly, deterministic | Image rebuild required to bump Slurm, image size +~80 MB, version coupled to image |

For the turnkey flow today, per-deploy wins:

1. The base image already needs network to reach the clustr server during
   deploy — adding GitHub Releases to that allow-list is trivial.
2. Bumping Slurm is then a `versions.yml` PR + new clustr server release;
   does not require operators to rebuild and re-stage gold images.
3. Image size matters for PXE TFTP if we ever bake the rootfs into the
   initramfs; the squashfs streaming model we use today doesn't care about
   80 MB but image authors do.
4. The 30s deploy delta is dominated by the existing rsync + dnf kernel
   install. Slurm install is in the noise.

The base-image-bake path remains a documented option for air-gapped sites,
gated by a `bake_slurm: true` flag on the image build job. Not in MVP.

### 8.3 Code touchpoints

| File | Change |
|---|---|
| `internal/slurm/manager.go` | Resolve `"clustr-builtin"` (and empty string) to the embedded URL; keep arbitrary URL as escape hatch |
| `internal/slurm/builder.go` | If it interacts with repo URL, mirror manager logic |
| `internal/deploy/finalize.go::installSlurmInChroot` | Add `gpgcheck=1` + drop GPG key into chroot at `/etc/pki/rpm-gpg/RPM-GPG-KEY-clustr`; flip `gpgcheck=0` to `gpgcheck=1` in the `.repo` template |
| `internal/deploy/finalize.go::elVersionFromURL` | Recognize the new URL pattern (`/el9-x86_64`, `/el10-x86_64`) in addition to OpenHPC's `EL_9` / `EL_10` |
| `cmd/server/main.go` | New `-ldflags` for `builtinSlurmVersion`, `builtinSlurmRepoBase` |
| `Makefile` (root) | Pass build vars from `build/slurm/versions.yml` into `go build` ldflags |
| `pkg/api/types.go` | If the slurm config struct needs a new `BuiltinVersion` field for diagnostics, add it |
| `docs/slurm-module.md` | Replace OpenHPC table in §2.1 with "clustr ships Slurm bundled — no repo URL needed by default" |
| `scripts/dev-vm/create.sh` | Drop OpenHPC reference if any (current grep shows none — verify in PR) |

---

## 9. CI workflow design

### 9.1 New workflow: `.github/workflows/slurm-build.yml`

**Triggers:**

```yaml
on:
  push:
    tags:
      - "slurm-v*"
  workflow_dispatch:
    inputs:
      slurm_version:
        description: "Override version from versions.yml"
        required: false
  schedule:
    - cron: "0 13 * * 1"   # Mondays 13:00 UTC — check upstream for new releases
```

**Jobs:**

```
jobs:
  check-upstream:        # only on schedule — open issue if new Slurm release
  build:                 # matrix over targets in versions.yml
    needs: []            # tag-triggered runs skip check-upstream
    strategy:
      matrix:
        target:
          - { os: el9,  container: "rockylinux/rockylinux:9@sha256:...",  arch: x86_64 }
    steps:
      - checkout
      - read versions.yml → env
      - import SchedMD pubkey + clustr signing key
      - download slurm tarball
      - sha256 + gpg verify tarball
      - install BuildRequires inside container
      - rpmbuild -ta
      - rpm --addsign on output RPMs
      - createrepo_c repodata
      - generate manifest.json
      - upload-artifact (per target)
  release:
    needs: [build]
    runs-on: ubuntu-latest
    steps:
      - download all build artifacts
      - assemble release tree (el9-x86_64/, el10-x86_64/, ...)
      - generate clustr-slurm.repo file with the release URL filled in
      - cosign sign manifest.json (sprint 2)
      - gh release create slurm-vX.Y.Z-clustrR --notes-file ...
```

### 9.2 Caching strategy

Slurm builds in ~6 minutes on a hosted runner. Worth caching:
- The tarball itself (`actions/cache` keyed on sha256)
- The build container image (`docker pull` is hot via Actions cache)

Not worth caching: the BuildRequires `dnf install` (5 min one-time per
container layer; cache the container layer instead).

### 9.3 Failure modes and behaviour

| Failure | Action |
|---|---|
| Tarball SHA mismatch | Hard fail — possible compromise, page humans |
| GPG verify fail | Hard fail — possible compromise, page humans |
| `rpmbuild` fail | Hard fail — open issue with last 2 KB of log |
| `rpm --addsign` fail | Hard fail — secret may be unset |
| `createrepo_c` fail | Hard fail |
| `gh release create` fail | Soft retry once, then hard fail |
| Schedule found new upstream | Open issue with title `[slurm-build] new upstream Slurm vX.Y.Z available` |

CI must be watched per the standing rule. The workflow attaches a status
check named `slurm-build / build (el9-x86_64)` that the operator polls until
green before declaring the release complete.

---

## 10. MUNGE

MUNGE is a hard runtime dependency of Slurm. Two viable paths:

| Option | Trust | Effort | Recommendation |
|---|---|---|---|
| **EPEL `munge` package** | Red Hat–adjacent, signed, well-maintained | Zero | **MVP** |
| Build from `dun/munge` source, mirror Slurm pipeline | We own it end to end | Real — second pipeline | Sprint 3 |

**MVP decision: EPEL.** EPEL is treated by RHEL/Rocky as part of the standard
ecosystem; pinning to a specific EPEL package + GPG-verified by the EPEL
signing key is acceptable supply-chain hygiene for a v0.x product. The build
job for Slurm declares EPEL as the source of `munge-devel`, so MVP is
internally consistent.

**Sprint 3 exit ramp:** add `build/munge/versions.yml`, new
`munge-build.yml` workflow that mirrors the Slurm pipeline. Same tarball
verify → rpmbuild → sign → release pattern. We then publish our own MUNGE
RPMs in the same release as the matching Slurm version, and the `.repo` file
includes both. The deploy code does not change — `dnf install munge` just
resolves to our package instead of EPEL's.

We do NOT bundle MUNGE into the Slurm spec or install script; it stays a
separate package with its own version axis.

---

## 11. Migration path

### Sprint 1 (MVP — this sprint)

Goal: turnkey 2-node EL9 cluster boots on clustr-built Slurm RPMs, no
OpenHPC dependency anywhere.

1. **Day 1**: land this design doc (DONE when this PR merges)
2. **Day 1-2** (Dinesh): create `build/slurm/versions.yml`,
   `build/slurm/keys/`, `.github/workflows/slurm-build.yml`
3. **Day 2** (Gilfoyle): generate clustr GPG signing key, store in GitHub
   Actions secrets, commit public key
4. **Day 2-3** (Dinesh): land workflow, dry-run on a `slurm-v24.11.4-rc1`
   tag, verify RPMs build clean and are signed
5. **Day 3**: cut `slurm-v24.11.4-clustr1` tag, verify GitHub Release page
6. **Day 4** (Dinesh): wire `internal/slurm/manager.go` and
   `internal/deploy/finalize.go` to default to the new repo URL; keep
   user-supplied URL override
7. **Day 4-5** (Gilfoyle, lab-validate): run a full deploy on cloner dev
   host (192.168.1.151) against vm201/vm202, confirm `srun hostname` works
8. **Day 5**: update `docs/slurm-module.md` — remove OpenHPC table, document
   the bundled-version model

**MVP exit criteria:**
- A clean `clustr-server` binary boots a 2-node Slurm cluster end-to-end
  with no `slurm_repo_url` set
- The `dnf install` step inside the chroot pulls signed RPMs from a
  GitHub Release URL, with `gpgcheck=1`
- CI green on both `clustr` repo and the `slurm-build.yml` workflow run

### Sprint 2 (production-shaped)

- EL10 x86_64 build target
- cosign signing of manifest + Rekor entry
- Scheduled upstream-watch job (auto-open issue)
- `RPM-GPG-KEY-clustr` rotation runbook
- Document air-gap install procedure (download release tarball, scp to a
  local web server, override `slurm_repo_url`)

### Sprint 3 (own the chain)

- Build MUNGE from source, mirror pipeline
- Optional: NVML build flavor for GPU clusters
- Optional: PMIx build flavor for MPI shops

### Sprint 4+ (only when a customer asks)

- Ubuntu DEB pipeline
- aarch64
- Self-hosted yum repo (would re-evaluate against OCI artifacts)

---

## 12. Tradeoffs we are explicitly accepting

| Tradeoff | Why it's acceptable |
|---|---|
| We now own a CVE-watch process for Slurm | Schedule + scheduled workflow keeps us honest; SchedMD security mailing list is public |
| GitHub Releases is our CDN — coupled to GitHub | We are already coupled to GitHub for source, CI, container registry. One more dimension of the same risk. |
| MVP is EL9-only — same matrix coverage as today | We were already EL9-only because OpenHPC EL10 doesn't exist. Net surface is unchanged. |
| First-deploy install needs internet | Already true today (current OpenHPC path). Air-gap path is a Sprint 2 doc, not new infra. |
| Plugin matrix is intentionally narrow (no CUDA, no PMIx, no IB) | Adds 4 build flavors and a CUDA toolkit to CI for zero current customers. Cost > benefit. |
| Per-deploy install adds ~30s vs. baked image | Negligible vs. existing kernel install + rsync time. Operationally simpler. |

---

## 13. Reversibility classification

| Decision | Type | Why |
|---|---|---|
| Build from upstream tarball (vs. fork patch series) | **Reversible** | We can fork later if we ever need to carry patches |
| RPM packaging from in-tarball spec | **Reversible** | Switch to debs/tarball later without breaking RPM consumers |
| GitHub Releases as CDN | **Reversible** | Same artifacts, different `baseurl` — config change |
| Per-deploy install (vs. bake) | **Reversible** | Adding bake-mode is additive, no schema change |
| EL9 x86_64 first | **Reversible** | Add matrix entries, no architectural rework |
| **clustr GPG signing key identity** | **Irreversible-ish** | Key rotation is supported but every consumer that ever trusted the old key keeps it forever. Treat with care. |
| **`SlurmRepoURL = "clustr-builtin"` sentinel value in DB** | **Irreversible** | Ships as part of API + data model. Renaming requires a migration. Get the name right in PR review. |
| Bundling `builtinSlurmVersion` into the server binary | **Reversible** | Could move to a config file later |

The only thing in this design that is genuinely hard to back out of is the
GPG key identity and the sentinel string in the DB. Both warrant careful PR
review.

### Kill criteria

This whole approach should be revisited if any of these become true:

- We ship 5+ build flavors (CUDA, PMIx, IB, etc.) — at that point a real
  repo server starts paying for itself
- A customer needs day-zero CVE patching — we may need to commit to faster
  release cadence than "tag, build, release" allows
- GitHub Releases changes their ToS to disallow binary distribution — flip
  to OCI artifacts on ghcr.io
- We need to ship to genuinely air-gapped customers regularly — invest in
  signed offline bundles + an installer

---

## 14. Open questions for founder review

1. **Naming**: `slurm-v24.11.4-clustr1` vs. `slurm/v24.11.4-clustr1` for the
   tag. The `-clustr1` suffix lets us reissue without bumping upstream
   version. Alternative: omit the suffix and force-republish (dangerous).
   Recommendation: keep `-clustrN` suffix.
2. **Upstream version policy**: pin to latest stable, or pin to one minor
   version behind for "boring" bias? Recommendation: latest stable, with the
   weekly upstream-watch job opening an issue rather than auto-PRing the
   bump.
3. **Single-key vs. per-target signing key**: one clustr GPG key for all
   RPMs, or one per OS family? Recommendation: single key. Simpler. Rotate
   annually.
4. **Plugin flavors**: confirm MVP ships zero optional plugins. Customers can
   request CUDA in v1.1.

---

## 15. Implementation handoff (for Dinesh)

When picking this up, the build-script + CI workflow can be implemented
without touching any clustr server code. Suggested order:

1. **Bootstrap** (no clustr code change):
   - Create `build/slurm/versions.yml` with Slurm 24.11.4 pinned
   - Commit SchedMD's release pubkey at `build/slurm/keys/schedmd-release.asc`
     (download from https://www.schedmd.com/security_pubkey.php and verify
     out-of-band before committing)
   - Generate the clustr signing key offline:
     `gpg --quick-gen-key "clustr Release Key (slurm) <robert.romero@sqoia.dev>" rsa4096 sign 2y`
     Export public to `build/slurm/keys/clustr-release.asc.pub`. Export
     private to a file, base64-encode, store as
     `CLUSTR_RPM_SIGNING_KEY` GitHub Actions secret. Set passphrase as
     `CLUSTR_RPM_SIGNING_PASSPHRASE`.
2. **Workflow scaffold**:
   - Write `.github/workflows/slurm-build.yml` per §9.1
   - First implement the tag-triggered path; defer schedule + workflow_dispatch
     to a follow-up
   - Test with a `slurm-v24.11.4-clustr0` "release candidate" tag — verify
     output looks right, then delete the tag and the release
3. **Cut MVP release**: tag `slurm-v24.11.4-clustr1`, verify GitHub Release
   page has all artifacts, manifest.json is consistent, RPMs are signed
   (`rpm -K *.rpm` against the imported public key)
4. **Wire deploy path**:
   - Add `builtinSlurmVersion` and `builtinSlurmRepoBase` build vars
     (ldflags) to `cmd/server/main.go`, populated from `versions.yml` via
     Makefile
   - Update `internal/slurm/manager.go` to resolve `"clustr-builtin"` →
     `builtinSlurmRepoBase + "/el${EL}-${ARCH}"`
   - Update `installSlurmInChroot` to write the GPG key into
     `/etc/pki/rpm-gpg/` inside the chroot and flip `gpgcheck=0` →
     `gpgcheck=1` in the rendered `.repo` content
   - Update `elVersionFromURL` to recognize `/el9-x86_64/` and `/el10-x86_64/`
     URL forms
5. **Lab validation** (Gilfoyle): full vm201 + vm202 deploy on cloner host,
   verify `srun hostname` returns from both nodes

Things explicitly NOT in this handoff (do not start without explicit go):
- MUNGE from-source pipeline
- EL10 build target
- cosign / Rekor integration
- Scheduled upstream-watch
- Image-bake mode

Watch CI on every push per the standing rule. Do not declare the MVP done
until the slurm-build workflow has produced a green release **and** the
clustr-server `ci.yml` is green on the commit that wires up the new repo
URL.
