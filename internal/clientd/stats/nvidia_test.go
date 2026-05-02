package stats

import (
	"context"
	"os"
	"testing"
)

func TestParseNvidiaSMI_2GPU(t *testing.T) {
	data, err := os.ReadFile("testdata/nvidia-smi-2gpu.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	samples := parseNvidiaSMI(data)
	if len(samples) == 0 {
		t.Fatal("expected samples, got none")
	}

	// Build a lookup: sensor+gpu → value.
	type key struct {
		sensor string
		gpu    string
	}
	byKey := make(map[key]float64)
	for _, s := range samples {
		byKey[key{s.Sensor, s.Labels["gpu"]}] = s.Value
	}

	// GPU 0 assertions.
	if v, ok := byKey[key{"gpu_util_pct", "0"}]; !ok || v != 42.0 {
		t.Errorf("gpu0 gpu_util_pct: want 42, got %v (ok=%v)", v, ok)
	}
	if v, ok := byKey[key{"mem_util_pct", "0"}]; !ok || v != 17.0 {
		t.Errorf("gpu0 mem_util_pct: want 17, got %v (ok=%v)", v, ok)
	}
	if v, ok := byKey[key{"temp_celsius", "0"}]; !ok || v != 58.0 {
		t.Errorf("gpu0 temp_celsius: want 58, got %v (ok=%v)", v, ok)
	}
	if v, ok := byKey[key{"power_watts", "0"}]; !ok || v != 302.45 {
		t.Errorf("gpu0 power_watts: want 302.45, got %v (ok=%v)", v, ok)
	}
	if v, ok := byKey[key{"pcie_gen_current", "0"}]; !ok || v != 4.0 {
		t.Errorf("gpu0 pcie_gen_current: want 4, got %v (ok=%v)", v, ok)
	}
	if v, ok := byKey[key{"ecc_uncorrectable_count", "0"}]; !ok || v != 0.0 {
		t.Errorf("gpu0 ecc_uncorrectable_count: want 0, got %v (ok=%v)", v, ok)
	}
	// 14336 MiB → 14336 * 1024 * 1024 bytes
	const gpu0MemUsed = 14336 * 1024 * 1024
	if v, ok := byKey[key{"mem_used_bytes", "0"}]; !ok || v != gpu0MemUsed {
		t.Errorf("gpu0 mem_used_bytes: want %v, got %v (ok=%v)", gpu0MemUsed, v, ok)
	}

	// GPU 1 assertions.
	if v, ok := byKey[key{"gpu_util_pct", "1"}]; !ok || v != 89.0 {
		t.Errorf("gpu1 gpu_util_pct: want 89, got %v (ok=%v)", v, ok)
	}
	if v, ok := byKey[key{"temp_celsius", "1"}]; !ok || v != 71.0 {
		t.Errorf("gpu1 temp_celsius: want 71, got %v (ok=%v)", v, ok)
	}
	if v, ok := byKey[key{"ecc_uncorrectable_count", "1"}]; !ok || v != 3.0 {
		t.Errorf("gpu1 ecc_uncorrectable_count: want 3 (volatile), got %v (ok=%v)", v, ok)
	}

	// Name label should be populated.
	for _, s := range samples {
		if s.Labels["gpu"] == "0" && s.Labels["name"] == "" {
			t.Error("gpu0: name label is empty")
		}
	}
}

func TestNvidiaPlugin_AbsentBinary(t *testing.T) {
	// Ensure the plugin emits nothing when nvidia-smi is absent.
	// We rely on the test environment not having nvidia-smi.
	// If it is installed, skip — we can't control the hardware in CI.
	if _, err := findBinary("nvidia-smi"); err == nil {
		t.Skip("nvidia-smi present in PATH; skipping absent-binary test")
	}

	p := NewNvidiaPlugin()
	samples := p.Collect(context.Background())
	if len(samples) != 0 {
		t.Errorf("expected 0 samples when binary absent, got %d", len(samples))
	}
}

// findBinary is a thin wrapper around os.LookPath for test use.
func findBinary(name string) (string, error) {
	path := ""
	for _, dir := range filepath_splitList(os.Getenv("PATH")) {
		candidate := dir + "/" + name
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			path = candidate
			break
		}
	}
	if path == "" {
		return "", os.ErrNotExist
	}
	return path, nil
}

func filepath_splitList(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, p := range splitPathList(s) {
		result = append(result, p)
	}
	return result
}

func splitPathList(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ':' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
