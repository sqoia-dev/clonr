# clustr-serverd: Bundled Slurm Repository

clustr-serverd ships a bundled Slurm RPM repository that it serves directly
to deployed nodes.  Deployed nodes run `dnf install slurm ...` against the
clustr-server — they never reach GitHub or schedmd.com at deploy time.

For the full architectural rationale see `docs/slurm-build-pipeline.md`.

---

## 1. Where the repo lives

The repo directory defaults to `/var/lib/clustr/repo/` (configurable via
`CLUSTR_REPO_DIR` env var or `repo_dir` YAML key).

After a bundle is installed the layout is:

```
/var/lib/clustr/repo/
├── RPM-GPG-KEY-clustr                  # embedded GPG public key
└── el9-x86_64/
    ├── .installed-version              # JSON metadata (see §3)
    ├── slurm-24.11.4-1.el9.x86_64.rpm
    ├── slurm-slurmctld-24.11.4-...rpm
    ├── ...
    └── repodata/
        ├── repomd.xml
        ├── repomd.xml.asc
        ├── primary.xml.gz
        └── ...
```

The `repodata/` tree is pre-generated at build time (by `createrepo_c` in
the CI pipeline) — `createrepo_c` is not required on the clustr-server host.

---

## 2. HTTP surface

All paths under `/repo/*` are served publicly (no API key required) from the
repo directory.  `http.FileServer` handles Range requests and ETags.

| URL | Content |
|---|---|
| `GET /repo/el9-x86_64/repodata/repomd.xml` | Repo metadata XML |
| `GET /repo/el9-x86_64/<package>.rpm` | Signed RPM |
| `GET /repo/RPM-GPG-KEY-clustr` | GPG public key |
| `GET /repo/health` | JSON status (see §4) |

Cache-Control headers:
- `repodata/*` — `public, max-age=300` (5 min; safe to recheck)
- `*.rpm` — `public, max-age=86400, immutable` (content-addressed by version)

---

## 3. Installing and managing bundles

### Install from GitHub Release (default)

```bash
# Install the version compiled into this binary (default):
clustr-serverd bundle install

# Install a specific release tag:
clustr-serverd bundle install --from-release slurm-v24.11.4-clustr1
```

The command downloads `clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz`
from the GitHub Release, verifies its SHA256, verifies RPM signatures against
the embedded clustr GPG key, then atomically swaps it into place.

### Air-gap / offline install

```bash
# Side-load from a file you transferred manually:
clustr-serverd bundle install --from-file /path/to/clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz
```

The SHA256 is read from a sibling `.sha256` file.  Pass `--sha256 <hex>` to
override.

### List installed bundles

```bash
clustr-serverd bundle list
```

Output:

```
DISTRO-ARCH    SLURM VERSION  CLUSTR RELEASE  INSTALLED AT               SHA256 (short)
el9-x86_64     24.11.4        1               2026-04-27T12:00:00Z       d5e397e19bb4...
```

### Rollback

```bash
clustr-serverd bundle install --rollback
```

Swaps the most recent `.previous-*` backup directory back into place.
One previous bundle is kept after each install.

### Idempotency

Re-running `bundle install` with the same bundle SHA256 is a no-op:

```
Bundle v24.11.4-clustr1 already installed (idempotent — nothing to do)
```

---

## 4. /repo/health endpoint

```
GET /repo/health
```

Returns JSON with all installed bundles:

```json
{
  "installed": [
    {
      "distro": "el9",
      "arch": "x86_64",
      "slurm_version": "24.11.4",
      "clustr_release": "1",
      "installed_at": "2026-04-27T12:00:00Z",
      "bundle_sha256": "d5e397e19bb407b380eacfc03185ab8e1a705365eb598c0e042f80d19a91d9d6"
    }
  ]
}
```

Returns `{"installed":[]}` if no bundle is installed yet.

---

## 5. Manifest schema

`manifest.json` inside the bundle (also available at the GitHub Release):

```json
{
  "slurm_version": "24.11.4",
  "clustr_release": 1,
  "distro": "el9",
  "arch": "x86_64",
  "build_sha": "<git sha of the clustr repo at build time>",
  "build_timestamp": "2026-04-27T12:00:00Z",
  "bundle_sha256": "d5e397e19bb407b380eacfc03185ab8e1a705365eb598c0e042f80d19a91d9d6",
  "sha256_per_rpm": {
    "slurm-24.11.4-1.el9.x86_64.rpm": "..."
  }
}
```

---

## 6. GPG key

The clustr release signing public key is embedded in the `clustr-serverd`
binary at compile time from `build/slurm/keys/clustr-release.asc.pub`.

Fingerprint: `9EDB9E63AB84551E25C1416841E51A6653BBA540`

On every `bundle install` the embedded key is written to
`<repoDir>/RPM-GPG-KEY-clustr` (overwriting any copy from the bundle itself).
The embedded key is the source of truth.

---

## 7. Build-time constants

The Makefile reads `build/slurm/versions.yml` and injects three ldflags into
the server binary:

| ldflag | Purpose |
|---|---|
| `builtinSlurmVersion` | Slurm version (e.g. `24.11.4`) |
| `builtinSlurmBundleVersion` | Full bundle tag (e.g. `v24.11.4-clustr1`) |
| `builtinSlurmBundleSHA256` | SHA256 of the bundle tarball |

When `clustr-serverd bundle install` is called with no flags it uses these
compiled-in values, so a fresh install just works.
