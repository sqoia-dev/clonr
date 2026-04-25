# ISO Environment Group Detection

## Problem Statement

`kickstart.go` line 174 hardcodes `@^minimal-environment` in every RHEL-family
kickstart. This works for Rocky Linux ISOs that ship that group, but breaks or
produces surprising results when:

- An admin builds from a DVD ISO that ships additional environments
  (`@^server-product-environment`, `@^workstation-product-environment`, etc.)
  and wants one of those instead.
- The minimal ISO (`Rocky-10.1-x86_64-minimal.iso`) ships only
  `@^minimal-environment`, making the hardcode accidentally correct but for the
  wrong reason — no feedback, no validation.
- A future distro or point release renames the group ID.

The goal is to detect available environment groups from the ISO's comps XML,
expose them via API, and let the admin pick one before the build starts — with
`minimal-environment` as the backward-compatible default.

---

## Architecture Decision

### Two-step probe-then-build flow (chosen)

Add a new endpoint `POST /api/v1/factory/probe-iso` that:

1. Downloads (or reuses the cache) the ISO.
2. Extracts and parses the comps XML.
3. Returns available environment groups within seconds.

The existing `POST /api/v1/factory/build-from-iso` gains one optional field:
`base_environment` (string, the `<id>` value from comps). When empty, behavior
is identical to today: `@^minimal-environment` is used.

**Why a separate endpoint, not inline in the build?**

The build runs async and takes 5-30 minutes. If the admin discovers post-build
that they got the wrong environment they have to re-run a 20-minute job. The
probe endpoint returns in under 60 seconds (download + XML parse) and gives the
admin a choice before committing the build slot. This matches the pattern already
established for `GET /api/v1/image-roles` — present options, then let the admin
pick.

**Alternatives considered:**

- *Inline at build time with a `detect_environment=true` flag* — defers the
  decision too late; operator has no visibility before committing.
- *Pre-flight validation only (no UI choice)* — solves the "wrong group breaks
  install" case but not the "I want Server instead of Minimal" case.
- *Polling model (start build, block on environment selection)* — adds complex
  mid-build state machine that conflicts with the existing async pattern.

---

## Comps XML Structure

RHEL-family ISOs store group metadata at `repodata/*-comps*.xml` (the exact
filename is referenced in `repodata/repomd.xml`). Environment groups live under
`<comps><environment>`:

```xml
<environment>
  <id>minimal-environment</id>
  <_name>Minimal Install</_name>
  <_description>Basic functionality.</_description>
  <display_order>99</display_order>
  <grouplist>
    <groupid>core</groupid>
  </grouplist>
  <optionlist>
    <groupid>standard</groupid>
  </optionlist>
</environment>
```

The kickstart `@^<id>` syntax maps directly to the `<id>` element.

The `<_name>` and `<_description>` elements use a gettext-style prefix (`_`)
for i18n; the text content is the English string for our purposes.

---

## Data Model

### Cached metadata sidecar

The ISO is already cached at `<iso-cache-dir>/<sha256(url)>.iso`. We write a
companion JSON file at `<sha256(url)>.meta.json` in the same directory. This
keeps probe results free indefinitely — no re-download, no re-parse.

```jsonc
// <sha256(url)>.meta.json
{
  "iso_url":    "https://download.rockylinux.org/.../Rocky-10.1-x86_64-dvd1.iso",
  "probed_at":  "2026-04-20T14:22:00Z",
  "distro":     "rocky",
  "volume_label": "Rocky-10-1-x86_64-dvd",
  "environments": [
    {
      "id":           "minimal-environment",
      "name":         "Minimal Install",
      "description":  "Basic functionality.",
      "display_order": 99,
      "is_default":   true
    },
    {
      "id":           "server-product-environment",
      "name":         "Server",
      "description":  "An integrated, easy-to-manage server.",
      "display_order": 1,
      "is_default":   false
    }
  ]
}
```

`is_default` is set to `true` for the entry whose `id` matches
`minimal-environment`, falling back to the entry with the lowest `display_order`
when `minimal-environment` is absent.

The sidecar is written by `ProbeISO` and read by subsequent calls. On a cache
hit (`.meta.json` exists and `iso_url` matches), the probe endpoint returns
immediately without touching the ISO file.

---

## New Go Types

### `pkg/api/types.go` additions

```go
// ISOEnvironmentGroup describes one installable environment group from
// an ISO's comps XML, as returned by POST /api/v1/factory/probe-iso.
type ISOEnvironmentGroup struct {
    ID           string `json:"id"`
    Name         string `json:"name"`
    Description  string `json:"description,omitempty"`
    DisplayOrder int    `json:"display_order,omitempty"`
    IsDefault    bool   `json:"is_default"`
}

// ProbeISORequest is the body for POST /api/v1/factory/probe-iso.
type ProbeISORequest struct {
    URL string `json:"url"`
}

// ProbeISOResponse is returned by POST /api/v1/factory/probe-iso.
type ProbeISOResponse struct {
    URL          string                `json:"url"`
    Distro       string                `json:"distro"`
    VolumeLabel  string                `json:"volume_label,omitempty"`
    Environments []ISOEnvironmentGroup `json:"environments"`
    // NoComps is true when the ISO does not contain comps XML (Ubuntu, Debian,
    // minimal ISOs without group data). The UI should suppress the picker.
    NoComps bool `json:"no_comps,omitempty"`
}
```

### `BuildFromISORequest` addition

```go
// BaseEnvironment is the comps environment group to install, e.g.
// "minimal-environment" or "server-product-environment". Only applies to
// RHEL-family kickstart builds. When empty, "minimal-environment" is used.
// Obtain valid values from POST /api/v1/factory/probe-iso before building.
BaseEnvironment string `json:"base_environment,omitempty"`
```

---

## New Go Package: `internal/image/isoinstaller/comps`

Keeping this isolated makes it testable without needing a real ISO.

```
internal/image/isoinstaller/comps/
    comps.go        // ProbeComps(isoPath string) ([]EnvironmentGroup, error)
    comps_test.go
```

### Extraction strategy

The existing codebase already has `extractFromISO` in `kernelboot.go` which
tries `7z` then `isoinfo`. The comps probe uses the same toolchain:

```
Step 1: Find repomd.xml
  7z e -so <iso> repodata/repomd.xml

Step 2: Parse repomd.xml to find the comps filename
  <data type="group_gz"> or <data type="group">
  <location href="repodata/abc123-comps-Everything.x86_64.xml.gz"/>

Step 3: Extract and decompress the comps file
  7z e -so <iso> repodata/<comps-filename> | gunzip (if .gz)

Step 4: Parse XML -> []EnvironmentGroup
```

Why not loop-mount? Loop mounting requires root and a kernel loop module.
`extractFromISO` already established the `7z`/`isoinfo` precedent for
privilege-free extraction — stay consistent. `7z` handles ISO 9660 + Joliet +
Rock Ridge natively and is already listed as a build dependency.

If `repomd.xml` is absent or has no `group` data entry, return
`(nil, nil)` — the caller interprets that as `NoComps: true`.

---

## New Factory Method

```go
// ProbeISO downloads (or cache-hits) an ISO, parses its comps XML, and
// returns available environment groups. If the ISO has no comps data
// (Ubuntu, Debian, minimal ISOs), groups is nil and noComps is true.
//
// Results are cached alongside the ISO as <sha256(url)>.meta.json.
// Subsequent calls with the same URL return immediately from cache.
func (f *Factory) ProbeISO(ctx context.Context, rawURL string) (groups []api.ISOEnvironmentGroup, distro string, volumeLabel string, noComps bool, err error)
```

`ProbeISO` reuses `isoCachePath` for the ISO path and derives the sidecar path
as `isoPath + ".meta.json"` (same directory, same key). The download uses
`downloadURLWithResume` with a progress callback wired to a progress handle
so the UI can show download progress while the ISO is being fetched for the
first time (same pattern as `buildISOAsync`).

---

## API Endpoints

### `POST /api/v1/factory/probe-iso`

**Request**
```json
{ "url": "https://download.rockylinux.org/.../Rocky-10.1-x86_64-dvd1.iso" }
```

**Behavior**
- Validates URL (uses existing `validatePullURL`).
- Detects distro from URL (existing `isoinstaller.DetectDistro`).
- Returns 400 if URL is empty.
- Downloads ISO to cache if not already present. This is synchronous — the
  handler blocks. For a cold cache this takes up to several minutes on a slow
  link. The client must use a long HTTP timeout (recommend 600s). Because the
  ISO is being pulled to serve a probe request, it will be in cache for the
  subsequent build request: no double-download.
- Returns 200 with `ProbeISOResponse`.

**Response — Rocky DVD ISO**
```json
{
  "url": "https://download.rockylinux.org/.../Rocky-10.1-x86_64-dvd1.iso",
  "distro": "rocky",
  "volume_label": "Rocky-10-1-x86_64-dvd",
  "environments": [
    { "id": "minimal-environment",          "name": "Minimal Install",  "is_default": true,  "display_order": 99 },
    { "id": "server-product-environment",   "name": "Server",           "is_default": false, "display_order": 1  },
    { "id": "workstation-product-environment", "name": "Workstation",   "is_default": false, "display_order": 10 }
  ]
}
```

**Response — Rocky Minimal ISO or Ubuntu**
```json
{
  "url": "https://download.rockylinux.org/.../Rocky-10.1-x86_64-minimal.iso",
  "distro": "rocky",
  "volume_label": "Rocky-10-1-x86_64-minimal",
  "environments": [],
  "no_comps": true
}
```

### `POST /api/v1/factory/build-from-iso` (extended)

Gains the `base_environment` field in `BuildFromISORequest`. No breaking change:
omitting the field produces identical behavior to today.

---

## Kickstart Template Change

In `kickstart.go`, the `ksTemplateData` struct gains:

```go
// BaseEnvironment is the comps @^<id> group to install.
// Defaults to "minimal-environment".
BaseEnvironment string
```

The template line:

```
@^minimal-environment
```

Becomes:

```
@^{{.BaseEnvironment}}
```

`generateKickstart` sets `BaseEnvironment` from `opts.BaseEnvironment`, falling
back to `"minimal-environment"` when empty.

`BuildOptions` gains `BaseEnvironment string`. `BuildFromISORequest` propagates
it through `factory.go → buildISOAsync → buildOpts`.

---

## UI Flow

### New: two-step build form

```
Step 1 — Enter ISO URL
  [ISO URL field]
  [Probe ISO] button

  On click:
    POST /api/v1/factory/probe-iso { "url": "..." }
    Show spinner: "Fetching ISO metadata..."
    (Note: if ISO is already cached this returns in <5s.)

Step 2 — Configure build
  If response.no_comps == true OR response.environments is empty:
    No environment picker is shown.
    Build uses the default (minimal-environment for RHEL, n/a for Ubuntu).

  If response.environments is non-empty:
    Show a radio group / select:
      [o] Minimal Install (recommended for HPC)
      [ ] Server
      [ ] Workstation
      [ ] Server with GUI
    The entry with is_default=true is pre-selected.

  Remaining fields (name, version, roles, firmware, etc.) are unchanged.

  [Start Build] → POST /api/v1/factory/build-from-iso {
    "url": "...",
    "base_environment": "server-product-environment",
    ...
  }
```

The probe step is optional in the API — an admin building programmatically can
skip it entirely and pass `base_environment` directly if they already know the
group ID. The UI enforces the probe step to prevent typos.

---

## Implementation Plan

Order is chosen to avoid partially-complete states at each step boundary.

### Step 1 — Comps parser (`internal/image/isoinstaller/comps/comps.go`)

- `ProbeComps(isoPath string) ([]EnvironmentGroup, error)` where
  `EnvironmentGroup` mirrors `api.ISOEnvironmentGroup`.
- Uses `7z` (via `exec.Command`) to extract `repodata/repomd.xml`, parses it
  to find the comps filename, extracts and optionally decompresses the comps
  file, and parses it with `encoding/xml`.
- Returns `nil, nil` (not an error) when no comps data is found.
- Write unit tests with a fixture: a minimal synthetic comps XML string (no
  real ISO needed — just test the XML parsing logic).

### Step 2 — API types (`pkg/api/types.go`)

- Add `ISOEnvironmentGroup`, `ProbeISORequest`, `ProbeISOResponse`.
- Add `BaseEnvironment string` to `BuildFromISORequest`.

### Step 3 — Factory method (`internal/image/factory.go`)

- Add `ProbeISO(ctx, url)` method.
- Implement sidecar cache read/write (`.meta.json`).
- Wire `isoCachePath` for ISO download; sidecar is `isoPath + ".meta.json"`.

### Step 4 — Kickstart template (`internal/image/isoinstaller/kickstart.go`)

- Add `BaseEnvironment string` to `ksTemplateData` and `BuildOptions`.
- Replace hardcoded `@^minimal-environment` with `@^{{.BaseEnvironment}}`.
- Set default in `generateKickstart` when field is empty.
- Propagate through `factory.go`: `req.BaseEnvironment` → `buildOpts.BaseEnvironment`.

### Step 5 — Handler (`internal/server/handlers/factory.go`)

- Add `ProbeISO` handler for `POST /api/v1/factory/probe-iso`.
- Register route in `server.go` (same group as `build-from-iso`).

### Step 6 — UI

- Add probe step to the ISO build form.
- Show/hide environment picker based on `no_comps`.
- Pre-select `is_default` entry.

---

## Edge Cases

### Rocky/RHEL minimal ISO (no comps XML)

The minimal ISO ships only installation packages; there is no `repodata/`
directory and no comps XML. `ProbeComps` returns `nil, nil`. The `ProbeISOResponse`
has `no_comps: true` and an empty `environments` list. The UI hides the picker.
The kickstart uses `@^minimal-environment` by default, which is correct because
the minimal ISO only ships the minimal group — it will fail if you try to
install any other group anyway.

### Ubuntu, Debian, Alpine, SUSE

These distros use different installer automation formats (autoinstall, preseed,
AutoYaST, answers). The `@^<group>` syntax is RHEL-specific. `ProbeComps`
returns `nil, nil` for any ISO that lacks `repodata/repomd.xml`. The probe
endpoint returns `no_comps: true`. The `base_environment` field in
`BuildFromISORequest` is silently ignored for non-kickstart distros (same
pattern as `CustomKickstart` for non-RHEL distros).

### Custom kickstart override

When `BuildFromISORequest.CustomKickstart` is non-empty, the entire generated
kickstart is replaced. `base_environment` is irrelevant and ignored. The
kickstart path in `generateKickstart` already returns early on non-empty
`customKickstart`, so no change is needed there.

### 7z not installed

`ProbeComps` uses the same `7z`-then-`isoinfo` fallback ladder as
`extractFromISO`. If neither tool is available, `ProbeComps` returns an error.
The probe handler returns HTTP 500 with a message directing the admin to install
`p7zip-full`. This is the same gate that already blocks direct kernel boot.

### Large ISO, probe called before full download

`ProbeISO` downloads the full ISO before probing. Partial downloads use the
`.partial` resume file (same as `buildISOAsync`). The admin must wait for the
full download the first time, but subsequent builds and re-probes of the same
URL are instant. An optimization (range-request to extract only `repodata/`)
is possible but out of scope: it requires knowing the ISO's internal directory
table, which varies by ISO layout, and would add significant complexity for
marginal gain on a one-time operation.

### comps XML with no `<environment>` elements

Some ISOs ship a comps XML that only defines `<group>` elements, not
`<environment>` elements (e.g., certain CentOS Stream stream ISOs). `ProbeComps`
returns an empty slice (not nil) and `noComps` stays false. The UI would show
an empty picker — handle this by treating an empty `environments` list the same
as `no_comps: true` in the UI: hide the picker, use the default.

### comps XML `display_order` absent

The `<display_order>` element is optional in the comps schema. When absent,
default to `0`. The `is_default` selection uses group ID matching
(`minimal-environment`) first; display order is only used for sort ordering
in the UI.

### Race: two builds from the same URL simultaneously, sidecar not yet written

Both calls pass through `ProbeISO`. The sidecar write is a simple
`os.WriteFile` (atomic on Linux via kernel page cache). If two goroutines
race on the first probe, the last writer wins and both end up with the same
correct data. No corruption risk; comps XML is deterministic for a given ISO.
