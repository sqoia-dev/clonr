# Slurm Build & Distribution Pipeline

**Status:** Design — not yet implemented
**Owner:** Richard (architecture), Dinesh (implementation)
**Decision date:** 2026-04-27
**Last revised:** 2026-04-27 (founder refinement: deployed nodes pull from the
clustr-server itself, not from GitHub)
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
the resulting RPM packages with our own GPG key, and ship them as a versioned
bundle. The clustr-server itself becomes the package repository for deployed
nodes: at server-install time, the installer fetches the bundle from a GitHub
Release and unpacks it under `/var/lib/clustr/repo/`; clustr-server then
serves that directory as a standard yum/dnf repo at
`http://<clustr-server>/repo/<distro>-<arch>/`. Deployed nodes point `dnf` at
the clustr-server, never at GitHub or schedmd.com.**

GitHub Releases is the **build artifact destination** (where CI publishes
signed RPMs). The clustr-server is the **distribution endpoint** (what end
nodes actually pull from). End nodes never reach GitHub at deploy time.

This replaces the OpenHPC repo dependency in `internal/slurm/manager.go` and
`internal/deploy/finalize.go`. The fields `SlurmRepoURL` /
`EnableRequest.SlurmRepoURL` will be retained as a typed value
(`"clustr-builtin"` by default, which resolves to the clustr-server's own
`/repo/` URL; opaque URL string for power-user override) so the migration is
non-breaking for existing DB rows.

---

## 2. Why we are doing this

| Driver | OpenHPC (today) | clustr-built + server-hosted (target) |
|---|---|---|
| EL10 support | 404 — does not exist | We control the matrix, ship when ready |
| Patch latency | Months behind upstream CVE fixes | Tag-and-build same day |
| Supply-chain provenance | Trust openhpc.community + their build hosts | Trust SchedMD signing key + our CI + our signing key |
| Air-gap / offline install | Requires building a mirror | Built-in — clustr-server **is** the mirror |
| Deploy-time external network | Required (openhpc.community) | None — nodes only talk to the clustr-server |
| Differentiation | None — everyone uses OpenHPC | "Turnkey HPC, signed by us, fully self-contained" |
| Founder directive | "I dont want to use openhpc repo" + "part of the clustr bundle, has its own repo clients pull from" | Aligned |

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
- Run `createrepo_c` at **build time** so the bundle ships pre-generated
  `repodata/` (no runtime dependency on `createrepo_c` on the clustr-server
  host)
- Package the signed RPMs + `repodata/` + `RPM-GPG-KEY-clustr` + `manifest.json`
  as a single bundle artifact: `clustr-slurm-bundle-vX.Y.Z-clustr1.tar.gz`
- Attach the bundle + individual RPMs to a GitHub Release at tag
  `slurm-vX.Y.Z-clustr1` (individual files for transparency / `rpm -K`
  spot-check; bundle is the canonical install artifact)
- **clustr-server installer**: fetch the bundle from the GitHub Release
  matching the embedded `builtinSlurmVersion`, verify, unpack to
  `/var/lib/clustr/repo/el9-x86_64/`, write `RPM-GPG-KEY-clustr` to
  `/var/lib/clustr/repo/RPM-GPG-KEY-clustr`. Idempotent.
- **clustr-server runtime**: serve `/var/lib/clustr/repo/` over HTTP at
  `/repo/*` using `http.FileServer` (same chi router as `/ui/*` today).
  Public endpoint, no auth (it's signed RPMs anyway). Range requests + ETag
  via stdlib defaults — sufficient for `dnf`.
- Embed the clustr signing pubkey in the server binary (Go `embed` of
  `build/slurm/keys/clustr-release.asc.pub`) so a freshly installed server
  can write the keyfile without a network round-trip
- Pull MUNGE from EPEL for now (documented exit ramp). Note: EPEL is fetched
  by the deployed node, not the clustr-server — see §10 for the air-gap exit.
- Wire `internal/deploy/finalize.go::installSlurmInChroot` to default the
  `.repo` URL to `cfg.ServerURL + "/repo/el9-x86_64/"` and inject the GPG key
  from the embedded pubkey

### In scope (next two sprints, "production-shaped")
- EL10 x86_64 build target (validate spec file builds clean on EL10)
- cosign signing of release artifacts (in addition to RPM GPG signing)
- Scheduled weekly check for new Slurm upstream releases (file an issue)
- MUNGE built from source (mirror the same pipeline) — at which point MUNGE
  RPMs ship in the same bundle and the deploy-time EPEL dependency goes away
  (true air-gap)
- Sigstore/Rekor transparency log entry per release
- Multi-version repo serving (host both `el9-x86_64-24.05/` and
  `el9-x86_64-24.11/` simultaneously) — only if a real customer needs to
  span versions

### Out of scope (will not build until demand exists)
- Ubuntu 22.04 / 24.04 DEB packages
- aarch64 builds
- Cross-distro single-tarball + install script ("conda-style") install path
- Building Slurm with NVIDIA / GPU plugin support (`--with-nvml`) — adds CUDA
  to the build matrix; defer until a customer asks
- Building with `--with-pmix`, `--with-ofed`, `--with-hdf5` — same reason
- Hosting multiple concurrent Slurm versions on a single clustr-server
  (one bundle per server release; see §6.4)

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

### 6.4 Bundle packaging

After signing, the build job produces a single artifact:
`clustr-slurm-bundle-vX.Y.Z-clustrR-el9-x86_64.tar.gz`

Layout inside the tarball:

```
clustr-slurm-bundle/
├── manifest.json                 # version, sha256s, build provenance
├── RPM-GPG-KEY-clustr            # public key (also embedded in server binary)
└── el9-x86_64/
    ├── slurm-24.11.4-1.el9.x86_64.rpm
    ├── slurm-slurmctld-24.11.4-1.el9.x86_64.rpm
    ├── slurm-slurmd-24.11.4-1.el9.x86_64.rpm
    ├── ... (other sub-packages)
    └── repodata/
        ├── repomd.xml
        ├── repomd.xml.asc        # repodata signed with the same clustr key
        ├── primary.xml.gz
        ├── filelists.xml.gz
        └── other.xml.gz
```

**Why pre-generate `repodata/` at build time:**

1. `createrepo_c` becomes a build-host dependency (already present in the
   build container), not a runtime dependency on the clustr-server
   (Linode Nanode, plus future air-gap installs on minimal RHEL hosts).
2. Repo metadata is part of the supply-chain artifact — it's covered by the
   bundle's manifest checksum. A clustr-server cannot accidentally generate
   inconsistent metadata between installs.
3. Operators who side-load the bundle onto a USB stick get a working repo
   the moment they untar it. No post-processing.

**Why a single bundle artifact (option C from the design discussion):**

| Option | Pro | Con | Verdict |
|---|---|---|---|
| **A: Bundle baked into clustr-server release tarball** | Single download for the operator | clustr-server tarball balloons by ~80 MB per Slurm flavor; couples server release to Slurm release | Reject |
| **B: Server installer fetches RPMs individually from GitHub on first run** | Smaller server binary | N HTTP round-trips, fragile, can't side-load to air-gap host | Reject |
| **C: Separate `clustr-slurm-bundle-*.tar.gz` artifact, fetched once** | Server stays small; bundle is one atomic download; trivially side-loadable for air-gap; bundle version pin is a single ldflag in the server binary | Two artifacts to manage per release | **CHOSEN** |

The clustr-server binary embeds `builtinSlurmBundleVersion = "v24.11.4-clustr1"`
via `-ldflags`. The installer (and a runtime self-heal job) fetches
`clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz` from
`https://github.com/sqoia-dev/clustr/releases/download/slurm-v24.11.4-clustr1/`
exactly once per server install, verifies it against the bundle SHA256 also
embedded in the binary, and unpacks to `/var/lib/clustr/repo/`. After that,
the server has no dependency on GitHub for Slurm distribution.

**Multi-version policy (v1):** one Slurm bundle per clustr-server release.
Bumping Slurm = a new clustr-server release. This is dramatically simpler
than concurrent multi-version hosting and matches the actual user need (a
clustr cluster runs one Slurm version). When/if multi-version hosting is
needed, the layout already supports it (`/repo/el9-x86_64-24.11/`,
`/repo/el9-x86_64-24.05/`) — additive change, no rework.

---

## 7. Hosting and distribution

### 7.1 Two-layer model: GitHub Releases is the build sink, clustr-server is the deploy source

The distribution architecture has two distinct layers that must not be
conflated:

| Layer | What it is | Who consumes it | Network reachability required |
|---|---|---|---|
| **Build artifact layer (GitHub Release)** | Where CI publishes signed bundles | The clustr-server installer (once per server install) | clustr-server host needs HTTPS to `github.com` at install time |
| **Distribution layer (clustr-server `/repo/`)** | Where deployed nodes pull RPMs | Deployed cluster nodes (dnf at deploy time, plus any future `dnf update` runs) | Deployed nodes only need HTTP to the clustr-server (already mandatory for everything else) |

Deployed nodes never reach GitHub. The clustr-server is the package
repository for the cluster it manages. This is the founder refinement.

### 7.2 Why this beats the GitHub-direct model considered earlier

The original design (commit `0caa32f`) pointed deployed nodes' `dnf` directly
at GitHub Release URLs. That worked but had three real costs:

1. **Every deployed node needs internet** to install/update Slurm. For an
   on-prem HPC cluster behind a corporate firewall, this is often a no-go.
2. **GitHub asset URLs leak our distribution path** to every node, including
   transient compute nodes. Operationally noisy.
3. **No air-gap story without separate work.** With the bundled-server model,
   air-gap is the *default* — the clustr-server is already self-contained.
   The operator running an air-gapped install just needs to side-load the
   bundle onto the clustr-server host, not onto every cluster node.

### 7.3 Decision: clustr-server serves the repo via stdlib `http.FileServer`

Two viable serving options:

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Stdlib `http.FileServer` mounted on chi at `/repo/*`** | Same router as `/ui/*` today; range requests + ETag from stdlib; one binary; no reverse-proxy ordering issues | Goes through clustr-server's process (CPU, FDs) | **CHOSEN** |
| Caddy serves `/var/lib/clustr/repo/` directly | Slightly less load on clustr-server; can apply Caddy's caching | Adds Caddy as a hard dep on the clustr-server host (today it's optional — only needed for TLS termination on multi-tenant deploys); needs config coordination between two processes; `dnf` traffic dwarfs UI traffic anyway | Reject |

The stdlib choice is aligned with current architecture: `internal/server/server.go`
already mounts `http.FileServer(http.FS(staticFS))` for the embedded UI on
`/ui/*`. Adding a sibling `r.Handle("/repo/*", http.StripPrefix("/repo",
http.FileServer(http.Dir("/var/lib/clustr/repo"))))` is a four-line change.

`dnf` requires byte-range requests for `repodata/` lookups; stdlib
`http.FileServer` already implements `Range:` correctly. ETag/Last-Modified
are also handled by stdlib. We add `Cache-Control: public, max-age=300` to
the response for `repodata/repomd.xml` so dnf can cheaply re-check, and a
longer `max-age=86400` for the actual RPM blobs (which are content-addressed
by version in their filenames).

**Endpoint:** `GET /repo/<distro>-<arch>/...` — public, no auth, no API key.
The artifacts are GPG-signed; serving them anonymously is the standard
yum/dnf model.

### 7.4 Server-install and self-heal flow

The clustr-server installer (`scripts/install.sh` and the autodeploy unit on
the cloner host) gains a "fetch slurm bundle" step:

```
1. Read builtinSlurmBundleVersion + builtinSlurmBundleSHA256 from the
   clustr-serverd binary (compiled-in via ldflags).
2. If /var/lib/clustr/repo/.installed-version == builtinSlurmBundleVersion,
   skip (idempotent).
3. Download
   https://github.com/sqoia-dev/clustr/releases/download/<tag>/clustr-slurm-bundle-<ver>-el9-x86_64.tar.gz
   to /var/lib/clustr/repo/.staging/.
4. Verify SHA256 against the embedded value. Mismatch = abort, leave existing
   /var/lib/clustr/repo/ untouched.
5. Atomic-rename the staging dir into place, write
   /var/lib/clustr/repo/.installed-version.
6. Write /var/lib/clustr/repo/RPM-GPG-KEY-clustr from the embedded pubkey.
   (Belt-and-braces — the bundle also contains it; we trust the embedded one
   as the source of truth.)
```

Failure modes:
- **No internet at install time** → installer logs a clear "manual bundle
  install required" error with the exact URL and SHA256 the operator must
  side-load. This is the air-gap path: `scp clustr-slurm-bundle-*.tar.gz
  root@clustr-server:/var/lib/clustr/repo/.staging.tar.gz; clustr-serverd
  bundle install /var/lib/clustr/repo/.staging.tar.gz`. (We add a
  `bundle install` subcommand to the server binary for this.)
- **Server upgrade with new bundle version** → autodeploy detects the
  version mismatch, re-runs the fetch+verify+swap. Old bundle is preserved
  at `/var/lib/clustr/repo/.previous` for one cycle in case rollback is
  needed.

### 7.5 GPG key distribution

The clustr signing pubkey lives in three places, by intent:

| Location | Source | Used by |
|---|---|---|
| `build/slurm/keys/clustr-release.asc.pub` (in repo) | Source of truth, reviewed at PR | Build job (for `rpm --addsign` verification), Go `embed` directive |
| Embedded in `clustr-serverd` binary via `//go:embed build/slurm/keys/clustr-release.asc.pub` | Compiled in at build time | Server installer writes it to `/var/lib/clustr/repo/RPM-GPG-KEY-clustr` and (via deploy code) into the chroot at `/etc/pki/rpm-gpg/RPM-GPG-KEY-clustr` |
| Inside the bundle artifact | `clustr-slurm-bundle-*.tar.gz` includes `RPM-GPG-KEY-clustr` for transparency / offline verification | Operator running `rpm -K` to spot-check signatures |

Embedding in the binary is the right pattern because (a) the deploy code
runs server-side and needs to inject the key into the deployed node's
chroot before `dnf install`, (b) it removes any race between bundle install
and first deploy, (c) the key is ~3 KB, and (d) a key rotation already
requires a server release, so co-locating them is the natural cadence.

### 7.6 Provenance and supply chain

Every release publishes (on GitHub):

1. `clustr-slurm-bundle-vX.Y.Z-clustrR-el9-x86_64.tar.gz` — the canonical
   install artifact (RPMs + repodata + key + manifest)
2. Individual RPMs (for transparency — operators can `rpm -K` them
   independently)
3. `RPM-GPG-KEY-clustr` (also in bundle, exposed top-level for visibility)
4. `manifest.json` — `{slurm_version, clustr_release, build_sha,
   container_digest, build_timestamp, bundle_sha256, sha256_per_rpm}`
5. `manifest.json.sig` — cosign signature (sprint 2)
6. `BUILD-LOG.txt` — full `rpmbuild` stdout/stderr

Human-readable release notes link upstream Slurm changelog and any clustr
patches applied (none planned for MVP — we ship vanilla upstream).

The end-user node sees none of these directly. It sees:
`http://<clustr-server>/repo/el9-x86_64/repodata/repomd.xml` and the RPM
URLs that flow from there. That is the entire surface.

---

## 8. Integration with the clustr deploy flow

### 8.1 End-to-end data flow

This is the canonical picture. Three stages, three different network paths.

```
┌─────────────────────────────────────────────────────────────────────────┐
│ STAGE 1 — BUILD TIME (in CI, on tag push)                               │
│                                                                          │
│   github.com/sqoia-dev/clustr (tag: slurm-v24.11.4-clustr1)             │
│              │                                                           │
│              ▼                                                           │
│   GitHub Actions (slurm-build.yml)                                       │
│     - download slurm-24.11.4.tar.bz2 from schedmd.com                   │
│     - sha256 + GPG verify against SchedMD key                           │
│     - rpmbuild -ta in rockylinux:9 container                            │
│     - rpm --addsign with CLUSTR_RPM_SIGNING_KEY                         │
│     - createrepo_c repodata/                                             │
│     - tar czf clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz    │
│              │                                                           │
│              ▼                                                           │
│   GitHub Release at tag slurm-v24.11.4-clustr1                          │
│     - clustr-slurm-bundle-*.tar.gz       ← canonical install artifact   │
│     - individual RPMs                     ← transparency                │
│     - manifest.json                                                      │
│     - RPM-GPG-KEY-clustr                                                 │
└─────────────────────────────────────────────────────────────────────────┘
                              │
                              │ (operator runs install.sh OR autodeploy
                              │  pulls a new clustr-server release whose
                              │  embedded builtinSlurmBundleVersion bumped)
                              ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ STAGE 2 — SERVER-INSTALL TIME (once per clustr-server install/upgrade)  │
│                                                                          │
│   clustr-server installer on the management host                         │
│     - read embedded builtinSlurmBundleVersion + SHA256 from binary       │
│     - HTTPS GET github.com/.../clustr-slurm-bundle-*.tar.gz             │
│     - verify SHA256                                                      │
│     - tar xzf into /var/lib/clustr/repo/.staging/                        │
│     - atomic rename → /var/lib/clustr/repo/el9-x86_64/                   │
│     - write /var/lib/clustr/repo/RPM-GPG-KEY-clustr (from embed)         │
│     - mark .installed-version                                            │
│              │                                                           │
│              ▼                                                           │
│   clustr-serverd starts                                                  │
│     - chi router mounts /repo/* → http.FileServer(/var/lib/clustr/repo)  │
│     - public, no auth (signed RPMs)                                      │
└─────────────────────────────────────────────────────────────────────────┘
                              │
                              │ (operator triggers a node deploy)
                              ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ STAGE 3 — DEPLOY TIME (once per node deploy)                             │
│                                                                          │
│   clustr-server's deploy code (finalize.go::installSlurmInChroot)        │
│     - URL = cfg.ServerURL + "/repo/el9-x86_64/"                          │
│     - write into chroot at /etc/yum.repos.d/clustr-slurm.repo:           │
│         baseurl=http://<clustr-server>:<port>/repo/el9-x86_64/          │
│         gpgcheck=1                                                       │
│         gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr               │
│     - write embedded pubkey into chroot at                               │
│         /etc/pki/rpm-gpg/RPM-GPG-KEY-clustr                              │
│     - chroot dnf install -y slurm slurm-slurmctld slurm-slurmd munge     │
│              │                                                           │
│              ▼                                                           │
│   Deployed node sends HTTP GET /repo/el9-x86_64/repodata/repomd.xml      │
│   to the clustr-server. Pulls signed RPMs. Verifies signatures locally   │
│   against the embedded key. Installs.                                    │
│                                                                          │
│   Deployed node never reaches github.com or schedmd.com.                 │
└─────────────────────────────────────────────────────────────────────────┘
```

External network reach summary:

| Actor | Reaches | When |
|---|---|---|
| GHA build runner | schedmd.com (tarball), GitHub (release upload) | At tag push |
| clustr-server host | github.com (one-time bundle fetch) | At server install/upgrade |
| Deployed cluster nodes | clustr-server only (port 8080) | At every deploy + every `dnf update` |
| Deployed cluster nodes | EPEL (for MUNGE only) | At deploy time, until §10 sprint 3 lands |

### 8.2 Today (pre-change)

- Operator calls `POST /api/v1/modules/slurm/enable
  {"slurm_repo_url": "https://repos.openhpc.community/OpenHPC/3/EL_9"}`
- `internal/slurm/manager.go` stores the URL on `slurm_module_config`
- `internal/deploy/finalize.go::installSlurmInChroot` writes
  `/etc/yum.repos.d/clustr-slurm.repo` inside the chroot pointing at that URL,
  then runs `dnf install -y slurm slurm-slurmctld slurm-slurmd munge`

### 8.3 Target

- The Slurm module defaults to `slurm_repo_url = "clustr-builtin"`
- On enable, the manager resolves `"clustr-builtin"` to the URL derived at
  request-handling time from the running server's own URL:
  `cfg.ServerURL + "/repo/el${EL}-${ARCH}/"`. There is no hard-coded GitHub
  URL anywhere in the deploy path. (`cfg.ServerURL` is the same value used
  to construct `verifyBootURL` today — see `cmd/clustr/main.go:652` and
  `internal/deploy/phonehome.go`.)
- `installSlurmInChroot` is unchanged in shape — it still writes a `.repo`
  file and runs `dnf install`. The changes:
  - `baseurl` now points at the clustr-server's `/repo/<distro>-<arch>/`
  - `gpgcheck=1` is set
  - The embedded clustr pubkey is written into the chroot at
    `/etc/pki/rpm-gpg/RPM-GPG-KEY-clustr`
  - `gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr` (file:// not http://
    — the key is already on disk, no point in another HTTP round-trip)
- The clustr-server binary embeds `builtinSlurmBundleVersion = "v24.11.4-clustr1"`
  and `builtinSlurmBundleSHA256` via `-ldflags`. Bumping the bundled Slurm
  version is a clustr-server release.
- Operators who want to override (for testing a new build, an air-gapped
  alternate mirror, or a customer's own internal yum repo) can still pass an
  explicit URL via the existing `slurm_repo_url` field — same field, same
  validation. The new `"clustr-builtin"` sentinel just means "use my
  clustr-server's `/repo/`".

**Decision: per-deploy install (NOT bake into base image).** Same rationale
as before — restated for completeness:

| Approach | Pro | Con |
|---|---|---|
| **Per-deploy `dnf install` from clustr-server `/repo/`** | Base image stays generic, version bump = clustr-server bump only, no image rebuild required, deploys still work fully air-gapped (clustr-server is local) | Adds ~30s to deploy |
| Bake Slurm into base image | Faster deploy by ~30s, fully deterministic image | Image rebuild required to bump Slurm, image size +~80 MB, version coupled to image |

For the turnkey flow, per-deploy wins:

1. With the bundled-server-repo model, the deployed node never needs
   external internet for Slurm — `dnf` hits the clustr-server, which is
   already in the same management VLAN. The "needs internet at deploy time"
   downside of per-deploy install is gone.
2. Bumping Slurm is a `versions.yml` PR + new clustr-server release.
   Operators do not need to rebuild and re-stage gold images.
3. Image size matters for PXE TFTP if we ever bake the rootfs into the
   initramfs; the squashfs streaming model we use today doesn't care about
   80 MB but image authors do.
4. The 30s deploy delta is dominated by the existing rsync + dnf kernel
   install. Slurm install is in the noise.

The base-image-bake path remains a documented option for sites where the
deployed nodes must boot with Slurm pre-installed (true zero-deploy-time
install), gated by a `bake_slurm: true` flag on the image build job. Not in
MVP.

### 8.4 Code touchpoints

| File | Change |
|---|---|
| `internal/slurm/manager.go` | Resolve `"clustr-builtin"` (and empty string) to `cfg.ServerURL + "/repo/el${EL}-${ARCH}/"`; keep arbitrary URL as escape hatch |
| `internal/slurm/builder.go` | If it interacts with repo URL, mirror manager logic |
| `internal/deploy/finalize.go::installSlurmInChroot` | (a) baseurl from manager, (b) flip `gpgcheck=0` → `gpgcheck=1`, (c) write embedded pubkey into chroot at `/etc/pki/rpm-gpg/RPM-GPG-KEY-clustr`, (d) `gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr` |
| `internal/deploy/finalize.go::elVersionFromURL` | Recognize the new URL pattern (`/repo/el9-x86_64/`, `/repo/el10-x86_64/`) in addition to OpenHPC's `EL_9` / `EL_10` |
| `internal/server/server.go` | Mount `/repo/*` → `http.StripPrefix("/repo", http.FileServer(http.Dir(cfg.RepoDir)))` next to existing `/ui/*` handler. Public route, **above** the `apiKeyAuth` middleware on `/api/v1`. Add `Cache-Control` middleware for `/repo/repodata/*` (300s) and `/repo/**/*.rpm` (86400s). |
| `internal/config/config.go` | Add `RepoDir string` (default `/var/lib/clustr/repo`, env `CLUSTR_REPO_DIR`) |
| `cmd/clustr-serverd/main.go` | New `-ldflags` for `builtinSlurmBundleVersion`, `builtinSlurmBundleSHA256`, `builtinSlurmVersion`. New `bundle install <path>` subcommand for air-gap side-load. |
| `cmd/clustr-serverd/bundle.go` (new) | Bundle fetch + verify + atomic-swap logic. Called by installer and at startup if `.installed-version` mismatches. |
| `internal/server/keys.go` (new or extend existing) | `//go:embed build/slurm/keys/clustr-release.asc.pub` + accessor returning `[]byte` |
| `Makefile` (root) | Read `build/slurm/versions.yml`, pass `builtinSlurmBundleVersion` / `builtinSlurmBundleSHA256` / `builtinSlurmVersion` into `go build` ldflags |
| `scripts/install.sh` | Call `clustr-serverd bundle install --from-release <version>` after binary is in place |
| `scripts/autodeploy/clustr-autodeploy.sh` | Run `clustr-serverd bundle install --from-release <embedded-version>` if version mismatch detected, before restarting `clustr-serverd` |
| `pkg/api/types.go` | Optional `BuiltinSlurmVersion` field on slurm config diagnostics endpoint |
| `docs/slurm-module.md` | Replace OpenHPC table in §2.1 with "clustr ships Slurm bundled — `dnf` pulls from your own clustr-server, no external repo URL" |
| `scripts/dev-vm/create.sh` | Drop OpenHPC reference if any (current grep shows none — verify in PR) |

The chi route ordering is load-bearing: `/repo/*` must be mounted before
`/api/v1` and must not be wrapped in `apiKeyAuth`. PR review checklist item.

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
      - generate manifest.json (versions, build_sha, container_digest, per-RPM SHA256, bundle SHA256)
      - tar czf clustr-slurm-bundle-vX.Y.Z-clustrR-el9-x86_64.tar.gz {bundle layout from §6.4}
      - record bundle SHA256 in manifest.json (this value is what the
        clustr-server binary embeds via ldflags)
      - cosign sign manifest.json (sprint 2)
      - gh release create slurm-vX.Y.Z-clustrR --notes-file ...
        attaching: bundle.tar.gz, individual RPMs, manifest.json,
        RPM-GPG-KEY-clustr, BUILD-LOG.txt
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

Goal: turnkey 2-node EL9 cluster boots on clustr-built Slurm RPMs that are
served by the clustr-server itself. No OpenHPC dependency, no GitHub network
hop at deploy time.

1. **Day 1**: land this design doc (DONE when this PR merges)
2. **Day 1-2** (Dinesh): create `build/slurm/versions.yml`,
   `build/slurm/keys/`, `.github/workflows/slurm-build.yml` (bundle output
   per §6.4)
3. **Day 2** (Gilfoyle): generate clustr GPG signing key, store in GitHub
   Actions secrets, commit public key
4. **Day 2-3** (Dinesh): land workflow, dry-run on a `slurm-v24.11.4-rc1`
   tag, verify RPMs build clean, are signed, and the bundle artifact is
   well-formed (`tar tzf | head`, `rpm -K`, `repodata/repomd.xml` valid)
5. **Day 3**: cut `slurm-v24.11.4-clustr1` tag, verify GitHub Release page
   has bundle + manifest + key + individual RPMs
6. **Day 3-4** (Dinesh): server-side serving + bundle install
   - Add `RepoDir` to `internal/config/config.go`
   - Mount `/repo/*` in `internal/server/server.go`
   - Embed pubkey via `//go:embed`
   - Write `cmd/clustr-serverd/bundle.go` (fetch + verify + atomic-swap)
   - Add `clustr-serverd bundle install` subcommand
   - Wire `Makefile` to inject `builtinSlurmBundleVersion` +
     `builtinSlurmBundleSHA256` into ldflags from `versions.yml`
7. **Day 4** (Dinesh): deploy-path wiring
   - `internal/slurm/manager.go` resolves `"clustr-builtin"` → `cfg.ServerURL + "/repo/<distro>-<arch>/"`
   - `internal/deploy/finalize.go::installSlurmInChroot` writes pubkey to
     chroot, flips `gpgcheck=1`, uses `file://` for `gpgkey`
   - Update `elVersionFromURL` to recognize `/repo/el9-x86_64/`
8. **Day 4** (Gilfoyle): autodeploy script integration
   - Update `scripts/autodeploy/clustr-autodeploy.sh` on the cloner host so
     post-rebuild it runs `clustr-serverd bundle install` if version mismatch
9. **Day 4-5** (Gilfoyle, lab-validate):
   - Wipe `/var/lib/clustr/repo/` on cloner host
   - `clustr-serverd bundle install` from a fresh start — verify download,
     SHA256 check, atomic swap
   - `curl http://192.168.1.151:8080/repo/el9-x86_64/repodata/repomd.xml`
     returns 200
   - Full deploy on vm201/vm202, confirm dnf hits clustr-server (not GitHub),
     confirm `srun hostname` works on both nodes
10. **Day 5**: update `docs/slurm-module.md` — remove OpenHPC table, document
    the bundled-server-repo model + the `bundle install` subcommand

**MVP exit criteria:**
- A clean `clustr-serverd` binary, after `bundle install`, exposes
  `/repo/el9-x86_64/repodata/repomd.xml` over HTTP
- A 2-node cluster deploys end-to-end with no `slurm_repo_url` set, with
  every Slurm RPM pulled from the clustr-server (verified by `tcpdump` /
  the deployed node's `dnf` log showing only the clustr-server IP, never
  github.com)
- `gpgcheck=1` is enforced and signature verification succeeds against the
  embedded clustr key
- `clustr-serverd bundle install` works from a local file (air-gap path)
- CI green on both `clustr` repo (`ci.yml`) and the `slurm-build.yml`
  workflow run for the cut tag

### Sprint 2 (production-shaped)

- EL10 x86_64 build target (bundle + repo subdir naturally extends)
- cosign signing of bundle + manifest + Rekor entry
- Scheduled upstream-watch job (auto-open issue)
- `RPM-GPG-KEY-clustr` rotation runbook
- Document the formal air-gap install procedure end-to-end (download bundle
  on a workstation with internet, transport via USB/scp, run
  `clustr-serverd bundle install /path/to/bundle.tar.gz` on the air-gapped
  clustr-server host)

### Sprint 3 (own the chain)

- Build MUNGE from source, mirror pipeline
- Optional: NVML build flavor for GPU clusters
- Optional: PMIx build flavor for MPI shops

### Sprint 4+ (only when a customer asks)

- Ubuntu DEB pipeline (clustr-server's `/repo/` would gain a `deb-jammy/`
  subtree alongside `el9-x86_64/` — same model, different package format)
- aarch64
- Multi-version concurrent hosting on a single clustr-server (only when
  there's a real "we need to span Slurm versions across the cluster"
  customer; layout already supports it)

---

## 12. Tradeoffs we are explicitly accepting

| Tradeoff | Why it's acceptable |
|---|---|
| We now own a CVE-watch process for Slurm | Schedule + scheduled workflow keeps us honest; SchedMD security mailing list is public |
| GitHub Releases is our build-artifact host — coupled to GitHub | Only the clustr-server installer touches GitHub, exactly once per server install. Deployed nodes never do. We are already coupled to GitHub for source + CI + container registry. One more dimension of the same risk, on a path that triggers ~once/year per cluster. |
| MVP is EL9-only — same matrix coverage as today | We were already EL9-only because OpenHPC EL10 doesn't exist. Net surface is unchanged. |
| clustr-server install needs internet (one-time bundle fetch) | Air-gap path is in MVP via `clustr-serverd bundle install <local-file>`. Operator side-loads the bundle from a workstation that does have internet. |
| clustr-server now serves arbitrary file content (the repo) over HTTP | `http.FileServer` with content under our exclusive write control + path traversal protection from stdlib. Same trust posture as the existing `/ui/*` route. |
| Plugin matrix is intentionally narrow (no CUDA, no PMIx, no IB) | Adds 4 build flavors and a CUDA toolkit to CI for zero current customers. Cost > benefit. |
| Per-deploy install adds ~30s vs. baked image | Negligible vs. existing kernel install + rsync time. Operationally simpler. |
| One Slurm version per clustr-server release (no concurrent multi-version) | Matches actual usage — a cluster runs one Slurm version. Layout supports adding multi-version later without rework. |

---

## 13. Reversibility classification

| Decision | Type | Why |
|---|---|---|
| Build from upstream tarball (vs. fork patch series) | **Reversible** | We can fork later if we ever need to carry patches |
| RPM packaging from in-tarball spec | **Reversible** | Switch to debs/tarball later without breaking RPM consumers |
| GitHub Releases as the build-artifact sink | **Reversible** | Could move to ghcr.io OCI artifacts or our own object store; the bundle layout is the contract, not the URL |
| clustr-server hosts the repo (not Caddy, not external CDN) | **Reversible** | `http.FileServer` line is removable; `/var/lib/clustr/repo/` could be served by Caddy without changing on-disk layout |
| Single bundle artifact (vs. baked-into-server vs. per-RPM fetch) | **Reversible** | Bundle layout is internal; could split or merge without changing the deploy contract |
| Per-deploy install (vs. bake) | **Reversible** | Adding bake-mode is additive, no schema change |
| EL9 x86_64 first | **Reversible** | Add matrix entries, no architectural rework |
| One Slurm version per clustr-server release | **Reversible** | Layout already supports `/repo/el9-x86_64-24.05/` siblings |
| **clustr GPG signing key identity** | **Irreversible-ish** | Key rotation is supported but every consumer that ever trusted the old key keeps it forever. Treat with care. |
| **`SlurmRepoURL = "clustr-builtin"` sentinel value in DB** | **Irreversible** | Ships as part of API + data model. Renaming requires a migration. Get the name right in PR review. |
| **`/repo/<distro>-<arch>/` URL path served by clustr-server** | **Irreversible** | Once a deployed node has this URL in `/etc/yum.repos.d/`, changing the path breaks `dnf update` on every previously-deployed node. Get this right at MVP. |
| **`/var/lib/clustr/repo/` as the on-disk repo location** | **Reversible** | Operators don't depend on this path; configurable via `CLUSTR_REPO_DIR` |
| Bundling `builtinSlurmVersion` + `builtinSlurmBundleVersion` into the server binary | **Reversible** | Could move to a config file later |
| Embedding the clustr pubkey in the server binary | **Reversible** | Could fetch from disk; embedding is a deploy-simplicity choice |

The only thing in this design that is genuinely hard to back out of is the
GPG key identity and the sentinel string in the DB. Both warrant careful PR
review.

### Kill criteria

This whole approach should be revisited if any of these become true:

- We ship 5+ build flavors (CUDA, PMIx, IB, etc.) — at that point the bundle
  size justifies splitting into per-flavor bundles or hosting an OCI registry
  instead of a single tarball
- A customer needs day-zero CVE patching — we may need to commit to faster
  release cadence than "tag, build, release" allows
- GitHub Releases changes their ToS to disallow binary distribution — flip
  the build-artifact sink to OCI artifacts on ghcr.io. The clustr-server
  bundle-install path adapts (different fetch URL); the deployed-node side
  is unaffected because nodes don't talk to GitHub.
- A customer's clustr-server host genuinely cannot reach GitHub even at
  install time and the bundle is too large to side-load conveniently —
  invest in a SchedMD-style "anonymous mirror" worker that can run on a
  trusted intermediate host
- The clustr-server's HTTP `/repo/*` traffic becomes a meaningful fraction
  of its load (large clusters, many concurrent dnf updates) — front it with
  Caddy or move to a sidecar; on-disk layout is already in the right place

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
5. **Repo URL path**: `/repo/el9-x86_64/` vs. `/slurm/repo/el9-x86_64/` vs.
   `/packages/el9-x86_64/`. The `/repo/` prefix is short, generic enough to
   accommodate non-Slurm packages later (MUNGE, future modules), and
   doesn't collide with anything in the current chi router. Recommendation:
   `/repo/`. **This is irreversible once shipped — locks in for every
   deployed node.**
6. **Server install fetch authentication**: clustr-serverd fetches the
   bundle from a GitHub Release on a public repo at install time. Should
   we authenticate the fetch (gh API token) or use the unauthenticated
   public-asset URL? Recommendation: unauthenticated public URL — bundle
   is verified by SHA256 + RPMs are GPG-signed, so the fetch transport is
   not a trust boundary. Simpler.
7. **`/var/lib/clustr/repo/` permissions**: world-readable (containers,
   non-root processes can serve it directly) or root-only (clustr-serverd
   reads as root, serves via FileServer)? Recommendation: 0755, root-owned,
   files 0644. Standard yum repo permissions; lets future Caddy fronting
   work without a permission dance.

---

## 15. Implementation handoff (for Dinesh)

The work splits into four landable PRs. Land in order — each one is reviewable
on its own and unblocks the next. Suggested order:

### PR 1 — Build pipeline bootstrap (no clustr-server code change)

- Create `build/slurm/versions.yml` with Slurm 24.11.4 pinned (sha256, schedmd
  key ID, `clustr_release: 1`)
- Commit SchedMD's release pubkey at `build/slurm/keys/schedmd-release.asc`
  (download from https://www.schedmd.com/security_pubkey.php and verify
  out-of-band before committing)
- Generate the clustr signing key offline (Gilfoyle owns this step):
  `gpg --quick-gen-key "clustr Release Key (slurm) <robert.romero@sqoia.dev>" rsa4096 sign 2y`
  Export public to `build/slurm/keys/clustr-release.asc.pub`. Export private
  to a file, base64-encode, store as `CLUSTR_RPM_SIGNING_KEY` GitHub Actions
  secret. Set passphrase as `CLUSTR_RPM_SIGNING_PASSPHRASE`.

### PR 2 — `slurm-build.yml` workflow producing the bundle

- Write `.github/workflows/slurm-build.yml` per §9.1
- The build job must produce, in addition to individual signed RPMs:
  - A `repodata/` directory generated by `createrepo_c` at build time
  - A bundle tarball matching the layout in §6.4:
    `clustr-slurm-bundle-vX.Y.Z-clustrR-el9-x86_64.tar.gz`
  - A `manifest.json` that includes `bundle_sha256` (this exact value will
    be embedded into the clustr-serverd binary in PR 4 for verification)
- First implement only the tag-triggered path; defer schedule +
  workflow_dispatch to a follow-up
- Test with a `slurm-v24.11.4-clustr0` "release candidate" tag — verify
  output: `tar tzf clustr-slurm-bundle-*.tar.gz` shows the layout from §6.4,
  `rpm -K` against the public key passes, `repodata/repomd.xml` is valid
  XML. Delete the tag + release after verification.

### PR 3 — clustr-server: bundle install + `/repo/` serving

This PR is the operational core of the new model. Land it before PR 4 so
that operators can manually point a node at the new repo URL for testing
even before the manager defaults change.

- `internal/config/config.go`: add `RepoDir string` (default
  `/var/lib/clustr/repo`, env `CLUSTR_REPO_DIR`)
- `internal/server/keys.go` (new): `//go:embed
  build/slurm/keys/clustr-release.asc.pub` + `func ClustrReleasePubkey() []byte`
- `internal/server/server.go`: mount `/repo/*` route immediately after the
  existing `/ui/*` mount (line ~748). Public, no auth. Use
  `http.StripPrefix("/repo", http.FileServer(http.Dir(s.cfg.RepoDir)))`.
  Add a small middleware that sets `Cache-Control: public, max-age=300` for
  paths matching `/repo/.+/repodata/.+` and `max-age=86400` for `*.rpm`.
- `cmd/clustr-serverd/bundle.go` (new): implement
  - `bundleInstallFromURL(ctx, url, expectedSHA256, destDir)` —
    download → SHA256-verify → tar-extract to staging → atomic-rename
  - `bundleInstallFromFile(ctx, path, expectedSHA256, destDir)` — same,
    skipping the download
  - Both write `<destDir>/RPM-GPG-KEY-clustr` from the embedded pubkey
    (overwrites the bundle's own copy — embedded is source of truth)
  - Both write `<destDir>/.installed-version` containing the bundle version
    string for idempotency
- `cmd/clustr-serverd/main.go`: add `bundle install [--from-release | --from-file <path>]`
  subcommand. New `-ldflags` for `builtinSlurmBundleVersion`,
  `builtinSlurmBundleSHA256`, `builtinSlurmVersion`.
- Top-level `Makefile`: read `build/slurm/versions.yml` (use `yq` or a
  small awk fallback) and inject ldflags into `go build`.
- `scripts/install.sh`: after binary placement, call
  `clustr-serverd bundle install --from-release` (assumes outbound HTTPS).
  If it fails, print a clear "side-load with `bundle install --from-file`"
  message with the exact filename and SHA256.

Verify in PR 3:
- Fresh `clustr-serverd bundle install --from-release` against a real
  GitHub Release (the PR-2 RC tag), then `curl
  http://127.0.0.1:8080/repo/el9-x86_64/repodata/repomd.xml` returns 200
- `--from-file` works against a local `.tar.gz` (air-gap path)
- Re-running install with the same version is a no-op (idempotent)

### PR 4 — Wire the deploy path to the bundled-server-repo

- `internal/slurm/manager.go`: resolve `"clustr-builtin"` (and empty string)
  to `cfg.ServerURL + "/repo/el${EL}-${ARCH}/"`. Keep arbitrary URL strings
  as the escape hatch.
- `internal/deploy/finalize.go::installSlurmInChroot`:
  - Write the embedded pubkey into the chroot at
    `/etc/pki/rpm-gpg/RPM-GPG-KEY-clustr` (use the same accessor from
    `internal/server/keys.go`)
  - Flip `gpgcheck=0` → `gpgcheck=1` in the rendered `.repo` template
  - Use `gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr` (file://, not
    http:// — the key is already on-disk in the chroot)
- `internal/deploy/finalize.go::elVersionFromURL`: extend to recognize
  `/repo/el9-x86_64/` and `/repo/el10-x86_64/` URL forms (alongside the
  existing OpenHPC patterns we keep for the override case)
- `docs/slurm-module.md`: replace the OpenHPC §2.1 table with the
  bundled-server-repo description; document the `bundle install` subcommand

### PR 5 (Gilfoyle) — Autodeploy + lab validation

- Update `scripts/autodeploy/clustr-autodeploy.sh` on the cloner host so
  post-rebuild it runs `clustr-serverd bundle install --from-release` when
  the embedded `builtinSlurmBundleVersion` differs from
  `/var/lib/clustr/repo/.installed-version`. Re-run install of the
  autodeploy unit per the standing pattern.
- Wipe `/var/lib/clustr/repo/` on cloner host, push a clustr-server commit
  that bumps the bundle version, observe autodeploy fetch + swap, then
  start the deploy
- Full vm201 + vm202 deploy on cloner host
- Verification (must all pass):
  - `tcpdump -i mgmt0 'host github.com'` on a deployed node during `dnf
    install` shows zero packets to GitHub
  - Deployed node's `/var/log/dnf.log` shows the clustr-server's IP for all
    Slurm RPM downloads
  - `srun hostname` returns from both nodes
  - `rpm -qi slurm | grep Signature` shows the clustr key

### Things explicitly NOT in this handoff (do not start without explicit go)

- MUNGE from-source pipeline
- EL10 build target
- cosign / Rekor integration
- Scheduled upstream-watch
- Image-bake mode
- Multi-version concurrent repo hosting
- Caddy fronting for `/repo/*` (stdlib is the MVP choice)

### CI watch (standing rule reminder)

Watch CI on every push per the standing rule in CLAUDE.md. Do not declare
the MVP done until **all** of these are green on the same SHA:
- `clustr` repo `ci.yml` (build + test)
- `slurm-build.yml` workflow on the cut `slurm-v24.11.4-clustr1` tag
- The lab validation in PR 5

---

## 16. PR5 lab validation failures and PR6 fixes

The first end-to-end lab run (`docs/lab-validation-pr5.md`, bundle
`v24.11.4-clustr1`) exposed two build-pipeline gaps that required a
`clustr2` rebuild. This section documents the root causes and the fixes
shipped in clustr2.

### Failure A — `libjwt.so.1` not in bundle (EPEL required at deploy time)

**Symptom:** Manual Slurm install post-boot required `dnf install epel-release`
before `dnf install slurm` would resolve. Gilfoyle's tcpdump test showed zero
external traffic during the automated reimage, but the manual EPEL install
added a network dep that would be triggered by any fresh deploy.

**Root cause:** The base `slurm` RPM (and `slurm-slurmctld`, `slurm-slurmd`)
link against `libjwt.so.1` for JWT-based auth token verification in the Slurm
daemons.  `libjwt` is not in Rocky 9 BaseOS or AppStream — it lives only in
EPEL.  The clustr1 bundle contained only Slurm RPMs; no libjwt RPM was
included.

Additionally, `slurmrestd` was silently **disabled** in the clustr1 build
because `http-parser-devel` (required by slurmrestd's configure check) was
listed as "optional" (install with `--skip-unavailable`). EPEL provides
`http-parser-devel`, but the build ran the optional install after enabling EPEL
and it apparently resolved. However, the configure check for http-parser
reported "not found" (`checking whether to compile slurmrestd... no` at line
3468 of the BUILD-LOG.txt), which means `http-parser-devel` was not actually
installed successfully despite EPEL being enabled. This caused `slurmrestd` to
be absent from the RPM set.

**Fix (clustr2):**
1. Build `libjwt` 1.18.3 from upstream source (same download → SHA256-verify →
   rpmbuild → GPG-sign pattern as Slurm). Include the runtime RPM
   (`libjwt-1.18.3.el9.x86_64.rpm`) in the bundle alongside the Slurm RPMs.
   `libjwt-devel` and `libjwt-devel-docs` are build-time artifacts only and
   are not included in the bundle.
2. Move `http-parser-devel` from the optional install to the mandatory install
   list so a missing `http-parser-devel` causes a hard build failure rather
   than silently disabling slurmrestd.
3. Also add `jansson-devel` to mandatory deps (required by libjwt's configure).

**Why 1.x series (not 2.x/3.x)?** The Slurm 24.11.4 binary links against
`libjwt.so.1`, which is the SO name for the 1.x ABI. The 2.x/3.x releases
renamed the library (different SO version). Using 1.18.3 matches the exact
ABI that the Slurm build expects.

### Failure B — `slurmscriptd` described as "missing binary" (misdiagnosis)

**Symptom reported in PR5:** The report described "slurmscriptd binary missing
from slurm-slurmctld RPM" and noted that `systemctl start slurmctld` crashed.

**Root cause (corrected):** `slurmscriptd` is **not** a standalone binary in
Slurm 24.x. It is compiled as `.o` files and linked directly into the
`slurmctld` binary (`src/slurmctld/Makefile.am` declares `sbin_PROGRAMS =
slurmctld`, not a separate `slurmscriptd` target). When `slurmctld` starts, it
fork+execs `/proc/self/exe` (itself) with the `SLURMSCRIPTD_MODE` environment
variable set to run as the scriptd subprocess. The upstream spec's `%files
slurmctld` section correctly lists only `/usr/sbin/slurmctld` and the systemd
unit.

The actual `systemctl start slurmctld` failure in PR5 was caused by
`User=slurm` in the slurmctld.service unit — the `slurm` system user was
not being created during the manual post-boot install because the RPM
pre-install scriptlet ran in a non-chroot context, or the base image's
existing OpenHPC `slurm` user had conflicting UID/GID. The workaround of
invoking `/usr/sbin/slurmctld` directly as root bypassed the systemd `User=`
constraint, confirming the binary itself is present and functional.

**Fix (clustr2):** Add a binary-presence verification step in
`slurm-build.yml` that hard-fails the build if any expected binary is missing
from its expected RPM. The verified binaries are:
`slurmctld`, `slurmd`, `slurmdbd`, `slurmrestd`, `sackd`.
`slurmscriptd` is intentionally **not** in this list because it is not a
standalone binary — it is the `slurmctld` binary itself.

The systemd unit `User=slurm` issue is addressed in the separate
"base image OpenHPC cleanup" PR (tracked in `docs/lab-validation-pr5.md §F`).

### What libjwt bundling enables

With libjwt in the bundle, the deployed node's `dnf install` chain becomes:

```
[clustr-slurm] repo (served by clustr-server) provides:
  slurm-24.11.4-1.el9.x86_64.rpm        → Requires: libjwt.so.1
  slurm-slurmctld-24.11.4-1.el9.x86_64.rpm
  slurm-slurmrestd-24.11.4-1.el9.x86_64.rpm  (now present, was missing in clustr1)
  libjwt-1.18.3.el9.x86_64.rpm          → Provides: libjwt.so.1
  ...
```

`dnf install slurm slurm-slurmctld slurm-slurmd` resolves all deps from
the clustr repo alone. No EPEL fetch at deploy time. Zero-egress claim holds.
