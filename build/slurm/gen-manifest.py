#!/usr/bin/env python3
"""
gen-manifest.py — Generate bundle/manifest.json for a Slurm RPM bundle.

Called by .github/workflows/slurm-build.yml.  All inputs come from
environment variables so the caller can export them without shell escaping
concerns.

Environment variables (all required):
  TARGET_DIR       — directory containing .rpm files, e.g. bundle/el9-x86_64
  SLURM_VERSION    — upstream Slurm version string, e.g. 24.11.4
  CLUSTR_RELEASE   — integer release counter, e.g. 1
  DISTRO           — OS family, e.g. el9
  ARCH             — CPU arch, e.g. x86_64
  BUILT_AT         — ISO8601 UTC timestamp, e.g. 2026-04-27T14:00:00Z
  BUILD_SHA        — git commit SHA (GITHUB_SHA)
  OUTPUT_FILE      — path to write manifest.json (default: bundle/manifest.json)

The bundle_sha256 field is initially set to "__PENDING__".  The caller
updates it after computing the bundle tarball SHA256.

Schema version 2 changes (clustr2):
  - "rpms" list now includes libjwt RPM alongside Slurm RPMs
  - "bundled_deps" top-level key lists non-Slurm packages bundled for
    zero-egress deploy (currently: libjwt)
"""

import hashlib
import json
import os
import sys


def get_env(name):
    val = os.environ.get(name)
    if not val:
        print(f"ERROR: environment variable {name} is not set", file=sys.stderr)
        sys.exit(1)
    return val


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


target_dir = get_env("TARGET_DIR")
slurm_version = get_env("SLURM_VERSION")
clustr_release = int(get_env("CLUSTR_RELEASE"))
distro = get_env("DISTRO")
arch = get_env("ARCH")
built_at = get_env("BUILT_AT")
build_sha = get_env("BUILD_SHA")
output_file = os.environ.get("OUTPUT_FILE", "bundle/manifest.json")

rpms = []
bundled_deps = []
for fname in sorted(os.listdir(target_dir)):
    if not fname.endswith(".rpm"):
        continue
    fpath = os.path.join(target_dir, fname)
    entry = {
        "name": fname,
        "sha256": sha256_file(fpath),
        "size": os.path.getsize(fpath),
    }
    # Separate Slurm RPMs from bundled dependency RPMs for transparency.
    # Slurm RPMs all start with "slurm-" per upstream naming convention.
    if fname.startswith("slurm-") or fname.startswith("slurm-"):
        rpms.append(entry)
    else:
        # Non-Slurm packages (e.g. libjwt) are bundled deps.
        bundled_deps.append(entry)

manifest = {
    "schema_version": 2,
    "slurm_version": slurm_version,
    "clustr_release": clustr_release,
    "distro": distro,
    "arch": arch,
    "built_at": built_at,
    "build_sha": build_sha,
    "rpms": rpms,
    "bundled_deps": bundled_deps,
    "signing_key_id": "41E51A6653BBA540",
    "signing_key_fingerprint": "9EDB9E63AB84551E25C1416841E51A6653BBA540",
    "bundle_sha256": "__PENDING__",
}

with open(output_file, "w") as f:
    json.dump(manifest, f, indent=2)

print(f"manifest.json written with {len(rpms)} RPM entries to {output_file}")
