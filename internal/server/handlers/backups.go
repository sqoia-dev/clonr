package handlers

// backups.go — Sprint 41 Day 4
//
// Implements the plugin backup list and restore endpoints.
//
//   GET  /api/v1/backups?node_id=<>&plugin=<>
//     List backup snapshots for a node/plugin pair, newest first.
//     Auth: requires backup.list verb.
//
//   POST /api/v1/backups/{id}/restore
//     Initiate a restore: instructs the node to run
//     clustr-privhelper backup-restore for the named tarball.
//     Returns 202 with a job-id. Auth: requires backup.restore verb.
//
//   GET  /api/v1/backups/{id}/restore-status?job_id=<>
//     Poll job state. Returns current status.
//
// Design: docs/design/sprint-41-auth-safety.md §5.3 and §7.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// restoreJobTTL is how long a completed restore job stays visible via the
// status endpoint. After this window the job is garbage-collected from the
// in-memory store.
const restoreJobTTL = 30 * time.Minute

// RestoreJobState tracks the lifecycle of an asynchronous restore operation.
type RestoreJobState struct {
	ID        string
	BackupID  string
	NodeID    string
	Plugin    string
	Status    string // "pending", "running", "done", "failed"
	Error     string
	StartedAt time.Time
	DoneAt    *time.Time
}

// BackupDBIface is the database subset required by BackupsHandler.
type BackupDBIface interface {
	ListPluginBackups(ctx context.Context, nodeID, pluginName string) ([]db.PluginBackup, error)
	GetPluginBackup(ctx context.Context, id string) (*db.PluginBackup, error)
	GetPluginBackupByPendingPush(ctx context.Context, pendingPushID string) (*db.PluginBackup, error)
}

// BackupsHandler handles the plugin backup list and restore endpoints.
type BackupsHandler struct {
	DB           BackupDBIface
	Hub          ClientdHubIface
	Audit        *db.AuditService
	GetActorInfo func(r *http.Request) (string, string)

	// restoreMu guards jobs.
	restoreMu sync.Mutex
	// jobs is a short-lived in-memory job store. Keyed by job ID.
	jobs map[string]*RestoreJobState
}

// NewBackupsHandler creates a BackupsHandler with an initialised job store.
func NewBackupsHandler(database BackupDBIface, hub ClientdHubIface, audit *db.AuditService, getActorInfo func(*http.Request) (string, string)) *BackupsHandler {
	return &BackupsHandler{
		DB:           database,
		Hub:          hub,
		Audit:        audit,
		GetActorInfo: getActorInfo,
		jobs:         make(map[string]*RestoreJobState),
	}
}

// ── wire types ────────────────────────────────────────────────────────────────

type backupListItem struct {
	ID                     string  `json:"id"`
	NodeID                 string  `json:"node_id"`
	PluginName             string  `json:"plugin_name"`
	BlobPath               string  `json:"blob_path"`
	TakenAt                string  `json:"taken_at"` // RFC3339
	PendingDangerousPushID *string `json:"pending_dangerous_push_id,omitempty"`
}

type backupListResponse struct {
	Backups []backupListItem `json:"backups"`
	Total   int              `json:"total"`
}

type restoreInitiateResponse struct {
	JobID    string `json:"job_id"`
	BackupID string `json:"backup_id"`
	NodeID   string `json:"node_id"`
	Plugin   string `json:"plugin"`
	Status   string `json:"status"`
}

type restoreStatusResponse struct {
	JobID     string  `json:"job_id"`
	BackupID  string  `json:"backup_id"`
	NodeID    string  `json:"node_id"`
	Plugin    string  `json:"plugin"`
	Status    string  `json:"status"`
	Error     *string `json:"error,omitempty"`
	StartedAt string  `json:"started_at"`
	DoneAt    *string `json:"done_at,omitempty"`
}

// ── handlers ─────────────────────────────────────────────────────────────────

// HandleList handles GET /api/v1/backups?node_id=<>&plugin=<>&pending_id=<>.
//
// When pending_id is supplied, the response contains the single backup tied
// to that dangerous-push confirmation (used by `clustr restore replay --pending-id`).
// When node_id / plugin are supplied, the response contains all matching backups.
func (h *BackupsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	pluginName := r.URL.Query().Get("plugin")
	pendingID := r.URL.Query().Get("pending_id")

	var rows []db.PluginBackup

	if pendingID != "" {
		// Look up the single backup tied to this dangerous-push confirmation.
		b, err := h.DB.GetPluginBackupByPendingPush(r.Context(), pendingID)
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, backupListResponse{Backups: []backupListItem{}, Total: 0})
			return
		}
		if err != nil {
			log.Error().Err(err).Str("pending_id", pendingID).
				Msg("backups: get by pending push failed")
			writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
				Error: "internal server error",
				Code:  "internal_error",
			})
			return
		}
		rows = []db.PluginBackup{*b}
	} else {
		var err error
		rows, err = h.DB.ListPluginBackups(r.Context(), nodeID, pluginName)
		if err != nil {
			log.Error().Err(err).Str("node_id", nodeID).Str("plugin", pluginName).
				Msg("backups: list failed")
			writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
				Error: "internal server error",
				Code:  "internal_error",
			})
			return
		}
	}

	items := make([]backupListItem, 0, len(rows))
	for _, b := range rows {
		item := backupListItem{
			ID:         b.ID,
			NodeID:     b.NodeID,
			PluginName: b.PluginName,
			BlobPath:   b.BlobPath,
			TakenAt:    b.TakenAt.Format(time.RFC3339),
		}
		if b.PendingDangerousPushID != "" {
			s := b.PendingDangerousPushID
			item.PendingDangerousPushID = &s
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, backupListResponse{
		Backups: items,
		Total:   len(items),
	})
}

// HandleRestore handles POST /api/v1/backups/{id}/restore.
// Initiates an async restore and returns 202 with a job-id.
func (h *BackupsHandler) HandleRestore(w http.ResponseWriter, r *http.Request) {
	backupID := chi.URLParam(r, "id")
	if backupID == "" {
		writeValidationError(w, "missing backup id")
		return
	}

	backup, err := h.DB.GetPluginBackup(r.Context(), backupID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{
			Error: "backup not found",
			Code:  "not_found",
		})
		return
	}
	if err != nil {
		log.Error().Err(err).Str("backup_id", backupID).Msg("backups: get backup failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "internal server error",
			Code:  "internal_error",
		})
		return
	}

	jobID := "rj-" + uuid.New().String()
	job := &RestoreJobState{
		ID:        jobID,
		BackupID:  backupID,
		NodeID:    backup.NodeID,
		Plugin:    backup.PluginName,
		Status:    "pending",
		StartedAt: time.Now().UTC(),
	}

	h.restoreMu.Lock()
	h.jobs[jobID] = job
	h.restoreMu.Unlock()

	// Launch the restore asynchronously so we can return 202 immediately.
	go h.runRestore(backup, job)

	// Audit the restore initiation.
	actorID, actorLabel := h.GetActorInfo(r)
	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			db.AuditActionConfigRestore,
			"node", backup.NodeID,
			r.RemoteAddr,
			map[string]string{
				"plugin":    backup.PluginName,
				"backup_id": backupID,
				"job_id":    jobID,
				"blob_path": backup.BlobPath,
			},
			nil,
		)
	}

	log.Info().
		Str("job_id", jobID).
		Str("backup_id", backupID).
		Str("node_id", backup.NodeID).
		Str("plugin", backup.PluginName).
		Str("actor", actorLabel).
		Msg("backups: restore initiated")

	writeJSON(w, http.StatusAccepted, restoreInitiateResponse{
		JobID:    jobID,
		BackupID: backupID,
		NodeID:   backup.NodeID,
		Plugin:   backup.PluginName,
		Status:   "pending",
	})
}

// HandleRestoreStatus handles GET /api/v1/backups/{id}/restore-status?job_id=<>.
func (h *BackupsHandler) HandleRestoreStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		writeValidationError(w, "job_id query parameter is required")
		return
	}

	h.restoreMu.Lock()
	job, ok := h.jobs[jobID]
	h.restoreMu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{
			Error: "restore job not found (may have expired)",
			Code:  "not_found",
		})
		return
	}

	resp := restoreStatusResponse{
		JobID:     job.ID,
		BackupID:  job.BackupID,
		NodeID:    job.NodeID,
		Plugin:    job.Plugin,
		Status:    job.Status,
		StartedAt: job.StartedAt.Format(time.RFC3339),
	}
	if job.Error != "" {
		resp.Error = &job.Error
	}
	if job.DoneAt != nil {
		s := job.DoneAt.Format(time.RFC3339)
		resp.DoneAt = &s
	}

	writeJSON(w, http.StatusOK, resp)
}

// runRestore sends a backup-restore operator_exec_request to the node and
// waits for the result, updating job state throughout.
func (h *BackupsHandler) runRestore(backup *db.PluginBackup, job *RestoreJobState) {
	h.setJobStatus(job.ID, "running", "")

	msgID := uuid.New().String()

	// We send an operator_exec_request asking the node to run
	// /usr/sbin/clustr-privhelper backup-restore ... . The privhelper is the
	// single privilege boundary; clientd's operator_exec path runs the command
	// as the clustr user, which can call the setuid privhelper.
	payload := clientd.OperatorExecRequestPayload{
		RefMsgID: msgID,
		Command:  "/usr/sbin/clustr-privhelper",
		Args: []string{
			"backup-restore",
			"--tarball", backup.BlobPath,
			"--node-id", backup.NodeID,
			"--plugin", backup.PluginName,
		},
		TimeoutSec: 120,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		h.setJobStatus(job.ID, "failed", fmt.Sprintf("marshal payload: %v", err))
		return
	}

	msg := clientd.ServerMessage{
		Type:    "operator_exec_request",
		MsgID:   msgID,
		Payload: json.RawMessage(payloadBytes),
	}

	// Register to receive the exec result.
	resultCh := h.Hub.RegisterOperatorExec(msgID)
	defer h.Hub.UnregisterOperatorExec(msgID)

	if err := h.Hub.Send(backup.NodeID, msg); err != nil {
		h.setJobStatus(job.ID, "failed", fmt.Sprintf("node not connected: %v", err))
		return
	}

	// Wait for result (timeout hardcoded at 3 minutes — privhelper timeout is 2 minutes).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	select {
	case result := <-resultCh:
		if result.ExitCode != 0 || result.Error != "" {
			errMsg := result.Error
			if errMsg == "" {
				errMsg = fmt.Sprintf("privhelper exited %d: %s", result.ExitCode, result.Stderr)
			}
			h.setJobStatus(job.ID, "failed", errMsg)
			return
		}
		h.setJobStatus(job.ID, "done", "")

	case <-ctx.Done():
		h.setJobStatus(job.ID, "failed", "restore timed out after 3 minutes")
	}
}

// setJobStatus updates job status and optionally records an error.
func (h *BackupsHandler) setJobStatus(jobID, status, errMsg string) {
	h.restoreMu.Lock()
	defer h.restoreMu.Unlock()
	job, ok := h.jobs[jobID]
	if !ok {
		return
	}
	job.Status = status
	job.Error = errMsg
	if status == "done" || status == "failed" {
		now := time.Now().UTC()
		job.DoneAt = &now
		// Schedule TTL expiry.
		go func() {
			time.Sleep(restoreJobTTL)
			h.restoreMu.Lock()
			delete(h.jobs, jobID)
			h.restoreMu.Unlock()
		}()
	}
}

