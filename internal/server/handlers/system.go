package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sqoia-dev/clustr/internal/image"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ImageBuildStoreIface is the subset of BuildProgressStore needed by SystemHandler.
// Defined here to avoid an import cycle with the server package.
type ImageBuildStoreIface interface {
	// ActiveBuildIDs returns image IDs whose build is in a non-terminal phase.
	ActiveBuildIDs() []string
}

// ActiveReimagesDBIface is the subset of *db.DB needed by SystemHandler.
// Defined here to avoid importing the concrete db package from handlers.
type ActiveReimagesDBIface interface {
	ListAllActiveReimageIDs(ctx context.Context) ([]string, error)
	WaitForActiveReimages(ctx context.Context)
}

// DHCPLeasesIface is the subset of *pxe.DHCPServer needed by SystemHandler.
// Defined here to avoid an import cycle between handlers and pxe.
type DHCPLeasesIface interface {
	// RecentLeases returns MACs that received a lease within the given window.
	RecentLeases(window time.Duration) []string
}

// DeployProgressLister is the subset of ProgressStore needed by SystemHandler.
// Defined here to avoid a circular import with the server package.
type DeployProgressLister interface {
	// List returns a snapshot of all currently tracked deploy progress entries.
	List() []api.DeployProgress
}

// SystemHandler exposes server-level operational status endpoints.
// Currently used by the autodeploy script to check whether a restart is safe.
type SystemHandler struct {
	Initramfs *InitramfsHandler

	// ImageBuilds, when set, is queried for in-flight ISO/image factory builds.
	ImageBuilds ImageBuildStoreIface

	// Reimages, when set, is queried for active (non-terminal) reimage requests.
	Reimages ActiveReimagesDBIface

	// DeployProgress, when set, is queried for in-progress node-initiated deploys.
	// This covers the path where a node POSTs to /api/v1/deploy/progress without
	// a corresponding reimage_requests row (BUG-18).
	DeployProgress DeployProgressLister

	// Shells, when set, is queried for open image shell sessions.
	Shells *image.ShellManager

	// DHCPLeases, when set, is used to detect nodes mid-PXE-boot.
	// Any MAC with a lease issued within the last 30 seconds is reported as pxe_in_flight.
	DHCPLeases DHCPLeasesIface
}

// GetActiveJobs handles GET /api/v1/system/active-jobs.
//
// Returns the set of long-running operations that must complete before
// clustr-serverd can be safely restarted.  Empty arrays indicate the server
// is idle and a restart will not interrupt any in-flight work.
//
// The autodeploy script defers the restart when ANY field in the response is
// non-empty — a single endpoint, single decision.
//
// This endpoint is intentionally unauthenticated so the autodeploy script
// can poll it without an API key (same fail-open pattern used by the
// existing image/reimage guards).
//
// Response shape:
//
//	{
//	  "initramfs_builds":  ["initramfs_<build-id>"],
//	  "image_builds":      ["image_<image-id>"],
//	  "reimages":          ["reimage_<request-id>"],
//	  "operator_sessions": ["shell_<session-id>"],
//	  "pxe_in_flight":     ["<mac>"]
//	}
func (h *SystemHandler) GetActiveJobs(w http.ResponseWriter, r *http.Request) {
	// 1. Initramfs builds — query the BuildSession map from BUG-14.
	// We hold h.Initramfs.mu while iterating sessions, then call sess.isDone()
	// which locks sess.mu. Lock order (h.mu → sess.mu) matches runBuildAsync.
	initramfsBuilds := []string{}
	if h.Initramfs != nil {
		h.Initramfs.mu.Lock()
		for buildID, sess := range h.Initramfs.sessions {
			if done, _ := sess.isDone(); !done {
				initramfsBuilds = append(initramfsBuilds, "initramfs_"+buildID)
			}
		}
		// Also capture legacy RebuildInitramfs single-slot builds which don't
		// create a BuildSession but do set h.Initramfs.running = true.
		if h.Initramfs.running && len(initramfsBuilds) == 0 {
			initramfsBuilds = append(initramfsBuilds, "initramfs_rebuild")
		}
		h.Initramfs.mu.Unlock()
	}

	// 2. Image builds — query the BuildProgressStore for non-terminal ISO builds.
	imageBuilds := []string{}
	if h.ImageBuilds != nil {
		for _, id := range h.ImageBuilds.ActiveBuildIDs() {
			imageBuilds = append(imageBuilds, "image_"+id)
		}
	}

	// 3. Reimages — query the DB for pending/triggered/in_progress records.
	reimages := []string{}
	if h.Reimages != nil {
		ctx := r.Context()
		ids, err := h.Reimages.ListAllActiveReimageIDs(ctx)
		if err == nil {
			for _, id := range ids {
				reimages = append(reimages, "reimage_"+id)
			}
		}
		// Non-fatal: on DB error leave reimages empty (fail-open — same pattern
		// as the existing initramfs guard in RebuildInitramfs.hasActiveDeployViaDB).
	}

	// 3b. Deploys — query ProgressStore for node-initiated deploys in a
	// non-terminal phase. This covers the path where a node POSTs progress
	// directly without a reimage_requests row (BUG-18).
	// Terminal phases per api.DeployProgress: "complete" and "error".
	deploys := []string{}
	if h.DeployProgress != nil {
		for _, entry := range h.DeployProgress.List() {
			if entry.Phase == "complete" || entry.Phase == "error" || entry.Phase == "" {
				continue
			}
			deploys = append(deploys, "deploy_"+entry.NodeMAC)
		}
	}

	// 4. Operator shell sessions — query the ShellManager for open chroot sessions.
	operatorSessions := []string{}
	if h.Shells != nil {
		for _, sess := range h.Shells.ListSessions() {
			operatorSessions = append(operatorSessions, "shell_"+sess.ID)
		}
	}

	// 5. PXE in-flight — any MAC that received a DHCP lease in the last 30s.
	// Coarse heuristic: leases expire after leaseDur (24h), so a lease with
	// issuedAt > (now - 30s) means the node is likely still mid-PXE chainload.
	pxeInFlight := []string{}
	if h.DHCPLeases != nil {
		pxeInFlight = h.DHCPLeases.RecentLeases(30 * time.Second)
		if pxeInFlight == nil {
			pxeInFlight = []string{}
		}
	}

	writeJSON(w, http.StatusOK, api.ActiveJobsResponse{
		InitramfsBuilds:  initramfsBuilds,
		ImageBuilds:      imageBuilds,
		Reimages:         reimages,
		Deploys:          deploys,
		OperatorSessions: operatorSessions,
		PxeInFlight:      pxeInFlight,
	})
}

// WaitForActiveReimages blocks until all active reimage requests reach a
// terminal state or ctx expires. Mirrors BuildProgressStore.WaitForActive.
// Call this from the autodeploy script's defer-cap logic to give in-flight
// reimages a final grace window before forcing a restart.
func (h *SystemHandler) WaitForActiveReimages(ctx context.Context) {
	if h.Reimages == nil {
		return
	}
	h.Reimages.WaitForActiveReimages(ctx)
}

// WaitForAllActive blocks until all tracked operation classes are idle or ctx
// expires. The autodeploy script should call this when the defer cap is hit to
// give in-flight work a final 120s grace window before forcing the restart.
func (h *SystemHandler) WaitForAllActive(ctx context.Context) {
	// Image builds have their own WaitForActive on BuildProgressStore.
	// Reimages are drained here.
	if h.Reimages != nil {
		h.Reimages.WaitForActiveReimages(ctx)
	}
	// Initramfs builds and shell sessions are very short-lived;
	// the autodeploy defer loop handles those by retrying.
}

//lint:ignore U1000 used in system_test.go idle-state assertions; linker tree-shakes it from prod binary
// activeJobCount returns the total number of active jobs across all categories.
// Used internally and in tests to assert idle state.
func (h *SystemHandler) activeJobCount(ctx context.Context) int {
	n := 0
	if h.Initramfs != nil {
		h.Initramfs.mu.Lock()
		for _, sess := range h.Initramfs.sessions {
			if done, _ := sess.isDone(); !done {
				n++
			}
		}
		if h.Initramfs.running && n == 0 {
			n++
		}
		h.Initramfs.mu.Unlock()
	}
	if h.ImageBuilds != nil {
		n += len(h.ImageBuilds.ActiveBuildIDs())
	}
	if h.Reimages != nil {
		ids, _ := h.Reimages.ListAllActiveReimageIDs(ctx)
		n += len(ids)
	}
	if h.DeployProgress != nil {
		for _, entry := range h.DeployProgress.List() {
			if entry.Phase != "complete" && entry.Phase != "error" && entry.Phase != "" {
				n++
			}
		}
	}
	if h.Shells != nil {
		n += len(h.Shells.ListSessions())
	}
	if h.DHCPLeases != nil {
		n += len(h.DHCPLeases.RecentLeases(30 * time.Second))
	}
	return n
}

//lint:ignore U1000 used in system_test.go build-ID assertions; linker tree-shakes from prod binary
// formatInitramfsID returns the label used in initramfs_builds entries.
// Extracted for use in tests.
func formatInitramfsID(buildID string) string {
	return fmt.Sprintf("initramfs_%s", buildID)
}
