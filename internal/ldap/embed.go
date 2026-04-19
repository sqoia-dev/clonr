package ldap

import "embed"

//go:embed templates
var templateFS embed.FS

//go:embed assets/clonr-slapd.service assets/50-clonr-slapd.rules
var assetFS embed.FS
