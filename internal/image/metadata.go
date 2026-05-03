// Package image provides the core image management primitives for clustr.
package image

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ImageMetadata is the JSON sidecar stored alongside the rootfs tarball at
// /var/lib/clustr/images/<image-id>/metadata.json.
//
// Schema version history:
//
//	1 — initial schema (ADR-0009 Sprint 3 prep)
//
// The sidecar is the authoritative source for content-level metadata. The DB
// column base_images.metadata_json mirrors it for fast queries without disk I/O.
type ImageMetadata struct {
	// SchemaVersion distinguishes future incompatible schema changes.
	// Consumers must reject sidecar files with a SchemaVersion they don't know.
	SchemaVersion int `json:"schema_version"`

	// ID is the canonical UUID that matches the base_images.id DB column.
	ID string `json:"id"`

	// Name is the human-readable image name (e.g. "rocky10-compute").
	Name string `json:"name"`

	// Distro is the short distribution name, lower-cased (e.g. "rocky", "debian").
	Distro string `json:"distro"`

	// DistroVersion is the distribution release version (e.g. "10.1", "12.0").
	DistroVersion string `json:"distro_version"`

	// KernelVersion is the uname -r output from the rootfs
	// (e.g. "5.14.0-503.40.1.el9_5.x86_64").
	KernelVersion string `json:"kernel_version"`

	// KernelPinned is true when the image was built with a specific kernel version
	// that should not be updated by the deployer or node firstboot scripts.
	KernelPinned bool `json:"kernel_pinned"`

	// Architecture is the CPU architecture (e.g. "x86_64", "aarch64").
	Architecture string `json:"architecture"`

	// FirmwareSupport lists the firmware interfaces this image can be deployed to.
	// Content-only images (ADR-0009) always support both ["bios", "uefi"] because
	// bootloader binaries are excluded from the tarball and re-installed at deploy
	// time for the target node's firmware type.
	FirmwareSupport []string `json:"firmware_support"`

	// ContentSHA256 is the hex-encoded sha256 of the rootfs tarball.
	// Matches the X-Clustr-Blob-SHA256 header served by GET /images/:id/blob.
	ContentSHA256 string `json:"content_sha256"`

	// ContentSizeBytes is the byte length of the rootfs tarball.
	ContentSizeBytes int64 `json:"content_size_bytes"`

	// CreatedAt is when the image record was first created.
	CreatedAt time.Time `json:"created_at"`

	// BuildMethod identifies how the image rootfs was produced.
	// Valid values: "iso", "host-capture", "pull", "import".
	BuildMethod string `json:"build_method"`

	// PackageManifest is the full list of installed RPM or DEB package NVRs,
	// e.g. ["bash-5.1.8-6.el9.x86_64", ...]. Populated during image build;
	// omitted when the manifest was not captured.
	PackageManifest []string `json:"package_manifest,omitempty"`

	// RequiredSecrets describes secrets the deployer must inject into the node
	// rootfs before first boot (ADR-0009 secrets architecture amendment).
	// Omitted when the image has no secret requirements.
	RequiredSecrets []RequiredSecret `json:"required_secrets,omitempty"`

	// DefaultKernelArgs is the list of extra kernel command-line arguments to
	// append at deploy time (e.g. "console=ttyS0,115200", "rd.shell").
	// Omitted when no extra args are needed.
	DefaultKernelArgs []string `json:"default_kernel_args,omitempty"`

	// PostInstallScripts lists paths within the rootfs that the deployer should
	// execute inside a chroot after the rootfs is laid down (e.g.
	// "/usr/local/sbin/clustr-firstboot.sh"). Omitted when empty.
	PostInstallScripts []string `json:"post_install_scripts,omitempty"`
}

// RequiredSecret describes a secret file the deployer must inject into the
// node rootfs before first boot. The deployer reads the secret from its
// configured secrets store and writes it to Path inside the rootfs, setting
// the specified ownership and permissions.
type RequiredSecret struct {
	// Name is the logical name of the secret (e.g. "munge.key").
	// The deployer looks this name up in its secrets store.
	Name string `json:"name"`

	// Path is the absolute path inside the rootfs where the secret is written
	// (e.g. "/etc/munge/munge.key").
	Path string `json:"path"`

	// Owner is the owning user (e.g. "munge"). Applied via chown inside the rootfs.
	Owner string `json:"owner"`

	// Group is the owning group (e.g. "munge"). Applied via chgrp inside the rootfs.
	Group string `json:"group"`

	// Mode is the octal permission string (e.g. "0400"). Applied via chmod inside
	// the rootfs.
	Mode string `json:"mode"`
}

// MetadataSchemaVersion is the current schema version written by this code.
// Bump when making backward-incompatible changes to ImageMetadata.
const MetadataSchemaVersion = 1

// metadataFileName is the name of the sidecar file within the image directory.
const metadataFileName = "metadata.json"

// MetadataPath returns the on-disk path of the metadata sidecar for imageID.
// imageDir is the root image storage directory (e.g. /var/lib/clustr/images).
func MetadataPath(imageDir, imageID string) string {
	return filepath.Join(imageDir, imageID, metadataFileName)
}

// WriteMetadata marshals m to JSON and writes it to the sidecar path for
// imageID under imageDir. The parent directory is created if absent.
func WriteMetadata(imageDir, imageID string, m ImageMetadata) error {
	m.SchemaVersion = MetadataSchemaVersion
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("image metadata: marshal: %w", err)
	}
	dir := filepath.Join(imageDir, imageID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("image metadata: mkdir %s: %w", dir, err)
	}
	path := MetadataPath(imageDir, imageID)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("image metadata: write %s: %w", path, err)
	}
	return nil
}

// ReadMetadata reads and unmarshals the metadata sidecar for imageID.
// Returns os.ErrNotExist (wrapped) if the sidecar has not been written yet.
func ReadMetadata(imageDir, imageID string) (ImageMetadata, error) {
	path := MetadataPath(imageDir, imageID)
	data, err := os.ReadFile(path)
	if err != nil {
		return ImageMetadata{}, fmt.Errorf("image metadata: read %s: %w", path, err)
	}
	var m ImageMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		return ImageMetadata{}, fmt.Errorf("image metadata: unmarshal %s: %w", path, err)
	}
	return m, nil
}

// StoreMetadata is a context-aware helper that writes the metadata sidecar and
// returns the JSON-encoded bytes for callers that also need to persist to the DB.
func StoreMetadata(ctx context.Context, imageDir, imageID string, m ImageMetadata) ([]byte, error) {
	_ = ctx // reserved for future async/trace use
	if err := WriteMetadata(imageDir, imageID, m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}
