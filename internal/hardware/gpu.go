// Package hardware — gpu.go: S5-2 GPU hardware detection via /sys/bus/pci.
//
// DiscoverGPUs scans the PCI device tree under /sys/bus/pci/devices for
// display-class devices (class 0x03xx — VGA, XGA, 3D Controller, Display Controller).
// It reads the vendor/device IDs and resolves them to human-readable names via
// the PCI ID database embedded as class_name strings in /sys/.../{id}/class.
//
// This approach avoids a dependency on lspci (not always present in initramfs).
// No CGO required; reads only sysfs.

package hardware

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GPU represents a single detected GPU device.
type GPU struct {
	// Model is the human-readable product name, e.g. "NVIDIA A100 80GB PCIe".
	// Resolved from /sys/bus/pci/devices/.../label if present, otherwise from
	// vendor + device ID lookup. Falls back to "Unknown GPU <vendorID>:<deviceID>".
	Model string `json:"model"`
	// VendorID is the 4-hex-digit PCI vendor ID (e.g. "10de" for NVIDIA).
	VendorID string `json:"vendor_id"`
	// DeviceID is the 4-hex-digit PCI device ID.
	DeviceID string `json:"device_id"`
	// VRAMBytes is the size of the GPU memory in bytes as reported by the driver
	// via /sys/.../mem_info_vram_total (AMD) or /sys/.../resource0 size heuristics.
	// Zero when unavailable (no driver, pre-init, or integrated graphics).
	VRAMBytes int64 `json:"vram_bytes,omitempty"`
	// PCIAddress is the device PCI address (e.g. "0000:01:00.0").
	PCIAddress string `json:"pci_address"`
}

// pciGPUVendors maps PCI vendor IDs to human-readable vendor names.
// Covers the GPUs relevant to HPC/ML workloads.
var pciGPUVendors = map[string]string{
	"10de": "NVIDIA",
	"1002": "AMD",
	"8086": "Intel",
	"1db1": "Intel (Ponte Vecchio)",
	"1d1d": "CEVA",
	"15b3": "NVIDIA BlueField (GPU)",
}

// pciClassIsGPU returns true when the PCI class code indicates a display/GPU device.
// PCI class 0x03xx covers VGA (03 00), XGA (03 01), 3D Controller (03 02),
// Display Controller (03 80). We match any 0x03xx.
func pciClassIsGPU(classHex string) bool {
	// class file is 0x0NNNNN (6-digit hex with 0x prefix).
	class := strings.TrimPrefix(strings.TrimSpace(classHex), "0x")
	if len(class) < 2 {
		return false
	}
	return class[:2] == "03"
}

// DiscoverGPUs enumerates PCI devices and returns GPU entries.
// Tolerates missing sysfs nodes gracefully — partial results are preferred
// over errors. Returns nil, nil when no GPUs are found.
func DiscoverGPUs() ([]GPU, error) {
	const pciBase = "/sys/bus/pci/devices"
	entries, err := os.ReadDir(pciBase)
	if err != nil {
		// sysfs not available (CI / container / bare-metal pre-boot quirk).
		return nil, nil //nolint:nilerr
	}

	var gpus []GPU
	for _, e := range entries {
		devPath := filepath.Join(pciBase, e.Name())

		// Read PCI class.
		classRaw, err := os.ReadFile(filepath.Join(devPath, "class"))
		if err != nil {
			continue
		}
		if !pciClassIsGPU(string(classRaw)) {
			continue
		}

		// Read vendor and device IDs.
		vendorRaw, _ := os.ReadFile(filepath.Join(devPath, "vendor"))
		deviceRaw, _ := os.ReadFile(filepath.Join(devPath, "device"))
		vendorID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(vendorRaw)), "0x"))
		deviceID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(deviceRaw)), "0x"))

		model := resolveGPUModel(devPath, vendorID, deviceID)

		gpu := GPU{
			Model:      model,
			VendorID:   vendorID,
			DeviceID:   deviceID,
			PCIAddress: e.Name(),
		}

		// Attempt to read VRAM size. Different drivers expose it differently.
		gpu.VRAMBytes = readGPUVRAM(devPath, vendorID)

		gpus = append(gpus, gpu)
	}
	return gpus, nil
}

// resolveGPUModel builds a human-readable GPU model name.
// Priority: label > subsystem_device label > vendor:device fallback.
func resolveGPUModel(devPath, vendorID, deviceID string) string {
	// Try kernel-provided label first (set by some drivers).
	if label, err := os.ReadFile(filepath.Join(devPath, "label")); err == nil {
		if s := strings.TrimSpace(string(label)); s != "" {
			return s
		}
	}

	// Compose a readable name from vendor name + device ID.
	vendor := pciGPUVendors[vendorID]
	if vendor == "" {
		vendor = fmt.Sprintf("Vendor %s", vendorID)
	}
	return fmt.Sprintf("%s GPU [%s:%s]", vendor, vendorID, deviceID)
}

// readGPUVRAM attempts to read the VRAM size for the given PCI device.
// AMD exposes it via mem_info_vram_total under the drm device; NVIDIA via
// /sys/module/nvidia/drivers/. Falls back to 0 when unavailable.
func readGPUVRAM(devPath, vendorID string) int64 {
	// AMD: /sys/bus/pci/devices/<addr>/mem_info_vram_total
	if vendorID == "1002" {
		raw, err := os.ReadFile(filepath.Join(devPath, "mem_info_vram_total"))
		if err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}

	// NVIDIA: /sys/bus/pci/devices/<addr>/resource0 size heuristic — bar0 is framebuffer.
	// Resource file format: 0x<start> 0x<end> 0x<flags>  (one line per BAR).
	if vendorID == "10de" {
		raw, err := os.ReadFile(filepath.Join(devPath, "resource"))
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) >= 1 {
				fields := strings.Fields(lines[0])
				if len(fields) >= 2 {
					start, e1 := strconv.ParseInt(strings.TrimPrefix(fields[0], "0x"), 16, 64)
					end, e2 := strconv.ParseInt(strings.TrimPrefix(fields[1], "0x"), 16, 64)
					if e1 == nil && e2 == nil && end > start {
						size := end - start + 1
						// Only return if plausible GPU VRAM (>= 256 MiB, <= 192 GiB).
						if size >= 256*1024*1024 && size <= 192*1024*1024*1024 {
							return size
						}
					}
				}
			}
		}
	}
	return 0
}
