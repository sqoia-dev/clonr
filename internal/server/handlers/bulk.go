// Package handlers — bulk.go implements the multi-select bulk-action endpoints
// (Sprint 44 BULK-MULTISELECT-POWER and BULK-MULTISELECT-ACTIONS).
//
// All bulk endpoints share the same request/response shape:
//
//	POST /api/v1/nodes/bulk/power/{action}   { node_ids: [...] }
//	POST /api/v1/nodes/bulk/reimage          { node_ids: [...], image_id?, force? }
//	POST /api/v1/nodes/bulk/drain            { node_ids: [...], reason? }
//	POST /api/v1/nodes/bulk/netboot          { node_ids: [...] }
//	POST /api/v1/nodes/bulk/exec             { node_ids: [...], command, args?, timeout_sec? }
//
//	200 → { "results": [ { "node_id": "...", "ok": true } | { "node_id": "...", "ok": false, "error": "..." }, ... ] }
//
// Concurrency is bounded by a per-request worker pool (default 16, override via
// CLUSTR_BULK_CONCURRENCY env var, hard cap 64) so that fanning out to a 100-node
// selection doesn't open 100 simultaneous IPMI sessions to a single rack BMC.
//
// Partial failure is the norm: one unreachable BMC must not fail the batch. Each
// per-node error is captured in the result object; the HTTP status is always 200
// when the request body parsed correctly.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/power"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// BulkDB is the minimal DB surface the bulk handlers need. Defined as an
// interface so tests can substitute fakeInterfacesDB without spinning up a real
// SQLite database. Production callers pass *db.DB which already satisfies this.
type BulkDB interface {
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
}

// ─── Wire types ───────────────────────────────────────────────────────────────

// BulkRequest is the common envelope for all bulk endpoints. Action-specific
// fields are added per-handler via embedded structs (see bulkExecRequest).
type BulkRequest struct {
	NodeIDs []string `json:"node_ids"`
}

// BulkNodeResult is one row in the response.results array.
type BulkNodeResult struct {
	NodeID string `json:"node_id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	// Optional diagnostic — not all callers populate. e.g. exit_code for exec,
	// reimage_id for reimage. Present only when relevant.
	Detail map[string]any `json:"detail,omitempty"`
}

// BulkResponse is the response body shape for every bulk endpoint.
type BulkResponse struct {
	Results []BulkNodeResult `json:"results"`
}

// ─── Concurrency cap ──────────────────────────────────────────────────────────

// bulkDefaultConcurrency is the default per-request worker count. Tuned to be
// well under the typical max-sessions cap on enterprise BMCs (Supermicro X11:
// 6 sessions; HPE iLO 5: 13 sessions) when distributed across a rack of nodes,
// while still parallelising enough to make a 32-node action complete in seconds
// rather than minutes.
const bulkDefaultConcurrency = 16

// bulkMaxConcurrency is the hard upper bound regardless of env var.
const bulkMaxConcurrency = 64

// bulkConcurrency reads CLUSTR_BULK_CONCURRENCY and clamps to [1, bulkMaxConcurrency].
func bulkConcurrency() int {
	if v := os.Getenv("CLUSTR_BULK_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > bulkMaxConcurrency {
				return bulkMaxConcurrency
			}
			return n
		}
	}
	return bulkDefaultConcurrency
}

// ─── Handler ──────────────────────────────────────────────────────────────────

// BulkPowerCallable is the per-node callback signature used by fanOut. It
// returns nil on success or a non-nil error that gets surfaced to the client.
type BulkPowerCallable func(ctx context.Context, nodeID string) (detail map[string]any, err error)

// BulkHandler serves the /api/v1/nodes/bulk/* family.
type BulkHandler struct {
	DB       BulkDB
	Registry *power.Registry
	Cache    PowerCache
	// Reimage, when non-nil, is used by HandleReimage. Decoupled so tests can
	// drop in a stub without wiring a full Orchestrator.
	Reimage BulkReimageRunner
	// Exec, when non-nil, is used by HandleExec.
	Exec BulkExecRunner
	// ProviderFactory, when non-nil, is used in place of Registry to build a
	// power.Provider for a given NodeConfig. Tests inject a stub here so they
	// don't have to register fake provider types in the global registry.
	ProviderFactory func(cfg api.NodeConfig) (power.Provider, error)
}

// BulkReimageRunner triggers a reimage for one node. Returns the new reimage
// request ID on success.
type BulkReimageRunner interface {
	StartReimage(ctx context.Context, nodeID, imageID string, force bool, requestedBy string) (reimageID string, err error)
}

// BulkExecRunner runs a single command on one node.
type BulkExecRunner interface {
	ExecOne(ctx context.Context, nodeID, command string, args []string, timeoutSec int) (exitCode int, output string, err error)
}

// ─── Concurrency-bounded fan-out ──────────────────────────────────────────────

// fanOut runs fn for every nodeID with at most `limit` concurrent calls.
// Per-node errors are captured into the BulkResponse.results array, never
// raised — the batch always returns a per-node result regardless of partial
// failure. Order of nodeIDs is preserved in the output.
func fanOut(ctx context.Context, nodeIDs []string, limit int, fn BulkPowerCallable) []BulkNodeResult {
	if limit <= 0 {
		limit = bulkDefaultConcurrency
	}

	results := make([]BulkNodeResult, len(nodeIDs))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, nid := range nodeIDs {
		i, nid := i, nid
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			row := BulkNodeResult{NodeID: nid}
			detail, err := fn(ctx, nid)
			if err != nil {
				row.OK = false
				row.Error = err.Error()
			} else {
				row.OK = true
				row.Detail = detail
			}
			results[i] = row
		}()
	}
	wg.Wait()
	return results
}

// dedupeNodeIDs returns a new slice with empty strings filtered and exact
// duplicates collapsed (keeping first occurrence). Same-content duplicate
// payloads from over-eager UI multi-select would otherwise yield two power
// commands per BMC, breaking the partial-failure contract.
func dedupeNodeIDs(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ─── POST /api/v1/nodes/bulk/power/{action} ───────────────────────────────────

// HandleBulkPower implements POST /api/v1/nodes/bulk/power/{action}.
// action ∈ {on, off, cycle, reset, soft-off}.
func (h *BulkHandler) HandleBulkPower(w http.ResponseWriter, r *http.Request) {
	action := chi.URLParam(r, "action")
	switch action {
	case "on", "off", "cycle", "reset", "soft-off":
		// ok
	default:
		writeValidationError(w, fmt.Sprintf("invalid power action %q (valid: on, off, cycle, reset, soft-off)", action))
		return
	}

	var req BulkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	ids := dedupeNodeIDs(req.NodeIDs)
	if len(ids) == 0 {
		writeValidationError(w, "node_ids is required and must be non-empty")
		return
	}

	results := fanOut(r.Context(), ids, bulkConcurrency(), func(ctx context.Context, nodeID string) (map[string]any, error) {
		return nil, h.runPowerAction(ctx, nodeID, action)
	})

	if h.Cache != nil {
		for _, res := range results {
			if res.OK {
				h.Cache.Invalidate(res.NodeID)
			}
		}
	}

	log.Info().Str("action", action).Int("targets", len(ids)).
		Int("ok", countOK(results)).Msg("bulk power: complete")
	writeJSON(w, http.StatusOK, BulkResponse{Results: results})
}

// runPowerAction is the per-node body of HandleBulkPower. Reuses Sprint 34's
// internal/ipmi via the power.Provider abstraction so the same fallback rules
// (PowerProvider config or legacy BMC) apply.
func (h *BulkHandler) runPowerAction(ctx context.Context, nodeID, action string) error {
	cfg, err := h.DB.GetNodeConfig(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("load node: %w", err)
	}

	prov, err := h.providerForNode(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, bmcTimeout)
	defer cancel()

	switch action {
	case "on":
		return prov.PowerOn(ctx)
	case "off":
		return prov.PowerOff(ctx)
	case "cycle":
		return prov.PowerCycle(ctx)
	case "reset":
		return prov.Reset(ctx)
	case "soft-off":
		// soft-off is "ACPI shutdown" — most providers don't expose a
		// separate verb, fall through to PowerOff which freeipmi maps to
		// soft. legacyIPMIAdapter -> ipmi.Client.PowerOff -> 'chassis power
		// soft' under our wrapper.
		return prov.PowerOff(ctx)
	}
	return fmt.Errorf("unsupported action %q", action)
}

// providerForNode mirrors IPMIHandler.nodeProvider but without the ResponseWriter
// dependency so it can be called from a fan-out worker.
func (h *BulkHandler) providerForNode(cfg api.NodeConfig) (power.Provider, error) {
	if h.ProviderFactory != nil {
		return h.ProviderFactory(cfg)
	}
	if cfg.PowerProvider != nil && cfg.PowerProvider.Type != "" {
		if h.Registry == nil {
			return nil, fmt.Errorf("power registry not initialised")
		}
		return h.Registry.Create(power.ProviderConfig{
			Type:   cfg.PowerProvider.Type,
			Fields: cfg.PowerProvider.Fields,
		})
	}
	if cfg.BMC == nil || cfg.BMC.IPAddress == "" {
		return nil, fmt.Errorf("no power provider configured for this node")
	}
	if h.Registry == nil {
		return nil, fmt.Errorf("power registry not initialised")
	}
	return h.Registry.Create(power.ProviderConfig{
		Type: "ipmi",
		Fields: map[string]string{
			"host":     cfg.BMC.IPAddress,
			"username": cfg.BMC.Username,
			"password": cfg.BMC.Password,
		},
	})
}

// ─── POST /api/v1/nodes/bulk/reimage ──────────────────────────────────────────

// bulkReimageRequest extends BulkRequest with reimage-specific fields.
type bulkReimageRequest struct {
	NodeIDs []string `json:"node_ids"`
	ImageID string   `json:"image_id,omitempty"`
	Force   bool     `json:"force,omitempty"`
}

// HandleBulkReimage triggers a reimage on every selected node.
func (h *BulkHandler) HandleBulkReimage(w http.ResponseWriter, r *http.Request) {
	var req bulkReimageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	ids := dedupeNodeIDs(req.NodeIDs)
	if len(ids) == 0 {
		writeValidationError(w, "node_ids is required and must be non-empty")
		return
	}
	if h.Reimage == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.ErrorResponse{
			Error: "reimage runner not configured",
			Code:  "not_implemented",
		})
		return
	}

	requestedBy := actorLabelFromRequest(r)
	results := fanOut(r.Context(), ids, bulkConcurrency(), func(ctx context.Context, nodeID string) (map[string]any, error) {
		rid, err := h.Reimage.StartReimage(ctx, nodeID, req.ImageID, req.Force, requestedBy)
		if err != nil {
			return nil, err
		}
		return map[string]any{"reimage_id": rid}, nil
	})

	log.Info().Int("targets", len(ids)).Int("ok", countOK(results)).Msg("bulk reimage: complete")
	writeJSON(w, http.StatusOK, BulkResponse{Results: results})
}

// ─── POST /api/v1/nodes/bulk/drain ────────────────────────────────────────────

// bulkDrainRequest carries an optional Slurm drain reason.
type bulkDrainRequest struct {
	NodeIDs []string `json:"node_ids"`
	// Reason is the string passed to `scontrol update nodename=<name>
	// state=DRAIN reason=<reason>`. Defaults to "clustr-bulk-drain" when
	// empty.
	Reason string `json:"reason,omitempty"`
}

// HandleBulkDrain runs `scontrol update nodename=<name> state=DRAIN
// reason=<reason>` on the head node for each selected node. The Slurm head
// must be reachable to the clustr server via the head-exec hook (configured
// elsewhere). When Exec is nil, this returns 503 — operators get a clear
// "drain not wired" signal rather than a silent no-op.
//
// Implementation note: this calls h.Exec.ExecOne with command="scontrol" so a
// future #220 group-drain RPC can swap the runner without touching this
// endpoint's wire shape.
func (h *BulkHandler) HandleBulkDrain(w http.ResponseWriter, r *http.Request) {
	var req bulkDrainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	ids := dedupeNodeIDs(req.NodeIDs)
	if len(ids) == 0 {
		writeValidationError(w, "node_ids is required and must be non-empty")
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = "clustr-bulk-drain"
	}

	if h.Exec == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.ErrorResponse{
			Error: "exec runner not configured (drain requires slurm-head-exec)",
			Code:  "not_implemented",
		})
		return
	}

	results := fanOut(r.Context(), ids, bulkConcurrency(), func(ctx context.Context, nodeID string) (map[string]any, error) {
		hostname, err := h.hostnameFor(ctx, nodeID)
		if err != nil {
			return nil, err
		}
		args := []string{"update", fmt.Sprintf("nodename=%s", hostname),
			"state=DRAIN", fmt.Sprintf("reason=%s", reason)}
		exit, out, runErr := h.Exec.ExecOne(ctx, nodeID, "scontrol", args, 30)
		if runErr != nil {
			return nil, runErr
		}
		if exit != 0 {
			return nil, fmt.Errorf("scontrol exit %d: %s", exit, strings.TrimSpace(out))
		}
		return map[string]any{"hostname": hostname}, nil
	})

	log.Info().Int("targets", len(ids)).Int("ok", countOK(results)).Msg("bulk drain: complete")
	writeJSON(w, http.StatusOK, BulkResponse{Results: results})
}

// ─── POST /api/v1/nodes/bulk/netboot ──────────────────────────────────────────

// HandleBulkNetboot sets next-boot=PXE on every selected node. Does not power
// the node off — operators typically chain this with bulk/power/cycle.
func (h *BulkHandler) HandleBulkNetboot(w http.ResponseWriter, r *http.Request) {
	var req BulkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	ids := dedupeNodeIDs(req.NodeIDs)
	if len(ids) == 0 {
		writeValidationError(w, "node_ids is required and must be non-empty")
		return
	}

	results := fanOut(r.Context(), ids, bulkConcurrency(), func(ctx context.Context, nodeID string) (map[string]any, error) {
		cfg, err := h.DB.GetNodeConfig(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("load node: %w", err)
		}
		prov, err := h.providerForNode(cfg)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, bmcTimeout)
		defer cancel()
		return nil, prov.SetNextBoot(ctx, power.BootPXE)
	})

	log.Info().Int("targets", len(ids)).Int("ok", countOK(results)).Msg("bulk netboot: complete")
	writeJSON(w, http.StatusOK, BulkResponse{Results: results})
}

// ─── POST /api/v1/nodes/bulk/exec ─────────────────────────────────────────────

type bulkExecRequest struct {
	NodeIDs    []string `json:"node_ids"`
	Command    string   `json:"command"`
	Args       []string `json:"args,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
}

// HandleBulkExec runs a single non-streaming command on every selected node.
// For SSE-streamed output use the existing /exec endpoint with a node-id
// selector — that endpoint's wire shape is incompatible with the bulk result
// shape so we keep them separate to avoid surprising callers.
func (h *BulkHandler) HandleBulkExec(w http.ResponseWriter, r *http.Request) {
	var req bulkExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	ids := dedupeNodeIDs(req.NodeIDs)
	if len(ids) == 0 {
		writeValidationError(w, "node_ids is required and must be non-empty")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeValidationError(w, "command is required")
		return
	}
	timeout := req.TimeoutSec
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 3600 {
		timeout = 3600
	}

	if h.Exec == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.ErrorResponse{
			Error: "exec runner not configured",
			Code:  "not_implemented",
		})
		return
	}

	results := fanOut(r.Context(), ids, bulkConcurrency(), func(ctx context.Context, nodeID string) (map[string]any, error) {
		exit, out, err := h.Exec.ExecOne(ctx, nodeID, req.Command, req.Args, timeout)
		if err != nil {
			return nil, err
		}
		detail := map[string]any{"exit_code": exit}
		if out != "" {
			detail["output"] = out
		}
		if exit != 0 {
			return detail, fmt.Errorf("exit %d", exit)
		}
		return detail, nil
	})

	log.Info().Str("command", req.Command).Int("targets", len(ids)).
		Int("ok", countOK(results)).Msg("bulk exec: complete")
	writeJSON(w, http.StatusOK, BulkResponse{Results: results})
}

// ─── shared helpers ───────────────────────────────────────────────────────────

func (h *BulkHandler) hostnameFor(ctx context.Context, nodeID string) (string, error) {
	cfg, err := h.DB.GetNodeConfig(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if cfg.Hostname == "" {
		return "", fmt.Errorf("node has no hostname")
	}
	return cfg.Hostname, nil
}

// actorLabelFromRequest pulls a human-readable identifier out of the request
// for audit attribution. Falls back to remote-addr when no auth context is
// present (e.g. unit tests without middleware).
func actorLabelFromRequest(r *http.Request) string {
	if v := r.Header.Get("X-Clustr-Actor"); v != "" {
		return v
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "bulk"
}

func countOK(results []BulkNodeResult) int {
	n := 0
	for _, r := range results {
		if r.OK {
			n++
		}
	}
	return n
}

