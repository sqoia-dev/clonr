package stats

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// NvidiaPlugin reports per-GPU metrics by shelling out to `nvidia-smi -q -x`.
//
// The plugin silently emits no samples when:
//   - The `nvidia-smi` binary is not installed.
//   - nvidia-smi returns a non-zero exit code (no GPUs present, driver not loaded).
//
// Sensors produced per GPU (labels gpu=<index>, name=<product name>):
//   - "gpu_util_pct"          — GPU core utilisation          (unit: pct)
//   - "mem_util_pct"          — memory bus utilisation        (unit: pct)
//   - "mem_used_bytes"        — framebuffer bytes used        (unit: bytes)
//   - "mem_total_bytes"       — framebuffer total bytes       (unit: bytes)
//   - "temp_celsius"          — GPU junction temperature      (unit: celsius)
//   - "fan_pct"               — fan speed                     (unit: pct)
//   - "power_watts"           — current power draw            (unit: watts)
//   - "pcie_gen_current"      — current PCIe link generation  (unit: count)
//   - "ecc_uncorrectable_count" — volatile uncorrectable ECC errors (unit: count)
type NvidiaPlugin struct{}

// NewNvidiaPlugin creates an NvidiaPlugin.
func NewNvidiaPlugin() *NvidiaPlugin { return &NvidiaPlugin{} }

func (p *NvidiaPlugin) Name() string { return "nvidia" }

// --- XML structures matching nvidia-smi -q -x output ---

type nvidiaSMILog struct {
	XMLName xml.Name    `xml:"nvidia_smi_log"`
	GPUs    []nvidiaGPU `xml:"gpu"`
}

type nvidiaGPU struct {
	ProductName   string              `xml:"product_name"`
	MinorNumber   string              `xml:"minor_number"`
	Utilization   nvidiaUtilization   `xml:"utilization"`
	FBMemory      nvidiaFBMemory      `xml:"fb_memory_usage"`
	Temperature   nvidiaTemperature   `xml:"temperature"`
	FanSpeed      string              `xml:"fan_speed"`
	PowerReadings nvidiaPowerReadings `xml:"power_readings"`
	PCI           nvidiaPCI           `xml:"pci"`
	ECCErrors     nvidiaECCErrors     `xml:"ecc_errors"`
}

type nvidiaUtilization struct {
	GPUUtil string `xml:"gpu_util"`
	MemUtil string `xml:"memory_util"`
}

type nvidiaFBMemory struct {
	Total string `xml:"total"`
	Used  string `xml:"used"`
}

type nvidiaTemperature struct {
	GPUTemp string `xml:"gpu_temp"`
}

type nvidiaPowerReadings struct {
	PowerDraw string `xml:"power_draw"`
}

type nvidiaPCI struct {
	GPULinkInfo nvidiaPCILinkInfo `xml:"pci_gpu_link_info"`
}

type nvidiaPCILinkInfo struct {
	PCIeGen nvidiaPCIeGen `xml:"pcie_gen"`
}

type nvidiaPCIeGen struct {
	CurrentLinkGen string `xml:"current_link_gen"`
}

type nvidiaECCErrors struct {
	Volatile nvidiaECCCounts `xml:"volatile"`
}

type nvidiaECCCounts struct {
	SRAMUncorrectable string `xml:"sram_uncorrectable"`
}

func (p *NvidiaPlugin) Collect(ctx context.Context) []Sample {
	nvidiaSMIPath, err := exec.LookPath("nvidia-smi")
	if err != nil {
		// Binary not present — stay silent.
		return nil
	}

	out, err := runWithTimeout(ctx, nvidiaSMIPath, "-q", "-x")
	if err != nil {
		log.Debug().Err(err).Msg("stats/nvidia: nvidia-smi -q -x failed (non-fatal)")
		return nil
	}

	return parseNvidiaSMI(out)
}

// parseNvidiaSMI parses the XML output of `nvidia-smi -q -x` and returns samples.
func parseNvidiaSMI(data []byte) []Sample {
	var smiLog nvidiaSMILog
	if err := xml.Unmarshal(data, &smiLog); err != nil {
		log.Debug().Err(err).Msg("stats/nvidia: failed to parse nvidia-smi XML")
		return nil
	}

	now := time.Now().UTC()
	var samples []Sample

	for i, gpu := range smiLog.GPUs {
		// Use minor_number as the gpu index label; fall back to enumeration index.
		idx := gpu.MinorNumber
		if idx == "" {
			idx = fmt.Sprintf("%d", i)
		}
		name := strings.TrimSpace(gpu.ProductName)
		labels := map[string]string{"gpu": idx, "name": name}

		if v, ok := parsePctField(gpu.Utilization.GPUUtil); ok {
			samples = append(samples, Sample{Sensor: "gpu_util_pct", Value: v, Unit: "pct", Labels: labels, TS: now})
		}
		if v, ok := parsePctField(gpu.Utilization.MemUtil); ok {
			samples = append(samples, Sample{Sensor: "mem_util_pct", Value: v, Unit: "pct", Labels: labels, TS: now})
		}
		if v, ok := parseMiBToBytes(gpu.FBMemory.Used); ok {
			samples = append(samples, Sample{Sensor: "mem_used_bytes", Value: v, Unit: "bytes", Labels: labels, TS: now})
		}
		if v, ok := parseMiBToBytes(gpu.FBMemory.Total); ok {
			samples = append(samples, Sample{Sensor: "mem_total_bytes", Value: v, Unit: "bytes", Labels: labels, TS: now})
		}
		if v, ok := parseCelsiusField(gpu.Temperature.GPUTemp); ok {
			samples = append(samples, Sample{Sensor: "temp_celsius", Value: v, Unit: "celsius", Labels: labels, TS: now})
		}
		if v, ok := parsePctField(gpu.FanSpeed); ok {
			samples = append(samples, Sample{Sensor: "fan_pct", Value: v, Unit: "pct", Labels: labels, TS: now})
		}
		if v, ok := parseWattsField(gpu.PowerReadings.PowerDraw); ok {
			samples = append(samples, Sample{Sensor: "power_watts", Value: v, Unit: "watts", Labels: labels, TS: now})
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(gpu.PCI.GPULinkInfo.PCIeGen.CurrentLinkGen), 64); err == nil {
			samples = append(samples, Sample{Sensor: "pcie_gen_current", Value: v, Unit: "count", Labels: labels, TS: now})
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(gpu.ECCErrors.Volatile.SRAMUncorrectable), 64); err == nil {
			samples = append(samples, Sample{Sensor: "ecc_uncorrectable_count", Value: v, Unit: "count", Labels: labels, TS: now})
		}
	}

	return samples
}

// parsePctField strips " %" and parses the float. Returns (0, false) for "N/A".
func parsePctField(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "n/a") {
		return 0, false
	}
	s = strings.TrimSuffix(s, " %")
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseMiBToBytes strips " MiB" and converts MiB → bytes.
func parseMiBToBytes(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "n/a") {
		return 0, false
	}
	s = strings.TrimSuffix(s, " MiB")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v * 1024 * 1024, true
}

// parseCelsiusField strips " C" and parses the float.
func parseCelsiusField(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "n/a") {
		return 0, false
	}
	s = strings.TrimSuffix(s, " C")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseWattsField strips " W" and parses the float.
func parseWattsField(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "n/a") {
		return 0, false
	}
	s = strings.TrimSuffix(s, " W")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
