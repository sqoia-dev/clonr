//go:build webdist

// Package web embeds the built SPA into the clustr-serverd binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distRaw embed.FS

// distFS returns an fs.FS rooted at the dist/ directory.
func distFS() (fs.FS, error) {
	return fs.Sub(distRaw, "dist")
}
