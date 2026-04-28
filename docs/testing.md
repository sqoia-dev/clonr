# clustr Testing Guide

## Test Suite Overview

| Command | What it runs | When |
|---|---|---|
| `go test ./...` | All Go unit + integration tests | Every push/PR via CI |
| `make test` | Same as above | Local |
| `node --test test/js/...` | JS unit tests (CSP policy, app-utils) | Every push/PR via CI |
| `make test-js` | Same as above | Local |
| `make a11y` | WCAG 2.1 AA audit (axe-core + jsdom) | Every push/PR via CI |
| `make smoke` | Docker smoke test — node registration | Every push/PR via CI |

## Smoke Test (I2)

The smoke test exercises the critical path that a PXE-booted node follows when
it first contacts clustr-serverd.

### What it tests

1. `clustr-serverd` starts inside Docker and serves `GET /api/v1/health`
2. The bootstrap admin API key is generated and printed to stdout
3. A simulated node POSTs `POST /api/v1/nodes/register` with a synthetic hardware
   profile (a single NIC with a test MAC address)
4. `GET /api/v1/nodes` returns the node with `state == "registered"`

### Why not real QEMU?

KVM is not available on GitHub-hosted runners (they run inside VMs). QEMU TCG
(software emulation) for a full PXE boot + image deploy cycle would exceed the
5-minute wall-clock budget. `POST /api/v1/nodes/register` is the exact HTTP call
`clustr-static` makes from the PXE initramfs — testing this endpoint with a
realistic JSON payload exercises the same server code path.

A full QEMU-based end-to-end test is reserved for the lab-validate workflow
(which runs on real hardware via the Proxmox lab — see `docs/release.md`).

### Running locally

```bash
# Requires: Docker daemon running, curl
make smoke

# With a custom image or longer timeout:
SMOKE_IMAGE=myimage:tag SMOKE_TIMEOUT=120 bash scripts/ci/smoke.sh
```

### CI status

The smoke job runs on every push and PR, declared in `.github/workflows/ci.yml`
as a separate job with `continue-on-error: true` during the initial flake window.

Acceptance criteria (per sprint plan I2):
- `make smoke` is green on 3 consecutive main pushes
- Once that threshold is met, remove `continue-on-error: true` from the CI job

### Flake policy

If the smoke test flakes (intermittent failure not related to a code regression):
1. File a GitHub issue tagged `ci:flake` with the run ID
2. Leave `continue-on-error: true` in place
3. Investigate the timing or teardown race in `scripts/ci/smoke.sh`

Do not remove `continue-on-error: true` until the flake is resolved.

## Go Unit Tests

```bash
go test ./... -v -count=1 -timeout 300s
```

No external services required — all tests use in-memory SQLite via `db.Open` with
a temp dir. The `AuthDevMode: true` config bypasses the database key lookup so
tests can use a mock `Bearer test-token` header.

## JavaScript Tests

### Prerequisites

```bash
npm install --prefix test/js axe-core jsdom
```

### Commands

```bash
# CSP policy assertions (no npm deps needed):
node --test test/js/csp-policy.test.mjs

# App utils unit tests (no npm deps needed):
node --test test/js/app-utils.test.mjs

# WCAG 2.1 AA accessibility audit (requires axe-core + jsdom):
make a11y
```

## Lighthouse Performance Budget

Lighthouse CI runs against the static HTML during CI. The budget thresholds are
in `lighthouse-budget.json`; the runner config is in `.lighthouserc.json`.

Performance assertions are **warnings** (non-blocking); the accessibility score
gate (`>= 0.90`) is a hard CI failure.

```bash
# Install:
npm install -g @lhci/cli

# Run locally:
lhci autorun
```

## Link Check

Markdown link checker runs against `docs/*.md`, `README.md`, and `CHANGELOG.md`.
External URLs are skipped (configured in `.github/mlc-config.json`) to prevent
flaky failures from network unavailability.

```bash
npx --yes markdown-link-check --config .github/mlc-config.json README.md
```
