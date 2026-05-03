package hardware

import (
	"testing"
)

func TestPCIClassIsGPU(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"0x030000", true},  // VGA Compatible Controller
		{"0x030100", true},  // XGA Controller
		{"0x030200", true},  // 3D Controller (NVIDIA Tesla/compute-only)
		{"0x038000", true},  // Display Controller (catch-all 0x03xx)
		{"0x020000", false}, // Ethernet controller
		{"0x040300", false}, // Audio device
		{"0x060000", false}, // Host bridge
		{"", false},
		{"0x", false},
	}
	for _, c := range cases {
		got := pciClassIsGPU(c.input)
		if got != c.want {
			t.Errorf("pciClassIsGPU(%q) = %v; want %v", c.input, got, c.want)
		}
	}
}

func TestResolveGPUModel_FallbackToVendorID(t *testing.T) {
	// devPath points to a non-existent directory — no label file.
	// Should fall back to "NVIDIA GPU [10de:2230]".
	model := resolveGPUModel("/nonexistent/device", "10de", "2230")
	if model == "" {
		t.Fatal("expected non-empty model string")
	}
	// Must contain the vendor name.
	if !containsStr(model, "NVIDIA") {
		t.Errorf("expected NVIDIA in model string, got %q", model)
	}
}

func TestResolveGPUModel_UnknownVendor(t *testing.T) {
	model := resolveGPUModel("/nonexistent/device", "dead", "beef")
	if model == "" {
		t.Fatal("expected non-empty model string")
	}
	// Should include the vendor ID.
	if !containsStr(model, "dead") {
		t.Errorf("expected vendor id 'dead' in model string, got %q", model)
	}
}

func TestDiscoverGPUs_NoSysfs(t *testing.T) {
	// On CI hosts (containers) /sys/bus/pci/devices may not exist.
	// DiscoverGPUs should return nil, nil — never an error — when sysfs is absent.
	// We cannot inject a fake root easily here so just call it and accept nil.
	gpus, err := DiscoverGPUs()
	if err != nil {
		t.Fatalf("DiscoverGPUs should not return error when sysfs is absent, got: %v", err)
	}
	// gpus may be nil (no sysfs) or a real list — both are valid.
	_ = gpus
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && contains(s, sub))
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
