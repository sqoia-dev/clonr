#!/usr/bin/env python3
"""
gen-manifest.py — Generate bundle/manifest.json for a Slurm RPM bundle.

Called by .github/workflows/slurm-build.yml.  All inputs come from
environment variables so the caller can export them without shell escaping
concerns.

Environment variables (all required unless noted):
  TARGET_DIR       — directory containing clustr-built .rpm files, e.g. bundle/el9-x86_64
  SLURM_VERSION    — upstream Slurm version string, e.g. 24.11.4
  CLUSTR_RELEASE   — integer release counter, e.g. 5
  DISTRO           — OS family, e.g. el9
  ARCH             — CPU arch, e.g. x86_64
  BUILT_AT         — ISO8601 UTC timestamp, e.g. 2026-04-27T14:00:00Z
  BUILD_SHA        — git commit SHA (GITHUB_SHA)
  OUTPUT_FILE      — path to write manifest.json (default: bundle/manifest.json)

Optional environment variables (schema v3, GAP-17):
  DEPS_DIR         — directory containing passthrough dep .rpm files (e.g. bundle/el9-x86_64-deps)
                     If set and the directory exists, a separate "dep_rpms" list is emitted
                     and has_deps_subdir is set to true.
  HAS_DEPS_SUBDIR  — set to "true" to force has_deps_subdir: true even if DEPS_DIR is not set.

The bundle_sha256 field is initially set to "__PENDING__".  The caller
updates it after computing the bundle tarball SHA256.

Schema version history:
  v1: initial (slurm RPMs only)
  v2: adds libjwt RPM to rpms list; adds bundled_deps key
  v3 (clustr5, GAP-17): splits into rpms (clustr-built) and dep_rpms (Rocky/EPEL
     passthrough); adds has_deps_subdir flag; bundled_deps removed in favor of dep_rpms.
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


def list_rpms(directory):
    """Return a sorted list of RPM entries (name, sha256, size) from a directory."""
    entries = []
    try:
        fnames = sorted(os.listdir(directory))
    except FileNotFoundError:
        return entries
    for fname in fnames:
        if not fname.endswith(".rpm"):
            continue
        fpath = os.path.join(directory, fname)
        entries.append({
            "name": fname,
            "sha256": sha256_file(fpath),
            "size": os.path.getsize(fpath),
        })
    return entries


target_dir = get_env("TARGET_DIR")
slurm_version = get_env("SLURM_VERSION")
clustr_release = int(get_env("CLUSTR_RELEASE"))
distro = get_env("DISTRO")
arch = get_env("ARCH")
built_at = get_env("BUILT_AT")
build_sha = get_env("BUILD_SHA")
output_file = os.environ.get("OUTPUT_FILE", "bundle/manifest.json")

deps_dir = os.environ.get("DEPS_DIR", "")
has_deps_subdir_env = os.environ.get("HAS_DEPS_SUBDIR", "").lower() == "true"

# Collect RPMs from the primary (clustr-built) subdir.
# Slurm RPMs start with "slurm-"; libjwt starts with "libjwt-".
# Both are clustr-signed.
rpms = list_rpms(target_dir)

# Collect dep RPMs from the deps subdir (Rocky/EPEL passthrough, schema v3).
dep_rpms = []
has_deps_subdir = False
if deps_dir and os.path.isdir(deps_dir):
    dep_rpms = list_rpms(deps_dir)
    has_deps_subdir = True
elif has_deps_subdir_env:
    has_deps_subdir = True

manifest = {
    "schema_version": 3,
    "slurm_version": slurm_version,
    "clustr_release": clustr_release,
    "distro": distro,
    "arch": arch,
    "built_at": built_at,
    "build_sha": build_sha,
    "has_deps_subdir": has_deps_subdir,
    "rpms": rpms,          # clustr-built RPMs (slurm-*, libjwt-*)
    "dep_rpms": dep_rpms,  # Rocky/EPEL passthrough RPMs
    "signing_key_id": "41E51A6653BBA540",
    "signing_key_fingerprint": "9EDB9E63AB84551E25C1416841E51A6653BBA540",
    "dep_signing_keys": {
        "rocky9": "21CB256AE16FC54C6E652949702D426D350D275D",
        "epel9":  "FF8AD1344597106ECE813B918A3872BF3228467C",
    },
    "bundle_sha256": "__PENDING__",
}

with open(output_file, "w") as f:
    json.dump(manifest, f, indent=2)

print(f"manifest.json (schema v3) written:")
print(f"  {len(rpms)} clustr-built RPM entries")
print(f"  {len(dep_rpms)} dep RPM entries")
print(f"  has_deps_subdir: {has_deps_subdir}")
print(f"  output: {output_file}")
