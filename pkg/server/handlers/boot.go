package handlers

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/pxe"
)

// BootHandler serves boot assets and dynamic iPXE scripts over HTTP.
// Boot files (vmlinuz, initramfs.img) are served from BootDir.
// iPXE chainload files (ipxe.efi, undionly.kpxe) are served from TFTPDir.
type BootHandler struct {
	// BootDir is the directory containing vmlinuz and initramfs.img.
	BootDir string
	// TFTPDir is the directory containing ipxe.efi and undionly.kpxe.
	TFTPDir string
	// ServerURL is the public URL of clonr-serverd (e.g. http://10.99.0.1:8080).
	// Used to generate the iPXE boot script.
	ServerURL string
}

// ServeIPXEScript handles GET /api/v1/boot/ipxe.
// Returns a dynamically generated iPXE boot script.
func (h *BootHandler) ServeIPXEScript(w http.ResponseWriter, r *http.Request) {
	script, err := pxe.GenerateBootScript(h.ServerURL)
	if err != nil {
		log.Error().Err(err).Msg("boot: generate iPXE script")
		http.Error(w, "failed to generate boot script", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(script)
}

// ServeVMLinuz handles GET /api/v1/boot/vmlinuz.
func (h *BootHandler) ServeVMLinuz(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.BootDir, "vmlinuz"), "application/octet-stream")
}

// ServeInitramfs handles GET /api/v1/boot/initramfs.img.
func (h *BootHandler) ServeInitramfs(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.BootDir, "initramfs.img"), "application/octet-stream")
}

// ServeIPXEEFI handles GET /api/v1/boot/ipxe.efi.
func (h *BootHandler) ServeIPXEEFI(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.TFTPDir, "ipxe.efi"), "application/octet-stream")
}

// ServeUndionlyKPXE handles GET /api/v1/boot/undionly.kpxe.
func (h *BootHandler) ServeUndionlyKPXE(w http.ResponseWriter, r *http.Request) {
	h.serveFile(w, r, filepath.Join(h.TFTPDir, "undionly.kpxe"), "application/octet-stream")
}

func (h *BootHandler) serveFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn().Str("path", path).Msg("boot: file not found")
			writeError(w, api.ErrNotFound)
			return
		}
		log.Error().Err(err).Str("path", path).Msg("boot: open file")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
}
