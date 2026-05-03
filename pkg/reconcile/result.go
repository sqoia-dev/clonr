// Package reconcile defines shared types for clustr's artifact reconciliation
// subsystem. These types are used by the HTTP API layer, internal reconcile
// callers (startup pass, periodic timer, pre-deploy guard), and the CLI.
//
// Design: per-type reconcilers (image blobs, initramfs builds, bundle repos)
// each return a *Result using the types defined here so the audit log, API
// response, and UI all share the same vocabulary. A generic Reconcilable
// interface is deliberately NOT defined here — the per-type differences are
// large enough that a common interface would degenerate to func(ctx) error.
// Revisit at Sprint 33 if four typed reconcilers exist.
package reconcile

import (
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// Outcome is the top-level result of a single reconcile pass for one artifact.
type Outcome string

const (
	// OutcomeOK means all checks passed — artifact is consistent with the DB.
	OutcomeOK Outcome = "ok"

	// OutcomeHealed means the reconciler detected drift and self-healed it by
	// updating the DB record to match the on-disk artifact. Applies only when
	// independent corroborating evidence (metadata.json) confirms the on-disk
	// artifact is the correct source of truth (F1 and F6).
	OutcomeHealed Outcome = "healed"

	// OutcomeQuarantined means the reconciler detected drift it cannot safely
	// self-heal: the image status is set to 'corrupt' and the artifact is left
	// untouched. Applies to F2, F3, F5.
	OutcomeQuarantined Outcome = "quarantined"

	// OutcomeBlobMissing means the blob file does not exist on disk (F4).
	// The image status is set to 'blob_missing'.
	OutcomeBlobMissing Outcome = "blob_missing"

	// OutcomeNoChange means the image was already in a non-ready status (e.g.
	// already quarantined or blob_missing) and the reconciler took no action.
	OutcomeNoChange Outcome = "no_change"

	// OutcomeReFinalized means force_re_finalize was requested and accepted:
	// the DB checksum + size_bytes were updated to match disk, metadata.json
	// was rewritten, and status is now 'ready'.
	OutcomeReFinalized Outcome = "re_finalized"
)

// BlobPathResolution describes how the reconciler resolved the blob path.
type BlobPathResolution string

const (
	// BlobPathFoundAtDBPath means os.Stat(blob_path) succeeded.
	BlobPathFoundAtDBPath BlobPathResolution = "found_at_db_path"

	// BlobPathFoundAtDefaultLayout means blob_path was absent or stale but
	// <imageDir>/<id>/rootfs.tar exists and was used instead (F6 self-heal).
	BlobPathFoundAtDefaultLayout BlobPathResolution = "found_at_default_layout"

	// BlobPathNotFound means neither path yielded a file (F4).
	BlobPathNotFound BlobPathResolution = "not_found"
)

// Checks holds the detailed inspection results from a single reconcile pass.
// All fields are populated regardless of outcome so the operator can see the
// full picture even when everything is healthy.
type Checks struct {
	// BlobExists is true when the blob file was found at any path.
	BlobExists bool `json:"blob_exists"`

	// SizeOnDisk is the file size in bytes as returned by os.Stat.
	// Zero when BlobExists is false.
	SizeOnDisk int64 `json:"size_on_disk"`

	// SizeInDB is the size_bytes column value from the DB at check time.
	SizeInDB int64 `json:"size_in_db"`

	// SHAOnDisk is the hex-encoded SHA256 of the on-disk blob.
	// Empty when BlobExists is false or when the hash was skipped (e.g. size
	// mismatch detected before hashing).
	SHAOnDisk string `json:"sha_on_disk"`

	// SHAInDB is the checksum column value from the DB at check time.
	SHAInDB string `json:"sha_in_db"`

	// SHAInMetadata is the content_sha256 field from metadata.json, or empty
	// if the sidecar was absent or did not contain a SHA.
	SHAInMetadata string `json:"sha_in_metadata"`

	// BlobPathResolution describes how (or whether) the blob file was located.
	BlobPathResolution BlobPathResolution `json:"blob_path_resolution"`
}

// Result is the structured output of ReconcileImage. It is returned by the
// core reconcile function and serialised directly as the HTTP response body
// for POST /api/v1/images/:id/reconcile.
type Result struct {
	// ImageID is the UUID of the image that was inspected.
	ImageID string `json:"image_id"`

	// Outcome summarises what the reconciler did.
	Outcome Outcome `json:"outcome"`

	// PreviousStatus is the image status before this reconcile pass.
	PreviousStatus api.ImageStatus `json:"previous_status"`

	// NewStatus is the image status after this reconcile pass (may equal
	// PreviousStatus when outcome is ok or no_change).
	NewStatus api.ImageStatus `json:"new_status"`

	// Checks holds the detailed per-field inspection results.
	Checks Checks `json:"checks"`

	// ActionsTaken lists the DB or on-disk mutations performed, e.g.
	// ["updated_checksum", "updated_size_bytes", "updated_blob_path"].
	// Empty when outcome is ok or no_change.
	ActionsTaken []string `json:"actions_taken"`

	// AuditID is the ID of the audit_log row written for this reconcile pass,
	// or empty if no state change occurred.
	AuditID string `json:"audit_id,omitempty"`

	// ErrorDetail explains why the reconciler quarantined the image or why
	// it failed. Empty on ok / healed / no_change outcomes.
	ErrorDetail string `json:"error_detail,omitempty"`

	// CheckedAt is when the reconcile pass ran.
	CheckedAt time.Time `json:"checked_at"`
}

// Opts controls the behaviour of a single ReconcileImage call.
type Opts struct {
	// CacheTTL is the maximum age of a cached reconcile result that may be
	// returned without re-hashing. Zero means always re-hash.
	CacheTTL time.Duration

	// ForceReFinalize, when true, accepts the on-disk SHA as the new truth:
	// updates the DB record and rewrites metadata.json, then sets status=ready.
	// Requires the blob to exist on disk. Rejected if the image is already healthy.
	ForceReFinalize bool

	// FailOnQuarantine, when true, causes ReconcileImage to return a non-nil
	// error when the outcome is quarantined or blob_missing. Used by the
	// pre-deploy guard so callers can treat quarantine as a hard failure.
	FailOnQuarantine bool
}
