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
# clustr4: SHA256 of clustr-slurm-bundle-v24.11.4-clustr4-el9-x86_64.tar.gz
BUNDLE_SHA256        ?= 923dd3b3f30265da51b7f6ed3fbc501cde355471aedc41b24ed7afa038c04241

LDFLAGS    := -ldflags="-X main.version=$(VERSION) \
              -X main.commitSHA=$(COMMIT) \
              -X main.buildTime=$(BUILD_TIME) \
              -X main.builtinSlurmVersion=$(SLURM_VERSION) \
              -X main.builtinSlurmBundleVersion=$(BUNDLE_VERSION) \
              -X main.builtinSlurmBundleSHA256=$(BUNDLE_SHA256) \
              -s -w"

.PHONY: all client server clientd static clean test

all: client server clientd

client:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/clustr ./cmd/clustr

server:
	go build $(LDFLAGS) -o bin/clustr-serverd ./cmd/clustr-serverd

clientd:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/clustr-clientd ./cmd/clustr-clientd

# static builds a fully static binary suitable for embedding in PXE initramfs.
# Uses -a to force rebuild of all packages with CGO disabled.
static:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -a -o bin/clustr-static ./cmd/clustr

test:
	go test ./... -v

clean:
	rm -rf bin/
