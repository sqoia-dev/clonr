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

func TestRenderConfig_MissingKey_UsesZeroValue(t *testing.T) {
	// RenderConfig uses Option("missingkey=zero") — missing fields render as
	// their zero value (empty string for string fields), not an error.
	tmpl := `Cluster={{.ClusterName}} Extra={{.NoSuchField}}`
	ctx := RenderContext{ClusterName: "test"}

	out, err := RenderConfig(tmpl, ctx)
	if err != nil {
		t.Fatalf("RenderConfig missing key: %v", err)
	}
	mustContain(t, out, "Cluster=test")
	mustContain(t, out, "Extra=")
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
