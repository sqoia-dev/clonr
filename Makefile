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

# Kernel version pinned in packaging/initramfs-builder/Dockerfile.
# Update here when the production kernel is bumped.
INITRAMFS_KERNEL_VERSION ?= 5.14.0-503.40.1.el9_5.x86_64

# Builder image at ghcr.io/sqoia-dev/initramfs-builder:<kernel-version>.
# Built via .github/workflows/initramfs-builder.yml (manual dispatch).
# Not built on every push — only when the kernel pin changes.
INITRAMFS_BUILDER_IMAGE ?= ghcr.io/sqoia-dev/initramfs-builder:$(INITRAMFS_KERNEL_VERSION)

# MODULES_PATH: directory containing kernel modules.
# In CI this is /modules (pre-populated inside the builder image).
# Locally: docker run --rm -v $(pwd):/clustr $(INITRAMFS_BUILDER_IMAGE) \
#            make -C /clustr initramfs MODULES_PATH=/modules
MODULES_PATH ?=

.PHONY: all client server clientd privhelper static clean test web initramfs initramfs-verify

all: web client server clientd privhelper

web:
	cd web && pnpm install --frozen-lockfile && pnpm build
	rm -rf internal/server/web/dist
	cp -r web/dist internal/server/web/dist

client:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/clustr ./cmd/clustr

server: web
	go build $(LDFLAGS) -o bin/clustr-serverd ./cmd/clustr-serverd

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

test:
	go test ./... -v

clean:
	rm -rf bin/

# ── initramfs ────────────────────────────────────────────────────────────────
# Build the PXE initramfs image.
#
# Prerequisites:
#   - bin/clustr must already be built (make static or make client)
#   - MODULES_PATH must point to a directory containing kernel modules
#     (see INITRAMFS_BUILDER_IMAGE above for the CI/local workflow)
#
# Typical CI invocation (inside builder container):
#   make initramfs MODULES_PATH=/modules
#
# Typical local invocation (pulls builder image):
#   docker run --rm -v $(shell pwd):/clustr $(INITRAMFS_BUILDER_IMAGE) \
#     make -C /clustr static initramfs MODULES_PATH=/modules
initramfs: bin/clustr
	@if [ -z "$(MODULES_PATH)" ]; then \
	  echo ""; \
	  echo "ERROR: MODULES_PATH is not set."; \
	  echo ""; \
	  echo "  CI:    make initramfs MODULES_PATH=/modules"; \
	  echo "  Local: docker run --rm -v \$$(pwd):/clustr $(INITRAMFS_BUILDER_IMAGE)"; \
	  echo "           make -C /clustr initramfs MODULES_PATH=/modules"; \
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
