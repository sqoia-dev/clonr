// Package web embeds the built SPA into the clustr-serverd binary.
package web

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// stableTopLevelAssets lists top-level files that are stable across deploys
// but not content-hashed by Vite. Served with a 1-day cache + stale-while-revalidate.
var stableTopLevelAssets = map[string]bool{
	"favicon.svg":          true,
	"favicon.ico":          true,
	"icons.svg":            true,
	"robots.txt":           true,
	"manifest.webmanifest": true,
	"apple-touch-icon.png": true,
}

// FS returns an fs.FS rooted at the dist/ directory.
func FS() (fs.FS, error) {
	return distFS()
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
		if errors.Is(err, fs.ErrNotExist) {
			// Not found — fall back to index.html so client-side router handles it.
			h.serveIndex(w, r)
			return
		}
		// Genuine I/O or FS error — surface it rather than silently serving index.html.
		log.Error().Err(err).Str("path", path).Msg("web: open failed")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	stat, err := f.Stat()
	f.Close()
	if err != nil || stat.IsDir() {
		h.serveIndex(w, r)
		return
	}

	// Set cache headers based on path type:
	//   /assets/*        — Vite content-hashed → cache forever (immutable)
	//   stable top-level — not hashed but stable → 1-day cache + stale-while-revalidate
	//   everything else  — no-store (includes index.html, the SPA shell)
	bare := path[1:] // strip leading /
	if len(path) >= 8 && path[:8] == "/assets/" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if stableTopLevelAssets[bare] {
		w.Header().Set("Cache-Control", "public, max-age=86400, stale-while-revalidate=604800")
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
