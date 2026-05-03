package hardware

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// Disk represents a block device discovered via lsblk.
type Disk struct {
	Name       string      // kernel device name, e.g. sda, nvme0n1
	Size       uint64      // total size in bytes
	Model      string
	Serial     string
	Transport  string      // sata, nvme, usb, etc.
	Rotational bool        // true = HDD, false = SSD/NVMe
	PhySector  int         // physical sector size in bytes
	LogSector  int         // logical sector size in bytes
	PtType     string      // partition table type: gpt, dos
	PtUUID     string      // partition table UUID
	Partitions []Partition
}

// Partition represents a disk partition from lsblk output.
type Partition struct {
	Name       string
	Size       uint64
	FSType     string
	MountPoint string
	PartUUID   string
	PartType   string  // GPT partition type GUID
	PartLabel  string
}

// lsblkOutput is the top-level JSON structure returned by lsblk --json.
type lsblkOutput struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

// lsblkDevice mirrors the lsblk JSON schema for a single block device.
type lsblkDevice struct {
	Name       string        `json:"name"`
	Size       uint64Json    `json:"size"`
	Type       string        `json:"type"`
	Model      string        `json:"model"`
	Serial     string        `json:"serial"`
	FSType     string        `json:"fstype"`
	MountPoint string        `json:"mountpoint"`
	Tran       string        `json:"tran"`
	Rota       bool          `json:"rota"`
	PhySec     int           `json:"phy-sec"`
	LogSec     int           `json:"log-sec"`
	PtType     string        `json:"pttype"`
	PtUUID     string        `json:"ptuuid"`
	PartUUID   string        `json:"partuuid"`
	PartType   string        `json:"parttype"`
	PartLabel  string        `json:"partlabel"`
	Children   []lsblkDevice `json:"children"`
}

// uint64Json handles lsblk's size field which may be a string or number
// depending on the lsblk version.
type uint64Json uint64

func (u *uint64Json) UnmarshalJSON(b []byte) error {
	// lsblk --bytes outputs a plain number, but some versions quote it
	var n uint64
	if err := json.Unmarshal(b, &n); err == nil {
		*u = uint64Json(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*u = 0
		return nil
	}
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fmt.Errorf("hardware/disk: cannot parse size %q: %w", s, err)
	}
	*u = uint64Json(n)
	return nil
}

// lsblkColumns is the fixed column set we request from lsblk. The order
// matters because it must match the JSON field names above.
const lsblkColumns = "NAME,SIZE,TYPE,MODEL,SERIAL,FSTYPE,MOUNTPOINT,TRAN,ROTA,PHY-SEC,LOG-SEC,PTTYPE,PTUUID,PARTUUID,PARTTYPE,PARTLABEL"

// DiscoverDisks invokes lsblk to enumerate all block devices and their
// partitions. Only top-level disk devices (type == "disk") are returned;
// partitions are nested under their parent Disk.
func DiscoverDisks() ([]Disk, error) {
	return discoverDisksFromOutput(lsblkRunner)
}

// lsblkRunner is the real lsblk executor. It is a package-level variable so
// tests can substitute a fake without subprocesses.
var lsblkRunner = func() ([]byte, error) {
	return exec.Command("lsblk", "--json", "--bytes",
		"--output", lsblkColumns).Output()
}

func discoverDisksFromOutput(runner func() ([]byte, error)) ([]Disk, error) {
	raw, err := runner()
	if err != nil {
		return nil, fmt.Errorf("hardware/disk: lsblk failed: %w", err)
	}
	return parseLsblkJSON(raw)
}

func parseLsblkJSON(raw []byte) ([]Disk, error) {
	var out lsblkOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("hardware/disk: parse lsblk JSON: %w", err)
	}

	var disks []Disk
	for _, dev := range out.Blockdevices {
		if dev.Type != "disk" {
			continue
		}
		d := Disk{
			Name:       dev.Name,
			Size:       uint64(dev.Size),
			Model:      dev.Model,
			Serial:     dev.Serial,
			Transport:  dev.Tran,
			Rotational: dev.Rota,
			PhySector:  dev.PhySec,
			LogSector:  dev.LogSec,
			PtType:     dev.PtType,
			PtUUID:     dev.PtUUID,
		}
		for _, child := range dev.Children {
			if child.Type != "part" {
				continue
			}
			d.Partitions = append(d.Partitions, Partition{
				Name:       child.Name,
				Size:       uint64(child.Size),
				FSType:     child.FSType,
				MountPoint: child.MountPoint,
				PartUUID:   child.PartUUID,
				PartType:   child.PartType,
				PartLabel:  child.PartLabel,
			})
		}
		disks = append(disks, d)
	}
	return disks, nil
}
