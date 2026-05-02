// Package clientd provides the shared message types and WebSocket client
// for the clustr-clientd daemon.
package clientd

import (
	"encoding/json"
	"time"
)

// ─── Stats message types (#131) ──────────────────────────────────────────────

// StatsSample mirrors stats.Sample for cross-package serialisation.
// It is defined here (in the clientd package) so the server can import it
// without creating an import cycle through internal/clientd/stats.
type StatsSample struct {
	Sensor string            `json:"sensor"`
	Value  float64           `json:"value"`
	Unit   string            `json:"unit,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	TS     time.Time         `json:"ts"`
}

// StatsBatchPayload is the payload for the "stats_batch" node→server message.
// A single stats_batch carries all samples from one plugin for one collection tick.
// The server stores samples idempotently keyed on (batch_id); the node may
// re-send unacknowledged batches with the same batch_id on reconnect.
type StatsBatchPayload struct {
	// BatchID is a client-generated UUID for idempotent server-side ingestion.
	BatchID string `json:"batch_id"`
	// NodeID is the node's UUID, used by the server for DB writes.
	NodeID string `json:"node_id"`
	// Plugin is the plugin name (e.g. "cpu", "memory").
	Plugin string `json:"plugin"`
	// Samples are the measurements from this collection cycle.
	Samples []StatsSample `json:"samples"`
	// TSCollected is the wall-clock time when the batch was produced on the node.
	TSCollected time.Time `json:"ts_collected"`
}

// StatsAckPayload is the payload for the "stats_ack" server→node message.
// The server sends this after ingesting (or rejecting) a stats_batch.
// When Accepted is true, the node may discard the buffered batch.
// When Accepted is false, the node should retry on the next reconnect.
type StatsAckPayload struct {
	// BatchID echoes the batch_id from the corresponding stats_batch message.
	BatchID string `json:"batch_id"`
	// Accepted is true when the server successfully persisted the batch.
	Accepted bool `json:"accepted"`
	// Error is a human-readable rejection reason (only set when Accepted is false).
	Error string `json:"error,omitempty"`
}

// ClientMessage is sent from node to server over the clientd WebSocket.
type ClientMessage struct {
	Type    string          `json:"type"`              // "hello", "heartbeat", "log_batch", "ack", "exec_result"
	MsgID   string          `json:"msg_id"`            // UUID for ack correlation
	Payload json.RawMessage `json:"payload,omitempty"` // type-specific payload
}

// ServerMessage is sent from server to node over the clientd WebSocket.
type ServerMessage struct {
	Type    string          `json:"type"`              // "ack", "config_push", "exec_request", …
	MsgID   string          `json:"msg_id"`            // UUID echoed in ack
	Payload json.RawMessage `json:"payload,omitempty"` // type-specific payload
}

// HelloPayload is the payload for the "hello" message sent on connect.
type HelloPayload struct {
	NodeID         string  `json:"node_id"`
	Hostname       string  `json:"hostname"`
	KernelVersion  string  `json:"kernel_version"`
	UptimeSeconds  float64 `json:"uptime_seconds"`
	ClientdVersion string  `json:"clientd_version"`
}

// HeartbeatPayload is the payload for the "heartbeat" message sent every 60s.
type HeartbeatPayload struct {
	UptimeSeconds  float64         `json:"uptime_seconds"`
	Load1          float64         `json:"load_1"`
	Load5          float64         `json:"load_5"`
	Load15         float64         `json:"load_15"`
	MemTotalKB     int64           `json:"mem_total_kb"`
	MemAvailKB     int64           `json:"mem_avail_kb"`
	DiskUsage      []DiskUsage     `json:"disk_usage"`
	Services       []ServiceStatus `json:"services"`
	KernelVersion  string          `json:"kernel_version"`
	ClientdVersion string          `json:"clientd_version"`
}

// DiskUsage describes filesystem utilization for a single mount point.
type DiskUsage struct {
	MountPoint string `json:"mount_point"`
	TotalBytes int64  `json:"total_bytes"`
	UsedBytes  int64  `json:"used_bytes"`
}

// ServiceStatus describes the current state of a whitelisted systemd service.
type ServiceStatus struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
	State  string `json:"state"`
}

// AckPayload is the payload for the "ack" message (both directions).
type AckPayload struct {
	RefMsgID string `json:"ref_msg_id"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// ConfigPushPayload is the payload for the "config_push" server→node message.
// The server sends this to request an atomic config file replacement on the node.
type ConfigPushPayload struct {
	// Target is the whitelisted config key (e.g. "hosts", "sssd", "chrony").
	Target string `json:"target"`
	// Content is the full file content to write.
	Content string `json:"content"`
	// Checksum is "sha256:<hex>" computed by the server over Content.
	// The node validates this before writing.
	Checksum string `json:"checksum"`
}

// LogPullStartPayload is the payload for the "log_pull_start" server→node message.
// It instructs the node to begin streaming journal entries back as "log_batch" messages.
type LogPullStartPayload struct {
	// Units is an optional list of systemd unit names to filter on.
	// Empty means all units.
	Units []string `json:"units,omitempty"`
	// Priority is the maximum syslog severity to include (0=emerg, 7=debug).
	// -1 means no filter (include everything).
	Priority int `json:"priority"`
	// Since is an optional journalctl --since argument (e.g. "2 minutes ago").
	// Empty means start from now (follow mode, no history).
	Since string `json:"since,omitempty"`
}

// SlurmConfigPushPayload is the payload for the "slurm_config_push" server→node message.
// The server sends this to push one or more Slurm config files and hook scripts to the node atomically.
type SlurmConfigPushPayload struct {
	// PushOpID is the push operation UUID (for ack correlation and state updates).
	PushOpID string `json:"push_op_id"`
	// Files is the list of config files to write. Each file has content + checksum.
	Files []SlurmFilePush `json:"files"`
	// Scripts is the optional list of hook scripts to write (e.g. Prolog, Epilog).
	// Script content changes do NOT trigger scontrol reconfigure because the path
	// in slurm.conf does not change — only the executable at that path is replaced.
	Scripts []SlurmScriptPush `json:"scripts,omitempty"`
	// ApplyAction is "reconfigure" (scontrol reconfigure) or "restart" (systemctl restart slurmd).
	ApplyAction string `json:"apply_action"`
}

// SlurmScriptPush is one Slurm hook script within a SlurmConfigPushPayload.
type SlurmScriptPush struct {
	// ScriptType is the Slurm parameter name, e.g. "Prolog".
	ScriptType string `json:"script_type"`
	// Content is the full script content (must start with a shebang).
	Content string `json:"content"`
	// Checksum is "sha256:<hex>" computed by the server over Content.
	Checksum string `json:"checksum"`
	// DestPath is the absolute path where the script should be written on the node.
	DestPath string `json:"dest_path"`
	// Version is the DB version number for state tracking after successful write.
	Version int `json:"version"`
}

// SlurmFilePush is one file within a SlurmConfigPushPayload.
type SlurmFilePush struct {
	// Filename is the logical name (e.g. "slurm.conf").
	Filename string `json:"filename"`
	// Content is the full rendered file content.
	Content string `json:"content"`
	// Checksum is "sha256:<hex>" computed by the server over Content.
	Checksum string `json:"checksum"`
	// DestPath is the absolute destination path (e.g. "/etc/slurm/slurm.conf").
	DestPath string `json:"dest_path"`
}

// SlurmBinaryPushPayload is the payload for the "slurm_binary_push" server→node message.
// The server sends this to instruct the node to download and install a new Slurm build.
type SlurmBinaryPushPayload struct {
	// BuildID is the server-side build UUID for tracking and ack correlation.
	BuildID string `json:"build_id"`
	// Version is the Slurm version string, e.g. "24.05.3".
	Version string `json:"version"`
	// ArtifactURL is the signed download URL for the build artifact tarball.
	ArtifactURL string `json:"artifact_url"`
	// Checksum is the SHA-256 hex digest of the artifact to verify after download.
	Checksum string `json:"checksum"`
}

// SlurmBinaryAckPayload is the payload for the "ack" message sent after slurm_binary_push.
type SlurmBinaryAckPayload struct {
	// BuildID is the build UUID from the push message.
	BuildID string `json:"build_id"`
	// OK is true when the binary was installed successfully.
	OK bool `json:"ok"`
	// Error is a human-readable description of the failure, if any.
	Error string `json:"error,omitempty"`
	// InstalledVersion is the Slurm version now running on the node.
	InstalledVersion string `json:"installed_version,omitempty"`
}

// SlurmDnfUpgradePayload is the payload for the "slurm_dnf_upgrade" server→node message.
// The server sends this to instruct the node to install Slurm packages from
// clustr-internal-repo via dnf. This is the primary upgrade path.
type SlurmDnfUpgradePayload struct {
	// BuildID is the server-side build UUID for tracking and ack correlation.
	BuildID string `json:"build_id"`
	// Version is the target Slurm version string, e.g. "25.11.5".
	Version string `json:"version"`
	// PkgSpecs are the fully-qualified package specs to install, e.g.
	// ["slurm-25.11.5-clustr1.el9", "slurmd-25.11.5-clustr1.el9"].
	// The privhelper dnf-upgrade verb installs all of these from clustr-internal-repo.
	PkgSpecs []string `json:"pkg_specs"`
}

// SlurmDnfUpgradeAckPayload is the payload for the "ack" message sent after slurm_dnf_upgrade.
type SlurmDnfUpgradeAckPayload struct {
	// BuildID is the build UUID from the push message.
	BuildID string `json:"build_id"`
	// OK is true when dnf completed successfully.
	OK bool `json:"ok"`
	// Error is a human-readable failure description, if any.
	Error string `json:"error,omitempty"`
	// InstalledVersion is the Slurm version now running on the node (from sinfo --version).
	InstalledVersion string `json:"installed_version,omitempty"`
	// FallbackUsed is always false for this path (distinguishes from artifact path in logs).
	FallbackUsed bool `json:"fallback_used"`
}

// SlurmArtifactInstallPayload is the payload for the "slurm_artifact_install" server→node message.
// This is the FALLBACK path — operator-triggered only, never automatic.
// It instructs the node to download the raw tarball artifact and extract it directly.
type SlurmArtifactInstallPayload struct {
	// BuildID is the server-side build UUID for tracking and ack correlation.
	BuildID string `json:"build_id"`
	// Version is the Slurm version string, e.g. "25.11.5".
	Version string `json:"version"`
	// ArtifactURL is the signed download URL for the build artifact tarball.
	ArtifactURL string `json:"artifact_url"`
	// Checksum is the SHA-256 hex digest of the artifact to verify after download.
	Checksum string `json:"checksum"`
}

// SlurmArtifactInstallAckPayload is the payload for the "ack" message sent after slurm_artifact_install.
type SlurmArtifactInstallAckPayload struct {
	// BuildID is the build UUID from the push message.
	BuildID string `json:"build_id"`
	// OK is true when the artifact was installed successfully.
	OK bool `json:"ok"`
	// Error is a human-readable failure description, if any.
	Error string `json:"error,omitempty"`
	// InstalledVersion is the Slurm version now running on the node (from sinfo --version).
	InstalledVersion string `json:"installed_version,omitempty"`
	// FallbackUsed is always true for this path — so the server can distinguish
	// artifact installs from dnf-managed upgrades in the audit trail.
	FallbackUsed bool `json:"fallback_used"`
}

// SlurmAdminCmdPayload is the payload for the "slurm_admin_cmd" server→node message.
// Only accepted on nodes with slurmctld running (controller role).
// Used by the upgrade orchestrator to drain/resume nodes and check job queues.
type SlurmAdminCmdPayload struct {
	// Command is one of: "drain", "resume", "check_queue", "reconfigure".
	Command string `json:"command"`
	// Nodes is the list of Slurm node names (not clustr UUIDs) to act on.
	// For check_queue and reconfigure this may be empty.
	Nodes []string `json:"nodes"`
	// Reason is passed to scontrol drain (optional).
	Reason string `json:"reason,omitempty"`
}

// SlurmAdminCmdResult is the payload for the "ack" response to a slurm_admin_cmd.
type SlurmAdminCmdResult struct {
	OK       bool   `json:"ok"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	JobCount int    `json:"job_count,omitempty"` // for check_queue: number of running/pending jobs
}

// ─── Disk layout capture message types (#146) ────────────────────────────────

// DiskCaptureRequestPayload is the payload for the "disk_capture_request"
// server→node message.  The server sends this when an operator calls
// POST /api/v1/disk-layouts/capture/{node_id}.  The node replies with
// "disk_capture_result" carrying the serialised api.DiskLayout JSON.
type DiskCaptureRequestPayload struct {
	// RefMsgID is the server's msg_id; echoed in the disk_capture_result reply.
	RefMsgID string `json:"ref_msg_id"`
}

// DiskCaptureResultPayload is the payload for the "disk_capture_result"
// node→server message.
type DiskCaptureResultPayload struct {
	// RefMsgID echoes the msg_id from the corresponding disk_capture_request.
	RefMsgID string `json:"ref_msg_id"`
	// LayoutJSON is the JSON-serialised api.DiskLayout captured on the node.
	// Empty when Error is set.
	LayoutJSON string `json:"layout_json,omitempty"`
	// Error is a human-readable failure description; empty on success.
	Error string `json:"error,omitempty"`
}

// ─── Console message types (#128) ────────────────────────────────────────────
// These message types are defined here for future use when the console broker
// is routed through the clientd WebSocket (Sprint 24 in-browser console). In
// the current implementation (Sprint 21) the console handler opens the upstream
// (ipmitool SOL or SSH PTY) directly server-side; the clientd WS is not used
// for console I/O.

// ConsoleRequestPayload is the payload for the "console_request" server→node
// message. Reserved for Sprint 24 when the in-browser console UI is added and
// the console session is brokered through the clientd WebSocket rather than
// a direct server→BMC/SSH connection.
type ConsoleRequestPayload struct {
	// SessionID is the operator-facing session identifier for correlation.
	SessionID string `json:"session_id"`
	// Mode is "ipmi-sol" or "ssh".
	Mode string `json:"mode"`
}

// ConsoleDataPayload is the payload for the "console_data" message exchanged
// during an active console session (both directions). Data is raw terminal
// bytes — not base64-encoded because the clientd WebSocket transport handles
// text frames; the broker transcodes as needed.
type ConsoleDataPayload struct {
	// SessionID correlates the data to an active console session.
	SessionID string `json:"session_id"`
	// Data is the raw terminal byte sequence.
	Data string `json:"data"`
}

// ExecRequestPayload is the payload for the "exec_request" server→node message.
// The server sends this to request execution of a whitelisted diagnostic command.
type ExecRequestPayload struct {
	// RefMsgID is the msg_id of this server message, echoed in the exec_result reply
	// so the waiting HTTP handler can correlate the response.
	RefMsgID string   `json:"ref_msg_id"`
	Command  string   `json:"command"`
	Args     []string `json:"args"`
}

// ExecResultPayload is the payload for the "exec_result" client→server message.
// The node sends this after executing (or refusing) an exec_request.
type ExecResultPayload struct {
	RefMsgID  string `json:"ref_msg_id"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

// OperatorExecRequestPayload is the payload for the "operator_exec_request" server→node message.
// Unlike exec_request (which enforces a whitelist for diagnostic commands), this message type
// runs an arbitrary command on behalf of an authenticated admin/operator. The server sends
// this ONLY after verifying that the requesting API key has admin or operator scope.
// There is NO argument whitelist — the operator takes full responsibility for the command.
//
// Commands are still run without a shell (exec.Command, not /bin/sh -c) for safety.
type OperatorExecRequestPayload struct {
	// RefMsgID is the server's msg_id; echoed in the operator_exec_result reply for correlation.
	RefMsgID string `json:"ref_msg_id"`
	// Command is the binary to execute (must be an absolute path or a PATH-resolvable name).
	Command string `json:"command"`
	// Args is the argument list (no shell expansion; no shell is invoked).
	Args []string `json:"args"`
	// TimeoutSec is the execution timeout in seconds. 0 means use the default (60s).
	// The server hard-caps this at 3600s (1h).
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// OperatorExecResultPayload is the payload for the "operator_exec_result" client→server message.
// The node sends this after completing (or failing) an operator_exec_request.
type OperatorExecResultPayload struct {
	RefMsgID  string `json:"ref_msg_id"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

// SlurmConfigAckPayload is the payload for the "ack" message sent after a slurm_config_push.
// It carries per-file and per-script results and the apply action result.
// The outer ClientMessage type is "ack" and the RefMsgID identifies the push message.
type SlurmConfigAckPayload struct {
	// PushOpID is the push operation UUID from the original SlurmConfigPushPayload.
	PushOpID string `json:"push_op_id"`
	// OK is true when all files/scripts were written and the apply action succeeded.
	OK bool `json:"ok"`
	// Error is a human-readable summary of the failure, if any.
	Error string `json:"error,omitempty"`
	// FileResults holds per-file write outcomes.
	FileResults []SlurmFileApplyResult `json:"file_results"`
	// ScriptResults holds per-script write outcomes.
	ScriptResults []SlurmScriptApplyResult `json:"script_results,omitempty"`
	// ApplyOutput is the stdout/stderr of the apply action command (truncated to 2 KB).
	ApplyOutput string `json:"apply_output,omitempty"`
	// ApplyExitCode is the exit code of the apply action command.
	ApplyExitCode int `json:"apply_exit_code"`
}

// SlurmScriptApplyResult is the per-script result within a SlurmConfigAckPayload.
type SlurmScriptApplyResult struct {
	ScriptType string `json:"script_type"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// SlurmFileApplyResult is the per-file result within a SlurmConfigAckPayload.
type SlurmFileApplyResult struct {
	Filename string `json:"filename"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// ─── BIOS settings message types (#159) ──────────────────────────────────────

// BiosReadRequestPayload is the payload for the "bios_read_request"
// server→node message.  The server sends this to request a snapshot of current
// BIOS settings from a running node via clustr-clientd.  The node replies with
// "bios_read_result" carrying the raw settings.
//
// Used by: the periodic drift check; GET /api/v1/nodes/{id}/bios/current.
type BiosReadRequestPayload struct {
	// RefMsgID is the server's msg_id; echoed in the bios_read_result reply.
	RefMsgID string `json:"ref_msg_id"`
	// Vendor is the provider to use ("intel").
	Vendor string `json:"vendor"`
}

// BiosReadResultPayload is the payload for the "bios_read_result"
// node→server message.  The node sends this after reading current BIOS
// settings via the vendor binary (or reporting an error).
type BiosReadResultPayload struct {
	// RefMsgID echoes the msg_id from the corresponding bios_read_request.
	RefMsgID string `json:"ref_msg_id"`
	// Vendor identifies the provider that produced these settings.
	Vendor string `json:"vendor"`
	// Settings is the flat list of current BIOS settings on the node.
	// Empty (not nil) on error.
	Settings []BiosSetting `json:"settings"`
	// Error is a human-readable failure description; empty on success.
	Error string `json:"error,omitempty"`
}

// BiosApplyRequestPayload is the payload for the "bios_apply_request"
// server→node message.  This is the POST-boot apply path (future; not enabled
// in v1 — BIOS apply runs in initramfs per D1 of the Sprint 25 design).
// The message handler exists so the post-boot path can be enabled without a
// protocol change.  The node replies with "bios_apply_result".
type BiosApplyRequestPayload struct {
	// RefMsgID is the server's msg_id; echoed in the bios_apply_result reply.
	RefMsgID string `json:"ref_msg_id"`
	// Vendor is the provider to use ("intel").
	Vendor string `json:"vendor"`
	// SettingsJSON is the raw profile settings JSON object to apply.
	// The node writes this to /var/lib/clustr/bios-staging/<id>.json and
	// passes the path to the privhelper bios-apply verb.
	SettingsJSON string `json:"settings_json"`
	// ProfileID is the bios_profiles.id of the source profile for audit.
	ProfileID string `json:"profile_id"`
}

// BiosApplyResultPayload is the payload for the "bios_apply_result"
// node→server message.  The node sends this after applying (or failing to
// apply) a bios_apply_request.
type BiosApplyResultPayload struct {
	// RefMsgID echoes the msg_id from the corresponding bios_apply_request.
	RefMsgID string `json:"ref_msg_id"`
	// ProfileID echoes the profile_id from the request for audit correlation.
	ProfileID string `json:"profile_id"`
	// OK is true when the apply completed without error.
	OK bool `json:"ok"`
	// AppliedCount is the number of settings that were changed.
	// 0 is valid when the node was already at the desired state.
	AppliedCount int `json:"applied_count"`
	// Error is a human-readable failure description; empty on success.
	Error string `json:"error,omitempty"`
}

// BiosDriftPayload is the payload for the "bios_drift" node→server message.
// The node sends this when the periodic drift check (every 24h) detects that
// current BIOS settings diverge from the applied_settings_hash recorded at the
// last successful apply.
//
// Drift is REPORTED only — clustr never auto-corrects BIOS drift.
// The operator must trigger a re-apply via `clustr bios apply`.
type BiosDriftPayload struct {
	// NodeID is the node's UUID (redundant with the WebSocket session but
	// included for easy log correlation when payload is stored standalone).
	NodeID string `json:"node_id"`
	// Vendor identifies the provider that detected drift.
	Vendor string `json:"vendor"`
	// ProfileID is the bios_profiles.id of the assigned profile.
	ProfileID string `json:"profile_id"`
	// ExpectedHash is the applied_settings_hash stored in node_bios_profile.
	ExpectedHash string `json:"expected_hash"`
	// ActualHash is sha256(current_settings_json) computed on the node.
	ActualHash string `json:"actual_hash"`
	// DetectedAt is the wall-clock time the drift was detected on the node.
	DetectedAt time.Time `json:"detected_at"`
	// DriftedSettings contains the settings that differ (name + current value).
	// Populated on a best-effort basis; may be empty if the diff was computed
	// only via hash comparison.
	DriftedSettings []BiosSetting `json:"drifted_settings,omitempty"`
}

// BiosSetting is a single BIOS key/value pair in the clientd message wire format.
// Mirrors internal/bios.Setting for cross-package serialisation — defined here
// so the server can import it without creating an import cycle.
type BiosSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
