// Package clientd provides the shared message types and WebSocket client
// for the clonr-clientd daemon.
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
// The server sends this to push one or more Slurm config files to the node atomically.
type SlurmConfigPushPayload struct {
	// PushOpID is the push operation UUID (for ack correlation and state updates).
	PushOpID string `json:"push_op_id"`
	// Files is the list of config files to write. Each file has content + checksum.
	Files []SlurmFilePush `json:"files"`
	// ApplyAction is "reconfigure" (scontrol reconfigure) or "restart" (systemctl restart slurmd).
	ApplyAction string `json:"apply_action"`
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

// SlurmConfigAckPayload is the payload for the "ack" message sent after a slurm_config_push.
// It carries per-file results and the apply action result.
// The outer ClientMessage type is "ack" and the RefMsgID identifies the push message.
type SlurmConfigAckPayload struct {
	// PushOpID is the push operation UUID from the original SlurmConfigPushPayload.
	PushOpID string `json:"push_op_id"`
	// OK is true when all files were written and the apply action succeeded.
	OK bool `json:"ok"`
	// Error is a human-readable summary of the failure, if any.
	Error string `json:"error,omitempty"`
	// FileResults holds per-file write outcomes.
	FileResults []SlurmFileApplyResult `json:"file_results"`
	// ApplyOutput is the stdout/stderr of the apply action command (truncated to 2 KB).
	ApplyOutput string `json:"apply_output,omitempty"`
	// ApplyExitCode is the exit code of the apply action command.
	ApplyExitCode int `json:"apply_exit_code"`
}

// SlurmFileApplyResult is the per-file result within a SlurmConfigAckPayload.
type SlurmFileApplyResult struct {
	Filename string `json:"filename"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

