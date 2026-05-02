// fake-syscfg is a test double for the Intel SYSCFG utility.
//
// It mimics the command-line interface expected by the intel bios provider:
//
//	/s -       → print current settings as key=value pairs (stdout)
//	/r <path>  → read a settings file and "apply" the settings (stdout confirmation)
//	/d         → list supported setting names (stdout)
//
// Settings are loaded from FAKE_SYSCFG_SETTINGS (JSON object env var).
// When FAKE_SYSCFG_SETTINGS is unset, a built-in canned set is used.
//
// Exit codes:
//
//	0 — success
//	1 — usage error (unknown flag / missing arg)
//	2 — simulated hardware error (set FAKE_SYSCFG_FAIL=1)
//
// Build:
//
//	go build -o fake-syscfg ./test/bios/fake-syscfg
//
// Usage in tests:
//
//	os.Setenv("FAKE_SYSCFG_SETTINGS", `{"HyperThreading":"Enabled","VTx":"Enabled"}`)
//	p := intel.NewWithBinaryPath("/path/to/fake-syscfg")
//	settings, _ := p.ReadCurrent(ctx)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultSettings is the canned setting set used when FAKE_SYSCFG_SETTINGS is unset.
var defaultSettings = map[string]string{
	"HyperThreading":     "Enabled",
	"VTx":               "Enabled",
	"VTd":               "Disabled",
	"TurboMode":         "Enabled",
	"PowerPerformance":  "MaxPerformance",
	"SecureBoot":        "Disabled",
	"QuietBoot":         "Enabled",
	"NetworkBoot":       "Enabled",
}

func main() {
	if os.Getenv("FAKE_SYSCFG_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "fake-syscfg: simulated hardware error (FAKE_SYSCFG_FAIL=1)")
		os.Exit(2)
	}

	settings := loadSettings()

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: fake-syscfg /s - | /r <file> | /d")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "/s":
		// /s -  → print current settings as key=value
		if len(os.Args) < 3 || os.Args[2] != "-" {
			fmt.Fprintln(os.Stderr, "Usage: fake-syscfg /s -")
			os.Exit(1)
		}
		printSettings(settings)

	case "/r":
		// /r <path> → apply settings from file
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: fake-syscfg /r <path>")
			os.Exit(1)
		}
		path := os.Args[2]
		if err := applySettings(path, settings); err != nil {
			fmt.Fprintf(os.Stderr, "fake-syscfg: apply error: %v\n", err)
			os.Exit(2)
		}
		fmt.Println("fake-syscfg: settings applied successfully (simulated)")

	case "/d":
		// /d → list supported setting names
		for name := range settings {
			fmt.Println(name)
		}

	default:
		fmt.Fprintf(os.Stderr, "fake-syscfg: unknown flag %q\n", os.Args[1])
		os.Exit(1)
	}
}

// loadSettings loads the settings map from FAKE_SYSCFG_SETTINGS or defaults.
func loadSettings() map[string]string {
	raw := os.Getenv("FAKE_SYSCFG_SETTINGS")
	if raw == "" {
		return defaultSettings
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		fmt.Fprintf(os.Stderr, "fake-syscfg: invalid FAKE_SYSCFG_SETTINGS JSON: %v\n", err)
		os.Exit(1)
	}
	return m
}

// printSettings emits settings in the syscfg /s - format:
//   KEY=VALUE\n ...
func printSettings(settings map[string]string) {
	// Sort for deterministic output.
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	// Simple insertion sort — small map, determinism over performance.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, name := range names {
		fmt.Printf("%s=%s\n", name, settings[name])
	}
}

// applySettings reads an INI-style key=value file (as written by intel.Apply)
// and prints a confirmation of what would be applied. In the fake, we just
// validate the file format and confirm.
//
// The intel provider writes the staging file as:
//
//	SettingName=DesiredValue\n
//	...
func applySettings(path string, current map[string]string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read settings file: %w", err)
	}

	desired := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		idx := strings.Index(line, "=")
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if name != "" {
			desired[name] = value
		}
	}

	var applied []string
	for name, value := range desired {
		if strings.EqualFold(current[name], value) {
			continue // already correct
		}
		applied = append(applied, fmt.Sprintf("  %s: %q -> %q", name, current[name], value))
	}
	if len(applied) == 0 {
		fmt.Println("fake-syscfg: no settings changed")
		return nil
	}
	fmt.Printf("fake-syscfg: applied %d setting(s):\n", len(applied))
	for _, line := range applied {
		fmt.Println(line)
	}
	return nil
}
