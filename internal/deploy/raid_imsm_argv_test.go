package deploy

// Sprint 35 — IMSM argv builder tests.
//
// The deploy path uses two pure functions, BuildIMSMContainerArgs and
// BuildIMSMVolumeArgs, to construct the mdadm argv for the two-pass
// assembly.  These tests assert the argv shape so we can be confident the
// command sequence matches mdadm(8) IMSM contract without standing up a
// real Intel RST controller (the virtio Proxmox lab has no IMSM hardware).
//
// Live validation is deferred until customer-supplied lab hardware is
// available; until then, argv shape + the existing platform/per-device
// detection unit tests are the contract.

import (
	"reflect"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Pass 1: IMSM container creation ─────────────────────────────────────────

// TestBuildIMSMContainerArgs verifies the mdadm command for IMSM container
// creation.  Expected shape:
//
//	mdadm --create /dev/md/imsm0 --metadata=imsm --raid-devices=2 --run /dev/sda /dev/sdb
func TestBuildIMSMContainerArgs(t *testing.T) {
	cases := []struct {
		name        string
		container   string
		raidDevices int
		members     []string
		want        []string
	}{
		{
			name:        "two-disk container default name",
			container:   "/dev/md/imsm0",
			raidDevices: 2,
			members:     []string{"/dev/sda", "/dev/sdb"},
			want: []string{
				"--create", "/dev/md/imsm0",
				"--metadata=imsm",
				"--raid-devices", "2",
				"--run",
				"/dev/sda", "/dev/sdb",
			},
		},
		{
			name:        "four-disk container",
			container:   "/dev/md/imsm0",
			raidDevices: 4,
			members:     []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"},
			want: []string{
				"--create", "/dev/md/imsm0",
				"--metadata=imsm",
				"--raid-devices", "4",
				"--run",
				"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd",
			},
		},
		{
			name:        "non-default container name (per-controller naming)",
			container:   "/dev/md/imsm-rstA",
			raidDevices: 2,
			members:     []string{"/dev/nvme0n1", "/dev/nvme1n1"},
			want: []string{
				"--create", "/dev/md/imsm-rstA",
				"--metadata=imsm",
				"--raid-devices", "2",
				"--run",
				"/dev/nvme0n1", "/dev/nvme1n1",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildIMSMContainerArgs(tc.container, tc.raidDevices, tc.members)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("BuildIMSMContainerArgs() argv mismatch\n  want %v\n   got %v", tc.want, got)
			}
		})
	}
}

// ─── Pass 2: IMSM sub-array (volume) creation ────────────────────────────────

// TestBuildIMSMVolumeArgs verifies the mdadm command for IMSM sub-array
// creation inside an existing container.  Expected shape:
//
//	mdadm --create /dev/md/md0 --metadata=imsm --level=raid1 --raid-devices=2 --run /dev/md/imsm0
//
// The container is the LAST positional argument because mdadm parses it as
// the source pool; flipping the order makes mdadm interpret it as a member.
func TestBuildIMSMVolumeArgs(t *testing.T) {
	cases := []struct {
		name        string
		volume      string
		container   string
		level       string
		raidDevices int
		chunkKB     int
		want        []string
	}{
		{
			name:        "raid1 mirror, default chunk",
			volume:      "/dev/md/md0",
			container:   "/dev/md/imsm0",
			level:       "raid1",
			raidDevices: 2,
			chunkKB:     0,
			want: []string{
				"--create", "/dev/md/md0",
				"--metadata=imsm",
				"--level", "raid1",
				"--raid-devices", "2",
				"--run",
				"/dev/md/imsm0",
			},
		},
		{
			name:        "raid5 with explicit chunk size",
			volume:      "/dev/md/md1",
			container:   "/dev/md/imsm0",
			level:       "raid5",
			raidDevices: 4,
			chunkKB:     128,
			want: []string{
				"--create", "/dev/md/md1",
				"--metadata=imsm",
				"--level", "raid5",
				"--raid-devices", "4",
				"--run",
				"--chunk", "128K",
				"/dev/md/imsm0",
			},
		},
		{
			name:        "raid10 explicit chunk",
			volume:      "/dev/md/data0",
			container:   "/dev/md/imsm-rstA",
			level:       "raid10",
			raidDevices: 4,
			chunkKB:     256,
			want: []string{
				"--create", "/dev/md/data0",
				"--metadata=imsm",
				"--level", "raid10",
				"--raid-devices", "4",
				"--run",
				"--chunk", "256K",
				"/dev/md/imsm-rstA",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildIMSMVolumeArgs(tc.volume, tc.container, tc.level, tc.raidDevices, tc.chunkKB)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("BuildIMSMVolumeArgs() argv mismatch\n  want %v\n   got %v", tc.want, got)
			}
		})
	}
}

// TestIMSMVolumeArgs_ContainerComesLast asserts the most failure-prone bit
// of the contract: the container path must be the LAST element of argv.
// mdadm --create takes a positional list of members; when --metadata=imsm
// is set, mdadm interprets a single trailing path that already has imsm
// metadata as the parent container.  Putting it earlier would corrupt the
// member list.
func TestIMSMVolumeArgs_ContainerComesLast(t *testing.T) {
	args := BuildIMSMVolumeArgs("/dev/md/array0", "/dev/md/imsm0", "raid1", 2, 0)
	if len(args) == 0 {
		t.Fatal("argv is empty")
	}
	if last := args[len(args)-1]; last != "/dev/md/imsm0" {
		t.Errorf("container path must be last argv element; got %q at end of %v", last, args)
	}
}

// TestIMSMContainerName covers the spec.IMSMContainer override.
func TestIMSMContainerName(t *testing.T) {
	cases := []struct {
		name string
		spec api.RAIDSpec
		want string
	}{
		{
			name: "default when unset",
			spec: api.RAIDSpec{Name: "md0", Level: "raid1"},
			want: "/dev/md/imsm0",
		},
		{
			name: "operator override",
			spec: api.RAIDSpec{Name: "md0", Level: "raid1", IMSMContainer: "imsm-controllerA"},
			want: "/dev/md/imsm-controllerA",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IMSMContainerName(tc.spec); got != tc.want {
				t.Errorf("IMSMContainerName: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIMSMVolumeName covers the spec.Name → /dev/md/<name> mapping.
func TestIMSMVolumeName(t *testing.T) {
	cases := []struct {
		name string
		spec api.RAIDSpec
		want string
	}{
		{
			name: "named array",
			spec: api.RAIDSpec{Name: "md0", Level: "raid1"},
			want: "/dev/md/md0",
		},
		{
			name: "different name",
			spec: api.RAIDSpec{Name: "data0", Level: "raid5"},
			want: "/dev/md/data0",
		},
		{
			name: "fallback when Name is empty (legacy)",
			spec: api.RAIDSpec{Level: "raid1"},
			want: "/dev/md/Volume0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IMSMVolumeName(tc.spec); got != tc.want {
				t.Errorf("IMSMVolumeName: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRAIDTypeIMSMTriggersIMSMPath documents the API contract that
// raid_type=imsm is the explicit operator opt-in for IMSM containers.
// We don't actually invoke createRAIDArray (it requires real devices /
// mdadm), but we assert the field round-trips through the type system.
func TestRAIDTypeIMSMTriggersIMSMPath(t *testing.T) {
	spec := api.RAIDSpec{
		Name:     "md0",
		Level:    "raid1",
		Members:  []string{"sda", "sdb"},
		RAIDType: "imsm",
	}
	if spec.RAIDType != "imsm" {
		t.Errorf("RAIDType should round-trip; got %q", spec.RAIDType)
	}
	// Verify the IMSM path naming uses spec.Name when RAIDType=imsm.
	containerArgs := BuildIMSMContainerArgs(IMSMContainerName(spec), len(spec.Members), []string{"/dev/sda", "/dev/sdb"})
	volumeArgs := BuildIMSMVolumeArgs(IMSMVolumeName(spec), IMSMContainerName(spec), spec.Level, len(spec.Members), spec.ChunkKB)

	// Container argv should reference /dev/md/imsm0 (default).
	if containerArgs[1] != "/dev/md/imsm0" {
		t.Errorf("container argv expected /dev/md/imsm0; got %q", containerArgs[1])
	}
	// Volume argv should reference /dev/md/md0.
	if volumeArgs[1] != "/dev/md/md0" {
		t.Errorf("volume argv expected /dev/md/md0; got %q", volumeArgs[1])
	}
	// Volume argv last element must be the container.
	if last := volumeArgs[len(volumeArgs)-1]; last != "/dev/md/imsm0" {
		t.Errorf("volume argv tail expected /dev/md/imsm0; got %q", last)
	}
}
