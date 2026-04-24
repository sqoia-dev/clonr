package slurm

import "embed"

//go:embed templates
var templateFS embed.FS

//go:embed deps_matrix.json
var depsMatrixJSON []byte
