# Intel BIOS Settings — Operator Setup Guide

**Feature:** clustr BIOS push (#159)
**Applies to:** Intel Xeon servers managed by clustr using the Intel SYSCFG utility

---

## Why operator-supplied binary

Intel SYSCFG is distributed under the **Intel End User License Agreement For
Developer Tools**, which prohibits redistribution as part of a third-party
product.  clustr is open-source (MIT) and cannot bundle the Intel binary in its
RPM or in the deploy initramfs without violating Intel's EULA.

The operator must place the binary at a well-known path on the clustr server.
The build-initramfs script detects the binary and bundles it for deploy.

---

## Download

1. Log in at [Intel Registration Center](https://registrationcenter.intel.com/).
2. Download **Save and Restore System Configuration Utility (SYSCFG) for Linux**
   (search "SYSCFG" on the Intel download center).
3. Extract the zip.  The binary of interest is named `syscfg` (not `syscfg.efi`).

Version tested with clustr: SYSCFG 14.x.  Earlier versions may emit different
setting names — if in doubt, run `syscfg /d` on a representative node to list
available setting names before creating profiles.

---

## Place the binary

```bash
# On the clustr server, as root:
install -m 755 /path/to/syscfg /var/lib/clustr/vendor-bios/intel/syscfg
```

The directory `/var/lib/clustr/vendor-bios/intel/` is automatically created by
clustr-serverd during startup.  Mode 0700, owner `clustr`.

Verify placement:

```bash
clustr-cli bios provider verify intel
# or via the API:
curl -H "Authorization: Bearer <key>" https://<server>/api/v1/bios/providers/intel/verify
```

Expected response when binary is present:

```json
{"vendor":"intel","available":true,"bin_path":"/var/lib/clustr/vendor-bios/intel/syscfg"}
```

---

## SYSCFG settings format

The `settings_json` field in a BIOS profile is a flat JSON object whose keys
are SYSCFG setting names exactly as `syscfg /d` reports them:

```json
{
  "Intel(R) Hyper-Threading Technology": "Disable",
  "Power Performance Tuning":             "OS Controls EPB",
  "Energy Efficient Turbo":               "Disable",
  "PCIe ASPM Support":                    "Disable"
}
```

**Important notes:**
- Setting names are case-insensitive at diff time but use the casing `syscfg /d`
  emits for the apply file.  Paste names directly from `syscfg /d` output.
- Values are opaque strings; allowed values vary by setting and firmware version.
  Run `syscfg /d` on a representative node to see available options per setting.
- clustr applies settings as a **partial override** — settings present in the
  current BIOS but absent from the profile are left unchanged.

---

## Create a profile

Via CLI:

```bash
clustr bios profiles create \
  --name hpc-default \
  --vendor intel \
  --settings '{"Intel(R) Hyper-Threading Technology":"Disable","Energy Efficient Turbo":"Disable"}' \
  --description "HPC defaults: HT off, turbo off"
```

Via API:

```bash
curl -X POST https://<server>/api/v1/bios-profiles \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "hpc-default",
    "vendor": "intel",
    "settings_json": "{\"Intel(R) Hyper-Threading Technology\":\"Disable\"}",
    "description": "HPC defaults"
  }'
```

---

## Assign a profile to a node

```bash
clustr bios assign <node-id> hpc-default
```

Or via API:

```bash
curl -X PUT https://<server>/api/v1/nodes/<node-id>/bios-profile \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"profile_id":"<profile-uuid>"}'
```

The profile is applied on the **next deploy** of the node (full reimage or BIOS-only deploy).

---

## Apply without reimaging

To push BIOS settings without reimaging the disk:

```bash
clustr bios apply <node-selector>
```

This triggers a BIOS-only deploy: the node PXE-boots into initramfs, applies
BIOS settings, and reboots to disk without touching the OS image.

---

## Initramfs bundling

When `scripts/build-initramfs.sh` runs, it checks for the Intel binary:

```
bios: intel binary present, bundling /var/lib/clustr/vendor-bios/intel/syscfg → initramfs:/usr/local/bin/intel-syscfg
```

If absent:

```
bios: intel binary absent, intel BIOS apply will fail on nodes with Intel profiles (see docs/BIOS-INTEL-SETUP.md)
```

The initramfs build does NOT fail when the binary is absent — it produces a
valid initramfs that will fail BIOS apply (and log the operator runbook URL) at
deploy time.  Nodes without an Intel profile assigned are unaffected.

---

## Drift detection

After each deploy, clustr-clientd (the node agent) reads current BIOS settings
every 24 hours using `syscfg /s`, computes a hash, and compares it to
`applied_settings_hash` from the last apply.  If they differ, a drift event is
recorded in the alert engine.

Drift detection is **read-only** — clustr never auto-corrects BIOS drift.
The operator must trigger a re-apply explicitly via `clustr bios apply`.

---

## Future vendors

Dell `racadm` and Supermicro `sum` follow the same operator supply chain:

| Vendor | Expected path |
|---|---|
| Intel | `/var/lib/clustr/vendor-bios/intel/syscfg` |
| Dell | `/var/lib/clustr/vendor-bios/dell/racadm` |
| Supermicro | `/var/lib/clustr/vendor-bios/supermicro/sum` |

Each vendor's implementation is a single file in `internal/bios/<vendor>/`.
No changes to the interface, no changes to the deploy flow.
