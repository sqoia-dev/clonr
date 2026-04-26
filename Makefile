VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -ldflags="-X main.version=$(VERSION) -X main.commitSHA=$(COMMIT) -X main.buildTime=$(BUILD_TIME) -s -w"

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
