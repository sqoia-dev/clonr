#!/usr/bin/env python3
"""
gen-release-notes.py — Generate GitHub Release notes for a Slurm bundle release.

Called by .github/workflows/slurm-build.yml.

Environment variables (all required):
  TAG            — full release tag, e.g. slurm-v24.11.4-clustr1
  SLURM_VERSION  — upstream Slurm version, e.g. 24.11.4
  CLUSTR_N       — clustr release number, e.g. 1
  BUNDLE_SHA256  — SHA256 of the bundle tarball
  BUNDLE_NAME    — filename of the bundle tarball
  OUTPUT_FILE    — path to write release notes (default: /tmp/release-notes.md)
"""

import os
import sys


def get_env(name):
    val = os.environ.get(name)
    if not val:
        print(f"ERROR: environment variable {name} is not set", file=sys.stderr)
        sys.exit(1)
    return val


tag = get_env("TAG")
ver = get_env("SLURM_VERSION")
cn = get_env("CLUSTR_N")
bsha = get_env("BUNDLE_SHA256")
bname = get_env("BUNDLE_NAME")
output_file = os.environ.get("OUTPUT_FILE", "/tmp/release-notes.md")

notes = f"""## Slurm {ver} - clustr release {cn}

Built from the upstream SchedMD tarball. All RPMs signed with the clustr Release Signing Key.

### Signing key
- Key ID: `41E51A6653BBA540`
- Fingerprint: `9EDB9E63AB84551E25C1416841E51A6653BBA540`
- Public key: `RPM-GPG-KEY-clustr` (attached to this release)

### Bundle SHA256 (for PR3 ldflags)
```
{bsha}  {bname}
```

### Install via clustr-server (PR3)
```
clustr-serverd bundle install --from-release {tag}
```

### Verify RPM signatures manually
```bash
rpm --import RPM-GPG-KEY-clustr
rpm -K slurm-*.rpm
```

### Assets
- `{bname}` - canonical install artifact (signed RPMs + repodata + key + manifest)
- `{bname}.sha256` - bundle SHA256 sidecar
- `manifest.json` - build provenance, per-RPM SHA256s, bundle SHA256
- `RPM-GPG-KEY-clustr` - clustr release signing public key
- `slurm-*.rpm` - individual signed RPMs (for rpm -K spot-check)
- `BUILD-LOG.txt` - full rpmbuild stdout/stderr
"""

with open(output_file, "w") as f:
    f.write(notes)

print(f"Release notes written to {output_file}")
