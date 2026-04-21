package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// LLDPResult holds the outcome of an LLDP topology validation check.
type LLDPResult struct {
	// Match is true when the node's LLDP data agrees with the expected switch/port.
	Match         bool   `json:"match"`
	FoundSwitch   string `json:"found_switch,omitempty"`
	FoundPort     string `json:"found_port,omitempty"`
	ExpectedSwitch string `json:"expected_switch,omitempty"`
	ExpectedPort  string `json:"expected_port,omitempty"`
	// RawOutput preserves the raw lldpcli JSON for debugging.
	RawOutput     string `json:"raw_output,omitempty"`
}

// lldpNeighbor is the minimal structure we parse from `lldpcli show neighbors -f json`.
// Only the fields we care about for validation are extracted.
type lldpNeighbor struct {
	Chassis map[string]struct {
		Name struct {
			Value string `json:"value"`
		} `json:"name"`
		ID struct {
			Value string `json:"value"`
		} `json:"id"`
	} `json:"chassis"`
	Port map[string]struct {
		ID struct {
			Value string `json:"value"`
		} `json:"id"`
		Descr struct {
			Value string `json:"value"`
		} `json:"descr"`
	} `json:"port"`
}

type lldpShowOutput struct {
	Lldp struct {
		Interface map[string]struct {
			Neighbors []struct {
				Rid    string       `json:"rid"`
				Neighbor lldpNeighbor `json:"chassis"`
				// lldpcli JSON nests chassis and port differently per version; we
				// unmarshal the raw message and iterate via a loose map.
			} `json:"neighbor"`
		} `json:"interface"`
	} `json:"lldp"`
}

// validateLLDP runs lldpcli inside the deployed rootfs chroot and checks
// whether the node is connected to the expected switch and port.
//
// This is a best-effort step: lldpd must be installed and running in the
// initramfs or deployed OS for this to yield data. Non-fatal on error.
//
// If expectedSwitch or expectedPort is empty, only the presence of any LLDP
// neighbor is checked; Match=true if at least one neighbor is found.
func validateLLDP(ctx context.Context, mountRoot, expectedSwitch, expectedPort string) (*LLDPResult, error) {
	result := &LLDPResult{
		ExpectedSwitch: expectedSwitch,
		ExpectedPort:   expectedPort,
	}

	// Run lldpcli inside the chroot.
	cmd := exec.CommandContext(ctx,
		"chroot", mountRoot,
		"lldpcli", "show", "neighbors", "-f", "json",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return result, fmt.Errorf("lldp: lldpcli failed: %w (stderr: %s)", err, stderr.String())
	}

	raw := stdout.String()
	result.RawOutput = raw

	// Parse the lldpcli JSON output. The schema varies between lldpd versions;
	// we use a loose map-based approach to extract the first neighbor's chassis
	// name and port ID without coupling to a specific schema version.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return result, fmt.Errorf("lldp: parse output: %w", err)
	}

	foundSwitch, foundPort := extractFirstNeighbor(parsed)
	result.FoundSwitch = foundSwitch
	result.FoundPort = foundPort

	if expectedSwitch == "" && expectedPort == "" {
		result.Match = foundSwitch != ""
		return result, nil
	}

	switchMatch := expectedSwitch == "" ||
		strings.EqualFold(foundSwitch, expectedSwitch) ||
		strings.Contains(strings.ToLower(foundSwitch), strings.ToLower(expectedSwitch))
	portMatch := expectedPort == "" ||
		strings.EqualFold(foundPort, expectedPort) ||
		strings.Contains(strings.ToLower(foundPort), strings.ToLower(expectedPort))

	result.Match = switchMatch && portMatch
	return result, nil
}

// extractFirstNeighbor walks the lldpcli JSON tree and returns the chassis name
// and port ID of the first LLDP neighbor found. Returns empty strings if none found.
//
// The lldpcli JSON structure (as of lldpd 1.0.x):
//
//	{"lldp": {"interface": {"eth0": {"via": "LLDP", "rid": "1",
//	  "age": "...", "chassis": {"switch-name": {"name": [{"value": "switch-name"}],
//	  "id": [{"type": "mac", "value": "aa:bb:cc:..."}]}},
//	  "port": {"port-id": {"id": [{"type": "ifname", "value": "Eth1"}],
//	  "descr": [{"value": "..."}]}}}}}}
func extractFirstNeighbor(parsed map[string]interface{}) (chassisName, portID string) {
	lldp, _ := parsed["lldp"].(map[string]interface{})
	if lldp == nil {
		return
	}
	ifaces, _ := lldp["interface"].(map[string]interface{})
	if ifaces == nil {
		return
	}
	for _, ifaceVal := range ifaces {
		iface, _ := ifaceVal.(map[string]interface{})
		if iface == nil {
			continue
		}
		chassis, _ := iface["chassis"].(map[string]interface{})
		port, _ := iface["port"].(map[string]interface{})

		// Extract chassis name: first key in the chassis map.
		for chassisKey, chassisData := range chassis {
			chassisName = chassisKey // fallback: use map key as name
			if cd, ok := chassisData.(map[string]interface{}); ok {
				if nameArr, ok := cd["name"].([]interface{}); ok && len(nameArr) > 0 {
					if nameObj, ok := nameArr[0].(map[string]interface{}); ok {
						if v, ok := nameObj["value"].(string); ok && v != "" {
							chassisName = v
						}
					}
				}
			}
			break
		}

		// Extract port ID: first key in the port map.
		for _, portData := range port {
			if pd, ok := portData.(map[string]interface{}); ok {
				if idArr, ok := pd["id"].([]interface{}); ok && len(idArr) > 0 {
					if idObj, ok := idArr[0].(map[string]interface{}); ok {
						if v, ok := idObj["value"].(string); ok {
							portID = v
						}
					}
				}
			}
			break
		}
		return // return after first interface
	}
	return
}

// RunLLDPValidation is the integration point called from applyNodeConfig (Step 10).
// It is non-fatal — on error or mismatch, a warning is logged but deployment continues.
func RunLLDPValidation(ctx context.Context, mountRoot, expectedSwitch, expectedPort string) {
	log := logger()

	result, err := validateLLDP(ctx, mountRoot, expectedSwitch, expectedPort)
	if err != nil {
		log.Warn().Err(err).Msg("finalize: LLDP validation skipped (lldpcli not available or failed — non-fatal)")
		return
	}

	if result.Match {
		log.Info().
			Str("found_switch", result.FoundSwitch).
			Str("found_port", result.FoundPort).
			Str("expected_switch", expectedSwitch).
			Str("expected_port", expectedPort).
			Msg("finalize: LLDP topology validation passed")
	} else {
		log.Warn().
			Str("found_switch", result.FoundSwitch).
			Str("found_port", result.FoundPort).
			Str("expected_switch", expectedSwitch).
			Str("expected_port", expectedPort).
			Msg("WARNING finalize: LLDP topology mismatch — node may be cabled to wrong switch port (non-fatal, continuing deploy)")
	}
}
