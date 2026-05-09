package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/internal/clientd/stats"
)

// TestInfiniBandSysfs_FixtureTree builds a minimal /sys/class/infiniband
// tree and verifies the plugin emits samples with the expected
// MetricName foreign-keys.
func TestInfiniBandSysfs_FixtureTree(t *testing.T) {
	root := t.TempDir()
	port := filepath.Join(root, "mlx5_0", "ports", "1")
	counters := filepath.Join(port, "counters")
	if err := os.MkdirAll(counters, 0o755); err != nil {
		t.Fatal(err)
	}

	must := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(port, "state"), "4: ACTIVE\n")
	must(filepath.Join(port, "rate"), "100 Gb/sec (4X)\n")
	must(filepath.Join(port, "link_layer"), "InfiniBand\n")
	must(filepath.Join(counters, "port_rcv_data"), "1024\n") // → 4096 bytes
	must(filepath.Join(counters, "port_xmit_data"), "2048\n")
	must(filepath.Join(counters, "symbol_error"), "0\n")

	reg := stats.NewMetricRegistry()
	p := NewInfiniBandSysfsPlugin(reg, root)
	samples := p.Collect(context.Background())

	if len(samples) != 6 {
		t.Fatalf("want 6 samples, got %d: %+v", len(samples), samples)
	}

	// Index by MetricName for assertions.
	byName := map[string]stats.Sample{}
	for _, s := range samples {
		if s.MetricName == "" {
			t.Errorf("sample %q missing MetricName foreign-key", s.Sensor)
		}
		if s.Labels["port"] != "mlx5_0/1" {
			t.Errorf("sample %q has port label %q, want mlx5_0/1",
				s.Sensor, s.Labels["port"])
		}
		byName[s.MetricName] = s
	}

	if byName["ib_state"].Value != 4 {
		t.Errorf("ib_state = %v, want 4", byName["ib_state"].Value)
	}
	if byName["ib_rate_gbps"].Value != 100 {
		t.Errorf("ib_rate_gbps = %v, want 100", byName["ib_rate_gbps"].Value)
	}
	if byName["ib_link_layer"].Value != 1 {
		t.Errorf("ib_link_layer = %v, want 1 (IB)", byName["ib_link_layer"].Value)
	}
	if byName["ib_port_rcv_data_bytes"].Value != 4096 {
		t.Errorf("rx bytes = %v, want 4096 (1024 * 4)",
			byName["ib_port_rcv_data_bytes"].Value)
	}
	if byName["ib_port_xmit_data_bytes"].Value != 8192 {
		t.Errorf("tx bytes = %v, want 8192 (2048 * 4)",
			byName["ib_port_xmit_data_bytes"].Value)
	}
}

func TestInfiniBandSysfs_NoRoot(t *testing.T) {
	reg := stats.NewMetricRegistry()
	// Point at a path that doesn't exist.
	p := NewInfiniBandSysfsPlugin(reg, filepath.Join(t.TempDir(), "no-such-root"))
	samples := p.Collect(context.Background())
	if samples != nil {
		t.Errorf("expected nil samples on missing root, got %+v", samples)
	}
}

func TestInfiniBandSysfs_EmptyRoot(t *testing.T) {
	root := t.TempDir() // exists but no devices
	reg := stats.NewMetricRegistry()
	p := NewInfiniBandSysfsPlugin(reg, root)
	samples := p.Collect(context.Background())
	if len(samples) != 0 {
		t.Errorf("expected 0 samples on empty root, got %d", len(samples))
	}
}

func TestInfiniBandSysfs_PartialReads(t *testing.T) {
	// A port with only "state" — other files missing.  Should emit one
	// sample for state and skip the rest without error.
	root := t.TempDir()
	port := filepath.Join(root, "mlx5_0", "ports", "1")
	if err := os.MkdirAll(port, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(port, "state"), []byte("1: DOWN\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := stats.NewMetricRegistry()
	p := NewInfiniBandSysfsPlugin(reg, root)
	samples := p.Collect(context.Background())

	if len(samples) != 1 {
		t.Fatalf("want 1 sample (state only), got %d", len(samples))
	}
	if samples[0].MetricName != "ib_state" || samples[0].Value != 1 {
		t.Errorf("got %+v, want ib_state=1", samples[0])
	}
}

func TestInfiniBandSysfs_TwoDevicesTwoPorts(t *testing.T) {
	root := t.TempDir()
	for _, dev := range []string{"mlx5_0", "mlx5_1"} {
		for _, port := range []string{"1", "2"} {
			pdir := filepath.Join(root, dev, "ports", port)
			if err := os.MkdirAll(pdir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pdir, "state"), []byte("4: ACTIVE\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	reg := stats.NewMetricRegistry()
	p := NewInfiniBandSysfsPlugin(reg, root)
	samples := p.Collect(context.Background())

	// 2 devs × 2 ports × 1 metric (state only) = 4 samples.
	if len(samples) != 4 {
		t.Errorf("want 4 samples (2 dev × 2 port), got %d", len(samples))
	}
	seen := map[string]bool{}
	for _, s := range samples {
		seen[s.Labels["port"]] = true
	}
	want := []string{"mlx5_0/1", "mlx5_0/2", "mlx5_1/1", "mlx5_1/2"}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("missing port label %q in samples", w)
		}
	}
}
