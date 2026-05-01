package ldap

import "embed"

//go:embed templates
var templateFS embed.FS

//go:embed assets/clustr-slapd.service assets/50-clustr-slapd.rules assets/openssh-lpk.ldif
var assetFS embed.FS
