package handlers

import (
	"net/http"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// BundlesHandler serves GET /api/v1/bundles.
// It exposes the built-in bundle metadata that was compiled into the binary
// via -ldflags so the web UI can display it on the Bundles tab.
type BundlesHandler struct {
	SlurmVersion  string // e.g. "24.11.4"
	BundleVersion string // e.g. "v24.11.4-clustr5"
	BundleSHA256  string // tarball SHA256 hex
}

// ListBundles handles GET /api/v1/bundles.
// Returns an array of bundle descriptors. Always contains at least the
// built-in Slurm bundle; future clustr versions may add more entries.
func (h *BundlesHandler) ListBundles(w http.ResponseWriter, _ *http.Request) {
	name := "slurm-" + h.BundleVersion
	bundles := []api.Bundle{
		{
			Name:          name,
			SlurmVersion:  h.SlurmVersion,
			BundleVersion: h.BundleVersion,
			SHA256:        h.BundleSHA256,
			Kind:          "builtin",
			Source:        "embedded",
		},
	}
	writeJSON(w, http.StatusOK, api.ListBundlesResponse{
		Bundles: bundles,
		Total:   len(bundles),
	})
}
