// Package web embeds the built SPA into the clustr-serverd binary.
package web

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// FS returns an fs.FS rooted at the dist/ directory.
func FS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

// Handler returns an http.Handler that serves the SPA with fallback to
// index.html for any path not found in the embedded FS (client-side routing).
func Handler() (http.Handler, error) {
	sub, err := FS()
	if err != nil {
		return nil, err
	}
	return &spaHandler{fileServer: http.FileServer(http.FS(sub)), sub: sub}, nil
}

type spaHandler struct {
	fileServer http.Handler
	sub        fs.FS
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Try to open the requested path in the embedded FS.
	path := r.URL.Path
	if path == "/" || path == "" {
		h.serveIndex(w, r)
		return
	}
	f, err := h.sub.Open(path[1:]) // strip leading /
	if err != nil {
		// Not found — fall back to index.html so client-side router handles it.
		h.serveIndex(w, r)
		return
	}
	stat, err := f.Stat()
	f.Close()
	if err != nil || stat.IsDir() {
		h.serveIndex(w, r)
		return
	}
	// Vite content-hashes filenames under /assets/ — safe to cache forever.
	// Everything else (favicon, etc.) is not hashed, so use no-cache.
	if len(path) >= 8 && path[:8] == "/assets/" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		setCacheNoStore(w)
	}
	h.fileServer.ServeHTTP(w, r)
}

// setCacheNoStore sets headers that prevent all caching.
func setCacheNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
}

func (h *spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	f, err := h.sub.Open("index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "index.html read failed", http.StatusInternalServerError)
		return
	}
	setCacheNoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}
