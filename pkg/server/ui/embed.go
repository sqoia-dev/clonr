// Package ui embeds the static web UI files into the clonr-serverd binary.
package ui

import "embed"

//go:embed static
var StaticFiles embed.FS
