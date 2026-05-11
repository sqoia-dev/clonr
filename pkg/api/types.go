// Package api defines the shared wire types used by clustr-serverd and the clustr CLI.
// All JSON field names here are authoritative — the REST API contract.
package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// KeyScope defines the access level of an API key.
type KeyScope string

const (
	KeyScopeAdmin    KeyScope = "admin"    // full access to all admin routes
	KeyScopeOperator KeyScope = "operator" // operator session scope: admin routes minus key/user management
	KeyScopeNode     KeyScope = "node"     // limited: register, deploy-complete, logs ingest
)

// ImageStatus represents the lifecycle state of a BaseImage.
type ImageStatus string

const (
	ImageStatusBuilding    ImageStatus = "building"
	ImageStatusReady       ImageStatus = "ready"
	ImageStatusError       ImageStatus = "error"
	ImageStatusArchived    ImageStatus = "archived"
	ImageStatusInterrupted ImageStatus = "interrupted" // F2/F3: build interrupted, resumable

	// ImageStatusCorrupt means the on-disk blob disagrees with the DB checksum and
	// there is insufficient corroborating evidence to auto-heal (reconcile F2/F3/F5).
	// Operator must investigate; use POST /images/:id/reconcile to re-check or
	// force-re-finalize after manual repair.
	ImageStatusCorrupt ImageStatus = "corrupt"

	// ImageStatusBlobMissing means the DB row exists but the blob file is absent
	// from disk (reconcile F4). Operator must restore from backup or delete the row.
	ImageStatusBlobMissing ImageStatus = "blob_missing"
)

// ImageFormat describes how the image blob is stored on disk.
type ImageFormat string

const (
	ImageFormatFilesystem ImageFormat = "filesystem" // tar archive of a root filesystem
	ImageFormatBlock      ImageFormat = "block"      // raw block device image (partclone/dd)
)

// ImageFirmware identifies the firmware interface the image was built for.
// Allowed values: "uefi" (default, OVMF/EDK2) and "bios" (legacy SeaBIOS / i386-pc GRUB).
type ImageFirmware string

const (
	// FirmwareUEFI is the default — OVMF pflash drives in QEMU, efibootmgr on deploy.
	FirmwareUEFI ImageFirmware = "uefi"
	// FirmwareBIOS targets legacy BIOS nodes: SeaBIOS in the installer VM,
	// grub2-install --target=i386-pc at deploy time. GPT+biosboot partition is used
	// so disks >2 TB are supported.
	FirmwareBIOS ImageFirmware = "bios"
)

// FstabEntry describes a single mount to add to /etc/fstab during finalization.
// Entries are stored on NodeConfig and NodeGroup; the effective list is the
// group entries merged with node entries (node overrides group by mount point).
type FstabEntry struct {
	Source     string `json:"source"`            // e.g. "nfs-server:/export/home"
	MountPoint string `json:"mount_point"`       // e.g. "/home/shared"
	FSType     string `json:"fs_type"`           // "nfs", "nfs4", "cifs", "lustre", …
	Options    string `json:"options"`           // "defaults,_netdev,vers=4"
	Dump       int    `json:"dump"`              // usually 0
	Pass       int    `json:"pass"`              // usually 0 for network mounts
	AutoMkdir  bool   `json:"auto_mkdir"`        // create mount point if missing
	Comment    string `json:"comment,omitempty"` // human-readable note
}

// NodeGroup is a named set of nodes that share a disk layout override and other
// configuration. Nodes may optionally belong to a group; when they do, the
// group's DiskLayoutOverride takes precedence over the image default but is
// overridden by a node-level DiskLayoutOverride.
type NodeGroup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Role is an optional HPC role label: "compute", "login", "storage", "gpu", "admin".
	Role               string       `json:"role,omitempty"`
	DiskLayoutOverride *DiskLayout  `json:"disk_layout_override,omitempty"` // nil = use image default
	ExtraMounts        []FstabEntry `json:"extra_mounts,omitempty"`
	// ExpiresAt is an optional UTC timestamp after which this allocation is
	// considered expired. Nil means no expiration. Set by admin or PI via
	// PUT /api/v1/node-groups/{id}/expiration (Sprint F, v1.5.0).
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// NodeGroupWithCount embeds NodeGroup and adds a live member count from the
// node_group_memberships table.
type NodeGroupWithCount struct {
	NodeGroup
	MemberCount int `json:"member_count"`
}

// DiskLayout describes the partition schema expected on a target node.
// It is part of BaseImage — never per-node.
type DiskLayout struct {
	// RAIDArrays defines software RAID arrays to create before partitioning.
	// Arrays are created first; PartitionSpec.Device may reference an array name
	// (e.g. "md0") to partition on top of a RAID array instead of a raw disk.
	RAIDArrays []RAIDSpec `json:"raid_arrays,omitempty"`
	// ZFSPools defines ZFS zpools to create during deployment.
	// When non-empty, ZFS pool creation replaces the standard mkfs+mount flow for
	// the affected devices. Supported vdev types: "mirror", "raidz".
	// v1 constraint: single rpool (root) with optional bpool (/boot) only.
	ZFSPools   []ZFSPool       `json:"zfs_pools,omitempty"`
	Partitions []PartitionSpec `json:"partitions"`
	Bootloader Bootloader      `json:"bootloader"`
	// TargetDevice is an optional operator hint specifying the preferred kernel
	// device name (e.g. "nvme0n1") to deploy to. When set, selectTargetDisk
	// will prefer this device over automatic selection heuristics.
	TargetDevice string `json:"target_device,omitempty"`
}

// ZFSPool describes a ZFS zpool to create during deployment.
// v1 supports mirror and raidz vdev types with a single root dataset.
type ZFSPool struct {
	// Name is the zpool name, e.g. "rpool" or "bpool".
	Name string `json:"name"`
	// VdevType is the vdev topology: "mirror", "raidz", or "stripe" (no keyword).
	// Use "stripe" for a single-disk or striped pool.
	VdevType string `json:"vdev_type"`
	// Members lists the member devices by kernel name (e.g. ["sda3", "sdb3"])
	// or whole-disk names (e.g. ["sda", "sdb"]).
	Members []string `json:"members"`
	// Mountpoint is where the root dataset of this pool mounts, e.g. "/" or "/boot".
	Mountpoint string `json:"mountpoint"`
	// Properties is a map of zpool/dataset properties to set at creation time,
	// e.g. {"ashift": "12", "compression": "lz4"}.
	Properties map[string]string `json:"properties,omitempty"`
}

// RAIDSpec describes a software RAID array to create during deployment.
type RAIDSpec struct {
	// Name is the md device name, e.g. "md0".
	Name string `json:"name"`
	// Level is the RAID level: "raid0", "raid1", "raid5", "raid6", "raid10".
	Level string `json:"level"`
	// Members lists the member devices by kernel name (e.g. "sda", "sdb") or
	// by size-based selector (e.g. "smallest-2" = the two smallest disks).
	Members []string `json:"members"`
	// ChunkKB is the chunk size in KiB. When 0, mdadm picks the default for
	// the RAID level (typically 512K for raid0/5/6/10, unused for raid1).
	ChunkKB int `json:"chunk_kb,omitempty"`
	// Spare is the number of hot spare devices to include in the array.
	Spare int `json:"spare,omitempty"`
	// ForceSoftware, when true, bypasses IMSM hardware-RAID detection and
	// always assembles this array as a software md RAID regardless of whether
	// an IMSM-capable controller is present. Default false: the deploy path
	// routes to IMSM assembly when the platform supports it.
	ForceSoftware bool `json:"force_software,omitempty"`
	// RAIDType is the explicit assembly type for this array.  One of:
	//   ""     — auto.  Use platform/per-device IMSM detection (legacy path).
	//   "imsm" — operator-opted-in Intel IMSM hardware RAID.  Skip
	//            autodetection and always go through createIMSMArray.
	//   "md"   — explicit software md RAID.  Equivalent to ForceSoftware=true.
	// Sprint 35 / IMSM: layouts that explicitly want IMSM containers set
	// raid_type=imsm so the selection is deterministic and not platform-dependent.
	RAIDType string `json:"raid_type,omitempty"`
	// IMSMContainer is the device-name fragment to use for the IMSM container
	// that holds this sub-array.  Defaults to "imsm0" when empty.  Multiple
	// arrays sharing the same IMSMContainer are co-located in the same
	// IMSM container (mirrors clustervisor disk_raid_imsm semantics where
	// volumes are sub-arrays of one container per controller).
	IMSMContainer string `json:"imsm_container,omitempty"`
}

// PartitionSpec describes a single partition within a DiskLayout.
type PartitionSpec struct {
	// Device is the target block device for this partition. If empty, the
	// deployer uses the automatically selected target disk. If set to an md
	// device name (e.g. "md0"), the partition is created on that RAID array.
	Device     string   `json:"device,omitempty"`
	Label      string   `json:"label"`
	SizeBytes  int64    `json:"size_bytes"` // 0 = fill remaining
	Filesystem string   `json:"filesystem"` // "xfs", "ext4", "vfat", "swap"
	MountPoint string   `json:"mountpoint"`
	Flags      []string `json:"flags"`     // ["boot", "esp"]
	MinBytes   int64    `json:"min_bytes"` // minimum disk size to satisfy this layout
}

// Bootloader specifies which bootloader is used and its target platform.
type Bootloader struct {
	Type   string `json:"type"`   // "grub2", "systemd-boot"
	Target string `json:"target"` // "x86_64-efi", "i386-pc"
}

// AnchorPair marks a named region within a target file so that two plugins
// can each own a distinct section of the same file without overwriting each
// other. Both Begin and End are matched on whole-line equality (no substring
// matching). Setting Anchors on any opcode other than "overwrite" is a
// deploy-time error.
type AnchorPair struct {
	// Begin is the exact line that marks the start of the managed region,
	// e.g. "# BEGIN clustr/limits-slurm".
	Begin string `json:"begin"`
	// End is the exact line that marks the end of the managed region,
	// e.g. "# END clustr/limits-slurm".
	End string `json:"end"`
}

// InstallInstruction is a single step run inside the deployed filesystem during
// the in-chroot phase of every deploy, AFTER node-identity config is applied
// and BEFORE bootloader installation. Instructions are run in order; the image
// author is responsible for idempotency on re-deploys.
//
// Opcode semantics:
//
//   - "modify"    — find-and-replace within an existing file. Payload is a
//     JSON-encoded {"find": "<regex>", "replace": "<string>"}.
//     Target must exist; if it does not, the deploy fails.
//
//   - "overwrite" — write Payload (as text) to Target, replacing if present.
//     Mode 0644 is used unless the file already exists with a
//     different mode (in which case the existing mode is preserved).
//     Target's parent directory must already exist.
//     When Anchors is non-nil, only the region between the anchor lines is
//     replaced; content outside the markers is preserved byte-for-byte.
//
//   - "script"    — write Payload as a POSIX shell script to a temp file and
//     run it inside the target via chroot(2). Fails the deploy if
//     the script exits non-zero.
type InstallInstruction struct {
	Opcode  string      `json:"opcode"`            // "modify" | "overwrite" | "script"
	Target  string      `json:"target"`            // path within the chrooted target root
	Payload string      `json:"payload"`           // semantics depend on opcode
	Anchors *AnchorPair `json:"anchors,omitempty"` // optional; only honoured for opcode "overwrite"
}

// BaseImage is a deployable OS image, stripped of all node-specific identity.
// It is immutable once finalized (Status == ImageStatusReady).
type BaseImage struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Version string      `json:"version"`
	OS      string      `json:"os"`
	Arch    string      `json:"arch"`
	Status  ImageStatus `json:"status"`
	Format  ImageFormat `json:"format"`
	// Firmware identifies the firmware interface this image was built for.
	// "uefi" (default) or "bios" (legacy). Existing images without this field
	// stored default to "uefi" via the DB column DEFAULT.
	Firmware     ImageFirmware `json:"firmware"`
	SizeBytes    int64         `json:"size_bytes"`
	Checksum     string        `json:"checksum"` // sha256 hex of the blob
	DiskLayout   DiskLayout    `json:"disk_layout"`
	Tags         []string      `json:"tags"`
	SourceURL    string        `json:"source_url,omitempty"`
	Notes        string        `json:"notes"`
	ErrorMessage string        `json:"error_message,omitempty"`
	// BuildMethod identifies how the image was created: "pull", "import", "capture", "iso".
	// Used by the UI to decide which detail view to show (e.g. build progress panel).
	BuildMethod string `json:"build_method,omitempty"`
	// BuiltForRoles holds the HPC role IDs that were selected when the image was
	// built via the Build from ISO flow. Used by the node-assignment UI to warn
	// when a node's role tag doesn't match the image's built-for roles.
	BuiltForRoles []string `json:"built_for_roles,omitempty"`
	// InstallInstructions is an ordered list of filesystem mutations applied
	// inside the deployed root during every deploy's in-chroot phase.
	// Omitted from the API response when empty.
	InstallInstructions []InstallInstruction `json:"install_instructions,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
	FinalizedAt         *time.Time           `json:"finalized_at,omitempty"`
}

// InterfaceConfig holds the static network configuration for one NIC on a node.
type InterfaceConfig struct {
	MACAddress string   `json:"mac_address"`
	Name       string   `json:"name"`       // "eth0", "ens3"
	IPAddress  string   `json:"ip_address"` // CIDR: "192.168.1.50/24"
	Gateway    string   `json:"gateway,omitempty"`
	DNS        []string `json:"dns,omitempty"`
	MTU        int      `json:"mtu,omitempty"`
	Bond       string   `json:"bond,omitempty"`
}

// BMCNodeConfig holds IPMI/BMC network and credential configuration applied
// during node finalization. The password field is write-only — it is applied
// on the node itself and is never returned by the API.
type BMCNodeConfig struct {
	IPAddress string `json:"ip_address"`
	Netmask   string `json:"netmask"`
	Gateway   string `json:"gateway"`
	Username  string `json:"username"`
	Password  string `json:"password"` // applied during finalize, never returned by API
}

// IBInterfaceConfig holds per-device InfiniBand / IPoIB configuration applied
// during node finalization.
type IBInterfaceConfig struct {
	DeviceName string   `json:"device_name"`          // e.g. "mlx5_0"
	PKeys      []string `json:"pkeys"`                // partition keys, e.g. ["0x8001"]
	IPoIBMode  string   `json:"ipoib_mode"`           // "connected" or "datagram"
	IPAddress  string   `json:"ip_address,omitempty"` // IPoIB IP in CIDR notation
	MTU        int      `json:"mtu,omitempty"`        // typically 65520 for connected mode
}

// PowerProviderConfig holds the type and backend-specific fields for a node's
// power management provider. The "type" field selects the backend ("ipmi",
// "proxmox", …); "fields" carries backend-specific key/value pairs.
//
// Security: Fields may contain credentials. Always call Sanitize() before
// returning this struct in an API response.
type PowerProviderConfig struct {
	Type   string            `json:"type"`
	Fields map[string]string `json:"fields"`
}

// sensitiveFields lists the key names whose values are redacted by Sanitize.
var sensitiveFields = []string{
	"password", "token_secret", "secret", "api_key", "api_secret",
}

// Sanitize returns a copy of c with credential fields replaced by "****".
// Always call this before including a PowerProviderConfig in an API response.
func (c *PowerProviderConfig) Sanitize() *PowerProviderConfig {
	if c == nil {
		return nil
	}
	out := &PowerProviderConfig{
		Type:   c.Type,
		Fields: make(map[string]string, len(c.Fields)),
	}
	for k, v := range c.Fields {
		out.Fields[k] = v
	}
	for _, name := range sensitiveFields {
		if _, ok := out.Fields[name]; ok {
			out.Fields[name] = "****"
		}
	}
	return out
}

// NodeState enumerates the lifecycle states of a NodeConfig.
// The state is derived from existing fields via NodeConfig.State() rather than
// stored as a separate column, so it cannot drift from the underlying data.
type NodeState string

const (
	// NodeStateRegistered: node has PXE-booted and self-registered but no image
	// has been assigned yet. The node is idle, waiting for admin action.
	NodeStateRegistered NodeState = "registered"

	// NodeStateConfigured: a base image has been assigned but the node has not
	// yet run a successful deployment. Next PXE boot will trigger a deploy.
	NodeStateConfigured NodeState = "configured"

	// NodeStateDeploying: reserved for future use when a deploy is actively
	// in-flight and the server can observe it via progress callbacks.
	NodeStateDeploying NodeState = "deploying"

	// NodeStateDeployed: the most recent deploy succeeded and reimage_pending is
	// false. The PXE handler returns "exit" so the node boots from local disk.
	NodeStateDeployed NodeState = "deployed"

	// NodeStateReimagePending: admin has requested a reimage. The next PXE boot
	// will trigger a fresh deploy regardless of prior deployment state.
	NodeStateReimagePending NodeState = "reimage_pending"

	// NodeStateFailed: the most recent deploy failed and no successful deploy has
	// occurred since. Needs admin attention.
	NodeStateFailed NodeState = "failed"

	// NodeStateDeployedPreboot: deploy-complete callback received from clustr-static
	// inside the PXE initramfs. Rootfs written successfully. Waiting for the OS to
	// phone home via POST /verify-boot to confirm the bootloader + kernel work.
	// ADR-0008.
	NodeStateDeployedPreboot NodeState = "deployed_preboot"

	// NodeStateDeployedVerified: OS phoned home after first boot. Bootloader, kernel,
	// and systemd all started. This is the terminal success state. ADR-0008.
	NodeStateDeployedVerified NodeState = "deployed_verified"

	// NodeStateBiosApplying: a bios_only reimage is in progress. The node is in
	// initramfs applying BIOS settings. Transitions back to the previous state
	// after the apply completes (or to NodeStateFailed on error).
	NodeStateBiosApplying NodeState = "bios_applying"

	// NodeStateDeployVerifyTimeout: verify-boot was not received within
	// CLUSTR_VERIFY_TIMEOUT after deploy_completed_preboot_at. Indicates a likely
	// bootloader, kernel, or network failure. Needs operator attention. ADR-0008.
	NodeStateDeployVerifyTimeout NodeState = "deploy_verify_timeout"

	// NodeStateDeployedLDAPFailed: the OS phoned home post-boot (so the
	// bootloader+kernel work) but the LDAP module is enabled cluster-wide and
	// the verify-boot payload reports sssd is not active or pam_sss.so is not
	// present. Indicates LDAP client setup failed in the deployed image —
	// users cannot log in via the cluster directory. Operator must reimage
	// (or fix the image) to recover. v0.1.15.
	//
	// Distinct from NodeStateDeployedVerified: a node that booted but is
	// unable to authenticate cluster users is NOT functionally ready.
	NodeStateDeployedLDAPFailed NodeState = "deployed_ldap_failed"
)

// SystemGroup is a local POSIX group to be injected into every deployed node.
type SystemGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	GID         int       `json:"gid"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SystemAccount is a local POSIX user account to be injected into every deployed node.
type SystemAccount struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	UID           int       `json:"uid"`
	PrimaryGID    int       `json:"primary_gid"`
	Shell         string    `json:"shell"`
	HomeDir       string    `json:"home_dir"`
	CreateHome    bool      `json:"create_home"`
	SystemAccount bool      `json:"system_account"`
	Comment       string    `json:"comment"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SystemAccountsNodeConfig carries the full set of accounts and groups to inject
// during finalization. Populated at reimage-request time; always reflects the
// current DB state at the moment the deploy starts.
type SystemAccountsNodeConfig struct {
	Groups   []SystemGroup   `json:"groups"`
	Accounts []SystemAccount `json:"accounts"`
}

// LDAPNodeConfig holds the read-only LDAP client configuration injected into
// a node's deployed filesystem during finalization. It carries the service
// account credentials — NEVER the Directory Manager (admin) credentials.
// The admin password is held only in clustr-serverd's memory and is never
// templated into any node asset.
type LDAPNodeConfig struct {
	// ServerURI is the ldaps:// URI of the slapd server, e.g. "ldaps://clustr-server:636".
	ServerURI string `json:"server_uri"`
	// BaseDN is the LDAP base DN, e.g. "dc=cluster,dc=local".
	BaseDN string `json:"base_dn"`
	// ServiceBindDN is the read-only service account DN used by nodes.
	// e.g. "cn=node-reader,ou=services,dc=cluster,dc=local"
	ServiceBindDN string `json:"service_bind_dn"`
	// ServiceBindPasswd is the plaintext password for the service account.
	// This is stored in sssd.conf on each node and should be treated as
	// a low-privilege read-only credential.
	ServiceBindPasswd string `json:"service_bind_passwd"`
	// CACertPEM is the PEM-encoded CA certificate used to verify the slapd TLS cert.
	// Written to multiple locations on the node during finalization.
	CACertPEM string `json:"ca_cert_pem"`
}

// SudoersNodeConfig holds the LDAP group-based sudoers configuration injected
// into a node's deployed filesystem during finalization. When non-nil, a sudoers
// drop-in file is written to /etc/sudoers.d/<group_cn> so members of the LDAP
// group can run sudo on the deployed node. SSSD resolves membership at sudo time;
// no per-user file push is required.
type SudoersNodeConfig struct {
	// GroupCN is the CN of the LDAP posixGroup whose members receive sudo access.
	// E.g. "clustr-admins". The drop-in file is named after this CN.
	GroupCN string `json:"group_cn"`
	// NoPasswd, when true, writes NOPASSWD:ALL so members can sudo without a password.
	NoPasswd bool `json:"no_passwd"`
}

// HostEntry represents a single /etc/hosts entry for a cluster node.
// Populated transiently at registration time; never stored in the database.
type HostEntry struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	FQDN     string `json:"fqdn,omitempty"`
	// Aliases holds additional hostnames written after Hostname on the same line.
	// Used to add service-specific aliases (e.g. "clustr-server" for LDAP resolution).
	Aliases []string `json:"aliases,omitempty"`
}

// NodeProviderValues lists the valid values for NodeConfig.Provider.
// Empty string means "not set / unknown". "ipmi" and "proxmox" match the
// registered power-backend types in internal/power/.
var NodeProviderValues = []string{"", "ipmi", "proxmox"}

// IsValidNodeProvider reports whether s is an accepted provider value.
func IsValidNodeProvider(s string) bool {
	for _, v := range NodeProviderValues {
		if s == v {
			return true
		}
	}
	return false
}

// NodeConfig holds everything that makes a deployed image specific to one
// physical node. Applied at deploy time — never baked into the BaseImage blob.
type NodeConfig struct {
	ID           string            `json:"id"`
	Hostname     string            `json:"hostname"`
	HostnameAuto bool              `json:"hostname_auto"`
	FQDN         string            `json:"fqdn"`
	PrimaryMAC   string            `json:"primary_mac"`
	Interfaces   []InterfaceConfig `json:"interfaces"`
	SSHKeys      []string          `json:"ssh_keys"`
	KernelArgs   string            `json:"kernel_args"`
	// Provider identifies the node's hardware/power backend: "ipmi", "proxmox", or ""
	// (unset). This is a metadata label — it does not automatically reconfigure BMC.
	// Added in migration 076.
	Provider string `json:"provider,omitempty"`
	// Tags holds unstructured node labels used for filtering and Slurm role assignment.
	// Renamed from Groups in S2-4; the JSON field "groups" is also emitted for one
	// release (v0.x) for backward compatibility with existing CLI versions.
	Tags []string `json:"tags"`
	// Groups is deprecated — use Tags. Kept for JSON backward compatibility through v1.0.
	// Removed in v1.1. Callers should read Tags; Groups mirrors Tags during the
	// dual-emit window.
	Groups      []string            `json:"groups"`
	CustomVars  map[string]string   `json:"custom_vars"`
	BaseImageID string              `json:"base_image_id,omitempty"`
	BMC         *BMCNodeConfig      `json:"bmc,omitempty"`
	IBConfig    []IBInterfaceConfig `json:"ib_config,omitempty"`
	// PowerProvider selects the power management backend for this node.
	// If nil, the server falls back to legacy BMC-based IPMI when BMC is set.
	PowerProvider *PowerProviderConfig `json:"power_provider,omitempty"`
	// GroupID optionally links this node to a NodeGroup. When set, the group's
	// DiskLayoutOverride is consulted during layout resolution if the node has
	// no node-level override.
	GroupID string `json:"group_id,omitempty"`
	// DiskLayoutOverride, when non-nil, completely replaces the image's disk
	// layout for this specific node. Takes highest priority in resolution.
	DiskLayoutOverride *DiskLayout `json:"disk_layout_override,omitempty"`
	// LDAPConfig, when non-nil, causes finalization to write sssd.conf, ldap.conf,
	// and the CA certificate bundle into the deployed filesystem so the node can
	// authenticate users against the clustr LDAP server.
	// ServiceBindDN/ServiceBindPasswd carry the read-only node-reader account;
	// the admin (Directory Manager) credentials are NEVER present here.
	LDAPConfig *LDAPNodeConfig `json:"ldap_config,omitempty"`

	// SystemAccounts, when non-nil and non-empty, causes finalization to inject
	// local POSIX accounts and groups into /etc/passwd, /etc/group, and /etc/shadow.
	SystemAccounts *SystemAccountsNodeConfig `json:"system_accounts,omitempty"`

	// NetworkConfig, when non-nil, causes finalization to write NetworkManager
	// keyfiles for bond interfaces and IPoIB, and optionally inject opensm.conf.
	// This is additive to Interfaces: both are written; Interfaces handles simple
	// static IPs, NetworkConfig handles bonds, VLANs, and IPoIB.
	NetworkConfig *NetworkNodeConfig `json:"network_config,omitempty"`

	// SlurmConfig, when non-nil, causes finalization to write Slurm config files
	// to /etc/slurm/ on the deployed node. Nil when the Slurm module is disabled
	// or not yet enabled.
	SlurmConfig *SlurmNodeConfig `json:"slurm_config,omitempty"`

	// SudoersConfig, when non-nil, causes finalization to write a sudoers drop-in
	// to /etc/sudoers.d/<group_cn> so LDAP group members can run sudo on the node.
	// Requires LDAPConfig to also be set — SSSD resolves group membership at sudo time.
	SudoersConfig *SudoersNodeConfig `json:"sudoers_config,omitempty"`

	// ClusterHosts is the full cluster host roster injected at registration time.
	// Finalization writes these into /etc/hosts so nodes can resolve each other
	// and the clustr server before DNS/LDAP is available.
	// Transient: populated at registration, never stored in the database.
	ClusterHosts []HostEntry `json:"cluster_hosts,omitempty"`

	// ExtraMounts holds additional /etc/fstab entries written during finalization.
	// The effective list is group mounts merged with node mounts; use
	// EffectiveExtraMounts to resolve. Stored as node-level on NodeConfig only
	// after server-side merging for the deploy path.
	ExtraMounts []FstabEntry `json:"extra_mounts,omitempty"`
	// VerifyTimeoutOverride, when non-nil, overrides CLUSTR_VERIFY_TIMEOUT for this
	// specific node. Value is in seconds. A value of 0 disables the timeout for this
	// node entirely. NULL means use the global default. Added in migration 054.
	VerifyTimeoutOverride *int `json:"verify_timeout_override,omitempty"`
	// ReimagePending is set to true by the reimage orchestrator after it fires
	// PowerCycle. The PXE boot handler returns the full clustr initramfs boot
	// script while this flag is set, causing the node to deploy fresh.
	// Cleared by the deploy-complete callback once deployment finalizes.
	ReimagePending bool `json:"reimage_pending,omitempty"`
	// LastDeployFailedAt is the Unix timestamp of the most recent failed deploy.
	// Used by State() to determine NodeStateFailed.
	LastDeployFailedAt *time.Time `json:"last_deploy_failed_at,omitempty"`

	// ADR-0008: Two-Phase Deploy Success fields.

	// DeployCompletedPrebootAt is set when clustr-static POSTs deploy-complete from
	// inside the PXE initramfs. Proves the rootfs was written without error.
	// Does NOT prove the OS boots. See ADR-0008.
	DeployCompletedPrebootAt *time.Time `json:"deploy_completed_preboot_at,omitempty"`
	// DeployVerifiedBootedAt is set when the deployed OS phones home via
	// POST /api/v1/nodes/{id}/verify-boot. Proves bootloader + kernel + systemd
	// all started. Terminal success state. See ADR-0008.
	DeployVerifiedBootedAt *time.Time `json:"deploy_verified_booted_at,omitempty"`
	// DeployVerifyTimeoutAt is set by the background scanner when verify-boot was
	// not received within CLUSTR_VERIFY_TIMEOUT after deploy_completed_preboot_at.
	// Indicates a likely bootloader or network failure. See ADR-0008.
	DeployVerifyTimeoutAt *time.Time `json:"deploy_verify_timeout_at,omitempty"`
	// LastSeenAt is updated on every verify-boot call. Acts as a heartbeat —
	// the most recent time the deployed OS successfully contacted the server.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`

	// HardwareProfile is the raw hardware discovery JSON from the node.
	// Populated on auto-registration; nil when node was created manually.
	HardwareProfile json.RawMessage `json:"hardware_profile,omitempty"`
	// DetectedFirmware is the node's boot firmware type reported by the deploy
	// agent on registration: "uefi" or "bios". Empty for manually-created nodes
	// or legacy registrations that predate this field.
	DetectedFirmware string `json:"detected_firmware,omitempty"`
	// LDAPReady is set by the server when a node phones home via verify-boot and
	// includes sssd status. nil = not yet checked; true = sssd connected;
	// false = sssd not ready (see LDAPReadyDetail). Sprint 15 #99.
	LDAPReady       *bool  `json:"ldap_ready,omitempty"`
	LDAPReadyDetail string `json:"ldap_ready_detail,omitempty"`

	// Sprint 34 BOOT-POLICY (#225 follow-up).
	//
	// BootOrderPolicy declares the operator's intent for the on-NVRAM
	// BootOrder UEFI variable after a successful deploy. One of:
	//
	//   "auto"    — back-compat default; finalize uses the v0.1.22 reactive
	//               repair (PXE first if found, otherwise leave alone).
	//   "network" — explicit "PXE / network entries lead BootOrder". Used for
	//               compute nodes that should always be re-imageable.
	//   "os"      — explicit "OS entries lead, PXE second". Used for login,
	//               storage, or service nodes that the operator wants to
	//               cold-boot from disk by default and only PXE on demand.
	//
	// Stored in the boot_order_policy column added by migration 105. Empty
	// string is read as "auto" for forward-compat.
	BootOrderPolicy string `json:"boot_order_policy,omitempty"`

	// Sprint 34 BOOT-SETTINGS-MODAL (#160 follow-up).
	//
	// NetbootMenuEntry, when non-empty, references a row in the boot_entries
	// catalog (#160). ServeIPXEScript chains to that entry on the next PXE
	// boot for nodes that are NOT mid-reimage. No FK; a dangling reference
	// is degraded to "fall back to default disk-boot menu" with a logged
	// warning.
	NetbootMenuEntry string `json:"netboot_menu_entry,omitempty"`

	// KernelCmdline, when non-empty, is appended verbatim to the kernel
	// cmdline of the served boot script. Used for serial console pinning
	// ("console=ttyS0,115200n8") or temporary debug flags ("nomodeset").
	// Validation: length cap of 4 KiB, NUL-byte rejection.
	KernelCmdline string `json:"kernel_cmdline,omitempty"`

	// Sprint 37 DISKLESS Bundle A — node operating mode.
	//
	// OperatingMode declares how this node boots and (optionally) installs.
	// Persisted in the operating_mode column added by migration 111. Valid
	// values are enumerated by OperatingModeValues; the SQLite-side CHECK
	// constraint is the canonical guard, so an unknown value cannot be
	// persisted regardless of API-layer validation. Empty string is
	// normalized to OperatingModeBlockInstall on read for forward-compat.
	//
	// Bundle A wires only OperatingModeBlockInstall end-to-end. The other
	// three values are reserved enum slots; ServeIPXEScript serves a TODO
	// sentinel script for them so the protocol is observable in lab without
	// shipping a half-broken boot path. Bundle B will deliver
	// initramfs-variant + NFS-export wiring for the stateless modes.
	OperatingMode string `json:"operating_mode"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OperatingMode constants enumerate the values legal for NodeConfig.OperatingMode.
// Mirrors the CHECK constraint defined in migration 111 — keep these in sync.
//
// Sprint 37 DISKLESS Bundle A.
const (
	// OperatingModeBlockInstall is the current/default behavior: initramfs
	// writes the base image to disk and reboots into the on-disk OS. The
	// only mode wired end-to-end as of Bundle A.
	OperatingModeBlockInstall = "block_install"

	// OperatingModeFilesystemInstall reserves an enum slot for a chroot/
	// rsync-style filesystem install path. TODO Bundle B.
	OperatingModeFilesystemInstall = "filesystem_install"

	// OperatingModeStatelessNFS reserves an enum slot for a stateless boot
	// that mounts the cluster NFS export of the image rootfs and never
	// writes to disk. TODO Bundle B.
	OperatingModeStatelessNFS = "stateless_nfs"

	// OperatingModeStatelessRAM reserves an enum slot for a stateless boot
	// where the rootfs is loaded fully into RAM via a custom initrd. TODO
	// Bundle B.
	OperatingModeStatelessRAM = "stateless_ram"
)

// OperatingModeValues lists all accepted values for NodeConfig.OperatingMode,
// in the same order as the migration 111 CHECK constraint. Used by API-layer
// validators (e.g. the PATCH /api/v1/nodes/{id} handler) to reject bogus
// values with a 400 before the SQLite CHECK fires.
var OperatingModeValues = []string{
	OperatingModeBlockInstall,
	OperatingModeFilesystemInstall,
	OperatingModeStatelessNFS,
	OperatingModeStatelessRAM,
}

// IsValidOperatingMode reports whether s is an accepted operating_mode value.
// Empty string is NOT accepted — callers that want "default" should pass
// OperatingModeBlockInstall explicitly so the wire contract is symmetric.
func IsValidOperatingMode(s string) bool {
	for _, v := range OperatingModeValues {
		if s == v {
			return true
		}
	}
	return false
}

// State derives the current lifecycle state of this node from its stored fields.
// This is the canonical way to determine what the PXE boot handler should return.
//
// ADR-0008 two-phase priority order (highest to lowest):
//  1. ReimagePending — always overrides everything else.
//  2. LastDeployFailedAt after deploy_completed_preboot_at — node is in error.
//  3. DeployVerifiedBootedAt set + LDAPReady==false — deployed_ldap_failed
//     (booted but LDAP client broken; v0.1.15).
//  4. DeployVerifiedBootedAt set — deployed_verified (OS phoned home post-boot).
//  5. DeployVerifyTimeoutAt set — deploy_verify_timeout (OS never phoned home).
//  6. DeployCompletedPrebootAt set — deployed_preboot (initramfs done, awaiting boot).
//  7. BaseImageID set — node is configured but never deployed.
//  8. Otherwise — node is registered but has no image.
//
// LDAP gating rationale (v0.1.15): pre-v0.1.15, a node whose verify-boot
// payload reported sssd_status=not_installed transitioned to deployed_verified
// because the state machine only checked DeployVerifiedBootedAt. That made
// "deployed_verified" mean "node booted and called home" instead of "node is
// functionally ready", masking real provisioning failures (sssd not installed,
// pam_sss.so missing, LDAP unreachable). When LDAPReady is explicitly false
// — meaning the LDAP module is enabled and the node phoned home with sssd
// not connected — the state is downgraded to deployed_ldap_failed so the UI
// surfaces the failure and operators don't false-positive on cluster readiness.
// LDAPReady==nil (unknown / older client / LDAP not configured for this node)
// preserves the legacy deployed_verified semantics.
//
// S6-8: LastDeploySucceededAt back-compat fallback removed (column dropped in migration 049).
func (n *NodeConfig) State() NodeState {
	if n.ReimagePending {
		return NodeStateReimagePending
	}

	if n.LastDeployFailedAt != nil {
		if n.DeployCompletedPrebootAt == nil || n.LastDeployFailedAt.After(*n.DeployCompletedPrebootAt) {
			return NodeStateFailed
		}
	}

	// Two-phase success states (ADR-0008).
	if n.DeployVerifiedBootedAt != nil {
		// v0.1.15: gate on LDAP readiness when the node has LDAP configured.
		// LDAPReady is only recorded when the node phoned home with sssd_status
		// non-empty AND LDAPNodeIsConfigured returned true, so a non-nil false
		// here means the cluster expected LDAP to work and it doesn't.
		if n.LDAPReady != nil && !*n.LDAPReady {
			return NodeStateDeployedLDAPFailed
		}
		return NodeStateDeployedVerified
	}
	if n.DeployVerifyTimeoutAt != nil {
		return NodeStateDeployVerifyTimeout
	}
	if n.DeployCompletedPrebootAt != nil {
		return NodeStateDeployedPreboot
	}

	if n.BaseImageID != "" {
		return NodeStateConfigured
	}
	return NodeStateRegistered
}

// EffectiveLayout resolves the disk layout that will be used when deploying
// this node, following the three-level priority hierarchy:
//
//  1. Node-level override (highest) — DiskLayoutOverride on this NodeConfig.
//  2. Group-level override — DiskLayoutOverride on the NodeGroup, if any.
//  3. Image default (lowest) — DiskLayout on the BaseImage.
//
// Pass group=nil when the node is not in a group or the group has no override.
func (n *NodeConfig) EffectiveLayout(img *BaseImage, group *NodeGroup) DiskLayout {
	if n.DiskLayoutOverride != nil {
		return *n.DiskLayoutOverride
	}
	if group != nil && group.DiskLayoutOverride != nil {
		return *group.DiskLayoutOverride
	}
	if img != nil {
		return img.DiskLayout
	}
	return DiskLayout{}
}

// EffectiveLayoutSource returns a human-readable label describing which level
// of the hierarchy provided the effective layout: "node", "group", or "image".
func (n *NodeConfig) EffectiveLayoutSource(img *BaseImage, group *NodeGroup) string {
	if n.DiskLayoutOverride != nil {
		return "node"
	}
	if group != nil && group.DiskLayoutOverride != nil {
		return "group"
	}
	return "image"
}

// EffectiveExtraMounts returns the merged fstab entries for this node.
// Group entries form the base; node entries override by mount point or append.
// Pass group=nil when the node is not in a group.
func (n *NodeConfig) EffectiveExtraMounts(group *NodeGroup) []FstabEntry {
	result := []FstabEntry{}
	seen := map[string]int{}

	if group != nil {
		for _, m := range group.ExtraMounts {
			seen[m.MountPoint] = len(result)
			result = append(result, m)
		}
	}
	for _, m := range n.ExtraMounts {
		if idx, exists := seen[m.MountPoint]; exists {
			result[idx] = m // node overrides group for this mount point
		} else {
			result = append(result, m)
		}
	}
	return result
}

// allowedFSTypes is the whitelist of supported filesystem types for FstabEntry.
var allowedFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smbfs": true,
	"beegfs": true, "lustre": true, "xfs": true, "ext4": true,
	"ext3": true, "vfat": true, "tmpfs": true, "bind": true,
	"9p": true, "gpfs": true,
}

// forbiddenMountPoints lists paths that must never be used as extra mount points.
var forbiddenMountPoints = map[string]bool{
	"/": true, "/boot": true, "/proc": true, "/sys": true, "/dev": true, "/run": true,
}

// networkFSTypes lists filesystem types that require network access at mount
// time and should carry the _netdev option so systemd waits for the network.
var networkFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smbfs": true,
	"beegfs": true, "lustre": true, "gpfs": true, "9p": true,
}

// ValidateFstabEntry checks that e is safe to write into /etc/fstab.
// Returns a non-nil error describing the first problem found.
func ValidateFstabEntry(e FstabEntry) error {
	if e.Source == "" {
		return fmt.Errorf("fstab entry source must not be empty")
	}
	if e.MountPoint == "" || e.MountPoint[0] != '/' {
		return fmt.Errorf("fstab entry mount_point %q must be an absolute path", e.MountPoint)
	}
	if forbiddenMountPoints[e.MountPoint] {
		return fmt.Errorf("fstab entry mount_point %q is a reserved system path and cannot be overridden", e.MountPoint)
	}
	if !allowedFSTypes[e.FSType] {
		return fmt.Errorf("fstab entry fs_type %q is not in the allowed list", e.FSType)
	}
	return nil
}

// IsNetworkFS reports whether fsType requires network connectivity at mount time.
func IsNetworkFS(fsType string) bool {
	return networkFSTypes[fsType]
}

// --- Request types ---

// CreateImageRequest is the body for POST /api/v1/images.
type CreateImageRequest struct {
	Name       string      `json:"name"`
	Version    string      `json:"version"`
	OS         string      `json:"os"`
	Arch       string      `json:"arch"`
	Format     ImageFormat `json:"format"`
	DiskLayout DiskLayout  `json:"disk_layout"`
	Tags       []string    `json:"tags"`
	SourceURL  string      `json:"source_url,omitempty"`
	Notes      string      `json:"notes"`
}

// PullRequest is the body for POST /api/v1/factory/pull.
type PullRequest struct {
	URL        string      `json:"url"`
	Name       string      `json:"name"`
	Version    string      `json:"version"`
	OS         string      `json:"os"`
	Arch       string      `json:"arch"`
	Format     ImageFormat `json:"format"`
	DiskLayout DiskLayout  `json:"disk_layout"`
	Tags       []string    `json:"tags"`
	Notes      string      `json:"notes"`
}

// CreateNodeConfigRequest is the body for POST /api/v1/nodes.
type CreateNodeConfigRequest struct {
	Hostname   string            `json:"hostname"`
	FQDN       string            `json:"fqdn"`
	PrimaryMAC string            `json:"primary_mac"`
	Interfaces []InterfaceConfig `json:"interfaces"`
	SSHKeys    []string          `json:"ssh_keys"`
	KernelArgs string            `json:"kernel_args"`
	// Tags holds unstructured node labels for filtering and Slurm role assignment.
	Tags []string `json:"tags"`
	// Groups is a deprecated alias for Tags, accepted for backward compatibility through v1.0.
	Groups      []string          `json:"groups"`
	CustomVars  map[string]string `json:"custom_vars"`
	BaseImageID string            `json:"base_image_id"`
	// Provider identifies the node's hardware/power backend: "ipmi", "proxmox", or "".
	Provider string `json:"provider,omitempty"`
}

// UpdateNodeConfigRequest is the body for PUT /api/v1/nodes/:id.
type UpdateNodeConfigRequest struct {
	Hostname   string            `json:"hostname"`
	FQDN       string            `json:"fqdn"`
	PrimaryMAC string            `json:"primary_mac"`
	Interfaces []InterfaceConfig `json:"interfaces"`
	SSHKeys    []string          `json:"ssh_keys"`
	KernelArgs string            `json:"kernel_args"`
	// Tags holds unstructured node labels for filtering and Slurm role assignment.
	Tags []string `json:"tags"`
	// Groups is a deprecated alias for Tags, accepted for backward compatibility through v1.0.
	Groups      []string          `json:"groups"`
	CustomVars  map[string]string `json:"custom_vars"`
	BaseImageID string            `json:"base_image_id"`
	// PowerProvider, when non-nil, replaces the power provider config for this
	// node. Omit (or send null) to preserve the existing provider and credentials.
	// Use ClearPowerProvider=true to explicitly remove the power provider.
	PowerProvider *PowerProviderConfig `json:"power_provider,omitempty"`
	// ClearPowerProvider, when true, explicitly removes the power provider config.
	// Use this instead of omitting power_provider (which preserves the existing
	// config) when you want to revert to the legacy BMC/IPMI fallback.
	ClearPowerProvider bool   `json:"clear_power_provider,omitempty"`
	GroupID            string `json:"group_id,omitempty"`
	// DiskLayoutOverride, when non-nil, replaces the image/group disk layout for
	// this node. Send null or omit to clear a previously set override.
	DiskLayoutOverride *DiskLayout `json:"disk_layout_override,omitempty"`
	// ClearLayoutOverride, when true, explicitly removes any node-level override.
	// Use this instead of sending an empty DiskLayoutOverride, which is ambiguous.
	ClearLayoutOverride bool `json:"clear_layout_override,omitempty"`
	// ExtraMounts replaces the node-level extra fstab entries. Send an empty
	// slice to clear all node-level mounts (group mounts are unaffected).
	ExtraMounts []FstabEntry `json:"extra_mounts,omitempty"`
	// VerifyTimeoutOverride, when non-nil, overrides CLUSTR_VERIFY_TIMEOUT for this
	// node. Value is in seconds. Set to 0 to disable the timeout for this node.
	// Omit (null) to use the global default.
	VerifyTimeoutOverride *int `json:"verify_timeout_override,omitempty"`
	// ClearVerifyTimeoutOverride, when true, removes any per-node override and reverts
	// to the global CLUSTR_VERIFY_TIMEOUT setting.
	ClearVerifyTimeoutOverride bool `json:"clear_verify_timeout_override,omitempty"`
}

// UpdateNodeBootSettingsRequest is the body for PUT /api/v1/nodes/:id/boot-settings.
//
// Sprint 34 BOOT-SETTINGS-MODAL.  All three fields are pointer-typed so the
// caller can distinguish "leave alone" (omit / null) from "clear" (explicit
// empty string).  The web modal always sends every field even when the
// operator only changed one — pointer semantics keep that lossless.
//
// Validation (server-side, see internal/server/handlers/nodes.go:UpdateBootSettings):
//
//   - BootOrderPolicy: when non-nil, must be one of "auto", "network", "os".
//     Empty string is treated as "clear → fall back to auto".
//   - NetbootMenuEntry: when non-nil and non-empty, must reference a row in
//     boot_entries (looked up at write time; a missing row is a 400).
//   - KernelCmdline: when non-nil, must be ≤ 4096 bytes and free of NUL.
type UpdateNodeBootSettingsRequest struct {
	BootOrderPolicy  *string `json:"boot_order_policy,omitempty"`
	NetbootMenuEntry *string `json:"netboot_menu_entry,omitempty"`
	KernelCmdline    *string `json:"kernel_cmdline,omitempty"`
}

// ─── Node group request types ─────────────────────────────────────────────────

// CreateNodeGroupRequest is the body for POST /api/v1/node-groups.
type CreateNodeGroupRequest struct {
	Name               string       `json:"name"`
	Description        string       `json:"description"`
	Role               string       `json:"role,omitempty"`
	DiskLayoutOverride *DiskLayout  `json:"disk_layout_override,omitempty"`
	ExtraMounts        []FstabEntry `json:"extra_mounts,omitempty"`
}

// ─── Sprint 42 Day 2 — JSON-SCHEMA validation request types ──────────────────

// CreateUserRequest is the wire type for POST /api/v1/admin/users and
// POST /api/v1/users. Exported so cmd/generate-schemas can reflect on it
// and emit a JSON Schema used for API-boundary validation.
type CreateUserRequest struct {
	Username string `json:"username" jsonschema:"required,minLength=1"`
	Password string `json:"password" jsonschema:"required,minLength=8"`
	Role     string `json:"role" jsonschema:"required,enum=admin,enum=operator,enum=readonly,enum=viewer,enum=pi,enum=director"`
}

// DangerousPushStageRequest is the wire type for POST /api/v1/config/dangerous-push.
// Exported so cmd/generate-schemas can emit a JSON Schema for this endpoint.
type DangerousPushStageRequest struct {
	NodeID     string          `json:"node_id" jsonschema:"required,minLength=1"`
	PluginName string          `json:"plugin_name" jsonschema:"required,minLength=1"`
	Payload    json.RawMessage `json:"payload,omitempty" jsonschema:"description=Reserved for future use"`
}

// DangerousPushConfirmRequest is the wire type for
// POST /api/v1/config/dangerous-push/{id}/confirm.
// Exported so cmd/generate-schemas can emit a JSON Schema for this endpoint.
type DangerousPushConfirmRequest struct {
	ConfirmString string `json:"confirm_string" jsonschema:"required,minLength=1"`
}

// UpdateNodeGroupRequest is the body for PUT /api/v1/node-groups/:id.
type UpdateNodeGroupRequest struct {
	Name                string      `json:"name"`
	Description         string      `json:"description"`
	Role                string      `json:"role,omitempty"`
	DiskLayoutOverride  *DiskLayout `json:"disk_layout_override,omitempty"`
	ClearLayoutOverride bool        `json:"clear_layout_override,omitempty"`
	// ExtraMounts replaces the group-level extra fstab entries.
	ExtraMounts []FstabEntry `json:"extra_mounts,omitempty"`
}

// AddGroupMembersRequest is the body for POST /api/v1/node-groups/:id/members.
type AddGroupMembersRequest struct {
	NodeIDs []string `json:"node_ids"`
}

// GroupMembersResponse is returned by GET /api/v1/node-groups/:id (detail) and
// POST /api/v1/node-groups/:id/members.
type GroupMembersResponse struct {
	Group   NodeGroup    `json:"group"`
	Members []NodeConfig `json:"members"`
}

// GroupReimageRequest is the body for POST /api/v1/node-groups/:id/reimage.
type GroupReimageRequest struct {
	ImageID           string `json:"image_id"`
	Concurrency       int    `json:"concurrency,omitempty"`          // default 5
	PauseOnFailurePct int    `json:"pause_on_failure_pct,omitempty"` // default 20
}

// GroupReimageJobStatus is the response from POST /api/v1/node-groups/:id/reimage
// and GET /api/v1/reimages/jobs/:jobID.
type GroupReimageJobStatus struct {
	JobID             string    `json:"job_id"`
	GroupID           string    `json:"group_id"`
	ImageID           string    `json:"image_id"`
	Status            string    `json:"status"`
	TotalNodes        int       `json:"total_nodes"`
	TriggeredNodes    int       `json:"triggered_nodes"`
	SucceededNodes    int       `json:"succeeded_nodes"`
	FailedNodes       int       `json:"failed_nodes"`
	Concurrency       int       `json:"concurrency"`
	PauseOnFailurePct int       `json:"pause_on_failure_pct"`
	ErrorMessage      string    `json:"error_message,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// AssignGroupRequest is the body for PUT /api/v1/nodes/:id/group.
type AssignGroupRequest struct {
	// GroupID is the group to assign. Empty string removes the node from its
	// current group (equivalent to DELETE).
	GroupID string `json:"group_id"`
}

// ─── Node group response types ────────────────────────────────────────────────

// ListNodeGroupsResponse wraps the node groups list with live member counts.
type ListNodeGroupsResponse struct {
	Groups []NodeGroupWithCount `json:"groups"`
	Total  int                  `json:"total"`
}

// ─── Layout recommendation types ─────────────────────────────────────────────

// LayoutRecommendation is the response from GET /api/v1/nodes/:id/layout-recommendation.
// It contains a suggested DiskLayout derived from hardware discovery and the
// reasoning behind each decision so admins can evaluate it before applying.
type LayoutRecommendation struct {
	Layout    DiskLayout `json:"layout"`
	Reasoning string     `json:"reasoning"`
	Warnings  []string   `json:"warnings,omitempty"`
}

// StorageRecommendation is the response from
// GET /api/v1/nodes/:id/layout-recommendation?role=storage.
// It separates the OS mdadm RAID1 layout from the ZFS data pool configuration
// so that storage node provisioning can be reviewed and overridden independently.
type StorageRecommendation struct {
	// OSLayout is the mdadm RAID1 layout for the OS drives (2 smallest drives).
	OSLayout DiskLayout `json:"os_layout"`
	// ZFSPools contains the data pool plus optional SLOG/L2ARC vdevs.
	ZFSPools []ZFSPool `json:"zfs_pools"`
	// Reasoning contains a human-readable explanation of every decision made.
	Reasoning []string `json:"reasoning"`
	// Warnings lists non-fatal concerns the operator should review.
	Warnings []string `json:"warnings,omitempty"`
	// Stats summarises capacity and drive allocation at a glance.
	Stats StorageStats `json:"stats"`
}

// StorageStats provides a capacity summary for a StorageRecommendation.
type StorageStats struct {
	// RawCapacityBytes is the total raw bytes across all data drives.
	RawCapacityBytes int64 `json:"raw_capacity_bytes"`
	// UsableCapacityBytes is the estimated usable capacity after parity overhead.
	UsableCapacityBytes int64 `json:"usable_capacity_bytes"`
	// VdevCount is the number of ZFS vdevs in the data pool.
	VdevCount int `json:"vdev_count"`
	// DrivesForOS is the number of drives consumed by the OS RAID1.
	DrivesForOS int `json:"drives_for_os"`
	// DrivesForData is the number of HDD/SSD drives allocated to the ZFS data pool.
	DrivesForData int `json:"drives_for_data"`
	// DrivesForCache is the number of NVMe/SSD drives used for SLOG + L2ARC.
	DrivesForCache int `json:"drives_for_cache"`
	// ParityOverhead is the fraction of raw capacity consumed by parity (e.g. 0.20 for raidz2/10-wide).
	ParityOverhead float64 `json:"parity_overhead"`
}

// LayoutValidationRequest is the body for POST /api/v1/nodes/:id/layout/validate.
type LayoutValidationRequest struct {
	Layout DiskLayout `json:"layout"`
}

// LayoutValidationResponse is returned by the validation endpoint.
type LayoutValidationResponse struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// EffectiveLayoutResponse is returned by GET /api/v1/nodes/:id/effective-layout.
type EffectiveLayoutResponse struct {
	Layout  DiskLayout `json:"layout"`
	Source  string     `json:"source"` // "node", "group", or "image"
	GroupID string     `json:"group_id,omitempty"`
	ImageID string     `json:"image_id,omitempty"`
}

// EffectiveMountsResponse is returned by GET /api/v1/nodes/:id/effective-mounts.
// It shows the merge result along with where each entry originates.
type EffectiveMountEntry struct {
	FstabEntry
	Source  string `json:"source"`             // "node" or "group"
	GroupID string `json:"group_id,omitempty"` // set when source == "group"
}

type EffectiveMountsResponse struct {
	Mounts  []EffectiveMountEntry `json:"mounts"`
	NodeID  string                `json:"node_id"`
	GroupID string                `json:"group_id,omitempty"`
}

// ─── Disk layout catalog (#146) ──────────────────────────────────────────────

// StoredDiskLayout is a named, reusable disk layout record from the disk_layouts
// table.  It can be assigned to a node group (group default) or to an individual
// node (per-node override).  The Layout field carries the full partition/FS/RAID
// spec; it is stored as JSON in the DB column layout_json.
//
// Precedence during deploy (highest → lowest):
//  1. node.disk_layout_id          — per-node override
//  2. node_groups.disk_layout_id   — group default
//  3. existing inline override / image default
type StoredDiskLayout struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	SourceNodeID string     `json:"source_node_id,omitempty"` // nil for hand-authored
	CapturedAt   time.Time  `json:"captured_at"`
	Layout       DiskLayout `json:"layout"`
	// FirmwareKind is the firmware family this layout targets.  One of
	// "bios", "uefi", "any".  Used by the firmware-aware selector
	// (Sprint 35 / #255) to prefer a UEFI-bearing layout for UEFI nodes
	// when neither the node nor its group has pinned a specific layout.
	// Defaults to "any" for layouts created before migration 110.
	FirmwareKind string    `json:"firmware_kind"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// FirmwareKind constants for StoredDiskLayout.FirmwareKind.
const (
	FirmwareKindBIOS = "bios"
	FirmwareKindUEFI = "uefi"
	FirmwareKindAny  = "any"
)

// ListDiskLayoutsResponse is returned by GET /api/v1/disk-layouts.
type ListDiskLayoutsResponse struct {
	Layouts []StoredDiskLayout `json:"layouts"`
	Total   int                `json:"total"`
}

// ─── Rack model (#149) ───────────────────────────────────────────────────────

// Rack represents a physical rack unit in the datacenter inventory.
type Rack struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	HeightU   int       `json:"height_u"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Positions is populated when ?include=positions is used on list/get endpoints.
	Positions []NodeRackPosition `json:"positions,omitempty"`

	// Enclosures is populated when ?include=enclosures (or ?include=positions,enclosures)
	// is used. Each enclosure includes its slot occupancy.
	Enclosures []Enclosure `json:"enclosures,omitempty"`
}

// NodeRackPosition describes the physical U-slot assignment for a node in a rack.
// For rack-direct nodes: RackID is set, EnclosureID and SlotIndex are empty.
// For enclosure-resident nodes: EnclosureID and SlotIndex are set, RackID is empty.
type NodeRackPosition struct {
	NodeID string `json:"node_id"`

	// Rack-direct fields (mutually exclusive with enclosure fields).
	RackID  string `json:"rack_id,omitempty"`
	SlotU   int    `json:"slot_u,omitempty"`
	HeightU int    `json:"height_u,omitempty"`

	// Enclosure-resident fields (mutually exclusive with rack-direct fields).
	EnclosureID string `json:"enclosure_id,omitempty"`
	SlotIndex   int    `json:"slot_index,omitempty"`
}

// ListRacksResponse is returned by GET /api/v1/racks.
type ListRacksResponse struct {
	Racks []Rack `json:"racks"`
	Total int    `json:"total"`
}

// ─── Enclosure model (#231 Sprint 31) ────────────────────────────────────────

// Enclosure represents a physical multi-node chassis unit placed in a rack.
// Examples: blade chassis, 2U-twin, half-width 1U shelf, 4U quad chassis.
type Enclosure struct {
	ID       string `json:"id"`
	RackID   string `json:"rack_id"`
	RackSlotU int   `json:"rack_slot_u"` // bottom-most U the chassis occupies
	HeightU  int    `json:"height_u"`    // how many U the chassis occupies
	TypeID   string `json:"type_id"`     // key into the canned EnclosureType catalog
	Label    string `json:"label,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Slots is the ordered occupancy array (slot 1..SlotCount).
	// Populated by list/get endpoints. Nil when not requested.
	Slots []EnclosureSlot `json:"slots,omitempty"`
}

// EnclosureSlot represents one physical slot inside an enclosure.
// NodeID is empty when the slot is unoccupied.
type EnclosureSlot struct {
	SlotIndex int    `json:"slot_index"` // 1-based
	NodeID    string `json:"node_id,omitempty"`
}

// ListEnclosuresResponse is returned by GET /api/v1/racks/{rack_id}/enclosures.
type ListEnclosuresResponse struct {
	Enclosures []Enclosure `json:"enclosures"`
	Total      int         `json:"total"`
}

// EnclosureType describes a canned chassis form-factor.
// The catalog is defined in internal/enclosures/types.go and exposed via
// GET /api/v1/enclosure-types.
type EnclosureType struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	HeightU     int    `json:"height_u"`
	SlotCount   int    `json:"slot_count"`
	Orientation string `json:"orientation"`
	Description string `json:"description"`
}

// ListEnclosureTypesResponse is returned by GET /api/v1/enclosure-types.
type ListEnclosureTypesResponse struct {
	Types []EnclosureType `json:"types"`
	Total int             `json:"total"`
}

// ─── Unified placement endpoint (#231) ────────────────────────────────────────

// PlacementRequest is the body for PUT /api/v1/nodes/{node_id}/placement.
// Kind selects which union branch is active:
//
//	"rack_u"        — place the node directly in a rack slot
//	"enclosure_slot" — place the node in a chassis slot
//	"unassigned"    — remove from any placement (same as DELETE /placement)
type PlacementRequest struct {
	Kind string `json:"kind"` // "rack_u" | "enclosure_slot" | "unassigned"

	// rack_u fields
	RackID  string `json:"rack_id,omitempty"`
	SlotU   int    `json:"slot_u,omitempty"`
	HeightU int    `json:"height_u,omitempty"`

	// enclosure_slot fields
	EnclosureID string `json:"enclosure_id,omitempty"`
	SlotIndex   int    `json:"slot_index,omitempty"`
}

// UnassignedNodeStub is a lightweight node descriptor for nodes with no rack
// assignment. Returned by GET /api/v1/nodes/unassigned.
type UnassignedNodeStub struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
}

// ListUnassignedNodesResponse is returned by GET /api/v1/nodes/unassigned.
type ListUnassignedNodesResponse struct {
	Nodes []UnassignedNodeStub `json:"nodes"`
	Total int                  `json:"total"`
}

// --- Response types ---

// ErrorResponse is the standard error envelope returned on 4xx/5xx.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// ListImagesResponse wraps the images list with pagination metadata.
// page/per_page/next_cursor are present when ?page= or ?per_page= is used.
type ListImagesResponse struct {
	Images     []BaseImage `json:"images"`
	Total      int         `json:"total"`
	Page       int         `json:"page,omitempty"`
	PerPage    int         `json:"per_page,omitempty"`
	NextCursor int         `json:"next_cursor,omitempty"` // next page number, 0 = no more pages
}

// GPGKey represents a user-imported GPG public key stored in the database.
// The three embedded release keys (clustr, rocky-9, EPEL-9) are returned by
// GET /api/v1/gpg-keys with source="embedded"; keys added via POST have source="user".
type GPGKey struct {
	Fingerprint string    `json:"fingerprint"`
	Owner       string    `json:"owner"`
	ArmoredKey  string    `json:"armored_key,omitempty"` // omitted in list responses
	Source      string    `json:"source"`                // "embedded" or "user"
	CreatedAt   time.Time `json:"created_at"`
}

// ListGPGKeysResponse is returned by GET /api/v1/gpg-keys.
type ListGPGKeysResponse struct {
	Keys []GPGKey `json:"keys"`
}

// ImportGPGKeyRequest is the body for POST /api/v1/gpg-keys.
type ImportGPGKeyRequest struct {
	// ArmoredKey is the ASCII-armored public key block (BEGIN PGP PUBLIC KEY BLOCK).
	ArmoredKey string `json:"armored_key"`
	// Owner is a human-readable label for the key (optional).
	Owner string `json:"owner,omitempty"`
}

// ImageEventKind classifies an image lifecycle event published on the SSE channel.
type ImageEventKind string

const (
	ImageEventCreated   ImageEventKind = "image.created"
	ImageEventUpdated   ImageEventKind = "image.updated"
	ImageEventDeleted   ImageEventKind = "image.deleted"
	ImageEventFinalized ImageEventKind = "image.finalized" // blob upload complete, status → ready
)

// ImageEvent is published on GET /api/v1/images/events (SSE stream) whenever an
// image's lifecycle state changes. Consumers can react without polling.
type ImageEvent struct {
	Kind  ImageEventKind `json:"kind"`
	Image *BaseImage     `json:"image,omitempty"` // nil when kind == image.deleted
	ID    string         `json:"id"`              // always populated (use when image is nil)
}

// GroupReimageEventKind classifies a group reimage SSE event.
type GroupReimageEventKind string

const (
	GroupReimageEventQueued    GroupReimageEventKind = "reimage.queued"    // node enqueued
	GroupReimageEventStarted   GroupReimageEventKind = "reimage.started"   // node reimage triggered
	GroupReimageEventImaging   GroupReimageEventKind = "reimage.imaging"   // node is being imaged
	GroupReimageEventVerifying GroupReimageEventKind = "reimage.verifying" // node verify-boot in progress
	GroupReimageEventDone      GroupReimageEventKind = "reimage.done"      // node completed successfully
	GroupReimageEventFailed    GroupReimageEventKind = "reimage.failed"    // node failed
	GroupReimageEventCompleted GroupReimageEventKind = "reimage.completed" // whole job terminal
)

// GroupReimageEvent is published on
// GET /api/v1/node-groups/{id}/reimage/events?job_id=<jid>
// for each per-node state transition and the final job summary.
type GroupReimageEvent struct {
	Kind       GroupReimageEventKind `json:"kind"`
	JobID      string                `json:"job_id"`
	NodeID     string                `json:"node_id,omitempty"`
	Position   int                   `json:"position,omitempty"`    // for reimage.queued
	Progress   *int                  `json:"progress,omitempty"`    // 0-100, for reimage.imaging
	DurationMS int64                 `json:"duration_ms,omitempty"` // for reimage.done
	Error      string                `json:"error,omitempty"`       // for reimage.failed
	Succeeded  int                   `json:"succeeded,omitempty"`   // for reimage.completed
	Failed     int                   `json:"failed,omitempty"`      // for reimage.completed
	Total      int                   `json:"total,omitempty"`       // for reimage.completed
}

// ListNodesResponse wraps the node configs list with pagination metadata.
// page/per_page/next_cursor are present when ?page= or ?per_page= is used.
type ListNodesResponse struct {
	Nodes      []NodeConfig `json:"nodes"`
	Total      int          `json:"total"`
	Page       int          `json:"page,omitempty"`
	PerPage    int          `json:"per_page,omitempty"`
	NextCursor int          `json:"next_cursor,omitempty"` // next page number, 0 = no more pages
}

// HealthResponse is returned by GET /api/v1/health.
type HealthResponse struct {
	Status    string `json:"status"`
	Version   string `json:"version,omitempty"`
	CommitSHA string `json:"commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	// FlipBackFailures is the number of verify-boot flip-back failures since
	// the process started. Non-zero indicates Proxmox boot-order reset failures.
	// Only present when the server has tracking wired (S4-9).
	FlipBackFailures *int64 `json:"flip_back_failures,omitempty"`
}

// ImageInUseResponse is returned with 409 Conflict when a DELETE /api/v1/images/:id
// is rejected because nodes have the image assigned.
type ImageInUseResponse struct {
	Error string       `json:"error"`
	Code  string       `json:"code"`
	Nodes []NodeConfig `json:"nodes"`
}

// ─── Log types ───────────────────────────────────────────────────────────────

// LogEntry is a single structured log event shipped from a CLI client.
//
// Phase, when non-empty, names the deploy phase active at the moment the line
// was emitted (see ProgressReporter phase names: "preflight", "partitioning",
// "formatting", "downloading", "extracting", "finalizing", "deploy-complete",
// plus "hardware", "register", "bios", "image-fetch", "multicast",
// "wait-for-assign"). The web UI uses this to colour-group the live install
// log per Sprint 33 STREAM-LOG-PHASE; consumers that don't care about the
// field can ignore it. Empty for non-deploy log streams.
type LogEntry struct {
	ID        string                 `json:"id"`
	NodeMAC   string                 `json:"node_mac"`
	Hostname  string                 `json:"hostname,omitempty"`
	Level     string                 `json:"level"`     // "debug", "info", "warn", "error"
	Component string                 `json:"component"` // "hardware", "deploy", "chroot", "ipmi", "efiboot"
	Phase     string                 `json:"phase,omitempty"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// LogFilter specifies query constraints for log retrieval.
type LogFilter struct {
	NodeMAC   string
	Hostname  string
	Level     string
	Component string
	Since     *time.Time
	Limit     int
}

// ListLogsResponse wraps a log query result.
type ListLogsResponse struct {
	Logs  []LogEntry `json:"logs"`
	Total int        `json:"total"`
}

// ─── PXE / auto-registration types ───────────────────────────────────────────

// RegisterRequest is the body for POST /api/v1/nodes/register.
// Sent by the clustr client on first PXE boot to register itself with the server.
type RegisterRequest struct {
	// HardwareProfile is the raw JSON from hardware.Discover().
	HardwareProfile json.RawMessage `json:"hardware_profile"`
	// DetectedFirmware is the node's boot firmware type as detected by the
	// deploy agent: "uefi" or "bios". Populated from hardware.DetectFirmware().
	// Empty string means unknown (legacy clients that predate this field).
	DetectedFirmware string `json:"detected_firmware,omitempty"`
	// MulticastMode is the value of --multicast from the deploy agent (#157).
	// Accepted values: "auto" (default), "off", "require".
	// Empty string is treated as "auto" by the server for backward compatibility.
	MulticastMode string `json:"multicast_mode,omitempty"`
	// LegacyConfigApply, when true, instructs the deploy agent to re-apply
	// hostname, sssd, hosts, and limits via the imperative path during Finalize.
	// By default (false) these four plugins are managed by the reactive observer
	// (Sprint 36) and are skipped during the in-chroot finalize step.
	// Set by --legacy-config-apply on the deploy CLI.
	// Backward-compatible: omitempty means older servers receive no field and
	// treat it as false (default behaviour unchanged).
	LegacyConfigApply bool `json:"legacy_config_apply,omitempty"`
}

// RegisterResponse is the response body for POST /api/v1/nodes/register.
type RegisterResponse struct {
	NodeConfig *NodeConfig `json:"node_config"`
	// Action tells the client what to do next:
	//   "deploy"      — an image has been assigned; proceed with deployment.
	//   "bios_only"   — apply BIOS profile, then reboot immediately (no image fetch).
	//   "wait"        — no image assigned yet; poll GET /api/v1/nodes/by-mac/:mac every 30s.
	//   "capture"     — admin wants to capture this node's image (future).
	Action string `json:"action"`
	// DryRun, when true, instructs the deploy client to execute the full PXE
	// boot sequence (disk selection, partitioning decisions, etc.) but skip the
	// actual disk wipe and filesystem operations. Set when the triggering
	// reimage request had dry_run=true.
	DryRun bool `json:"dry_run,omitempty"`
	// BiosProfile, when non-nil, is the BIOS profile assigned to this node.
	// The deploy agent applies this profile in initramfs before image fetch.
	// Nil when no profile is assigned. (#159)
	BiosProfile *BiosProfile `json:"bios_profile,omitempty"`
	// BiosOnly, when true, instructs the deploy agent to apply BiosProfile and
	// then reboot without fetching an image. Set when the triggering reimage
	// request had bios_only=true. (#159)
	BiosOnly bool `json:"bios_only,omitempty"`
}

// ─── Factory request types ────────────────────────────────────────────────────

// ImportISORequest is the JSON metadata posted alongside a multipart ISO upload.
type ImportISORequest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ISOEnvironmentGroup describes one installable environment group from an
// ISO's comps XML, as returned by POST /api/v1/factory/probe-iso.
type ISOEnvironmentGroup struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	DisplayOrder int    `json:"display_order,omitempty"`
	IsDefault    bool   `json:"is_default"`
}

// ProbeISORequest is the body for POST /api/v1/factory/probe-iso.
type ProbeISORequest struct {
	URL string `json:"url"`
}

// ProbeISOResponse is returned by POST /api/v1/factory/probe-iso.
type ProbeISOResponse struct {
	URL          string                `json:"url"`
	Distro       string                `json:"distro"`
	VolumeLabel  string                `json:"volume_label,omitempty"`
	Environments []ISOEnvironmentGroup `json:"environments"`
	// NoComps is true when the ISO does not contain comps XML (Ubuntu, Debian,
	// minimal ISOs without group data). The UI should suppress the picker.
	NoComps bool `json:"no_comps,omitempty"`
}

// CaptureRequest is the body for POST /api/v1/factory/capture.
type CaptureRequest struct {
	// SourceHost is the SSH-reachable hostname or IP of the node to capture.
	SourceHost   string   `json:"source_host"`
	SSHUser      string   `json:"ssh_user,omitempty"`
	SSHPassword  string   `json:"ssh_password,omitempty"` // write-only, never returned
	SSHKeyPath   string   `json:"ssh_key_path,omitempty"`
	SSHPort      int      `json:"ssh_port,omitempty"` // defaults to 22 when zero
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Tags         []string `json:"tags"`
	Notes        string   `json:"notes"`
	ExcludePaths []string `json:"exclude_paths,omitempty"` // rsync --exclude patterns
}

// BuildFromISORequest is the body for POST /api/v1/factory/build-from-iso.
// It instructs clustr to download an installer ISO, run it in a temporary QEMU
// VM with an auto-generated kickstart/autoinstall config, and capture the
// installed OS as a deployable BaseImage.
//
// The build runs asynchronously and can take 5-30 minutes. Poll
// GET /api/v1/images/:id for status transitions: building → ready | error.
type BuildFromISORequest struct {
	// URL is the HTTP(S) URL of the installer ISO. Required.
	// Example: "https://download.rockylinux.org/pub/rocky/10/isos/x86_64/Rocky-10.1-x86_64-dvd1.iso"
	URL string `json:"url"`

	// Name is the human-readable name for the resulting BaseImage. Required.
	Name string `json:"name"`

	// Version is the image version string, e.g. "10.1". Optional.
	Version string `json:"version,omitempty"`

	// OS is a short OS identifier, e.g. "rocky", "ubuntu". Optional — auto-detected
	// from URL when empty.
	OS string `json:"os,omitempty"`

	// Arch is the CPU architecture, e.g. "x86_64". Optional.
	Arch string `json:"arch,omitempty"`

	// Distro explicitly specifies the distribution family when auto-detection
	// is unreliable. Valid values: "rocky", "almalinux", "centos", "rhel",
	// "ubuntu", "debian", "suse", "alpine". Optional — auto-detected from URL.
	Distro string `json:"distro,omitempty"`

	// DiskSizeGB is the size in GiB of the blank disk presented to the installer.
	// Default: 20. Minimum: 10. The installed rootfs will be smaller.
	DiskSizeGB int `json:"disk_size_gb,omitempty"`

	// MemoryMB is the RAM in MiB allocated to the installer VM. Default: 2048.
	MemoryMB int `json:"memory_mb,omitempty"`

	// CPUs is the number of virtual CPUs for the installer VM. Default: 2.
	CPUs int `json:"cpus,omitempty"`

	// RoleIDs is the list of HPC node role preset IDs to include in the build.
	// Each role ID corresponds to a Role returned by GET /api/v1/image-roles.
	// The role package lists are merged and written into the kickstart %packages
	// stanza. Ignored when CustomKickstart is non-empty.
	RoleIDs []string `json:"role_ids,omitempty"`

	// InstallUpdates, when true, appends a %post section that runs the distro's
	// package manager update command (dnf update -y / apt-get upgrade -y).
	// Adds 5-10 minutes to the build but produces a fully patched image.
	InstallUpdates bool `json:"install_updates,omitempty"`

	// CustomKickstart, when non-empty, overrides the auto-generated
	// kickstart/autoinstall config with admin-supplied content.
	// Only respected for RHEL-family distros (Rocky, Alma, CentOS, RHEL).
	// For other distros, this field is silently ignored.
	CustomKickstart string `json:"custom_kickstart,omitempty"`

	// DefaultUsername, when non-empty, creates a named user in the installed OS
	// with sudo/wheel access. Supported for RHEL-family (Rocky, Alma, CentOS, RHEL)
	// kickstart builds. Silently ignored for other distros.
	DefaultUsername string `json:"default_username,omitempty"`

	// DefaultPassword is the plaintext password for DefaultUsername and for the
	// root account. It is hashed server-side before being written to the installer
	// config; it is never stored or logged in plaintext.
	// When omitted, the root account uses a fixed per-build hash and no user
	// directive is emitted.
	DefaultPassword string `json:"default_password,omitempty"`

	// Firmware selects the firmware mode for the installer VM and resulting image.
	// Allowed values: "uefi" (default) and "bios" (legacy SeaBIOS). When empty,
	// "uefi" is assumed for backward compatibility.
	// - "uefi": OVMF pflash drives are passed to QEMU; ESP partition is created;
	//   efibootmgr is used during finalization.
	// - "bios": SeaBIOS (-bios flag) is used; a biosboot GPT partition is created;
	//   grub2-install --target=i386-pc is run during finalization.
	Firmware string `json:"firmware,omitempty"`

	// SELinuxMode controls the SELinux mode in the built image.
	// Allowed values: "disabled", "permissive", "enforcing".
	// Default: "disabled" (common for HPC clusters).
	SELinuxMode string `json:"selinux_mode,omitempty"`

	// Tags is an optional list of string tags attached to the resulting image.
	Tags []string `json:"tags,omitempty"`

	// Notes is a free-text description stored on the resulting image.
	Notes string `json:"notes,omitempty"`

	// BaseEnvironment is the comps environment group to install, e.g.
	// "minimal-environment" or "server-product-environment". Only applies to
	// RHEL-family kickstart builds. When empty, "minimal-environment" is used.
	// Obtain valid values from POST /api/v1/factory/probe-iso before building.
	BaseEnvironment string `json:"base_environment,omitempty"`
}

// ImageRoleResponse is the wire type for a single HPC role preset returned by
// GET /api/v1/image-roles. It is the read-only, UI-facing projection of the
// internal isoinstaller.Role type.
type ImageRoleResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	PackageCount int    `json:"package_count"` // unique packages across all supported distros
	Notes        string `json:"notes,omitempty"`
}

// ListImageRolesResponse wraps the role list returned by GET /api/v1/image-roles.
type ListImageRolesResponse struct {
	Roles []ImageRoleResponse `json:"roles"`
	Total int                 `json:"total"`
}

// ─── Shell session types ──────────────────────────────────────────────────────

// ShellSessionResponse is returned when a session is opened.
type ShellSessionResponse struct {
	SessionID       string `json:"session_id"`
	ImageID         string `json:"image_id"`
	RootDir         string `json:"root_dir"`
	Warning         string `json:"warning"`
	WarningSeverity string `json:"warning_severity"`
}

// ShellMutationWarning is the canonical warning text included in every
// ShellSessionResponse and in the initial WebSocket warning frame.
// It is exported so tests can assert on the exact string.
const ShellMutationWarning = "EXPERIMENTAL: shell sessions can mutate the base image rootfs. " +
	"Image checksum will be invalidated on session close. " +
	"Overlay isolation (read-only base + RW overlay) is planned but not yet implemented."

// ExecRequest is the body for POST /api/v1/images/:id/shell-session/:sid/exec.
type ExecRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// ExecResponse is returned by the exec endpoint.
type ExecResponse struct {
	Output string `json:"output"`
}

// ─── Reimage types ────────────────────────────────────────────────────────────

// ReimageStatus enumerates valid states for a ReimageRequest.
type ReimageStatus string

const (
	ReimageStatusPending    ReimageStatus = "pending"
	ReimageStatusTriggered  ReimageStatus = "triggered"
	ReimageStatusInProgress ReimageStatus = "in_progress"
	ReimageStatusComplete   ReimageStatus = "complete"
	ReimageStatusFailed     ReimageStatus = "failed"
	ReimageStatusCanceled   ReimageStatus = "canceled"
)

// IsTerminal reports whether s is a terminal state (no further transitions).
func (s ReimageStatus) IsTerminal() bool {
	switch s {
	case ReimageStatusComplete, ReimageStatusFailed, ReimageStatusCanceled:
		return true
	}
	return false
}

// ReimageRequest is the server-side record for a reimage lifecycle.
type ReimageRequest struct {
	ID           string        `json:"id"`
	NodeID       string        `json:"node_id"`
	ImageID      string        `json:"image_id"`
	Status       ReimageStatus `json:"status"`
	ScheduledAt  *time.Time    `json:"scheduled_at,omitempty"`
	TriggeredAt  *time.Time    `json:"triggered_at,omitempty"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	CompletedAt  *time.Time    `json:"completed_at,omitempty"`
	ErrorMessage string        `json:"error_message,omitempty"`
	RequestedBy  string        `json:"requested_by"`
	DryRun       bool          `json:"dry_run,omitempty"`
	// BiosOnly, when true, means this reimage only applies BIOS settings.
	// The node PXE-boots into initramfs, applies the assigned profile, then reboots
	// without fetching an image. (#159)
	BiosOnly     bool          `json:"bios_only,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	// Terminal-state detail — populated on deploy-failed; nil on success or in-flight.
	ExitCode *int   `json:"exit_code,omitempty"`
	ExitName string `json:"exit_name,omitempty"`
	Phase    string `json:"phase,omitempty"`
	// InjectVars holds the per-deployment custom variable overrides (S4-11).
	// Merged with the node's custom_vars at trigger time; not persisted.
	// Delivered to the deploy agent via initramfs kernel cmdline.
	InjectVars map[string]string `json:"inject_vars,omitempty"`
}

// DeployFailedPayload is the JSON body for POST /api/v1/nodes/:id/deploy-failed.
// The deploy agent sends this so the server can capture classified failure detail.
type DeployFailedPayload struct {
	ExitCode int    `json:"exit_code"`
	ExitName string `json:"exit_name"`
	Phase    string `json:"phase"`
	Message  string `json:"message"`
}

// VerifyBootRequest is the JSON body for POST /api/v1/nodes/:id/verify-boot.
// Sent by the deployed OS on first boot (via clustr-verify-boot.service systemd
// oneshot) to confirm the bootloader, kernel, and systemd all started correctly.
// See ADR-0008.
type VerifyBootRequest struct {
	// Hostname is the OS-reported hostname (from /etc/hostname or uname -n).
	Hostname string `json:"hostname"`
	// KernelVersion is the running kernel version (from uname -r).
	KernelVersion string `json:"kernel_version"`
	// UptimeSeconds is the node uptime in seconds at call time (from /proc/uptime).
	// Used to verify the node has just booted rather than replaying an old request.
	UptimeSeconds float64 `json:"uptime_seconds"`
	// SystemctlState is the output of `systemctl is-system-running`.
	// Expected values: "running", "degraded". Other values are logged but not rejected.
	SystemctlState string `json:"systemctl_state"`
	// OSRelease is the OS identification string (from /etc/os-release PRETTY_NAME).
	OSRelease string `json:"os_release"`
	// SSSDStatus is the output of `sssctl domain-status <domain>` or "not_installed"
	// if sssd is absent. Empty string means the probe was not run (older client).
	// Sprint 15 #99 — LDAP node integration hardening.
	SSSDStatus string `json:"sssd_status,omitempty"`
	// PAMSSSOPresent is true when /etc/pam.d/system-auth contains "pam_sss.so".
	// Sprint 15 #99.
	PAMSSSPresent bool `json:"pam_sss_present,omitempty"`
}

// CreateReimageRequest is the body for POST /api/v1/nodes/:id/reimage.
type CreateReimageRequest struct {
	// ImageID is the base image to deploy. If empty the node's currently
	// assigned base_image_id is used.
	ImageID string `json:"image_id,omitempty"`
	// ScheduledAt, when non-nil, defers the reimage. nil = immediate.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	// DryRun sets next boot to PXE and power-cycles but does not wipe the disk.
	DryRun bool `json:"dry_run,omitempty"`
	// Force skips the image-ready and active-reimage pre-checks.
	Force bool `json:"force,omitempty"`
	// InjectVars, when non-nil, is merged with the node's custom_vars for THIS
	// deployment only (not persisted to the database). Keys in InjectVars override
	// the node's stored custom_vars. The merged set is delivered to the deploy
	// agent via initramfs kernel cmdline. (S4-11)
	InjectVars map[string]string `json:"inject_vars,omitempty"`
	// BiosOnly, when true, triggers a BIOS-settings-only apply: the node PXE-boots
	// into initramfs, applies the assigned BIOS profile via the vendor binary, and
	// reboots immediately without touching the disk or fetching an image. (#159)
	BiosOnly bool `json:"bios_only,omitempty"`
}

// ListReimagesResponse wraps the reimage history list.
type ListReimagesResponse struct {
	Requests []ReimageRequest `json:"requests"`
	Total    int              `json:"total"`
}

// ─── ISO build progress types ─────────────────────────────────────────────────

// BuildPhase is a named step in the ISO build pipeline.
type BuildPhase = string

const (
	BuildPhaseDownloadingISO   = "downloading_iso"
	BuildPhaseGeneratingConfig = "generating_config"
	BuildPhaseCreatingDisk     = "creating_disk"
	BuildPhaseLaunchingVM      = "launching_vm"
	BuildPhaseInstalling       = "installing"
	BuildPhaseExtracting       = "extracting"
	BuildPhaseScrubbing        = "scrubbing"
	BuildPhaseFinalizing       = "finalizing"
	BuildPhaseComplete         = "complete"
	BuildPhaseFailed           = "failed"
	BuildPhaseCanceled         = "canceled"
)

// BuildState is a snapshot of the current progress for one ISO build job.
// Returned by GET /api/v1/images/:id/build-progress.
type BuildState struct {
	ImageID      string    `json:"image_id"`
	Phase        string    `json:"phase"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	BytesTotal   int64     `json:"bytes_total"`
	BytesDone    int64     `json:"bytes_done"`
	ElapsedMS    int64     `json:"elapsed_ms"`
	ErrorMessage string    `json:"error_message,omitempty"`
	// SerialTail holds up to 100 recent lines from the QEMU serial console.
	SerialTail []string `json:"serial_tail,omitempty"`
	// QEMUStderr holds up to 50 recent lines from QEMU's own stderr output.
	QEMUStderr []string `json:"qemu_stderr,omitempty"`
}

// BuildEvent is one SSE message sent to subscribers of the build progress stream.
// It carries either a full state snapshot (on initial connect) or an incremental
// update (phase change, serial line, progress tick).
type BuildEvent struct {
	ImageID    string `json:"image_id"`
	Phase      string `json:"phase,omitempty"`
	SerialLine string `json:"serial_line,omitempty"` // non-empty = append-only line event
	StderrLine string `json:"stderr_line,omitempty"`
	BytesTotal int64  `json:"bytes_total,omitempty"`
	BytesDone  int64  `json:"bytes_done,omitempty"`
	ElapsedMS  int64  `json:"elapsed_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ── Network module types ──────────────────────────────────────────────────────

// NetworkSwitchRole enumerates the valid roles for a switch in the fabric.
type NetworkSwitchRole string

const (
	NetworkSwitchRoleManagement NetworkSwitchRole = "management" // IPMI/BMC access
	NetworkSwitchRoleData       NetworkSwitchRole = "data"       // compute traffic
	NetworkSwitchRoleInfiniBand NetworkSwitchRole = "infiniband" // IB fabric
)

// NetworkSwitch is an inventory record for a physical switch in the cluster fabric.
// clustr does not program switches in v1; this is documentation + SM-detection input.
type NetworkSwitch struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Role      NetworkSwitchRole `json:"role"`
	Vendor    string            `json:"vendor,omitempty"`
	Model     string            `json:"model,omitempty"`
	MgmtIP    string            `json:"mgmt_ip,omitempty"`
	Notes     string            `json:"notes,omitempty"`
	IsManaged bool              `json:"is_managed"` // for IB: false = no built-in SM
	// MACAddress is the MAC seen in the DHCP discover that triggered auto-discovery.
	// Empty for manually created switches.
	MACAddress string `json:"mac_address,omitempty"`
	// Status is "confirmed" (admin-created or admin-confirmed) or "discovered" (auto-detected via DHCP).
	Status string `json:"status,omitempty"`
	// DiscoveredAt is set when auto-discovery created this record.
	DiscoveredAt *time.Time `json:"discovered_at,omitempty"`
	// PortCount is the total number of switchports; used by the cabling plan generator.
	PortCount int `json:"port_count,omitempty"`
	// UplinkPorts is a comma-separated list of uplink port numbers excluded from node assignment.
	UplinkPorts string    `json:"uplink_ports,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// BondMember identifies a NIC to be enslaved to a bond.
type BondMember struct {
	ID        string `json:"id"`
	BondID    string `json:"bond_id"`
	MatchMAC  string `json:"match_mac,omitempty"`
	MatchName string `json:"match_name,omitempty"`
	SortOrder int    `json:"sort_order"`
}

// BondConfig describes one bond interface within a NetworkProfile.
type BondConfig struct {
	ID             string       `json:"id"`
	ProfileID      string       `json:"profile_id"`
	BondName       string       `json:"bond_name"` // "bond0"
	Mode           string       `json:"mode"`      // "802.3ad", "active-backup", etc.
	MTU            int          `json:"mtu"`
	VLANID         int          `json:"vlan_id"`   // 0 = no VLAN
	IPMethod       string       `json:"ip_method"` // "static", "dhcp", "none"
	IPCIDR         string       `json:"ip_cidr,omitempty"`
	LACPRate       string       `json:"lacp_rate,omitempty"`
	XmitHashPolicy string       `json:"xmit_hash_policy,omitempty"`
	SortOrder      int          `json:"sort_order"`
	Members        []BondMember `json:"members"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// IBProfile holds InfiniBand / IPoIB configuration for a NetworkProfile.
type IBProfile struct {
	ID          string    `json:"id"`
	ProfileID   string    `json:"profile_id"`
	IPoIBMode   string    `json:"ipoib_mode"`             // "connected" or "datagram"
	IPoIBMTU    int       `json:"ipoib_mtu"`              // 65520 for connected, 2044 for datagram
	IPMethod    string    `json:"ip_method"`              // "static", "dhcp", "none"
	PKeys       []string  `json:"pkeys"`                  // ["0x7fff", "0x8001"]
	DeviceMatch string    `json:"device_match,omitempty"` // "mlx5_" or "hfi1_"
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NetworkProfile is the top-level network configuration entity assigned to a NodeGroup.
type NetworkProfile struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Bonds       []BondConfig `json:"bonds,omitempty"`
	IB          *IBProfile   `json:"ib,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// OpenSMConfig holds the cluster-wide OpenSM configuration.
// Only one instance exists per clustr install. When Enabled=false, no OpenSM
// config is injected anywhere.
type OpenSMConfig struct {
	ID                string    `json:"id"`
	Enabled           bool      `json:"enabled"`
	HeadNodeProfileID string    `json:"head_node_profile_id"`
	ConfContent       string    `json:"conf_content"` // full opensm.conf text
	LogPrefix         string    `json:"log_prefix"`
	SMPriority        int       `json:"sm_priority"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ── Slurm module types ────────────────────────────────────────────────────────

// SlurmModuleConfig is the module state returned by GET /api/v1/slurm/status.
type SlurmModuleConfig struct {
	Enabled      bool     `json:"enabled"`
	Status       string   `json:"status"` // not_configured|ready|disabled|error
	ClusterName  string   `json:"cluster_name"`
	ManagedFiles []string `json:"managed_files"`
}

// SlurmNodeConfig is the read-only projection embedded in NodeConfig.
// Nil means the Slurm module is not active; finalize.go skips writeSlurmConfig().
// Non-nil means the module is enabled and this node should receive Slurm configs.
type SlurmNodeConfig struct {
	ClusterName string            `json:"cluster_name"`
	Roles       []string          `json:"roles,omitempty"` // e.g. ["controller"] or ["compute"]
	Configs     []SlurmConfigFile `json:"configs"`         // rendered content per file, ready to write
	Scripts     []SlurmScriptFile `json:"scripts,omitempty"`
	// SlurmRepoURL is the dnf repo URL for auto-install.  Empty = skip auto-install.
	SlurmRepoURL string `json:"slurm_repo_url,omitempty"`
	// MungeKey is the raw munge key bytes, base64-encoded (standard encoding).
	// finalize.go decodes this and writes it to /etc/munge/munge.key (mode 0400,
	// owner munge:munge) so munged can start on first boot.
	// Empty means no key was available — munge will fail to start (degraded node).
	MungeKey string `json:"munge_key,omitempty"`
	// GPGKey is the ASCII-armored clustr release GPG public key. When non-empty,
	// finalize.go writes it to /etc/pki/rpm-gpg/RPM-GPG-KEY-clustr in the
	// chroot and configures gpgcheck=1 in the generated .repo file. Populated
	// by the slurm manager when SlurmRepoURL resolves to the clustr-builtin
	// bundled repo path. Empty for operator-override custom repo URLs (gpgcheck=0).
	GPGKey string `json:"gpg_key,omitempty"`
}

// SlurmConfigFile is a rendered config file, ready for delivery to a node.
type SlurmConfigFile struct {
	Filename string `json:"filename"`  // e.g. "slurm.conf"
	Path     string `json:"path"`      // e.g. "/etc/slurm/slurm.conf"
	Content  string `json:"content"`   // rendered, node-specific plain text
	Checksum string `json:"checksum"`  // sha256 of Content
	FileMode string `json:"file_mode"` // e.g. "0644"
	Owner    string `json:"owner"`     // e.g. "slurm:slurm"
	Version  int    `json:"version"`   // version number from slurm_config_files
}

// SlurmScriptFile is a rendered Slurm hook script ready for delivery to a node.
type SlurmScriptFile struct {
	ScriptType string `json:"script_type"` // e.g. "Prolog"
	DestPath   string `json:"dest_path"`   // e.g. "/etc/slurm/prolog.sh"
	Content    string `json:"content"`
	Checksum   string `json:"checksum"`
	Version    int    `json:"version"`
}

// SlurmNodeOverride holds per-node hardware parameters and GRES data.
type SlurmNodeOverride struct {
	NodeID    string            `json:"node_id"`
	Params    map[string]string `json:"params"` // keyed by override_key
	UpdatedAt int64             `json:"updated_at"`
}

// SlurmPushOperation is the push operation status returned by the push endpoints.
type SlurmPushOperation struct {
	ID           string                     `json:"id"`
	Filenames    []string                   `json:"filenames"`
	FileVersions map[string]int             `json:"file_versions"`
	ApplyAction  string                     `json:"apply_action"`
	Status       string                     `json:"status"`
	NodeCount    int                        `json:"node_count"`
	SuccessCount int                        `json:"success_count"`
	FailureCount int                        `json:"failure_count"`
	StartedAt    int64                      `json:"started_at"`
	CompletedAt  *int64                     `json:"completed_at,omitempty"`
	NodeResults  map[string]SlurmNodeResult `json:"node_results,omitempty"`
}

// SlurmNodeResult is the per-node push result included in SlurmPushOperation.
type SlurmNodeResult struct {
	OK            bool                `json:"ok"`
	Error         string              `json:"error,omitempty"`
	FileResults   []SlurmFileResult   `json:"file_results"`
	ScriptResults []SlurmScriptResult `json:"script_results,omitempty"`
	ApplyResult   SlurmApplyResult    `json:"apply_result"`
}

// SlurmScriptResult is the per-script result within a SlurmNodeResult.
type SlurmScriptResult struct {
	ScriptType string `json:"script_type"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// SlurmFileResult is the per-file result within a SlurmNodeResult.
type SlurmFileResult struct {
	Filename string `json:"filename"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// SlurmApplyResult describes the outcome of the apply action (reconfigure/restart).
type SlurmApplyResult struct {
	Action   string `json:"action"`
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
}

// ── Slurm jobs / partitions (Sprint 24 #153) ─────────────────────────────────

// SlurmJob is a single job row returned by GET /api/v1/slurm/jobs.
// Fields are parsed from `squeue --noheader --format=...` output.
type SlurmJob struct {
	JobID       string `json:"job_id"`
	Name        string `json:"name"`
	State       string `json:"state"`       // e.g. RUNNING, PENDING, COMPLETED, FAILED
	User        string `json:"user"`
	Partition   string `json:"partition"`
	NumNodes    string `json:"num_nodes"`    // e.g. "2" or "1-4"
	TimeUsed    string `json:"time_used"`    // elapsed walltime D-HH:MM:SS
	TimeLimit   string `json:"time_limit"`   // D-HH:MM:SS or "UNLIMITED"
	Command     string `json:"command"`
	ReqCPUs     string `json:"req_cpus"`
	ReqMemory   string `json:"req_memory"`
	NodeList    string `json:"node_list"`    // allocated node list (NODELIST)
	Reason      string `json:"reason"`       // PendingReason when PENDING
}

// ListSlurmJobsResponse is returned by GET /api/v1/slurm/jobs.
type ListSlurmJobsResponse struct {
	Jobs  []SlurmJob `json:"jobs"`
	Total int        `json:"total"`
}

// SlurmPartitionInfo is a single partition row returned by GET /api/v1/slurm/partitions.
// Fields are parsed from `sinfo --noheader --format=...` output.
type SlurmPartitionInfo struct {
	Name           string `json:"name"`
	State          string `json:"state"`           // up|down|drain|inact
	TotalNodes     int    `json:"total_nodes"`
	AllocatedNodes int    `json:"allocated_nodes"`
	IdleNodes      int    `json:"idle_nodes"`
	IsDefault      bool   `json:"is_default"`
	MaxTime        string `json:"max_time"`         // e.g. "7-00:00:00" or "UNLIMITED"
}

// ListSlurmPartitionsResponse is returned by GET /api/v1/slurm/partitions.
type ListSlurmPartitionsResponse struct {
	Partitions []SlurmPartitionInfo `json:"partitions"`
	Total      int                  `json:"total"`
}

// ── Slurm build types (Sprint 8) ─────────────────────────────────────────────

// SlurmBuild is the API representation of one Slurm build attempt.
type SlurmBuild struct {
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	Arch             string   `json:"arch"`
	Status           string   `json:"status"` // building|completed|failed
	ConfigureFlags   []string `json:"configure_flags,omitempty"`
	ArtifactPath     string   `json:"artifact_path,omitempty"`
	ArtifactChecksum string   `json:"artifact_checksum,omitempty"`
	ArtifactSize     int64    `json:"artifact_size_bytes,omitempty"`
	StartedAt        int64    `json:"started_at"`
	CompletedAt      *int64   `json:"completed_at,omitempty"`
	ErrorMessage     string   `json:"error_message,omitempty"`
	IsActive         bool     `json:"is_active"`
}

// SlurmBuildDep is the API representation of one dependency artifact.
type SlurmBuildDep struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	ArtifactPath     string `json:"artifact_path"`
	ArtifactChecksum string `json:"artifact_checksum"`
}

// SlurmBinaryPushPayload is the payload for the "slurm_binary_push" server→node message.
// The server pushes this to instruct nodes to download and install a new Slurm build.
type SlurmBinaryPushPayload struct {
	BuildID     string `json:"build_id"`
	Version     string `json:"version"`
	ArtifactURL string `json:"artifact_url"`
	Checksum    string `json:"checksum"`
}

// DHCPLease is a single row in the DHCP allocations view.
// Fields are derived entirely from the node_configs table — no dnsmasq lease
// files are read. IP is stored as a CIDR in node interfaces; we surface the
// plain IP (no prefix length) here for readability.
type DHCPLease struct {
	NodeID      string     `json:"node_id"`
	Hostname    string     `json:"hostname"`
	MAC         string     `json:"mac"`
	IP          string     `json:"ip"` // plain dotted-decimal, no CIDR suffix
	Role        string     `json:"role,omitempty"`
	DeployState string     `json:"deploy_state"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	FirstSeenAt time.Time  `json:"first_seen_at"` // node created_at
}

// DHCPLeasesResponse is returned by GET /api/v1/dhcp/leases.
type DHCPLeasesResponse struct {
	Leases []DHCPLease `json:"leases"`
	Count  int         `json:"count"`
}

// Bundle describes a slurm RPM catalog entry visible in the Bundles tab.
//
// All entries come from the slurm_builds table — there are no synthetic entries.
// kind: "build" = from slurm_builds table (clustr build pipeline).
// source: "clustr-build-pipeline" = POST /api/v1/slurm/builds.
//
// GET /api/v1/bundles and the Slurm tab #builds section read from the same
// backing data (slurm_builds table). There is no separate store.
type Bundle struct {
	// ID is the slurm_builds.id UUID.
	ID            string `json:"id"`
	Name          string `json:"name"`           // e.g. "slurm-25.11.5-x86_64"
	SlurmVersion  string `json:"slurm_version"`  // e.g. "25.11.5"
	BundleVersion string `json:"bundle_version"` // matches SlurmVersion for build entries
	SHA256        string `json:"sha256"`         // artifact tarball SHA256 hex
	Kind          string `json:"kind"`           // "build"
	Source        string `json:"source"`         // "clustr-build-pipeline"

	// Status mirrors slurm_builds.status: "building" | "completed" | "failed"
	Status string `json:"status"`
	// IsActive is true when this build is the active RPM source for the cluster.
	IsActive bool `json:"is_active"`

	// NodesUsing is the count of enrolled nodes currently running this build's version.
	NodesUsing int `json:"nodes_using"`
	// LastDeployedAt is the unix timestamp of the most recent dnf install from this build.
	LastDeployedAt int64 `json:"last_deployed_at,omitempty"`

	// SigStatus: "signed" | "unsigned" | "unknown"
	SigStatus string `json:"sig_status,omitempty"`

	// Build timestamps (unix seconds).
	StartedAt   int64  `json:"started_at,omitempty"`
	CompletedAt *int64 `json:"completed_at,omitempty"`
}

// ListBundlesResponse is returned by GET /api/v1/bundles.
type ListBundlesResponse struct {
	Bundles []Bundle `json:"bundles"`
	Total   int      `json:"total"`
}

// ─── Boot Menu entries (#160) ────────────────────────────────────────────────

// BootEntryKind is the type of a boot entry.
type BootEntryKind string

const (
	BootEntryKindKernel  BootEntryKind = "kernel"
	BootEntryKindISO     BootEntryKind = "iso"
	BootEntryKindRescue  BootEntryKind = "rescue"
	BootEntryKindMemtest BootEntryKind = "memtest"
)

// BootEntry is a single row in the boot_entries table.
// Enabled entries are appended to the iPXE disk-boot menu at PXE-serve time.
type BootEntry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`      // "kernel" | "iso" | "rescue" | "memtest"
	KernelURL string    `json:"kernel_url"`
	InitrdURL string    `json:"initrd_url,omitempty"` // optional
	Cmdline   string    `json:"cmdline,omitempty"`    // optional
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListBootEntriesResponse is returned by GET /api/v1/boot-entries.
type ListBootEntriesResponse struct {
	Entries []BootEntry `json:"entries"`
	Total   int         `json:"total"`
}

// CreateBootEntryRequest is the body for POST /api/v1/boot-entries.
type CreateBootEntryRequest struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	KernelURL string `json:"kernel_url"`
	InitrdURL string `json:"initrd_url,omitempty"`
	Cmdline   string `json:"cmdline,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"` // defaults to true when nil
}

// UpdateBootEntryRequest is the body for PUT /api/v1/boot-entries/{id}.
type UpdateBootEntryRequest struct {
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	KernelURL string `json:"kernel_url,omitempty"`
	InitrdURL string `json:"initrd_url,omitempty"`
	Cmdline   string `json:"cmdline,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

// NetworkNodeConfig carries the resolved per-node network configuration
// injected into NodeConfig during the deploy pipeline.
type NetworkNodeConfig struct {
	// Bonds is the list of bond interfaces to create. Each entry produces
	// a set of NM keyfiles in the deployed rootfs.
	Bonds []BondConfig `json:"bonds,omitempty"`
	// IB, when non-nil, produces an IPoIB NM keyfile in the deployed rootfs.
	IB *IBProfile `json:"ib,omitempty"`
	// OpenSMConf, when non-empty, is written to /etc/opensm/opensm.conf
	// and opensm.service is enabled. Only set on the designated head node group.
	OpenSMConf string `json:"opensm_conf,omitempty"`
}

// ─── BIOS profiles (#159) ─────────────────────────────────────────────────────

// BiosProfile is a named, reusable set of vendor-specific BIOS settings stored
// in bios_profiles.  settings_json is an opaque flat JSON object whose keys are
// vendor-defined setting names and whose values are the desired setting values.
//
// Example settings_json for Intel SYSCFG:
//
//	{"Intel(R) Hyper-Threading Technology": "Disable", "Power Performance Tuning": "OS Controls EPB"}
//
// clustr does not own the settings schema; the operator must match the keys and
// values to what their firmware version accepts.  See docs/BIOS-INTEL-SETUP.md.
type BiosProfile struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Vendor       string    `json:"vendor"`        // "intel"; "dell"/"supermicro" in future sprints
	SettingsJSON string    `json:"settings_json"` // raw JSON object: {"name": "value", ...}
	Description  string    `json:"description,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NodeBiosProfile is the per-node binding stored in node_bios_profile.
// The absence of a row means the node has no assigned BIOS profile.
type NodeBiosProfile struct {
	NodeID               string     `json:"node_id"`
	ProfileID            string     `json:"profile_id"`
	LastAppliedAt        *time.Time `json:"last_applied_at,omitempty"`   // nil until first apply
	AppliedSettingsHash  string     `json:"applied_settings_hash,omitempty"` // sha256(settings_json) at last apply
	LastApplyError       string     `json:"last_apply_error,omitempty"`  // non-empty on last failure
}

// CreateBiosProfileRequest is the body for POST /api/v1/bios-profiles.
type CreateBiosProfileRequest struct {
	Name         string `json:"name"`
	Vendor       string `json:"vendor"`
	SettingsJSON string `json:"settings_json"` // must be a valid JSON object
	Description  string `json:"description,omitempty"`
}

// UpdateBiosProfileRequest is the body for PUT /api/v1/bios-profiles/{id}.
type UpdateBiosProfileRequest struct {
	Name         string `json:"name,omitempty"`
	SettingsJSON string `json:"settings_json,omitempty"`
	Description  string `json:"description,omitempty"`
}

// AssignBiosProfileRequest is the body for PUT /api/v1/nodes/{id}/bios-profile.
type AssignBiosProfileRequest struct {
	ProfileID string `json:"profile_id"`
}

// BiosProfileResponse wraps a single BiosProfile for API responses.
type BiosProfileResponse struct {
	Profile BiosProfile `json:"profile"`
}

// ListBiosProfilesResponse is the response body for GET /api/v1/bios-profiles.
type ListBiosProfilesResponse struct {
	Profiles []BiosProfile `json:"profiles"`
	Total    int           `json:"total"`
}

// NodeBiosProfileResponse wraps a NodeBiosProfile for API responses.
type NodeBiosProfileResponse struct {
	Binding NodeBiosProfile `json:"binding"`
}

// BiosProviderVerifyResponse is the response for GET /api/v1/bios/providers/{vendor}/verify.
type BiosProviderVerifyResponse struct {
	Vendor    string `json:"vendor"`
	Available bool   `json:"available"` // true when operator binary is present and executable
	BinPath   string `json:"bin_path"`  // expected path for operator reference
	Message   string `json:"message,omitempty"`
}

// BiosApplyResponse is the response body for POST /api/v1/nodes/{id}/bios/apply.
// Applied is the number of settings written to NVRAM.  When Applied is 0 and
// Message is "no changes — node is already at desired state", the node was
// already compliant and no privhelper invocation occurred.
// When Applied > 0, settings have been staged to NVRAM; a reboot is required
// for them to take effect (Message will say so).
type BiosApplyResponse struct {
	Applied int    `json:"applied"`
	Message string `json:"message"`
}

// ActiveJobsResponse is the response body for GET /api/v1/system/active-jobs.
// Each slice contains opaque job IDs or labels for the named operation class.
// Empty slices mean that class is idle and a server restart will not interrupt it.
// The autodeploy script defers a clustr-serverd restart when ANY field is non-empty.
type ActiveJobsResponse struct {
	// InitramfsBuilds is non-empty when an initramfs rebuild (POST
	// /system/initramfs/rebuild or POST /initramfs/build) is in progress.
	// Entries have the form "initramfs_<build-id>".
	InitramfsBuilds []string `json:"initramfs_builds"`
	// ImageBuilds is non-empty when one or more ISO/image factory builds are in
	// a non-terminal phase (downloading, installing, extracting, etc.).
	// Entries have the form "image_<image-id>".
	ImageBuilds []string `json:"image_builds"`
	// Reimages is non-empty when one or more node reimages are in a non-terminal
	// state (pending, triggered, in_progress).
	// Entries have the form "reimage_<request-id>".
	Reimages []string `json:"reimages"`
	// Deploys is non-empty when one or more nodes have an active deploy in
	// progress as reported via POST /api/v1/deploy/progress. This covers
	// node-initiated deploys that do not create a reimage_requests row (e.g.
	// the direct node-pull path). Terminal phases ("complete", "error") are
	// excluded. Entries have the form "deploy_<node-mac>".
	// BUG-18: previously absent; autodeploy could restart mid-deploy.
	Deploys []string `json:"deploys"`
	// OperatorSessions is non-empty when one or more image shell sessions are
	// currently open (active chroot/nspawn sessions).
	// Entries have the form "shell_<session-id>".
	OperatorSessions []string `json:"operator_sessions"`
	// PxeInFlight is non-empty when one or more nodes received a DHCP lease
	// within the last 30 seconds and may be mid-PXE-boot.
	// Entries are MAC address strings (e.g. "bc:24:11:da:58:6a").
	PxeInFlight []string `json:"pxe_in_flight"`
}
