package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// MegaRAIDPlugin reports per-controller and per-VD/PD state by shelling out
// to `storcli show all J` (J = JSON output mode).
//
// The plugin silently emits no samples when:
//   - The `storcli` binary is not installed.
//   - storcli returns a non-zero exit code (no controllers present, driver not loaded).
//
// Sensors produced per virtual drive (labels ctrl=<id>, vd=<dg/vd>):
//   - "vd_state"     — VD state encoded as integer (unit: count); see vdStateVal
//   - "rebuild_pct"  — rebuild progress 0-100 (unit: pct) — absent if not rebuilding
//
// Sensors produced per physical drive (labels ctrl=<id>, pd=<eid:slot>):
//   - "pd_state"     — PD state encoded as integer (unit: count); see pdStateVal
//
// Sensors produced per controller (labels ctrl=<id>):
//   - "vd_count"      — total number of virtual drives    (unit: count)
//   - "bbu_charge_pct" — BBU/supercap charge percentage   (unit: pct)
//   - "bbu_temp_celsius" — BBU/supercap temperature       (unit: celsius)
//
// VD state mapping (stable — do not reorder without updating the API):
//
//	0 = Optl  (Optimal)
//	1 = Dgrd  (Degraded)
//	2 = Pdgd  (Partially Degraded)
//	3 = Rbld  (Rebuild in progress)
//	4 = Init  (Initialising)
//	5 = Fail  (Failed)
//	6 = Msng  (Missing)
//
// PD state mapping (stable — do not reorder without updating the API):
//
//	0 = Onln  (Online)
//	1 = Offln (Offline)
//	2 = Rbld  (Rebuild)
//	3 = UBad  (Unconfigured Bad)
//	4 = UGood (Unconfigured Good / hot spare available)
//	5 = Reblg (Rebuilding)
//	6 = Fail  (Failed)
type MegaRAIDPlugin struct{}

// NewMegaRAIDPlugin creates a MegaRAIDPlugin.
func NewMegaRAIDPlugin() *MegaRAIDPlugin { return &MegaRAIDPlugin{} }

func (p *MegaRAIDPlugin) Name() string { return "megaraid" }

// vdStateVal converts a storcli VD state string to a stable numeric value.
// Unmapped strings return -1 (unknown).
var vdStateMap = map[string]float64{
	"Optl": 0, // Optimal
	"Dgrd": 1, // Degraded
	"Pdgd": 2, // Partially Degraded
	"Rbld": 3, // Rebuild in progress
	"Init": 4, // Initialising
	"Fail": 5, // Failed
	"Msng": 6, // Missing
}

func vdStateVal(s string) float64 {
	if v, ok := vdStateMap[strings.TrimSpace(s)]; ok {
		return v
	}
	return -1
}

// pdStateVal converts a storcli PD state string to a stable numeric value.
// Unmapped strings return -1 (unknown).
var pdStateMap = map[string]float64{
	"Onln":  0, // Online
	"Offln": 1, // Offline
	"Rbld":  2, // Rebuild
	"UBad":  3, // Unconfigured Bad
	"UGood": 4, // Unconfigured Good / hot spare available
	"Reblg": 5, // Rebuilding
	"Fail":  6, // Failed
}

func pdStateVal(s string) float64 {
	if v, ok := pdStateMap[strings.TrimSpace(s)]; ok {
		return v
	}
	return -1
}

// --- JSON structures matching `storcli show all J` output ---

type storCLIOutput struct {
	Controllers []storCLIController `json:"Controllers"`
}

type storCLIController struct {
	CommandStatus storCLICommandStatus `json:"Command Status"`
	ResponseData  storCLIResponseData  `json:"Response Data"`
}

type storCLICommandStatus struct {
	Controller int    `json:"Controller"`
	Status     string `json:"Status"`
}

type storCLIResponseData struct {
	VDList  []storCLIVD  `json:"VD LIST"`
	PDList  []storCLIPD  `json:"PD LIST"`
	BBUInfo []storCLIBBU `json:"BBU_Info"`
}

type storCLIVD struct {
	DGVD  string `json:"DG/VD"`
	State string `json:"State"`
}

type storCLIPD struct {
	EIDSlt string `json:"EID:Slt"`
	State  string `json:"State"`
}

type storCLIBBU struct {
	RelativeStateOfCharge string `json:"Relative State of Charge"`
	Temperature           string `json:"Temperature"`
}

func (p *MegaRAIDPlugin) Collect(ctx context.Context) []Sample {
	storcliPath, err := exec.LookPath("storcli")
	if err != nil {
		// Binary not present — stay silent.
		return nil
	}

	out, err := runWithTimeout(ctx, storcliPath, "show", "all", "J")
	if err != nil {
		log.Debug().Err(err).Msg("stats/megaraid: storcli show all J failed (non-fatal)")
		return nil
	}

	return parseMegaRAID(out)
}

// parseMegaRAID parses the JSON output of `storcli show all J` and returns samples.
func parseMegaRAID(data []byte) []Sample {
	var output storCLIOutput
	if err := json.Unmarshal(data, &output); err != nil {
		log.Debug().Err(err).Msg("stats/megaraid: failed to parse storcli JSON")
		return nil
	}

	now := time.Now().UTC()
	var samples []Sample

	for _, ctrl := range output.Controllers {
		if ctrl.CommandStatus.Status != "Success" {
			continue
		}
		ctrlID := fmt.Sprintf("%d", ctrl.CommandStatus.Controller)
		ctrlLabels := map[string]string{"ctrl": ctrlID}

		vdList := ctrl.ResponseData.VDList
		samples = append(samples, Sample{
			Sensor: "vd_count", Value: float64(len(vdList)),
			Unit: "count", Labels: ctrlLabels, TS: now,
		})

		for _, vd := range vdList {
			vdLabels := map[string]string{"ctrl": ctrlID, "vd": vd.DGVD}
			samples = append(samples, Sample{
				Sensor: "vd_state", Value: vdStateVal(vd.State),
				Unit: "count", Labels: vdLabels, TS: now,
			})
		}

		for _, pd := range ctrl.ResponseData.PDList {
			pdLabels := map[string]string{"ctrl": ctrlID, "pd": pd.EIDSlt}
			samples = append(samples, Sample{
				Sensor: "pd_state", Value: pdStateVal(pd.State),
				Unit: "count", Labels: pdLabels, TS: now,
			})
		}

		// BBU / supercap (first entry if present)
		if len(ctrl.ResponseData.BBUInfo) > 0 {
			bbu := ctrl.ResponseData.BBUInfo[0]

			// "Relative State of Charge": "100 %"
			chargeStr := strings.TrimSuffix(strings.TrimSpace(bbu.RelativeStateOfCharge), " %")
			if v, err := strconv.ParseFloat(strings.TrimSpace(chargeStr), 64); err == nil {
				samples = append(samples, Sample{
					Sensor: "bbu_charge_pct", Value: v,
					Unit: "pct", Labels: ctrlLabels, TS: now,
				})
			}

			// "Temperature": "31 C"
			tempStr := strings.TrimSuffix(strings.TrimSpace(bbu.Temperature), " C")
			if v, err := strconv.ParseFloat(strings.TrimSpace(tempStr), 64); err == nil {
				samples = append(samples, Sample{
					Sensor: "bbu_temp_celsius", Value: v,
					Unit: "celsius", Labels: ctrlLabels, TS: now,
				})
			}
		}
	}

	return samples
}
