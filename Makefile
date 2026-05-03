VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Read Slurm bundle metadata from build/slurm/versions.yml.
# We use awk rather than yq so the build has no extra tool dependency.
# Decision (Dinesh, 2026-04-27): awk fallback chosen over yq to keep the
# build self-contained; matches the design doc §15 "simpler path" guidance.
VERSIONS_YML := build/slurm/versions.yml
SLURM_VERSION        ?= $(shell awk '/^slurm:/{in_slurm=1} in_slurm && /version:/{gsub(/[" ]/, "", $$2); print $$2; exit}' $(VERSIONS_YML))
CLUSTR_RELEASE       ?= $(shell awk '/^clustr_release:/{gsub(/[" ]/, "", $$2); print $$2; exit}' $(VERSIONS_YML))
BUNDLE_VERSION       ?= v$(SLURM_VERSION)-clustr$(CLUSTR_RELEASE)
# Bundle SHA256 is set by the release workflow and embedded via ldflags.
# Default to the known SHA256 for the current bundle; update when bundle is rebuilt.
# clustr5: SHA256 of clustr-slurm-bundle-v24.11.4-clustr5-el9-x86_64.tar.gz
BUNDLE_SHA256        ?= 575ead6b320ff70b9496e5464a7536a224c35639f7c61bac0fec63721e7394b4

LDFLAGS    := -ldflags="-X main.version=$(VERSION) \
              -X main.commitSHA=$(COMMIT) \
              -X main.buildTime=$(BUILD_TIME) \
              -X main.builtinSlurmVersion=$(SLURM_VERSION) \
              -X main.builtinSlurmBundleVersion=$(BUNDLE_VERSION) \
              -X main.builtinSlurmBundleSHA256=$(BUNDLE_SHA256) \
              -s -w"

# Reference kernel version — matches production cluster nodes.
# CI resolves MODULES_PATH dynamically from the single installed kernel in
# the rockylinux:9 GHA container; update this comment when the prod kernel bumps.
INITRAMFS_KERNEL_VERSION ?= 5.14.0-503.40.1.el9_5.x86_64

# MODULES_PATH: directory containing kernel modules.
# CI: /lib/modules/$(ls /lib/modules) inside the rockylinux:9 GHA container.
# In-product button (CLUSTR_CI_MODE=1): /lib/modules on the clustr-serverd host.
# Dev cloner: SSH-pull fallback (see scripts/build-initramfs.sh mode 3).
MODULES_PATH ?=

.PHONY: all client server clientd privhelper static clean test test-web test-backend web initramfs initramfs-verify schemas check-initramfs-script-sync sync-initramfs-script check-bundles-kind-purity

all: web client server clientd privhelper

web:
	cd web && pnpm install --frozen-lockfile && pnpm build
	rm -rf internal/server/web/dist
	cp -r web/dist internal/server/web/dist

client:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/clustr ./cmd/clustr

server: web
	go build -tags webdist $(LDFLAGS) -o bin/clustr-serverd ./cmd/clustr-serverd

clientd:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/clustr-clientd ./cmd/clustr-clientd

# privhelper is the setuid root privilege helper (chmod 4755 applied by RPM post-install).
# Built without CGO for portability; no version ldflags needed (it has no --version flag).
privhelper:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/clustr-privhelper ./cmd/clustr-privhelper

# static builds a fully static binary suitable for embedding in PXE initramfs.
# Uses -a to force rebuild of all packages with CGO disabled.
static:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -a -o bin/clustr-static ./cmd/clustr

test: check-bundles-kind-purity
	GOTOOLCHAIN=auto go test ./... -v

# test-web: full-stack verification with the real embedded SPA (requires pnpm build).
test-web: web
	GOTOOLCHAIN=auto go test -tags webdist ./... -v

test-backend:
	go test $$(go list ./... | grep -v internal/server) -v

# schemas regenerates JSON Schema + OpenAPI 3.1 files from pkg/api types.
# CI runs this and fails if the committed schemas differ from the generated ones:
#   make schemas && git diff --exit-code pkg/api/schema/
schemas:
	go run ./cmd/generate-schemas --output-dir pkg/api/schema/v1

clean:
	rm -rf bin/

# ── initramfs ────────────────────────────────────────────────────────────────
# Build the PXE initramfs image.
#
# Prerequisites:
#   - bin/clustr must already be built (make static or make client)
#   - MODULES_PATH must point to a directory containing kernel modules
#
# Typical CI invocation (inside rockylinux:9 GHA container):
#   make initramfs MODULES_PATH=/lib/modules/$(ls /lib/modules)
#
# In-product "Build initramfs" button (CLUSTR_CI_MODE=1 on clustr-serverd host):
#   scripts/build-initramfs.sh detects Rocky 9 and sources /lib/modules directly.
initramfs: bin/clustr
	@if [ -z "$(MODULES_PATH)" ]; then \
	  echo ""; \
	  echo "ERROR: MODULES_PATH is not set."; \
	  echo ""; \
	  echo "  CI:    make initramfs MODULES_PATH=/lib/modules/\$$(ls /lib/modules)"; \
	  echo "  Dev:   set CLUSTR_CI_MODE=1 (if on Rocky 9) or MODULES_PATH=/path/to/modules"; \
	  echo ""; \
	  exit 1; \
	fi
	MODULES_PATH="$(MODULES_PATH)" bash scripts/build-initramfs.sh bin/clustr initramfs-clustr.img

# initramfs-verify: build twice and assert SHA256 equality.
# Validates that the build is bit-for-bit reproducible.
# Runs both builds sequentially in the same environment; used by CI.
initramfs-verify: bin/clustr
	@if [ -z "$(MODULES_PATH)" ]; then \
	  echo "ERROR: MODULES_PATH is not set."; exit 1; \
	fi
	@echo "--- Reproducibility check: build 1 ---"
	MODULES_PATH="$(MODULES_PATH)" bash scripts/build-initramfs.sh bin/clustr initramfs-run1.img
	@echo "--- Reproducibility check: build 2 ---"
	MODULES_PATH="$(MODULES_PATH)" bash scripts/build-initramfs.sh bin/clustr initramfs-run2.img
	@echo "--- Comparing SHA256 ---"
	@SHA1=$$(sha256sum initramfs-run1.img | awk '{print $$1}'); \
	 SHA2=$$(sha256sum initramfs-run2.img | awk '{print $$1}'); \
	 echo "  run1: $$SHA1"; \
	 echo "  run2: $$SHA2"; \
	 if [ "$$SHA1" = "$$SHA2" ]; then \
	   echo "PASS: initramfs is bit-identical across two builds"; \
	 else \
	   echo "FAIL: initramfs SHA256 mismatch — build is not reproducible"; \
	   exit 1; \
	 fi
	@rm -f initramfs-run1.img initramfs-run2.img \
	       initramfs-run1.modules.manifest initramfs-run2.modules.manifest

# ── initramfs script sync-check ──────────────────────────────────────────────
# The initramfs build script exists in two places:
#   - scripts/build-initramfs.sh          (canonical — used by Makefile + CI)
#   - internal/server/handlers/scripts/   (embedded via go:embed — used by the
#                                          in-product "Build Initramfs" button)
#
# These must always be identical. check-initramfs-script-sync fails CI when they
# drift. sync-initramfs-script is the one-command fix.

check-initramfs-script-sync:
	@diff scripts/build-initramfs.sh internal/server/handlers/scripts/build-initramfs.sh \
	  || (echo "" && echo "ERROR: embedded build-initramfs.sh is out of sync with canonical." \
	      && echo "       Run: make sync-initramfs-script" && exit 1)
	@echo "OK: build-initramfs.sh is in sync"

sync-initramfs-script:
	cp scripts/build-initramfs.sh internal/server/handlers/scripts/build-initramfs.sh
	@echo "Synced: internal/server/handlers/scripts/build-initramfs.sh"

# ── bundles kind purity ───────────────────────────────────────────────────────
# Fail CI if BundlesHandler emits a Kind value other than "build".
# Prevents re-introduction of synthetic bundle rows (see docs/SPRINT-EMBED-SLURM-REMOVAL.md).
check-bundles-kind-purity:
	@! grep -nE 'Kind:[[:space:]]+"[^"]+"' internal/server/handlers/bundles.go | grep -v '"build"' \
		|| (echo "ERROR: BundlesHandler must only emit Kind=build entries; see docs/SPRINT-EMBED-SLURM-REMOVAL.md" && exit 1)
	@echo "OK: bundles kind purity check passed"
