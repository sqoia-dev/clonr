package handlers

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/selector"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// CpHubIface is the hub interface required by CpHandler.
// It reuses the same operator exec infrastructure as ExecHandler.
type CpHubIface interface {
	IsConnected(nodeID string) bool
	Send(nodeID string, msg clientd.ServerMessage) error
	RegisterOperatorExec(msgID string) <-chan clientd.OperatorExecResultPayload
	UnregisterOperatorExec(msgID string)
}

// CpHandler implements POST /api/v1/cp — batch file copy across a node selector.
type CpHandler struct {
	DB  ExecDBIface
	Hub CpHubIface
}

// cpAPIRequest is the JSON body for POST /api/v1/cp.
type cpAPIRequest struct {
	// Selector fields — mirrors SelectorSet.
	Nodes        string `json:"nodes,omitempty"`
	Group        string `json:"group,omitempty"`
	All          bool   `json:"all,omitempty"`
	Active       bool   `json:"active,omitempty"`
	Racks        string `json:"racks,omitempty"`
	Chassis      string `json:"chassis,omitempty"`
	IgnoreStatus bool   `json:"ignore_status,omitempty"`

	// SrcPath is the source path on the server (file or directory to push).
	// Must be an absolute path readable by the clustr-serverd process.
	SrcPath string `json:"src_path"`

	// DstPath is the destination directory on the target nodes.
	DstPath string `json:"dst_path"`

	// Recursive, when true, copies a directory tree recursively.
	Recursive bool `json:"recursive,omitempty"`

	// Preserve, when true, preserves mode + owner + timestamps.
	Preserve bool `json:"preserve,omitempty"`

	// IncludeSelf is reserved for future use (controller as target).
	// In v1 the server does not push to itself via the cp path.
	IncludeSelf bool `json:"include_self,omitempty"`

	// Parallel is the max concurrent target nodes. Default 8, max 64.
	Parallel int `json:"parallel,omitempty"`
}

// cpNodeResult is the per-node result returned in the summary SSE event.
type cpNodeResult struct {
	NodeID   string `json:"node_id"`
	Hostname string `json:"hostname"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// cpSummaryEvent is the final SSE event for a cp operation.
type cpSummaryEvent struct {
	Type    string         `json:"type"` // "summary"
	Results []cpNodeResult `json:"results"`
	MaxExit int            `json:"max_exit_code"`
}

// cpTransferSizeLimitBytes is the maximum tarball size accepted for in-memory transfer.
// v1 design: tarballs are base64-encoded and passed as shell arguments.
// This limits the total uncompressed source size to 32 MB.
const cpTransferSizeLimitBytes = 32 * 1024 * 1024

// HandleCp handles POST /api/v1/cp.
// The server tars the source path, base64-encodes the tarball, and delivers it to
// each target node via an operator_exec_request that pipes through tar -xf on the node.
//
// v1 constraints:
//   - One-shot push (no rsync delta).
//   - Source must be readable by clustr-serverd.
//   - Max uncompressed source size: 32 MB (base64 + shell command fits in memory).
//   - Fail-isolated: a node failure does not stop delivery to other nodes.
func (h *CpHandler) HandleCp(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeValidationError(w, "failed to read request body")
		return
	}

	var req cpAPIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.SrcPath == "" {
		writeValidationError(w, "src_path is required")
		return
	}
	if req.DstPath == "" {
		writeValidationError(w, "dst_path is required")
		return
	}

	// Reject path traversal and relative paths.
	if !filepath.IsAbs(req.SrcPath) {
		writeValidationError(w, "src_path must be an absolute path")
		return
	}
	cleanSrc := filepath.Clean(req.SrcPath)

	parallel := req.Parallel
	if parallel <= 0 {
		parallel = 8
	}
	if parallel > 64 {
		parallel = 64
	}

	set := selector.SelectorSet{
		Nodes:        req.Nodes,
		Group:        req.Group,
		All:          req.All,
		Active:       req.Active,
		Racks:        req.Racks,
		Chassis:      req.Chassis,
		IgnoreStatus: req.IgnoreStatus,
	}
	if set.IsEmpty() {
		writeValidationError(w, "at least one selector required")
		return
	}

	nodeIDs, err := selector.Resolve(r.Context(), h.DB, set)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
			Error: err.Error(),
			Code:  "selector_error",
		})
		return
	}
	if len(nodeIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"message": "selector matched zero nodes",
			"results": []interface{}{},
		})
		return
	}

	// Check source existence.
	srcInfo, statErr := os.Stat(cleanSrc)
	if statErr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
			Error: fmt.Sprintf("src_path %q: %v", cleanSrc, statErr),
			Code:  "src_not_found",
		})
		return
	}
	if srcInfo.IsDir() && !req.Recursive {
		writeValidationError(w, "src_path is a directory; set recursive=true to copy directories")
		return
	}

	// Build tarball.
	tarball, tarErr := buildCpTarball(cleanSrc, req.Recursive)
	if tarErr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
			Error: fmt.Sprintf("failed to create tarball: %v", tarErr),
			Code:  "tarball_error",
		})
		return
	}
	if tarball.Len() > cpTransferSizeLimitBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, api.ErrorResponse{
			Error: fmt.Sprintf("source size exceeds %d MB limit; use a smaller source in v1", cpTransferSizeLimitBytes/1024/1024),
			Code:  "too_large",
		})
		return
	}

	// Encode tarball as base64 for shell pipe.
	b64Tar := base64.StdEncoding.EncodeToString(tarball.Bytes())

	// Resolve hostnames.
	hostnameByID := make(map[string]string, len(nodeIDs))
	allNodes, _ := h.DB.ListAllNodes(r.Context())
	for _, n := range allNodes {
		hostnameByID[n.ID] = n.Hostname
	}

	// Set up SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "streaming not supported",
			Code:  "no_flusher",
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	type nodeResult struct {
		nodeID   string
		hostname string
		exitCode int
		errStr   string
	}

	resultsCh := make(chan nodeResult, len(nodeIDs))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	dstPath := filepath.Clean(req.DstPath)
	preserveFlag := ""
	if req.Preserve {
		preserveFlag = " --preserve-permissions --xattrs --xattrs-include='*' --acls"
	}

	for _, nid := range nodeIDs {
		nid := nid
		hostname := hostnameByID[nid]
		if hostname == "" {
			hostname = nid
		}

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()

			nr := nodeResult{nodeID: nid, hostname: hostname}

			if !h.Hub.IsConnected(nid) {
				nr.exitCode = -1
				nr.errStr = "node not connected (clustr-clientd offline)"
				resultsCh <- nr
				return
			}

			// Build the extract shell command.
			// mkdir -p <dst> && printf '<b64>\n' | base64 -d | tar -xf - -C <dst> [preserve]
			// We use printf rather than echo to avoid any interpretation of escape sequences.
			shellCmd := fmt.Sprintf(
				"mkdir -p %s && printf '%%s\\n' %s | base64 -d | tar -xf - -C %s%s",
				shellQuote(dstPath),
				shellQuote(b64Tar),
				shellQuote(dstPath),
				preserveFlag,
			)

			msgID := uuid.New().String()
			execPayload, _ := json.Marshal(clientd.OperatorExecRequestPayload{
				RefMsgID:   msgID,
				Command:    "/bin/sh",
				Args:       []string{"-c", shellCmd},
				TimeoutSec: 300,
			})

			serverMsg := clientd.ServerMessage{
				Type:    "operator_exec_request",
				MsgID:   msgID,
				Payload: json.RawMessage(execPayload),
			}

			ch := h.Hub.RegisterOperatorExec(msgID)
			defer h.Hub.UnregisterOperatorExec(msgID)

			if sendErr := h.Hub.Send(nid, serverMsg); sendErr != nil {
				nr.exitCode = -1
				nr.errStr = "send failed: " + sendErr.Error()
				resultsCh <- nr
				return
			}

			log.Info().
				Str("node_id", nid).
				Str("hostname", hostname).
				Str("src", cleanSrc).
				Str("dst", dstPath).
				Int("tarball_bytes", tarball.Len()).
				Msg("cp: dispatched tar extract to node, waiting for result")

			select {
			case res := <-ch:
				nr.exitCode = res.ExitCode
				if res.Error != "" {
					nr.errStr = res.Error
				} else if res.ExitCode != 0 && res.Stderr != "" {
					nr.errStr = strings.TrimSpace(res.Stderr)
				}
			case <-time.After(310 * time.Second):
				nr.exitCode = -1
				nr.errStr = "timed out waiting for cp result (310s)"
			case <-r.Context().Done():
				nr.exitCode = -1
				nr.errStr = "client disconnected"
			}
			resultsCh <- nr
		}()
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var summaryResults []cpNodeResult
	maxExit := 0
	for res := range resultsCh {
		nr := cpNodeResult{
			NodeID:   res.nodeID,
			Hostname: res.hostname,
			ExitCode: res.exitCode,
			Error:    res.errStr,
		}
		summaryResults = append(summaryResults, nr)
		if res.exitCode > maxExit {
			maxExit = res.exitCode
		}
		if res.exitCode < 0 && maxExit < 1 {
			maxExit = 1
		}

		status := "ok"
		if res.errStr != "" || res.exitCode != 0 {
			status = "failed"
		}
		progressData, _ := json.Marshal(map[string]interface{}{
			"node":      res.hostname,
			"node_id":   res.nodeID,
			"status":    status,
			"exit_code": res.exitCode,
			"error":     res.errStr,
		})
		sseWrite(w, flusher, string(progressData))
	}

	sort.Slice(summaryResults, func(i, j int) bool {
		return summaryResults[i].Hostname < summaryResults[j].Hostname
	})

	summary := cpSummaryEvent{
		Type:    "summary",
		Results: summaryResults,
		MaxExit: maxExit,
	}
	summaryJSON, _ := json.Marshal(summary)
	sseWrite(w, flusher, string(summaryJSON))
}

// buildCpTarball creates an in-memory tar archive of srcPath.
// If srcPath is a directory and recursive=true, the full tree is archived.
// File paths in the archive are relative to the parent of srcPath so that
// extracting at the destination creates srcPath's base name under dstPath.
func buildCpTarball(srcPath string, recursive bool) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	info, err := os.Lstat(srcPath)
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		// Walk the directory tree.
		err = filepath.Walk(srcPath, func(path string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			// Compute archive path relative to parent of srcPath.
			relPath, relErr := filepath.Rel(filepath.Dir(srcPath), path)
			if relErr != nil {
				return relErr
			}
			if fi.IsDir() {
				hdr := &tar.Header{
					Name:     relPath + "/",
					Typeflag: tar.TypeDir,
					Mode:     int64(fi.Mode()),
					ModTime:  fi.ModTime(),
				}
				return tw.WriteHeader(hdr)
			}
			return addFileToTar(tw, path, relPath, fi)
		})
	} else {
		relPath := info.Name()
		err = addFileToTar(tw, srcPath, relPath, info)
	}

	if err != nil {
		tw.Close()
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar writer: %w", err)
	}
	return &buf, nil
}

// addFileToTar adds a single regular file to the tar writer.
func addFileToTar(tw *tar.Writer, srcPath, tarName string, fi os.FileInfo) error {
	hdr := &tar.Header{
		Name:    tarName,
		Size:    fi.Size(),
		Mode:    int64(fi.Mode()),
		ModTime: fi.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header for %s: %w", tarName, err)
	}

	f, err := os.Open(srcPath) //#nosec G304 -- operator-supplied path validated above
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy %s: %w", srcPath, err)
	}
	return nil
}

// shellQuote returns s wrapped in single quotes, with any embedded single quotes
// escaped per POSIX rules ('"'"').
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
