// Package ui embeds the static web UI files into the clustr-serverd binary.
package ui

import "embed"

//go:embed static
var StaticFiles embed.FS
