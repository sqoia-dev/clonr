// render_test.go — golden-file tests for Slurm config template rendering (S1-5, TD-1).
// RenderConfig is a pure function — no DB or external service required.
package slurm

import (
	"strings"
	"testing"
)

// ─── slurm.conf rendering ─────────────────────────────────────────────────────

func TestRenderConfig_SlurmConf_BasicCluster(t *testing.T) {
	tmplContent := `ClusterName={{.ClusterName}}
SlurmctldHost={{.ControllerHostname}}
{{- range .Nodes}}
NodeName={{.NodeName}} CPUs={{.CPUCount}} Sockets={{.Sockets}} CoresPerSocket={{.CoresPerSocket}} ThreadsPerCore={{.ThreadsPerCore}} RealMemory={{.RealMemoryMB}} State=UNKNOWN{{if .GRESParam}} Gres={{.GRESParam}}{{end}}
{{- end}}
PartitionName=batch Nodes=ALL Default=YES MaxTime=INFINITE State=UP`

	ctx := RenderContext{
		ClusterName:        "mycluster",
		ControllerHostname: "controller-node",
		Nodes: []NodeRenderData{
			{
				NodeID:         "node-1-id",
				NodeName:       "compute01",
				CPUCount:       "64",
				Sockets:        "2",
				CoresPerSocket: "16",
				ThreadsPerCore: "2",
				RealMemoryMB:   "256000",
			},
			{
				NodeID:         "node-2-id",
				NodeName:       "compute02",
				CPUCount:       "64",
				Sockets:        "2",
				CoresPerSocket: "16",
				ThreadsPerCore: "2",
				RealMemoryMB:   "256000",
				GRESParam:      "gpu:a100:4",
			},
		},
	}

	out, err := RenderConfig(tmplContent, ctx)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}

	mustContain(t, out, "ClusterName=mycluster")
	mustContain(t, out, "SlurmctldHost=controller-node")
	mustContain(t, out, "NodeName=compute01")
	mustContain(t, out, "CPUs=64")
	mustContain(t, out, "Sockets=2")
	mustContain(t, out, "CoresPerSocket=16")
	mustContain(t, out, "ThreadsPerCore=2")
	mustContain(t, out, "RealMemory=256000")
	mustContain(t, out, "NodeName=compute02")
	mustContain(t, out, "Gres=gpu:a100:4")
	mustNotContain(t, out, "Gres=\n") // compute01 has no GRES
	mustContain(t, out, "PartitionName=batch")
}

func TestRenderConfig_SlurmConf_NoNodes(t *testing.T) {
	tmpl := `ClusterName={{.ClusterName}}
SlurmctldHost={{.ControllerHostname}}
{{- range .Nodes}}
NodeName={{.NodeName}} CPUs={{.CPUCount}}
{{- end}}`

	ctx := RenderContext{
		ClusterName:        "empty-cluster",
		ControllerHostname: "head-node",
		Nodes:              nil,
	}

	out, err := RenderConfig(tmpl, ctx)
	if err != nil {
		t.Fatalf("RenderConfig empty nodes: %v", err)
	}
	mustContain(t, out, "ClusterName=empty-cluster")
	mustNotContain(t, out, "NodeName=")
}

func TestRenderConfig_SlurmConf_ControllerInNodesList(t *testing.T) {
	tmpl := `SlurmctldHost={{.ControllerHostname}}
{{- range .Nodes}}
NodeName={{.NodeName}} CPUs={{.CPUCount}}
{{- end}}`

	ctx := RenderContext{
		ClusterName:        "mixed",
		ControllerHostname: "head01",
		Nodes: []NodeRenderData{
			{NodeID: "ctrl", NodeName: "head01", CPUCount: "16", Sockets: "1", CoresPerSocket: "8", ThreadsPerCore: "2", RealMemoryMB: "64000"},
			{NodeID: "c1", NodeName: "node01", CPUCount: "32", Sockets: "2", CoresPerSocket: "8", ThreadsPerCore: "2", RealMemoryMB: "128000"},
		},
	}

	out, err := RenderConfig(tmpl, ctx)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	mustContain(t, out, "SlurmctldHost=head01")
	mustContain(t, out, "NodeName=head01")
	mustContain(t, out, "NodeName=node01")
}

// ─── gres.conf rendering ──────────────────────────────────────────────────────

func TestRenderConfig_GresConf_GPUNode(t *testing.T) {
	tmpl := `{{- if .CurrentNode}}
# gres.conf for {{.CurrentNode.NodeName}}
Name=gpu Type=a100 File=/dev/nvidia[0-3] Count=4
{{- end}}`

	ctx := RenderContext{
		ClusterName:        "gpu-cluster",
		ControllerHostname: "head",
		CurrentNode: &NodeRenderData{
			NodeID:   "gpu-node-1",
			NodeName: "gpu01",
		},
	}

	out, err := RenderConfig(tmpl, ctx)
	if err != nil {
		t.Fatalf("RenderConfig gres: %v", err)
	}
	mustContain(t, out, "gres.conf for gpu01")
	mustContain(t, out, "Name=gpu Type=a100")
}

// ─── plugstack.conf rendering ─────────────────────────────────────────────────

func TestRenderConfig_PlugstackConf_LiteralPassthrough(t *testing.T) {
	// Non-template content returned as-is.
	literal := "required /usr/lib64/slurm/spank_mpi_pmix.so\n"
	ctx := RenderContext{ClusterName: "c", ControllerHostname: "h"}

	out, err := RenderConfig(literal, ctx)
	if err != nil {
		t.Fatalf("RenderConfig literal: %v", err)
	}
	if out != literal {
		t.Errorf("literal passthrough: got %q, want %q", out, literal)
	}
}

// ─── overrideOrDefault ───────────────────────────────────────────────────────

func TestOverrideOrDefault(t *testing.T) {
	overrides := map[string]string{
		"cpus":   "128",
		"empty":  "",
	}

	if got := overrideOrDefault(overrides, "cpus", "1"); got != "128" {
		t.Errorf("cpus: got %q, want 128", got)
	}
	if got := overrideOrDefault(overrides, "empty", "default-val"); got != "default-val" {
		t.Errorf("empty: got %q, want default-val", got)
	}
	if got := overrideOrDefault(overrides, "missing", "fallback"); got != "fallback" {
		t.Errorf("missing: got %q, want fallback", got)
	}
}

// ─── MissingKey handling ──────────────────────────────────────────────────────

func TestRenderConfig_MapMissingKey_UsesZeroValue(t *testing.T) {
	// RenderConfig uses Option("missingkey=zero") — this silences missing map
	// key lookups (map[string]string), returning zero value for that type.
	// Note: missing STRUCT fields still error; this only applies to maps.
	tmpl := `Cluster={{.ClusterName}} Override={{index .Overrides "nonexistent-key"}}`
	ctx := RenderContext{
		ClusterName: "map-test",
		Overrides:   map[string]string{"key": "val"},
	}

	out, err := RenderConfig(tmpl, ctx)
	if err != nil {
		t.Fatalf("RenderConfig missing map key: %v", err)
	}
	mustContain(t, out, "Cluster=map-test")
	mustContain(t, out, "Override=")
}

// ─── D17: role alias tests ────────────────────────────────────────────────────

// TestIsComputeRole verifies that "compute" and "worker" are treated as
// equivalent compute roles, and other role strings are not.
func TestIsComputeRole(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{RoleCompute, true},
		{RoleWorker, true}, // deprecated alias — must still return true
		{RoleController, false},
		{RoleLogin, false},
		{RoleDBD, false},
		{RoleNone, false},
		{"", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		if got := IsComputeRole(tc.role); got != tc.want {
			t.Errorf("IsComputeRole(%q) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

// TestFilesForRoles_WorkerAlias verifies that a node with role "worker"
// receives the same config files as a node with role "compute".
func TestFilesForRoles_WorkerAlias(t *testing.T) {
	filesWorker := FilesForRoles([]string{RoleWorker})
	filesCompute := FilesForRoles([]string{RoleCompute})

	if len(filesWorker) != len(filesCompute) {
		t.Fatalf("FilesForRoles([worker]) len=%d, want %d (same as [compute])",
			len(filesWorker), len(filesCompute))
	}
	for i, f := range filesCompute {
		if filesWorker[i] != f {
			t.Errorf("FilesForRoles([worker])[%d] = %q, want %q", i, filesWorker[i], f)
		}
	}
}

// TestServicesForRoles_WorkerAlias verifies that a node with role "worker"
// gets slurmd.service enabled, same as a node with role "compute".
func TestServicesForRoles_WorkerAlias(t *testing.T) {
	svcsWorker := ServicesForRoles([]string{RoleWorker})
	svcsCompute := ServicesForRoles([]string{RoleCompute})

	if len(svcsWorker) != len(svcsCompute) {
		t.Fatalf("ServicesForRoles([worker]) len=%d, want %d (same as [compute])",
			len(svcsWorker), len(svcsCompute))
	}
	for i, s := range svcsCompute {
		if svcsWorker[i] != s {
			t.Errorf("ServicesForRoles([worker])[%d] = %q, want %q", i, svcsWorker[i], s)
		}
	}
}

// TestRenderConfig_NodeWithWorkerRole_AppearsInNodeNameBlock verifies that a
// node in a RenderContext with role "worker" is included in the NodeName block.
// Regression test for REG-2: render.go previously dropped "worker"-role nodes
// because the role check only matched "compute".
func TestRenderConfig_NodeWithWorkerRole_AppearsInNodeNameBlock(t *testing.T) {
	tmplContent := `{{- range .Nodes}}NodeName={{.NodeName}}
{{- end}}`

	// Build a context as buildRenderContext would — Nodes only contains entries
	// that passed the role filter. We directly construct a Nodes slice here to
	// test that "worker"-role nodes survive the filter check via IsComputeRole.
	//
	// The filter itself lives in buildRenderContext; we test IsComputeRole
	// separately above. This test validates the downstream template rendering
	// still includes the node once it passes the filter.
	ctx := RenderContext{
		ClusterName:        "lab",
		ControllerHostname: "slurm-controller",
		Nodes: []NodeRenderData{
			{
				NodeID:   "node-ctrl",
				NodeName: "slurm-controller",
				Roles:    []string{RoleController, RoleWorker},
				CPUCount: "2", Sockets: "1", CoresPerSocket: "1", ThreadsPerCore: "2",
				RealMemoryMB: "3905",
			},
			{
				NodeID:   "node-compute",
				NodeName: "slurm-compute",
				Roles:    []string{RoleWorker},
				CPUCount: "2", Sockets: "1", CoresPerSocket: "1", ThreadsPerCore: "2",
				RealMemoryMB: "3905",
			},
		},
	}

	out, err := RenderConfig(tmplContent, ctx)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}

	mustContain(t, out, "NodeName=slurm-controller")
	mustContain(t, out, "NodeName=slurm-compute")
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("output does not contain %q\nFull output:\n%s", sub, s)
	}
}

func mustNotContain(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("output should not contain %q\nFull output:\n%s", sub, s)
	}
}
