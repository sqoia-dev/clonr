// Package clientd provides the shared message types and WebSocket client
// for the clustr-clientd daemon.
package clientd

import "encoding/json"

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
	UptimeSeconds  float64       `json:"uptime_seconds"`
	Load1          float64       `json:"load_1"`
	Load5          float64       `json:"load_5"`
	Load15         float64       `json:"load_15"`
	MemTotalKB     int64         `json:"mem_total_kb"`
	MemAvailKB     int64         `json:"mem_avail_kb"`
	DiskUsage      []DiskUsage   `json:"disk_usage"`
	Services       []ServiceStatus `json:"services"`
	KernelVersion  string        `json:"kernel_version"`
	ClientdVersion string        `json:"clientd_version"`
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

